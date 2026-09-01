package commands

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// jwtCredential builds a token of the shape auth.InspectToken reads claims
// from. The signature is never verified — InspectToken is diagnostics only.
func jwtCredential(t *testing.T, claims string) string {
	t.Helper()
	return "sk-orq-header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
}

// snapshotCredentialGlobals restores the process-wide answers PreRun leaves
// behind, so a case here cannot decide what an unrelated test in this package
// sees.
func snapshotCredentialGlobals(t *testing.T) {
	t.Helper()
	prevExplicit := explicitAPIKey
	prevEnv, prevTaken := userEnvAPIKey, userEnvAPIKeyTaken
	t.Cleanup(func() {
		explicitAPIKey = prevExplicit
		userEnvAPIKey, userEnvAPIKeyTaken = prevEnv, prevTaken
	})
}

// After RES-1465 the key `orq setup` exported defers to the login session, so
// naming ORQ_API_KEY whenever the variable is set told the user the wrong
// credential on the most common post-setup machine — and saying which
// credential the next command uses is this command's whole reason to exist.
func TestDescribeCredentialNamesTheCredentialThatWillBeUsed(t *testing.T) {
	credsHarness(t)
	snapshotCredentialGlobals(t)

	active := "acme"
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	sessionToken := jwtCredential(t, `{"workspace_id":"ws_1","key_id":"key_session"}`)
	session := &auth.Session{
		ActiveWorkspaceKey: &active,
		WorkspaceTokens:    map[string]auth.StoredAccessToken{active: {Token: sessionToken, ExpiresAt: future}},
	}

	// The shell sources ~/.orq/env, so the minted key is exported; PreRun
	// decided it does not outrank the session.
	SetUserEnvAPIKey("sk-orq-MINTED")
	SetExplicitAPIKey(false)
	cred := describeCredential(session)
	if cred == nil || cred.Source != "session" {
		t.Fatalf("credential = %+v, want the session", cred)
	}
	if cred.WorkspaceID != "ws_1" {
		t.Errorf("workspace_id = %q, want the session token's own claim", cred.WorkspaceID)
	}
	if cred.Scope != scopeAllProjects || cred.ProjectID != "" {
		t.Errorf("scope = %q project = %q, want an unpinned session", cred.Scope, cred.ProjectID)
	}

	// A key the user really did configure still outranks the session.
	SetExplicitAPIKey(true)
	if cred := describeCredential(session); cred == nil || cred.Source != "ORQ_API_KEY" {
		t.Errorf("credential = %+v, want the user's exported key", cred)
	}
}

// A session pinned to a project reaches that project only, and the session
// records which one — no round trip and no guess from the token shape.
func TestDescribeCredentialReportsThePinnedProject(t *testing.T) {
	credsHarness(t)
	snapshotCredentialGlobals(t)
	SetUserEnvAPIKey("")
	SetExplicitAPIKey(false)

	active := "acme"
	session := &auth.Session{ActiveWorkspaceKey: &active, ActiveProjectID: "proj1"}
	cred := describeCredential(session)
	if cred == nil || cred.Scope != scopeProject || cred.ProjectID != "proj1" {
		t.Fatalf("credential = %+v, want the pinned project", cred)
	}
}

// The opaque sk-orq-<ULID>-<secret> shape carries no scope claims at all.
// Rendering that silence as "all projects" was an assertion nothing local
// could support.
func TestCredentialScopeSeparatesUnknownFromAllProjects(t *testing.T) {
	credsHarness(t)
	snapshotCredentialGlobals(t)
	SetExplicitAPIKey(true)

	for name, tc := range map[string]struct {
		key       string
		wantScope string
		wantLine  string
	}{
		"opaque key": {
			key:       "sk-orq-01HQZX9K3Q-secret",
			wantScope: scopeUnknown,
			wantLine:  "scope not recorded in the key",
		},
		"workspace jwt": {
			key:       jwtCredential(t, `{"workspace_id":"ws_1","key_id":"key_1"}`),
			wantScope: scopeAllProjects,
			wantLine:  "all projects",
		},
		"project jwt": {
			key:       jwtCredential(t, `{"workspace_id":"ws_1","projects":["proj1"]}`),
			wantScope: scopeProject,
			wantLine:  "one project",
		},
	} {
		t.Run(name, func(t *testing.T) {
			SetUserEnvAPIKey(tc.key)
			cred := describeCredential(nil)
			if cred == nil || cred.Scope != tc.wantScope {
				t.Fatalf("credential = %+v, want scope %q", cred, tc.wantScope)
			}
			if got := describeScope(*cred); got != tc.wantLine {
				t.Errorf("describeScope = %q, want %q", got, tc.wantLine)
			}

			var out bytes.Buffer
			prev := bartolocli.Stdout
			bartolocli.Stdout = &out
			t.Cleanup(func() { bartolocli.Stdout = prev })
			printIdentity(IdentityReport{Credential: cred}, "Signed in as")
			if !strings.Contains(out.String(), tc.wantLine) {
				t.Errorf("status line %q does not describe the scope as %q", out.String(), tc.wantLine)
			}
			// A rendered credential must never carry the secret itself.
			if strings.Contains(out.String(), tc.key) {
				t.Error("the status line printed the credential")
			}
		})
	}
}
