package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// resolveOmp points PI_CODING_AGENT_DIR at a fresh temp dir so the user's
// real models.yml and mcp.json are never touched; everything else in their
// agent dir is symlinked in (overlayOmpAgentDir). apiKey uses omp's $ENV_VAR
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

	config, err := BuildPiModelsJSON(resolved.BaseURL, resolved.GatewayModels, resolved.Infos)
	if err != nil {
		return nil, err
	}
	dir, cleanup, err := writeOmpConfigDir(config)
	if err != nil {
		return nil, err
	}
	if err := overlayOmpAgentDir(ctx, dir); err != nil {
		cleanup()
		return nil, err
	}

	// The session mcp.json replaces the user's, so the entry is always
	// written (like kimi); persistedMCPConfigured is not consulted. The
	// OAuth login survives: omp keys it by server URL in agent.db, which
	// the overlay links in.
	if url := mcpURL(ctx); url != "" {
		if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(httpMCPConfigJSON(url)), 0o600); err != nil {
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

// overlayOmpAgentDir symlinks every entry of the user's omp agent dir into
// the session dir except models.yml and mcp.json, which the session owns:
// writing them through a link would rewrite the user's files. Logins
// (agent.db), settings, extensions and sessions stay the user's own, and
// omp's writes to them land in the real dir. A missing dir, or one pi owns,
// is left out — pi's files are not omp's state.
func overlayOmpAgentDir(ctx *AgentContext, sessionDir string) error {
	src := strings.TrimSpace(ctx.Getenv("PI_CODING_AGENT_DIR"))
	if src == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		src = filepath.Join(home, ".omp", "agent")
	}
	if OmpPiOwned(src) {
		return nil
	}
	entries, err := os.ReadDir(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read omp agent dir: %w", err)
	}
	for _, e := range entries {
		if name := e.Name(); name != "models.yml" && name != "mcp.json" {
			if err := os.Symlink(filepath.Join(src, name), filepath.Join(sessionDir, name)); err != nil {
				return fmt.Errorf("link %s into the omp session dir: %w", name, err)
			}
		}
	}
	return nil
}

// OmpPiOwned reports a directory that belongs to pi: it holds models.json but
// no models.yml. PI_CODING_AGENT_DIR is read by both agents, so a machine
// that points it at pi's directory must not be offered omp, nor have omp's
// files dropped into it, nor have pi's files overlaid into an omp session.
func OmpPiOwned(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "models.json")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "models.yml"))
	return errors.Is(err, os.ErrNotExist)
}
