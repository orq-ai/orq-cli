package auth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// staleFixture is a server that rejects every token but fresh with reject, the
// way the platform answers a token minted before the workspace's authz epoch
// moved. /access-token hands out fresh, or fails with exchangeFail when set.
type staleFixture struct {
	srv          *httptest.Server
	fresh        string
	reject       string // 401 body for any other token
	alwaysReject bool   // reject fresh too
	exchangeFail string // 401 body for /access-token
	exchanges    atomic.Int32
	calls        atomic.Int32
}

const staleBody = `{"code":"authz_stale","message":"Authorization token is invalid."}`

func newStaleFixture(t *testing.T) *staleFixture {
	t.Helper()
	f := &staleFixture{fresh: jwtWithExpiry(t, time.Now().Add(time.Hour)), reject: staleBody}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access-token") {
			f.exchanges.Add(1)
			if f.exchangeFail != "" {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, f.exchangeFail)
				return
			}
			fmt.Fprintf(w, `{"access_token":%q}`, f.fresh)
			return
		}
		f.calls.Add(1)
		if f.alwaysReject || r.Header.Get("Authorization") != "Bearer "+f.fresh {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, f.reject)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost && !strings.Contains(string(body), `"name":"x"`) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"message":"body lost: %s"}`, body)
			return
		}
		fmt.Fprint(w, `{"project":{"project_id":"p1"},"data":[]}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// login writes a session against the fixture with stale cached for ws/project.
func (f *staleFixture) login(t *testing.T, ws, project, stale string) *Session {
	t.Helper()
	isolateHome(t)
	loginAgainst(t, f.srv)
	s := validSession(ws)
	u := ResolveURLs(f.srv.URL)
	s.APIBaseURL, s.V1BaseURL, s.AuthBaseURL, s.ProfileBaseURL = u.APIBaseURL, u.V1BaseURL, u.AuthBaseURL, u.ProfileBaseURL
	s.WorkspaceTokens[TokenCacheKey(ws, project)] = StoredAccessToken{Token: stale, ExpiresAt: "2099-01-01T00:00:00Z"}
	if err := SaveSession(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *staleFixture) counts(t *testing.T, exchanges, calls int32) {
	t.Helper()
	if f.exchanges.Load() != exchanges || f.calls.Load() != calls {
		t.Errorf("exchanges=%d calls=%d, want %d and %d", f.exchanges.Load(), f.calls.Load(), exchanges, calls)
	}
}

// Through auth.Client, so dropping the transport from NewClient fails here.
func TestStaleRetryRefreshesSessionTokenOnce(t *testing.T) {
	f := newStaleFixture(t)
	f.login(t, "orq-research", "proj-1", "stale-token")
	t.Setenv("ORQ_API_KEY", "stale-token")

	if _, err := NewClient(f.srv.URL).CreateProject("stale-token", "x", ""); err != nil {
		t.Fatalf("CreateProject after refresh: %v", err)
	}
	f.counts(t, 1, 2)
	got, _ := ReadSession()
	if tok := got.WorkspaceTokens[TokenCacheKey("orq-research", "proj-1")].Token; tok != f.fresh {
		t.Errorf("cached token = %q, want the fresh one", tok)
	}
	if env := os.Getenv("ORQ_API_KEY"); env != f.fresh {
		t.Errorf("ORQ_API_KEY = %q, want the fresh token", env)
	}
	if CurrentToken("stale-token") != f.fresh {
		t.Errorf("CurrentToken does not map the replaced token to the fresh one")
	}
}

func TestStaleRetryWithoutBody(t *testing.T) {
	f := newStaleFixture(t)
	f.login(t, "orq-research", "", "stale-token")
	if err := NewClient(f.srv.URL).ProbeToken("stale-token"); err != nil {
		t.Fatalf("ProbeToken after refresh: %v", err)
	}
	f.counts(t, 1, 2)
}

// A 401 that is not authz_stale is the server's real answer, and retrying it
// would replay a POST such as projects create.
func TestStaleRetryIgnoresOtherUnauthorized(t *testing.T) {
	for name, body := range map[string]string{
		"revoked":    `{"message":"token revoked"}`,
		"other code": `{"code":"unauthorized"}`,
		"not json":   `Unauthorized`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newStaleFixture(t)
			f.reject = body
			f.login(t, "orq-research", "", "stale-token")
			_, err := NewClient(f.srv.URL).CreateProject("stale-token", "x", "")
			if !Unauthorized(err) {
				t.Fatalf("err = %v, want the 401 passed through", err)
			}
			f.counts(t, 0, 1)
		})
	}
}

func TestStaleRetryLeavesExplicitKeyAlone(t *testing.T) {
	f := newStaleFixture(t)
	f.login(t, "orq-research", "", "session-token")
	_, err := NewClient(f.srv.URL).CreateProject("user-api-key", "x", "")
	if !Unauthorized(err) || !strings.Contains(err.Error(), "Authorization token is invalid") {
		t.Fatalf("err = %v, want the stale 401 passed through", err)
	}
	f.counts(t, 0, 1)
}

func TestStaleRetryDoesNotLoop(t *testing.T) {
	f := newStaleFixture(t)
	f.alwaysReject = true
	f.login(t, "orq-research", "", "stale-token")
	if _, err := NewClient(f.srv.URL).CreateProject("stale-token", "x", ""); !Unauthorized(err) {
		t.Fatalf("err = %v, want 401 after the single retry", err)
	}
	f.counts(t, 1, 2)
}

// A dead login must say so, not show the stale 401 that no retry can fix.
func TestStaleRetrySurfacesRefreshFailure(t *testing.T) {
	f := newStaleFixture(t)
	f.exchangeFail = `{"message":"refresh token revoked"}`
	f.login(t, "orq-research", "", "stale-token")
	_, err := NewClient(f.srv.URL).CreateProject("stale-token", "x", "")
	if err == nil || !strings.Contains(err.Error(), "orq auth login") {
		t.Fatalf("err = %v, want the refresh failure with its login remedy", err)
	}
}

// Another process already replaced the stale token: reuse its replacement
// instead of failing to find the old value.
func TestStaleRetryReusesConcurrentRefresh(t *testing.T) {
	f := newStaleFixture(t)
	s := f.login(t, "orq-research", "", f.fresh)
	s.StaleTokens = map[string]StaleToken{tokenHash("stale-token"): {Key: "orq-research", ExpiresAt: "2099-01-01T00:00:00Z"}}
	if err := SaveSession(s); err != nil {
		t.Fatal(err)
	}
	if err := NewClient(f.srv.URL).ProbeToken("stale-token"); err != nil {
		t.Fatalf("ProbeToken: %v", err)
	}
	f.counts(t, 0, 2)
}

// A body too large to buffer streams through once and keeps its 401.
func TestStaleRetrySkipsLargeBody(t *testing.T) {
	f := newStaleFixture(t)
	f.login(t, "orq-research", "", "stale-token")
	big := strings.NewReader(strings.Repeat("a", maxReplayBody+1))
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/v2/files", io.NopCloser(big))
	req.Header.Set("Authorization", "Bearer stale-token")
	res, err := NewHTTPClient(0).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want the 401", res.StatusCode)
	}
	f.counts(t, 0, 1)
}

func TestSaveSessionPrunesExpiredStaleTokens(t *testing.T) {
	isolateHome(t)
	s := validSession("ws")
	s.StaleTokens = map[string]StaleToken{
		"live": {Key: "ws", ExpiresAt: "2099-01-01T00:00:00Z"},
		"dead": {Key: "ws", ExpiresAt: "2000-01-01T00:00:00Z"},
	}
	if err := SaveSession(s); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadSession()
	if _, ok := got.StaleTokens["dead"]; ok || len(got.StaleTokens) != 1 {
		t.Errorf("StaleTokens = %v, want only the live record", got.StaleTokens)
	}
}
