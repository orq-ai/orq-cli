package auth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A deployment without the /v3/rpc route answers the Connect POST from its
// proxy, which used to end `orq setup` right after the browser approval. Both
// statuses a proxy sends for a route it does not have are covered: nginx says
// 404 for an unknown location and 405 for a known one that refuses POST.
func TestFetchProfileFallsBackToTheLegacyRESTEndpoint(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(fmt.Sprintf("rpc_%d", status), func(t *testing.T) {
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.Method+" "+r.URL.Path)
				if r.URL.Path == ProfileRPCPath {
					w.WriteHeader(status)
					fmt.Fprint(w, "<html><body>Not Allowed</body></html>")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"u1","email":"a@b.c","display_name":"A",
				 "workspaces":[{"key":"acme"}],"preferences":{"active_workspace":"acme"}}`)
			}))
			defer srv.Close()

			t.Setenv("ORQ_PROFILE_BASE_URL", "")
			t.Setenv("ORQ_V1_BASE_URL", "")
			client := NewClient(srv.URL)
			profile, err := client.FetchProfile("bootstrap-token")
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
			if client.ProfileTransport() != ProfileTransportREST {
				t.Errorf("transport = %q, want %q", client.ProfileTransport(), ProfileTransportREST)
			}
		})
	}
}

// A host already known to serve the legacy endpoint must not re-probe the RPC
// before every fetch: FetchProfile runs on `whoami`, `workspace use` and every
// command that resolves a workspace token, not only on setup.
func TestFetchProfileStartsWithTheRecordedTransport(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"u1","email":"a@b.c","workspaces":[{"key":"acme"}]}`)
	}))
	defer srv.Close()

	t.Setenv("ORQ_PROFILE_BASE_URL", "")
	t.Setenv("ORQ_V1_BASE_URL", "")
	if _, err := NewClient(srv.URL).WithProfileTransport(ProfileTransportREST).FetchProfile("tok"); err != nil {
		t.Fatalf("fetch profile: %v", err)
	}
	if len(paths) != 1 || paths[0] != "GET /v2/api/me" {
		t.Errorf("requests = %v, want the legacy endpoint alone", paths)
	}
}

// A credential the endpoint itself rejects must keep its own error: retrying
// the other endpoint would report a dead token as a missing route.
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

// A host routing neither endpoint is the case that produced the original bug
// report — a page of nginx markup where an error message belongs. The message
// names both URLs and both statuses and carries neither body.
func TestFetchProfileBothFailedReportsStatusesNotBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "<html><head><title>404 Not Found</title></head><body>nginx</body></html>")
	}))
	defer srv.Close()

	t.Setenv("ORQ_PROFILE_BASE_URL", "")
	t.Setenv("ORQ_V1_BASE_URL", "")
	_, err := NewClient(srv.URL).FetchProfile("bootstrap-token")
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if strings.Contains(msg, "<html") || strings.Contains(msg, "nginx") {
		t.Errorf("error leaks the proxy body: %s", msg)
	}
	for _, want := range []string{ProfileRPCPath, "/v2/api/me", "HTTP 404"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
}
