package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orq/cli/custom/auth"
)

// chdir moves into dir for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
}

// The scope default decides whether setup writes .env and .mcp.json into the
// working directory. Getting it wrong drops a live key into $HOME.
func TestLooksLikeProject(t *testing.T) {
	cases := []struct {
		marker string
		want   bool
	}{
		{".git", true},
		{"package.json", true},
		{"pyproject.toml", true},
		{"go.mod", true},
		{"", false},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		if tc.marker != "" {
			if err := os.WriteFile(filepath.Join(dir, tc.marker), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		chdir(t, dir)
		if got := looksLikeProject(); got != tc.want {
			t.Errorf("marker %q: looksLikeProject() = %v, want %v", tc.marker, got, tc.want)
		}
	}
}

func TestAppendEnvKeyWritesOnce(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	rep := newReporter(true)

	const token = "sk-orq-abc123-secret"
	for i := 0; i < 3; i++ {
		if err := appendEnvKey(rep, token); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	data, err := os.ReadFile(".env")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "ORQ_API_KEY="); n != 1 {
		t.Errorf("ORQ_API_KEY written %d times, want 1", n)
	}
}

// An existing .env belongs to the user; setup must not rewrite or reorder it.
func TestAppendEnvKeyPreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := os.WriteFile(".env", []byte("DATABASE_URL=postgres://localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := appendEnvKey(newReporter(true), "sk-orq-token"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(".env")
	if !strings.Contains(string(data), "DATABASE_URL=postgres://localhost") {
		t.Error("pre-existing .env content was lost")
	}
	if !strings.Contains(string(data), "ORQ_API_KEY=sk-orq-token") {
		t.Error("key was not appended")
	}
}

// A user-supplied key must never be replaced by ours.
func TestAppendEnvKeyLeavesExistingKeyAlone(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := os.WriteFile(".env", []byte("ORQ_API_KEY=sk-orq-users-own-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := appendEnvKey(newReporter(true), "sk-orq-newly-minted"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(".env")
	if strings.Contains(string(data), "sk-orq-newly-minted") {
		t.Error("overwrote a key the user already had")
	}
}

func TestAppendEnvKeyFilePermissions(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := appendEnvKey(newReporter(true), "sk-orq-token"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(".env")
	if err != nil {
		t.Fatal(err)
	}
	// The file holds a live credential; group/other must not be able to read it.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf(".env mode = %o, want no group/other access", perm)
	}
}

func TestEnvIsGitIgnored(t *testing.T) {
	cases := []struct {
		gitignore string
		want      bool
	}{
		{".env\n", true},
		{"/.env\n", true},
		{".env*\n", true},
		{"node_modules\n.env\n", true},
		{"node_modules\n", false},
		{"", false},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		chdir(t, dir)
		if tc.gitignore != "" {
			if err := os.WriteFile(".gitignore", []byte(tc.gitignore), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := envIsGitIgnored(); got != tc.want {
			t.Errorf("gitignore %q: got %v, want %v", tc.gitignore, got, tc.want)
		}
	}
}

// Tokens are echoed to the terminal and into scrollback; only a prefix should
// ever appear.
func TestMaskToken(t *testing.T) {
	const token = "sk-orq-abcdefghijklmnop-secretpart"
	masked := maskToken(token)
	if strings.Contains(masked, "secretpart") {
		t.Errorf("masked token still contains the secret: %q", masked)
	}
	if len(masked) > 16 {
		t.Errorf("masked token is too long: %q", masked)
	}
	if short := maskToken("sk-orq-tiny"); strings.Contains(short, "tiny") {
		t.Errorf("short token was not masked: %q", short)
	}
}

// Agents without their own skills directory share .agents/skills, and the
// caller relies on the bool to avoid installing it once per agent.
func TestSkillsDestinationSharedVsAgentSpecific(t *testing.T) {
	claude, ok := lookupAgent("claude")
	if !ok {
		t.Fatal("claude missing from the registry")
	}
	dest, shared, err := skillsDestination(claude, false)
	if err != nil {
		t.Fatal(err)
	}
	if shared {
		t.Error("claude should use its own skills directory")
	}
	if dest != ".claude/skills" {
		t.Errorf("dest = %q", dest)
	}

	pi, ok := lookupAgent("pi")
	if !ok {
		t.Fatal("pi missing from the registry")
	}
	dest, shared, err = skillsDestination(pi, false)
	if err != nil {
		t.Fatal(err)
	}
	if !shared {
		t.Error("pi should use the shared skills directory")
	}
	if dest != sharedSkillsDir {
		t.Errorf("dest = %q, want %q", dest, sharedSkillsDir)
	}
}

// pi has no MCP support; the registry must report that rather than inventing a
// config path.
func TestPiHasNoMCPConfig(t *testing.T) {
	pi, _ := lookupAgent("pi")
	path, err := pi.mcpConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Errorf("pi returned an MCP config path %q, want none", path)
	}
}

// Codex reads only the global TOML, so both scopes must resolve to the same
// absolute path rather than a project-relative one.
func TestCodexMCPConfigIsAlwaysGlobal(t *testing.T) {
	codex, _ := lookupAgent("codex")
	project, err := codex.mcpConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	global, err := codex.mcpConfig(true)
	if err != nil {
		t.Fatal(err)
	}
	if project != global {
		t.Errorf("codex project path %q != global path %q", project, global)
	}
	if !filepath.IsAbs(global) {
		t.Errorf("codex config path %q is not absolute", global)
	}
}

func TestAgentRegistryIsComplete(t *testing.T) {
	want := []string{"claude", "codex", "opencode", "kimi", "kilo", "pi"}
	got := agentIDs()
	if len(got) != len(want) {
		t.Fatalf("registry has %d agents, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("agent[%d] = %q, want %q", i, got[i], id)
		}
	}
	for _, id := range want {
		spec, ok := lookupAgent(id)
		if !ok {
			t.Fatalf("%s not found", id)
		}
		if spec.Label == "" {
			t.Errorf("%s has no label", id)
		}
		// Every agent that writes MCP config needs a manual fallback snippet
		// for when the write fails.
		if spec.writeMCP != nil && spec.manualSnippet == nil {
			t.Errorf("%s can write MCP config but has no manual snippet", id)
		}
	}
}

// The API rejects project IDs that are not ULIDs, while /v2/projects currently
// returns UUIDs. Getting this check wrong means either a hard failure at mint
// time or a silently over-scoped key.
func TestProjectScopableRejectsUUIDs(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"01HZXW2K7Y8Q9M0N1P2R3S4T5V", true},            // ULID
		{"proj_01HZXW2K7Y8Q9M0N1P2R3S4T5V", true},       // ULID with the documented prefix
		{"019def44-a743-7000-a442-c0db96b06699", false}, // what /v2/projects returns today
		{"", false},
		{"01HZXW2K7Y8Q9M0N1P2R3S4T5", false},  // 25 chars
		{"01hzxw2k7y8q9m0n1p2r3s4t5v", false}, // lowercase is not Crockford base32
	}
	for _, tc := range cases {
		if got := auth.ProjectScopable(tc.id); got != tc.want {
			t.Errorf("ProjectScopable(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// Project names are user-supplied and the key name has validation rules we do
// not control, so anything exotic must be stripped before it is sent.
func TestSanitizeKeyName(t *testing.T) {
	cases := map[string]string{
		"orq-cli host · project":   "orq-cli host project",
		"orq-cli host  My Project": "orq-cli host My Project",
		"emoji 🚀 name":             "emoji name",
		"  padded  ":               "padded",
		"···":                      "orq-cli",
		"":                         "orq-cli",
	}
	for in, want := range cases {
		if got := sanitizeKeyName(in); got != want {
			t.Errorf("sanitizeKeyName(%q) = %q, want %q", in, got, want)
		}
	}
	long := sanitizeKeyName(strings.Repeat("a", 200))
	if len(long) > 64 {
		t.Errorf("length %d exceeds 64", len(long))
	}
}
