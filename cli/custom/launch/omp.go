package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	DefaultOmpModel = "openai/gpt-5.6-terra"

	// omp (oh-my-pi) takes custom providers via a models.yml in its agent
	// dir; the schema matches pi's models.json, and JSON is valid YAML.
	OmpProvider = "orq"
)

func ompAgent() AgentDef {
	return AgentDef{
		Name:          "omp",
		Binary:        "omp",
		Label:         "omp",
		InstallHint:   "npm install -g @oh-my-pi/pi-coding-agent (https://omp.sh)",
		FetchesModels: true,
		AllowModels:   true,
		Prompt: &PromptMapping{
			Flags:  []string{"-p", "--prompt"},
			ToArgs: func(v string) []string { return []string{"-p", v} },
		},
		Resolve: resolveOmp,
	}
}

// resolveOmp mirrors resolvePi: a models.yml declaring the orq gateway as a
// provider, written into a fresh temp dir used as PI_CODING_AGENT_DIR so the
// user's real ~/.omp/agent is never touched. apiKey uses omp's $ENV_VAR
// interpolation — the key itself stays out of the file.
func resolveOmp(ctx *AgentContext) (*LaunchPlan, error) {
	resolved, err := ResolveGatewayConfig(ResolveInput{
		AuthToken:         ctx.Creds.APIKey,
		APIBaseURL:        ctx.Creds.APIBaseURL,
		Getenv:            ctx.Getenv,
		Flags:             ctx.Flags,
		Fetch:             ctx.Fetch,
		Normalize:         noopNormalize,
		DefaultModel:      DefaultOmpModel,
		BaseURLEnvKey:     "ORQ_OMP_BASE_URL",
		ModelEnvKey:       "", // omp reads no model env vars
		ModelsEnvKey:      "",
		CollectModelInfos: true,
	})
	if err != nil {
		return nil, err
	}

	// Same schema as pi's models.json; written verbatim (JSON is valid YAML).
	config, err := BuildPiModelsJSON(resolved.BaseURL, resolved.GatewayModels, resolved.Infos)
	if err != nil {
		return nil, err
	}
	dir, cleanup, err := writeOmpConfigDir(config)
	if err != nil {
		return nil, err
	}

	// The temp dir shadows the user's persisted mcp.json, so the session
	// entry is always written (like kimi); persistedMCPConfigured is not
	// consulted.
	if url := mcpURL(ctx); url != "" {
		if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(ompSessionMCPConfig(url)), 0o600); err != nil {
			cleanup()
			return nil, err
		}
	}

	// Skills do not go in the temp dir: omp is a sharedReader reading
	// .agents/skills, and PI_CODING_AGENT_DIR only redirects its config dir.
	plan := &LaunchPlan{
		Env: map[string]string{
			"ORQ_API_KEY":         ctx.Creds.APIKey,
			"ORQ_SERVER":          ctx.Creds.APIBaseURL,
			"PI_CODING_AGENT_DIR": dir,
		},
		PreArgs:  []string{"--provider", OmpProvider, "--model", resolved.GatewayModel},
		TempDirs: []TempDir{{HostPath: dir}},
		Cleanup:  cleanup,
	}
	maybeInstallSessionSkills(ctx, plan, "omp")
	appendModelWarnings(plan, resolved, noopNormalize, "openai/gpt-5-mini")
	appendCapWarning(plan, resolved)
	return plan, nil
}

func writeOmpConfigDir(modelsJSON string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "orq-omp-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, "models.yml"), []byte(modelsJSON), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// ompSessionMCPConfig is the mcp.json written into the session agent dir. omp
// authenticates this remote through its own OAuth flow; no headers.
func ompSessionMCPConfig(url string) string {
	encoded, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			MCPServerName: map[string]any{
				"type": "http",
				"url":  url,
			},
		},
	})
	return string(encoded)
}
