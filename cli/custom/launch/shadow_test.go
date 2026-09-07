package launch

import (
	"testing"

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
			var session *auth.Session
			if tc.activeWS != nil {
				session = &auth.Session{
					ActiveWorkspaceKey: tc.activeWS,
					GatewayKey:         tc.savedKey,
					GatewayWorkspace:   tc.savedWS,
				}
				saveLaunchSession(t, session)
			}
			if got := shadowsSession(tc.envKey, session); got != tc.want {
				t.Errorf("shadowsSession = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShadowsSessionReadsTheSavedGatewayKey(t *testing.T) {
	active := "acme"
	session := &auth.Session{ActiveWorkspaceKey: &active, GatewayKey: "sk-brought", GatewayWorkspace: "acme"}
	saveLaunchSession(t, session)
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
	active := "acme"
	session := &auth.Session{
		ActiveWorkspaceKey: &active,
		WorkspaceTokens:    map[string]auth.StoredAccessToken{"acme": {Token: "session-jwt"}},
		GatewayKey:         "sk-orq-MINTED",
		GatewayWorkspace:   "acme",
	}
	saveLaunchSession(t, session)
	if shadowsSession("session-jwt", session) {
		t.Error("our own injected session token reported as a shadowing key")
	}
	// A real foreign key must still be caught.
	if !shadowsSession("sk-somebody-elses", session) {
		t.Error("a genuinely different key stopped being reported")
	}
}

func saveLaunchSession(t *testing.T, session *auth.Session) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	urls := auth.ResolveURLs("")
	session.Version = 1
	session.APIBaseURL = urls.APIBaseURL
	session.V1BaseURL = urls.V1BaseURL
	session.AuthBaseURL = urls.AuthBaseURL
	session.ProfileBaseURL = urls.ProfileBaseURL
	session.RefreshToken = "refresh-token"
	session.BootstrapToken = auth.StoredAccessToken{Token: "bootstrap-token", ExpiresAt: "2099-01-01T00:00:00Z"}
	if session.WorkspaceTokens == nil {
		session.WorkspaceTokens = map[string]auth.StoredAccessToken{}
	}
	if err := auth.SaveSession(session); err != nil {
		t.Fatal(err)
	}
}

// supersededBySession is narrower than shadowsSession: only a key we minted
// ourselves, for a workspace we recorded, may be superseded by the session's
// active workspace. Anything unknown — no session, an unrecorded workspace, a
// key we did not mint — keeps the exported key winning.
func TestSupersededBySession(t *testing.T) {
	ws := func(s string) *string { return &s }

	for _, tc := range []struct {
		name          string
		envKey        string
		savedKey      string
		savedWS       string
		activeWS      *string
		workspaceTok  map[string]auth.StoredAccessToken
		wantMintedFor string
		wantSuper     bool
	}{
		{
			name: "saved key, different workspace", envKey: "sk-a", savedKey: "sk-a", savedWS: "acme",
			activeWS: ws("other"), wantMintedFor: "acme", wantSuper: true,
		},
		{
			name: "saved key, same workspace", envKey: "sk-a", savedKey: "sk-a", savedWS: "acme",
			activeWS: ws("acme"), wantMintedFor: "", wantSuper: false,
		},
		{
			name: "saved key, unrecorded workspace", envKey: "sk-a", savedKey: "sk-a", savedWS: "",
			activeWS: ws("acme"), wantMintedFor: "", wantSuper: false,
		},
		{
			name: "a key we did not mint", envKey: "sk-b", savedKey: "sk-a", savedWS: "acme",
			activeWS: ws("other"), wantMintedFor: "", wantSuper: false,
		},
		{
			name: "the session's own cached workspace token", envKey: "session-jwt", savedKey: "sk-a", savedWS: "acme",
			activeWS: ws("other"), workspaceTok: map[string]auth.StoredAccessToken{"other": {Token: "session-jwt"}},
			wantMintedFor: "", wantSuper: false,
		},
		{
			name: "no session", envKey: "sk-a", savedKey: "sk-a", savedWS: "acme",
			activeWS: nil, wantMintedFor: "", wantSuper: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var session *auth.Session
			if tc.activeWS != nil {
				session = &auth.Session{ActiveWorkspaceKey: tc.activeWS, WorkspaceTokens: tc.workspaceTok}
			}
			mintedFor, superseded := supersededBySession(tc.envKey, session, tc.savedKey, tc.savedWS)
			if mintedFor != tc.wantMintedFor || superseded != tc.wantSuper {
				t.Errorf("supersededBySession = (%q, %v), want (%q, %v)", mintedFor, superseded, tc.wantMintedFor, tc.wantSuper)
			}
		})
	}
}
