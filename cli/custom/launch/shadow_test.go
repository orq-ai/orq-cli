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
				bartolocli.Creds = newTestCreds(t)
				t.Cleanup(func() { bartolocli.Creds = nil })
			}
			viper.Set("profile", "default")
			t.Cleanup(func() { viper.Set("profile", "") })
			// gateway_key is the field `orq setup` writes now. Seeding api_key
			// here is what let the "warns forever" regression ship green: the
			// fixture described a credential layout setup no longer produces.
			bartolocli.Creds.Set("profiles.default.gateway_key", tc.savedKey)
			bartolocli.Creds.Set("profiles.default.api_key", "")
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

// A key the user brought themselves still lives in api_key, so the fallback has
// to keep working — otherwise the same warning fires forever for them instead.
func TestShadowsSessionReadsTheLegacyFieldToo(t *testing.T) {
	if bartolocli.Creds == nil {
		bartolocli.Creds = newTestCreds(t)
		t.Cleanup(func() { bartolocli.Creds = nil })
	}
	viper.Set("profile", "default")
	t.Cleanup(func() { viper.Set("profile", "") })
	bartolocli.Creds.Set("profiles.default.gateway_key", "")
	bartolocli.Creds.Set("profiles.default.api_key", "sk-brought")
	bartolocli.Creds.Set("profiles.default.workspace", "acme")

	active := "acme"
	session := &auth.Session{ActiveWorkspaceKey: &active}
	if shadowsSession("sk-brought", session) {
		t.Error("a user-supplied key in its own workspace reported as a mismatch")
	}
	if !shadowsSession("sk-other", session) {
		t.Error("a genuinely different key did not report as a mismatch")
	}
}

// The field fix alone was not enough. installSessionPreRun injects the session's
// own workspace token into ORQ_API_KEY whenever no api_key is configured — which
// the gateway_key split made the ordinary state — so launch was comparing our own
// token against the saved key and reporting a mismatch on every run.
func TestShadowsSessionIgnoresOurOwnInjectedToken(t *testing.T) {
	if bartolocli.Creds == nil {
		bartolocli.Creds = newTestCreds(t)
		t.Cleanup(func() { bartolocli.Creds = nil })
	}
	viper.Set("profile", "default")
	t.Cleanup(func() { viper.Set("profile", "") })
	bartolocli.Creds.Set("profiles.default.gateway_key", "sk-orq-MINTED")
	bartolocli.Creds.Set("profiles.default.api_key", "")
	bartolocli.Creds.Set("profiles.default.workspace", "acme")

	active := "acme"
	session := &auth.Session{
		ActiveWorkspaceKey: &active,
		WorkspaceTokens:    map[string]auth.StoredAccessToken{"acme": {Token: "session-jwt"}},
	}
	if shadowsSession("session-jwt", session) {
		t.Error("our own injected session token reported as a shadowing key")
	}
	// A real foreign key must still be caught.
	if !shadowsSession("sk-somebody-elses", session) {
		t.Error("a genuinely different key stopped being reported")
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
