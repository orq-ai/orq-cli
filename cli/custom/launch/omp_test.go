package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestOmpSessionMCPConfig(t *testing.T) {
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(ompSessionMCPConfig("https://mcp.example/v2/mcp")), &doc); err != nil {
		t.Fatal(err)
	}
	entry := doc.MCPServers[MCPServerName]
	if len(entry) != 2 || entry["type"] != "http" || entry["url"] != "https://mcp.example/v2/mcp" {
		t.Fatalf("entry must be exactly {type,url}: %v", entry)
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
	if got := strings.Join(dirEntries(t, dir), ","); got != "mcp.json,models.yml" {
		t.Fatalf("temp dir holds %s, want only mcp.json and models.yml", got)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
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
