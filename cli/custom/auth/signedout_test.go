package auth

import (
	"bytes"
	"fmt"
	bartolocli "github.com/orq-ai/bartolo/cli"
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

	// Refresh the active login (the realistic case), so the active host and the
	// session's own host are the same file. markSessionSignedOut now writes to the
	// session's own host rather than the active one; pinning them together keeps this
	// a single-file test instead of leaving a stray active-host copy.
	t.Setenv("ORQ_SERVER", srv.URL)

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

// A valid, active login asking for a workspace or project it cannot access gets
// a 400 "Account not found!" from the same /access-token endpoint. That is an
// authorization miss (e.g. a stale ActiveProjectID), not a dead refresh token,
// so the whole session must not be signed out — only the request errors.
func TestEnsureWorkspaceTokenDoesNotSignOutOnWorkspaceMiss(t *testing.T) {
	isolateHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"Account not found!"}`)
	}))
	defer srv.Close()

	s := validSession("acme")
	s.APIBaseURL = srv.URL
	s.RefreshToken = "live-refresh"
	if err := SaveSession(s); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if _, err := clientFor(srv).EnsureWorkspaceToken(s, "no-such-ws"); err == nil {
		t.Fatal("EnsureWorkspaceToken succeeded, want the server's rejection")
	}
	if s.SignedOut {
		t.Error("a workspace/project access miss must not mark the session SignedOut")
	}
	rows, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 1 || rows[0].Status == SessionStatusSignedOut {
		t.Fatalf("persisted rows = %+v, want not signed-out", rows)
	}
}

// The mirror of the workspace-miss case: when EnsureWorkspaceToken's refresh is
// rejected for the refresh token itself (400 "Invalid refresh token!"), the whole
// login is dead and must be marked signed-out, exactly as EnsureBootstrapToken
// does. This pins the markSessionSignedOut call inside EnsureWorkspaceToken, so
// deleting it fails the suite rather than shipping a silent dead login.
func TestEnsureWorkspaceTokenMarksSignedOutOnRejectedRefresh(t *testing.T) {
	isolateHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"Invalid refresh token!"}`)
	}))
	defer srv.Close()
	t.Setenv("ORQ_SERVER", srv.URL)

	s := validSession("acme")
	s.APIBaseURL = srv.URL
	s.RefreshToken = "dead-refresh"
	if err := SaveSession(s); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if _, err := clientFor(srv).EnsureWorkspaceToken(s, "acme"); err == nil {
		t.Fatal("EnsureWorkspaceToken succeeded, want the server's rejection")
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

// RefreshRejected is the crux of the login-health fix: the auth server ends a
// login: a 400 naming the refresh token, or a 403 deactivation. It must NOT
// match the 400 "Account not found!" that a live login gets for a workspace or
// project it cannot access, nor any transient failure.
func TestRefreshRejected(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"400 invalid refresh token", &APIError{Status: http.StatusBadRequest, Msg: "Invalid refresh token!"}, true},
		{"403 account deactivated", &APIError{Status: http.StatusForbidden, Msg: "Account has been deactivated"}, true},
		{"401 any body", &APIError{Status: http.StatusUnauthorized, Msg: "unauthorized"}, true},
		// A valid, active login asking for a workspace/project it cannot access:
		// the endpoint answers 400 "Account not found!". This is an authorization
		// miss, not a dead login, so it must not sign the whole session out.
		{"400 account not found (workspace/project miss)", &APIError{Status: http.StatusBadRequest, Msg: "Account not found!"}, false},
		{"403 forbidden route (not deactivation)", &APIError{Status: http.StatusForbidden, Msg: "This API key type cannot access this endpoint"}, false},
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

	previous, source := Server(), ServerSource()
	SetServer(srv.URL, "flag")
	t.Cleanup(func() { SetServer(previous, source) })

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

func TestEnsureWorkspaceTokenClearsSignedOutOnSuccess(t *testing.T) {
	isolateHome(t)
	srv := loginServer(t, "user-1", "acme")

	previous, source := Server(), ServerSource()
	SetServer(srv.URL, "flag")
	t.Cleanup(func() { SetServer(previous, source) })

	s := validSession("acme")
	s.APIBaseURL = srv.URL
	s.SignedOut = true
	s.BootstrapToken = StoredAccessToken{Token: "boot", ExpiresAt: formatISO(time.Now().Add(-time.Hour))}
	if err := SaveSession(s); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	out, err := clientFor(srv).EnsureWorkspaceToken(s, "acme")
	if err != nil {
		t.Fatalf("EnsureWorkspaceToken: %v", err)
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

	t.Run("unreachable server with a live bootstrap is ok", func(t *testing.T) {
		// --check always probes now; the probe to an unreachable host fails, but a
		// still-valid local bootstrap means the login is usable right now, so ok.
		host := save(t, "https://unused.invalid", future)
		if got := CheckSession(host); got != SessionStatusOK {
			t.Fatalf("got %q, want ok", got)
		}
	})

	t.Run("live bootstrap but server rejects the refresh is signed-out", func(t *testing.T) {
		// The headline of RES-1692: --check asks the server even when the local
		// bootstrap token is still valid, so a refresh revoked minutes ago reads
		// signed-out, not ok from the local clock alone. Without the always-probe
		// change this returned ok.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"message":"Invalid refresh token!"}`)
		}))
		defer srv.Close()
		host := save(t, srv.URL, future) // bootstrap NOT expired
		if got := CheckSession(host); got != SessionStatusSignedOut {
			t.Fatalf("got %q, want signed-out", got)
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

// A fresh `orq auth login` is a new credential, so the new session must not
// inherit a signed-out mark from the prior rejected one — otherwise the working
// login still reads dead.
func TestSessionForLoginClearsSignedOut(t *testing.T) {
	fresh := sessionForLogin(&Session{SignedOut: true, RefreshToken: "r"})
	if fresh.SignedOut {
		t.Error("sessionForLogin must clear SignedOut; a fresh login is not signed out")
	}
}

// markSessionSignedOut must write the mark to the session's OWN host file, not
// the active host's, so refreshing a non-active login does not stamp the wrong file.
func TestMarkSignedOutWritesOwnHostNotActive(t *testing.T) {
	isolateHome(t)
	t.Setenv("ORQ_SERVER", "https://active.invalid") // the active host

	s := validSession("acme")
	s.APIBaseURL = "https://other.invalid" // a different, non-active login
	if err := saveSessionTo(sessionPathFor(SessionHost(s.APIBaseURL)), s); err != nil {
		t.Fatal(err)
	}
	markSessionSignedOut(s)

	own, _ := os.ReadFile(sessionPathFor(SessionHost("https://other.invalid")))
	if !strings.Contains(string(own), `"signedOut": true`) {
		t.Fatalf("mark not written to the session's own host file:\n%s", own)
	}
	active, _ := os.ReadFile(sessionPathFor(SessionHost("https://active.invalid")))
	if strings.Contains(string(active), `"signedOut": true`) {
		t.Fatalf("mark wrongly written to the active host file:\n%s", active)
	}
}

func TestCheckSessionWarnsWhenProbeCannotPersist(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			isolateHome(t)
			var path string
			token := jwtWithExpiry(t, time.Now().Add(time.Hour))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// Replace the file after CheckSession has loaded it, deterministically
				// making its final atomic rename fail even when tests run as root.
				if err := os.Remove(path); err != nil {
					t.Error(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Error(err)
				}
				if rejected {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"message":"Invalid refresh token!"}`)
				} else {
					fmt.Fprintf(w, `{"access_token":%q}`, token)
				}
			}))
			defer srv.Close()
			session := validSession("acme")
			session.APIBaseURL = srv.URL
			host := SessionHost(srv.URL)
			path = sessionPathFor(host)
			if err := saveSessionTo(path, session); err != nil {
				t.Fatal(err)
			}
			var warning bytes.Buffer
			previous := bartolocli.Stderr
			bartolocli.Stderr = &warning
			t.Cleanup(func() { bartolocli.Stderr = previous })
			status := CheckSession(host)
			want := SessionStatusOK
			if rejected {
				want = SessionStatusSignedOut
			}
			if status != want {
				t.Fatalf("status = %q, want %q", status, want)
			}
			if !strings.Contains(warning.String(), "orq: could not") || !strings.Contains(warning.String(), path) {
				t.Fatalf("missing persistence warning: %s", warning.String())
			}
		})
	}
}

func TestRefreshRemedyDoesNotBlameLoginForServerError(t *testing.T) {
	original := &APIError{Status: 500, Msg: "refresh token service unavailable"}
	if got := withRefreshTokenRemedy(original, "https://example.com"); got != original {
		t.Fatalf("server error gained a misleading login remedy: %v", got)
	}
	deactivated := &APIError{Status: 403, Msg: "Account has been deactivated"}
	if got := withRefreshTokenRemedy(deactivated, "https://example.com"); got != deactivated {
		t.Fatalf("deactivation must retain server remedy: %v", got)
	}
}

func TestWorkspaceTokenRecordsRefreshHealth(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			isolateHome(t)
			token := jwtWithExpiry(t, time.Now().Add(time.Hour))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if rejected {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"message":"Invalid refresh token!"}`)
					return
				}
				fmt.Fprintf(w, `{"access_token":%q}`, token)
			}))
			defer srv.Close()
			previous, source := Server(), ServerSource()
			SetServer(srv.URL, "flag")
			t.Cleanup(func() { SetServer(previous, source) })
			session := validSession("acme")
			session.APIBaseURL = srv.URL
			session.SignedOut = !rejected
			if err := SaveSession(session); err != nil {
				t.Fatal(err)
			}
			_, err := clientFor(srv).WorkspaceToken(session, "acme")
			if (err != nil) != rejected {
				t.Fatalf("error = %v, rejected = %v", err, rejected)
			}
			persisted, err := ReadSession()
			if err != nil {
				t.Fatal(err)
			}
			if session.SignedOut != rejected || persisted.SignedOut != rejected {
				t.Fatalf("signed-out memory=%v disk=%v, want %v", session.SignedOut, persisted.SignedOut, rejected)
			}
		})
	}
}
