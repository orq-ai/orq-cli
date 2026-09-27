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

// staleServer answers /access-token with a fresh token and every other path
// with 401 authz_stale unless the request carries that fresh token, the way
// the platform rejects a token minted before the workspace's authz epoch moved.
// alwaysStale keeps rejecting even the fresh token.
func staleServer(t *testing.T, fresh string, alwaysStale bool) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var exchanges, calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access-token") {
			exchanges.Add(1)
			fmt.Fprintf(w, `{"access_token":%q}`, fresh)
			return
		}
		calls.Add(1)
		if alwaysStale || r.Header.Get("Authorization") != "Bearer "+fresh {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":"authz_stale","message":"Authorization token is invalid."}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, `{"echo":%q}`, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &exchanges, &calls
}

// sessionWithToken logs a session in against srv holding stale as the cached
// token for workspace ws scoped to project.
func sessionWithToken(t *testing.T, srv *httptest.Server, ws, project, stale string) {
	t.Helper()
	isolateHome(t)
	loginAgainst(t, srv)
	s := validSession(ws)
	u := ResolveURLs(srv.URL)
	s.APIBaseURL, s.V1BaseURL, s.AuthBaseURL, s.ProfileBaseURL = u.APIBaseURL, u.V1BaseURL, u.AuthBaseURL, u.ProfileBaseURL
	s.WorkspaceTokens[TokenCacheKey(ws, project)] = StoredAccessToken{Token: stale, ExpiresAt: "2099-01-01T00:00:00Z"}
	if err := SaveSession(s); err != nil {
		t.Fatal(err)
	}
}

func doPost(t *testing.T, srv *httptest.Server, bearer string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v2/projects", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	res, err := (&http.Client{Transport: NewStaleRetryTransport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestStaleRetryRefreshesSessionTokenOnce(t *testing.T) {
	fresh := jwtWithExpiry(t, time.Now().Add(time.Hour))
	srv, exchanges, calls := staleServer(t, fresh, false)
	sessionWithToken(t, srv, "orq-research", "proj-1", "stale-token")
	t.Setenv("ORQ_API_KEY", "stale-token")

	res := doPost(t, srv, "stale-token")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200 after refresh", res.StatusCode, body)
	}
	if !strings.Contains(string(body), `{\"name\":\"x\"}`) {
		t.Errorf("retry lost the request body: %s", body)
	}
	if exchanges.Load() != 1 || calls.Load() != 2 {
		t.Errorf("exchanges=%d calls=%d, want 1 and 2", exchanges.Load(), calls.Load())
	}
	got, _ := ReadSession()
	if tok := got.WorkspaceTokens[TokenCacheKey("orq-research", "proj-1")].Token; tok != fresh {
		t.Errorf("cached token = %q, want the fresh one", tok)
	}
	if env := os.Getenv("ORQ_API_KEY"); env != fresh {
		t.Errorf("ORQ_API_KEY = %q, want the fresh token", env)
	}
}

func TestStaleRetryLeavesExplicitKeyAlone(t *testing.T) {
	srv, exchanges, calls := staleServer(t, jwtWithExpiry(t, time.Now().Add(time.Hour)), false)
	sessionWithToken(t, srv, "orq-research", "", "session-token")

	res := doPost(t, srv, "user-api-key")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want the 401 passed through", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), AuthzStaleCode) {
		t.Errorf("401 body lost: %s", body)
	}
	if exchanges.Load() != 0 || calls.Load() != 1 {
		t.Errorf("exchanges=%d calls=%d, want 0 and 1", exchanges.Load(), calls.Load())
	}
}

func TestStaleRetryDoesNotLoop(t *testing.T) {
	srv, exchanges, calls := staleServer(t, jwtWithExpiry(t, time.Now().Add(time.Hour)), true)
	sessionWithToken(t, srv, "orq-research", "", "stale-token")

	res := doPost(t, srv, "stale-token")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 after the single retry", res.StatusCode)
	}
	if exchanges.Load() != 1 || calls.Load() != 2 {
		t.Errorf("exchanges=%d calls=%d, want 1 and 2", exchanges.Load(), calls.Load())
	}
}
