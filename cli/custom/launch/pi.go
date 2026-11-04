package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	DefaultPiModel = "openai/gpt-5.6-terra"

	// Pi (github.com/badlogic/pi-mono) takes custom providers via a
	// models.json in its agent dir; "openai-completions" = /chat/completions.
	PiProvider = "orq"
)

func piAgent() AgentDef {
	return AgentDef{
		Name:          "pi",
		Binary:        "pi",
		Label:         "Pi Coding Agent",
		InstallHint:   "npm install -g @earendil-works/pi-coding-agent (https://github.com/badlogic/pi-mono)",
		FetchesModels: true,
		AllowModels:   true,
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
//
// No MCP wiring: pi has no built-in MCP support by design (extensions only,
// e.g. third-party pi-mcp-adapter); it is the one agent without it. omp, which
// shares pi's lineage, does take MCP (see extendOmpPlan).
func resolvePi(ctx *AgentContext) (*LaunchPlan, error) {
	return resolvePiLike(ctx, piLikeSpec{
		agent:         "pi",
		tempPrefix:    "orq-pi-",
		configFile:    "models.json",
		defaultModel:  DefaultPiModel,
		baseURLEnvKey: "ORQ_PI_BASE_URL",
		modelEnvKey:   "PI_MODEL",
		modelsEnvKey:  "PI_MODELS",
		apiKeyRef:     "$ORQ_API_KEY",
	})
}

// piLikeSpec is what differs between the agents launched through pi's
// custom-provider mechanism.
type piLikeSpec struct {
	agent         string
	tempPrefix    string
	configFile    string
	defaultModel  string
	baseURLEnvKey string
	modelEnvKey   string
	modelsEnvKey  string
	apiKeyRef     string

	// extendPlan, if set, changes the session dir and plan after configFile is
	// written with the generated provider config. If it fails, the plan is
	// cleaned up. Changes it makes outside the session dir stay.
	extendPlan func(plan *LaunchPlan, dir, generated string) error
}

// resolvePiLike writes the generated provider config to a fresh temp dir used
// as PI_CODING_AGENT_DIR and returns the plan that launches the agent with it.
func resolvePiLike(ctx *AgentContext, spec piLikeSpec) (*LaunchPlan, error) {
	resolved, err := ResolveGatewayConfig(ResolveInput{
		AuthToken:         ctx.Creds.APIKey,
		APIBaseURL:        ctx.Creds.APIBaseURL,
		Getenv:            ctx.Getenv,
		Flags:             ctx.Flags,
		Fetch:             ctx.Fetch,
		Normalize:         noopNormalize,
		DefaultModel:      spec.defaultModel,
		BaseURLEnvKey:     spec.baseURLEnvKey,
		ModelEnvKey:       spec.modelEnvKey,
		ModelsEnvKey:      spec.modelsEnvKey,
		CollectModelInfos: true,
	})
	if err != nil {
		return nil, err
	}

	generated, err := BuildPiModelsJSON(resolved.BaseURL, spec.apiKeyRef, resolved.GatewayModels, resolved.Infos)
	if err != nil {
		return nil, err
	}
	dir, cleanup, err := writePiLikeConfigDir(spec.tempPrefix, spec.configFile, generated)
	if err != nil {
		return nil, err
	}

	// Skills do not go in here: pi and omp are sharedReaders that read
	// .agents/skills (cli/custom/skills/targets.go), and PI_CODING_AGENT_DIR
	// only redirects the agent's own config dir. Writing into it would be a
	// silent no-op, so they take the refcounted session install below, which
	// links into the current directory's set or, from $HOME, the global set.
	plan := &LaunchPlan{
		Env: map[string]string{
			"ORQ_API_KEY":         ctx.Creds.APIKey,
			"ORQ_SERVER":          ctx.Creds.APIBaseURL,
			"PI_CODING_AGENT_DIR": dir,
		},
		PreArgs:  []string{"--provider", PiProvider, "--model", resolved.GatewayModel},
		TempDirs: []TempDir{{HostPath: dir}},
		Cleanup:  cleanup,
	}
	if spec.extendPlan != nil {
		if err := spec.extendPlan(plan, dir, generated); err != nil {
			plan.Cleanup()
			return nil, err
		}
	}
	maybeInstallSessionSkills(ctx, plan, spec.agent)
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
// per model (mirrors the kimi/opencode split). apiKey uses the caller-supplied
// environment-variable reference, not the key itself.
func BuildPiModelsJSON(baseURL, apiKeyRef string, gatewayModels []string, infos []ModelInfo) (string, error) {
	limits := make(map[string]ModelInfo, len(infos))
	for _, info := range infos {
		limits[info.ID] = info
	}

	isResponses := ResponsesModelSet(infos)
	var models []piModel
	for _, id := range gatewayModels {
		var api string
		if isResponses(id) {
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

	encoded, err := json.MarshalIndent(map[string]any{
		"providers": map[string]piProviderConfig{
			PiProvider: {
				BaseURL: baseURL,
				APIKey:  apiKeyRef,
				API:     "openai-completions",
				Models:  models,
			},
		},
	}, "", "  ")
	return string(encoded), err
}

func writePiLikeConfigDir(tempPrefix, configFile, content string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", tempPrefix)
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, configFile), []byte(content), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}
