package launch

import "encoding/json"

const (
	DefaultCopilotModel = "openai/gpt-5.6-terra"

	// copilotProviderType is the only value COPILOT_PROVIDER_TYPE accepts for a
	// BYOK provider that speaks the OpenAI wire format, per `copilot help
	// environment`.
	copilotProviderType = "openai"
)

func copilotAgent() AgentDef {
	return AgentDef{
		Name:          "copilot",
		Binary:        "copilot",
		Label:         "GitHub Copilot CLI",
		InstallHint:   "npm install -g @github/copilot",
		FetchesModels: true,
		// Copilot's BYOK surface is one active COPILOT_MODEL per session, not a
		// picker list like opencode/pi's models.json — there is nothing for a
		// second, third, ... explicit model to mean here, so --models is not
		// offered (mirrors codex, which has the same one-model-per-session shape).
		AllowModels: false,
		Prompt: &PromptMapping{
			Flags:  []string{"-p", "--prompt"},
			ToArgs: func(v string) []string { return []string{"-p", v} },
		},
		Resolve: resolveCopilot,
	}
}

// resolveCopilot wires GitHub Copilot CLI through its BYOK env vars
// (COPILOT_PROVIDER_*), confirmed against the installed CLI's own `copilot
// help environment`. Unlike claude/gemini it needs no config directory at
// all: every provider setting is an env var Copilot reads directly, so
// there is no $HOME/config-dir redirection to manage or clean up.
func resolveCopilot(ctx *AgentContext) (*LaunchPlan, error) {
	resolved, err := ResolveGatewayConfig(ResolveInput{
		AuthToken:     ctx.Creds.APIKey,
		APIBaseURL:    ctx.Creds.APIBaseURL,
		Getenv:        ctx.Getenv,
		Flags:         ctx.Flags,
		Fetch:         ctx.Fetch,
		Normalize:     noopNormalize,
		DefaultModel:  DefaultCopilotModel,
		BaseURLEnvKey: "ORQ_COPILOT_BASE_URL",
		ModelEnvKey:   "COPILOT_MODEL",
		// One active model, so there is no explicit-models list to merge in.
		ModelsEnvKey: "",
		// DefaultBaseURL is deliberately left unset. Copilot is OpenAI-wire-shaped,
		// so it rides the same router as codex/kimi/opencode/pi, and those agents
		// omit it too. Passing DefaultGatewayBaseURL explicitly would look
		// equivalent but is not: it sits ahead of deriveFromAPIBase in
		// ResolveGatewayConfig's chain, so a non-empty value there sends every
		// on-prem model call to the public gateway instead of the customer's host.
		// Needed for the completions/responses wire_api split below, same as
		// kimi/opencode/pi.
		CollectModelInfos: true,
	})
	if err != nil {
		return nil, err
	}

	wireAPI := "completions"
	if ResponsesModelSet(resolved.Infos)(resolved.GatewayModel) {
		wireAPI = "responses"
	}

	plan := &LaunchPlan{
		Env: map[string]string{
			"ORQ_API_KEY":               ctx.Creds.APIKey,
			"ORQ_SERVER":                ctx.Creds.APIBaseURL,
			"COPILOT_PROVIDER_BASE_URL": resolved.BaseURL,
			"COPILOT_PROVIDER_API_KEY":  ctx.Creds.APIKey,
			"COPILOT_PROVIDER_TYPE":     copilotProviderType,
			"COPILOT_PROVIDER_WIRE_API": wireAPI,
			"COPILOT_MODEL":             resolved.GatewayModel,
		},
	}
	appendModelWarnings(plan, resolved, noopNormalize, "openai/gpt-5.4")

	// --additional-mcp-config wires the orq MCP server for this session only,
	// without touching the user's real Copilot config (mirrors codex's -c
	// override for the same server, claude's --mcp-config file). The payload
	// is wrapped in a top-level "mcpServers" object: a bare name-keyed map was
	// rejected live with `mcpServers: Required`.
	if url := mcpURL(ctx); url != "" && !persistedMCPConfigured("copilot") {
		encoded, err := json.Marshal(copilotMCPConfig(url))
		if err != nil {
			return nil, err
		}
		plan.PreArgs = append(plan.PreArgs, "--additional-mcp-config", string(encoded))
	}
	maybeInstallSessionSkills(ctx, plan, "copilot")
	return plan, nil
}

// copilotMCPConfig is the --additional-mcp-config payload for one session's
// orq MCP server. Copilot authenticates the remote through its own OAuth
// flow, same as claude/kimi.
func copilotMCPConfig(url string) map[string]any {
	return map[string]any{
		"mcpServers": map[string]any{
			MCPServerName: map[string]any{
				"type": "http",
				"url":  url,
			},
		},
	}
}
