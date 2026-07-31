package launch

import (
	"encoding/json"
	"fmt"
)

const (
	DefaultOpenCodeModel = "openai/gpt-5-mini"

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
	npmPackage   string
}

func opencodeAgent() AgentDef {
	return openCodeFamilyAgent(openCodeFamily{
		name:         "opencode",
		binary:       "opencode",
		label:        "OpenCode",
		configEnvVar: "OPENCODE_CONFIG_CONTENT",
		installHint:  "https://opencode.ai/docs",
		npmPackage:   "opencode-ai",
	})
}

func kiloAgent() AgentDef {
	return openCodeFamilyAgent(openCodeFamily{
		name:         "kilo",
		binary:       "kilo",
		label:        "Kilo CLI",
		configEnvVar: "KILO_CONFIG_CONTENT",
		installHint:  "npm install -g @kilocode/cli (https://kilo.ai/docs/cli)",
		npmPackage:   "@kilocode/cli",
	})
}

func openCodeFamilyAgent(family openCodeFamily) AgentDef {
	return AgentDef{
		Name:        family.name,
		Binary:      family.binary,
		Label:       family.label,
		InstallHint: family.installHint,
		NpmPackage:  family.npmPackage,
		AllowModels: true,
		Prompt: &PromptMapping{
			Flags:  []string{"-p", "--prompt"},
			ToArgs: func(v string) []string { return []string{"run", v} },
		},
		Resolve: func(ctx *AgentContext) (*LaunchPlan, error) {
			return resolveOpenCodeFamily(ctx, family)
		},
	}
}

// ToOpenCodeModel maps a gateway id onto the right inline provider.
func ToOpenCodeModel(model string) string {
	gatewayModel := opencodeNormalize(model)
	if IsResponsesModel(gatewayModel) {
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
	})
	if err != nil {
		return nil, err
	}

	configJSON, err := BuildOpenCodeConfigContent(resolved.BaseURL, resolved.GatewayModel, resolved.GatewayModels)
	if err != nil {
		return nil, err
	}

	plan := &LaunchPlan{
		Env: map[string]string{
			"ORQ_API_KEY":       ctx.Creds.APIKey,
			family.configEnvVar: configJSON,
		},
	}
	if resolved.ModelFetchWarning != "" {
		plan.Warnings = append(plan.Warnings, resolved.ModelFetchWarning)
	}
	if ShouldWarnMissingProviderPrefix(resolved.GatewayModel, opencodeNormalize) {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"model %q has no provider/ prefix; the gateway expects e.g. openai/gpt-5-mini", resolved.GatewayModel))
	}
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
func BuildOpenCodeConfigContent(baseURL, gatewayModel string, gatewayModels []string) (string, error) {
	options := map[string]string{"baseURL": baseURL, "apiKey": "{env:ORQ_API_KEY}"}
	toModelMap := func(models []string) map[string]map[string]any {
		out := make(map[string]map[string]any, len(models))
		for _, m := range models {
			out[m] = map[string]any{"name": m}
		}
		return out
	}

	var chatModels, responsesModels []string
	for _, m := range gatewayModels {
		if IsResponsesModel(m) {
			responsesModels = append(responsesModels, m)
		} else {
			chatModels = append(chatModels, m)
		}
	}

	provider := map[string]openCodeProvider{}
	if len(chatModels) > 0 {
		provider[OpenCodeChatProvider] = openCodeProvider{
			Npm:     "@ai-sdk/openai-compatible",
			Name:    "Orq Gateway",
			Options: options,
			Models:  toModelMap(chatModels),
		}
	}
	if len(responsesModels) > 0 {
		provider[OpenCodeResponsesProvider] = openCodeProvider{
			Npm:     "@ai-sdk/openai",
			Name:    "Orq Gateway (Responses)",
			Options: options,
			Models:  toModelMap(responsesModels),
		}
	}

	encoded, err := json.Marshal(struct {
		Schema   string                      `json:"$schema"`
		Provider map[string]openCodeProvider `json:"provider"`
		Model    string                      `json:"model"`
	}{
		Schema:   "https://opencode.ai/config.json",
		Provider: provider,
		Model:    ToOpenCodeModel(gatewayModel),
	})
	return string(encoded), err
}
