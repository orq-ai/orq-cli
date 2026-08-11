package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orq/cli/custom/auth"
)

const testMCPURL = "https://api.orq.ai/v2/mcp"

func readBack(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return out
}

// The whole point of merging rather than overwriting: an agent config is the
// user's, and it usually already has content we must not lose.
func TestWriteMCPPreservesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	original := `{
  "mcpServers": {"other-server": {"type": "http", "url": "https://example.com"}},
  "unrelatedTopLevelKey": {"deeply": {"nested": true}}
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeMCPServersJSON(path, testMCPURL); err != nil {
		t.Fatalf("writeMCPServersJSON: %v", err)
	}

	cfg := readBack(t, path)
	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers missing after write")
	}
	if _, ok := servers["other-server"]; !ok {
		t.Error("existing MCP server was dropped")
	}
	if _, ok := cfg["unrelatedTopLevelKey"]; !ok {
		t.Error("unrelated top-level key was dropped")
	}
	orq, ok := servers[mcpServerName].(map[string]any)
	if !ok {
		t.Fatal("orq-workspace was not registered")
	}
	if orq["url"] != testMCPURL {
		t.Errorf("url = %v, want %v", orq["url"], testMCPURL)
	}
}

// A config we cannot parse must be left alone: rewriting it would destroy data.
func TestWriteMCPRefusesUnparseableConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	broken := `{"mcpServers": {`
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeMCPServersJSON(path, testMCPURL); err == nil {
		t.Fatal("expected an error for unparseable JSON")
	}

	data, _ := os.ReadFile(path)
	if string(data) != broken {
		t.Error("unparseable config was modified")
	}
}

// Re-running setup must not accumulate duplicates or drift.
func TestWriteMCPIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	for i := 0; i < 3; i++ {
		if err := writeMCPRemoteJSON(path, testMCPURL); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	cfg := readBack(t, path)
	servers := cfg["mcp"].(map[string]any)
	if len(servers) != 1 {
		t.Errorf("got %d servers after 3 runs, want 1", len(servers))
	}
}

// The credential must never be baked into a file the user might commit.
func TestWriteMCPNeverEmbedsRawSecret(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ORQ_API_KEY", "sk-orq-super-secret-value")

	cases := map[string]func(string, string) error{
		"claude.json":   writeMCPServersJSON,
		"opencode.json": writeMCPRemoteJSON,
		"kimi.json":     writeMCPKimiJSON,
		"config.toml":   writeMCPCodexTOML,
	}
	for name, write := range cases {
		path := filepath.Join(dir, name)
		if err := write(path, testMCPURL); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "super-secret-value") {
			t.Errorf("%s embedded the raw API key", name)
		}
		if !strings.Contains(string(data), "ORQ_API_KEY") {
			t.Errorf("%s does not reference the ORQ_API_KEY env var", name)
		}
	}
}

// Codex config.toml is appended to, never parsed, so re-runs must detect the
// existing section instead of stacking duplicate blocks.
func TestWriteMCPCodexTOMLAppendsOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	preexisting := "model = \"o3\"\n\n[mcp_servers.other]\nurl = \"https://example.com\"\n"
	if err := os.WriteFile(path, []byte(preexisting), 0o600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := writeMCPCodexTOML(path, testMCPURL); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	data, _ := os.ReadFile(path)
	got := string(data)
	if n := strings.Count(got, "[mcp_servers."+mcpServerName+"]"); n != 1 {
		t.Errorf("orq section appears %d times, want 1", n)
	}
	if !strings.Contains(got, "[mcp_servers.other]") || !strings.Contains(got, `model = "o3"`) {
		t.Error("pre-existing TOML content was lost")
	}
}

func tarGzFixture(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractTarGzWritesFiles(t *testing.T) {
	dir := t.TempDir()
	archive := tarGzFixture(t, map[string]string{
		"repo-main/.agents/skills/build-agent/SKILL.md": "# build agent",
	})
	if err := extractTarGz(bytes.NewReader(archive), dir); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "repo-main", ".agents", "skills", "build-agent", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected file was not extracted: %v", err)
	}
	if string(got) != "# build agent" {
		t.Errorf("content = %q", got)
	}
}

// assistant-plugins keeps its skills at the repo root; older archives kept them
// under .agents/skills. Both must resolve, or setup reports "skills archive
// contained none of ..." and silently instruments agents without skills.
func TestFindSkillsRootAcceptsBothLayouts(t *testing.T) {
	for _, layout := range archiveSkillsDirs {
		cache := t.TempDir()
		skill := filepath.Join(cache, "assistant-plugins-main", layout, "orq-cli")
		if err := os.MkdirAll(skill, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := findSkillsRoot(cache)
		if err != nil {
			t.Fatalf("%s: findSkillsRoot: %v", layout, err)
		}
		want := filepath.Join(cache, "assistant-plugins-main", layout)
		if got != want {
			t.Errorf("%s: got %q, want %q", layout, got, want)
		}
	}
}

func TestFindSkillsRootReportsMissingLayout(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "assistant-plugins-main", ".agents", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := findSkillsRoot(cache); err == nil {
		t.Fatal("expected an error when the archive holds no skills directory")
	}
}

// A tarball is remote input: an entry escaping the destination would let it
// overwrite arbitrary files.
func TestExtractTarGzRejectsPathTraversal(t *testing.T) {
	for _, name := range []string{"../escaped.txt", "repo/../../escaped.txt"} {
		dir := t.TempDir()
		archive := tarGzFixture(t, map[string]string{name: "pwned"})
		err := extractTarGz(bytes.NewReader(archive), dir)
		if err == nil {
			t.Errorf("%s: expected extraction to be refused", name)
		}
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.txt")); statErr == nil {
			t.Errorf("%s: file escaped the destination directory", name)
		}
	}
}

func TestInstallSkillsCopiesEverySkill(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(t.TempDir(), "skills")
	for _, skill := range []string{"build-agent", "run-experiment"} {
		if err := os.MkdirAll(filepath.Join(src, skill, "resources"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, skill, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, skill, "resources", "notes.md"), []byte("y"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A stray file at the top level is not a skill and must be ignored.
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}

	count, err := installSkills(src, dest)
	if err != nil {
		t.Fatalf("installSkills: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
	if _, err := os.Stat(filepath.Join(dest, "build-agent", "resources", "notes.md")); err != nil {
		t.Errorf("nested resource not copied: %v", err)
	}
}

func TestSafeJoinAllowsNormalEntries(t *testing.T) {
	dest := t.TempDir()
	got, err := safeJoin(dest, "repo-main/.agents/skills/a/SKILL.md")
	if err != nil {
		t.Fatalf("safeJoin: %v", err)
	}
	if !strings.HasPrefix(got, dest) {
		t.Errorf("got %q, want a path under %q", got, dest)
	}
}

func model(provider, id string, ctx int, active, fns bool, kind string) auth.RouterModel {
	m := auth.RouterModel{ModelID: id, Provider: provider, Type: kind, Active: active, Functions: fns}
	m.Metadata.ContextWindow = ctx
	return m
}

// A coding agent needs tool calling, and a retired or chat-less model in the
// config is a dead entry the user has to debug.
func TestCandidateCodingModelsFiltersUnusable(t *testing.T) {
	catalogue := []auth.RouterModel{
		model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat"),
		model("anthropic", "claude-sonnet-4-5", 200000, true, true, "chat"),
		model("anthropic", "claude-sonnet-legacy", 200000, false, true, "chat"), // inactive
		model("openai", "gpt-5-notools", 400000, true, false, "chat"),           // no tools
		model("openai", "gpt-5-embed", 400000, true, true, "embedding"),         // wrong type
		model("cohere", "command-r", 128000, true, true, "chat"),                // not preferred
	}
	groups := auth.CandidateCodingModels(catalogue, []string{"anthropic/claude-sonnet-4", "openai/gpt-5"})

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1 (only the anthropic family is usable)", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Fatalf("got %d anthropic candidates, want 2", len(groups[0]))
	}
	// Newest first, so the caller probes the current model before older ones.
	if got := groups[0][0].ModelID; got != "claude-sonnet-4-6" {
		t.Errorf("best candidate = %q, want claude-sonnet-4-6", got)
	}
}

// Kimi 0.34 resolves provider credentials from config.toml only — no env
// fallback, no ${VAR} interpolation — so the literal key is required.
func TestKimiProviderBlockWritesLiteralKey(t *testing.T) {
	models := []auth.RouterModel{model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")}
	block := kimiProviderBlock("https://api.orq.ai/v2/router", "sk-test-key", models)

	for _, want := range []string{
		`[providers.orq]`,
		`type = "openai"`,
		`base_url = "https://api.orq.ai/v2/router"`,
		`api_key = "sk-test-key"`,
		`[models."claude-sonnet-4-6"]`,
		`model = "anthropic/claude-sonnet-4-6"`,
		`max_context_size = 200000`,
	} {
		if !strings.Contains(block, want) {
			t.Errorf("block is missing %q\n---\n%s", want, block)
		}
	}
}

// Kimi requires max_context_size, so a catalogue entry without one must still
// produce a loadable config.
func TestKimiProviderBlockDefaultsMissingContextWindow(t *testing.T) {
	models := []auth.RouterModel{model("anthropic", "mystery", 0, true, true, "chat")}
	if !strings.Contains(kimiProviderBlock("https://x/v2/router", "sk-test-key", models), "max_context_size = 128000") {
		t.Error("missing context window did not fall back to a default")
	}
}

func TestWriteKimiProviderTOMLAppendsOnceAndPreserves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[providers.moonshot]\ntype = \"kimi\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	models := []auth.RouterModel{model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")}

	for i := 0; i < 3; i++ {
		if err := writeKimiProviderTOML(path, "https://api.orq.ai/v2/router", "sk-test-key", models); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	data, _ := os.ReadFile(path)
	got := string(data)
	if n := strings.Count(got, "[providers.orq]"); n != 1 {
		t.Errorf("provider block written %d times, want 1", n)
	}
	if !strings.Contains(got, "[providers.moonshot]") {
		t.Error("pre-existing provider was lost")
	}
	if n := strings.Count(got, `api_key = "sk-test-key"`); n != 1 {
		t.Errorf("api_key written %d times, want 1", n)
	}
}

// A config written by an older build carries an `${ORQ_API_KEY}` placeholder
// kimi never interpolates, so it sends that string as the bearer and every
// request comes back 401. Re-running setup must repair it, not skip it.
func TestWriteKimiProviderTOMLReplacesStaleOrqTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	stale := `default_model = "kimi-k2.7-code-highspeed"

[providers.orq]
type = "openai"
base_url = "https://api.orq.ai/v2/router"
api_key = "${ORQ_API_KEY}"

[providers.orq.env]
ORQ_API_KEY = "ORQ_API_KEY"

[models."kimi-k2.7-code-highspeed"]
provider = "orq"
model = "moonshotai/kimi-k2.7-code-highspeed"
max_context_size = 262144

[thinking]
enabled = false
`
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	models := []auth.RouterModel{model("moonshotai", "kimi-k2.7-code-highspeed", 262144, true, true, "chat")}
	if err := writeKimiProviderTOML(path, "https://api.orq.ai/v2/router", "sk-fresh-key", models); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	got := string(data)
	if strings.Contains(got, "${ORQ_API_KEY}") {
		t.Errorf("placeholder key survived\n---\n%s", got)
	}
	if strings.Contains(got, "[providers.orq.env]") {
		t.Errorf("dead env sub-table survived\n---\n%s", got)
	}
	if !strings.Contains(got, `api_key = "sk-fresh-key"`) {
		t.Errorf("fresh key not written\n---\n%s", got)
	}
	// Duplicate tables make kimi reject the whole file.
	if n := strings.Count(got, "[providers.orq]"); n != 1 {
		t.Errorf("[providers.orq] appears %d times, want 1", n)
	}
	if n := strings.Count(got, `[models."kimi-k2.7-code-highspeed"]`); n != 1 {
		t.Errorf("model table appears %d times, want 1", n)
	}
	// Everything we do not own must survive byte for byte.
	for _, want := range []string{`default_model = "kimi-k2.7-code-highspeed"`, "[thinking]", "enabled = false"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost unrelated config %q\n---\n%s", want, got)
		}
	}
}
