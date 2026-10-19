package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestWriteOmpConfigDir(t *testing.T) {
	const content = `{"providers":{"orq":{}}}`
	dir, cleanup, err := writeOmpConfigDir(content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(dir), "orq-omp-") {
		t.Fatalf("dir %q is not an orq-omp- temp dir", dir)
	}
	path := filepath.Join(dir, "models.yml")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != content {
		t.Fatalf("models.yml = %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("models.yml mode = %v, %v", info, err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup left %s behind: %v", dir, err)
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestOmpResolvePlan(t *testing.T) {
	ctx := sessionSkillsCtx(GatewayFlags{MCP: true})
	ctx.Fetch = fetcherOf(DefaultOmpModel)
	plan, err := ompAgent().Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()

	dir := plan.Env["PI_CODING_AGENT_DIR"]
	if dir == "" || plan.Env["ORQ_API_KEY"] != "orq-key" || plan.Env["ORQ_SERVER"] != DefaultGatewayAPIBaseURL {
		t.Fatalf("env: %v", plan.Env)
	}
	if len(plan.PreArgs) != 4 || plan.PreArgs[0] != "--provider" || plan.PreArgs[1] != OmpProvider ||
		plan.PreArgs[2] != "--model" || plan.PreArgs[3] != DefaultOmpModel {
		t.Fatalf("PreArgs: %v", plan.PreArgs)
	}
	if len(plan.TempDirs) != 1 || plan.TempDirs[0].HostPath != dir {
		t.Fatalf("TempDirs: %v", plan.TempDirs)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	entry := doc.MCPServers[MCPServerName]
	if entry["type"] != "http" || entry["url"] != mcpURL(ctx) || len(entry) != 2 {
		t.Fatalf("mcp entry: %v", entry)
	}
	if strings.Contains(string(raw), "orq-key") || strings.Contains(string(raw), "headers") {
		t.Fatalf("mcp.json must carry no credential or headers: %s", raw)
	}

	// models.yml is JSON content; any YAML consumer must read it.
	models, err := os.ReadFile(filepath.Join(dir, "models.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(models), "orq-key") {
		t.Fatalf("models.yml leaks the key:\n%s", models)
	}
	var parsed struct {
		Providers map[string]struct {
			APIKey string `yaml:"apiKey"`
			Models []struct {
				ID string `yaml:"id"`
			} `yaml:"models"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(models, &parsed); err != nil {
		t.Fatalf("models.yml is not valid YAML: %v\n%s", err, models)
	}
	prov := parsed.Providers[OmpProvider]
	if prov.APIKey != OmpAPIKeyRef {
		t.Fatalf("apiKey = %q", prov.APIKey)
	}
	found := false
	for _, m := range prov.Models {
		if m.ID == DefaultOmpModel {
			found = true
		}
	}
	if !found {
		t.Fatalf("gateway model %s missing from providers.orq.models: %+v", DefaultOmpModel, prov.Models)
	}

	plan.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup left %s behind", dir)
	}
}

func TestOmpResolveNoMCP(t *testing.T) {
	plan, err := ompAgent().Resolve(sessionSkillsCtx(GatewayFlags{}))
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	dir := plan.Env["PI_CODING_AGENT_DIR"]
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("session MCP config exists with MCP off: %v", err)
	}
}

// The session dir is the user's agent dir plus orq: their logins (agent.db,
// where omp keys the MCP OAuth credential by URL), settings, mcp.json and
// sessions are linked in; models.yml and .mcp.json are the session's own
// copies of the user's with orq merged in. The user's files are byte-for-byte
// unchanged, including after cleanup.
func TestOmpSessionKeepsTheUsersConfigAndAddsOrq(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	user := t.TempDir()
	files := map[string]string{
		"agent.db":   "db",
		"config.yml": "theme: dark\n",
		"models.yml": "# mine\nproviders:\n  local:\n    baseUrl: http://localhost:11434\n",
		"mcp.json":   `{"mcpServers":{"github":{"type":"http","url":"https://gh.example"}}}`,
		".mcp.json":  `{"mcpServers":{"linear":{"type":"http","url":"https://linear.example"}}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(user, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(user, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := sessionSkillsCtx(GatewayFlags{MCP: true})
	ctx.Getenv = env(map[string]string{"PI_CODING_AGENT_DIR": user})
	plan, err := ompAgent().Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := plan.Env["PI_CODING_AGENT_DIR"]
	if dir == user {
		t.Fatal("session runs in the user's own dir")
	}
	for _, name := range []string{"agent.db", "config.yml", "mcp.json", "sessions"} {
		if target, err := os.Readlink(filepath.Join(dir, name)); err != nil || target != filepath.Join(user, name) {
			t.Errorf("%s: link %q, %v", name, target, err)
		}
	}

	models, err := os.ReadFile(filepath.Join(dir, "models.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Providers map[string]any `yaml:"providers"`
	}
	if err := yaml.Unmarshal(models, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Providers["local"] == nil || parsed.Providers[OmpProvider] == nil || !strings.Contains(string(models), "# mine") {
		t.Errorf("session models.yml lost the user's provider or comment, or lacks orq:\n%s", models)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mcp struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &mcp); err != nil {
		t.Fatal(err)
	}
	if mcp.MCPServers["linear"]["url"] != "https://linear.example" || mcp.MCPServers[MCPServerName]["url"] != mcpURL(ctx) {
		t.Errorf("session .mcp.json = %s", raw)
	}

	plan.Cleanup()
	for name, body := range files {
		if got, err := os.ReadFile(filepath.Join(user, name)); err != nil || string(got) != body {
			t.Errorf("user's %s = %q, %v", name, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(user, "sessions")); err != nil {
		t.Errorf("cleanup removed the user's sessions dir: %v", err)
	}
}

func TestOmpSymlinkFailureDiscardsPartialOverlay(t *testing.T) {
	user := t.TempDir()
	files := map[string]string{
		"00-config.yml": "theme: private\n",
		"agent.db":      "private-db",
		".mcp.json":     `{"mcpServers":{"private":{"type":"http","url":"https://private.example"}}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(user, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := sessionSkillsCtx(GatewayFlags{MCP: true})
	ctx.Getenv = env(map[string]string{"PI_CODING_AGENT_DIR": user})
	attempts := 0
	ctx.Symlink = func(oldname, newname string) error {
		attempts++
		if attempts == 2 {
			return os.ErrPermission
		}
		return os.Symlink(oldname, newname)
	}
	plan, err := resolveOmp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	if attempts != 2 {
		t.Fatalf("symlink attempts = %d, want partial link then failure", attempts)
	}
	if len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0], "isolated session config") {
		t.Fatalf("warnings = %v", plan.Warnings)
	}
	dir := plan.Env["PI_CODING_AGENT_DIR"]
	if got, want := strings.Join(dirEntries(t, dir), ","), ".mcp.json,models.yml"; got != want {
		t.Fatalf("isolated session entries = %q, want %q", got, want)
	}
	models, err := os.ReadFile(filepath.Join(dir, "models.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var modelDoc map[string]any
	if err := yaml.Unmarshal(models, &modelDoc); err != nil {
		t.Fatal(err)
	}
	providers, _ := modelDoc["providers"].(map[string]any)
	if len(providers) != 1 || providers[OmpProvider] == nil || strings.Contains(string(models), "private") {
		t.Fatalf("isolated models.yml contains user config: %s", models)
	}
	mcp, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil || strings.Contains(string(mcp), "private") || !strings.Contains(string(mcp), MCPServerName) {
		t.Fatalf("isolated mcp config = %s, %v", mcp, err)
	}
	for name, want := range files {
		if got, err := os.ReadFile(filepath.Join(user, name)); err != nil || string(got) != want {
			t.Errorf("user file %s changed: %q, %v", name, got, err)
		}
	}
}

func TestOmpSessionMCPIgnoresInvalidAndNonObjectUserConfig(t *testing.T) {
	for name, content := range map[string]string{
		"malformed": `{`,
		"empty":     "",
		"null":      `null`,
		"array":     `[]`,
		"string":    `"servers"`,
	} {
		t.Run(name, func(t *testing.T) {
			user := t.TempDir()
			path := filepath.Join(user, ".mcp.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ompSessionMCP(user, "https://session.example")
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Servers map[string]map[string]any `json:"mcpServers"`
			}
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatal(err)
			}
			entry := doc.Servers[MCPServerName]
			if len(doc.Servers) != 1 || entry["url"] != "https://session.example" || entry["type"] != "http" {
				t.Fatalf("session config = %s", got)
			}
			if source, err := os.ReadFile(path); err != nil || string(source) != content {
				t.Fatalf("user source changed: %q, %v", source, err)
			}
		})
	}
}

// An orq-workspace entry the user already has (from `orq connect omp mcp`) is
// theirs: the session adds none of its own over it.
func TestOmpSessionMCPKeepsTheUsersOrqEntry(t *testing.T) {
	user := t.TempDir()
	mine := `{"mcpServers":{"` + MCPServerName + `":{"type":"http","url":"https://mine.example"}}}`
	if err := os.WriteFile(filepath.Join(user, ".mcp.json"), []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := ompSessionMCP(user, "https://session.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "https://mine.example") || strings.Contains(string(raw), "session.example") {
		t.Errorf("session .mcp.json = %s", raw)
	}
}

func TestOmpSessionDoesNotOverlayAPiOwnedDir(t *testing.T) {
	pi := t.TempDir()
	if err := os.WriteFile(filepath.Join(pi, "models.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := sessionSkillsCtx(GatewayFlags{})
	ctx.Getenv = env(map[string]string{"PI_CODING_AGENT_DIR": pi})
	plan, err := ompAgent().Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	if got := strings.Join(dirEntries(t, plan.Env["PI_CODING_AGENT_DIR"]), ","); got != "models.yml" {
		t.Fatalf("pi's dir was overlaid: %s", got)
	}
}

func TestOmpNewStateSurvivesCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "partial"}[existing], func(t *testing.T) {
			user := filepath.Join(t.TempDir(), "agent")
			if existing {
				if err := os.Mkdir(user, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ctx := sessionSkillsCtx(GatewayFlags{})
			ctx.Getenv = env(map[string]string{"PI_CODING_AGENT_DIR": user})
			first, err := resolveOmp(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Cleanup()
			dir := first.Env["PI_CODING_AGENT_DIR"]
			if err := os.WriteFile(filepath.Join(dir, "agent.db"), []byte("login-state"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sessions", "conversation.jsonl"), []byte("conversation"), 0o600); err != nil {
				t.Fatal(err)
			}
			first.Cleanup()
			second, err := resolveOmp(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Cleanup()
			for name, want := range map[string]string{"agent.db": "login-state", "sessions/conversation.jsonl": "conversation"} {
				got, err := os.ReadFile(filepath.Join(second.Env["PI_CODING_AGENT_DIR"], name))
				if err != nil || string(got) != want {
					t.Errorf("second launch %s = %q, %v; want %q", name, got, err, want)
				}
			}
		})
	}
}

func TestOmpDefaultLegacyDirectoryKeepsUserSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	user := filepath.Join(home, ".omp", "agent")
	if err := os.MkdirAll(user, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"providers":{"local":{"baseUrl":"http://localhost:11434"}}}`
	if err := os.WriteFile(filepath.Join(user, "models.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(user, "config.yml"), []byte("theme: dark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := sessionSkillsCtx(GatewayFlags{})
	ctx.HomeDir = func() (string, error) { return home, nil }
	plan, err := resolveOmp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	dir := plan.Env["PI_CODING_AGENT_DIR"]
	if got, err := os.ReadFile(filepath.Join(dir, "config.yml")); err != nil || string(got) != "theme: dark\n" {
		t.Fatalf("legacy omp config lost: %q, %v", got, err)
	}
	models, err := os.ReadFile(filepath.Join(dir, "models.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers map[string]any `yaml:"providers"`
	}
	if err := yaml.Unmarshal(models, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Providers["local"] == nil || doc.Providers[OmpProvider] == nil {
		t.Fatalf("legacy providers lost: %s", models)
	}
	plan.Cleanup()
	if got, err := os.ReadFile(filepath.Join(user, "models.json")); err != nil || string(got) != legacy {
		t.Fatalf("legacy models changed: %q, %v", got, err)
	}
}

func TestOmpInvalidStatePathPreservesUserFiles(t *testing.T) {
	user := t.TempDir()
	sentinel := filepath.Join(user, "settings.json")
	if err := os.WriteFile(sentinel, []byte(`{"keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(user, "sessions"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := sessionSkillsCtx(GatewayFlags{})
	ctx.Getenv = env(map[string]string{"PI_CODING_AGENT_DIR": user})
	if _, err := resolveOmp(ctx); err == nil {
		t.Fatal("resolveOmp succeeded with a file at sessions/")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != `{"keep":true}` {
		t.Fatalf("user settings changed on invalid state path: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(user, "sessions")); err != nil || string(got) != "not a directory" {
		t.Fatalf("invalid user state path changed: %q, %v", got, err)
	}
}
func TestPrepareOmpStatePreservesExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "agent.db")
	const contents = "existing-login-state"
	if err := os.WriteFile(db, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareOmpState(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(db)
	if err != nil || string(got) != contents {
		t.Fatalf("existing agent.db changed: %q, %v", got, err)
	}
}

func TestOmpRedirectedLaunchDoesNotRequireHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	user := t.TempDir()
	if err := os.WriteFile(filepath.Join(user, "config.yml"), []byte("theme: dark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := sessionSkillsCtx(GatewayFlags{NoSkills: true})
	ctx.HomeDir = func() (string, error) { return "", os.ErrNotExist }
	ctx.Getenv = env(map[string]string{"PI_CODING_AGENT_DIR": user})
	plan, err := resolveOmp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	got, err := os.ReadFile(filepath.Join(plan.Env["PI_CODING_AGENT_DIR"], "config.yml"))
	if err != nil || string(got) != "theme: dark\n" {
		t.Fatalf("redirected config was lost without a home directory: %q, %v", got, err)
	}
}
