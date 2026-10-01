package launch

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	// DefaultClaudeGatewayURL is the anthropic-native gateway path — not the
	// router URL. Claude Code speaks the Anthropic API directly.
	DefaultClaudeGatewayURL = DefaultGatewayAPIBaseURL + "/v3/anthropic"

	// Claude Code resolves /model opus|sonnet|haiku through these three. Left
	// unset it sends the bare alias, which is not a model id the gateway can
	// resolve, so a session could not switch tiers at all. No
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
		HelpModel:   "Model to start with (with gateway routing, a gateway ref: provider/model_id)",
		Prompt:      nil, // claude's own -p passes through untouched
		Resolve:     resolveClaude,
	}
}

// resolveClaude routes claude through the gateway and adds the orq MCP server,
// skills and session tracing. --no-gateway, --no-mcp, --no-skills and --no-otel
// disable those capabilities. MCP is a --mcp-config PreArg pointing at a temp file; skills
// are linked into ~/.claude/skills for the session rather than fetched as a
// plugin, unless ORQ_SKILLS_URL pins a bundle, which is loaded with
// --plugin-url instead.
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

	fail := func(err error) (*LaunchPlan, error) {
		if plan.Cleanup != nil {
			plan.Cleanup()
		}
		return nil, err
	}

	routed := false
	if ctx.Flags.Router {
		routed = routeThroughGateway(ctx, plan)
	} else {
		if ctx.Flags.Model != "" {
			plan.Env["ANTHROPIC_MODEL"] = ctx.Flags.Model
		}
		// An inherited ANTHROPIC_MODEL left over from a routed setup is the
		// same mistake as passing one, so both get the warning.
		if model := firstNonEmpty(ctx.Flags.Model, ctx.Getenv("ANTHROPIC_MODEL")); model != "" && !ShouldWarnMissingProviderPrefix(model, noopNormalize) {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"model %q is a gateway ref, but with --no-gateway claude talks to Anthropic directly, which expects e.g. claude-sonnet-5-5", model))
		}
		if ctx.Flags.BaseURL != "" {
			plan.Warnings = append(plan.Warnings, "--base-url only applies with gateway routing; ignoring it")
		}
		// Without gateway routing the launcher sets none of these, so whatever the
		// shell exports reaches claude. Saying "your own login" while an
		// inherited ANTHROPIC_BASE_URL bills someone else is the surprise this
		// ticket is about, so name it.
		var inherited []string
		for _, k := range loginOverrides {
			if ctx.Getenv(k) != "" {
				inherited = append(inherited, k)
			}
		}
		if len(inherited) > 0 {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"%s set in your shell, so claude uses it rather than your own login; unset it to use your own login", strings.Join(inherited, ", ")))
		}
	}
	if ctx.Flags.Trace {
		if err := wireTrace(ctx, plan); err != nil {
			return fail(fmt.Errorf("session tracing: %w", err))
		}
		if routed {
			plan.Warnings = append(plan.Warnings,
				"gateway routing records each model call twice, once in the AI Router and once in the session trace, so summed costs across both double-count; pass --no-otel to keep one copy")
		}
	} else {
		declineTrace(ctx, plan)
	}

	if url := mcpURL(ctx); url != "" && !persistedMCPConfigured("claude") {
		path, cleanup, err := writeClaudeMCPConfig(url)
		if err != nil {
			return fail(err)
		}
		plan.PreArgs = append(plan.PreArgs, "--mcp-config", path)
		plan.TempDirs = append(plan.TempDirs, TempDir{HostPath: filepath.Dir(path)})
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

// loginOverrides are the variables that take claude off the user's own login.
var loginOverrides = []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"}

func noopNormalize(model string) string { return model }

// claudeProviderSwitches are the env switches that move claude off the
// Anthropic API and onto a provider with its own endpoint variable. Read from
// the claude 2.1.281 bundle, which keeps them in one list next to
// ANTHROPIC_BEDROCK_BASE_URL and its siblings.
var claudeProviderSwitches = []string{
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS",
	"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD",
	"CLAUDE_CODE_USE_MANTLE",
	"CLAUDE_CODE_USE_GATEWAY",
}

// routeThroughGateway points claude at the anthropic-native gateway path. Only
// the model the user asked for is exported: an unset ANTHROPIC_MODEL leaves
// claude's own default and any /model choice alone, and the tier variables
// below turn its default aliases into gateway refs. It returns false when a
// provider switch in the shell overrides the route.
func routeThroughGateway(ctx *AgentContext, plan *LaunchPlan) bool {
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
	if ctx.Flags.Model != "" {
		plan.Env["ANTHROPIC_MODEL"] = ctx.Flags.Model
	}
	plan.Env["ANTHROPIC_BASE_URL"] = baseURL
	plan.Env["ANTHROPIC_AUTH_TOKEN"] = ctx.Creds.APIKey
	plan.Env["ANTHROPIC_API_KEY"] = "" // explicitly empty so claude uses the auth token
	// Each of these picks a provider whose own base URL claude reads instead of
	// ANTHROPIC_BASE_URL. One left in the shell is the user's own arrangement,
	// so it stays in force, but gateway routing then routes nothing and says so.
	var providerSwitches []string
	for _, k := range claudeProviderSwitches {
		if getenv(k) != "" {
			providerSwitches = append(providerSwitches, k)
		}
	}
	if len(providerSwitches) > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%s set in your shell sends claude to that provider, so gateway routing does nothing and nothing bills to orq; unset it to route through the orq.ai AI Router", strings.Join(providerSwitches, ", ")))
	} else {
		billed := firstNonEmpty(ctx.Creds.Workspace, "the workspace this API key belongs to")
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"gateway routing sends this session's model calls through the orq.ai AI Router; usage bills to %s, not your Claude subscription", billed))
	}
	// Tier aliases, so /model opus|sonnet|haiku resolves to a gateway ref.
	// ANTHROPIC_SMALL_FAST_MODEL is deliberately not among them: current Claude
	// Code reads the haiku tier below for its background calls.
	plan.Env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = firstNonEmpty(getenv("ANTHROPIC_DEFAULT_OPUS_MODEL"), DefaultClaudeOpusModel)
	plan.Env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = firstNonEmpty(getenv("ANTHROPIC_DEFAULT_SONNET_MODEL"), DefaultClaudeSonnetModel)
	plan.Env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = firstNonEmpty(getenv("ANTHROPIC_DEFAULT_HAIKU_MODEL"), DefaultClaudeHaikuModel)
	return len(providerSwitches) == 0
}
