package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A server-rejected refresh (401) is the server ending the login. It must be
// recorded on the session file so `auth sessions`, `doctor` and the hints read
// the login as dead without another network call.
func TestEnsureBootstrapTokenMarksSignedOutOnRejectedRefresh(t *testing.T) {
	isolateHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
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

	// SaveSession writes to the active resolved host's file, so point the active
	// server at the fixture's host and return the host both share.
	save := func(t *testing.T, apiBase, bootstrapExpiry string) string {
		t.Helper()
		isolateHome(t)
		prev, prevSrc := Server(), ServerSource()
		SetServer(apiBase, "flag")
		t.Cleanup(func() { SetServer(prev, prevSrc) })
		s := validSession("acme")
		s.APIBaseURL = apiBase
		s.BootstrapToken = StoredAccessToken{Token: "boot", ExpiresAt: bootstrapExpiry}
		if err := SaveSession(s); err != nil {
			t.Fatal(err)
		}
		return SessionHost(apiBase)
	}

	t.Run("live bootstrap is ok without a network call", func(t *testing.T) {
		host := save(t, "https://unused.invalid", future)
		if got := CheckSession(host); got != SessionStatusOK {
			t.Fatalf("got %q, want ok", got)
		}
	})

	t.Run("expired and rejected is signed-out", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Invalid refresh token!"}`)
		}))
		defer srv.Close()
		host := save(t, srv.URL, past)
		if got := CheckSession(host); got != SessionStatusSignedOut {
			t.Fatalf("got %q, want signed-out", got)
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
