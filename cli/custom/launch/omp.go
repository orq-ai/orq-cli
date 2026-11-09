package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var errOmpSymlink = errors.New("omp user-state symlink failed")

const (
	DefaultOmpModel = "openai/gpt-5.6-terra"

	// omp (oh-my-pi) takes custom providers via a models.yml in its agent
	// dir; the schema matches pi's models.json, and JSON is valid YAML.
	// resolvePiLike passes PiProvider to --provider, so omp's key must match.
	OmpProvider = PiProvider

	ompConfigFile = "models.yml"
)

func ompAgent() AgentDef {
	return AgentDef{
		Name:          "omp",
		Binary:        "omp",
		Label:         "oh-my-pi (omp)",
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

// resolveOmp launches omp through resolvePiLike; extendOmpPlan adds the
// overlay of the user's agent dir.
func resolveOmp(ctx *AgentContext) (*LaunchPlan, error) {
	return resolvePiLike(ctx, piLikeSpec{
		agent:        "omp",
		tempPrefix:   "orq-omp-",
		configFile:   ompConfigFile,
		defaultModel: DefaultOmpModel,
		// omp reads no model env vars, so modelEnvKey and modelsEnvKey stay empty.
		baseURLEnvKey: "ORQ_OMP_BASE_URL",
		apiKeyRef:     OmpAPIKeyRef,
		extendPlan:    extendOmpPlan,
	})
}

// extendOmpPlan merges orq into a session copy of the user's models.yml, adds
// the session MCP entry, seeds persistent state in the user's dir, overlays
// that dir or falls back to an isolated config, and clears the profile
// variables.
func extendOmpPlan(ctx *AgentContext, plan *LaunchPlan, dir, generated string) error {
	userDir, err := ompUserAgentDir(ctx)
	if err != nil {
		return err
	}
	userModels := ""
	if userDir != "" {
		userModels = filepath.Join(userDir, ompConfigFile)
	}
	models, err := OmpMergeProvider(userModels, generated)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ompConfigFile), models, 0o600); err != nil {
		return err
	}
	owned := map[string]bool{ompConfigFile: true}

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
			return err
		}
		owned[".mcp.json"] = true
	}
	if userDir != "" {
		if err := prepareOmpState(userDir); err != nil {
			return err
		}
	}
	symlink := ctx.Symlink
	if symlink == nil {
		symlink = os.Symlink
	}
	if linkErr := linkOmpAgentDir(userDir, dir, owned, symlink); linkErr != nil {
		if !errors.Is(linkErr, errOmpSymlink) {
			return linkErr
		}
		if resetErr := rebuildIsolatedOmpConfig(dir, generated, mcpURL(ctx)); resetErr != nil {
			return fmt.Errorf("symlink omp user config: %w (isolated fallback: %v)", linkErr, resetErr)
		}
		plan.Warnings = append(plan.Warnings, "Symlinks are unavailable; omp is using an isolated session config. User omp config, login state, and sessions are not carried over for this launch.")
	}
	if strings.TrimSpace(ctx.Getenv("OMP_PROFILE")) != "" || strings.TrimSpace(ctx.Getenv("PI_PROFILE")) != "" {
		plan.Env["OMP_PROFILE"] = ""
		plan.Env["PI_PROFILE"] = ""
		plan.Warnings = append(plan.Warnings, "OMP_PROFILE/PI_PROFILE is set; orq launch omp uses the default profile for this session. For more flexible persistent wiring, use orq connect omp, then start omp normally (named profiles need their own directory wired).")
	}
	return nil
}

// ompUserAgentDir applies the pi ownership guard to the shared agent directory.
func ompUserAgentDir(ctx *AgentContext) (string, error) {
	home := ctx.HomeDir
	if home == nil {
		home = os.UserHomeDir
	}
	homeDir, _ := home()
	dir, err := OmpAgentDir(ctx.Getenv, homeDir)
	if err != nil {
		return "", err
	}
	if OmpPiOwned(dir, homeDir) {
		return "", nil
	}
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		return "", fmt.Errorf("omp agent dir %s is not a directory", dir)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return dir, nil
}

// OmpAgentDir resolves the shared pi/omp agent directory without consulting
// process-global home state.
func OmpAgentDir(getenv func(string) string, home string) (string, error) {
	dir := strings.TrimSpace(getenv("PI_CODING_AGENT_DIR"))
	if dir == "" {
		if home == "" {
			return "", errors.New("omp agent directory needs a home directory or PI_CODING_AGENT_DIR")
		}
		dir = filepath.Join(home, ".omp", "agent")
	}
	return filepath.Abs(filepath.Clean(dir))
}

// Seed persistent state before linking, so a first login or conversation is
// not created in the disposable session directory.
func prepareOmpState(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		return fmt.Errorf("create omp sessions dir: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "agent.db"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create omp state database: %w", err)
	}
	return file.Close()
}

// ompSessionMCP is the user's .mcp.json (if any) with the orq-workspace
// entry added. A same-named entry of theirs is kept. Invalid JSON values are
// treated as absent and are never written back to the user's directory.
func ompSessionMCP(userDir, url string) ([]byte, error) {
	var cfg map[string]any
	if userDir != "" {
		path := filepath.Join(userDir, ".mcp.json")
		raw, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil && json.Unmarshal(raw, &cfg) != nil {
			cfg = nil
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		cfg["mcpServers"] = servers
	}
	if _, ok := servers[MCPServerName]; !ok {
		servers[MCPServerName] = HTTPMCPEntry(url)
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// linkOmpAgentDir symlinks user state into the session. The caller handles
// symlink failure by discarding this overlay and rebuilding an isolated one.
func linkOmpAgentDir(userDir, sessionDir string, owned map[string]bool, symlink func(string, string) error) error {
	if userDir == "" {
		return nil
	}
	entries, err := os.ReadDir(userDir)
	if err != nil {
		return fmt.Errorf("read omp agent dir: %w", err)
	}
	for _, e := range entries {
		if name := e.Name(); !owned[name] {
			if err := symlink(filepath.Join(userDir, name), filepath.Join(sessionDir, name)); err != nil {
				return fmt.Errorf("link %s into the omp session dir: %w: %w", name, errOmpSymlink, err)
			}
		}
	}
	return nil
}

func rebuildIsolatedOmpConfig(dir, models, mcpURL string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ompConfigFile), []byte(models), 0o600); err != nil {
		return err
	}
	if mcpURL != "" {
		data, err := json.Marshal(map[string]any{
			"mcpServers": map[string]any{MCPServerName: HTTPMCPEntry(mcpURL)},
		})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// OmpPiOwned guards redirected directories holding pi's models.json but no
// models.yml. The default ~/.omp/agent belongs to omp even before its JSON
// config has been migrated to YAML.
func OmpPiOwned(dir, home string) bool {
	if absolute, err := filepath.Abs(dir); err == nil && absolute == filepath.Join(home, ".omp", "agent") {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "models.json")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, ompConfigFile))
	return errors.Is(err, os.ErrNotExist)
}
