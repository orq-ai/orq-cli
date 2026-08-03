package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	DefaultPiModel = "openai/gpt-5-mini"

	// Pi (github.com/badlogic/pi-mono) takes custom providers via a
	// models.json in its agent dir; "openai-completions" = /chat/completions.
	PiProvider = "orq"
)

func piAgent() AgentDef {
	return AgentDef{
		Name:        "pi",
		Binary:      "pi",
		Label:       "Pi Coding Agent",
		InstallHint: "npm install -g @earendil-works/pi-coding-agent (https://github.com/badlogic/pi-mono)",
		NpmPackage:  "@earendil-works/pi-coding-agent",
		AllowModels: true,
		Prompt: &PromptMapping{
			Flags:  []string{"-p", "--prompt"},
			ToArgs: func(v string) []string { return []string{"-p", v} },
		},
		Resolve: resolvePi,
	}
}

// resolvePi wires pi's custom-provider mechanism: a models.json declaring the
// orq gateway as an openai-completions provider, written into a fresh temp
// dir used as PI_CODING_AGENT_DIR so the user's real ~/.pi/agent is never
// touched. The apiKey field uses pi's $ENV_VAR interpolation — the key itself
// stays out of the file.
func resolvePi(ctx *AgentContext) (*LaunchPlan, error) {
	resolved, err := ResolveGatewayConfig(ResolveInput{
		AuthToken:         ctx.Creds.APIKey,
		APIBaseURL:        ctx.Creds.APIBaseURL,
		Getenv:            ctx.Getenv,
		Flags:             ctx.Flags,
		Fetch:             ctx.Fetch,
		Normalize:         noopNormalize,
		DefaultModel:      DefaultPiModel,
		BaseURLEnvKey:     "ORQ_PI_BASE_URL",
		ModelEnvKey:       "PI_MODEL",
		ModelsEnvKey:      "PI_MODELS",
		CollectModelInfos: true,
	})
	if err != nil {
		return nil, err
	}

	config, err := BuildPiModelsJSON(resolved.BaseURL, resolved.GatewayModels, resolved.Infos)
	if err != nil {
		return nil, err
	}
	dir, cleanup, err := writePiConfigDir(config)
	if err != nil {
		return nil, err
	}

	// No MCP wiring: pi has no built-in MCP support by design (extensions
	// only, e.g. third-party pi-mcp-adapter). Reuse mcpURL() here when pi
	// grows a native config surface.
	plan := &LaunchPlan{
		Env: map[string]string{
			"ORQ_API_KEY":         ctx.Creds.APIKey,
			"PI_CODING_AGENT_DIR": dir,
		},
		PreArgs:  []string{"--provider", PiProvider, "--model", resolved.GatewayModel},
		TempDirs: []TempDir{{HostPath: dir}},
		Cleanup:  cleanup,
	}
	appendModelWarnings(plan, resolved, noopNormalize, "openai/gpt-5-mini")
	appendCapWarning(plan, resolved)
	return plan, nil
}

type piCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type piModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Reasoning     bool     `json:"reasoning"`
	Input         []string `json:"input"`
	Cost          piCost   `json:"cost"`
	ContextWindow int      `json:"contextWindow"`
	MaxTokens     int      `json:"maxTokens"`
	API           string   `json:"api,omitempty"` // per-model override of the provider api
}

type piProviderConfig struct {
	BaseURL string    `json:"baseUrl"`
	APIKey  string    `json:"apiKey"`
	API     string    `json:"api"`
	Models  []piModel `json:"models"`
}

// BuildPiModelsJSON serializes pi's models.json: one provider whose default
// api is chat completions, with openai/* models flipped to the Responses API
// per model (mirrors the kimi/opencode split). apiKey holds pi's env
// interpolation syntax, not the key.
func BuildPiModelsJSON(baseURL string, gatewayModels []string, infos []ModelInfo) (string, error) {
	limits := make(map[string]ModelInfo, len(infos))
	for _, info := range infos {
		limits[info.ID] = info
	}

	var models []piModel
	for _, id := range gatewayModels {
		var api string
		if IsResponsesModel(id) {
			api = "openai-responses"
		}
		contextSize, outputSize := fallbackContextSize, fallbackOutputSize
		if limit, ok := limits[id]; ok {
			if limit.ContextWindow > 0 {
				contextSize = limit.ContextWindow
			}
			if limit.MaxOutputTokens > 0 {
				outputSize = limit.MaxOutputTokens
			}
		}
		models = append(models, piModel{
			ID:            id,
			Name:          id,
			Input:         []string{"text"},
			ContextWindow: contextSize,
			MaxTokens:     outputSize,
			API:           api,
		})
	}

	// Schema requires a top-level "providers" wrapper (the docs' bare example
	// is rejected by pi's validator).
	encoded, err := json.MarshalIndent(map[string]any{
		"providers": map[string]piProviderConfig{
			PiProvider: {
				BaseURL: baseURL,
				APIKey:  "$ORQ_API_KEY",
				API:     "openai-completions",
				Models:  models,
			},
		},
	}, "", "  ")
	return string(encoded), err
}

func writePiConfigDir(modelsJSON string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "orq-pi-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(modelsJSON), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}
