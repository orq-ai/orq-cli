package commands

import (
	"bytes"
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
