package custom

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// The key `orq setup` exports into ~/.orq/env is ours, not a user override, so
// it must not outrank the session — that is what made `orq workspace use` and
// `orq projects use` silent no-ops after setup (RES-1465). Anything else keeps
// winning.
func TestOwnExportedKeyDefersOnlyToOurOwnKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	creds, err := bartolocli.NewCredentialsFile(t.TempDir())
	if err != nil {
		t.Fatalf("NewCredentialsFile: %v", err)
	}
	// Restore rather than nil out: other tests in this package share the
	// global and a nil one silently changes what they are testing.
	prevCreds := bartolocli.Creds
	bartolocli.Creds = creds
	t.Cleanup(func() { bartolocli.Creds = prevCreds })
	writeSession := func() {
		dir := filepath.Join(home, ".orq", "sessions")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "my.orq.ai.json"), []byte(`{"version":1,"apiBaseUrl":"https://api.orq.ai","v1BaseUrl":"https://api.orq.ai/v1","authBaseUrl":"https://api.orq.ai/v2/auth","profileBaseUrl":"https://api.orq.ai/v2/auth/profile","user":{"id":"u"},"workspaces":[],"activeWorkspaceKey":"acme","refreshToken":"r","bootstrapToken":{"token":"t","expiresAt":"2099-01-01T00:00:00Z"},"workspaceTokens":{},"gatewayKey":"sk-orq-OURS"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// No session: the exported key is the only credential there is.
	t.Setenv("ORQ_API_KEY", "sk-orq-OURS")
	if ownExportedKey() {
		t.Error("without a session the exported key must keep winning")
	}

	writeSession()
	if !ownExportedKey() {
		t.Error("our own exported key must defer to the session")
	}

	// A key we did not mint is a deliberate override.
	t.Setenv("ORQ_API_KEY", "sk-orq-THEIRS")
	if ownExportedKey() {
		t.Error("a key we did not mint must keep winning")
	}

	// A credentials profile is an explicit choice too.
	t.Setenv("ORQ_API_KEY", "sk-orq-OURS")
	prevProfile := viper.GetString("profile")
	viper.Set("profile", "default")
	t.Cleanup(func() { viper.Set("profile", prevProfile) })
	creds.Set("profiles.default.api_key", "sk-orq-PROFILE")
	if ownExportedKey() {
		t.Error("a profile api_key must keep winning")
	}
}

// setProjectRef points --project/ORQ_PROJECT at ref for one test. The viper key
// is a process global shared with every other test in the package, so the
// previous value is restored rather than zeroed.
func setProjectRef(t *testing.T, ref string) {
	t.Helper()
	prev := viper.GetString("project")
	viper.Set("project", ref)
	t.Cleanup(func() { viper.Set("project", prev) })
}

// projectIDCommand is a stand-in for one of the 31 generated commands the
// generator puts --project-id on.
func projectIDCommand(t *testing.T, value string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().String("project-id", "", "")
	if value != "" {
		if err := cmd.Flags().Set("project-id", value); err != nil {
			t.Fatal(err)
		}
	}
	return cmd
}

// An explicitly typed --project-id is the top of the stated precedence, above
// the session's active project. It used to lose to it: after `orq projects use
// banking`, a command run with --project-id <marketing> minted a token scoped
// to banking and answered with banking's rows, and filed creates there too.
func TestExplicitProjectIDOutranksTheActiveProject(t *testing.T) {
	setProjectRef(t, "")
	const marketing = "11111111-2222-3333-4444-555555555555"
	session := &auth.Session{ActiveProjectID: "99999999-8888-7777-6666-555555555555", ActiveProjectName: "Banking"}

	got, err := resolveProjectID(projectIDCommand(t, marketing), session, "")
	if err != nil {
		t.Fatalf("resolveProjectID: %v", err)
	}
	if got != marketing {
		t.Errorf("token narrowed to %q, want the explicitly passed --project-id %q", got, marketing)
	}

	// --project-id beats --project too, and neither may reach the session's
	// project once one of them was typed.
	setProjectRef(t, "banking")
	got, err = resolveProjectID(projectIDCommand(t, marketing), session, "")
	if err != nil {
		t.Fatalf("resolveProjectID with --project as well: %v", err)
	}
	if got != marketing {
		t.Errorf("token narrowed to %q, want --project-id to outrank --project (%q)", got, marketing)
	}
}

// A --project-id the token exchange cannot take must not fall back to the
// session's project: that would send a token for one project alongside a
// project_id parameter for another. The token stays workspace-wide and the flag
// travels as a plain request parameter.
func TestNonIDProjectIDLeavesTheTokenUnnarrowed(t *testing.T) {
	setProjectRef(t, "")
	session := &auth.Session{ActiveProjectID: "99999999-8888-7777-6666-555555555555"}

	got, err := resolveProjectID(projectIDCommand(t, "banking"), session, "")
	if err != nil {
		t.Fatalf("resolveProjectID: %v", err)
	}
	if got != "" {
		t.Errorf("token narrowed to %q, want it left unnarrowed", got)
	}
}

// Nothing typed leaves the session's active project in charge — the rung below
// the two flags, and the one every plain `orq ...` invocation lands on.
func TestNoProjectFlagsKeepTheActiveProject(t *testing.T) {
	setProjectRef(t, "")
	session := &auth.Session{ActiveProjectID: "banking-id"}

	got, err := resolveProjectID(projectIDCommand(t, ""), session, "")
	if err != nil {
		t.Fatalf("resolveProjectID: %v", err)
	}
	if got != "banking-id" {
		t.Errorf("resolved %q, want the session's active project banking-id", got)
	}
}

// Resolving `--project <key-or-name>` has to authenticate with whatever
// credential the user actually configured. It read ORQ_API_KEY and nothing
// else, so an ORQ_TOKEN / ORQ_AUTHORIZATION / credentials-profile user got a
// 401 out of a flag that should have cost them nothing.
func TestBridgeProjectFlagUsesAnyConfiguredCredential(t *testing.T) {
	for _, tc := range []struct {
		name, envVar, envKey, profileKey string
	}{
		{name: "ORQ_API_KEY", envVar: "ORQ_API_KEY", envKey: "sk-env-api-key"},
		{name: "ORQ_TOKEN", envVar: "ORQ_TOKEN", envKey: "sk-env-token"},
		{name: "ORQ_AUTHORIZATION", envVar: "ORQ_AUTHORIZATION", envKey: "sk-env-authorization"},
		{name: "credentials profile", profileKey: "sk-profile-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.envKey
			if want == "" {
				want = tc.profileKey
			}
			var seen string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				if seen != "Bearer "+want {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, `{"error":"unauthorized"}`)
					return
				}
				fmt.Fprint(w, `{"data":[{"project_id":"11111111-2222-3333-4444-555555555555","key":"banking","name":"Banking"}],"has_more":false}`)
			}))
			t.Cleanup(srv.Close)

			for _, envVar := range apiKeyEnvVars {
				t.Setenv(envVar, "")
			}
			if tc.envVar != "" {
				t.Setenv(tc.envVar, tc.envKey)
			}
			prevProfile := viper.GetString("profile")
			viper.Set("profile", "")
			if tc.profileKey != "" {
				viper.Set("profile", "default")
			}
			t.Cleanup(func() { viper.Set("profile", prevProfile) })
			creds, err := bartolocli.NewCredentialsFile(t.TempDir())
			if err != nil {
				t.Fatalf("NewCredentialsFile: %v", err)
			}
			// Restore rather than nil out: the global is shared with the rest
			// of the package.
			prevCreds := bartolocli.Creds
			bartolocli.Creds = creds
			t.Cleanup(func() { bartolocli.Creds = prevCreds })
			if tc.profileKey != "" {
				creds.Set("profiles.default.api_key", tc.profileKey)
			}
			prevServer, prevSource := auth.Server(), auth.ServerSource()
			auth.SetServer(srv.URL, "test")
			t.Cleanup(func() { auth.SetServer(prevServer, prevSource) })

			setProjectRef(t, "banking")
			cmd := projectIDCommand(t, "")
			if err := bridgeProjectFlag(cmd, nil); err != nil {
				t.Fatalf("bridgeProjectFlag authenticated as %q: %v", seen, err)
			}
			if got := cmd.Flags().Lookup("project-id").Value.String(); got != "11111111-2222-3333-4444-555555555555" {
				t.Errorf("--project-id = %q, want the id --project resolved to", got)
			}
		})
	}
}

// The generated --project bridge is part of the same credential decision as
// Bartolo's request handler. A selected profile must choose both its server and
// its key even when the shell exports a different pair; splitting those two
// decisions can send one deployment's key to another deployment.
func TestGeneratedProjectNameUsesProfileKeyAndServerOverEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	var profileRequests, envRequests int
	var profilePaths []string
	profileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profileRequests++
		profilePaths = append(profilePaths, r.URL.RequestURI())
		if got := r.Header.Get("Authorization"); got != "Bearer sk-profile" {
			t.Errorf("profile server Authorization = %q, want the profile key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2/projects" {
			fmt.Fprint(w, `{"data":[{"project_id":"11111111-2222-3333-4444-555555555555","key":"banking","name":"Banking"}],"has_more":false}`)
			return
		}
		fmt.Fprint(w, `{"data":[],"has_more":false}`)
	}))
	t.Cleanup(profileServer.Close)
	envServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		envRequests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"project_id":"11111111-2222-3333-4444-555555555555","key":"banking","name":"Banking"}],"has_more":false}`)
	}))
	t.Cleanup(envServer.Close)

	t.Setenv("ORQ_API_KEY", "sk-environment")
	t.Setenv("ORQ_SERVER", envServer.URL)
	root := buildRoot(t)
	bartolocli.Creds.Set("profiles.acme.api_key", "sk-profile")
	bartolocli.Creds.Set("profiles.acme.server", profileServer.URL)
	bartolocli.Creds.Set("profiles.acme.type", "")
	// buildRoot is used repeatedly in this package, while viper's programmatic
	// values are process-global. Pin the value the flag represents so this
	// integration remains about credential/server routing rather than prior
	// tests' binding order.
	setProjectRef(t, "banking")
	root.SetArgs([]string{"--profile", "acme", "--project", "banking", "alerts", "list"})
	var runErr error
	captureOutput(t, func() { runErr = root.Execute() })
	if runErr != nil {
		t.Fatalf("generated command: %v", runErr)
	}
	if envRequests != 0 {
		t.Errorf("environment server received %d requests, want none", envRequests)
	}
	if profileRequests < 2 {
		t.Errorf("profile server received %d requests (%v), want project lookup and generated request", profileRequests, profilePaths)
	}
}

// Setup has its own HTTP client rather than Bartolo's generated request path,
// so it needs an integration pin of the same profile-first decision. The
// environment server deliberately answers successfully: the test must detect
// routing to the wrong host, not pass only because that host rejected the key.
func TestSetupUsesProfileKeyAndServerOverEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	var profileRequests, envRequests int
	profileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profileRequests++
		if got := r.Header.Get("Authorization"); got != "Bearer sk-profile" {
			t.Errorf("profile server Authorization = %q, want the profile key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"settings":{"key":"acme"}}`)
	}))
	t.Cleanup(profileServer.Close)
	envServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		envRequests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"settings":{"key":"environment"}}`)
	}))
	t.Cleanup(envServer.Close)

	t.Setenv("ORQ_API_KEY", "sk-environment")
	t.Setenv("ORQ_SERVER", envServer.URL)
	root := buildRoot(t)
	bartolocli.Creds.Set("profiles.acme.api_key", "sk-profile")
	bartolocli.Creds.Set("profiles.acme.server", profileServer.URL)
	bartolocli.Creds.Set("profiles.acme.type", "")
	root.SetArgs([]string{"--profile", "acme", "--no-input", "setup", "--no-project"})
	var runErr error
	captureOutput(t, func() { runErr = root.Execute() })
	if runErr != nil {
		t.Fatalf("setup: %v", runErr)
	}
	if envRequests != 0 {
		t.Errorf("environment server received %d requests, want none", envRequests)
	}
	if profileRequests < 2 {
		t.Errorf("profile server received %d requests, want saved-key check and verification", profileRequests)
	}
}
