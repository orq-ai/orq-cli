package commands

import (
	"bytes"
	"os"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// A key in ORQ_TOKEN is as durable across logout as one in ORQ_API_KEY, and the
// warning used to name ORQ_API_KEY regardless: the user unset a variable they
// had never set and the next command authenticated again anyway.
func TestLingeringKeyWarningNamesTheVariableThatIsSet(t *testing.T) {
	t.Setenv("ORQ_API_KEY", "")
	t.Setenv("ORQ_TOKEN", "sk-orq-token")
	t.Setenv("ORQ_AUTHORIZATION", "")
	// Process globals shared with every other test in the package: restore the
	// previous values rather than zeroing them.
	prevExplicit := explicitAPIKey
	SetExplicitAPIKey(true)
	t.Cleanup(func() { SetExplicitAPIKey(prevExplicit) })

	var out bytes.Buffer
	prevErr := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prevErr })

	warnLingeringAPIKeys()

	got := out.String()
	if !strings.Contains(got, "ORQ_TOKEN") {
		t.Errorf("warning does not name the variable that is set: %q", got)
	}
	if strings.Contains(got, "ORQ_API_KEY") {
		t.Errorf("warning names a variable that is not set: %q", got)
	}
	if strings.Contains(got, "sk-orq-token") {
		t.Errorf("warning echoed the key value: %q", got)
	}
}

// Since bartolo v0.15.0 a .env is read only under $ORQ_DOTENV=1, so an
// unimported line is inert: warning about it sent users to edit a file that was
// not signing them back in.
func TestLingeringKeyWarningIgnoresAnUnloadedDotEnv(t *testing.T) {
	t.Setenv("ORQ_API_KEY", "")
	t.Setenv("ORQ_TOKEN", "")
	t.Setenv("ORQ_AUTHORIZATION", "")
	prevExplicit := explicitAPIKey
	SetExplicitAPIKey(true)
	t.Cleanup(func() { SetExplicitAPIKey(prevExplicit) })

	// A real file on disk: the warning this replaced found it by reading ./.env
	// itself, and named it whether or not bartolo had imported anything.
	chdir(t, t.TempDir())
	if err := os.WriteFile(".env", []byte("ORQ_API_KEY=sk-orq-inert\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	prevErr := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prevErr })

	// bartolo imported nothing, which is what an absent $ORQ_DOTENV leaves.
	prevOrigin := dotEnvOrigin
	dotEnvOrigin = func(string) string { return "" }
	t.Cleanup(func() { dotEnvOrigin = prevOrigin })

	warnLingeringAPIKeys()

	if got := out.String(); strings.Contains(got, ".env") {
		t.Errorf("warned about a .env bartolo never read: %q", got)
	}
}

// With loading on, the file is the action: unsetting the shell variable leaves
// the next command authenticated.
func TestLingeringKeyWarningNamesTheDotEnvBartoloLoaded(t *testing.T) {
	t.Setenv("ORQ_API_KEY", "sk-orq-fromfile")
	t.Setenv("ORQ_TOKEN", "")
	t.Setenv("ORQ_AUTHORIZATION", "")
	prevExplicit := explicitAPIKey
	SetExplicitAPIKey(true)
	t.Cleanup(func() { SetExplicitAPIKey(prevExplicit) })

	var out bytes.Buffer
	prevErr := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prevErr })

	prevOrigin := dotEnvOrigin
	dotEnvOrigin = func(key string) string {
		if key == "ORQ_API_KEY" {
			return ".env.local"
		}
		return ""
	}
	t.Cleanup(func() { dotEnvOrigin = prevOrigin })

	warnLingeringAPIKeys()

	got := out.String()
	if !strings.Contains(got, ".env.local") {
		t.Errorf("warning does not name the file that supplied the key: %q", got)
	}
	// 'unset ORQ_API_KEY' does not survive the next run; the file re-imports it.
	if strings.Contains(got, "unset ORQ_API_KEY") {
		t.Errorf("warning told the user to unset an imported variable: %q", got)
	}
	if strings.Contains(got, "sk-orq-fromfile") {
		t.Errorf("warning echoed the key value: %q", got)
	}
}
