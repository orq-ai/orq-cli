package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// tokenTestEnv isolates HOME and the format, and seeds a login on apiBase
// whose active pair is acme / project p1 with the given cached tokens.
func tokenTestEnv(t *testing.T, apiBase string, tokens map[string]auth.StoredAccessToken) {
	t.Helper()
	switchTestEnv(t)
	prevExplicit, prevScope := explicitAPIKey, sessionScope
	t.Cleanup(func() { explicitAPIKey, sessionScope = prevExplicit, prevScope })
	explicitAPIKey = false
	SetSessionScope("", "")
	origServer, origSource := auth.Server(), auth.ServerSource()
	auth.SetServer(apiBase, "flag")
	t.Cleanup(func() { auth.SetServer(origServer, origSource) })
	workspace := "acme"
	exp := time.Now().Add(time.Hour).Format(time.RFC3339)
	if err := auth.SaveSession(&auth.Session{
		Version: 1, APIBaseURL: apiBase, AuthBaseURL: apiBase, V1BaseURL: apiBase, ProfileBaseURL: apiBase,
		RefreshToken:       "refresh",
		BootstrapToken:     auth.StoredAccessToken{Token: "bootstrap", ExpiresAt: exp},
		ActiveWorkspaceKey: &workspace,
		ActiveProjectID:    "p1",
		WorkspaceTokens:    tokens,
	}); err != nil {
		t.Fatal(err)
	}
}

func runToken(t *testing.T, format string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	previous := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = previous })
	cmd := NewTokenCommand()
	// -o is the root's persistent flag in a real tree; declare it here so a
	// typed -o reaches the command the same way.
	cmd.Flags().StringP("output-format", "o", "", "")
	if format != "" {
		args = append(args, "-o", format)
	}
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func decodeTokenReport(t *testing.T, out string) tokenReport {
	t.Helper()
	var r tokenReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("auth token -o json is not JSON: %v\n%s", err, out)
	}
	return r
}

// The PreRun records the pair it resolved (--workspace/--project included);
// auth token reports that slot, not the active pair.
func TestAuthTokenReportsThePreRunScope(t *testing.T) {
	exp := time.Now().Add(time.Hour).Format(time.RFC3339)
	tokenTestEnv(t, "http://127.0.0.1:1", map[string]auth.StoredAccessToken{
		"acme#p1":  {Token: "tok-active", ExpiresAt: exp},
		"other#p9": {Token: "tok-override", ExpiresAt: exp},
	})
	SetSessionScope("other", "p9")

	out, err := runToken(t, "json")
	if err != nil {
		t.Fatal(err)
	}
	r := decodeTokenReport(t, out)
	want := tokenReport{Token: "tok-override", ExpiresAt: exp, Workspace: "other", ProjectID: "p9", Server: "http://127.0.0.1:1"}
	if r != want {
		t.Errorf("report = %+v, want %+v", r, want)
	}
}

// Without a typed -o the output is the token alone, so $(orq auth token) works
// even where a standing default asks for JSON.
func TestAuthTokenPrintsTheBareToken(t *testing.T) {
	exp := time.Now().Add(time.Hour).Format(time.RFC3339)
	tokenTestEnv(t, "http://127.0.0.1:1", map[string]auth.StoredAccessToken{"acme#p1": {Token: "tok-active", ExpiresAt: exp}})
	t.Setenv("ORQ_OUTPUT_FORMAT", "json")

	out, err := runToken(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "tok-active\n" {
		t.Errorf("output = %q, want the bare token and a newline", out)
	}
}

// With an API key in force the PreRun records no scope, so the active pair's
// cached token is the answer, and an unexpired one needs no request.
func TestAuthTokenUsesTheActivePairUnderAnExplicitKey(t *testing.T) {
	exp := time.Now().Add(time.Hour).Format(time.RFC3339)
	tokenTestEnv(t, "http://127.0.0.1:1", map[string]auth.StoredAccessToken{"acme#p1": {Token: "tok-active", ExpiresAt: exp}})
	t.Setenv("ORQ_API_KEY", "sk-orq-user-key")
	explicitAPIKey = true

	out, err := runToken(t, "json")
	if err != nil {
		t.Fatal(err)
	}
	if r := decodeTokenReport(t, out); r.Token != "tok-active" || r.Workspace != "acme" || r.ProjectID != "p1" {
		t.Errorf("report = %+v, want the active acme/p1 token, never the API key", r)
	}
}

// An expired token, which the PreRun leaves in the slot when its own exchange
// failed, is replaced by a fresh exchange, and the new token is cached.
func TestAuthTokenExchangesAnExpiredToken(t *testing.T) {
	const fresh = "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjQxMDI0NDQ4MDB9.sig" // exp 2100-01-01
	var exchanges atomic.Int32
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/access-token") {
			http.NotFound(w, r)
			return
		}
		exchanges.Add(1)
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q}`, fresh)
	}))
	t.Cleanup(srv.Close)
	past := time.Now().Add(-time.Minute).Format(time.RFC3339)
	tokenTestEnv(t, srv.URL, map[string]auth.StoredAccessToken{"acme#p1": {Token: "tok-stale", ExpiresAt: past}})
	SetSessionScope("acme", "p1")

	out, err := runToken(t, "json")
	if err != nil {
		t.Fatal(err)
	}
	r := decodeTokenReport(t, out)
	if r.Token != fresh || exchanges.Load() != 1 {
		t.Fatalf("token = %q after %d exchanges, want the fresh token after one", r.Token, exchanges.Load())
	}
	if body["workspace_key"] != "acme" || body["project_id"] != "p1" || body["refresh_token"] != "refresh" {
		t.Errorf("exchange body = %v, want the active pair and the session's refresh token", body)
	}
	if !strings.HasPrefix(r.ExpiresAt, "2100-01-01") {
		t.Errorf("expires_at = %q, want the fresh token's expiry", r.ExpiresAt)
	}
	session, err := auth.ReadSession()
	if err != nil || session == nil || session.WorkspaceTokens["acme#p1"].Token != fresh {
		t.Errorf("fresh token not cached in the session (err %v)", err)
	}
}

func TestAuthTokenWithoutAnActiveWorkspaceSaysSo(t *testing.T) {
	tokenTestEnv(t, "http://127.0.0.1:1", nil)
	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	session.ActiveWorkspaceKey = nil
	if err := auth.SaveSession(session); err != nil {
		t.Fatal(err)
	}
	if _, err := runToken(t, ""); err == nil || !strings.Contains(err.Error(), "orq workspace use") {
		t.Fatalf("err = %v, want the workspace use remedy", err)
	}
}

func TestAuthTokenWithoutALoginSaysSo(t *testing.T) {
	switchTestEnv(t)
	if _, err := runToken(t, "json"); !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("err = %v, want ErrNotLoggedIn", err)
	}
}
