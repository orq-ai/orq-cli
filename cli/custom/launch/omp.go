package launch

import (
	"encoding/json"
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

// resolveOmp points PI_CODING_AGENT_DIR at a fresh temp dir that is the
// user's own agent dir plus orq: every entry is symlinked in except
// models.yml and .mcp.json, which the session owns as copies of the user's
// with orq's provider and MCP entry merged in. The user's files are only
// read. apiKey uses omp's $ENV_VAR interpolation — the key itself stays out
// of the file.
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
	userDir := ompUserAgentDir(ctx)
	models := []byte(config)
	if userDir != "" {
		if models, err = OmpMergeProvider(filepath.Join(userDir, "models.yml"), config); err != nil {
			return nil, err
		}
	}
	dir, cleanup, err := writeOmpConfigDir(string(models))
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{"models.yml": true}

	// omp reads both mcp.json and .mcp.json from its agent dir and keeps the
	// first entry per server name, mcp.json first. So the user's mcp.json is
	// linked in untouched and orq's entry goes in the session's .mcp.json: an
	// orq-workspace entry `orq connect omp mcp` persisted wins, as it does for
	// every agent. The OAuth login is keyed by server URL in agent.db, which
	// is linked in, so it carries across launches.
	if url := mcpURL(ctx); url != "" {
		data, err := ompSessionMCP(userDir, url)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, ".mcp.json"), data, 0o600)
		}
		if err != nil {
			cleanup()
			return nil, err
		}
		owned[".mcp.json"] = true
	}
	if err := linkOmpAgentDir(userDir, dir, owned); err != nil {
		cleanup()
		return nil, err
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

func writeOmpConfigDir(models string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "orq-omp-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, "models.yml"), []byte(models), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// ompUserAgentDir is the directory omp itself would use — $PI_CODING_AGENT_DIR,
// else ~/.omp/agent — or "" when it does not exist or belongs to pi, whose
// files are not omp's state.
func ompUserAgentDir(ctx *AgentContext) string {
	dir := strings.TrimSpace(ctx.Getenv("PI_CODING_AGENT_DIR"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".omp", "agent")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() || OmpPiOwned(dir) {
		return ""
	}
	return dir
}

// ompSessionMCP is the user's .mcp.json (if any) with the orq-workspace
// entry added. A same-named entry of theirs is kept.
func ompSessionMCP(userDir, url string) ([]byte, error) {
	cfg := map[string]any{}
	if userDir != "" {
		path := filepath.Join(userDir, ".mcp.json")
		raw, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
			}
		}
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		cfg["mcpServers"] = servers
	}
	if _, ok := servers[MCPServerName]; !ok {
		servers[MCPServerName] = map[string]any{"type": "http", "url": url}
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// linkOmpAgentDir symlinks every entry of userDir into sessionDir except the
// ones the session owns: written through a link, those would rewrite the
// user's files. Logins (agent.db), settings, mcp.json, extensions, skills
// and sessions stay the user's own, and omp's writes to them land there.
func linkOmpAgentDir(userDir, sessionDir string, owned map[string]bool) error {
	if userDir == "" {
		return nil
	}
	entries, err := os.ReadDir(userDir)
	if err != nil {
		return fmt.Errorf("read omp agent dir: %w", err)
	}
	for _, e := range entries {
		if name := e.Name(); !owned[name] {
			if err := os.Symlink(filepath.Join(userDir, name), filepath.Join(sessionDir, name)); err != nil {
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
