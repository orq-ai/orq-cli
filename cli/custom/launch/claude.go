package launch

import (
	"fmt"
	"path/filepath"
)

const (
	// DefaultClaudeGatewayURL is the anthropic-native gateway path — not the
	// router URL. Claude Code speaks the Anthropic API directly.
	DefaultClaudeGatewayURL = DefaultGatewayAPIBaseURL + "/v3/anthropic"

	// Claude Code resolves /model opus|sonnet|haiku through these three. Left
	// unset it sends the bare alias, which the gateway rejects for having no
	// provider/ prefix, so a session could not switch tiers at all. No
	// claude-haiku-5 exists yet; 4-5 is the current haiku.
	DefaultClaudeOpusModel   = "anthropic/claude-opus-5"
	DefaultClaudeSonnetModel = "anthropic/claude-sonnet-5"
	DefaultClaudeHaikuModel  = "anthropic/claude-haiku-4-5"
)

func claudeAgent() AgentDef {
	return AgentDef{
		Name:        "claude",
		Binary:      "claude",
		Label:       "Claude Code",
		InstallHint: "npm install -g @anthropic-ai/claude-code",
		AllowModels: false,
		Traceable:   true,
		HelpRoute:   helpRoute("Anthropic", DefaultClaudeGatewayURL),
		HelpModel:   "Model to start with (with --router, a gateway ref: provider/model_id)",
		Prompt:      nil, // claude's own -p passes through untouched
		Resolve:     resolveClaude,
	}
}

// resolveClaude leaves claude on the user's own login by default and adds only
// the orq MCP server and skills. --router moves model traffic onto the gateway
// (and so onto workspace billing); --trace captures the session. Both are
// opt-in because either changes what the user is billed for or what leaves
// their machine. MCP is a --mcp-config PreArg pointing at a temp file; skills
// are linked into ~/.claude/skills for the session rather than fetched as a
// plugin.
func resolveClaude(ctx *AgentContext) (*LaunchPlan, error) {
	plan := &LaunchPlan{
		Env: map[string]string{
			// A nested `orq` invocation from inside the session reads these, so the
			// launch-follows-session invariant holds even one process down. The
			// orq-trace plugin resolves its key from ORQ_API_KEY too.
			"ORQ_API_KEY": ctx.Creds.APIKey,
			"ORQ_SERVER":  ctx.Creds.APIBaseURL,
		},
	}

	if ctx.Flags.Router {
		routeThroughGateway(ctx, plan)
	} else if ctx.Flags.Model != "" {
		plan.Env["ANTHROPIC_MODEL"] = ctx.Flags.Model
	}
	if ctx.Flags.Trace {
		wireTrace(ctx, plan)
	}

	if url := mcpURL(ctx); url != "" && !persistedMCPConfigured("claude") {
		path, cleanup, err := writeClaudeMCPConfig(url)
		if err != nil {
			return nil, err
		}
		plan.PreArgs = []string{"--mcp-config", path}
		plan.TempDirs = []TempDir{{HostPath: filepath.Dir(path)}}
		plan.AddCleanup(cleanup)
	}
	if url := skillsPluginURL(ctx); url != "" {
		// Explicit ORQ_SKILLS_URL only: session-only plugin load, where claude
		// fetches the zip itself and nothing is installed into the user's
		// ~/.claude config.
		plan.PreArgs = append(plan.PreArgs, "--plugin-url", url)
	}
	maybeInstallSessionSkills(ctx, plan, "claude")
	return plan, nil
}

func noopNormalize(model string) string { return model }

// routeThroughGateway points claude at the anthropic-native gateway path. Only
// the model the user asked for is exported: an unset ANTHROPIC_MODEL leaves
// claude's own default and any /model choice alone, and the tier variables
// below are what turn its default aliases into gateway refs.
func routeThroughGateway(ctx *AgentContext, plan *LaunchPlan) {
	getenv := ctx.Getenv

	// Deliberately NOT ORQ_GATEWAY_URL: that is the OpenAI-shaped router URL
	// shared by the other agents, and claude speaks the Anthropic-native API —
	// inheriting it would misroute every request. Claude gets its own key.
	baseURL := firstNonEmpty(
		ctx.Flags.BaseURL,
		getenv("ORQ_ANTHROPIC_BASE_URL"),
		deriveFromAPIBase(ctx.Creds.APIBaseURL, "/v3/anthropic"),
		DefaultClaudeGatewayURL,
	)
	if model := firstNonEmpty(ctx.Flags.Model, getenv("ANTHROPIC_MODEL")); model != "" {
		if ctx.Flags.Model != "" {
			plan.Env["ANTHROPIC_MODEL"] = model
		}
		if ShouldWarnMissingProviderPrefix(model, noopNormalize) {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"model %q has no provider/ prefix; the gateway expects e.g. anthropic/claude-sonnet-4-6", model))
		}
	}

	plan.Env["ANTHROPIC_BASE_URL"] = baseURL
	plan.Env["ANTHROPIC_AUTH_TOKEN"] = ctx.Creds.APIKey
	plan.Env["ANTHROPIC_API_KEY"] = "" // explicitly empty so claude uses the auth token
	// Tier aliases, so /model opus|sonnet|haiku resolves to a gateway ref.
	plan.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = firstNonEmpty(getenv("ANTHROPIC_DEFAULT_OPUS_MODEL"), DefaultClaudeOpusModel)
	plan.Env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = firstNonEmpty(getenv("ANTHROPIC_DEFAULT_SONNET_MODEL"), DefaultClaudeSonnetModel)
	plan.Env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = firstNonEmpty(getenv("ANTHROPIC_DEFAULT_HAIKU_MODEL"), DefaultClaudeHaikuModel)

	billed := firstNonEmpty(ctx.Creds.Workspace, "the workspace this API key belongs to")
	plan.Warnings = append(plan.Warnings, fmt.Sprintf(
		"--router sends this session's model calls through the orq.ai AI Router; usage bills to %s, not your Claude subscription", billed))
}
