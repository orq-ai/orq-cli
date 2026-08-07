package launch

import (
	"fmt"
	"path/filepath"
)

const (
	// DefaultClaudeGatewayURL is the anthropic-native gateway path — not the
	// router URL. Claude Code speaks the Anthropic API directly.
	DefaultClaudeGatewayURL     = "https://my.orq.ai/v3/anthropic"
	DefaultClaudeModel          = "anthropic/claude-sonnet-4-6"
	DefaultClaudeSmallFastModel = "anthropic/claude-haiku-4-5"
)

func claudeAgent() AgentDef {
	return AgentDef{
		Name:        "claude",
		Binary:      "claude",
		Label:       "Claude Code",
		InstallHint: "npm install -g @anthropic-ai/claude-code",
		NpmPackage:  "@anthropic-ai/claude-code",
		AllowModels: false,
		Prompt:      nil, // claude's own -p passes through untouched
		Resolve:     resolveClaude,
	}
}

// resolveClaude wires claude through env vars (gateway base URL, auth token,
// models — no /v2/models fetch) plus two PreArgs: --mcp-config pointing at a
// temp file and --plugin-url for the pinned skills plugin.
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

	var warnings []string
	if ShouldWarnMissingProviderPrefix(model, noopNormalize) {
		warnings = append(warnings, fmt.Sprintf(
			"model %q has no provider/ prefix; the gateway expects e.g. anthropic/claude-sonnet-4-6", model))
	}

	plan := &LaunchPlan{
		Env: map[string]string{
			"ANTHROPIC_BASE_URL":         baseURL,
			"ANTHROPIC_AUTH_TOKEN":       ctx.Creds.APIKey,
			"ANTHROPIC_API_KEY":          "", // explicitly empty so claude uses the auth token
			"ANTHROPIC_MODEL":            model,
			"ANTHROPIC_SMALL_FAST_MODEL": smallFast,
		},
		Warnings: warnings,
	}

	if url := mcpURL(ctx); url != "" {
		path, cleanup, err := writeClaudeMCPConfig(url)
		if err != nil {
			return nil, err
		}
		// claude expands ${ORQ_API_KEY} from env when loading the config file.
		plan.Env["ORQ_API_KEY"] = ctx.Creds.APIKey
		plan.PreArgs = []string{"--mcp-config", path}
		plan.TempDirs = []TempDir{{HostPath: filepath.Dir(path)}}
		plan.Cleanup = cleanup
	}
	if url := skillsPluginURL(ctx); url != "" {
		// Session-only plugin load: claude fetches the zip itself, nothing is
		// installed into the user's ~/.claude config.
		plan.PreArgs = append(plan.PreArgs, "--plugin-url", url)
	}
	return plan, nil
}

func noopNormalize(model string) string { return model }
