package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// A server-rejected refresh is the server ending the login. The auth server
// answers it with 400 "Invalid refresh token!" (not 401, see RefreshRejected),
// and it must be recorded on the session file so `auth sessions`, `doctor` and
// the hints read the login as dead without another network call.
func TestEnsureBootstrapTokenMarksSignedOutOnRejectedRefresh(t *testing.T) {
	isolateHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"Invalid refresh token!"}`)
	}))
	defer srv.Close()

	s := validSession("acme")
	s.APIBaseURL = srv.URL
	s.RefreshToken = "dead-refresh"
	// Expired bootstrap forces EnsureBootstrapToken to exchange the refresh token.
	s.BootstrapToken = StoredAccessToken{Token: "boot", ExpiresAt: formatISO(time.Now().Add(-time.Hour))}
	if err := SaveSession(s); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if _, err := clientFor(srv).EnsureBootstrapToken(s); err == nil {
		t.Fatal("EnsureBootstrapToken succeeded, want the server's rejection")
	}
	if !s.SignedOut {
		t.Error("in-memory session not marked SignedOut after a rejected refresh")
	}
	rows, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != SessionStatusSignedOut {
		t.Fatalf("persisted rows = %+v, want one row with status %q", rows, SessionStatusSignedOut)
	}
}

// A transient failure (the server is reachable but 500s, or cannot be reached)
// is not a verdict on the login: the refresh must error without marking the
// session signed-out, or a blip would wrongly report a live login as dead.
func TestEnsureBootstrapTokenDoesNotSignOutOnTransientError(t *testing.T) {
	isolateHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"boom"}`)
	}))
	defer srv.Close()

	s := validSession("acme")
	s.APIBaseURL = srv.URL
	s.RefreshToken = "live-refresh"
	s.BootstrapToken = StoredAccessToken{Token: "boot", ExpiresAt: formatISO(time.Now().Add(-time.Hour))}
	if err := SaveSession(s); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if _, err := clientFor(srv).EnsureBootstrapToken(s); err == nil {
		t.Fatal("EnsureBootstrapToken succeeded, want the server error")
	}
	if s.SignedOut {
		t.Error("a transient server error must not mark the session SignedOut")
	}
}

// RefreshRejected is the crux of the login-health fix: the auth server ends a
// login with 400/403 and a refresh-token or account message, never a bare 401
// on a different body. Transient failures must not match.
func TestRefreshRejected(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"400 invalid refresh token", &APIError{Status: http.StatusBadRequest, Msg: "Invalid refresh token!"}, true},
		{"400 account not found", &APIError{Status: http.StatusBadRequest, Msg: "Account not found!"}, true},
		{"403 account deactivated", &APIError{Status: http.StatusForbidden, Msg: "Account has been deactivated"}, true},
		{"401 any body", &APIError{Status: http.StatusUnauthorized, Msg: "unauthorized"}, true},
		{"400 unrelated validation error", &APIError{Status: http.StatusBadRequest, Msg: "workspace_key must be a string"}, false},
		{"500 server error", &APIError{Status: http.StatusInternalServerError, Msg: "Invalid refresh token!"}, false},
		{"transport error", fmt.Errorf("dial tcp: connection refused"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RefreshRejected(tc.err); got != tc.want {
				t.Fatalf("RefreshRejected(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A later successful refresh clears the signed-out mark, so logging in again (or
// a server that accepts the token once more) restores the login.
func TestEnsureBootstrapTokenClearsSignedOutOnSuccess(t *testing.T) {
	isolateHome(t)
	srv := loginServer(t, "user-1", "acme")

	s := validSession("acme")
	s.APIBaseURL = srv.URL
	s.SignedOut = true
	s.BootstrapToken = StoredAccessToken{Token: "boot", ExpiresAt: formatISO(time.Now().Add(-time.Hour))}
	if err := SaveSession(s); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	out, err := clientFor(srv).EnsureBootstrapToken(s)
	if err != nil {
		t.Fatalf("EnsureBootstrapToken: %v", err)
	}
	if out.SignedOut {
		t.Error("a successful refresh must clear SignedOut in memory")
	}
	rows, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 1 || rows[0].Status == SessionStatusSignedOut {
		t.Fatalf("persisted rows = %+v, want not signed-out", rows)
	}
}

// ListSessions reports a persisted SignedOut session as signed-out, so the
// listing shows a dead login without a network call.
func TestListSessionsReportsSignedOut(t *testing.T) {
	isolateHome(t)
	s := validSession("acme")
	s.SignedOut = true
	if err := SaveSession(s); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	rows, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Status != SessionStatusSignedOut {
		t.Fatalf("status = %q, want %q", rows[0].Status, SessionStatusSignedOut)
	}
}

// CheckSession probes one login against its server for `auth sessions --check`:
// a live bootstrap is ok without a call, an expired one that the server rejects
// is signed-out, an expired one the server accepts is ok, and a server that
// cannot be reached stays needs-refresh rather than being called dead.
func TestCheckSession(t *testing.T) {
	future := formatISO(time.Now().Add(time.Hour))
	past := formatISO(time.Now().Add(-time.Hour))

	// Write the fixture to the probed host's own file (not via SaveSession,
	// which targets the active host) so the test exercises CheckSession's
	// real per-host read and write, no active-server crutch.
	save := func(t *testing.T, apiBase, bootstrapExpiry string) string {
		t.Helper()
		isolateHome(t)
		s := validSession("acme")
		s.APIBaseURL = apiBase
		s.BootstrapToken = StoredAccessToken{Token: "boot", ExpiresAt: bootstrapExpiry}
		host := SessionHost(apiBase)
		if err := saveSessionTo(sessionPathFor(host), s); err != nil {
			t.Fatal(err)
		}
		return host
	}

	t.Run("live bootstrap is ok without a network call", func(t *testing.T) {
		host := save(t, "https://unused.invalid", future)
		if got := CheckSession(host); got != SessionStatusOK {
			t.Fatalf("got %q, want ok", got)
		}
	})

	t.Run("expired and rejected is signed-out", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"message":"Invalid refresh token!"}`)
		}))
		defer srv.Close()
		host := save(t, srv.URL, past)
		if got := CheckSession(host); got != SessionStatusSignedOut {
			t.Fatalf("got %q, want signed-out", got)
		}
	})

	t.Run("server error stays needs-refresh (not a verdict)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"message":"boom"}`)
		}))
		defer srv.Close()
		host := save(t, srv.URL, past)
		if got := CheckSession(host); got != SessionStatusNeedsRefresh {
			t.Fatalf("got %q, want needs-refresh", got)
		}
	})

	t.Run("expired and accepted is ok", func(t *testing.T) {
		srv := loginServer(t, "user-1", "acme")
		host := save(t, srv.URL, past)
		if got := CheckSession(host); got != SessionStatusOK {
			t.Fatalf("got %q, want ok", got)
		}
	})

	t.Run("unreachable server stays needs-refresh", func(t *testing.T) {
		host := save(t, "http://127.0.0.1:0", past) // nothing listening
		if got := CheckSession(host); got != SessionStatusNeedsRefresh {
			t.Fatalf("got %q, want needs-refresh", got)
		}
	})

	t.Run("missing file is unreadable", func(t *testing.T) {
		isolateHome(t)
		if got := CheckSession("nope.invalid"); got != SessionStatusUnreadable {
			t.Fatalf("got %q, want unreadable", got)
		}
	})
}

// TestCheckSessionPersistsToProbedHostNotActive is the regression for the bug
// where CheckSession refreshed through EnsureBootstrapToken, whose SaveSession
// always wrote the active host's file. Probing a non-active login must record
// its result on its own file and leave the active login untouched.
func TestCheckSessionPersistsToProbedHostNotActive(t *testing.T) {
	isolateHome(t)

	// A healthy active login that --check must not touch.
	active := validSession("acme")
	active.APIBaseURL = "https://active.invalid"
	active.BootstrapToken = StoredAccessToken{Token: "boot", ExpiresAt: formatISO(time.Now().Add(time.Hour))}
	activeHost := SessionHost(active.APIBaseURL)
	if err := saveSessionTo(sessionPathFor(activeHost), active); err != nil {
		t.Fatal(err)
	}

	// A different, non-active login whose server rejects the refresh.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"Invalid refresh token!"}`)
	}))
	defer srv.Close()
	other := validSession("acme")
	other.APIBaseURL = srv.URL
	other.BootstrapToken = StoredAccessToken{Token: "boot", ExpiresAt: formatISO(time.Now().Add(-time.Hour))}
	otherHost := SessionHost(srv.URL)
	if err := saveSessionTo(sessionPathFor(otherHost), other); err != nil {
		t.Fatal(err)
	}

	if got := CheckSession(otherHost); got != SessionStatusSignedOut {
		t.Fatalf("CheckSession(other) = %q, want signed-out", got)
	}
	// The probed host's file records signed-out.
	if data, _ := os.ReadFile(sessionPathFor(otherHost)); !strings.Contains(string(data), `"signedOut": true`) {
		t.Fatalf("probed host file not marked signed-out:\n%s", data)
	}
	// The active host's file is untouched.
	if data, _ := os.ReadFile(sessionPathFor(activeHost)); strings.Contains(string(data), `"signedOut": true`) {
		t.Fatalf("active host file was wrongly modified:\n%s", data)
	}
}
