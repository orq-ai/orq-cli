package custom

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// profileHarness gives a test its own HOME and a credentials.json loaded the
// way the CLI loads it, so ProfileExists answers about that file and nothing
// on the developer's machine.
func profileHarness(t *testing.T, credentials string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".orq")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	creds, err := bartolocli.NewCredentialsFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	prev := bartolocli.Creds
	bartolocli.Creds = creds
	t.Cleanup(func() {
		bartolocli.Creds = prev
		viper.Set("profile", "")
		viper.Set("profile-selected", "")
	})
}

func findCommand(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	cmd, _, err := root.Find(path)
	if err != nil {
		t.Fatalf("find %v: %v", path, err)
	}
	if cmd == root {
		t.Fatalf("find %v resolved to the root command", path)
	}
	return cmd
}

// The upgrade path this guards: MigrateLayout deletes the keyless profile a
// previous release wrote, and an exported ORQ_PROFILE naming it survives the
// deletion. Without the guard bartolo aborts with "no authentication handler
// configured", which names neither the profile nor where it came from.
func TestUnknownProfileFromTheEnvironmentIsRejectedByName(t *testing.T) {
	profileHarness(t, `{"profiles":{"other":{"api_key":"sk-orq-real","type":"apikey"}}}`)
	root := buildRoot(t)
	viper.Set("profile", "acme")

	err := rejectUnknownProfile(findCommand(t, root, "models", "list"))
	if err == nil {
		t.Fatal("an unknown profile was accepted")
	}
	for _, want := range []string{`unknown profile "acme"`, "ORQ_PROFILE", "unset ORQ_PROFILE", "orq auth profile list", "--server"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// A profile selected with `orq auth profile use` is not on the command line,
// so telling the user to drop a flag they never typed is worse than useless.
func TestUnknownProfileFromAPersistedSelectionSaysHowToClearIt(t *testing.T) {
	profileHarness(t, `{"profiles":{}}`)
	root := buildRoot(t)
	viper.Set("profile-selected", "acme")

	err := rejectUnknownProfile(findCommand(t, root, "models", "list"))
	if err == nil {
		t.Fatal("an unknown profile was accepted")
	}
	if !strings.Contains(err.Error(), "orq auth profile clear") {
		t.Errorf("error does not say how to clear the selection: %v", err)
	}
	if strings.Contains(err.Error(), "--profile ") {
		t.Errorf("error blames a flag the user did not pass: %v", err)
	}
}

// The commands that create, list or unselect a profile are the way out of this
// state, so the guard must not be what stops them.
func TestUnknownProfileStillAllowsTheCommandsThatFixIt(t *testing.T) {
	profileHarness(t, `{"profiles":{}}`)
	root := buildRoot(t)
	viper.Set("profile", "acme")

	for _, path := range [][]string{{"doctor"}, {"auth", "login"}, {"auth", "profile", "list"}, {"version"}} {
		if err := rejectUnknownProfile(findCommand(t, root, path...)); err != nil {
			t.Errorf("%v: %v", path, err)
		}
	}
}

func TestAKnownProfilePassesTheGuard(t *testing.T) {
	profileHarness(t, `{"profiles":{"acme":{"api_key":"sk-orq-real","type":"apikey"}}}`)
	root := buildRoot(t)
	viper.Set("profile", "acme")

	if err := rejectUnknownProfile(findCommand(t, root, "models", "list")); err != nil {
		t.Fatalf("a configured profile was rejected: %v", err)
	}
}

func TestNoProfileSelectedPassesTheGuard(t *testing.T) {
	profileHarness(t, `{"profiles":{}}`)
	root := buildRoot(t)

	if err := rejectUnknownProfile(findCommand(t, root, "models", "list")); err != nil {
		t.Fatalf("no profile in force was rejected: %v", err)
	}
}

// `orq --profile x launch <agent>` has to reach the agent with the profile's
// key. bartolo resolves the profile for its own requests; the child process
// cannot, so the PreRun exports it. Without this, launch falls through to the
// login session and wires the agent with a credential the user did not pick.
func TestAnInForceProfileIsExportedForChildProcesses(t *testing.T) {
	profileHarness(t, `{"profiles":{"acme":{"api_key":"sk-orq-profile","type":"apikey"}}}`)
	buildRoot(t)
	viper.Set("profile", "acme")
	t.Setenv("ORQ_API_KEY", "")

	applyProfileAPIKey()

	if got := os.Getenv("ORQ_API_KEY"); got != "sk-orq-profile" {
		t.Errorf("ORQ_API_KEY = %q, want the profile's key", got)
	}
}

// A losing key left in the environment lets a child pick the credential the
// parent already decided against, so the others are cleared and the swap is
// announced once.
func TestAnInForceProfileClearsAndAnnouncesAShadowedEnvironmentKey(t *testing.T) {
	profileHarness(t, `{"profiles":{"acme":{"api_key":"sk-orq-profile","type":"apikey"}}}`)
	buildRoot(t)
	viper.Set("profile", "acme")
	t.Setenv("ORQ_API_KEY", "sk-orq-environment")

	var out bytes.Buffer
	prev := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prev })

	applyProfileAPIKey()

	if got := os.Getenv("ORQ_API_KEY"); got != "sk-orq-profile" {
		t.Errorf("ORQ_API_KEY = %q, want the profile's key", got)
	}
	if !strings.Contains(out.String(), "ORQ_API_KEY") || !strings.Contains(out.String(), "acme") {
		t.Errorf("no warning naming the shadowed variable and the winner: %q", out.String())
	}
}

// A keyless profile exports nothing — and must not clear what the user has set
// either: bartolo fails the call on the profile itself, and hiding the
// environment would only make that harder to diagnose.
func TestAKeylessProfileExportsNothingAndClearsNothing(t *testing.T) {
	profileHarness(t, `{"profiles":{"acme":{"type":"apikey"}}}`)
	buildRoot(t)
	viper.Set("profile", "acme")
	t.Setenv("ORQ_API_KEY", "sk-orq-environment")

	applyProfileAPIKey()

	if got := os.Getenv("ORQ_API_KEY"); got != "sk-orq-environment" {
		t.Errorf("ORQ_API_KEY = %q, want the user's own key untouched", got)
	}
}
