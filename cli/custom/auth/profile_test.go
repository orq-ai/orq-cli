package auth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The REST profile endpoint (GET /v2/api/me) was deleted when profiles moved to
// identity-api, so a default that still points there 404s every fresh setup.
func TestResolveURLsDefaultsProfileToTheIdentityRPC(t *testing.T) {
	t.Setenv("ORQ_PROFILE_BASE_URL", "")
	t.Setenv("ORQ_API_BASE_URL", "")
	got := ResolveURLs("https://api.orq.ai/").ProfileBaseURL
	want := "https://api.orq.ai" + ProfileRPCPath
	if got != want {
		t.Errorf("profile base url = %q, want %q", got, want)
	}
}

// Connect unary calls are POSTs carrying a JSON request message, and the reply
// wraps the profile in a GetProfileResponse envelope. A GET, or reading the
// body as a bare profile, gets nothing back.
func TestFetchProfileCallsTheRPCAndUnwrapsTheEnvelope(t *testing.T) {
	var method, body, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"profile":{"id":"u1","email":"a@b.c","display_name":"A",
		 "workspaces":[{"key":"acme"}],"preferences":{"active_workspace":"acme"}}}`)
	}))
	defer srv.Close()

	t.Setenv("ORQ_PROFILE_BASE_URL", srv.URL)
	profile, err := NewClient(srv.URL).FetchProfile("bootstrap-token")
	if err != nil {
		t.Fatalf("fetch profile: %v", err)
	}
	if method != http.MethodPost {
		t.Errorf("method = %s, want POST", method)
	}
	if body != "{}" {
		t.Errorf("request body = %q, want an empty JSON message", body)
	}
	if auth != "Bearer bootstrap-token" {
		t.Errorf("authorization = %q", auth)
	}
	if profile.Email != "a@b.c" || len(profile.Workspaces) != 1 {
		t.Errorf("profile not unwrapped from the envelope: %+v", profile)
	}
	if profile.Preferences.ActiveWorkspace != "acme" {
		t.Errorf("active workspace = %q", profile.Preferences.ActiveWorkspace)
	}
}

// A deployment without the /v3/rpc route answers the Connect POST with a proxy
// 404/405, which used to end `orq setup` right after the browser approval. The
// CLI has to fall back to the REST endpoint such a deployment still serves.
func TestFetchProfileFallsBackToTheLegacyRESTEndpoint(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.URL.Path == ProfileRPCPath {
			w.WriteHeader(http.StatusMethodNotAllowed)
			fmt.Fprint(w, "<html><body>405 Not Allowed</body></html>")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"u1","email":"a@b.c","display_name":"A",
		 "workspaces":[{"key":"acme"}],"preferences":{"active_workspace":"acme"}}`)
	}))
	defer srv.Close()

	t.Setenv("ORQ_PROFILE_BASE_URL", "")
	t.Setenv("ORQ_V1_BASE_URL", "")
	profile, err := NewClient(srv.URL).FetchProfile("bootstrap-token")
	if err != nil {
		t.Fatalf("fetch profile: %v", err)
	}
	if profile.Email != "a@b.c" || profile.Preferences.ActiveWorkspace != "acme" {
		t.Errorf("legacy profile not parsed: %+v", profile)
	}
	want := []string{"POST " + ProfileRPCPath, "GET /v2/api/me"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("requests = %v, want %v", paths, want)
	}
}

// A credential the RPC itself rejects must keep its own error: retrying the old
// endpoint would report a dead token as a missing route.
func TestFetchProfileDoesNotFallBackOnAuthFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"unauthorized"}`)
	}))
	defer srv.Close()

	t.Setenv("ORQ_PROFILE_BASE_URL", "")
	t.Setenv("ORQ_V1_BASE_URL", "")
	if _, err := NewClient(srv.URL).FetchProfile("dead-token"); err == nil {
		t.Fatal("expected an error")
	} else if !Unauthorized(err) {
		t.Errorf("error = %v, want the RPC's own 401", err)
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1", calls)
	}
}
