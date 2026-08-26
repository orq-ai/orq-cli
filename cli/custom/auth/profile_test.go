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
