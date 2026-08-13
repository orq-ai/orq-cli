package commands

import (
	"encoding/json"
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

func TestWriteKimiProviderTOMLAppendsOnceAndPreserves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[providers.moonshot]\ntype = \"kimi\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	models := []auth.RouterModel{model("anthropic", "claude-sonnet-4-6", 200000, true, true, "chat")}

	for i := 0; i < 3; i++ {
		if err := writeKimiProviderTOML(path, "https://api.orq.ai/v2/router", "sk-test-key", models, ""); err != nil {
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
	if err := writeKimiProviderTOML(path, "https://api.orq.ai/v2/router", "sk-fresh-key", models, ""); err != nil {
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
		if err := writeKimiProviderTOML(path, "https://x/v3/router", "sk-k", models, "anthropic/claude-sonnet-4-6"); err != nil {
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
		if err := writeKimiProviderTOML(path, "https://x/v3/router", "sk-k", models, "anthropic/claude-sonnet-4-6"); err != nil {
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
		if err := writeKimiProviderTOML(path, "https://x/v3/router", "sk-k", models, "anthropic/claude-sonnet-4-6"); err != nil {
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
		if err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", models, "anthropic/claude-sonnet-4-6"); err != nil {
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
			if err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", collide, "openai/gpt-4o"); err != nil {
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
		if err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k", collide, "openai/gpt-4o"); err != nil {
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
