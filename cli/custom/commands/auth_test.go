package commands

import (
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// A profile is an API key. Logging into a browser under one would create a
// second thing with the same name, which is the confusion this release ends.
func TestBrowserLoginRefusesAProfile(t *testing.T) {
	prev := bartolocli.Creds
	creds, err := bartolocli.NewCredentialsFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bartolocli.Creds = creds
	viper.Set("profile", "work")
	t.Cleanup(func() { bartolocli.Creds = prev; viper.Set("profile", "") })

	cmd := NewLoginCommand()
	cmd.SetArgs([]string{"--no-open"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `profile "work" is an API key`) {
		t.Fatalf("err = %v, want the profile refusal", err)
	}
}
