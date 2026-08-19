package launch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultKimiModel = "anthropic/claude-sonnet-4-6"

	// Kimi Code (@moonshot-ai/kimi-code) is a standalone CLI (not an OpenCode
	// fork). Provider type "openai" = /chat/completions, "openai_responses" =
	// /responses. Kimi 0.34 supports neither ${VAR} interpolation nor an
	// env-key field for provider credentials, so the literal api_key goes into
	// config.toml — acceptable only because the file is 0600 in a temp dir
	// that is deleted when the session ends.
	KimiChatProvider      = "orq"
	KimiResponsesProvider = "orq-responses"

	kimiChatType      = "openai"
	kimiResponsesType = "openai_responses"
)

var kimiNormalize = MakeNormalizeModel([]string{KimiResponsesProvider, KimiChatProvider})

func kimiAgent() AgentDef {
	return AgentDef{
		Name:          "kimi",
		Binary:        "kimi",
		Label:         "Kimi Code",
		InstallHint:   "npm install -g @moonshot-ai/kimi-code (or curl -fsSL https://code.kimi.com/kimi-code/install.sh | bash)",
		NpmPackage:    "@moonshot-ai/kimi-code",
		FetchesModels: true,
		AllowModels:   true,
		Prompt: &PromptMapping{
			Flags:  []string{"-p", "--prompt"},
			ToArgs: func(v string) []string { return []string{"--prompt", v} },
		},
		Resolve: resolveKimi,
	}
}

// resolveKimi writes a config.toml with the router base URL and per-model
// context/output caps into a fresh temp dir used as KIMI_CODE_HOME (kimi has
// no per-file config override), so the user's real ~/.kimi-code is never
// touched.
func resolveKimi(ctx *AgentContext) (*LaunchPlan, error) {
	resolved, err := ResolveGatewayConfig(ResolveInput{
		AuthToken:         ctx.Creds.APIKey,
		APIBaseURL:        ctx.Creds.APIBaseURL,
		Getenv:            ctx.Getenv,
		Flags:             ctx.Flags,
		Fetch:             ctx.Fetch,
		Normalize:         kimiNormalize,
		DefaultModel:      DefaultKimiModel,
		BaseURLEnvKey:     "ORQ_KIMI_BASE_URL",
		ModelEnvKey:       "KIMI_MODEL",
		ModelsEnvKey:      "KIMI_MODELS",
		CollectModelInfos: true,
	})
	if err != nil {
		return nil, err
	}

	toml := BuildKimiConfigTOML(resolved.BaseURL, ctx.Creds.APIKey, resolved.GatewayModel, resolved.GatewayModels, resolved.Infos)
	home, cleanup, err := writeKimiConfigHome(toml)
	if err != nil {
		return nil, err
	}
	if url := mcpURL(ctx); url != "" {
		if err := os.WriteFile(filepath.Join(home, "mcp.json"), []byte(kimiMCPConfig(url)), 0o600); err != nil {
			cleanup()
			return nil, err
		}
	}

	plan := &LaunchPlan{
		Env: map[string]string{
			// The provider credential lives in config.toml (kimi has no env
			// fallback); this env var only feeds mcp.json's bearerTokenEnvVar.
			"ORQ_API_KEY":    ctx.Creds.APIKey,
			"KIMI_CODE_HOME": home,
		},
		TempDirs: []TempDir{{HostPath: home}},
		Cleanup:  cleanup,
	}
	appendModelWarnings(plan, resolved, kimiNormalize, "anthropic/claude-sonnet-4-6")
	appendCapWarning(plan, resolved)
	return plan, nil
}

// TOMLString encodes a TOML basic string. JSON string encoding is a valid
// TOML basic string (quotes, backslashes, and control chars all escaped) —
// raw control characters from a hostile model id would otherwise make an
// agent reject the whole config file. Every TOML writer in both commands
// must use it; hand-rolled quoting is how a hostile id breaks a config open.
func TOMLString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// BuildKimiConfigTOML serializes kimi's config.toml. The literal api_key is
// unavoidable: kimi resolves provider credentials from the file only (no env
// fallback or interpolation), so callers must keep the file private and
// short-lived.
//
// An empty gatewayModel omits default_model. Launch always names one — it owns
// a throwaway config dir — but setup merges into the user's real file, where
// kimi stores the model they picked in the UI, and that choice is not ours to
// replace.
func BuildKimiConfigTOML(baseURL, apiKey, gatewayModel string, gatewayModels []string, infos []ModelInfo) string {
	limits := make(map[string]ModelInfo, len(infos))
	for _, info := range infos {
		limits[info.ID] = info
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

	var b strings.Builder
	if gatewayModel != "" {
		fmt.Fprintf(&b, "default_model = %s\n\n", TOMLString(gatewayModel))
	}

	provider := func(name, typ string) {
		fmt.Fprintf(&b, "[providers.%s]\n", name)
		fmt.Fprintf(&b, "type = %s\n", TOMLString(typ))
		fmt.Fprintf(&b, "base_url = %s\n", TOMLString(baseURL))
		fmt.Fprintf(&b, "api_key = %s\n\n", TOMLString(apiKey))
	}
	if len(chatModels) > 0 {
		provider(KimiChatProvider, kimiChatType)
	}
	if len(responsesModels) > 0 {
		provider(KimiResponsesProvider, kimiResponsesType)
	}

	model := func(id, providerName string) {
		contextSize, outputSize := fallbackContextSize, fallbackOutputSize
		if limit, ok := limits[id]; ok {
			if limit.ContextWindow > 0 {
				contextSize = limit.ContextWindow
			}
			if limit.MaxOutputTokens > 0 {
				outputSize = limit.MaxOutputTokens
			}
		}
		fmt.Fprintf(&b, "[models.%s]\n", TOMLString(id))
		fmt.Fprintf(&b, "provider = %s\n", TOMLString(providerName))
		fmt.Fprintf(&b, "model = %s\n", TOMLString(id))
		fmt.Fprintf(&b, "max_context_size = %d\n", contextSize)
		// Kimi sends max_tokens = max_output_size on the chat path; must be <=
		// the model's real output cap or the upstream rejects the request.
		fmt.Fprintf(&b, "max_output_size = %d\n\n", outputSize)
	}
	for _, id := range chatModels {
		model(id, KimiChatProvider)
	}
	for _, id := range responsesModels {
		model(id, KimiResponsesProvider)
	}

	return strings.TrimSuffix(b.String(), "\n")
}

func writeKimiConfigHome(toml string) (home string, cleanup func(), err error) {
	home, err = os.MkdirTemp("", "orq-kimi-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(home) }
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(toml), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return home, cleanup, nil
}
