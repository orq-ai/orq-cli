package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// jwtWithExpiry builds a token decodeJWTExpiry accepts. Only the payload's exp
// claim is read, so the header and signature are filler.
func jwtWithExpiry(t *testing.T, at time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]int64{"exp": at.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	seg := base64.RawURLEncoding.EncodeToString(payload)
	return "eyJhbGciOiJIUzI1NiJ9." + seg + ".sig"
}

// loginServer answers the two calls CreateSessionFromDeviceApproval makes: the
// profile RPC and the access-token exchange.
func loginServer(t *testing.T, profileID string, workspaceKeys ...string) *httptest.Server {
	t.Helper()
	token := jwtWithExpiry(t, time.Now().Add(time.Hour))
	ws := make([]string, 0, len(workspaceKeys))
	for _, k := range workspaceKeys {
		ws = append(ws, fmt.Sprintf(`{"key":%q}`, k))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Routed, not catch-all: a client calling the wrong endpoint must fail
		// the test rather than be handed a profile anyway.
		switch {
		case strings.HasSuffix(r.URL.Path, "/access-token"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":%q}`, token)
		case r.URL.Path == ProfileRPCPath:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w,
				`{"profile":{"id":%q,"email":"who@example.com","display_name":"Who","workspaces":[%s],"preferences":{"active_workspace":%q}}}`,
				profileID, strings.Join(ws, ","), workspaceKeys[0])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// loginAgainst points the package-global server at the test server before
// anything resolves a path. SessionFilePath is host-scoped through it, so
// without this a test writes its fixture to my.orq.ai.json and then logs in
// against a different host: every assertion about "the previous session" would
// be made about a file the code under test never reads.
func loginAgainst(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	prev, prevSource := Server(), ServerSource()
	SetServer(srv.URL, "flag")
	t.Cleanup(func() { SetServer(prev, prevSource) })
	return clientFor(srv)
}

func clientFor(srv *httptest.Server) *Client {
	// NewClient derives every URL the way production does; overriding them by
	// hand bakes paths ResolveURLs never produces into the fixture.
	return NewClient(srv.URL)
}

// carriedFields is every durable field a login must decide about. Listing them
// in one struct is what makes a forgotten field visible: a new durable field on
// Session that nobody adds here shows up as a row that cannot express it.
type carriedFields struct {
	gatewayKey        string
	gatewayKeyID      string
	gatewayExpiresAt  string
	gatewayWorkspace  string
	gatewayProject    string
	activeProjectID   string
	activeProjectName string
}

func fieldsOf(s *Session) carriedFields {
	return carriedFields{
		gatewayKey:        s.GatewayKey,
		gatewayKeyID:      s.GatewayKeyID,
		gatewayExpiresAt:  s.GatewayKeyExpiresAt,
		gatewayWorkspace:  s.GatewayWorkspace,
		gatewayProject:    s.GatewayProject,
		activeProjectID:   s.ActiveProjectID,
		activeProjectName: s.ActiveProjectName,
	}
}

func allFields() carriedFields {
	return carriedFields{
		gatewayKey:        "sk-orq-MINTED",
		gatewayKeyID:      "key-id-1",
		gatewayExpiresAt:  "2099-01-01T00:00:00Z",
		gatewayWorkspace:  "acme",
		gatewayProject:    "proj-1",
		activeProjectID:   "proj-1",
		activeProjectName: "Amina",
	}
}

// gatewayRecordOnly is what survives a login that moved workspace: the five
// fields describing the minted key travel as one record — GatewayProject is the
// project the key was *scoped to*, not the user's selection — while the active
// project belongs to the workspace it was chosen in and does not.
func gatewayRecordOnly() carriedFields {
	f := allFields()
	f.activeProjectID, f.activeProjectName = "", ""
	return f
}

func sessionWith(userID, workspace string, f carriedFields) *Session {
	s := validSession(workspace)
	s.User = &SessionUser{ID: userID, Email: "who@example.com"}
	s.GatewayKey = f.gatewayKey
	s.GatewayKeyID = f.gatewayKeyID
	s.GatewayKeyExpiresAt = f.gatewayExpiresAt
	s.GatewayWorkspace = f.gatewayWorkspace
	s.GatewayProject = f.gatewayProject
	s.ActiveProjectID = f.activeProjectID
	s.ActiveProjectName = f.activeProjectName
	return s
}

func TestReLoginCarriesOverOwnFields(t *testing.T) {
	cases := []struct {
		name string
		// anonymousProfile makes the login's profile carry no user id, so a
		// row can pair it with a stored session that has none either.
		anonymousProfile bool
		// before is the session on disk when the login starts; nil for none.
		before *Session
		// corrupt replaces the file's bytes after before is written.
		corrupt string
		// unreadable strips read permission from the session file.
		unreadable bool
		// workspace is the --workspace argument, or the interactive pick.
		workspace string
		want      carriedFields
		wantWS    string
		wantWarn  string
	}{{
		name:      "first login on this machine keeps nothing and warns about nothing",
		before:    nil,
		workspace: "acme",
		want:      carriedFields{},
		wantWS:    "acme",
	}, {
		name:      "same user, same workspace, everything survives",
		before:    sessionWith("user-1", "acme", allFields()),
		workspace: "acme",
		want:      allFields(),
		wantWS:    "acme",
	}, {
		name:      "same user choosing another workspace keeps the key, drops the project",
		before:    sessionWith("user-1", "acme", allFields()),
		workspace: "other",
		want:      gatewayRecordOnly(),
		wantWS:    "other",
	}, {
		name: "no workspace chosen resolves the previous one, so the project stays",
		// The server preference names "acme" here too, but the point is that a
		// non-interactive re-login must not read an unchosen workspace as a
		// change: `orq workspace use` never PATCHes the preference, so the two
		// drift and the project would be dropped on an ordinary scripted login.
		before:    sessionWith("user-1", "acme", allFields()),
		workspace: "",
		want:      allFields(),
		wantWS:    "acme",
	}, {
		name:      "a different user inherits nothing and is told the key was dropped",
		before:    sessionWith("user-2", "acme", allFields()),
		workspace: "acme",
		want:      carriedFields{},
		wantWS:    "acme",
		wantWarn:  "orq api-keys delete key-id-1",
	}, {
		name:      "a session with no user is not the same user",
		before:    sessionWith("", "acme", allFields()),
		workspace: "acme",
		want:      carriedFields{},
		wantWS:    "acme",
		wantWarn:  "orq api-keys delete key-id-1",
	}, {
		name: "two unknown users are not one user",
		// Both ids empty must not compare equal: that hands a minted key to
		// whoever logs in next.
		anonymousProfile: true,
		before:           sessionWith("", "acme", allFields()),
		workspace:        "acme",
		want:             carriedFields{},
		wantWS:           "acme",
		wantWarn:         "orq api-keys delete key-id-1",
	}, {
		name: "a session that fails validateSession still yields its key",
		// Invalid for use, still parseable: the login supplies the very field
		// validateSession is missing, so the key must not be lost over it.
		before:    func() *Session { s := sessionWith("user-1", "acme", allFields()); s.RefreshToken = ""; return s }(),
		workspace: "acme",
		want:      allFields(),
		wantWS:    "acme",
	}, {
		name:      "bytes that do not parse leave nothing to carry, and say so",
		before:    sessionWith("user-1", "acme", allFields()),
		corrupt:   "{not json",
		workspace: "acme",
		want:      carriedFields{},
		wantWS:    "acme",
		wantWarn:  "could not read the previous session",
	}, {
		name:       "an unreadable session file is reported, not passed over",
		before:     sessionWith("user-1", "acme", allFields()),
		unreadable: true,
		workspace:  "acme",
		want:       carriedFields{},
		wantWS:     "acme",
		wantWarn:   "could not read the previous session",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateHome(t)
			profileID := "user-1"
			if tc.anonymousProfile {
				profileID = ""
			}
			srv := loginServer(t, profileID, "acme", "other")
			client := loginAgainst(t, srv)

			var out bytes.Buffer
			prevErr := bartolocli.Stderr
			bartolocli.Stderr = &out
			t.Cleanup(func() { bartolocli.Stderr = prevErr })

			if tc.before != nil {
				if err := saveSessionTo(SessionFilePath(), tc.before); err != nil {
					t.Fatalf("saveSessionTo: %v", err)
				}
			}
			if tc.corrupt != "" {
				if err := os.WriteFile(SessionFilePath(), []byte(tc.corrupt), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unreadable {
				if err := os.Chmod(SessionFilePath(), 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(SessionFilePath(), 0o600) })
			}

			approved := &ApprovedDeviceLogin{
				AccessToken:  jwtWithExpiry(t, time.Now().Add(time.Hour)),
				RefreshToken: "refresh-new",
			}
			session, err := client.CreateSessionFromDeviceApproval(approved, nil, tc.workspace)
			if err != nil {
				t.Fatalf("CreateSessionFromDeviceApproval: %v", err)
			}
			if tc.unreadable {
				// Readable again so the assertions below can re-read the file
				// the login just wrote.
				if err := os.Chmod(SessionFilePath(), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			// The returned session and the file must agree: callers use the
			// return value, every later command reads the file.
			onDisk, err := ReadSession()
			if err != nil {
				t.Fatalf("ReadSession: %v", err)
			}
			for label, got := range map[string]*Session{"returned": session, "on disk": onDisk} {
				if diff := fieldsOf(got); diff != tc.want {
					t.Errorf("%s session fields = %+v, want %+v", label, diff, tc.want)
				}
				if ws := workspaceKeyOf(got); ws != tc.wantWS {
					t.Errorf("%s active workspace = %q, want %q", label, ws, tc.wantWS)
				}
				if got.RefreshToken != "refresh-new" {
					t.Errorf("%s refresh token = %q, want the one this login issued", label, got.RefreshToken)
				}
			}
			if tc.wantWarn == "" {
				if strings.Contains(out.String(), "gateway key") || strings.Contains(out.String(), "could not read") {
					t.Errorf("stderr = %q, want no warning", out.String())
				}
			} else if !strings.Contains(out.String(), tc.wantWarn) {
				t.Errorf("stderr = %q, want it to contain %q", out.String(), tc.wantWarn)
			}
		})
	}
}

// The account switch leaves the previous user's key exported and outranking the
// new login (RES-1465's precedence rule reads it as foreign once GatewayKey is
// gone). Fixing that is outside this change, but the login must not be silent
// about it: the user is signed in on paper while commands authenticate as
// somebody else.
func TestReLoginAsAnotherUserWarnsTheExportedKeyStillWins(t *testing.T) {
	isolateHome(t)
	srv := loginServer(t, "user-1", "acme")
	client := loginAgainst(t, srv)

	var out bytes.Buffer
	prevErr := bartolocli.Stderr
	bartolocli.Stderr = &out
	t.Cleanup(func() { bartolocli.Stderr = prevErr })

	before := sessionWith("user-2", "acme", allFields())
	before.User.Email = "someone.else@example.com"
	if err := saveSessionTo(SessionFilePath(), before); err != nil {
		t.Fatalf("saveSessionTo: %v", err)
	}

	approved := &ApprovedDeviceLogin{
		AccessToken:  jwtWithExpiry(t, time.Now().Add(time.Hour)),
		RefreshToken: "refresh-new",
	}
	if _, err := client.CreateSessionFromDeviceApproval(approved, nil, "acme"); err != nil {
		t.Fatalf("CreateSessionFromDeviceApproval: %v", err)
	}
	if !strings.Contains(out.String(), "someone.else@example.com") {
		t.Errorf("stderr = %q, want it to name the user whose key is still exported", out.String())
	}
	if !strings.Contains(out.String(), "orq setup") {
		t.Errorf("stderr = %q, want it to name the command that replaces the key", out.String())
	}
}
