package launch

import (
	"fmt"
	"path/filepath"
)

const (
	// DefaultClaudeGatewayURL is the anthropic-native gateway path — not the
	// router URL. Claude Code speaks the Anthropic API directly.
	DefaultClaudeGatewayURL     = DefaultGatewayAPIBaseURL + "/v3/anthropic"
	DefaultClaudeModel          = "anthropic/claude-sonnet-5"
	DefaultClaudeSmallFastModel = "anthropic/claude-haiku-4-5"

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
		Prompt:      nil, // claude's own -p passes through untouched
		Resolve:     resolveClaude,
	}
}

// resolveClaude wires claude through env vars (gateway base URL, auth token,
// models — no /v2/models fetch) plus a --mcp-config PreArg pointing at a temp
// file. Skills are linked into ~/.claude/skills for the session rather than
// fetched as a plugin.
func resolveClaude(ctx *AgentContext) (*LaunchPlan, error) {
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
	model := firstNonEmpty(ctx.Flags.Model, getenv("ANTHROPIC_MODEL"), DefaultClaudeModel)
	smallFast := firstNonEmpty(getenv("ANTHROPIC_SMALL_FAST_MODEL"), DefaultClaudeSmallFastModel)
	opus := firstNonEmpty(getenv("ANTHROPIC_DEFAULT_OPUS_MODEL"), DefaultClaudeOpusModel)
	sonnet := firstNonEmpty(getenv("ANTHROPIC_DEFAULT_SONNET_MODEL"), DefaultClaudeSonnetModel)
	haiku := firstNonEmpty(getenv("ANTHROPIC_DEFAULT_HAIKU_MODEL"), DefaultClaudeHaikuModel)

	var warnings []string
	if ShouldWarnMissingProviderPrefix(model, noopNormalize) {
		warnings = append(warnings, fmt.Sprintf(
			"model %q has no provider/ prefix; the gateway expects e.g. anthropic/claude-sonnet-4-6", model))
	}

	plan := &LaunchPlan{
		Env: map[string]string{
			"ANTHROPIC_BASE_URL":   baseURL,
			"ANTHROPIC_AUTH_TOKEN": ctx.Creds.APIKey,
			"ANTHROPIC_API_KEY":    "", // explicitly empty so claude uses the auth token
			// A nested `orq` invocation from inside the session reads this, so the
			// launch-follows-session invariant holds even one process down.
			"ORQ_API_KEY":                ctx.Creds.APIKey,
			"ORQ_SERVER":                 ctx.Creds.APIBaseURL,
			"ANTHROPIC_MODEL":            model,
			"ANTHROPIC_SMALL_FAST_MODEL": smallFast,
			// Tier aliases, so /model opus|sonnet|haiku resolves to a gateway ref.
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   opus,
			"ANTHROPIC_DEFAULT_SONNET_MODEL": sonnet,
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  haiku,
		},
		Warnings: warnings,
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
