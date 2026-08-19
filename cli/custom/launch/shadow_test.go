package launch

import (
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"

	"orq/cli/custom/auth"
)

// `orq setup` writes ~/.orq/env exporting the key it just minted and offers to
// source it from the shell profile, so the normal machine has both an env key
// and a session. Warning on their mere coexistence fires forever and tells the
// user to unset the variable that makes MCP work for bare agents.
func TestShadowsSessionOnlyOnARealMismatch(t *testing.T) {
	ws := func(s string) *string { return &s }

	for _, tc := range []struct {
		name     string
		envKey   string
		savedKey string
		savedWS  string
		activeWS *string
		want     bool
	}{
		{name: "no session", envKey: "sk-a", savedKey: "sk-a", savedWS: "acme", activeWS: nil, want: false},
		{name: "our key, same workspace", envKey: "sk-a", savedKey: "sk-a", savedWS: "acme", activeWS: ws("acme"), want: false},
		{name: "our key, other workspace", envKey: "sk-a", savedKey: "sk-a", savedWS: "acme", activeWS: ws("other"), want: true},
		{name: "unrecorded workspace", envKey: "sk-a", savedKey: "sk-a", savedWS: "", activeWS: ws("acme"), want: false},
		{name: "a key we did not mint", envKey: "sk-b", savedKey: "sk-a", savedWS: "acme", activeWS: ws("acme"), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if bartolocli.Creds == nil {
				bartolocli.Creds = &bartolocli.CredentialsFile{Viper: viper.New()}
				t.Cleanup(func() { bartolocli.Creds = nil })
			}
			viper.Set("profile", "default")
			t.Cleanup(func() { viper.Set("profile", "") })
			bartolocli.Creds.Set("profiles.default.api_key", tc.savedKey)
			bartolocli.Creds.Set("profiles.default.workspace", tc.savedWS)

			var session *auth.Session
			if tc.activeWS != nil {
				session = &auth.Session{ActiveWorkspaceKey: tc.activeWS}
			}
			if got := shadowsSession(tc.envKey, session); got != tc.want {
				t.Errorf("shadowsSession = %v, want %v", got, tc.want)
			}
		})
	}
}
