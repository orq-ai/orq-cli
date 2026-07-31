package launch

import "fmt"

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

// resolveClaude ports spawn-claude.ts: no model fetch, env-only wiring.
func resolveClaude(ctx *AgentContext) (*LaunchPlan, error) {
	getenv := ctx.Getenv

	baseURL := firstNonEmpty(ctx.Flags.BaseURL, getenv("ORQ_GATEWAY_URL"), DefaultClaudeGatewayURL)
	model := firstNonEmpty(ctx.Flags.Model, getenv("ANTHROPIC_MODEL"), DefaultClaudeModel)
	smallFast := firstNonEmpty(getenv("ANTHROPIC_SMALL_FAST_MODEL"), DefaultClaudeSmallFastModel)

	var warnings []string
	if ShouldWarnMissingProviderPrefix(model, noopNormalize) {
		warnings = append(warnings, fmt.Sprintf(
			"model %q has no provider/ prefix; the gateway expects e.g. anthropic/claude-sonnet-4-6", model))
	}

	return &LaunchPlan{
		Env: map[string]string{
			"ANTHROPIC_BASE_URL":         baseURL,
			"ANTHROPIC_AUTH_TOKEN":       ctx.Creds.APIKey,
			"ANTHROPIC_API_KEY":          "", // explicitly empty so claude uses the auth token
			"ANTHROPIC_MODEL":            model,
			"ANTHROPIC_SMALL_FAST_MODEL": smallFast,
		},
		Warnings: warnings,
	}, nil
}

func noopNormalize(model string) string { return model }
