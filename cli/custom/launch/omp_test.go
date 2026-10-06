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
	if dir == "" || plan.Env["ORQ_API_KEY"] != "orq-key" || plan.Env["ORQ_SERVER"] != DefaultGatewayAPIBaseURL || len(plan.Env) != 3 {
		t.Fatalf("env: %v", plan.Env)
	}
	if len(plan.PreArgs) != 4 || plan.PreArgs[0] != "--provider" || plan.PreArgs[1] != OmpProvider ||
		plan.PreArgs[2] != "--model" || plan.PreArgs[3] != DefaultOmpModel {
		t.Fatalf("PreArgs: %v", plan.PreArgs)
	}
	if len(plan.TempDirs) != 1 || plan.TempDirs[0].HostPath != dir {
		t.Fatalf("TempDirs: %v", plan.TempDirs)
	}

	// omp is a sharedReader: session skills go to the real home, never the
	// redirected config dir.
	if got := strings.Join(dirEntries(t, dir), ","); got != ".mcp.json,models.yml" {
		t.Fatalf("temp dir holds %s, want only .mcp.json and models.yml", got)
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
	if prov.APIKey != "$ORQ_API_KEY" {
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
	if got := strings.Join(dirEntries(t, dir), ","); got != "models.yml" {
		t.Fatalf("with MCP off the temp dir holds %s, want only models.yml", got)
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
