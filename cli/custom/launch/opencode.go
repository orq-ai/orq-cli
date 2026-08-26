package launch

import "encoding/json"

const (
	DefaultOpenCodeModel = "openai/gpt-5.6-terra"

	// OpenCode loads one AI SDK package per provider (no per-model override),
	// so we expose two: openai models go through the Responses API (function
	// tools + reasoning_effort are rejected on /chat/completions for gpt-5.x),
	// the rest stay on chat completions.
	OpenCodeChatProvider      = "orq"
	OpenCodeResponsesProvider = "orq-openai"
)

var opencodeNormalize = MakeNormalizeModel([]string{OpenCodeResponsesProvider, OpenCodeChatProvider})

// openCodeFamily names the differences between OpenCode and its Kilo fork:
// identical config schema and {env:VAR} interpolation, only the binary and the
// inline-config env var differ.
type openCodeFamily struct {
	name         string
	binary       string
	label        string
	configEnvVar string
	installHint  string
}

func opencodeAgent() AgentDef {
	return openCodeFamilyAgent(openCodeFamily{
		name:         "opencode",
		binary:       "opencode",
		label:        "OpenCode",
		configEnvVar: "OPENCODE_CONFIG_CONTENT",
		installHint:  "https://opencode.ai/docs",
	})
}

func kiloAgent() AgentDef {
	return openCodeFamilyAgent(openCodeFamily{
		name:         "kilo",
		binary:       "kilo",
		label:        "Kilo CLI",
		configEnvVar: "KILO_CONFIG_CONTENT",
		installHint:  "npm install -g @kilocode/cli (https://kilo.ai/docs/cli)",
	})
}

func openCodeFamilyAgent(family openCodeFamily) AgentDef {
	return AgentDef{
		Name:          family.name,
		Binary:        family.binary,
		Label:         family.label,
		InstallHint:   family.installHint,
		FetchesModels: true,
		AllowModels:   true,
		Prompt: &PromptMapping{
			Flags:  []string{"-p", "--prompt"},
			ToArgs: func(v string) []string { return []string{"run", v} },
		},
		Resolve: func(ctx *AgentContext) (*LaunchPlan, error) {
			return resolveOpenCodeFamily(ctx, family)
		},
	}
}

// ToOpenCodeModel maps a gateway id onto the right inline provider. isResponses
// must be the same predicate used to split the model map, or the default model
// names a provider that does not list it.
func ToOpenCodeModel(isResponses func(string) bool, model string) string {
	gatewayModel := opencodeNormalize(model)
	if isResponses(gatewayModel) {
		return OpenCodeResponsesProvider + "/" + gatewayModel
	}
	return OpenCodeChatProvider + "/" + gatewayModel
}

func resolveOpenCodeFamily(ctx *AgentContext, family openCodeFamily) (*LaunchPlan, error) {
	resolved, err := ResolveGatewayConfig(ResolveInput{
		AuthToken:     ctx.Creds.APIKey,
		APIBaseURL:    ctx.Creds.APIBaseURL,
		Getenv:        ctx.Getenv,
		Flags:         ctx.Flags,
		Fetch:         ctx.Fetch,
		Normalize:     opencodeNormalize,
		DefaultModel:  DefaultOpenCodeModel,
		BaseURLEnvKey: "ORQ_OPENCODE_BASE_URL",
		ModelEnvKey:   "OPENCODE_MODEL",
		ModelsEnvKey:  "OPENCODE_MODELS",
		// Needed for the chat/responses provider split, not for caps: opencode
		// takes model names only.
		CollectModelInfos: true,
	})
	if err != nil {
		return nil, err
	}

	mcpServerURL := mcpURL(ctx)
	if mcpServerURL != "" && (family.name == "opencode" || family.name == "kilo") && persistedMCPConfigured(family.name) {
		mcpServerURL = ""
	}
	configJSON, err := BuildOpenCodeConfigContent(resolved.BaseURL, resolved.GatewayModel, resolved.GatewayModels, resolved.Infos, mcpServerURL)
	if err != nil {
		return nil, err
	}

	plan := &LaunchPlan{
		Env: map[string]string{
			"ORQ_API_KEY":       ctx.Creds.APIKey,
			family.configEnvVar: configJSON,
		},
	}
	appendModelWarnings(plan, resolved, opencodeNormalize, "openai/gpt-5-mini")
	// Both members of the family read the shared ~/.agents/skills, which is in
	// the real home and cannot be redirected by any config env var they take.
	maybeInstallSessionSkills(ctx, plan, family.name)
	return plan, nil
}

type openCodeProvider struct {
	Npm     string                    `json:"npm"`
	Name    string                    `json:"name"`
	Options map[string]string         `json:"options"`
	Models  map[string]map[string]any `json:"models"`
}

// BuildOpenCodeConfigContent serializes the inline OpenCode/Kilo config JSON.
// The api key stays out of the file via {env:ORQ_API_KEY} interpolation.
// A non-empty mcpServerURL adds the orq MCP server as a remote entry.
func BuildOpenCodeConfigContent(baseURL, gatewayModel string, gatewayModels []string, infos []ModelInfo, mcpServerURL string) (string, error) {
	options := map[string]string{"baseURL": baseURL, "apiKey": "{env:ORQ_API_KEY}"}
	toModelMap := func(models []string) map[string]map[string]any {
		out := make(map[string]map[string]any, len(models))
		for _, m := range models {
			out[m] = map[string]any{"name": m}
		}
		return out
	}

	isResponses := ResponsesModelSet(infos)
	var chatModels, responsesModels []string
	for _, m := range gatewayModels {
		if isResponses(m) {
			responsesModels = append(responsesModels, m)
		} else {
			chatModels = append(chatModels, m)
		}
	}

	provider := map[string]openCodeProvider{}
	if len(chatModels) > 0 {
		provider[OpenCodeChatProvider] = openCodeProvider{
			Npm:     "@ai-sdk/openai-compatible",
			Name:    ProviderDisplayName,
			Options: options,
			Models:  toModelMap(chatModels),
		}
	}
	if len(responsesModels) > 0 {
		provider[OpenCodeResponsesProvider] = openCodeProvider{
			Npm:     "@ai-sdk/openai",
			Name:    ProviderResponsesDisplayName,
			Options: options,
			Models:  toModelMap(responsesModels),
		}
	}

	var mcp map[string]any
	if mcpServerURL != "" {
		mcp = openCodeMCPBlock(mcpServerURL)
	}

	encoded, err := json.Marshal(struct {
		Schema   string                      `json:"$schema"`
		Provider map[string]openCodeProvider `json:"provider"`
		Model    string                      `json:"model"`
		MCP      map[string]any              `json:"mcp,omitempty"`
	}{
		Schema:   "https://opencode.ai/config.json",
		Provider: provider,
		Model:    ToOpenCodeModel(isResponses, gatewayModel),
		MCP:      mcp,
	})
	return string(encoded), err
}
