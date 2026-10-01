package custom

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"orq/cli/custom/auth"
	"orq/cli/custom/commands"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

func TestNamedAPIKeyLoginResolvesOnNextCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	previousRoot := bartolocli.Root
	previousProfile := viper.GetString("profile")
	previousViperServer := viper.GetString("server")
	previousServer, previousSource := auth.Server(), auth.ServerSource()
	previousExplicit := commands.UsingExplicitAPIKey()
	t.Cleanup(func() {
		bartolocli.Root = previousRoot
		viper.Set("profile", previousProfile)
		viper.Set("server", previousViperServer)
		auth.SetServer(previousServer, previousSource)
		commands.SetExplicitAPIKey(previousExplicit)
	})
	for _, name := range apiKeyEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv("ORQ_PROFILE", "")

	nextCommand := false
	nextRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if nextCommand {
			nextRequests++
			if got := r.Header.Get("Authorization"); got != "Bearer sk-orq-named" {
				t.Errorf("next command Authorization = %q, want named profile key", got)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
	}))
	t.Cleanup(srv.Close)

	root := buildRoot(t)
	t.Cleanup(func() {
		for _, name := range []string{"profile", "server"} {
			flag := root.PersistentFlags().Lookup(name)
			flag.Changed = false
		}
	})
	root.SetArgs([]string{"--server", srv.URL, "--profile", "work", "auth", "login", "--api-key", "sk-orq-named"})
	var loginErr error
	captureOutput(t, func() { loginErr = root.Execute() })
	if loginErr != nil {
		t.Fatalf("login under --profile work: %v", loginErr)
	}
	if got := bartolocli.Creds.GetString("profiles.work.api_key"); got != "sk-orq-named" {
		t.Fatalf("saved profile key = %q, want sk-orq-named", got)
	}

	nextCommand = true
	root.SetArgs([]string{"--server", srv.URL, "--profile", "work", "models", "list"})
	var listErr error
	captureOutput(t, func() { listErr = root.Execute() })
	if listErr != nil {
		t.Fatalf("models list with saved profile: %v", listErr)
	}
	if nextRequests == 0 {
		t.Error("next command made no authenticated request")
	}
}

// apiKeyLoginHarness isolates HOME and credentials so a test can drive the
// api-key login store and PreRun injection without touching the real ~/.orq or
// leaking a selected profile from a sibling test.
func apiKeyLoginHarness(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	previousServer, previousSource := auth.Server(), auth.ServerSource()
	auth.SetServer("", "default")
	t.Cleanup(func() { auth.SetServer(previousServer, previousSource) })

	creds, err := bartolocli.NewCredentialsFile(t.TempDir())
	if err != nil {
		t.Fatalf("NewCredentialsFile: %v", err)
	}
	prevCreds := bartolocli.Creds
	bartolocli.Creds = creds
	t.Cleanup(func() { bartolocli.Creds = prevCreds })

	prevProfile := viper.GetString("profile")
	viper.Set("profile", "")
	t.Cleanup(func() { viper.Set("profile", prevProfile) })

	// A clean environment: no user-supplied key of any spelling.
	for _, v := range apiKeyEnvVars {
		t.Setenv(v, "")
	}
}

// The regression this ticket is about: `orq auth login --api-key` stores a key,
// and on the next command — with no ORQ_API_KEY and no selected profile — that
// key must resolve and be injected into ORQ_API_KEY. Before this fix the key
// went to an unselected `default` profile that nothing resolved, so this
// injection never happened and the user was not actually logged in.
func TestStoredAPIKeyLoginInjectsIntoEnvOnCleanEnvironment(t *testing.T) {
	apiKeyLoginHarness(t)

	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{
		APIBaseURL: auth.ResolveURLs("").APIBaseURL,
		APIKey:     "sk-orq-LOGIN",
	}); err != nil {
		t.Fatalf("SaveAPIKeyLogin: %v", err)
	}

	// Sanity: the environment really is clean before injection, so a pass below
	// is the stored login being read — not a leaked env key.
	if v := os.Getenv("ORQ_API_KEY"); v != "" {
		t.Fatalf("ORQ_API_KEY should start empty, got %q", v)
	}

	applyStoredAPIKeyLogin()

	if got := os.Getenv("ORQ_API_KEY"); got != "sk-orq-LOGIN" {
		t.Fatalf("stored api-key login was not injected: ORQ_API_KEY = %q, want sk-orq-LOGIN", got)
	}
	// The key is ours, injected by us, so the "Using ORQ_API_KEY from
	// environment" notice must stay silent.
	if !ownExportedKey() {
		t.Error("an injected api-key login must be treated as own-exported so the env notice stays silent")
	}
}

// A user-supplied key in the environment is a deliberate override and must stay
// authoritative: the stored login never displaces it.
func TestUserSuppliedKeyOutranksStoredAPIKeyLogin(t *testing.T) {
	apiKeyLoginHarness(t)
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{APIKey: "sk-orq-LOGIN"}); err != nil {
		t.Fatalf("SaveAPIKeyLogin: %v", err)
	}
	t.Setenv("ORQ_API_KEY", "sk-orq-USER")

	applyStoredAPIKeyLogin()

	if got := os.Getenv("ORQ_API_KEY"); got != "sk-orq-USER" {
		t.Errorf("user key was displaced: ORQ_API_KEY = %q, want sk-orq-USER", got)
	}
	// A key we did not mint is not own-exported, so it wins and the notice fires.
	if ownExportedKey() {
		t.Error("a user-supplied key must not be treated as own-exported")
	}
}

// A selected profile is a complete credential bartolo resolves itself; the
// stored api-key login must not fight it.
func TestSelectedProfileOutranksStoredAPIKeyLogin(t *testing.T) {
	apiKeyLoginHarness(t)
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{APIKey: "sk-orq-LOGIN"}); err != nil {
		t.Fatalf("SaveAPIKeyLogin: %v", err)
	}
	viper.Set("profile", "acme")
	bartolocli.Creds.Set("profiles.acme.api_key", "sk-orq-PROFILE")

	applyStoredAPIKeyLogin()

	// applyStoredAPIKeyLogin short-circuits on a profile in force, so it exports
	// nothing here (applyProfileAPIKey handles the profile key separately).
	if got := os.Getenv("ORQ_API_KEY"); got == "sk-orq-LOGIN" {
		t.Error("stored api-key login displaced a selected profile")
	}
}

// The old code wrote the key to a bartolo profile named `default` that nothing
// selected. This pins that the login path no longer does that: after a login
// there is no `default` profile, only the host-keyed api-key login file.
func TestAPIKeyLoginWritesNoDefaultProfile(t *testing.T) {
	apiKeyLoginHarness(t)
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{APIKey: "sk-orq-LOGIN"}); err != nil {
		t.Fatalf("SaveAPIKeyLogin: %v", err)
	}
	if bartolocli.ProfileExists("default") {
		t.Error("an api-key login must not create a `default` profile")
	}
	// The credential lives in the host-keyed store instead.
	if _, err := os.Stat(auth.APIKeyLoginFilePath()); err != nil {
		t.Errorf("api-key login file missing: %v", err)
	}
}

// The latest API-key login is active even when a browser session remains on
// disk. A gateway key sourced from ~/.orq/env belongs to that older session
// and must not keep the new login from taking effect.
func TestStoredAPIKeyLoginOutranksBrowserSessionGatewayKey(t *testing.T) {
	apiKeyLoginHarness(t)

	// A browser session for this host, carrying a gateway key so ownExportedKeys
	// has the session side to compare against.
	urls := auth.ResolveURLs("")
	session := &auth.Session{
		Version:        1,
		APIBaseURL:     urls.APIBaseURL,
		V1BaseURL:      urls.V1BaseURL,
		AuthBaseURL:    urls.AuthBaseURL,
		ProfileBaseURL: urls.ProfileBaseURL,
		RefreshToken:   "refresh-abc",
		BootstrapToken: auth.StoredAccessToken{Token: "boot", ExpiresAt: "2099-01-01T00:00:00Z"},
		GatewayKey:     "sk-orq-GATEWAY",
	}
	if err := auth.SaveSession(session); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{
		APIBaseURL: auth.ResolveURLs("").APIBaseURL,
		APIKey:     "sk-orq-LOGIN",
	}); err != nil {
		t.Fatalf("SaveAPIKeyLogin: %v", err)
	}

	t.Setenv("ORQ_API_KEY", "sk-orq-GATEWAY")
	if !applyStoredAPIKeyLogin() {
		t.Fatal("stored API-key login should be active after replacing the gateway key")
	}

	// The login key is injected (no profile, no user key to defer to)...
	if got := os.Getenv("ORQ_API_KEY"); got != "sk-orq-LOGIN" {
		t.Fatalf("stored api-key login was not injected: ORQ_API_KEY = %q", got)
	}
}

// An agent may carry a workspace token from an older browser session in its
// environment. That session token also defers to the latest API-key login.
func TestStoredAPIKeyLoginOutranksBrowserSessionToken(t *testing.T) {
	apiKeyLoginHarness(t)
	urls := auth.ResolveURLs("")
	session := &auth.Session{
		Version:        1,
		APIBaseURL:     urls.APIBaseURL,
		V1BaseURL:      urls.V1BaseURL,
		AuthBaseURL:    urls.AuthBaseURL,
		ProfileBaseURL: urls.ProfileBaseURL,
		RefreshToken:   "refresh-abc",
		BootstrapToken: auth.StoredAccessToken{Token: "boot", ExpiresAt: "2099-01-01T00:00:00Z"},
		WorkspaceTokens: map[string]auth.StoredAccessToken{
			auth.TokenCacheKey("acme", ""): {Token: "session-token", ExpiresAt: "2099-01-01T00:00:00Z"},
		},
	}
	if err := auth.SaveSession(session); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{APIKey: "sk-orq-LOGIN"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORQ_API_KEY", "session-token")
	if !applyStoredAPIKeyLogin() || os.Getenv("ORQ_API_KEY") != "sk-orq-LOGIN" {
		t.Fatalf("stored login did not replace the old session token: %q", os.Getenv("ORQ_API_KEY"))
	}
}
