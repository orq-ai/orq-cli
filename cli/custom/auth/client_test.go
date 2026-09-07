package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
func loginServer(t *testing.T, profileID, workspaceKey string) *httptest.Server {
	t.Helper()
	token := jwtWithExpiry(t, time.Now().Add(time.Hour))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/access-token"):
			fmt.Fprintf(w, `{"access_token":%q}`, token)
		default:
			fmt.Fprintf(w,
				`{"profile":{"id":%q,"email":"who@example.com","display_name":"Who","workspaces":[{"key":%q}],"preferences":{"active_workspace":%q}}}`,
				profileID, workspaceKey, workspaceKey)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func clientFor(srv *httptest.Server) *Client {
	c := NewClient(srv.URL)
	c.URLs = URLs{
		APIBaseURL:     srv.URL,
		V1BaseURL:      srv.URL + "/v1",
		AuthBaseURL:    srv.URL + "/v2/auth",
		ProfileBaseURL: srv.URL + "/v2/auth/profile",
	}
	return c
}

// A re-login must not discard what `orq setup` recorded on the session. The
// gateway key is the credential the coding agents are wired with, its id is the
// only local handle for revoking it, and RES-1465's precedence rule identifies
// the exported key by comparing it against GatewayKey — so dropping the field
// re-arms the bug that rule exists to fix, on a machine that never touched
// ORQ_API_KEY by hand.
func TestReLoginKeepsGatewayKeyForTheSameUser(t *testing.T) {
	isolateHome(t)
	srv := loginServer(t, "user-1", "acme")

	existing := validSession("acme")
	existing.User = &SessionUser{ID: "user-1", Email: "who@example.com"}
	existing.GatewayKey = "sk-orq-MINTED"
	existing.GatewayKeyID = "key-id-1"
	existing.GatewayKeyExpiresAt = "2099-01-01T00:00:00Z"
	existing.GatewayWorkspace = "acme"
	existing.GatewayProject = "proj-1"
	existing.ActiveProjectID = "proj-1"
	existing.ActiveProjectName = "Amina"
	if err := SaveSession(existing); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	approved := &ApprovedDeviceLogin{
		AccessToken:  jwtWithExpiry(t, time.Now().Add(time.Hour)),
		RefreshToken: "refresh-new",
	}
	session, err := clientFor(srv).CreateSessionFromDeviceApproval(approved, nil, "acme")
	if err != nil {
		t.Fatalf("CreateSessionFromDeviceApproval: %v", err)
	}
	if session.RefreshToken != "refresh-new" {
		t.Errorf("refresh token = %q, want the one the login just issued", session.RefreshToken)
	}

	// The returned session and the one on disk must agree: callers use the
	// return value, and every later command reads the file.
	onDisk, err := ReadSession()
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	for _, got := range []*Session{session, onDisk} {
		if got.GatewayKey != "sk-orq-MINTED" {
			t.Errorf("gateway key = %q, want it preserved", got.GatewayKey)
		}
		if got.GatewayKeyID != "key-id-1" {
			t.Errorf("gateway key id = %q, want it preserved", got.GatewayKeyID)
		}
		if got.GatewayKeyExpiresAt != "2099-01-01T00:00:00Z" {
			t.Errorf("gateway key expiry = %q, want it preserved", got.GatewayKeyExpiresAt)
		}
		if got.GatewayWorkspace != "acme" || got.GatewayProject != "proj-1" {
			t.Errorf("gateway scope = %q/%q, want it preserved", got.GatewayWorkspace, got.GatewayProject)
		}
		if got.ActiveProjectID != "proj-1" || got.ActiveProjectName != "Amina" {
			t.Errorf("active project = %q/%q, want it preserved", got.ActiveProjectID, got.ActiveProjectName)
		}
	}
}

// The key belongs to the login that minted it. Someone else logging in on this
// machine must not inherit it: they would authenticate their agents as the
// previous user, and `logout` would offer them a key id that is not theirs.
func TestReLoginDropsGatewayKeyForADifferentUser(t *testing.T) {
	isolateHome(t)
	srv := loginServer(t, "user-2", "acme")

	existing := validSession("acme")
	existing.User = &SessionUser{ID: "user-1", Email: "who@example.com"}
	existing.GatewayKey = "sk-orq-MINTED"
	existing.GatewayKeyID = "key-id-1"
	existing.ActiveProjectID = "proj-1"
	if err := SaveSession(existing); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	approved := &ApprovedDeviceLogin{
		AccessToken:  jwtWithExpiry(t, time.Now().Add(time.Hour)),
		RefreshToken: "refresh-new",
	}
	session, err := clientFor(srv).CreateSessionFromDeviceApproval(approved, nil, "acme")
	if err != nil {
		t.Fatalf("CreateSessionFromDeviceApproval: %v", err)
	}
	if session.GatewayKey != "" || session.GatewayKeyID != "" {
		t.Errorf("another user inherited the gateway key %q/%q", session.GatewayKey, session.GatewayKeyID)
	}
	if session.ActiveProjectID != "" {
		t.Errorf("another user inherited the active project %q", session.ActiveProjectID)
	}
}
