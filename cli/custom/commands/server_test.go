package commands

import (
	"os"
	"path/filepath"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// One name for the host. The root PreRun decides it; everything here reads that
// decision, and the session only wins when nothing explicit did.
func TestServerResolution(t *testing.T) {
	t.Cleanup(func() { auth.SetServer("", "default") })
	session := &auth.Session{APIBaseURL: "https://session.example"}

	auth.SetServer("", "default")
	if got := sessionAPIBase(session); got != "https://session.example" {
		t.Fatalf("no override: got %q", got)
	}

	auth.SetServer("https://flag.example", "flag")
	if got := sessionAPIBase(session); got != "https://flag.example" {
		t.Fatalf("resolved server should win: got %q", got)
	}
	if got := auth.ServerSource(); got != "flag" {
		t.Fatalf("source: got %q", got)
	}

	// Provenance is recorded, not inferred: an env var holding exactly the
	// session's host must still report as env, which is what `orq doctor` shows.
	auth.SetServer(session.APIBaseURL, "env")
	if got := auth.ServerSource(); got != "env" {
		t.Fatalf("source for a value equal to the session host: got %q", got)
	}

	auth.SetServer("", "default")
	t.Setenv("ORQ_SERVER", "")
	t.Setenv("ORQ_API_BASE_URL", "https://legacy.example")
	if got := apiBaseFromEnv(); got != "https://legacy.example" {
		t.Fatalf("deprecated env: got %q", got)
	}
	t.Setenv("ORQ_SERVER", "https://env.example")
	if got := apiBaseFromEnv(); got != "https://env.example" {
		t.Fatalf("ORQ_SERVER should beat the deprecated spelling: got %q", got)
	}
	t.Setenv("ORQ_SERVER", "")
	t.Setenv("ORQ_API_BASE_URL", "")
	if got := apiBaseFromEnv(); got != auth.DefaultAPIBaseURL {
		t.Fatalf("default: got %q", got)
	}
}

// A host bound to a profile is what makes `orq --profile acme ...` route with
// no flag, so it has to survive a write and be readable back.
func TestBindProfileServer(t *testing.T) {
	dir := t.TempDir()
	prevDir := viper.GetString("config-directory")
	viper.Set("config-directory", dir)
	t.Cleanup(func() { viper.Set("config-directory", prevDir) })

	prevCreds := bartolocli.Creds
	bartolocli.Creds = newTestCreds(t)
	t.Cleanup(func() { bartolocli.Creds = prevCreds })

	profile := auth.ActiveProfile()
	if err := BindProfileServer(profile, "https://orq.acme.internal"); err != nil {
		t.Fatal(err)
	}
	if got := ProfileServer(); got != "https://orq.acme.internal" {
		t.Fatalf("ProfileServer: got %q", got)
	}

	info, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Credentials, not config: nobody else on the box reads this.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials.json mode: got %o, want 600", perm)
	}

	// The default host is never pinned — it would outlive a change of default.
	if err := BindProfileServer(profile, "  "); err != nil {
		t.Fatal(err)
	}
	if got := ProfileServer(); got != "https://orq.acme.internal" {
		t.Errorf("a blank host must not overwrite a bound one: got %q", got)
	}
}

func newTestCreds(t *testing.T) *bartolocli.CredentialsFile {
	t.Helper()
	creds, err := bartolocli.NewCredentialsFile(t.TempDir())
	if err != nil {
		t.Fatalf("NewCredentialsFile: %v", err)
	}
	return creds
}
