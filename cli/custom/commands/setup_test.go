package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/rs/zerolog"
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
// catalogue is full of models nobody enabled", so the inactive ones must not
// be counted.
func TestCountEnabledModelsIgnoresInactive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/models") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		// /v2/models answers with a bare array, not a {data: []} envelope.
		fmt.Fprint(w, `[{"provider":"openai","model_id":"gpt-5-mini","is_active":true},
		                {"provider":"openai","model_id":"gpt-4","is_active":false}]`)
	}))
	defer srv.Close()

	got, err := countEnabledModels(auth.NewClient(srv.URL), &authState{apiBase: srv.URL, bearer: "t"})
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
