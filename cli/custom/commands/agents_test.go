package commands

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pelletier/go-toml"

	"orq/cli/custom/auth"
	"orq/cli/custom/launch"
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

// enabled, not active: is_active is true for every catalogue entry, so the
// fixtures set the workspace's enabled flag, which is what the code filters on.
func model(provider, id string, ctx int, enabled, fns bool, kind string) auth.RouterModel {
	m := auth.RouterModel{ModelID: id, Provider: provider, Type: kind, Active: true, Enabled: enabled, Functions: fns}
	m.Metadata.ContextWindow = ctx
	return m
}

// Both commands read the same endpoint and must address a model the same way.
// They did not: launch uses the published refId, setup composed
// provider + "/" + model_id. For a workspace's custom models and autorouters
// the endpoint sends refId "workspace@orq/<name>" while provider is "orq", so
// setup wrote "orq/<name>" — which the agents' model normalizers strip back to
// a bare name the gateway cannot resolve. Config that names uncallable models
// is worse than config that omits them: the model shows up in the picker and
// fails at the first prompt.
func TestSetupAndLaunchAddressModelsIdentically(t *testing.T) {
	const payload = `[
	  {"provider":"anthropic","model_id":"claude-sonnet-4-6","refId":"anthropic/claude-sonnet-4-6",
	   "model_type":"chat","enabled":true,"is_active":true,"has_functions":true,
	   "metadata":{"context_window":200000,"max_output_tokens":64000}},
	  {"provider":"orq","model_id":"router-fast","refId":"acme@orq/router-fast",
	   "model_type":"chat","enabled":true,"is_active":true,"has_functions":true,
	   "metadata":{"context_window":128000,"max_output_tokens":8192}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	catalogue, err := auth.NewClient(srv.URL).ListModels("t")
	if err != nil {
		t.Fatal(err)
	}
	var setupRefs []string
	for _, m := range usableCodingModels(catalogue) {
		setupRefs = append(setupRefs, m.Ref())
	}

	infos, err := launch.FetchEnabledModels("t", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var launchRefs []string
	for _, info := range infos {
		launchRefs = append(launchRefs, info.ID)
	}

	sort.Strings(setupRefs)
	sort.Strings(launchRefs)
	if strings.Join(setupRefs, ",") != strings.Join(launchRefs, ",") {
		t.Errorf("the two commands address the catalogue differently:\n  setup:  %v\n  launch: %v",
			setupRefs, launchRefs)
	}
	// Named explicitly so a regression reads as the bug it is, rather than as
	// two lists that merely stopped matching.
	if got := setupRefs[0]; got != "acme@orq/router-fast" {
		t.Errorf("custom model addressed as %q, want the published refId", got)
	}
}

// A coding agent needs tool calling, and a retired or chat-less model in the
// config is a dead entry the user has to debug.
func TestCandidateCodingModelsFiltersUnusable(t *testing.T) {
	catalogue := []auth.RouterModel{
		model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat"),
		model("anthropic", "claude-sonnet-4-5", 200000, true, true, "chat"),
		model("anthropic", "claude-sonnet-legacy", 200000, false, true, "chat"), // not enabled in this workspace
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
	block := kimiBlock("https://api.orq.ai/v2/router", "sk-test-key", models)

	for _, want := range []string{
		`[providers.orq]`,
		`type = "openai"`,
		`base_url = "https://api.orq.ai/v2/router"`,
		`api_key = "sk-test-key"`,
		`[models."anthropic/claude-sonnet-4-6"]`,
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
	if !strings.Contains(kimiBlock("https://x/v2/router", "sk-test-key", models), "max_context_size = 128000") {
		t.Error("missing context window did not fall back to a default")
	}
}

func openCodeModels() []auth.RouterModel {
	models := []auth.RouterModel{
		model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat"),
		model("openai", "gpt-5.4", 400000, true, true, "chat"),
	}
	models[1].Metadata.SupportsResponses = true
	return models
}

// The config belongs to the user; setup adds one provider to it. Anything else
// in the document — their own providers, their settings, their keybinds — has
// to come back out unchanged.
func TestOpenCodeProviderMergePreservesUserKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	original := `{
  "$schema": "https://opencode.ai/config.json",
  "theme": "tokyonight",
  "provider": {"ollama": {"npm": "@ai-sdk/openai-compatible", "models": {"llama3": {}}}},
  "keybinds": {"leader": "ctrl+x"}
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeOpenCodeProviderJSON(path, "https://api.orq.ai/v3/router", "sk-k",
		openCodeModels(), "anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}

	cfg := readBack(t, path)
	if cfg["theme"] != "tokyonight" {
		t.Errorf("theme = %v, want tokyonight", cfg["theme"])
	}
	if _, ok := cfg["keybinds"]; !ok {
		t.Error("keybinds were dropped")
	}
	providers, ok := cfg["provider"].(map[string]any)
	if !ok {
		t.Fatal("provider block missing")
	}
	if _, ok := providers["ollama"]; !ok {
		t.Error("the user's own provider was dropped")
	}
	for _, want := range []string{launch.OpenCodeChatProvider, launch.OpenCodeResponsesProvider} {
		if _, ok := providers[want]; !ok {
			t.Errorf("provider %q was not registered", want)
		}
	}
}

// The decision this whole change rests on: setup makes orq available, never the
// default. "model" is the user's own default, and neither agent has a profile
// mechanism to scope a change to — writing it would repoint every session at a
// provider whose credential setup cannot guarantee is exported.
func TestOpenCodeProviderNeverSetsTopLevelModel(t *testing.T) {
	t.Run("absent: stays absent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "opencode.json")
		if _, err := writeOpenCodeProviderJSON(path, "https://api.orq.ai/v3/router", "sk-k",
			openCodeModels(), "anthropic/claude-sonnet-4-6"); err != nil {
			t.Fatal(err)
		}
		if got, ok := readBack(t, path)["model"]; ok {
			t.Errorf("setup made orq the default model (%v); it must only offer it", got)
		}
	})

	t.Run("present: untouched", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "opencode.json")
		if err := os.WriteFile(path, []byte(`{"model": "ollama/llama3"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := writeOpenCodeProviderJSON(path, "https://api.orq.ai/v3/router", "sk-k",
			openCodeModels(), "anthropic/claude-sonnet-4-6"); err != nil {
			t.Fatal(err)
		}
		if got := readBack(t, path)["model"]; got != "ollama/llama3" {
			t.Errorf("model = %v, want the user's own ollama/llama3", got)
		}
	})
}

// opencode and kilo interpolate {env:ORQ_API_KEY} at load time, so the key has
// no business being in a file that often lives in a repo.
func TestOpenCodeProviderNeverEmbedsTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	if _, err := writeOpenCodeProviderJSON(path, "https://api.orq.ai/v3/router", "sk-canary-do-not-write",
		openCodeModels(), "anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	got := string(mustRead(t, path))
	if strings.Contains(got, "sk-canary-do-not-write") {
		t.Error("the api key was written into the opencode config")
	}
	if !strings.Contains(got, "{env:ORQ_API_KEY}") {
		t.Errorf("the config does not reference ORQ_API_KEY:\n%s", got)
	}
}

// Setup re-reads the catalogue on every run, so a model the workspace has since
// disabled must disappear rather than linger as an entry that 403s. Re-running
// must also not stack duplicate providers.
func TestOpenCodeProviderRefreshesOnRerun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	if _, err := writeOpenCodeProviderJSON(path, "https://api.orq.ai/v3/router", "sk-k",
		openCodeModels(), "anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	// Second run: the openai model is gone from the workspace, so the Responses
	// provider has nothing left to serve.
	shrunk := []auth.RouterModel{model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")}
	if _, err := writeOpenCodeProviderJSON(path, "https://api.orq.ai/v3/router", "sk-k",
		shrunk, "anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}

	providers := readBack(t, path)["provider"].(map[string]any)
	if _, ok := providers[launch.OpenCodeResponsesProvider]; ok {
		t.Error("the responses provider survived with no models to serve")
	}
	chat, ok := providers[launch.OpenCodeChatProvider].(map[string]any)
	if !ok {
		t.Fatal("chat provider missing after rerun")
	}
	if n := len(chat["models"].(map[string]any)); n != 1 {
		t.Errorf("chat provider lists %d models after rerun, want 1", n)
	}
}

// pi's models.json is where users declare their own local providers (ollama,
// vLLM, LM Studio), so setup adds one key to it and leaves the rest alone.
func TestPiProviderMergePreservesUserProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	original := `{
  "providers": {
    "ollama": {"baseUrl": "http://127.0.0.1:11434/v1", "apiKey": "ollama",
      "api": "openai-completions", "models": [{"id": "gemma4:e2b"}]}
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	written, err := writePiProviderJSON(path, "https://api.orq.ai/v3/router", "sk-k",
		openCodeModels(), "anthropic/claude-sonnet-4-6")
	if err != nil {
		t.Fatal(err)
	}
	if written != 2 {
		t.Errorf("reported %d models, want 2", written)
	}

	providers, ok := readBack(t, path)["providers"].(map[string]any)
	if !ok {
		t.Fatal("providers block missing")
	}
	if _, ok := providers["ollama"]; !ok {
		t.Error("the user's own provider was dropped")
	}
	orq, ok := providers[launch.PiProvider].(map[string]any)
	if !ok {
		t.Fatal("the orq provider was not registered")
	}
	if n := len(orq["models"].([]any)); n != 2 {
		t.Errorf("orq provider lists %d models, want 2", n)
	}
}

// pi resolves $ORQ_API_KEY at request time, so the durable config holds the
// reference and never the credential.
func TestPiProviderNeverEmbedsTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if _, err := writePiProviderJSON(path, "https://api.orq.ai/v3/router", "sk-canary-do-not-write",
		openCodeModels(), "anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	got := string(mustRead(t, path))
	if strings.Contains(got, "sk-canary-do-not-write") {
		t.Error("the api key was written into pi's models.json")
	}
	if !strings.Contains(got, "$ORQ_API_KEY") {
		t.Errorf("the config does not reference ORQ_API_KEY:\n%s", got)
	}
}

// A rerun must replace our block rather than merge into it, or a model the
// workspace has since disabled lingers as an entry that 403s.
func TestPiProviderRefreshesOnRerun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if _, err := writePiProviderJSON(path, "https://api.orq.ai/v3/router", "sk-k",
		openCodeModels(), "anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	shrunk := []auth.RouterModel{model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")}
	if _, err := writePiProviderJSON(path, "https://api.orq.ai/v3/router", "sk-k",
		shrunk, "anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}

	providers := readBack(t, path)["providers"].(map[string]any)
	orq, ok := providers[launch.PiProvider].(map[string]any)
	if !ok {
		t.Fatal("orq provider missing after rerun")
	}
	if n := len(orq["models"].([]any)); n != 1 {
		t.Errorf("orq provider lists %d models after rerun, want 1", n)
	}
}

// Doctor reads pi's wiring out of "providers"; opencode and kilo keep theirs
// under "provider". A detector reading the wrong key reports a wired agent as
// unwired forever.
func TestPiProviderDetectorPairsWithTheWriter(t *testing.T) {
	pi, ok := lookupAgent("pi")
	if !ok {
		t.Fatal("pi is not registered")
	}
	path := filepath.Join(t.TempDir(), "models.json")
	if pi.providerPresent(path) {
		t.Fatal("a missing file read as wired")
	}
	if _, err := writePiProviderJSON(path, "https://api.orq.ai/v3/router", "sk-k",
		openCodeModels(), "anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	if !pi.providerPresent(path) {
		t.Fatal("the config the writer just produced read as unwired")
	}
}

// pi resolves its agent dir as $PI_CODING_AGENT_DIR, then ~/.pi/agent — and
// `orq launch pi` sets that variable itself. Path, detection and the write must
// follow the same resolution, or setup detects one directory, writes a second,
// and reports wired while pi reads a third.
func TestPiPathAndDetectHonorTheEnvDir(t *testing.T) {
	pi, ok := lookupAgent("pi")
	if !ok {
		t.Fatal("pi is not registered")
	}

	// Env set, directory exists: both halves point inside it.
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("HOME", t.TempDir()) // a ~/.pi/agent leftover must not win
	path, err := pi.providerConfig(true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "models.json"); path != want {
		t.Errorf("providerConfig = %q, want %q", path, want)
	}
	if !pi.detect() {
		t.Error("env dir exists but pi was not detected")
	}

	// Env set to a directory that does not exist: not detected, even though
	// ~/.pi/agent does — writing there would report a file pi never reads.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "nonexistent"))
	if pi.detect() {
		t.Error("detected via ~/.pi/agent while PI_CODING_AGENT_DIR points elsewhere")
	}

	// A regular file at the env path is not an agent dir.
	file := filepath.Join(home, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", file)
	if pi.detect() {
		t.Error("a regular file at PI_CODING_AGENT_DIR read as detected")
	}

	// Env unset: the home fallback still works.
	t.Setenv("PI_CODING_AGENT_DIR", "")
	if !pi.detect() {
		t.Error("~/.pi/agent exists but pi was not detected")
	}
	path, err = pi.providerConfig(true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".pi", "agent", "models.json"); path != want {
		t.Errorf("providerConfig = %q, want %q", path, want)
	}
}

// The profile is only useful if codex can load it standalone, so it must parse
// and carry model / model_provider at the root. Dotted keys emitted in place,
// or root keys written after the [model_providers.orq] header, would both read
// as members of that table and leave codex on its own default provider.
func TestCodexProfileIsSelfContained(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orq.config.toml")
	if _, err := writeCodexProviderTOML(path, "https://api.orq.ai/v3/router", "sk-secret", openCodeModels(),
		"openai/gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	tree, err := toml.LoadBytes(mustRead(t, path))
	if err != nil {
		t.Fatalf("profile is not valid TOML: %v", err)
	}
	for key, want := range map[string]any{
		"model":                        "openai/gpt-5.4",
		"model_provider":               "orq",
		"model_providers.orq.name":     "Orq AI Gateway",
		"model_providers.orq.base_url": "https://api.orq.ai/v3/router",
		"model_providers.orq.env_key":  "ORQ_API_KEY",
		"model_providers.orq.wire_api": "responses",
	} {
		if got := tree.Get(key); got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}

// Codex takes the key from the environment, so nothing here should ever hold
// it. This is the whole reason the profile can be written to disk at all.
func TestCodexProfileNeverEmbedsTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orq.config.toml")
	if _, err := writeCodexProviderTOML(path, "https://api.orq.ai/v3/router", "sk-canary-do-not-write",
		[]auth.RouterModel{model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")},
		"anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustRead(t, path)), "sk-canary-do-not-write") {
		t.Error("the api key was written into the codex profile")
	}
}

// The profile exists so that plain `codex` keeps working exactly as it did.
// Writing into the base config instead would repoint codex globally at a
// provider whose credential setup cannot guarantee is exported.
func TestCodexProviderWriteLeavesBaseConfigAlone(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.toml")
	if err := writeMCPCodexTOML(base, testMCPURL); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, base)

	if _, err := writeCodexProviderTOML(filepath.Join(dir, "orq.config.toml"),
		"https://api.orq.ai/v3/router", "sk-k",
		[]auth.RouterModel{model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")},
		"anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, base)) != string(before) {
		t.Error("writing the orq profile modified codex's own config.toml")
	}
}

// Setup re-reads the catalogue on every run (a saved key is reused). The profile
// is ours, so it is replaced rather than appended to — appending would give the
// file duplicate keys, which is a hard TOML error and would take codex's whole
// profile down, not just the stale half.
func TestCodexProfileRewrittenOnRerun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orq.config.toml")
	catalogue := openCodeModels()
	catalogue = append(catalogue, model("azure", "gpt-5.4", 400000, true, true, "chat"))
	catalogue[len(catalogue)-1].Metadata.SupportsResponses = true
	for _, m := range []string{"openai/gpt-5.4", "azure/gpt-5.4"} {
		if _, err := writeCodexProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", catalogue, m); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := toml.LoadBytes(mustRead(t, path))
	if err != nil {
		t.Fatalf("profile is not valid TOML after a second run: %v", err)
	}
	if got := tree.Get("model"); got != "azure/gpt-5.4" {
		t.Errorf("model = %v after rerun, want the newer azure/gpt-5.4", got)
	}
}

// Codex can only speak the Responses API, and every request it sends carries
// OpenAI-shaped tool definitions including the built-in web_search type. Naming
// a model the gateway serves by translating to another vendor makes the
// upstream reject the tools outright — anthropic answers "tools.5: Input tag
// 'namespace' ... does not match any of the expected tags", the stream ends in
// response.failed, and codex reports only "ERROR: Reconnecting... 1/5". So the
// model this writer names is not the one every other agent gets.
func TestCodexDefaultModelMustSupportResponses(t *testing.T) {
	catalogue := []auth.RouterModel{
		model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat"), // chat only
		model("openai", "gpt-5.4", 400000, true, true, "chat"),
	}
	catalogue[1].Metadata.SupportsResponses = true

	// The globally proven default is anthropic, which codex cannot use.
	if got := codexDefaultModel(catalogue, "anthropic/claude-sonnet-4-6"); got != "openai/gpt-5.4" {
		t.Errorf("codex default = %q, want the Responses-capable openai/gpt-5.4", got)
	}
	// When the proven model does qualify, reuse it rather than second-guessing:
	// it is the one already shown to answer.
	if got := codexDefaultModel(catalogue, "openai/gpt-5.4"); got != "openai/gpt-5.4" {
		t.Errorf("codex default = %q, want the proven openai/gpt-5.4", got)
	}
	// Nothing Responses-capable enabled: name nothing rather than something
	// guaranteed to fail on the first prompt.
	if got := codexDefaultModel(catalogue[:1], "anthropic/claude-sonnet-4-6"); got != "" {
		t.Errorf("codex default = %q, want none when no model can serve Responses", got)
	}
}

// Size variants sort lexically *above* the model they are cut down from —
// gpt-5.4-nano > gpt-5.4-mini > gpt-5.4 — so ranking by id alone handed a
// coding agent the weakest member of the family. It is not only a quality
// question: nano rejects tools the full model accepts, and codex sends its
// whole tool set on the first request, so the session died with
// "[openai] Tool 'tool_search' is not supported with gpt-5.4-nano".
func TestCodexDefaultModelPrefersFullModelsOverSizeVariants(t *testing.T) {
	responses := func(refs ...string) []auth.RouterModel {
		out := make([]auth.RouterModel, 0, len(refs))
		for _, ref := range refs {
			provider, id, _ := strings.Cut(ref, "/")
			m := model(provider, id, 400000, true, true, "chat")
			m.Metadata.SupportsResponses = true
			out = append(out, m)
		}
		return out
	}

	// The workspace that broke: no gpt-5.6, so the gpt-5.4 family is all there
	// is, and -nano used to win it.
	got := codexDefaultModel(responses(
		"openai/gpt-5.4", "openai/gpt-5.4-mini", "openai/gpt-5.4-nano"), "")
	if got != "openai/gpt-5.4" {
		t.Errorf("codex default = %q, want the full openai/gpt-5.4", got)
	}

	// Version suffixes must still win, which is what the lexical rule was for.
	got = codexDefaultModel(responses("openai/gpt-5.4", "openai/gpt-5.6"), "")
	if got != "openai/gpt-5.6" {
		t.Errorf("codex default = %q, want the newer openai/gpt-5.6", got)
	}

	// Outside the preferred families the same rule applies.
	got = codexDefaultModel(responses("acme/model-1-mini", "acme/model-1"), "")
	if got != "acme/model-1" {
		t.Errorf("codex default = %q, want the full acme/model-1", got)
	}

	// Only variants available: the stronger edition, not the lexically
	// greatest. Plain full-vs-variant ranking still handed nano the win here —
	// the exact model whose tool rejection started all this.
	got = codexDefaultModel(responses("openai/gpt-5.4-mini", "openai/gpt-5.4-nano"), "")
	if got != "openai/gpt-5.4-mini" {
		t.Errorf("codex default = %q, want mini over nano", got)
	}

	// -flash is itself in the suffix list, so gemini's mainline flash must
	// still beat its own -lite edition rather than being demoted to a tie.
	got = codexDefaultModel(responses("google/gemini-2.5-flash-lite", "google/gemini-2.5-flash"), "")
	if got != "google/gemini-2.5-flash" {
		t.Errorf("codex default = %q, want flash over flash-lite", got)
	}
}

// The writer must apply that rule, not just the helper.
func TestCodexProfileNeverNamesAChatOnlyModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orq.config.toml")
	catalogue := []auth.RouterModel{
		model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat"),
		model("openai", "gpt-5.4", 400000, true, true, "chat"),
	}
	catalogue[1].Metadata.SupportsResponses = true

	if _, err := writeCodexProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k",
		catalogue, "anthropic/claude-sonnet-4-6"); err != nil {
		t.Fatal(err)
	}
	tree, err := toml.LoadBytes(mustRead(t, path))
	if err != nil {
		t.Fatalf("profile is not valid TOML: %v", err)
	}
	if got := tree.Get("model"); got != "openai/gpt-5.4" {
		t.Errorf("profile names %v, which codex cannot call over the Responses API", got)
	}
}

// Codex can only talk Responses, so a catalogue of chat-only models leaves
// nothing for it to open with. Writing model = "" would have codex fail to
// resolve a blank slug; omitting the key lets it fall back to its own default
// while still offering the provider.
//
// Reached with models present but none Responses-capable, not with an empty
// catalogue: that is refused outright now, because every writer clears its own
// keys first and would otherwise delete a working block.
func TestCodexProfileOmitsBlankModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orq.config.toml")
	chatOnly := []auth.RouterModel{model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")}
	if _, err := writeCodexProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", chatOnly, ""); err != nil {
		t.Fatal(err)
	}
	tree, err := toml.LoadBytes(mustRead(t, path))
	if err != nil {
		t.Fatalf("profile is not valid TOML: %v", err)
	}
	if tree.Has("model") {
		t.Errorf("model written despite none being proven: %v", tree.Get("model"))
	}
	if got := tree.Get("model_provider"); got != "orq" {
		t.Errorf("provider must still be registered, got %v", got)
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
		if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v2/router", "sk-test-key", models, ""); err != nil {
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
	if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v2/router", "sk-fresh-key", models, ""); err != nil {
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
	if n := strings.Count(got, `[models."moonshotai/kimi-k2.7-code-highspeed"]`); n != 1 {
		t.Errorf("model table appears %d times, want 1", n)
	}
	// Everything we do not own must survive byte for byte.
	for _, want := range []string{`default_model = "kimi-k2.7-code-highspeed"`, "[thinking]", "enabled = false"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost unrelated config %q\n---\n%s", want, got)
		}
	}
}

// kimi persists the user's model choice in default_model itself. Setup fills it
// when the file has none — otherwise a freshly wired agent opens on its own
// built-in model and the first prompt never reaches the gateway — but must
// never replace a value, because that value is a choice the user made.
func TestKimiDefaultModelFilledOnlyWhenAbsent(t *testing.T) {
	models := []auth.RouterModel{model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")}

	t.Run("absent: filled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if _, err := writeKimiProviderTOML(path, "https://x/v3/router", "sk-k", models, "anthropic/claude-sonnet-4-6"); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		if !strings.Contains(string(got), `default_model = "anthropic/claude-sonnet-4-6"`) {
			t.Errorf("default_model was not written:\n%s", got)
		}
	})

	t.Run("present: kept", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("default_model = \"my-own-pick\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := writeKimiProviderTOML(path, "https://x/v3/router", "sk-k", models, "anthropic/claude-sonnet-4-6"); err != nil {
			t.Fatal(err)
		}
		got := string(mustRead(t, path))
		if !strings.Contains(got, `default_model = "my-own-pick"`) {
			t.Errorf("the user's default_model was lost:\n%s", got)
		}
		if strings.Contains(got, `default_model = "anthropic/claude-sonnet-4-6"`) {
			t.Errorf("setup overwrote the user's default_model:\n%s", got)
		}
	})

	// A default_model inside another table is that table's setting, not kimi's
	// root config, so the root one must still be written.
	t.Run("scoped elsewhere: still filled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("[some.table]\ndefault_model = \"not-root\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := writeKimiProviderTOML(path, "https://x/v3/router", "sk-k", models, "anthropic/claude-sonnet-4-6"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(mustRead(t, path)), `default_model = "anthropic/claude-sonnet-4-6"`) {
			t.Error("a table-scoped default_model suppressed the root one")
		}
	})
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Duplicate table keys make the whole file invalid TOML, and kimi then reports
// "failed to decode config.toml" and configures NO models — not just the
// clashing ones. Several providers serve the same model_id
// (google/gemini-2.5-flash vs google-ai/gemini-2.5-flash, azure/gpt-4o vs
// openai/gpt-4o), so keying tables by anything less than the full ref collides
// the moment the catalogue is written wholesale.
func TestKimiProviderBlockHasNoDuplicateTables(t *testing.T) {
	models := []auth.RouterModel{
		model("google", "gemini-2.5-flash", 1000000, true, true, "chat"),
		model("google-ai", "gemini-2.5-flash", 1000000, true, true, "chat"),
		model("azure", "gpt-4o", 128000, true, true, "chat"),
		model("openai", "gpt-4o", 128000, true, true, "chat"),
	}
	block := kimiBlock("https://api.orq.ai/v3/router", "sk-k", models)

	seen := map[string]int{}
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "[") {
			seen[line]++
		}
	}
	for table, n := range seen {
		if n > 1 {
			t.Errorf("%s appears %d times — duplicate keys make the file undecodable\n---\n%s", table, n, block)
		}
	}
	if len(seen) != len(models)+1 { // one table per model, plus [providers.orq]
		t.Errorf("got %d tables for %d models, want %d", len(seen), len(models), len(models)+1)
	}
}

// kimi needs one provider per API shape. Writing every model as chat
// completions works but drops the Responses path, which is where gpt-5.x with
// tools and reasoning belongs — and `orq launch` splits them, so a config from
// setup would otherwise describe the same models differently.
func TestKimiProviderBlockSplitsByAPIShape(t *testing.T) {
	chat := model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")
	chat.Metadata.MaxOutputTokens = 64000
	responses := model("azure", "gpt-5.4", 1050000, true, true, "chat")
	responses.Metadata.MaxOutputTokens = 128000
	responses.Metadata.SupportsResponses = true

	block := kimiBlock("https://api.orq.ai/v3/router", "sk-k",
		[]auth.RouterModel{chat, responses})

	for _, want := range []string{
		`[providers.orq]`,
		`type = "openai"`,
		`[providers.orq-responses]`,
		`type = "openai_responses"`,
		"max_output_size = 64000",
		"max_output_size = 128000",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("missing %q\n---\n%s", want, block)
		}
	}
	if got := providerOfModel(t, block, "azure/gpt-5.4"); got != launch.KimiResponsesProvider {
		t.Errorf("azure/gpt-5.4 is Responses-capable but landed on %q", got)
	}
	if got := providerOfModel(t, block, "anthropic/claude-sonnet-4-6"); got != launch.KimiChatProvider {
		t.Errorf("a chat-only model landed on %q", got)
	}
}

// A model with no published caps must still get both fields: kimi requires
// them, and omitting one makes the config unusable.
func TestKimiProviderBlockDefaultsMissingOutputCap(t *testing.T) {
	block := kimiBlock("https://x/v3/router", "sk-k",
		[]auth.RouterModel{model("anthropic", "mystery", 0, true, true, "chat")})
	for _, want := range []string{"max_context_size = 128000", "max_output_size = 8192"} {
		if !strings.Contains(block, want) {
			t.Errorf("missing fallback %q\n---\n%s", want, block)
		}
	}
}

// providerOfModel reads a model's provider from its own table, since the
// [providers.*] headers all precede the [models.*] blocks.
func providerOfModel(t *testing.T, toml, ref string) string {
	t.Helper()
	key := `[models."` + ref + `"]`
	i := strings.Index(toml, key)
	if i < 0 {
		t.Fatalf("no table for %s\n---\n%s", ref, toml)
	}
	rest := toml[i+len(key):]
	line := rest[strings.Index(rest, "provider = ")+len("provider = "):]
	return strings.Trim(line[:strings.Index(line, "\n")], `"`)
}

// kimiBlock is the provider block exactly as writeKimiProviderTOML composes it,
// so these tests exercise the shared builder through setup's own call rather
// than through a second implementation of it.
func kimiBlock(routerURL, apiKey string, models []auth.RouterModel) string {
	refs, infos := launchCatalog(models)
	return launch.BuildKimiConfigTOML(routerURL, apiKey, "", refs, infos)
}

// usableCodingModels applies the filter both commands use, so the comparison
// starts from the same set rather than from each path's own idea of it.
func usableCodingModels(all []auth.RouterModel) []auth.RouterModel {
	out := []auth.RouterModel{}
	for _, m := range all {
		if m.Enabled && m.Type == "chat" && m.Functions {
			out = append(out, m)
		}
	}
	return out
}

// Re-running setup must be idempotent. The strip step has to recognise every
// table the write step emits: when the Responses provider was added to the
// writer and not to orqOwnedKimiTable, each run kept the stale
// [providers.orq-responses] block and every model pointing at it, then wrote
// them again. Three runs produced three provider blocks and 172 model tables
// for 128 models — duplicate keys, so kimi failed to decode the file and
// started with no models at all.
func TestWriteKimiProviderTOMLIsIdempotentAcrossBothProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[[hooks]]\ncommand = \"echo hi\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	chat := model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")
	chat.Metadata.MaxOutputTokens = 64000
	responses := model("azure", "gpt-5.4", 1050000, true, true, "chat")
	responses.Metadata.MaxOutputTokens = 128000
	responses.Metadata.SupportsResponses = true
	models := []auth.RouterModel{chat, responses}

	for i := 0; i < 3; i++ {
		if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", models, "anthropic/claude-sonnet-4-6"); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	got := string(mustRead(t, path))
	seen := map[string]int{}
	for _, line := range strings.Split(got, "\n") {
		// [[hooks]] is an array of tables and may legitimately repeat.
		if strings.HasPrefix(line, "[") && !strings.HasPrefix(line, "[[") {
			seen[line]++
		}
	}
	for table, n := range seen {
		if n > 1 {
			t.Errorf("%s appears %d times after 3 runs — duplicate keys make the file undecodable", table, n)
		}
	}
	if !strings.Contains(got, "[[hooks]]") {
		t.Error("unrelated user config was dropped")
	}
}

// mustParseKimiTOML fails the test unless the written config is valid TOML, and
// returns the parsed document so assertions can check structure rather than
// substrings.
//
// This exists because both kimi corruption bugs — duplicate model keys
// (74c16e8) and duplicate provider blocks (e042f8f) — shipped with a fully
// green suite. Every assertion was strings.Contains, which cannot see a
// duplicate key: the text is present, the file is still undecodable, and kimi
// responds by discarding every model rather than the clashing ones. Any new
// assertion about this file should go through here.
func mustParseKimiTOML(t *testing.T, config string) *toml.Tree {
	t.Helper()
	tree, err := toml.Load(config)
	if err != nil {
		t.Fatalf("kimi would refuse this config: %v\n---\n%s", err, config)
	}
	return tree
}

// The writer's output must be loadable by a TOML parser under the conditions
// that have actually broken it: models whose ids collide across providers, both
// API shapes present, and a re-run over a config we already wrote.
func TestKimiConfigIsAlwaysValidTOML(t *testing.T) {
	collide := []auth.RouterModel{
		model("google", "gemini-2.5-flash", 1000000, true, true, "chat"),
		model("google-ai", "gemini-2.5-flash", 1000000, true, true, "chat"),
		model("azure", "gpt-4o", 128000, true, true, "chat"),
		model("openai", "gpt-4o", 128000, true, true, "chat"),
	}
	collide[3].Metadata.SupportsResponses = true

	t.Run("fresh write", func(t *testing.T) {
		tree := mustParseKimiTOML(t, kimiBlock("https://api.orq.ai/v3/router", "sk-k", collide))
		models, _ := tree.Get("models").(*toml.Tree)
		if models == nil || len(models.Keys()) != len(collide) {
			t.Errorf("expected %d model tables, parsed %v", len(collide), models)
		}
	})

	t.Run("re-run over our own output", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		for i := 0; i < 3; i++ {
			if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", collide, "openai/gpt-4o"); err != nil {
				t.Fatalf("run %d: %v", i, err)
			}
			tree := mustParseKimiTOML(t, string(mustRead(t, path)))
			models, _ := tree.Get("models").(*toml.Tree)
			if models == nil || len(models.Keys()) != len(collide) {
				t.Fatalf("run %d: expected %d models, got %v", i, len(collide), models)
			}
			providers, _ := tree.Get("providers").(*toml.Tree)
			if providers == nil || len(providers.Keys()) != 2 {
				t.Fatalf("run %d: expected 2 providers, got %v", i, providers)
			}
		}
	})

	t.Run("alongside config we do not own", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		theirs := "default_model = \"their-pick\"\n\n[[hooks]]\ncommand = \"echo hi\"\n\n[thinking]\nenabled = false\n"
		if err := os.WriteFile(path, []byte(theirs), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", collide, "openai/gpt-4o"); err != nil {
			t.Fatal(err)
		}
		tree := mustParseKimiTOML(t, string(mustRead(t, path)))
		if got := tree.Get("default_model"); got != "their-pick" {
			t.Errorf("default_model = %v, want their-pick (theirs must survive)", got)
		}
		if tree.Get("thinking") == nil || tree.Get("hooks") == nil {
			t.Error("unrelated user config was lost")
		}
	})
}

// An empty catalogue reaches the writers in two ordinary situations: ListModels
// failed, or the workspace enables no chat model with function calling. Every
// writer clears the keys it owns before emitting new ones, so passing that
// through deleted a provider block an earlier run had written — and returned
// (0, nil), which the caller reported as "provider → orq gateway" followed by
// instructions to pick a model that no longer existed. Refusing is the only
// answer that cannot leave the agent worse off than it was found.
func TestEmptyCatalogueIsRefusedNotWrittenThrough(t *testing.T) {
	seeded := `{"provider":{"orq":{"name":"Orq AI Gateway","models":{"openai/gpt-5":{}}},"vendor":{"keep":true}},"model":"vendor/mine"}`

	for _, tc := range []struct {
		name  string
		seed  string
		write func(path string) (int, error)
	}{
		{"opencode", seeded, func(p string) (int, error) {
			return writeOpenCodeProviderJSON(p, "https://api.orq.ai/v3/router", "sk-k", nil, "")
		}},
		{"kimi", "[providers.orq]\ntype = \"openai\"\n", func(p string) (int, error) {
			return writeKimiProviderTOML(p, "https://api.orq.ai/v3/router", "sk-k", nil, "")
		}},
		{"codex", "", func(p string) (int, error) {
			return writeCodexProviderTOML(p, "https://api.orq.ai/v3/router", "sk-k", nil, "")
		}},
		{"pi", `{"providers":{"orq":{"models":[{"id":"openai/gpt-5"}]},"ollama":{"models":[]}}}`, func(p string) (int, error) {
			return writePiProviderJSON(p, "https://api.orq.ai/v3/router", "sk-k", nil, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			if tc.seed != "" {
				if err := os.WriteFile(path, []byte(tc.seed), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)

			if _, err := tc.write(path); !errors.Is(err, errNoModelsToOffer) {
				t.Fatalf("want errNoModelsToOffer, got %v", err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Errorf("file changed despite refusing to write:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// Codex resolves config.toml, profiles and everything else against CODEX_HOME.
// Detection looked only at ~/.codex, so a machine that moves it was never
// offered the agent whose profile the writers would then place correctly — and
// the header pointed at a ~/.codex/config.toml that does not exist there.
func TestCodexHonoursCodexHomeForDetectionAndHeader(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", dir)

	spec, ok := lookupAgent("codex")
	if !ok {
		t.Fatal("codex missing from the registry")
	}
	if spec.detect() {
		t.Error("detected codex before its directory exists")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !spec.detect() {
		t.Error("CODEX_HOME exists but codex was not detected")
	}

	path := filepath.Join(dir, "orq.config.toml")
	if _, err := writeCodexProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k",
		[]auth.RouterModel{model("openai", "gpt-5", 400000, true, true, "chat")}, ""); err != nil {
		t.Fatal(err)
	}
	body := string(mustRead(t, path))
	if want := filepath.Join(dir, "config.toml"); !strings.Contains(body, want) {
		t.Errorf("header does not name the resolved base config %q:\n%s", want, body)
	}
	if strings.Contains(body, "~/.codex/config.toml") {
		t.Error("header still hardcodes ~/.codex despite CODEX_HOME being set")
	}
}

// Every provider writer must ship with its read-side detector, in the same
// registry entry. Without one, doctor silently reports the agent unwired
// forever — the drift class the field exists to kill.
func TestProviderWritersPairWithDetectors(t *testing.T) {
	for _, spec := range agentRegistry() {
		if spec.writeProvider != nil && spec.providerPresent == nil {
			t.Errorf("%s has writeProvider but no providerPresent — doctor cannot see its wiring", spec.ID)
		}
	}
}

// Codex's detector must read the profile's content, not its existence: an
// empty or truncated file at the right path is not a wired agent.
func TestCodexProviderDetectorReadsContentNotExistence(t *testing.T) {
	codex, _ := lookupAgent("codex")
	path := filepath.Join(t.TempDir(), codexProfileName+".config.toml")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if codex.providerPresent(path) {
		t.Fatal("empty profile file read as wired")
	}
	if _, err := writeCodexProviderTOML(path, "https://api.orq.ai/v3/router", "sk-secret", openCodeModels(),
		"openai/gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	if !codex.providerPresent(path) {
		t.Fatal("profile the writer just produced read as unwired")
	}
}

// Doctor's coding-agent checks are the read-side of what the writers produce:
// wired = pass, detected-but-unwired = warn (an agent nobody wired is an
// offer, not a failure — the exit code must stay healthy), undetected = absent.
func TestCodingAgentChecksReadWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "no-codex")) // keep codex undetected
	t.Chdir(t.TempDir())                                    // no project-relative configs
	// Pinned, not inherited: a wired agent also warns when the shell exports no
	// ORQ_API_KEY, so leaving this to the environment made the test pass on a
	// developer's machine and fail on a clean one. The warning itself is
	// TestCodingAgentChecksWarnWhenTheShellHasNoKey's subject.
	t.Setenv("ORQ_API_KEY", "sk-orq-in-this-shell")

	// Nothing installed: no checks at all.
	if got := codingAgentChecks(); len(got) != 0 {
		t.Fatalf("no agents installed but got %d checks: %+v", len(got), got)
	}

	// Kimi installed but unwired → warn naming the wiring command.
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	checks := codingAgentChecks()
	if len(checks) != 1 || checks[0].ID != "coding_agent_kimi" {
		t.Fatalf("want one kimi check, got %+v", checks)
	}
	if checks[0].Status != "warn" || !strings.Contains(checks[0].Message, "orq setup coding-agents") {
		t.Fatalf("unwired agent should warn and name the fix, got %+v", checks[0])
	}

	// Wire both halves the way setup does, then it must pass.
	mcpPath := filepath.Join(home, ".kimi-code", "mcp.json")
	if err := writeMCPKimiJSON(mcpPath, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatal(err)
	}
	providerPath := filepath.Join(home, ".kimi-code", "config.toml")
	models := []auth.RouterModel{model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")}
	if _, err := writeKimiProviderTOML(providerPath, "https://api.orq.ai/v3/router", "sk-k", models, ""); err != nil {
		t.Fatal(err)
	}
	checks = codingAgentChecks()
	if len(checks) != 1 || checks[0].Status != "pass" {
		t.Fatalf("wired agent should pass, got %+v", checks)
	}

	// opencode registers MCP under "mcp", not "mcpServers" — the check must
	// read each agent's own format, which is the bug a kimi-only test missed.
	ocDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(ocDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ocPath := filepath.Join(ocDir, "opencode.json")
	if err := writeMCPRemoteJSON(ocPath, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatal(err)
	}
	if _, err := writeOpenCodeProviderJSON(ocPath, "https://api.orq.ai/v3/router", "sk-k", models, ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range codingAgentChecks() {
		if c.ID == "coding_agent_opencode" && c.Status != "pass" {
			t.Fatalf("opencode wired via its own 'mcp' key should pass, got %+v", c)
		}
	}
}

// Doctor cannot know which scope setup used — resolveScope picks the project
// directory when one looks like a project, $HOME otherwise, and --local /
// --global override both ways. Checking only the global path reported a
// correctly wired project-scoped agent as "detected but not wired", which is
// the one thing the check exists to get right.
func TestCodingAgentChecksFindProjectScopedWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "no-codex"))
	// Pinned for the same reason as in TestCodingAgentChecksReadWiring: the
	// scope question this test asks must not be answered by the runner's shell.
	t.Setenv("ORQ_API_KEY", "sk-orq-in-this-shell")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A project directory, wired the way `orq setup` does inside one: the
	// relative ./.mcp.json, never ~/.claude.json.
	t.Chdir(t.TempDir())
	claude, _ := lookupAgent("claude")
	projectPath, err := claude.mcpConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(projectPath) {
		t.Fatalf("expected a project-relative path, got %q", projectPath)
	}
	if err := writeMCPServersJSON(projectPath, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatal(err)
	}

	var claudeCheck *doctorCheck
	checks := codingAgentChecks()
	for i := range checks {
		if checks[i].ID == "coding_agent_claude" {
			claudeCheck = &checks[i]
		}
	}
	if claudeCheck == nil {
		t.Fatal("claude is installed and wired but produced no check")
	}
	if claudeCheck.Status != "pass" {
		t.Errorf("project-scoped wiring reported as %q: %s", claudeCheck.Status, claudeCheck.Message)
	}
	if got := claudeCheck.Details["mcp"]; got != projectPath {
		t.Errorf("check names %v, want the project path %q", got, projectPath)
	}
}

// A wired agent whose shell has no ORQ_API_KEY authenticates with an empty
// bearer and 401s on every call — indistinguishable from a broken install from
// inside the agent, while every file check passes. This is the state a user is
// in whenever they left a terminal open across setup, and it is the one thing
// doctor exists to name.
func TestDoctorWarnsWhenAWiredAgentHasNoKeyInTheEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "no-codex")) // keep codex undetected
	chdir(t, t.TempDir())

	// kimi, installed and fully wired.
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeMCPKimiJSON(filepath.Join(home, ".kimi-code", "mcp.json"), "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatal(err)
	}
	if _, err := writeKimiProviderTOML(filepath.Join(home, ".kimi-code", "config.toml"),
		"https://api.orq.ai/v3/router", "sk-key", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}

	find := func() doctorCheck {
		t.Helper()
		for _, c := range codingAgentChecks() {
			if c.ID == "coding_agent_kimi" {
				return c
			}
		}
		t.Fatal("no kimi check")
		return doctorCheck{}
	}

	t.Setenv("ORQ_API_KEY", "")
	stale := find()
	if stale.Status != "warn" {
		t.Errorf("wired agent with no key in the environment: status = %q, want warn", stale.Status)
	}
	if !strings.Contains(stale.Message, "ORQ_API_KEY") {
		t.Errorf("warning does not name the variable: %q", stale.Message)
	}
	if stale.Details["api_key_in_env"] != false {
		t.Errorf("details should record the missing key: %+v", stale.Details)
	}

	// The same machine, from a shell that has it.
	t.Setenv("ORQ_API_KEY", "sk-orq-live")
	if ok := find(); ok.Status != "pass" {
		t.Errorf("wired agent with the key exported: status = %q, want pass (%s)", ok.Status, ok.Message)
	}

	// A CLI-only credential is not what an agent config interpolates.
	t.Setenv("ORQ_API_KEY", "")
	t.Setenv("ORQ_TOKEN", "sk-orq-cli-only")
	if got := find(); got.Status != "warn" {
		t.Errorf("ORQ_TOKEN is not the variable agents read: status = %q, want warn", got.Status)
	}
}

// Defensive backstop for the writers' empty-defaultModel contract: no live
// path yields one today (writers are skipped on an empty catalogue and
// defaultCodingModel otherwise always returns a model), but codex omits its
// model key on empty (TestCodexProfileOmitsBlankModel) and kimi must likewise
// leave default_model out rather than write an empty one, which kimi would
// read as a model named "".
func TestKimiProfileOmitsBlankDefaultModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-key", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "default_model") {
		t.Errorf("wrote a default_model with nothing to put in it:\n%s", data)
	}

	// With a default model it must appear, or the omission above proves nothing.
	if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-key",
		openCodeModels(), "openai/gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), `default_model = "openai/gpt-5.4"`) {
		t.Errorf("default model missing:\n%s", data)
	}
}
