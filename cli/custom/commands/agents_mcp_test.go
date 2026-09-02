package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orq/cli/custom/auth"
	"orq/cli/custom/launch"
)

// credentialSubstrings are the strings section 2 forbids anywhere in a
// written MCP entry: no config may carry a header, a bearer variable name, or
// (by extension) a literal key.
var credentialSubstrings = []string{"Authorization", "headers", "bearer_token_env_var", "bearerTokenEnvVar"}

func assertNoCredential(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	for _, needle := range credentialSubstrings {
		if strings.Contains(string(data), needle) {
			t.Fatalf("%s contains forbidden credential marker %q:\n%s", path, needle, data)
		}
	}
}

// mcpAgents lists every registry entry with MCP support, for the tests that
// must hold for all of them (the credential canary chief among them).
func mcpAgents(t *testing.T) []agentSpec {
	t.Helper()
	var out []agentSpec
	for _, spec := range agentRegistry() {
		if spec.writeMCP != nil {
			out = append(out, spec)
		}
	}
	return out
}

func TestMCPRegistryShape(t *testing.T) {
	for _, spec := range agentRegistry() {
		wired := spec.writeMCP != nil
		if wired != (spec.mcpPresent != nil) {
			t.Errorf("%s: writeMCP and mcpPresent must be set together", spec.ID)
		}
		if wired != (spec.removeMCP != nil) {
			t.Errorf("%s: writeMCP and removeMCP must be set together", spec.ID)
		}
		if wired != (spec.mcpConfig != nil) {
			t.Errorf("%s: writeMCP and mcpConfig must be set together", spec.ID)
		}
	}
	pi, ok := lookupAgent("pi")
	if !ok {
		t.Fatal("pi not in registry")
	}
	if pi.mcpConfig != nil || pi.writeMCP != nil || pi.mcpPresent != nil || pi.removeMCP != nil {
		t.Error("pi has no MCP support and must leave all four fields nil")
	}
}

func TestMCPCredentialCanary(t *testing.T) {
	for _, spec := range mcpAgents(t) {
		t.Run(spec.ID, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", "")
			chdirTemp(t)

			path, err := spec.mcpConfig(true)
			if err != nil {
				t.Fatalf("resolving global path: %v", err)
			}
			if err := spec.writeMCP(path, "https://api.orq.ai/v2/mcp"); err != nil {
				t.Fatalf("writeMCP: %v", err)
			}
			assertNoCredential(t, path)
		})
	}
}

// TestMCPRegistryRoundTrip drives each MCP-capable registry row through its
// own trio — spec.writeMCP, spec.mcpPresent, spec.removeMCP — rather than
// calling the underlying helpers (jsonProviderPresentAt, removeJSONKeys, …)
// directly with hand-written keys. The format-level tests elsewhere in this
// file cover the shapes; this one is the guard against a registry row whose
// writer, reader, and remover disagree with each other — e.g. a writeMCP
// that writes "mcpServers" paired with a removeMCP still pointed at "mcp".
// Such a row would satisfy every other test here and still leave a dangling
// entry behind on every 'orq disconnect'.
func TestMCPRegistryRoundTrip(t *testing.T) {
	for _, spec := range mcpAgents(t) {
		t.Run(spec.ID, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", "")
			chdirTemp(t)

			path, err := spec.mcpConfig(true)
			if err != nil {
				t.Fatalf("resolving global path: %v", err)
			}

			if spec.mcpPresent(path) {
				t.Fatalf("%s: mcpPresent true before any write", spec.ID)
			}

			if err := spec.writeMCP(path, "https://api.orq.ai/v2/mcp"); err != nil {
				t.Fatalf("%s: writeMCP: %v", spec.ID, err)
			}
			if !spec.mcpPresent(path) {
				t.Fatalf("%s: writeMCP → mcpPresent(%s) is false; writer and reader disagree on key/format", spec.ID, path)
			}

			removed, err := spec.removeMCP(path)
			if err != nil {
				t.Fatalf("%s: removeMCP: %v", spec.ID, err)
			}
			if !removed {
				t.Fatalf("%s: removeMCP reported nothing removed, but mcpPresent said it was there", spec.ID)
			}
			if spec.mcpPresent(path) {
				t.Fatalf("%s: still mcpPresent(%s) after removeMCP; writer/remover disagree on key/format", spec.ID, path)
			}
		})
	}
}

// chdirTemp points cwd at a scratch directory for the length of the test, so
// project-scoped resolvers (claude, kimi) do not touch the real repo. It
// returns the directory as os.Getwd() will report it back — on macOS that is
// the /private-prefixed, symlink-resolved form, which matters when a test
// compares a resolver's output against this return value.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	resolved, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd after chdir: %v", err)
	}
	return resolved
}

func TestWriteMCPJSON_ClaudeRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")
	write := writeMCPJSON("mcpServers", claudeMCPEntry)
	present := jsonProviderPresentAt("mcpServers", launch.MCPServerName)

	if present(path) {
		t.Fatal("present before any write")
	}
	if err := write(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !present(path) {
		t.Fatal("not present after write")
	}
	cfg := readBack(t, path)
	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing or wrong type: %#v", cfg)
	}
	entry, ok := servers[launch.MCPServerName].(map[string]any)
	if !ok {
		t.Fatalf("%s entry missing or wrong type: %#v", launch.MCPServerName, servers)
	}
	if entry["type"] != "http" || entry["url"] != "https://api.orq.ai/v2/mcp" {
		t.Fatalf("unexpected entry shape: %#v", entry)
	}

	removed, err := removeJSONKeys(path, "mcpServers", launch.MCPServerName)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Fatal("remove reported nothing removed")
	}
	if present(path) {
		t.Fatal("still present after remove")
	}
}

func TestWriteMCPJSON_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	write := writeMCPJSON("mcp", remoteMCPEntry)

	if err := write(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading after first write: %v", err)
	}
	if err := write(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading after second write: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("second write changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	cfg := readBack(t, path)
	servers := cfg["mcp"].(map[string]any)
	if len(servers) != 1 {
		t.Fatalf("expected exactly one entry, got %d: %#v", len(servers), servers)
	}
}

func TestWriteMCPJSON_ForeignContentPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	if err := writeJSONConfig(path, map[string]any{
		"theme": "dark",
		"mcp": map[string]any{
			"some-other-server": map[string]any{"type": "remote", "url": "https://example.com/mcp"},
		},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	write := writeMCPJSON("mcp", remoteMCPEntry)
	if err := write(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := readBack(t, path)
	if cfg["theme"] != "dark" {
		t.Fatalf("unrelated top-level key lost: %#v", cfg)
	}
	servers := cfg["mcp"].(map[string]any)
	if _, ok := servers["some-other-server"]; !ok {
		t.Fatalf("unrelated MCP server lost: %#v", servers)
	}
	if _, ok := servers[launch.MCPServerName]; !ok {
		t.Fatalf("orq-workspace entry missing: %#v", servers)
	}

	removed, err := removeJSONKeys(path, "mcp", launch.MCPServerName)
	if err != nil || !removed {
		t.Fatalf("remove: removed=%v err=%v", removed, err)
	}
	cfg = readBack(t, path)
	if cfg["theme"] != "dark" {
		t.Fatalf("unrelated top-level key lost after remove: %#v", cfg)
	}
	servers = cfg["mcp"].(map[string]any)
	if _, ok := servers["some-other-server"]; !ok {
		t.Fatalf("unrelated MCP server lost after remove: %#v", servers)
	}
	if _, ok := servers[launch.MCPServerName]; ok {
		t.Fatalf("orq-workspace entry survived remove: %#v", servers)
	}
}

// TestWriteMCPJSON_UpgradesLegacyEntry is the regression e44c747 broke:
// a v4.13.10-era entry carrying a bearer header must be replaced wholesale,
// not merged into, on the next write.
func TestWriteMCPJSON_UpgradesLegacyEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := writeJSONConfig(path, map[string]any{
		"mcpServers": map[string]any{
			launch.MCPServerName: map[string]any{
				"type":    "http",
				"url":     "https://api.orq.ai/v2/mcp",
				"headers": map[string]any{"Authorization": "Bearer sk-live-leaked-key"},
			},
		},
	}); err != nil {
		t.Fatalf("seeding legacy entry: %v", err)
	}

	write := writeMCPJSON("mcpServers", claudeMCPEntry)
	if err := write(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("write: %v", err)
	}
	assertNoCredential(t, path)
	cfg := readBack(t, path)
	entry := cfg["mcpServers"].(map[string]any)[launch.MCPServerName].(map[string]any)
	if _, present := entry["headers"]; present {
		t.Fatalf("legacy headers survived the upgrade: %#v", entry)
	}
	if len(entry) != 2 || entry["type"] != "http" || entry["url"] != "https://api.orq.ai/v2/mcp" {
		t.Fatalf("upgraded entry is not the headerless shape: %#v", entry)
	}
}

func TestMCPProjectScopeFileMode(t *testing.T) {
	dir := chdirTemp(t)
	claude, ok := lookupAgent("claude")
	if !ok {
		t.Fatal("claude not in registry")
	}
	path, err := claude.mcpConfig(false)
	if err != nil {
		t.Fatalf("resolving project path: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("project path %s not under cwd %s", path, dir)
	}
	if err := claude.writeMCP(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("project .mcp.json mode = %o, want 0644 (matching what Claude itself creates)", info.Mode().Perm())
	}
}

// The mode has to survive the removal too: a committed .mcp.json that holds
// other servers stays on disk, and narrowing it to 0600 on every disconnect
// would fight the team it is shared with.
func TestMCPProjectScopeFileModeSurvivesRemoval(t *testing.T) {
	chdirTemp(t)
	claude, ok := lookupAgent("claude")
	if !ok {
		t.Fatal("claude not in registry")
	}
	path, err := claude.mcpConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	// A foreign entry, so the file survives the removal rather than being deleted.
	if err := writeJSONConfigMode(path, map[string]any{
		"mcpServers": map[string]any{"someone-else": map[string]any{"type": "http", "url": "https://example.com/mcp"}},
	}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := claude.writeMCP(path, launch.DefaultMCPURL); err != nil {
		t.Fatal(err)
	}
	if _, err := claude.removeMCP(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the foreign entry should have kept the file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode after removal = %o, want 0644", info.Mode().Perm())
	}
}

// readJSONConfig promises the file is "left untouched" when it cannot be
// merged into. ~/.claude.json is the agent's entire user state and is corrupted
// in the wild, so that promise is the one that matters most here.
func TestMCPWriteRefusesAnUnmergeableConfig(t *testing.T) {
	cases := map[string]string{
		"truncated":                `{"mcpServers": {"a":`,
		"not an object":            `[1, 2, 3]`,
		"section is not an object": `{"mcpServers": 5, "mcp": 5}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			for _, spec := range mcpAgents(t) {
				if spec.ID == "codex" {
					// codex's writer strips and rewrites TOML rather than
					// merging into a parsed document; writeTOMLConfig covers
					// the equivalent refusal for it.
					continue
				}
				home := t.TempDir()
				t.Setenv("HOME", home)
				chdirTemp(t)
				path, err := spec.mcpConfig(true)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := spec.writeMCP(path, launch.DefaultMCPURL); err == nil {
					t.Errorf("%s: writeMCP accepted %s config", spec.ID, name)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%s: %v", spec.ID, err)
				}
				if string(after) != body {
					t.Errorf("%s: refused write still changed the file:\n%s", spec.ID, after)
				}
			}
		})
	}
}

// The same promise on the TOML side: a rewrite that would not parse is refused
// rather than leaving codex with a config it cannot load.
func TestTOMLWriteRefusesToProduceInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "[model_providers.orq]\nname = \"Orq\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeTOMLConfig(path, "[unterminated\n"); err == nil {
		t.Fatal("invalid TOML was written")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatalf("the refused write still changed the file:\n%s", after)
	}
}

func TestMCPGlobalScopeFileModeUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claude, ok := lookupAgent("claude")
	if !ok {
		t.Fatal("claude not in registry")
	}
	path, err := claude.mcpConfig(true)
	if err != nil {
		t.Fatalf("resolving global path: %v", err)
	}
	if err := claude.writeMCP(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("~/.claude.json mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestMCPTwoScopeAgentsResolveBothPaths(t *testing.T) {
	for _, id := range []string{"claude", "kimi"} {
		t.Run(id, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := chdirTemp(t)

			spec, ok := lookupAgent(id)
			if !ok {
				t.Fatalf("%s not in registry", id)
			}
			project, err := spec.mcpConfig(false)
			if err != nil {
				t.Fatalf("project resolve: %v", err)
			}
			global, err := spec.mcpConfig(true)
			if err != nil {
				t.Fatalf("global resolve: %v", err)
			}
			if project == global {
				t.Fatalf("%s: project and global paths must differ (project=%s global=%s)", id, project, global)
			}
			if !strings.HasPrefix(project, dir) {
				t.Fatalf("%s: project path %s not under cwd %s", id, project, dir)
			}
			if !strings.HasPrefix(global, home) {
				t.Fatalf("%s: global path %s not under HOME %s", id, global, home)
			}
		})
	}
}

func TestKiloMCPPresentAcceptsJSONC(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "kilo.json")
	jsoncPath := filepath.Join(dir, "kilo.jsonc")

	if kiloMCPPresent(jsonPath) {
		t.Fatal("present with neither file written")
	}

	// Written directly against the .jsonc variant, as the shipped kilo binary
	// itself may: the reader must not require the .json form to exist.
	if err := writeJSONConfig(jsoncPath, map[string]any{
		"mcp": map[string]any{launch.MCPServerName: remoteMCPEntry("https://api.orq.ai/v2/mcp")},
	}); err != nil {
		t.Fatalf("seeding kilo.jsonc: %v", err)
	}
	if !kiloMCPPresent(jsonPath) {
		t.Fatal("kiloMCPPresent(kilo.json) did not fall back to kilo.jsonc")
	}
}

// The remover has to reach every file the reader accepts, or an entry made
// directly against kilo.jsonc reports as wired forever and never comes out.
func TestKiloRemoveMCPReachesJSONC(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "kilo.json")
	jsoncPath := filepath.Join(dir, "kilo.jsonc")

	if err := writeJSONConfig(jsoncPath, map[string]any{
		"mcp": map[string]any{launch.MCPServerName: remoteMCPEntry("https://api.orq.ai/v2/mcp")},
	}); err != nil {
		t.Fatalf("seeding kilo.jsonc: %v", err)
	}
	removed, err := kiloRemoveMCP(jsonPath)
	if err != nil {
		t.Fatalf("kiloRemoveMCP: %v", err)
	}
	if !removed {
		t.Fatal("kiloRemoveMCP reported nothing removed for an entry in kilo.jsonc")
	}
	if kiloMCPPresent(jsonPath) {
		t.Fatal("entry still present after removal")
	}
}

// A kilo.jsonc carrying comments does not parse as JSON, and the reader cannot
// see an entry in it — reporting that as a disconnect failure would be a lie.
func TestKiloRemoveMCPIgnoresUnparseableJSONC(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "kilo.json")
	if err := os.WriteFile(filepath.Join(dir, "kilo.jsonc"), []byte("// a comment\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONConfig(jsonPath, map[string]any{
		"mcp": map[string]any{launch.MCPServerName: remoteMCPEntry("https://api.orq.ai/v2/mcp")},
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := kiloRemoveMCP(jsonPath)
	if err != nil {
		t.Fatalf("an unreadable kilo.jsonc must not fail the removal: %v", err)
	}
	if !removed {
		t.Fatal("the kilo.json entry was not removed")
	}
}

func TestKiloMCPWriteGoesToJSON(t *testing.T) {
	kilo, ok := lookupAgent("kilo")
	if !ok {
		t.Fatal("kilo not in registry")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := kilo.mcpConfig(true)
	if err != nil {
		t.Fatalf("resolving path: %v", err)
	}
	if filepath.Base(path) != "kilo.json" {
		t.Fatalf("kilo mcpConfig resolves to %s, want kilo.json", filepath.Base(path))
	}
	if err := kilo.writeMCP(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("kilo.json not written: %v", err)
	}
}

// --- codex TOML ---

func TestCodexMCPRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	present := tomlTablePresent("mcp_servers." + launch.MCPServerName)

	if present(path) {
		t.Fatal("present before any write")
	}
	if err := writeCodexMCPTOML(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !present(path) {
		t.Fatal("not present after write")
	}
	assertNoCredential(t, path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if !strings.Contains(string(data), `[mcp_servers.`+launch.MCPServerName+`]`) ||
		!strings.Contains(string(data), `url = "https://api.orq.ai/v2/mcp"`) {
		t.Fatalf("unexpected config.toml content:\n%s", data)
	}

	removed, err := removeTOMLTables(path, codexOwnedMCPTable)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Fatal("remove reported nothing removed")
	}
	if present(path) {
		t.Fatal("still present after remove")
	}
}

func TestCodexMCPIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := writeCodexMCPTOML(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading after first write: %v", err)
	}
	if err := writeCodexMCPTOML(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading after second write: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("second write changed the file:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if strings.Count(string(second), "[mcp_servers."+launch.MCPServerName+"]") != 1 {
		t.Fatalf("expected exactly one orq-workspace table:\n%s", second)
	}
}

func TestCodexMCPPreservesForeignTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	seed := "[mcp_servers.some-other-server]\nurl = \"https://example.com/mcp\"\n\n" +
		"[sandbox]\nmode = \"workspace-write\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if err := writeCodexMCPTOML(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if !strings.Contains(string(data), "[mcp_servers.some-other-server]") ||
		!strings.Contains(string(data), "[sandbox]") {
		t.Fatalf("foreign tables lost:\n%s", data)
	}

	removed, err := removeTOMLTables(path, codexOwnedMCPTable)
	if err != nil || !removed {
		t.Fatalf("remove: removed=%v err=%v", removed, err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading after remove: %v", err)
	}
	if !strings.Contains(string(data), "[mcp_servers.some-other-server]") ||
		!strings.Contains(string(data), "[sandbox]") {
		t.Fatalf("foreign tables lost after remove:\n%s", data)
	}
	if strings.Contains(string(data), launch.MCPServerName) {
		t.Fatalf("orq-workspace table survived remove:\n%s", data)
	}
}

// TestCodexOwnedMCPTableAcceptsQuotedHeader is legal TOML that a naive
// string comparison misses: [mcp_servers."orq-workspace"] names the same
// table as [mcp_servers.orq-workspace]. Failing to claim it means the next
// write appends a second table with the same effective name, which codex
// cannot parse at all — data loss, not a cosmetic mismatch.
func TestCodexOwnedMCPTableAcceptsQuotedHeader(t *testing.T) {
	quoted := `[mcp_servers."` + launch.MCPServerName + `"]`
	if !codexOwnedMCPTable(quoted) {
		t.Fatalf("codexOwnedMCPTable did not claim the quoted header %q", quoted)
	}
	spaced := `[ mcp_servers . "` + launch.MCPServerName + `" ]`
	if !codexOwnedMCPTable(spaced) {
		t.Fatalf("codexOwnedMCPTable did not claim the spaced/quoted header %q", spaced)
	}
	// A different server must still be left alone.
	if codexOwnedMCPTable(`[mcp_servers."some-other-server"]`) {
		t.Fatal("codexOwnedMCPTable claimed an unrelated quoted table")
	}
}

// TestCodexMCPRewriteClaimsQuotedLegacyEntry is the end-to-end version: a
// hand-written or third-party-written quoted header on disk must be claimed
// (not duplicated) by the next writeCodexMCPTOML call.
func TestCodexMCPRewriteClaimsQuotedLegacyEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	seed := `[mcp_servers."` + launch.MCPServerName + `"]` + "\n" +
		`url = "https://stale.example/v2/mcp"` + "\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if err := writeCodexMCPTOML(path, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if strings.Count(string(data), "mcp_servers") != 1 {
		t.Fatalf("quoted legacy table was duplicated rather than claimed:\n%s", data)
	}
	if strings.Contains(string(data), "stale.example") {
		t.Fatalf("stale URL survived the rewrite:\n%s", data)
	}
	if !tomlTablePresent("mcp_servers." + launch.MCPServerName)(path) {
		t.Fatalf("rewritten table does not parse as mcp_servers.%s:\n%s", launch.MCPServerName, data)
	}
}

// TestCodexMCPLandsInBaseConfigNotProfile is the exact scenario the brief
// calls out: MCP is not profile-scoped, so it must live in the base
// config.toml codex always reads, in a file writeCodexProviderTOML never
// touches — and it must survive a subsequent provider write untouched.
func TestCodexMCPLandsInBaseConfigNotProfile(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "config.toml")
	profilePath := filepath.Join(dir, codexProfileName+".config.toml")

	if err := writeCodexMCPTOML(basePath, "https://api.orq.ai/v2/mcp"); err != nil {
		t.Fatalf("writing MCP: %v", err)
	}
	if _, err := os.Stat(profilePath); err == nil {
		t.Fatal("MCP write must not create the profile file")
	}

	// writeCodexProviderTOML rewrites the profile file wholesale; it must not
	// touch the base config.toml where the MCP table lives.
	models := []auth.RouterModel{model("anthropic", "claude-sonnet-4", 200000, true, true, "chat")}
	models[0].Metadata.SupportsResponses = true
	if _, err := writeCodexProviderTOML(profilePath, "https://router.example/v1", "", models, ""); err != nil {
		t.Fatalf("writeCodexProviderTOML: %v", err)
	}

	present := tomlTablePresent("mcp_servers." + launch.MCPServerName)
	if !present(basePath) {
		t.Fatal("MCP table lost from base config.toml after a provider write to the profile file")
	}
	data, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("reading base config.toml: %v", err)
	}
	if strings.Contains(string(data), "model_providers") {
		t.Fatalf("provider write leaked into the base config.toml:\n%s", data)
	}
}

func TestProjectScopeForCodexOpencodeAndKilo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	project := t.TempDir()
	t.Chdir(project)
	want := map[string][2]string{
		"codex":    {filepath.Join(project, ".codex", "config.toml"), filepath.Join(home, ".codex", "config.toml")},
		"opencode": {filepath.Join(project, "opencode.json"), filepath.Join(home, ".config", "opencode", "opencode.json")},
		"kilo":     {filepath.Join(project, "kilo.json"), filepath.Join(home, ".config", "kilo", "kilo.json")},
	}
	for id, paths := range want {
		spec, _ := lookupAgent(id)
		local, _ := spec.mcpConfig(false)
		global, _ := spec.mcpConfig(true)
		if local != paths[0] || global != paths[1] {
			t.Errorf("%s: local=%q global=%q; want %q, %q", id, local, global, paths[0], paths[1])
		}
		if !mcpScopeAware(spec) {
			t.Errorf("%s is not scope-aware", id)
		}
		if err := spec.writeMCP(local, "https://example.test/mcp"); err != nil {
			t.Fatalf("%s: write: %v", id, err)
		}
		if !spec.mcpPresent(local) {
			t.Errorf("%s: entry not present after write", id)
		}
		if _, err := os.Stat(global); !os.IsNotExist(err) {
			t.Errorf("%s: project write touched the global file", id)
		}
		assertNoCredential(t, local)
	}
}

func TestCodexProjectScopeHonoursCodexHomeForGlobalOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	project := t.TempDir()
	t.Chdir(project)
	spec, _ := lookupAgent("codex")
	global, _ := spec.mcpConfig(true)
	local, _ := spec.mcpConfig(false)
	if global != filepath.Join(codexHome, "config.toml") {
		t.Errorf("global = %q", global)
	}
	if local != filepath.Join(project, ".codex", "config.toml") {
		t.Errorf("local = %q", local)
	}
}
