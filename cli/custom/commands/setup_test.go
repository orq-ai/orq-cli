package commands

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

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

// Everything setup writes for these agents reaches the credential indirectly —
// codex through env_key, opencode and kilo through {env:ORQ_API_KEY} — and none
// of them resolve that from a project-scoped config. Codex reads MCP only from
// the home TOML; opencode discards a project config containing env references
// entirely and then silently answers "orq/anthropic/…" from whatever other
// provider it can find, while kilo reports the file as invalid. A project copy
// is written and never used, so both scopes resolve to the same absolute path.
func TestEnvReferencingConfigsAreAlwaysGlobal(t *testing.T) {
	for _, id := range []string{"codex", "opencode", "kilo"} {
		spec, ok := lookupAgent(id)
		if !ok {
			t.Fatalf("%s is not registered", id)
		}
		paths := map[string]func(bool) (string, error){
			"mcp":      spec.mcpConfig,
			"provider": spec.providerConfig,
		}
		for kind, resolve := range paths {
			if resolve == nil {
				continue
			}
			project, err := resolve(false)
			if err != nil {
				t.Fatal(err)
			}
			global, err := resolve(true)
			if err != nil {
				t.Fatal(err)
			}
			if project != global {
				t.Errorf("%s %s: project path %q != global path %q", id, kind, project, global)
			}
			if !filepath.IsAbs(global) {
				t.Errorf("%s %s: path %q is not absolute", id, kind, global)
			}
		}
	}
}

// pi is deliberately absent: it supports neither MCP nor a provider config, so
// with skills gone setup has nothing to write for it.
func TestAgentRegistryIsComplete(t *testing.T) {
	want := []string{"claude", "codex", "opencode", "kimi", "kilo"}
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

// The providers step exists to tell "no provider connected" apart from "the
// catalogue is full of models nobody enabled". is_active is true for every
// entry the gateway knows about, so counting it reported hundreds of models on
// a workspace with nothing connected and the warning could never fire; only the
// workspace's enabled set counts.
func TestCountEnabledModelsIgnoresInactive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/models") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		// /v2/models answers with a bare array, not a {data: []} envelope.
		fmt.Fprint(w, `[{"provider":"openai","model_id":"gpt-5-mini","model_type":"chat","is_active":true,"enabled":true},
		                {"provider":"openai","model_id":"gpt-4","model_type":"chat","is_active":true,"enabled":false},
		                {"provider":"openai","model_id":"dall-e","model_type":"image","is_active":true,"enabled":true}]`)
	}))
	defer srv.Close()

	got, _, err := listEnabledModels(auth.NewClient(srv.URL), &authState{apiBase: srv.URL, bearer: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("counted %d enabled models, want 1", got)
	}
}

// /v2/projects pages at 25 by default. Stopping at page one makes a name
// lookup miss an existing project and create a duplicate instead of reusing it.
func TestListProjectsFollowsPages(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query().Get("starting_after"))
		switch r.URL.Query().Get("starting_after") {
		case "":
			fmt.Fprint(w, `{"data":[{"project_id":"p1","name":"first"},{"project_id":"p2","name":"second"}],"has_more":true}`)
		case "p2":
			fmt.Fprint(w, `{"data":[{"project_id":"p3","name":"Ferranti"}],"has_more":false}`)
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("starting_after"))
		}
	}))
	defer srv.Close()

	projects, err := auth.NewClient(srv.URL).ListProjects("t")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 3 {
		t.Fatalf("got %d projects, want 3 across both pages", len(projects))
	}
	if projects[2].Name != "Ferranti" {
		t.Errorf("second page missing: %+v", projects)
	}
	if len(seen) != 2 || seen[1] != "p2" {
		t.Errorf("cursor walk: %v", seen)
	}
}

// A self-hosted install has no known dashboard URL, so the BYOK pointer has to
// degrade to something that still helps rather than to a broken link.
func TestModelsSettingsURLFallsBackToDocs(t *testing.T) {
	t.Setenv("ORQ_WEB_BASE_URL", "")

	hosted := modelsSettingsURL(&authState{apiBase: auth.DefaultAPIBaseURL})
	if hosted != defaultWebBaseURL+modelsSettingsPath {
		t.Errorf("hosted: got %s", hosted)
	}
	selfHosted := modelsSettingsURL(&authState{apiBase: "https://orq.internal"})
	if !strings.HasPrefix(selfHosted, docsURL) {
		t.Errorf("self-hosted: got %s, want a docs link", selfHosted)
	}

	t.Setenv("ORQ_WEB_BASE_URL", "https://orq.internal/app/")
	if got := modelsSettingsURL(&authState{apiBase: "https://orq.internal"}); got != "https://orq.internal/app"+modelsSettingsPath {
		t.Errorf("override: got %s", got)
	}
}

// Agent configs reference ORQ_API_KEY instead of inlining the key, so setup has
// to leave something behind that actually puts it in the environment — in the
// syntax of the user's shell, since fish cannot parse `export VAR=value`. The
// file holds a live credential, so it must not be world-readable.
func TestWriteShellEnvFile(t *testing.T) {
	cases := []struct {
		shell    string
		wantFile string
		wantLine string
	}{
		{"/bin/zsh", "env", "export ORQ_API_KEY=test-token-value"},
		{"/bin/bash", "env", "export ORQ_API_KEY=test-token-value"},
		{"/opt/homebrew/bin/fish", "env.fish", "set -gx ORQ_API_KEY test-token-value"},
		{"/bin/sh", "env", "export ORQ_API_KEY=test-token-value"},
		{"/usr/bin/some-future-shell", "env", "export ORQ_API_KEY=test-token-value"},
		{"", "env", "export ORQ_API_KEY=test-token-value"},
	}
	for _, tc := range cases {
		t.Run(tc.shell, func(t *testing.T) {
			dir := t.TempDir()
			viper.Set("config-directory", dir)
			t.Cleanup(func() { viper.Set("config-directory", "") })
			t.Setenv("SHELL", tc.shell)

			path, err := writeShellEnvFile("test-token-value")
			if err != nil {
				t.Fatalf("writeShellEnvFile: %v", err)
			}
			if got := filepath.Base(path); got != tc.wantFile {
				t.Errorf("file = %q, want %q", got, tc.wantFile)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), tc.wantLine) {
				t.Errorf("env file lacks %q:\n%s", tc.wantLine, body)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode := info.Mode().Perm(); mode != 0o600 {
				t.Errorf("mode = %o, want 600", mode)
			}
		})
	}
}

// An unrecognised shell must still get a runnable command; only the profile
// file is unknown. Printing an empty line would leave the user with nothing.
func TestDetectShellAlwaysGivesACommand(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/some-future-shell")
	sh := detectShell("/tmp/orqcfg")
	if sh.Profile != "" {
		t.Errorf("Profile = %q, want empty for an unknown shell", sh.Profile)
	}
	if sh.Line == "" {
		t.Error("Line is empty; the user would be told to run nothing")
	}
}

// fakeAuthHandler stands in for the generated client's bearer handler, which is
// registered by initGeneratedRuntime and so is absent in unit tests.
type fakeAuthHandler struct{}

func (fakeAuthHandler) ProfileKeys() []string { return []string{"api_key"} }
func (fakeAuthHandler) OnRequest(*zerolog.Logger, *http.Request) error {
	return nil
}

// bartolo resolves a profile's "type" against its handler registry verbatim, so
// a profile whose type names no handler authenticates nothing: every generated
// command aborts with "no authentication handler configured". The type we write
// must be one the registry answers to.
func TestWriteAPIKeyProfileWritesAResolvableType(t *testing.T) {
	restore := bartolocli.AuthHandlers
	t.Cleanup(func() { bartolocli.AuthHandlers = restore })
	if bartolocli.Creds == nil {
		// initAuth, which normally creates this, runs from the generated
		// runtime that unit tests do not start.
		bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
		t.Cleanup(func() { bartolocli.Creds = nil })
	}

	for _, registered := range []string{"", "apikey"} {
		t.Run("handler registered as "+strconv.Quote(registered), func(t *testing.T) {
			bartolocli.AuthHandlers = map[string]bartolocli.AuthHandler{
				registered: fakeAuthHandler{},
			}
			dir := t.TempDir()
			viper.Set("config-directory", dir)
			t.Cleanup(func() { viper.Set("config-directory", "") })

			if err := writeAPIKeyProfile("default", "a-key"); err != nil {
				t.Fatalf("writeAPIKeyProfile: %v", err)
			}
			written := bartolocli.Creds.GetString("profiles.default.type")
			if _, ok := bartolocli.AuthHandlers[written]; !ok {
				t.Errorf("wrote type %q, which resolves to no handler", written)
			}
		})
	}
}

// Bartolo auto-loads ./.env at startup, so a key defined there is what the CLI
// actually authenticates with — the parser must agree with bartolo's on the
// forms users write.
func TestDotEnvAPIKeyMirrorsBartoloParsing(t *testing.T) {
	cases := map[string]struct {
		content string
		file    string
		value   string
	}{
		"plain":         {"ORQ_API_KEY=sk-orq-abc\n", ".env", "sk-orq-abc"},
		"export prefix": {"export ORQ_API_KEY=sk-orq-abc\n", ".env", "sk-orq-abc"},
		"quoted":        {`ORQ_API_KEY="sk-orq-abc"` + "\n", ".env", "sk-orq-abc"},
		"comment only":  {"# ORQ_API_KEY=sk-orq-abc\n", "", ""},
		"other keys":    {"DATABASE_URL=postgres://x\n", "", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			chdir(t, t.TempDir())
			if err := os.WriteFile(".env", []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			file, value := dotEnvAPIKey()
			if file != tc.file || value != tc.value {
				t.Errorf("got (%q, %q), want (%q, %q)", file, value, tc.file, tc.value)
			}
		})
	}
}

// .env.local is the second file bartolo loads; a key there must be found too.
func TestDotEnvAPIKeyReadsEnvLocal(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.WriteFile(".env.local", []byte("ORQ_API_KEY=sk-orq-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, value := dotEnvAPIKey()
	if file != ".env.local" || value != "sk-orq-local" {
		t.Errorf("got (%q, %q), want (.env.local, sk-orq-local)", file, value)
	}
}

// Re-running setup must not stack duplicate source lines in the profile,
// however the user phrased the existing one.
func TestProfileSourcesEnvFileDetectsAnyPhrasing(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, ".zshenv")
	sh := shellSetup{EnvFile: "/home/u/.orq/env", Profile: profile, Line: ". /home/u/.orq/env"}

	if profileSourcesEnvFile(sh) {
		t.Error("missing profile reported as sourcing the env file")
	}
	if err := os.WriteFile(profile, []byte("source /home/u/.orq/env  # by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !profileSourcesEnvFile(sh) {
		t.Error("hand-written source line was not detected")
	}
}

// $HOME- and ~-relative spellings reference the same file; an absolute-path
// comparison missed them and stacked a duplicate source line.
func TestProfileSourcesEnvFileDetectsHomeRelativeSpelling(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(t.TempDir(), ".zshenv")
	sh := shellSetup{
		EnvFile: filepath.Join(home, ".orq", "env"),
		Profile: profile,
		Line:    ". " + filepath.Join(home, ".orq", "env"),
	}
	if err := os.WriteFile(profile, []byte(`[ -f "$HOME/.orq/env" ] && . "$HOME/.orq/env"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !profileSourcesEnvFile(sh) {
		t.Error(`"$HOME/.orq/env" spelling was not detected`)
	}
}

// A placeholder line with no value carries no credential; reporting it would
// warn about a key that does not exist and mask a later file's real key.
func TestDotEnvAPIKeySkipsEmptyValues(t *testing.T) {
	chdir(t, t.TempDir())
	if err := os.WriteFile(".env", []byte("ORQ_API_KEY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".env.local", []byte("ORQ_API_KEY=sk-orq-real\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, value := dotEnvAPIKey()
	if file != ".env.local" || value != "sk-orq-real" {
		t.Errorf("got (%q, %q), want (.env.local, sk-orq-real)", file, value)
	}
}

// The tilde spelling is how users hand-write the line; the suffix match must
// cover it like the $HOME form.
func TestProfileSourcesEnvFileDetectsTildeSpelling(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(t.TempDir(), ".zshenv")
	sh := shellSetup{
		EnvFile: filepath.Join(home, ".orq", "env"),
		Profile: profile,
		Line:    ". " + filepath.Join(home, ".orq", "env"),
	}
	if err := os.WriteFile(profile, []byte(". ~/.orq/env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !profileSourcesEnvFile(sh) {
		t.Error("~/.orq/env spelling was not detected")
	}
}

// Terminals differ in color depth; emitting a truecolor sequence on a basic
// terminal prints garbage instead of a color, so the palette must degrade.
func TestBrandPaletteDegradesWithTerminal(t *testing.T) {
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}
	brand, ok, _ := brandPalette(env(map[string]string{"COLORTERM": "truecolor"}))
	if !strings.Contains(brand, "38;2;223;83;37") || !strings.Contains(ok, "38;2;0;255;221") {
		t.Errorf("truecolor tier missing brand hexes: %q %q", brand, ok)
	}
	brand, ok, _ = brandPalette(env(map[string]string{"TERM": "xterm-256color"}))
	if !strings.Contains(brand, "38;5;166") || !strings.Contains(ok, "38;5;50") {
		t.Errorf("256-color tier wrong: %q %q", brand, ok)
	}
	brand, ok, _ = brandPalette(env(map[string]string{"TERM": "vt100"}))
	if strings.Contains(brand, "38;") || strings.Contains(ok, "38;") {
		t.Errorf("basic tier leaked extended sequences: %q %q", brand, ok)
	}
}

// --no-mcp must leave the provider config alone: "route my calls through orq
// but do not give the agent read/write access to my workspace" is the whole
// point of the flag, and skipping both would just be --no-agent.
func TestNoMCPStillWritesProviderConfig(t *testing.T) {
	kimi, ok := lookupAgent("kimi")
	if !ok {
		t.Fatal("kimi not in the registry")
	}
	if kimi.writeProvider == nil {
		t.Fatal("kimi has no provider writer, so --no-mcp would leave nothing to do")
	}
	if kimi.writeMCP == nil {
		t.Fatal("kimi has no MCP writer, so --no-mcp would be a no-op")
	}
}

// The single wiring question maps onto the two branch flags, and the flags
// pre-answer it so scripts never see a prompt. This pins the mapping: which
// flag combinations wire which branch, and that nothing-to-wire short-circuits
// before any agent work.
// Every case runs instrumentAgents for real and asserts on what it did —
// the MCP file it wrote (or didn't) and the provider branch it entered (or
// skipped). Kimi is the probe agent, run from a throwaway HOME and CWD so
// every write lands there. The gateway branch's marker is its own per-agent
// "no models to offer" line — the client is empty, so entering the branch
// always ends there, and unlike the list-models warning it is not memoized.
func TestWiringFlagsMapToBranches(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		noMCP, noGateway     bool
		yes, noInput         bool
		wantMCP, wantGateway bool
		wantNothing          bool
	}{
		{name: "no flags, no-input → both (unchanged default)", noInput: true, wantMCP: true, wantGateway: true},
		{name: "--yes → both", yes: true, wantMCP: true, wantGateway: true},
		{name: "--no-mcp → gateway only", noMCP: true, noInput: true, wantGateway: true},
		{name: "--no-gateway → MCP only", noGateway: true, noInput: true, wantMCP: true},
		{name: "both flags → nothing", noMCP: true, noGateway: true, noInput: true, wantNothing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			// Kimi's MCP path is project-scoped, so the write lands in CWD.
			chdir(t, t.TempDir())
			var out strings.Builder
			rep := &reporter{w: &out}
			opts := &setupOptions{noMCP: tc.noMCP, noGateway: tc.noGateway, yes: tc.yes, noInput: tc.noInput, agents: []string{"kimi"}}
			results := instrumentAgents(rep, auth.NewClient(""), &authState{}, opts)
			if tc.wantNothing {
				// Both flags set must short-circuit before any per-agent work
				// — no prompt (noInput), no network, no writes.
				if results != nil {
					t.Fatalf("nothing-to-wire returned %d results, want none", len(results))
				}
				return
			}
			_, err := os.Stat(filepath.Join(".kimi-code", "mcp.json"))
			if gotMCP := err == nil; gotMCP != tc.wantMCP {
				t.Errorf("MCP file written = %v, want %v\noutput:\n%s", gotMCP, tc.wantMCP, out.String())
			}
			if gotGateway := strings.Contains(out.String(), "no models to offer"); gotGateway != tc.wantGateway {
				t.Errorf("gateway branch entered = %v, want %v\noutput:\n%s", gotGateway, tc.wantGateway, out.String())
			}
		})
	}
}

// Codex resolves its config directory from $CODEX_HOME, falling back to
// ~/.codex. Setup must write where codex will read: a profile under a
// hardcoded ~/.codex on a machine that sets CODEX_HOME is never loaded, and
// the printed `codex --profile orq` hint sends the user to a profile codex
// cannot find.
func TestCodexPathsHonorCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex-home"))
	codex, _ := lookupAgent("codex")
	for kind, resolve := range map[string]func(bool) (string, error){
		"mcp": codex.mcpConfig, "provider": codex.providerConfig,
	} {
		got, err := resolve(false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, os.Getenv("CODEX_HOME")+string(os.PathSeparator)) {
			t.Errorf("%s path %q ignores CODEX_HOME", kind, got)
		}
	}
}

// Setup makes real, billed completions against the user's own provider keys.
// This pins how many: exactly one, for the model the agent will open with — the
// only one that must work before the user picks anything. The rest of the
// catalogue is written unprobed, as `orq launch` already does, and verification
// reuses that single probe rather than buying the same answer twice.
func TestSetupBillsOneCompletionTotal(t *testing.T) {
	var completions int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			atomic.AddInt64(&completions, 1)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
			return
		}
		fmt.Fprint(w, `[
		 {"provider":"anthropic","model_id":"claude-sonnet-4-6","model_type":"chat","enabled":true,"has_functions":true},
		 {"provider":"anthropic","model_id":"claude-opus-4-8","model_type":"chat","enabled":true,"has_functions":true},
		 {"provider":"openai","model_id":"gpt-5.4","model_type":"chat","enabled":true,"has_functions":true},
		 {"provider":"moonshotai","model_id":"kimi-k2.6","model_type":"chat","enabled":true,"has_functions":true}]`)
	}))
	defer srv.Close()

	codingModelsFetched, cachedCodingModels, provenModel = false, nil, ""
	defaultModelResolved, provenCandidate = false, auth.RouterModel{}
	client := auth.NewClient(srv.URL)
	state := &authState{apiBase: srv.URL, bearer: "t"}
	rep := newReporter(true)

	models := codingModels(rep, client, state)
	if len(models) != 4 {
		t.Errorf("wrote %d models, want all 4 enabled ones", len(models))
	}
	if n := atomic.LoadInt64(&completions); n != 0 {
		t.Errorf("listing models billed %d completions, want 0", n)
	}

	// Once per agent that has a provider writer, as instrumentAgents does.
	// Four agents used to mean four billed probes for the same answer.
	first, _ := defaultCodingModel(rep, client, state)
	for range 3 {
		again, ok := defaultCodingModel(rep, client, state)
		if !ok || again.Ref() != first.Ref() {
			t.Errorf("memoized default = %q, want %q", again.Ref(), first.Ref())
		}
	}
	afterProbe := atomic.LoadInt64(&completions)
	verifyGateway(rep, client, state)
	total := atomic.LoadInt64(&completions)

	t.Logf("completions: default-model probes=%d  verify=+%d  total=%d", afterProbe, total-afterProbe, total)
	if afterProbe != 1 {
		t.Errorf("choosing the default across four agents billed %d completions, want 1", afterProbe)
	}
	if total != afterProbe {
		t.Errorf("verification billed %d extra completions, want 0", total-afterProbe)
	}
}

// The subcommand exists to re-run wiring, so its contract is: reuse the saved
// credential, never create one, and refuse clearly when none exists. Scope
// flags conflict loudly rather than one silently winning.
func TestCodingAgentsSubcommand(t *testing.T) {
	sub := newSetupCodingAgentsCommand()
	if sub.Name() != "coding-agents" {
		t.Fatalf("subcommand is %q — the name is load-bearing, 'agents' collides with the platform product", sub.Name())
	}
	for _, flag := range []string{"agent", "gateway", "mcp", "global", "local"} {
		if sub.Flags().Lookup(flag) == nil {
			t.Errorf("missing flag --%s", flag)
		}
	}

	if err := resolveScope(&setupOptions{global: true, local: true}); err == nil {
		t.Error("--global with --local did not conflict")
	}

	// --local forces project scope even where inference would pick $HOME.
	t.Chdir(t.TempDir()) // no project markers here
	forced := &setupOptions{local: true}
	if err := resolveScope(forced); err != nil {
		t.Fatal(err)
	}
	if forced.global {
		t.Error("--local was overridden by home-directory inference")
	}
	inferred := &setupOptions{}
	if err := resolveScope(inferred); err != nil {
		t.Fatal(err)
	}
	if !inferred.global {
		t.Error("inference in a non-project directory should pick $HOME")
	}
}

// The noun is "coding agents" everywhere the surface names it, so the flags
// spell it the same way — `--agent` next to `--no-coding-agents` was the same
// word twice in two spellings on one command. The old names shipped in a
// release, so they keep working as hidden aliases rather than breaking a
// script written against them.
func TestCodingAgentFlagsAndTheirLegacyAliases(t *testing.T) {
	for _, cmd := range []*cobra.Command{NewSetupCommand(), newSetupCodingAgentsCommand()} {
		f := cmd.Flags()
		if f.Lookup("coding-agent") == nil {
			t.Errorf("%s: missing --coding-agent", cmd.Name())
		}
		legacy := f.Lookup("agent")
		if legacy == nil {
			t.Fatalf("%s: --agent must keep working for scripts written against it", cmd.Name())
		}
		if !legacy.Hidden {
			t.Errorf("%s: deprecated --agent should not appear in help", cmd.Name())
		}
	}

	setup := NewSetupCommand().Flags()
	if setup.Lookup("no-coding-agents") == nil {
		t.Error("missing --no-coding-agents")
	}
	old := setup.Lookup("no-agent")
	if old == nil || !old.Hidden {
		t.Error("--no-agent should survive as a hidden alias")
	}

	// Both spellings must land on the same field, or one of them silently
	// does nothing.
	for _, name := range []string{"coding-agent", "agent"} {
		opts := setupOptions{}
		c := &cobra.Command{}
		c.Flags().StringSliceVar(&opts.agents, "coding-agent", nil, "")
		c.Flags().StringSliceVar(&opts.agents, "agent", nil, "")
		if err := c.Flags().Parse([]string{"--" + name, "codex"}); err != nil {
			t.Fatal(err)
		}
		if len(opts.agents) != 1 || opts.agents[0] != "codex" {
			t.Errorf("--%s did not populate the agent list: %v", name, opts.agents)
		}
	}
}

// --gateway and --mcp each narrow to one half. Both together narrow to
// nothing, which used to exit 0 having silently done no work while
// --global with --local refused loudly for the same class of mistake.
func TestCodingAgentsRefusesContradictoryNarrowing(t *testing.T) {
	cmd := newSetupCodingAgentsCommand()
	cmd.SetArgs([]string{"--gateway", "--mcp"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("--gateway with --mcp should refuse, not silently wire nothing")
	}
	if !strings.Contains(err.Error(), "nothing to wire") {
		t.Errorf("error should explain the contradiction, got: %v", err)
	}
}

// The skip line names the flag of the command the user ran. Naming the
// parent's --no-gateway from a subcommand that has no such flag sent people
// looking for something that does not exist.
func TestSkipFlagNamesTheCommandsOwnSpelling(t *testing.T) {
	parent := &setupOptions{}
	if got := parent.skipFlag("mcp"); got != "--no-mcp" {
		t.Errorf("parent mcp skip names %q", got)
	}
	if got := parent.skipFlag("gateway"); got != "--no-gateway" {
		t.Errorf("parent gateway skip names %q", got)
	}
	sub := &setupOptions{narrowing: true}
	if got := sub.skipFlag("gateway"); got != "--mcp" {
		t.Errorf("subcommand gateway skip names %q, want the flag that caused it", got)
	}
	if got := sub.skipFlag("mcp"); got != "--gateway" {
		t.Errorf("subcommand mcp skip names %q, want the flag that caused it", got)
	}
}
