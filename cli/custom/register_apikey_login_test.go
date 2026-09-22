package custom

import (
	"os"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// apiKeyLoginHarness isolates HOME and credentials so a test can drive the
// api-key login store and PreRun injection without touching the real ~/.orq or
// leaking a selected profile from a sibling test.
func apiKeyLoginHarness(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

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
	// environment" notice must stay silent and the session (if any) still wins.
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
