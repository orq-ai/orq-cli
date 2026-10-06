package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

const ompRouter = "https://api.orq.ai/v3/router"

// ompUserModelsYAML is a models.yml with the things a rewrite through a map
// would destroy: comments, key order, a provider that is not ours.
const ompUserModelsYAML = `# my models
providers:
  # local inference
  ollama:
    baseUrl: http://localhost:11434 # default port
    models:
      - id: llama
top: 1
`

func ompHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	return home
}

func ompModelsPath(t *testing.T) string {
	t.Helper()
	path, err := ompPath("models.yml")(true)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOmpPathAndDetectHonorTheEnvDir(t *testing.T) {
	home := ompHome(t)
	if got := ompModelsPath(t); got != filepath.Join(home, ".omp", "agent", "models.yml") {
		t.Errorf("default path = %q", got)
	}
	if ompDetect() {
		t.Error("detected omp with no agent dir")
	}
	if err := os.MkdirAll(filepath.Join(home, ".omp", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !ompDetect() {
		t.Error("did not detect ~/.omp/agent")
	}

	dir := filepath.Join(t.TempDir(), "redirected")
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	if got := ompModelsPath(t); got != filepath.Join(dir, "models.yml") {
		t.Errorf("env path = %q", got)
	}
	if ompDetect() {
		t.Error("detected a missing PI_CODING_AGENT_DIR")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !ompDetect() {
		t.Error("did not detect an existing PI_CODING_AGENT_DIR")
	}
}

func TestOmpRefusesAPiOwnedDir(t *testing.T) {
	ompHome(t)
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if ompDetect() {
		t.Error("a pi-owned dir was detected as omp")
	}
	_, err := writeOmpProviderYAML(filepath.Join(dir, "models.yml"), ompRouter, "", openCodeModels(), "")
	if err == nil || !strings.Contains(err.Error(), "PI_CODING_AGENT_DIR") {
		t.Fatalf("err = %v, want one naming PI_CODING_AGENT_DIR", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "models.yml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("models.yml was written into pi's dir")
	}

	err = writeOmpMCP(filepath.Join(dir, "mcp.json"), ompRouter)
	if err == nil || !strings.Contains(err.Error(), "PI_CODING_AGENT_DIR") {
		t.Fatalf("mcp err = %v, want one naming PI_CODING_AGENT_DIR", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "mcp.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("mcp.json was written into pi's dir")
	}

	// A models.yml alongside makes it omp-writable again.
	if err := os.WriteFile(filepath.Join(dir, "models.yml"), []byte("providers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ompDetect() {
		t.Error("a dir holding models.yml was not detected")
	}
	if _, err := writeOmpProviderYAML(filepath.Join(dir, "models.yml"), ompRouter, "", openCodeModels(), ""); err != nil {
		t.Errorf("write into a dir with models.yml: %v", err)
	}
}

func TestOmpWriterCreatesAMissingFile(t *testing.T) {
	ompHome(t)
	path := ompModelsPath(t)
	n, err := writeOmpProviderYAML(path, ompRouter, "sk-secret", openCodeModels(), "")
	if err != nil {
		t.Fatal(err)
	}
	if n != len(openCodeModels()) {
		t.Errorf("listed %d models, want %d", n, len(openCodeModels()))
	}
	data := string(mustRead(t, path))
	if strings.Contains(data, "sk-secret") {
		t.Errorf("api key landed in the file:\n%s", data)
	}
	var parsed struct {
		Providers map[string]struct {
			BaseURL string           `yaml:"baseUrl"`
			APIKey  string           `yaml:"apiKey"`
			Models  []map[string]any `yaml:"models"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal([]byte(data), &parsed); err != nil {
		t.Fatalf("written file is not YAML: %v\n%s", err, data)
	}
	orq := parsed.Providers["orq"]
	if orq.BaseURL != ompRouter || orq.APIKey != "$ORQ_API_KEY" || len(orq.Models) != n {
		t.Errorf("provider block = %+v\n%s", orq, data)
	}
	if strings.Contains(data, "{") {
		t.Errorf("block was spliced in flow (JSON) style:\n%s", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if !ompProviderPresent(path) {
		t.Error("ompProviderPresent false after the write")
	}
}

func TestOmpWriterKeepsCommentsAndOrderAndReplacesOurBlock(t *testing.T) {
	ompHome(t)
	path := ompModelsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(ompUserModelsYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := writeOmpProviderYAML(path, ompRouter, "", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}
	merged := string(mustRead(t, path))
	for _, keep := range []string{"# my models", "# local inference", "# default port", "ollama:", "top: 1"} {
		if !strings.Contains(merged, keep) {
			t.Errorf("lost %q:\n%s", keep, merged)
		}
	}
	if strings.Index(merged, "ollama:") > strings.Index(merged, "orq:") || strings.Index(merged, "orq:") > strings.Index(merged, "top: 1") {
		t.Errorf("key order changed:\n%s", merged)
	}
	if string(mustRead(t, path+".orq-bak")) != ompUserModelsYAML {
		t.Error("backup is not the original file")
	}

	// A rerun with a different catalogue replaces the block, not merges into it.
	if _, err := writeOmpProviderYAML(path, "https://other.example/v3/router", "", openCodeModels()[:1], ""); err != nil {
		t.Fatal(err)
	}
	again := string(mustRead(t, path))
	if strings.Count(again, "orq:") != 1 || !strings.Contains(again, "https://other.example/v3/router") || strings.Contains(again, ompRouter) {
		t.Errorf("rerun did not replace our block:\n%s", again)
	}
	if !strings.Contains(again, "# local inference") {
		t.Errorf("rerun lost a comment:\n%s", again)
	}
}

func TestOmpWriterKeepsACommentOnlyFilesComments(t *testing.T) {
	ompHome(t)
	path := ompModelsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# user comment one\n# user comment two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeOmpProviderYAML(path, ompRouter, "", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}
	got := string(mustRead(t, path))
	for _, keep := range []string{"# user comment one", "# user comment two", "providers:", "orq:"} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %q:\n%s", keep, got)
		}
	}
}

func TestOmpRemoverKeepsCommentsAndOrder(t *testing.T) {
	ompHome(t)
	path := ompModelsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(ompUserModelsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeOmpProviderYAML(path, ompRouter, "", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}
	removed, err := removeOmpProvider(path)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if got := string(mustRead(t, path)); got != ompUserModelsYAML {
		t.Errorf("removal did not restore the user's file:\nwant %q\ngot  %q", ompUserModelsYAML, got)
	}
	if ompProviderPresent(path) {
		t.Error("still present after removal")
	}
	if removed, err := removeOmpProvider(path); err != nil || removed {
		t.Errorf("second removal: removed=%v err=%v", removed, err)
	}
}

func TestOmpRemoverDeletionFollowsTheBackupRule(t *testing.T) {
	t.Run("a file we created is deleted", func(t *testing.T) {
		ompHome(t)
		path := ompModelsPath(t)
		if _, err := writeOmpProviderYAML(path, ompRouter, "", openCodeModels(), ""); err != nil {
			t.Fatal(err)
		}
		if removed, err := removeOmpProvider(path); err != nil || !removed {
			t.Fatalf("removed=%v err=%v", removed, err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Error("a file holding only our block survived removal")
		}
	})
	t.Run("a file with a backup is kept", func(t *testing.T) {
		ompHome(t)
		path := ompModelsPath(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		// The user's file held only providers.orq, so it empties out — but it existed before us.
		if err := os.WriteFile(path, []byte("providers:\n  orq:\n    baseUrl: http://old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := writeOmpProviderYAML(path, ompRouter, "", openCodeModels(), ""); err != nil {
			t.Fatal(err)
		}
		if removed, err := removeOmpProvider(path); err != nil || !removed {
			t.Fatalf("removed=%v err=%v", removed, err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("a user's file was deleted: %v", err)
		}
		if ompProviderPresent(path) {
			t.Error("block still present")
		}
	})
}

func TestOmpMalformedFilesAreRefusedNotOverwritten(t *testing.T) {
	for name, content := range map[string]string{
		"invalid yaml":       "providers: [unclosed\n  orq: {",
		"not a mapping":      "- a\n- b\n",
		"providers a scalar": "providers: nope\n",
		"providers a list":   "providers:\n  - orq\n",
	} {
		t.Run(name, func(t *testing.T) {
			ompHome(t)
			path := ompModelsPath(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if ompProviderPresent(path) {
				t.Error("reads as present")
			}
			if _, err := removeOmpProvider(path); err == nil {
				t.Error("the remover accepted it")
			}
			if _, err := writeOmpProviderYAML(path, ompRouter, "", openCodeModels(), ""); err == nil {
				t.Error("the writer accepted it")
			}
			if got := string(mustRead(t, path)); got != content {
				t.Errorf("file was modified: %q", got)
			}
		})
	}
}

func TestOmpMCPEntryIsTypeAndURLOnly(t *testing.T) {
	entry := ompMCPEntry("https://api.orq.ai/v2/mcp")
	if len(entry) != 2 || entry["type"] != "http" || entry["url"] != "https://api.orq.ai/v2/mcp" {
		t.Errorf("entry = %v, want exactly {type: http, url}", entry)
	}
}

func TestOmpSpecWiring(t *testing.T) {
	home := ompHome(t)
	spec, ok := lookupAgent("omp")
	if !ok {
		t.Fatal("omp not registered")
	}
	if mcpScopeAware(spec) {
		t.Error("omp's MCP config is global-only, but reads as scope-aware")
	}
	path, err := spec.mcpConfig(true)
	if err != nil || path != filepath.Join(home, ".omp", "agent", "mcp.json") {
		t.Errorf("mcp path = %q, %v", path, err)
	}
	if spec.mcpLogin != "run /mcp in omp" {
		t.Errorf("mcpLogin = %q", spec.mcpLogin)
	}
	if spec.providerEmbedsKey {
		t.Error("omp must not embed the key")
	}
}

func TestMCPCheckNamesOmpLoginCommand(t *testing.T) {
	home := ompHome(t)
	t.Setenv("CODEX_HOME", "")
	dir := filepath.Join(home, ".omp", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	check, ok := mcpCheck()
	if !ok || check.Status != "warn" || !strings.Contains(check.Message, "orq connect omp mcp") {
		t.Fatalf("unwired omp: ok=%v %+v", ok, check)
	}

	spec, _ := lookupAgent("omp")
	if err := spec.writeMCP(filepath.Join(dir, "mcp.json"), "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatal(err)
	}
	check, ok = mcpCheck()
	if !ok || check.Status != "pass" || !strings.Contains(check.Message, "omp MCP entry present — run /mcp in omp") {
		t.Fatalf("wired omp: ok=%v %+v", ok, check)
	}
}
