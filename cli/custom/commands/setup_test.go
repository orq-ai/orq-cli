package commands

import (
	"encoding/json"
	"errors"
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
	"time"

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

// Every provider config reaches the credential from an absolute path: codex
// through env_key, opencode and kilo through {env:ORQ_API_KEY}, kimi literally.
// opencode discards a project config containing env references entirely and
// then silently answers from whatever other provider it can find, while kilo
// reports the file as invalid — so a project-relative copy would be written and
// never used.
func TestProviderConfigsResolveToOneAbsolutePath(t *testing.T) {
	for _, spec := range agentRegistry() {
		if spec.providerConfig == nil {
			continue
		}
		project, err := spec.providerConfig(false)
		if err != nil {
			t.Fatal(err)
		}
		global, err := spec.providerConfig(true)
		if err != nil {
			t.Fatal(err)
		}
		if project != global {
			t.Errorf("%s: project path %q != global path %q", spec.ID, project, global)
		}
		if !filepath.IsAbs(global) {
			t.Errorf("%s: path %q is not absolute", spec.ID, global)
		}
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
// a workspace with nothing connected and the warning could never fire. The
// count also requires function calling, or "N models available" precedes
// "no models to offer" from the same catalogue.
func TestCountEnabledModelsIgnoresInactive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/models") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		// /v2/models answers with a bare array, not a {data: []} envelope.
		fmt.Fprint(w, `[{"provider":"openai","model_id":"gpt-5-mini","model_type":"chat","is_active":true,"enabled":true,"has_functions":true},
		                {"provider":"openai","model_id":"gpt-4","model_type":"chat","is_active":true,"enabled":false,"has_functions":true},
		                {"provider":"openai","model_id":"old-chat","model_type":"chat","is_active":true,"enabled":true},
		                {"provider":"openai","model_id":"dall-e","model_type":"image","is_active":true,"enabled":true}]`)
	}))
	defer srv.Close()

	got, err := listEnabledModels(auth.NewClient(srv.URL), &authState{apiBase: srv.URL, bearer: "t"})
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

// Every dashboard settings page lives under the workspace key. An unprefixed
// link — which is what setup printed on every run — lands on a route that does
// not exist, and a self-hosted install has no dashboard URL at all, so both
// cases have to degrade to something that still helps.
func TestWorkspaceURLPrefixesTheWorkspaceOrFallsBackToDocs(t *testing.T) {
	t.Setenv("ORQ_WEB_BASE_URL", "")
	key := "acme"
	signedIn := &authState{
		apiBase: auth.DefaultAPIBaseURL,
		session: &auth.Session{ActiveWorkspaceKey: &key},
	}

	if got, want := workspaceURL(signedIn, providersPath), defaultWebBaseURL+"/acme/router/providers"; got != want {
		t.Errorf("BYOK link: got %s, want %s", got, want)
	}
	if got, want := workspaceURL(signedIn, creditsPath), defaultWebBaseURL+"/acme/admin/credits"; got != want {
		t.Errorf("credits link: got %s, want %s", got, want)
	}

	// No session means no workspace to name — an --api-key run. Emitting the
	// unprefixed page would be worse than sending them to the docs.
	if got := workspaceURL(&authState{apiBase: auth.DefaultAPIBaseURL}, providersPath); !strings.HasPrefix(got, docsURL) {
		t.Errorf("keyless: got %s, want a docs link", got)
	}
	if got := workspaceURL(&authState{apiBase: "https://orq.internal"}, providersPath); !strings.HasPrefix(got, docsURL) {
		t.Errorf("self-hosted: got %s, want a docs link", got)
	}

	t.Setenv("ORQ_WEB_BASE_URL", "https://orq.internal/app/")
	selfHosted := &authState{apiBase: "https://orq.internal", session: &auth.Session{ActiveWorkspaceKey: &key}}
	if got, want := workspaceURL(selfHosted, providersPath), "https://orq.internal/app/acme/router/providers"; got != want {
		t.Errorf("override: got %s, want %s", got, want)
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

			if err := writeAPIKeyProfile("default", "a-key", "test-ws"); err != nil {
				t.Fatalf("writeAPIKeyProfile: %v", err)
			}
			written := bartolocli.Creds.GetString("profiles.default.type")
			if _, ok := bartolocli.AuthHandlers[written]; !ok {
				t.Errorf("wrote type %q, which resolves to no handler", written)
			}
		})
	}
}

// credsHarness points bartolo's credential store and the config directory at
// throwaway state so profile reads and writes never touch the real machine.
func credsHarness(t *testing.T) {
	t.Helper()
	restoreCreds, restoreHandlers := bartolocli.Creds, bartolocli.AuthHandlers
	bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
	bartolocli.AuthHandlers = map[string]bartolocli.AuthHandler{"apikey": fakeAuthHandler{}}
	viper.Set("config-directory", t.TempDir())
	viper.Set("profile", "default")
	t.Cleanup(func() {
		bartolocli.Creds, bartolocli.AuthHandlers = restoreCreds, restoreHandlers
		viper.Set("config-directory", "")
		viper.Set("profile", "")
	})
}

// An API key is minted against whatever workspace is active at mint time and
// stays scoped to it forever. Reusing it after the session resolved a different
// workspace wires every agent config — kimi holds the literal key — to the
// workspace the user just switched away from, and verification passes because
// the key is valid there. So the profile records the key's workspace, and reuse
// requires a match: a provable mismatch re-mints, an unrecorded workspace (keys
// saved before the field existed, or brought via --api-key) is reused as before.
func TestSavedKeyIsReusedOnlyForItsOwnWorkspace(t *testing.T) {
	wsA, wsB := "workspace-a", "workspace-b"
	cases := map[string]struct {
		storedWS  string
		wantMints int64
	}{
		"same workspace reuses":        {storedWS: wsB, wantMints: 0},
		"unrecorded workspace reuses":  {storedWS: "", wantMints: 0},
		"other workspace mints for it": {storedWS: wsA, wantMints: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			credsHarness(t)
			var mints int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/api-keys/capabilities" {
					fmt.Fprint(w, `{"domains":[{"id":"chat_completions","group":3,"writable":true}]}`)
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v2/api-keys" {
					t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
				}
				atomic.AddInt64(&mints, 1)
				fmt.Fprint(w, `{"token":"sk-orq-fresh"}`)
			}))
			defer srv.Close()

			if err := saveAPIKeyProfile("sk-orq-old", tc.storedWS); err != nil {
				t.Fatal(err)
			}
			state := &authState{
				apiBase: srv.URL,
				bearer:  "session-token",
				session: &auth.Session{ActiveWorkspaceKey: &wsB, User: &auth.SessionUser{ID: "u1"}},
			}
			// noInput skips every confirm.
			opts := &setupOptions{noInput: true}
			_, _, err := resolveAPIKey(newReporter(true), auth.NewClient(srv.URL), state, opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := atomic.LoadInt64(&mints); got != tc.wantMints {
				t.Fatalf("minted %d keys, want %d", got, tc.wantMints)
			}

			wantKey, wantWS := "sk-orq-old", tc.storedWS
			if tc.wantMints > 0 {
				// The replacement must record the workspace it was minted for,
				// or the next run repeats the mismatch against a stale field.
				wantKey, wantWS = "sk-orq-fresh", wsB
			}
			if key, ws := savedAPIKey(); key != wantKey || ws != wantWS {
				t.Errorf("profile holds (%q, %q), want (%q, %q)", key, ws, wantKey, wantWS)
			}
		})
	}
}

// The subcommand never mints, so on a mismatch it must refuse — wiring with the
// saved key would silently point every agent at the key's workspace, not the
// one the session just resolved.
func TestKeyWorkspaceMismatch(t *testing.T) {
	cases := map[string]struct {
		saved, active string
		want          bool
	}{
		"different workspaces": {"ws-a", "ws-b", true},
		"same workspace":       {"ws-a", "ws-a", false},
		"saved unrecorded":     {"", "ws-b", false},
		"no session workspace": {"ws-a", "", false},
	}
	for name, tc := range cases {
		if got := keyWorkspaceMismatch(tc.saved, tc.active); got != tc.want {
			t.Errorf("%s: keyWorkspaceMismatch(%q, %q) = %v, want %v", name, tc.saved, tc.active, got, tc.want)
		}
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

// Nothing selected short-circuits before any agent work; a selected agent
// enters the gateway branch. The branch's marker is its own per-agent "no
// models to offer" line — the client is empty, so entering the branch always
// ends there, and unlike the list-models warning it is not memoized.
func TestGatewayWiringBranch(t *testing.T) {
	// Without this the test reads whatever the memos were left holding and
	// passes only because it happens to run before the tests that set them.
	resetSetupMemos(t)

	for _, tc := range []struct {
		name        string
		noGateway   bool
		wantGateway bool
	}{
		{name: "selected → gateway", wantGateway: true},
		{name: "--no-gateway → nothing", noGateway: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			chdir(t, t.TempDir())
			var out strings.Builder
			rep := &reporter{w: &out}
			opts := &setupOptions{noGateway: tc.noGateway, noInput: true, agents: []string{"kimi"}}
			srv := emptyCatalogueServer(t)
			results, err := instrumentAgents(rep, auth.NewClient(srv.URL), &authState{apiBase: srv.URL, bearer: "sk-orq-fixture"}, opts)
			if err != nil {
				t.Fatalf("instrumentAgents: %v", err)
			}
			if !tc.wantGateway {
				// Length, not nil: the no-gateway path still returns one result
				// per agent whose skills landed, and returns an empty slice when
				// none did.
				if len(results) != 0 {
					t.Fatalf("nothing-to-wire returned %d results, want none", len(results))
				}
				return
			}
			if !strings.Contains(out.String(), "no models to offer") {
				t.Errorf("gateway branch not entered\noutput:\n%s", out.String())
			}
		})
	}
}

// A provider write the user consented to and did not get is an agent failure,
// not a warning: it must reach the exit code (via errAgentFailed on the agent's
// Error) and the JSON. It used to evaporate — rep.warn, exit 0, success in --json.
func TestProviderWriteFailureIsAnAgentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"provider":"anthropic","model_id":"claude-sonnet-4-6","refId":"anthropic/claude-sonnet-4-6",
		 "model_type":"chat","enabled":true,"is_active":true,"has_functions":true,
		 "metadata":{"context_window":200000,"max_output_tokens":64000}}]`)
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	// ~/.kimi-code as a file: the provider write cannot create config.toml under it.
	if err := os.WriteFile(filepath.Join(home, ".kimi-code"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, t.TempDir())

	// codingModels memoizes per process; an earlier test's empty catalogue
	// would short-circuit this one into "no models to offer".
	resetSetupMemos(t)

	var out strings.Builder
	rep := &reporter{w: &out}
	opts := &setupOptions{noInput: true, agents: []string{"kimi"}}
	results, err := instrumentAgents(rep, auth.NewClient(srv.URL), &authState{apiBase: srv.URL, bearer: "t"}, opts)
	if err != nil {
		t.Fatalf("instrumentAgents: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Error == "" {
		t.Errorf("provider write failed but agent Error is empty — the failure would exit 0; output:\n%s", out.String())
	}
}

// Non-interactive setup mints and saves the key, wires nothing, and points at
// connect; connect then wires with the key setup saved. The pair is the CI
// composition and covers the mint-to-config chain end to end.
func TestSetupMintsThenConnectWires(t *testing.T) {
	const minted = "sk-orq-MINTED-THIS-RUN"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "api-keys"):
			fmt.Fprintf(w, `{"token":%q}`, minted)
		case strings.Contains(r.URL.Path, "model"):
			fmt.Fprint(w, `[{"provider":"anthropic","model_id":"claude-sonnet-4-6","refId":"anthropic/claude-sonnet-4-6",
			 "model_type":"chat","enabled":true,"is_active":true,"has_functions":true,
			 "metadata":{"context_window":200000,"max_output_tokens":64000}}]`)
		case strings.HasSuffix(r.URL.Path, "/v2/credits"):
			fmt.Fprint(w, `{"balance":25,"currency":"usd"}`)
		default:
			fmt.Fprint(w, `{"id":"u1","email":"a@b.c","display_name":"A",
			 "workspaces":[{"key":"acme","name":"Acme"}],"preferences":{"active_workspace":"acme"}}`)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Setenv("ORQ_API_BASE_URL", srv.URL)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	viper.Set("config-directory", t.TempDir())
	viper.Set("profile", "default")
	viper.Set("no-input", true)
	t.Cleanup(func() { viper.Set("config-directory", ""); viper.Set("profile", ""); viper.Set("no-input", false) })
	if bartolocli.Creds == nil {
		bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
		t.Cleanup(func() { bartolocli.Creds = nil })
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	key := "acme"
	exp := time.Now().Add(time.Hour).Format(time.RFC3339)
	if err := auth.SaveSession(&auth.Session{
		Version: 1, APIBaseURL: srv.URL, AuthBaseURL: srv.URL, V1BaseURL: srv.URL, ProfileBaseURL: srv.URL,
		RefreshToken: "refresh", BootstrapToken: auth.StoredAccessToken{Token: "bootstrap", ExpiresAt: exp},
		ActiveWorkspaceKey: &key,
		WorkspaceTokens:    map[string]auth.StoredAccessToken{key: {Token: "session-token", ExpiresAt: exp}},
	}); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	setup := NewSetupCommand()
	setup.SetArgs([]string{})
	if err := setup.Execute(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if saved, _ := savedAPIKey(); saved != minted {
		t.Fatalf("saved key = %q, want the minted one", saved)
	}
	if _, err := os.Stat(filepath.Join(home, ".kimi-code", "config.toml")); !os.IsNotExist(err) {
		t.Error("non-interactive setup wired an agent")
	}

	resetSetupMemos(t)
	connect := NewConnectCommand()
	connect.SetArgs([]string{"kimi"})
	if err := connect.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
	if err != nil {
		t.Fatalf("provider not wired: %v", err)
	}
	if !strings.Contains(string(data), minted) {
		t.Error("the minted key did not reach the agent config")
	}
	if strings.Contains(string(data), "session-token") {
		t.Error("the session token reached an agent config")
	}
}

// verified only says the API answered. Deriving the verdict from it alone
// printed "✓ Setup complete" and "setup_complete": true directly above an agent
// that failed to wire — the exit code was right, so a human saw a green verdict
// and CI keying on the field read success.
func TestSetupCompleteRequiresBothHalves(t *testing.T) {
	wired := agentResult{Agent: "kimi", Provider: "/x/config.toml"}
	failed := agentResult{Agent: "kimi", Error: "permission denied"}
	for name, tc := range map[string]struct {
		verified bool
		agents   []agentResult
		want     bool
	}{
		"verified, all wired":      {true, []agentResult{wired}, true},
		"verified, one failed":     {true, []agentResult{wired, failed}, false},
		"verified, nothing to do":  {true, nil, true},
		"unverified, all wired":    {false, []agentResult{wired}, false},
		"unverified and a failure": {false, []agentResult{failed}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := setupComplete(tc.verified, tc.agents); got != tc.want {
				t.Errorf("setupComplete(%v, %d agents) = %v, want %v", tc.verified, len(tc.agents), got, tc.want)
			}
		})
	}

	// The screen renders the same verdict it is given.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORQ_API_KEY", "sk-orq-set")
	var out strings.Builder
	printFinalScreen(&reporter{w: &out}, []agentResult{failed}, map[string]string{},
		setupComplete(true, []agentResult{failed}), &setupOptions{})
	if strings.Contains(out.String(), "Setup complete") {
		t.Errorf("a run with a failed agent claimed completion:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "failed checks") {
		t.Errorf("a failed run did not say so:\n%s", out.String())
	}
}

// agentResult.Error must become a non-zero exit: "wire failed, exit 0" is the
// regression this pins. connect is the wiring path drivable without a TTY.
func TestAFailedWireExitsNonZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"provider":"anthropic","model_id":"claude-sonnet-4-6","refId":"anthropic/claude-sonnet-4-6",
		 "model_type":"chat","enabled":true,"is_active":true,"has_functions":true,
		 "metadata":{"context_window":200000,"max_output_tokens":64000}}]`)
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Setenv("ORQ_API_BASE_URL", srv.URL)
	t.Chdir(t.TempDir())
	// ~/.kimi-code as a regular file: the provider write cannot succeed under it.
	if err := os.WriteFile(filepath.Join(home, ".kimi-code"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	viper.Set("config-directory", t.TempDir())
	viper.Set("profile", "default")
	t.Cleanup(func() { viper.Set("config-directory", ""); viper.Set("profile", "") })
	if bartolocli.Creds == nil {
		bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
		t.Cleanup(func() { bartolocli.Creds = nil })
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	resetSetupMemos(t)

	cmd := NewConnectCommand()
	// gateway named explicitly: a bare run now covers every built capability,
	// and ~/.kimi-code being a regular file fails the skills leg first, which
	// is a different (also fatal) error than the provider write under test.
	cmd.SetArgs([]string{"kimi", "gateway", "--api-key", "sk-orq-x"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("a failed provider write exited 0")
	}
	if !errors.Is(err, errAgentFailed) {
		t.Errorf("error = %v, want errAgentFailed", err)
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
		"provider": codex.providerConfig,
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

// Pins connect's and disconnect's shape. No provider config is project-scoped,
// so neither verb carries --global or --local: a flag that cannot change where
// a file lands is a lie.
func TestConnectCommandShape(t *testing.T) {
	c := NewConnectCommand()
	if c.Name() != "connect" {
		t.Fatalf("command is %q", c.Name())
	}
	for _, flag := range []string{"api-key", "yes", "dry-run", "status"} {
		if c.Flags().Lookup(flag) == nil {
			t.Errorf("connect missing flag --%s", flag)
		}
	}
	d := NewDisconnectCommand()
	if d.Name() != "disconnect" {
		t.Fatalf("command is %q", d.Name())
	}
	for _, flag := range []string{"yes", "dry-run"} {
		if d.Flags().Lookup(flag) == nil {
			t.Errorf("disconnect missing flag --%s", flag)
		}
	}
	for _, cmd := range []*cobra.Command{c, d} {
		for _, flag := range []string{"global", "local"} {
			if cmd.Flags().Lookup(flag) != nil {
				t.Errorf("%s still has --%s", cmd.Name(), flag)
			}
		}
	}
}

// setup owns the machine, not the agents: it carries no agent flags at all.
// The v4.13 spellings and the interim --coding-agent must fail at parse time
// rather than be silently ignored; agent selection lives on connect as
// positional arguments.
func TestSetupHasNoAgentFlags(t *testing.T) {
	f := NewSetupCommand().Flags()
	for _, name := range []string{"agent", "no-agent", "coding-agent", "no-coding-agents", "no-gateway", "global", "local"} {
		if f.Lookup(name) != nil {
			t.Errorf("setup still has --%s", name)
		}
	}
	cmd := NewSetupCommand()
	cmd.SetArgs([]string{"--coding-agent", "kimi"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Error("--coding-agent was accepted after removal")
	}
}

// The final screen must not tell the user to append a source line to a profile
// that already has one. The "is it exported here" check is always false on the
// run that just wrote the profile — the edit lands in new shells, not this one
// — so gating the append instruction on the environment alone printed
// `echo … >> ~/.zshenv` right under `✓ updated ~/.zshenv`. A user who followed
// both stacked a duplicate line per setup run.
func TestFinalScreenNeverReAppendsAWiredProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ORQ_API_KEY", "")
	dir := filepath.Join(home, ".orq")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The mint wrote the env file; without it the screen points at 'orq setup' instead.
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte("export ORQ_API_KEY=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set("config-directory", dir)
	t.Cleanup(func() { viper.Set("config-directory", "") })

	agents := []agentResult{{Agent: "kimi", Provider: filepath.Join(home, ".kimi-code", "config.toml")}}
	render := func() string {
		var out strings.Builder
		printFinalScreen(&reporter{w: &out}, agents, map[string]string{"docs": "https://docs.orq.ai"}, true, &setupOptions{})
		return out.String()
	}

	// Profile not wired: the append command is the right advice, and it names
	// the ~ form, which every shell expands in a source argument.
	if got := render(); !strings.Contains(got, "echo '. ~/.orq/env'") {
		t.Fatalf("unwired profile should offer the append command, got:\n%s", got)
	}

	// Profile already sources the env file — as it does on every run where the
	// user accepted the step-2 offer.
	profile := filepath.Join(home, ".zshenv")
	if err := os.WriteFile(profile, []byte(". "+filepath.Join(dir, "env")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := render()
	if strings.Contains(got, "echo '") {
		t.Errorf("wired profile still told to append:\n%s", got)
	}
	if !strings.Contains(got, ". ~/.orq/env") {
		t.Errorf("wired profile should still name the line to run in this shell:\n%s", got)
	}
}

// Setup prints the same few paths a dozen times; under a throwaway HOME the
// absolute form wraps the terminal, which is exactly the case every test run
// and every trial install hits.
func TestTildeShortensHomePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := tilde(filepath.Join(home, ".orq", "env")), filepath.Join("~", ".orq", "env"); got != want {
		t.Errorf("tilde() = %q, want %q", got, want)
	}
	// Outside $HOME, and $HOME itself, are left exactly as they are: a prefix
	// match on the bare home string would turn /tmp/homework into ~work.
	for _, p := range []string{"/etc/orq/env", home, home + "work/.orq/env"} {
		if got := tilde(p); got != p {
			t.Errorf("tilde(%q) = %q, want it unchanged", p, got)
		}
	}
}

// The flag help promises "use this API key instead of the one a previous
// setup saved" — so the key that lands in the agent configs must be the
// supplied one. It used to be split-brain: resolveAuth persisted the new key
// to credentials.json, then the agent-wiring step put the stale saved key back
// into the bearer, and every agent was wired to the old credential.
func TestCodingAgentsUsesTheSuppliedAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
			return
		}
		fmt.Fprint(w, `[{"provider":"anthropic","model_id":"claude-sonnet-4-6","refId":"anthropic/claude-sonnet-4-6",
		 "model_type":"chat","enabled":true,"is_active":true,"has_functions":true,
		 "metadata":{"context_window":200000,"max_output_tokens":64000}}]`)
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Setenv("ORQ_API_BASE_URL", srv.URL)
	t.Chdir(t.TempDir()) // no project markers: scope inference picks $HOME

	viper.Set("config-directory", t.TempDir())
	viper.Set("profile", "default")
	t.Cleanup(func() {
		viper.Set("config-directory", "")
		viper.Set("profile", "")
	})
	if bartolocli.Creds == nil {
		// initAuth, which normally creates this, runs from the generated
		// runtime that unit tests do not start.
		bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
		t.Cleanup(func() { bartolocli.Creds = nil })
	}
	if err := writeAPIKeyProfile("default", "sk-orq-OLD-STALE", ""); err != nil {
		t.Fatal(err)
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}

	resetSetupMemos(t)

	sub := NewConnectCommand()
	sub.SetArgs([]string{"kimi", "--api-key", "sk-orq-NEW-SUPPLIED"})
	if err := sub.Execute(); err != nil {
		t.Fatalf("coding-agents: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
	if err != nil {
		t.Fatalf("kimi config not written: %v", err)
	}
	tree := mustParseKimiTOML(t, string(data))
	got, _ := tree.Get("providers.orq.api_key").(string)
	if got != "sk-orq-NEW-SUPPLIED" {
		t.Errorf("agent config carries api_key %q, want the supplied key — the saved key overrode --api-key", got)
	}
}

// durableBearer answers "may this credential be written into an agent config".
// Both halves matter: a bearer that exists, and evidence it is a key rather
// than a session token. Reporting the zero value as durable writes api_key = ""
// into every config, and the agent then 401s while doctor calls it wired.
// A prompt that could not be drawn is not consent. Under test there is no TTY,
// so survey fails and the interactive path takes the error branch: at a
// true-default prompt, returning the default would mint keys and edit shell
// profiles for a user who pressed Ctrl-C. --yes and --no-input are unaffected,
// because both decide before a prompt is drawn.
func TestConfirmFailsClosedWhenThePromptCannotBeAsked(t *testing.T) {
	if got := (&setupOptions{}).confirm("mint a key?", true); got {
		t.Error("a prompt that failed to ask was taken as yes")
	}
	if got := (&setupOptions{yes: true}).confirm("mint a key?", true); !got {
		t.Error("--yes must still answer yes without asking")
	}
	if got := (&setupOptions{noInput: true}).confirm("mint a key?", true); !got {
		t.Error("--no-input must take the default, which is decided before any prompt")
	}
}

// Our own PreRun writes the session token into ORQ_API_KEY, so the environment
// cannot answer "did the user export a key". Reading it there told every
// logged-in user their shell was already exporting the key, suppressing the one
// hint that explains why their agents come up with an empty bearer.
func TestAgentKeyExportedReadsTheSnapshotNotTheInjection(t *testing.T) {
	t.Setenv("ORQ_API_KEY", "injected-session-token")
	prev, prevTaken := userEnvAPIKey, userEnvAPIKeyTaken
	t.Cleanup(func() { userEnvAPIKey, userEnvAPIKeyTaken = prev, prevTaken })

	SetUserEnvAPIKey("") // the user exported nothing
	if agentKeyExported() {
		t.Error("the injected session token was read as a user-exported key")
	}
	SetUserEnvAPIKey("sk-orq-real")
	if !agentKeyExported() {
		t.Error("a real exported key was not seen")
	}
}

func TestDurableBearerNeedsBothABearerAndItsProvenance(t *testing.T) {
	session := &auth.Session{}
	for name, tc := range map[string]struct {
		state *authState
		want  bool
	}{
		"zero value":            {&authState{}, false},
		"no session, no bearer": {&authState{apiBase: "x"}, false},
		"durableKey, no bearer": {&authState{durableKey: true}, false},
		"session token only":    {&authState{bearer: "sess", session: session}, false},
		"supplied key":          {&authState{bearer: "k", suppliedKey: "k", session: session}, true},
		"adopted or minted key": {&authState{bearer: "k", durableKey: true, session: session}, true},
		"api-key run":           {&authState{bearer: "k"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.state.durableBearer(); got != tc.want {
				t.Errorf("durableBearer() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Setup admits a run when ORQ_API_KEY is exported, so the wiring must
// accept the same credential. resolveAuth ranks the session above the env key,
// which left the run admitted and then skipping every provider write — the
// failure the skip message itself tells the user to fix by re-running setup.
func TestCodingAgentsWiresTheExportedKeyWhenLoggedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		case strings.Contains(r.URL.Path, "model"):
			fmt.Fprint(w, `[{"provider":"anthropic","model_id":"claude-sonnet-4-6","refId":"anthropic/claude-sonnet-4-6",
			 "model_type":"chat","enabled":true,"is_active":true,"has_functions":true,
			 "metadata":{"context_window":200000,"max_output_tokens":64000}}]`)
		default:
			fmt.Fprint(w, `{"id":"u1","email":"a@b.c","display_name":"A",
			 "workspaces":[{"key":"acme","name":"Acme"}],"preferences":{"active_workspace":"acme"}}`)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_BASE_URL", srv.URL)
	t.Chdir(t.TempDir())
	// The shape PreRun leaves behind: the user exported a key, and no profile was saved.
	t.Setenv("ORQ_API_KEY", "sk-orq-EXPORTED")

	viper.Set("config-directory", t.TempDir())
	viper.Set("profile", "default")
	t.Cleanup(func() { viper.Set("config-directory", ""); viper.Set("profile", "") })
	if bartolocli.Creds == nil {
		bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
		t.Cleanup(func() { bartolocli.Creds = nil })
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	key := "acme"
	exp := time.Now().Add(time.Hour).Format(time.RFC3339)
	if err := auth.SaveSession(&auth.Session{
		Version: 1, APIBaseURL: srv.URL, AuthBaseURL: srv.URL, V1BaseURL: srv.URL, ProfileBaseURL: srv.URL,
		RefreshToken: "refresh", BootstrapToken: auth.StoredAccessToken{Token: "bootstrap", ExpiresAt: exp},
		ActiveWorkspaceKey: &key,
		WorkspaceTokens:    map[string]auth.StoredAccessToken{key: {Token: "session-token", ExpiresAt: exp}},
	}); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	sub := NewConnectCommand()
	sub.SetArgs([]string{"kimi"})
	if err := sub.Execute(); err != nil {
		t.Fatalf("coding-agents: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
	if err != nil {
		t.Fatalf("provider not wired at all: %v", err)
	}
	tree := mustParseKimiTOML(t, string(data))
	got, _ := tree.Get("providers.orq.api_key").(string)
	if got != "sk-orq-EXPORTED" {
		t.Errorf("agent config carries api_key %q, want the exported key", got)
	}
	if strings.Contains(string(data), "session-token") {
		t.Error("the session token reached an agent config")
	}
}

// The subcommand's documented case: a logged-in user with a saved key and no
// --api-key. The saved key is durable, so the provider must be wired with it —
// not skipped as "no durable API key", which is what happened when adopting
// the key set the bearer without its durability.
func TestCodingAgentsWiresTheSavedKeyForALoggedInUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		case strings.Contains(r.URL.Path, "model"):
			fmt.Fprint(w, `[{"provider":"anthropic","model_id":"claude-sonnet-4-6","refId":"anthropic/claude-sonnet-4-6",
			 "model_type":"chat","enabled":true,"is_active":true,"has_functions":true,
			 "metadata":{"context_window":200000,"max_output_tokens":64000}}]`)
		default:
			fmt.Fprint(w, `{"id":"u1","email":"a@b.c","display_name":"A",
			 "workspaces":[{"key":"acme","name":"Acme"}],
			 "preferences":{"active_workspace":"acme"}}`)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Setenv("ORQ_API_BASE_URL", srv.URL)
	t.Chdir(t.TempDir())

	viper.Set("config-directory", t.TempDir())
	viper.Set("profile", "default")
	t.Cleanup(func() {
		viper.Set("config-directory", "")
		viper.Set("profile", "")
	})
	if bartolocli.Creds == nil {
		bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
		t.Cleanup(func() { bartolocli.Creds = nil })
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	if err := writeAPIKeyProfile("default", "sk-orq-SAVED-DURABLE", "acme"); err != nil {
		t.Fatal(err)
	}
	key := "acme"
	exp := time.Now().Add(time.Hour).Format(time.RFC3339)
	if err := auth.SaveSession(&auth.Session{
		Version:            1,
		APIBaseURL:         srv.URL,
		AuthBaseURL:        srv.URL,
		V1BaseURL:          srv.URL,
		ProfileBaseURL:     srv.URL,
		RefreshToken:       "refresh",
		BootstrapToken:     auth.StoredAccessToken{Token: "bootstrap", ExpiresAt: exp},
		ActiveWorkspaceKey: &key,
		WorkspaceTokens: map[string]auth.StoredAccessToken{
			key: {Token: "session-token", ExpiresAt: exp},
		},
	}); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	sub := NewConnectCommand()
	sub.SetArgs([]string{"kimi"})
	if err := sub.Execute(); err != nil {
		t.Fatalf("coding-agents: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml"))
	if err != nil {
		t.Fatalf("provider not wired at all: %v", err)
	}
	tree := mustParseKimiTOML(t, string(data))
	got, _ := tree.Get("providers.orq.api_key").(string)
	if got != "sk-orq-SAVED-DURABLE" {
		t.Errorf("agent config carries api_key %q, want the saved key", got)
	}
}

func resetSetupMemos(t *testing.T) {
	t.Helper()
	reset := func() {
		codingModelsFetched, cachedCodingModels = false, nil
	}
	reset()
	t.Cleanup(reset)
}

// gatewayFixture serves a catalogue, a credit balance, and counts every router
// call. The POST counter is the point: setup must never make one.
func gatewayFixture(t *testing.T, balance string, catalogue string) (*httptest.Server, *int64) {
	t.Helper()
	var routerCalls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			atomic.AddInt64(&routerCalls, 1)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		case strings.HasSuffix(r.URL.Path, "/v2/credits"):
			if balance == "" {
				http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
				return
			}
			fmt.Fprintf(w, `{"balance":%s,"currency":"usd"}`, balance)
		default:
			fmt.Fprint(w, catalogue)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &routerCalls
}

const oneChatModel = `[{"provider":"openai","model_id":"gpt-5.4","model_type":"chat","enabled":true,"has_functions":true}]`

// wiringHarness isolates the filesystem writes instrumentAgents performs.
// Every agent resolves its provider config under $HOME, so without this the
// config lands in the package source tree, which is how a test-server URL got
// committed once.
func wiringHarness(t *testing.T) {
	t.Helper()
	resetSetupMemos(t)
	t.Setenv("HOME", t.TempDir())
	chdir(t, t.TempDir())
	// Names a dashboard for the test server: workspaceLink builds no URL without
	// one, so the "Enable models" remedy would never render.
	t.Setenv("ORQ_WEB_BASE_URL", "https://my.orq.ai")
}

// Setup writes configuration. It does not send a model call to prove that
// configuration works, because a router request bills the user's credits and
// opens a span in their workspace, the first trace a brand-new account would
// ever see, for a request they did not make. The proof was also thinner than it
// looked: the probe sent no tools, so it passed on exactly the models that then
// failed an agent's real traffic.
func TestSetupMakesNoRouterCall(t *testing.T) {
	srv, routerCalls := gatewayFixture(t, "25", oneChatModel)
	wiringHarness(t)

	rep := &reporter{w: &strings.Builder{}}
	state := sessionWithToken(srv.URL)
	state.bearer = "t"
	instrumentAgents(rep, auth.NewClient(srv.URL), state, &setupOptions{noInput: true, agents: []string{"kimi"}})

	if n := atomic.LoadInt64(routerCalls); n != 0 {
		t.Fatalf("setup made %d router calls, want 0", n)
	}
	// A default is still chosen, from the catalogue ranking rather than a probe.
	cfg := filepath.Join(os.Getenv("HOME"), ".kimi-code", "config.toml")
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("provider config not written: %v", err)
	}
	if !strings.Contains(string(data), "openai/gpt-5.4") {
		t.Errorf("no default model written without a probe:\n%s", data)
	}
}

// pi resolves its provider config from $PI_CODING_AGENT_DIR, unlike every other
// agent in the registry, so it gets its own pass through the shared loop.
func TestInstrumentAgentsWiresPi(t *testing.T) {
	srv, _ := gatewayFixture(t, "25", oneChatModel)
	wiringHarness(t)

	var out strings.Builder
	rep := &reporter{w: &out}
	state := sessionWithToken(srv.URL)
	state.bearer = "t"
	results, err := instrumentAgents(rep, auth.NewClient(srv.URL), state, &setupOptions{noInput: true, agents: []string{"pi"}})
	if err != nil {
		t.Fatalf("instrumentAgents: %v", err)
	}

	cfg := filepath.Join(os.Getenv("HOME"), ".pi", "agent", "models.json")
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("provider config not written: %v", err)
	}
	if !strings.Contains(string(data), `"openai/gpt-5.4"`) {
		t.Errorf("catalogue model missing from models.json:\n%s", data)
	}
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("pi result carries an error: %+v", results)
	}
	if results[0].Provider == "" {
		t.Errorf("provider not reported as wired: %+v", results[0])
	}
}

// The models half of gateway readiness. The credit balance used to be reported
// here too and is gone: a zero balance blocks nothing on its own, and the four
// conditions that decide it — BYOK, private models, the recently-created flag,
// the subscription — are all unreadable from the CLI.
func TestGatewayReadinessStatesEachSayTheirPiece(t *testing.T) {
	cases := map[string]struct {
		balance, catalogue string
		selfHosted         bool
		wantFacts          []string
		wantAbsent         []string
		wantLinkBlocks     int
	}{
		"models available": {
			balance: "25", catalogue: oneChatModel,
			wantAbsent: []string{"Enable models", "credit", "refused", "available to coding agents"},
		},
		"no models": {
			balance: "25", catalogue: `[]`,
			wantFacts:      []string{"no models enabled for this workspace", "/acme/router/models"},
			wantAbsent:     []string{"credit", "refused", "0 models available"},
			wantLinkBlocks: 1,
		},
		// A zero balance is not a fact the CLI reports any more, in either state.
		"zero balance is silent, models present": {
			balance: "0", catalogue: oneChatModel,
			wantAbsent: []string{"credit", "refused", "/acme/admin/credits", "available to coding agents"},
		},
		"zero balance is silent, no models": {
			balance: "0", catalogue: `[]`,
			wantFacts:      []string{"no models enabled for this workspace", "/acme/router/models", "How it works"},
			wantAbsent:     []string{"credit", "refused", "/acme/admin/credits"},
			wantLinkBlocks: 1,
		},
		"self-hosted, no models": {
			balance: "25", catalogue: `[]`, selfHosted: true,
			wantFacts:  []string{"no models enabled for this workspace", "How it works"},
			wantAbsent: []string{"my.orq.ai", "credit"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/v2/credits") {
					fmt.Fprintf(w, `{"balance":%s,"currency":"usd"}`, tc.balance)
					return
				}
				fmt.Fprint(w, tc.catalogue)
			}))
			defer srv.Close()
			wiringHarness(t)
			if tc.selfHosted {
				t.Setenv("ORQ_WEB_BASE_URL", "")
			}

			var out strings.Builder
			rep := &reporter{w: &out}
			state := sessionWithToken(srv.URL)
			state.bearer = "t"
			instrumentAgents(rep, auth.NewClient(srv.URL), state, &setupOptions{noInput: true, agents: []string{"kimi"}})

			got := out.String()
			for _, want := range tc.wantFacts {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q:\n%s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("unexpected %q:\n%s", absent, got)
				}
			}
			// Each fact carries its own remedy, but the docs pointer is the line
			// that used to repeat once per fact.
			if n := strings.Count(got, "Enable models"); n != tc.wantLinkBlocks {
				t.Errorf("the models remedy appeared %d times, want %d:\n%s", n, tc.wantLinkBlocks, got)
			}
			if n := strings.Count(got, "How it works"); n > 1 {
				t.Errorf("docs pointer repeated %d times, want at most 1:\n%s", n, got)
			}
		})
	}
}

// The final screen's job is to say what is wired for which agent: the agent
// name as a flush-left heading, one indented row per capability, each row
// carrying its own ✓ or ✗. It has no per-framework start commands — those told
// the user how to run each agent, which is not the question this screen
// answers. With nothing to report the screen says so and points at
// `orq connect`.
func TestFinalScreenHasTwoStates(t *testing.T) {
	cases := map[string]struct {
		agents     []agentResult
		want       []string
		wantAbsent []string
		// wantOnce pins the point of the heading shape: a name that appears
		// twice means the per-capability rows regrew their own agent column.
		wantOnce []string
	}{
		"one agent": {
			agents:     []agentResult{{Agent: "kimi", Provider: "~/.kimi-code/config.toml"}},
			want:       []string{"kimi\n✓ gateway   ~/.kimi-code/config.toml\n"},
			wantAbsent: []string{"Nothing is wired", "orq launch", "Start "},
		},
		// One row per agent, so a screen that reports only the first fails here.
		"two agents": {
			agents: []agentResult{
				{Agent: "claude", Provider: ".claude-provider"},
				{Agent: "kimi", Provider: "~/.kimi-code/config.toml"},
			},
			want: []string{
				"claude\n✓ gateway   .claude-provider\n",
				"kimi\n✓ gateway   ~/.kimi-code/config.toml\n",
			},
		},
		// Both capabilities on one agent get a row each: a skills-only run used
		// to fall through to the nothing-wired branch, which was the bug.
		"gateway and skills": {
			agents: []agentResult{{
				Agent:    "claude",
				Provider: "~/.claude/settings.json",
				Skills:   "/home/u/.claude/skills",
			}},
			// The name is the heading, written once for both rows.
			want: []string{
				"claude\n✓ gateway   ~/.claude/settings.json\n✓ skills    ",
			},
			wantOnce:   []string{"claude\n"},
			wantAbsent: []string{"Nothing is wired"},
		},
		"skills only": {
			agents:     []agentResult{{Agent: "codex", Skills: "/home/u/.codex/skills"}},
			want:       []string{"codex\n✓ skills    "},
			wantAbsent: []string{"Nothing is wired", "gateway"},
		},
		"nothing wired": {
			agents:     nil,
			want:       []string{"Nothing is wired on this machine yet.", "orq connect"},
			wantAbsent: []string{"gateway", "Start "},
		},
		// A failed wire gets a red cross and the reason, not silence: an agent
		// the user asked for and did not get is the one row they most need.
		"errored agent shows the failure": {
			agents:     []agentResult{{Agent: "kimi", Provider: "~/.kimi-code/config.toml", Error: "boom"}},
			want:       []string{"kimi\n✗ gateway   boom\n"},
			wantAbsent: []string{"Nothing is wired", "~/.kimi-code/config.toml"},
		},
		// Skipped is a wire that was never attempted; it is still a capability
		// the user asked for and did not get.
		"skipped wire shows the reason": {
			agents:     []agentResult{{Agent: "kimi", Skipped: "no models to offer"}},
			want:       []string{"kimi\n✗ gateway   no models to offer\n"},
			wantAbsent: []string{"Nothing is wired"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", filepath.Join(home, "no-codex"))
			// Deterministic detection: kimi is the installed agent.
			if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
				t.Fatal(err)
			}
			// Silence the env-hint block so these cases assert only the branch
			// text; the ordering of hint vs suggestion has its own case below.
			t.Setenv("ORQ_API_KEY", "sk-orq-set")

			var out strings.Builder
			printFinalScreen(&reporter{w: &out}, tc.agents, map[string]string{}, true, &setupOptions{})
			got := out.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q:\n%s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("unexpected %q:\n%s", absent, got)
				}
			}
			for _, once := range tc.wantOnce {
				if n := strings.Count(got, once); n != 1 {
					t.Errorf("%q appeared %d times, want 1:\n%s", once, n, got)
				}
			}
		})
	}
}

// The verdict line is the one row that says whether to trust the run, and the
// footer rows are the only way out of a bad one. Both were unasserted: swapping
// the verdict branches left the whole suite green, and links was an empty map in
// every other test, so the Workspace and Stuck? rows were never rendered at all.
//
// Every label row is anchored, so a label that outgrows labelWidth fails here
// rather than silently starting its value one column left of the others.
func TestFinalScreenFailedVerdictAndFooter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "sk-orq-set")

	var out strings.Builder
	printFinalScreen(&reporter{w: &out},
		[]agentResult{{Agent: "kimi", Provider: "~/.kimi-code/config.toml"}},
		map[string]string{"workspace": "https://my.orq.ai/ws"},
		false, &setupOptions{})
	got := out.String()

	for _, want := range []string{
		"Setup finished with failed checks",
		"\nkimi\n✓ gateway   ~/.kimi-code/config.toml\n",
		"\nWorkspace   https://my.orq.ai/ws\n",
		"\nStuck?      orq doctor  ·  " + docsURL + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Setup complete") {
		t.Errorf("claimed success on a failed run:\n%s", got)
	}
}

// Everything above asserts the stripped rendering, because no test runs on a
// TTY. These goldens force the colour path through the humanOutput seam with a
// pinned palette, so recolouring the check, dropping the bold, or padding the
// painted label — mutations the stripped tests cannot see — fail on bytes.
func TestFinalScreenGoldens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "sk-orq-set")
	t.Setenv("NO_COLOR", "")
	if err := os.MkdirAll(filepath.Join(home, ".kimi-code"), 0o755); err != nil {
		t.Fatal(err)
	}

	prevHuman := humanOutput
	prevBrand, prevOK, prevWarn := ansiBrand, ansiOK, ansiWarn
	t.Cleanup(func() {
		humanOutput = prevHuman
		ansiBrand, ansiOK, ansiWarn = prevBrand, prevOK, prevWarn
	})
	ansiBrand, ansiOK, ansiWarn = "\033[95m", "\033[92m", "\033[93m"

	wired := []agentResult{{Agent: "kimi", Provider: "~/.kimi-code/config.toml"}}
	links := map[string]string{"workspace": "https://my.orq.ai/ws"}

	for name, tc := range map[string]struct {
		colour   bool
		agents   []agentResult
		verified bool
		want     string
	}{
		"wired, colour off": {false, wired, true, "" +
			"\n✓ Setup complete\n\n" +
			"kimi\n✓ gateway   ~/.kimi-code/config.toml\n\n" +
			"Workspace   https://my.orq.ai/ws\n" +
			"Stuck?      orq doctor  ·  https://docs.orq.ai\n\n"},
		"wired, colour on": {true, wired, true, "" +
			"\n\033[92m✓\033[0m \033[1mSetup complete\033[0m\n\n" +
			"\033[1m\033[95mkimi\033[0m\033[0m\n" +
			"\033[92m✓\033[0m gateway   \033[2m~/.kimi-code/config.toml\033[0m\n\n" +
			"\033[2mWorkspace  \033[0m https://my.orq.ai/ws\n" +
			"\033[2mStuck?     \033[0m orq doctor  \033[2m·\033[0m  https://docs.orq.ai\n\n"},
		"nothing wired, failed, colour on": {true, nil, false, "" +
			"\n\033[93m!\033[0m \033[1mSetup finished with failed checks — see above\033[0m\n\n" +
			"Nothing is wired on this machine yet.\n" +
			"\033[2mWire       \033[0m orq connect\n\n" +
			"\033[2mWorkspace  \033[0m https://my.orq.ai/ws\n" +
			"\033[2mStuck?     \033[0m orq doctor  \033[2m·\033[0m  https://docs.orq.ai\n\n"},
	} {
		t.Run(name, func(t *testing.T) {
			humanOutput = func() bool { return tc.colour }
			var out strings.Builder
			printFinalScreen(&reporter{w: &out}, tc.agents, links, tc.verified, &setupOptions{})
			if got := out.String(); got != tc.want {
				t.Errorf("golden mismatch:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// A gateway wire is only live once ORQ_API_KEY is exported, so the screen that
// reports one must also say when this shell lacks it — and say it below the
// wire it qualifies, not above a list the user has not read yet.
func TestFinalScreenEnvWarningFollowsTheWiredList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")

	var out strings.Builder
	printFinalScreen(&reporter{w: &out},
		[]agentResult{{Agent: "kimi", Provider: "~/.kimi-code/config.toml"}},
		map[string]string{}, true, &setupOptions{})
	got := out.String()

	warn := strings.Index(got, "agents inherit it from this shell")
	wire := strings.Index(got, "~/.kimi-code/config.toml")
	if warn == -1 || wire == -1 {
		t.Fatalf("expected both the wired row and the env warning:\n%s", got)
	}
	if warn < wire {
		t.Errorf("env warning printed above the wire it qualifies:\n%s", got)
	}
}

func sessionWithToken(apiBase string) *authState {
	key := "acme"
	return &authState{
		apiBase: apiBase,
		// By step 3 a real run has an API key as the bearer. Both fields, never
		// one: durableKey without a bearer is the illegal state useDurableKey exists to prevent.
		bearer:     "sk-orq-fixture",
		durableKey: true,
		session: &auth.Session{
			ActiveWorkspaceKey: &key,
			WorkspaceTokens: map[string]auth.StoredAccessToken{
				key: {Token: "session-token", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)},
			},
		},
	}
}

// emptyCatalogueServer answers /v2/models with an enabled-but-empty catalogue.
// Tests that mean "this workspace has no models" must use this rather than an
// unreachable client: a failed fetch is a different outcome and now reports as one.
func emptyCatalogueServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCodingAgentsDoesNotPersistASuppliedKey(t *testing.T) {
	srv := emptyCatalogueServer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Setenv("ORQ_API_BASE_URL", srv.URL)
	t.Chdir(t.TempDir())

	viper.Set("config-directory", t.TempDir())
	viper.Set("profile", "default")
	t.Cleanup(func() { viper.Set("config-directory", ""); viper.Set("profile", "") })
	if bartolocli.Creds == nil {
		bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
		t.Cleanup(func() { bartolocli.Creds = nil })
	}
	if bartolocli.Formatter == nil {
		bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
		t.Cleanup(func() { bartolocli.Formatter = nil })
	}
	if err := writeAPIKeyProfile("default", "sk-orq-SAVED", "acme"); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	sub := NewConnectCommand()
	sub.SetArgs([]string{"kimi", "--api-key", "sk-orq-BORROWED"})
	_ = sub.Execute() // the wire outcome is another test's subject

	key, ws := savedAPIKey()
	if key != "sk-orq-SAVED" {
		t.Errorf("saved key is now %q — the borrowed key replaced it", key)
	}
	if ws != "acme" {
		t.Errorf("saved workspace is now %q — blanking it disables the mismatch guard", ws)
	}
}

// An unreachable gateway is not an empty workspace. Reporting the first as the
// second told users to go enable models in a workspace that already had them,
// recorded no error, and exited 0 having wired nothing — and because the
// catalogue is memoized per run, one transient failure did it for every agent.
func TestACatalogueFetchFailureIsAnAgentError(t *testing.T) {
	// Closed immediately: the port is dead, so ListModels fails at the transport.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close()

	t.Setenv("HOME", t.TempDir())
	chdir(t, t.TempDir())
	resetSetupMemos(t)

	var out strings.Builder
	rep := &reporter{w: &out}
	opts := &setupOptions{noInput: true, agents: []string{"kimi"}}
	results, err := instrumentAgents(rep, auth.NewClient(dead), &authState{apiBase: dead, bearer: "sk-orq-fixture"}, opts)
	if err != nil {
		t.Fatalf("instrumentAgents: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Error == "" {
		t.Errorf("a failed catalogue fetch left Error empty — the run would exit 0:\n%s", out.String())
	}
	if strings.Contains(out.String(), "no models to offer") {
		t.Errorf("a fetch failure was reported as an empty workspace:\n%s", out.String())
	}
}

// claude has no provider config at all: it routes through the environment.
// Naming it must say so rather than print nothing and exit 0.
func TestGatewayOnlyOnAnAgentWithoutAProviderSaysSo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, t.TempDir())
	resetSetupMemos(t)

	srv := emptyCatalogueServer(t)
	var out strings.Builder
	rep := &reporter{w: &out}
	opts := &setupOptions{noInput: true, agents: []string{"claude"}}
	results, err := instrumentAgents(rep, auth.NewClient(srv.URL), &authState{apiBase: srv.URL, bearer: "sk-orq-fixture"}, opts)
	if err != nil {
		t.Fatalf("instrumentAgents: %v", err)
	}
	if !strings.Contains(out.String(), "no gateway provider config") {
		t.Errorf("gateway-only on claude printed nothing about the half that cannot exist:\n%q", out.String())
	}
	if len(results) != 1 || results[0].Provider != "" {
		t.Errorf("claude reported a provider it cannot have: %+v", results)
	}
}

// Skipping an empty catalogue protects a provider block an earlier run wrote,
// but "left unchanged" reads as "nothing is configured" — the state a user
// cannot tell apart by reading the file they were just told was untouched.
func TestEmptyCatalogueSaysWhetherAnEarlierWireSurvives(t *testing.T) {
	resetSetupMemos(t)
	kimi, _ := lookupAgent("kimi")

	for _, tc := range []struct {
		name   string
		wire   bool
		want   string
		unwant string
	}{
		{name: "nothing there", wire: false, want: "nothing written", unwant: "kept the"},
		{name: "earlier wire survives", wire: true, want: "kept the", unwant: "nothing written"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			chdir(t, t.TempDir())

			if tc.wire {
				path, err := kimi.providerConfig(true)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-k",
					[]auth.RouterModel{model("openai", "gpt-4o", 128000, true, true, "chat")}, ""); err != nil {
					t.Fatal(err)
				}
			}

			var out strings.Builder
			rep := &reporter{w: &out}
			opts := &setupOptions{noInput: true, agents: []string{"kimi"}}
			srv := emptyCatalogueServer(t)
			if _, err := instrumentAgents(rep, auth.NewClient(srv.URL), &authState{apiBase: srv.URL, bearer: "sk-orq-fixture"}, opts); err != nil {
				t.Fatalf("instrumentAgents: %v", err)
			}
			got := out.String()
			if !strings.Contains(got, tc.want) {
				t.Errorf("missing %q:\n%s", tc.want, got)
			}
			if strings.Contains(got, tc.unwant) {
				t.Errorf("unexpected %q:\n%s", tc.unwant, got)
			}
		})
	}
}

// Declining "Create a workspace API key now?" leaves authState.bearer as the
// session's expiring access token. That token must never be wired into agent
// configs: kimi embeds the literal value, and it 401s within the hour.
func TestSessionTokenIsNeverWiredIntoAgents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"provider":"anthropic","model_id":"claude-sonnet-4-6","refId":"anthropic/claude-sonnet-4-6",
		 "model_type":"chat","enabled":true,"is_active":true,"has_functions":true,
		 "metadata":{"context_window":200000,"max_output_tokens":64000}}]`)
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	chdir(t, t.TempDir())

	codingModelsFetched, cachedCodingModels = false, nil
	t.Cleanup(func() { codingModelsFetched, cachedCodingModels = false, nil })

	const sessionJWT = "eyJhbGciOiJIUzI1NiJ9.SESSION-ACCESS-TOKEN.sig"
	var out strings.Builder
	rep := &reporter{w: &out}
	opts := &setupOptions{noInput: true, agents: []string{"kimi"}}
	// The post-decline state: bearer is the session token, nothing durable.
	state := &authState{apiBase: srv.URL, bearer: sessionJWT, session: &auth.Session{}}

	results, err := instrumentAgents(rep, auth.NewClient(srv.URL), state, opts)
	if err != nil {
		t.Fatalf("instrumentAgents: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(home, ".kimi-code", "config.toml")); err == nil &&
		strings.Contains(string(body), sessionJWT) {
		t.Fatalf("session token written literally into kimi's config:\n%s", body)
	}
	if len(results) != 1 || results[0].Provider != "" {
		t.Errorf("provider should be skipped without a durable key: %+v", results)
	}
	if !strings.Contains(out.String(), "no durable API key") {
		t.Errorf("skip reason missing from output:\n%s", out.String())
	}
}

// The minted key is written into another program's config file, so it must
// carry gateway verbs and nothing else. It is also stored under its own field:
// as api_key it would satisfy apiKeyConfigured, and every generated command
// would then authenticate with a gateway-scoped credential instead of the
// session that can actually reach the platform.
func TestMintedKeyIsGatewayScopedAndStoredSeparately(t *testing.T) {
	credsHarness(t)
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/api-keys/capabilities" {
			fmt.Fprint(w, `{"domains":[
			 {"id":"chat_completions","group":"DOMAIN_GROUP_GATEWAY","writable":true},
			 {"id":"model","group":"DOMAIN_GROUP_GATEWAY","readable":true},
			 {"id":"mcp_gateway","group":"DOMAIN_GROUP_GATEWAY","writable":true},
			 {"id":"member","group":"DOMAIN_GROUP_WORKSPACE_ADMIN","writable":true},
			 {"id":"prompt","group":"DOMAIN_GROUP_PLATFORM","writable":true}]}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"token":"sk-orq-KEYID1-secret","api_key":{"id":"KEYID1"}}`)
	}))
	defer srv.Close()

	ws := "acme"
	state := &authState{apiBase: srv.URL, bearer: "session-token",
		session: &auth.Session{ActiveWorkspaceKey: &ws, User: &auth.SessionUser{ID: "u1"}}}
	token, created, err := ensureDurableKey(newReporter(true), auth.NewClient(srv.URL), state, &setupOptions{noInput: true})
	if err != nil || !created || token == "" {
		t.Fatalf("mint: token=%q created=%v err=%v", token, created, err)
	}

	if body["permission_mode"] != "restricted" {
		t.Errorf("permission_mode = %v, want restricted", body["permission_mode"])
	}
	access, _ := body["access"].(map[string]any)
	for _, denied := range []string{"member", "prompt", "mcp_gateway"} {
		if _, granted := access[denied]; granted {
			t.Errorf("%s granted to a coding-agent key: %v", denied, access)
		}
	}
	if access["chat_completions"] != "write" || access["model"] != "read" {
		t.Errorf("gateway domains missing or mis-levelled: %v", access)
	}

	profile := bartolocli.GetProfile()
	if profile["gateway_key"] != token || profile["gateway_key_id"] != "KEYID1" {
		t.Errorf("profile = %v, want the key and its id under gateway_key*", profile)
	}
	if profile["api_key"] != "" {
		t.Error("the minted key was stored as api_key and will shadow the session")
	}
	if got, _ := savedAPIKey(); got != token {
		t.Errorf("savedAPIKey = %q, want the gateway key", got)
	}
}

// There is no unrestricted path any more. The permission map is local, so a
// mint cannot fall back to full permissions the way it did while the map came
// from an endpoint that 404s in production.
func TestMintIsAlwaysRestricted(t *testing.T) {
	credsHarness(t)
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "capabilities") {
			t.Errorf("mint called the capability endpoint: %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"token":"sk-orq-01HZXW2K7Y8Q9M0N1P2R3S4T5V-secret"}`)
	}))
	defer srv.Close()

	ws := "acme"
	state := &authState{apiBase: srv.URL, bearer: "t",
		session: &auth.Session{ActiveWorkspaceKey: &ws, User: &auth.SessionUser{ID: "u1"}}}
	token, created, err := ensureDurableKey(newReporter(true), auth.NewClient(srv.URL), state, &setupOptions{noInput: true})
	if err != nil || !created {
		t.Fatalf("mint: created=%v err=%v", created, err)
	}
	if body["permission_mode"] != "restricted" {
		t.Errorf("permission_mode = %v, want restricted with no fallback", body["permission_mode"])
	}
	// No id in the response: it still comes back, parsed out of the token.
	if id := savedGatewayKeyID(); id != "01HZXW2K7Y8Q9M0N1P2R3S4T5V" {
		t.Errorf("gateway_key_id = %q, want the id parsed from %q", id, token)
	}
	// source is the field the live endpoint actually acts on.
	if body["source"] != "router" {
		t.Errorf("source = %v, want router — without it the server mints permission_mode all", body["source"])
	}
}

// Upgrading must not re-mint. A profile written before the split holds api_key
// and no gateway_key; that key is live and wired into agent configs.
func TestLegacyAPIKeyProfileIsReusedNotReminted(t *testing.T) {
	credsHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected call %s %s — a legacy key was re-minted", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	if err := saveAPIKeyProfile("sk-orq-legacy", "acme"); err != nil {
		t.Fatal(err)
	}
	ws := "acme"
	state := &authState{apiBase: srv.URL, bearer: "t",
		session: &auth.Session{ActiveWorkspaceKey: &ws, User: &auth.SessionUser{ID: "u1"}}}
	token, created, err := ensureDurableKey(newReporter(true), auth.NewClient(srv.URL), state, &setupOptions{noInput: true})
	if err != nil {
		t.Fatal(err)
	}
	if created || token != "sk-orq-legacy" {
		t.Errorf("token=%q created=%v, want the saved legacy key reused", token, created)
	}
}

// Renewal exists so an agent never meets the 401 itself: the replacement is
// minted and wired while the old key is still valid. A key with no recorded
// expiry is one minted before expiry existed, or one the user brought — no
// expiry recorded means none known, and re-minting on that guess orphans a
// working credential.
func TestGatewayKeyRenewsBeforeExpiryOnly(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		expiresAt string
		want      bool
	}{
		"31 days left":     {now.Add(31 * 24 * time.Hour).Format(time.RFC3339), false},
		"29 days left":     {now.Add(29 * 24 * time.Hour).Format(time.RFC3339), true},
		"already expired":  {now.Add(-time.Hour).Format(time.RFC3339), true},
		"no expiry stored": {"", false},
		"unparseable":      {"whenever", false},
	} {
		t.Run(name, func(t *testing.T) {
			credsHarness(t)
			if err := saveGatewayKeyProfile("sk-orq-old", "KEYID", now.Add(90*24*time.Hour), "acme"); err != nil {
				t.Fatal(err)
			}
			bartolocli.Creds.Set("profiles.default.gateway_key_expires_at", tc.expiresAt)
			if got := gatewayKeyDueForRenewal(now); got != tc.want {
				t.Errorf("dueForRenewal = %v, want %v", got, tc.want)
			}
		})
	}
}

// The superseded key is deliberately not revoked: it stays valid until its own
// expiry, and that overlap is what keeps an agent config working until this run
// rewrites it.
func TestRenewalMintsWithoutRevokingTheOldKey(t *testing.T) {
	credsHarness(t)
	var mints int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			t.Errorf("renewal revoked the superseded key: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Path == "/v2/api-keys/capabilities" {
			fmt.Fprint(w, `{"domains":[{"id":"chat_completions","group":3,"writable":true}]}`)
			return
		}
		atomic.AddInt64(&mints, 1)
		fmt.Fprint(w, `{"token":"sk-orq-NEWID-secret","api_key":{"id":"NEWID"}}`)
	}))
	defer srv.Close()

	if err := saveGatewayKeyProfile("sk-orq-OLD", "OLDID", time.Now().Add(10*24*time.Hour), "acme"); err != nil {
		t.Fatal(err)
	}
	ws := "acme"
	state := &authState{apiBase: srv.URL, bearer: "t",
		session: &auth.Session{ActiveWorkspaceKey: &ws, User: &auth.SessionUser{ID: "u1"}}}
	token, created, err := ensureDurableKey(newReporter(true), auth.NewClient(srv.URL), state, &setupOptions{noInput: true})
	if err != nil {
		t.Fatal(err)
	}
	if !created || token != "sk-orq-NEWID-secret" {
		t.Fatalf("token=%q created=%v, want a freshly minted key", token, created)
	}
	if n := atomic.LoadInt64(&mints); n != 1 {
		t.Errorf("minted %d keys, want 1", n)
	}
	profile := bartolocli.GetProfile()
	if profile["gateway_key"] != token || profile["gateway_key_id"] != "NEWID" {
		t.Errorf("profile still holds the superseded key: %v", profile)
	}
	at, ok := gatewayKeyExpiry()
	if !ok || at.Before(time.Now().Add(80*24*time.Hour)) {
		t.Errorf("expiry = %v (recorded %v), want roughly 90 days out", at, ok)
	}
}

// The mint sends an expiry and records it locally, so the renewal check needs
// no network and works offline.
func TestMintRecordsExpiryLocallyAndRemotely(t *testing.T) {
	credsHarness(t)
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/api-keys/capabilities" {
			fmt.Fprint(w, `{"domains":[{"id":"chat_completions","group":3,"writable":true}]}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"token":"sk-orq-KEYID-secret","api_key":{"id":"KEYID"}}`)
	}))
	defer srv.Close()

	ws := "acme"
	state := &authState{apiBase: srv.URL, bearer: "t",
		session: &auth.Session{ActiveWorkspaceKey: &ws, User: &auth.SessionUser{ID: "u1"}}}
	if _, _, err := ensureDurableKey(newReporter(true), auth.NewClient(srv.URL), state, &setupOptions{noInput: true}); err != nil {
		t.Fatal(err)
	}

	sent, _ := body["expiration"].(string)
	at, err := time.Parse(time.RFC3339, sent)
	if err != nil {
		t.Fatalf("expiration %q is not RFC3339: %v", sent, err)
	}
	if days := time.Until(at).Hours() / 24; days < 89 || days > 91 {
		t.Errorf("expiry is %.1f days out, want 90", days)
	}
	stored, ok := gatewayKeyExpiry()
	if !ok || stored.Sub(at).Abs() > time.Minute {
		t.Errorf("stored expiry %v does not match the one sent %v", stored, at)
	}
}

// Wired agents all hold the same key, so the day it lapses they fail together.
// The countdown is what stops that being a surprise.
func TestGatewayKeyExpiryCheckBands(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		expiresAt time.Time
		want      string
	}{
		"healthy": {now.Add(60 * 24 * time.Hour), "pass"},
		"soon":    {now.Add(29 * 24 * time.Hour), "warn"},
		"expired": {now.Add(-24 * time.Hour), "fail"},
	} {
		t.Run(name, func(t *testing.T) {
			credsHarness(t)
			if err := saveGatewayKeyProfile("sk-orq-k", "KEYID", tc.expiresAt, "acme"); err != nil {
				t.Fatal(err)
			}
			check, ok := gatewayKeyExpiryCheck(now)
			if !ok {
				t.Fatal("no expiry row for a key that records one")
			}
			if check.Status != tc.want {
				t.Errorf("status = %q, want %q (%s)", check.Status, tc.want, check.Message)
			}
		})
	}

	// A key with no recorded expiry gets no row at all: silence is honest,
	// "expires in 0 days" would not be.
	credsHarness(t)
	if err := saveAPIKeyProfile("sk-orq-legacy", "acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok := gatewayKeyExpiryCheck(now); ok {
		t.Error("a key with no recorded expiry produced an expiry row")
	}
}

// A spend cap that stops an agent mid-task is worse than the leak it prevents
// on a key that already expires, and budgets are a workspace-scope RPC a
// Developer is refused. So the mint recommends and never calls.
func TestMintNeverCallsTheBudgetAPI(t *testing.T) {
	credsHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "budget") {
			t.Errorf("the mint path called the budget API: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Path == "/v2/api-keys/capabilities" {
			fmt.Fprint(w, `{"domains":[{"id":"chat_completions","group":3,"writable":true}]}`)
			return
		}
		fmt.Fprint(w, `{"token":"sk-orq-KEYID-secret","api_key":{"id":"KEYID"}}`)
	}))
	defer srv.Close()

	var out strings.Builder
	ws := "acme"
	state := &authState{apiBase: srv.URL, bearer: "t",
		session: &auth.Session{ActiveWorkspaceKey: &ws, User: &auth.SessionUser{ID: "u1"}}}
	if _, _, err := ensureDurableKey(&reporter{w: &out}, auth.NewClient(srv.URL), state, &setupOptions{noInput: true}); err != nil {
		t.Fatal(err)
	}
	// The key id is deliberately not printed: it is a detail of our storage, and
	// 'orq api-keys list' is where someone who needs it looks.
	if got := out.String(); strings.Contains(got, "KEYID") {
		t.Errorf("mint leaked the key id to the terminal:\n%s", got)
	}
}

// The dashboard lists every restricted key as "Restricted" without saying
// restricted to what, so the name is the only place a gateway key is
// distinguishable from the MCP key that will sit beside it. sanitizeKeyName
// truncates from the right, so the purpose has to survive a long hostname.
func TestMintedKeyNameCarriesItsPurpose(t *testing.T) {
	credsHarness(t)
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/api-keys/capabilities" {
			fmt.Fprint(w, `{"domains":[{"id":"chat_completions","group":3,"writable":true}]}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"token":"sk-orq-KEYID-secret","api_key":{"id":"KEYID"}}`)
	}))
	defer srv.Close()

	ws := "acme"
	state := &authState{apiBase: srv.URL, bearer: "t",
		session: &auth.Session{ActiveWorkspaceKey: &ws, User: &auth.SessionUser{ID: "u1"}}}
	if _, _, err := ensureDurableKey(newReporter(true), auth.NewClient(srv.URL), state, &setupOptions{noInput: true}); err != nil {
		t.Fatal(err)
	}
	name, _ := body["name"].(string)
	if !strings.HasPrefix(name, "orq-cli gateway ") {
		t.Errorf("key name = %q, want it to lead with its purpose", name)
	}

	// A hostname long enough to hit the 64-char cap still leaves the purpose
	// readable — it is the hostname that gets cut, not the discriminator.
	long := sanitizeKeyName("orq-cli gateway " + strings.Repeat("prod-euw1-build-agent-pool-a", 4))
	if len(long) != 64 || !strings.HasPrefix(long, "orq-cli gateway ") {
		t.Errorf("truncated name = %q (%d chars), want the purpose preserved", long, len(long))
	}
}

// The env file is the one artefact that survived logout: a shell profile that
// sources it kept exporting a live key into every new shell, so "signed out"
// left the machine authenticated. It is emptied rather than deleted, because a
// profile carrying `. ~/.orq/env` would then error on every new shell.
func TestClearShellEnvFileEmptiesRatherThanDeletes(t *testing.T) {
	dir := t.TempDir()
	viper.Set("config-directory", dir)
	t.Cleanup(func() { viper.Set("config-directory", "") })

	posix := filepath.Join(dir, "env")
	fish := filepath.Join(dir, "env.fish")
	if err := os.WriteFile(posix, []byte("export ORQ_API_KEY=sk-orq-LIVE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A user who changed shells since setup has the other spelling lying around.
	if err := os.WriteFile(fish, []byte("set -gx ORQ_API_KEY sk-orq-LIVE\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleared := clearShellEnvFile()
	if len(cleared) != 2 {
		t.Fatalf("cleared %v, want both spellings", cleared)
	}
	for _, path := range []string{posix, fish} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was deleted; a profile sourcing it would now error: %v", path, err)
		}
		body := string(mustRead(t, path))
		if strings.Contains(body, "sk-orq-LIVE") || strings.Contains(body, "ORQ_API_KEY") {
			t.Errorf("%s still exports the key:\n%s", path, body)
		}
	}

	// Nothing to clear is not an error, and reports nothing.
	if got := clearShellEnvFile(); len(got) != 0 {
		t.Errorf("second run reported %v, want nothing", got)
	}
}

// The logout fix landed with no test: deleting the gateway-clearing lines left
// the whole suite green, which means "signed out" could go back to leaving a
// live gateway key in credentials.json without anything noticing.
func TestClearAPIKeyProfileClearsBothCredentials(t *testing.T) {
	t.Run("gateway key", func(t *testing.T) {
		credsHarness(t)
		if err := saveGatewayKeyProfile("sk-orq-K", "01HZXW2K7Y8Q9M0N1P2R3S4T5V", time.Now().Add(90*24*time.Hour), "acme"); err != nil {
			t.Fatal(err)
		}
		held, err := clearAPIKeyProfile()
		if err != nil || !held {
			t.Fatalf("held=%v err=%v, want a cleared gateway key", held, err)
		}
		profile := bartolocli.GetProfile()
		for _, field := range []string{"api_key", "gateway_key"} {
			if profile[field] != "" {
				t.Errorf("%s survived logout: %q", field, profile[field])
			}
		}
		if key, _ := savedAPIKey(); key != "" {
			t.Errorf("savedAPIKey still returns %q after logout", key)
		}
		// The key outlives the session: logout does not revoke it server-side.
		// The id and the expiry authenticate nothing, and they are the only
		// record of what is still live, so they are kept on purpose.
		for _, field := range []string{"gateway_key_id", "gateway_key_expires_at"} {
			if profile[field] == "" {
				t.Errorf("%s was discarded, leaving no handle to revoke the surviving key", field)
			}
		}
	})

	t.Run("legacy api key", func(t *testing.T) {
		credsHarness(t)
		if err := saveAPIKeyProfile("sk-orq-legacy", "acme"); err != nil {
			t.Fatal(err)
		}
		held, err := clearAPIKeyProfile()
		if err != nil || !held {
			t.Fatalf("held=%v err=%v, want a cleared legacy key", held, err)
		}
	})

	t.Run("nothing stored", func(t *testing.T) {
		credsHarness(t)
		if held, err := clearAPIKeyProfile(); held || err != nil {
			t.Errorf("held=%v err=%v, want (false, nil)", held, err)
		}
	})
}

// A key passed with --api-key must be the one later commands wire. savedAPIKey
// prefers gateway_key, so leaving a previously minted key in place made the
// supplied key silently lose — and only on the *next* command, which is the
// worst possible latency for this class of bug.
func TestSuppliedKeyDisplacesAMintedOne(t *testing.T) {
	credsHarness(t)
	if err := saveGatewayKeyProfile("sk-orq-MINTED", "01HZXW2K7Y8Q9M0N1P2R3S4T5V", time.Now().Add(90*24*time.Hour), "acme"); err != nil {
		t.Fatal(err)
	}
	if err := saveAPIKeyProfile("sk-orq-SUPPLIED", ""); err != nil {
		t.Fatal(err)
	}
	if key, _ := savedAPIKey(); key != "sk-orq-SUPPLIED" {
		t.Errorf("savedAPIKey = %q, want the key the user just supplied", key)
	}
	if id := savedGatewayKeyID(); id != "" {
		t.Errorf("stale gateway_key_id %q survived; it names a key no longer in use", id)
	}
	if _, ok := gatewayKeyExpiry(); ok {
		t.Error("stale expiry survived, so renewal would fire against a key that is not wired")
	}
}

// The mirror of TestSuppliedKeyDisplacesAMintedOne. Versions before the
// credential split wrote the minted key to api_key with a workspace beside it,
// so an upgraded machine that switches workspace mints a new gateway key while
// the old one survives. apiKeyConfigured reads only api_key, so leaving it
// there suppresses session injection and every generated command authenticates
// with a stale key for the workspace the user just left.
func TestMintedKeyDisplacesALegacyOne(t *testing.T) {
	credsHarness(t)
	if err := saveAPIKeyProfile("sk-orq-LEGACY-for-A", "workspace-A"); err != nil {
		t.Fatal(err)
	}
	if err := saveGatewayKeyProfile("sk-orq-NEW-for-B", "01HZXW2K7Y8Q9M0N1P2R3S4T5V", time.Now().Add(90*24*time.Hour), "workspace-B"); err != nil {
		t.Fatal(err)
	}
	if storedAPIKeyProfile() {
		t.Error("api_key survived the mint; apiKeyConfigured would suppress session injection and commands would use the stale key")
	}
	if key, ws := savedAPIKey(); key != "sk-orq-NEW-for-B" || ws != "workspace-B" {
		t.Errorf("savedAPIKey = (%q, %q), want the key just minted for workspace-B", key, ws)
	}
}

// A gateway key authenticates no `orq <entity>` command, so counting it here
// made doctor answer "authenticated: credentials.json" on the one screen whose
// job is to explain why nothing works.
func TestStoredAPIKeyProfileIgnoresTheGatewayKey(t *testing.T) {
	credsHarness(t)
	if err := saveGatewayKeyProfile("sk-orq-GATEWAY", "01HZXW2K7Y8Q9M0N1P2R3S4T5V", time.Now().Add(90*24*time.Hour), "acme"); err != nil {
		t.Fatal(err)
	}
	if storedAPIKeyProfile() {
		t.Error("a gateway-key-only profile reported as holding an authenticating key")
	}
	if err := saveAPIKeyProfile("sk-orq-REAL", "acme"); err != nil {
		t.Fatal(err)
	}
	if !storedAPIKeyProfile() {
		t.Error("a profile holding api_key reported as holding none")
	}
}

// Declining the mint leaves the agent unwired, so the run did not do what it
// was asked. Reporting it complete printed a green verdict directly above the
// warning that nothing was written, and told a CI job the run succeeded.
func TestSetupIsNotCompleteWhenAnAgentWasSkipped(t *testing.T) {
	if setupComplete(true, []agentResult{{Agent: "kimi", Skipped: "no durable API key"}}) {
		t.Error("a skipped agent counted as a completed setup")
	}
	if setupComplete(true, []agentResult{{Agent: "codex", Provider: "/tmp/x"}, {Agent: "kimi", Skipped: "no models to offer"}}) {
		t.Error("one skipped agent among wired ones counted as a completed setup")
	}
	if !setupComplete(true, []agentResult{{Agent: "claude"}, {Agent: "codex", Provider: "/tmp/x"}}) {
		t.Error("claude has no provider config to write, so an empty result for it is not a skip")
	}
}

// A skip is only a skip when nothing on disk wires the agent afterwards.
// Declining the mint on a machine an earlier run already wired leaves that wire
// working, so failing the run would report a break that did not happen.
func TestDecliningTheMintIsNotASkipWhenAnEarlierWireSurvives(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	resetSetupMemos(t)
	path := filepath.Join(home, ".kimi-code", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	spec, ok := lookupAgent("kimi")
	if !ok {
		t.Fatal("no kimi spec")
	}
	state := &authState{}

	var res agentResult
	writeAgentProvider(&reporter{w: io.Discard}, nil, state, &setupOptions{}, spec, &res)
	if res.Skipped == "" {
		t.Error("no durable key and no provider config on disk: the agent is unwired, so this is a skip")
	}

	if _, err := writeKimiProviderTOML(path, "https://api.orq.ai/v3/router", "sk-old", openCodeModels(), ""); err != nil {
		t.Fatal(err)
	}
	res = agentResult{}
	writeAgentProvider(&reporter{w: io.Discard}, nil, state, &setupOptions{}, spec, &res)
	if res.Skipped != "" {
		t.Errorf("an earlier run's provider block still wires kimi, got Skipped=%q", res.Skipped)
	}
}

// --no-input stops after the key and reports no agents at all. It promised
// none, so it is complete; only a run that named agents and did not deliver
// them is not.
func TestARunThatNeverReachedTheAgentStepIsComplete(t *testing.T) {
	if !setupComplete(true, nil) {
		t.Error("a run reporting no agents was marked incomplete")
	}
}

// The rule the quiet pass encodes: a path or an id may appear only inside a
// command the user should run, never as a report of what the CLI just did.
// Wiring an agent used to print the config file it wrote, the credentials file,
// the env file and the key id; all of that lives in 'orq doctor' and
// 'orq connect --status' now. Asserting it here so the next verbose line fails
// rather than quietly landing.
func TestWiringReportsNoPathsOrIDs(t *testing.T) {
	var out strings.Builder
	rep := &reporter{w: &out}
	kimi, _ := lookupAgent("kimi")
	codex, _ := lookupAgent("codex")

	reportAgent(rep, kimi, agentResult{Agent: "kimi", Provider: "/home/u/.kimi-code/config.toml", ModelCount: 137}, &setupOptions{})
	reportAgent(rep, codex, agentResult{Agent: "codex", Provider: "/home/u/.codex/orq.config.toml"}, &setupOptions{})

	got := out.String()
	for _, leaked := range []string{"config.toml", "/home/u", "credentials.json", ".orq/env"} {
		if strings.Contains(got, leaked) {
			t.Errorf("wiring output names %q, which belongs in doctor:\n%s", leaked, got)
		}
	}
	// It still has to say what happened, and whose gateway it is.
	for _, want := range []string{"Orq AI Gateway", "kimi", "codex", "137 models available"} {
		if !strings.Contains(got, want) {
			t.Errorf("wiring output lost %q:\n%s", want, got)
		}
	}
	// codex takes its model list elsewhere, so it must not claim a count.
	if strings.Contains(got, "0 models") {
		t.Errorf("codex claimed a model count it does not have:\n%s", got)
	}
}

// ============================================================================
// Capability picker (Task 11)
// ============================================================================

func TestSetupDefaultCapabilitiesIncludeSkills(t *testing.T) {
	requireSkillsCapability(t)
	caps := defaultCapabilities()
	if !hasCap(caps, capSkills) {
		t.Errorf("defaults = %v, want skills included", caps)
	}
	if !hasCap(caps, capGateway) {
		t.Errorf("defaults = %v, want gateway included", caps)
	}
	if hasCap(caps, capTracing) {
		t.Errorf("defaults = %v, want tracing excluded while it is unbuilt", caps)
	}
}

func TestSetupNonInteractiveUsesTheDefaultCapabilities(t *testing.T) {
	requireSkillsCapability(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	resetSetupMemos(t)

	opts := &setupOptions{noInput: true}
	caps, err := resolveCapabilities(newReporter(true), opts)
	if err != nil {
		t.Fatalf("resolveCapabilities: %v", err)
	}
	if !hasCap(caps, capSkills) {
		t.Errorf("caps = %v, want skills", caps)
	}
	if !hasCap(caps, capGateway) {
		t.Errorf("caps = %v, want gateway", caps)
	}
}

// An explicit --capability flag (opts.caps already set) wins over both the
// prompt and the defaults: it is the CI escape hatch, and must not block on a
// TTY that does not exist.
func TestSetupExplicitCapabilitiesWinOverDefaults(t *testing.T) {
	requireSkillsCapability(t)
	opts := &setupOptions{noInput: true, caps: []string{capSkills}}
	caps, err := resolveCapabilities(newReporter(true), opts)
	if err != nil {
		t.Fatalf("resolveCapabilities: %v", err)
	}
	if len(caps) != 1 || caps[0] != capSkills {
		t.Errorf("caps = %v, want the explicit [skills] untouched", caps)
	}
}

// The explicit flag wins over the defaults, but not over availability: it used
// to pass `tracing` straight through, leaving setup to complete "successfully"
// having connected nothing and never having said why.
func TestSetupExplicitCapabilitiesStillDropTheUnavailable(t *testing.T) {
	opts := &setupOptions{noInput: true, caps: []string{capTracing}}
	caps, err := resolveCapabilities(newReporter(true), opts)
	if err != nil {
		t.Fatalf("resolveCapabilities: %v", err)
	}
	if len(caps) != 0 {
		t.Errorf("caps = %v, want tracing dropped as unavailable", caps)
	}
}

// --yes takes the affirmative default without asking, same as opts.confirm.
func TestSetupYesUsesTheDefaultCapabilities(t *testing.T) {
	requireSkillsCapability(t)
	opts := &setupOptions{yes: true}
	caps, err := resolveCapabilities(newReporter(true), opts)
	if err != nil {
		t.Fatalf("resolveCapabilities: %v", err)
	}
	if !hasCap(caps, capSkills) || !hasCap(caps, capGateway) {
		t.Errorf("caps = %v, want the defaults", caps)
	}
}

// setup's step 3 printed a line per wire and then the final screen printed the
// same wires again, so every agent was reported twice on one screenful. The
// progress lines belong to connect, which has no final screen; setup keeps the
// screen and the model count moves onto it.
func TestSetupDoesNotReportEachWireTwice(t *testing.T) {
	kimi, _ := lookupAgent("kimi")
	res := agentResult{Agent: "kimi", Provider: "/home/u/.kimi-code/config.toml", ModelCount: 137}

	var quiet strings.Builder
	reportAgent(&reporter{w: &quiet}, kimi, res, &setupOptions{finalScreen: true})
	if quiet.String() != "" {
		t.Errorf("a run that ends on the final screen still printed the wire:\n%s", quiet.String())
	}

	// connect has no final screen, so there the line is the only report.
	var loud strings.Builder
	reportAgent(&reporter{w: &loud}, kimi, res, &setupOptions{})
	if !strings.Contains(loud.String(), "137 models available") {
		t.Errorf("connect lost its only report of the wire:\n%s", loud.String())
	}

	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORQ_API_KEY", "sk-orq-set")
	var screen strings.Builder
	printFinalScreen(&reporter{w: &screen}, []agentResult{res}, map[string]string{}, true, &setupOptions{})
	if !strings.Contains(screen.String(), "(137 models)") {
		t.Errorf("the count has nowhere left to appear:\n%s", screen.String())
	}
}
