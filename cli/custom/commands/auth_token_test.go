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
	"github.com/spf13/viper"
)

// tokenTestEnv isolates HOME and the format, and seeds a login on apiBase
// whose active pair is acme / project p1 with the given cached tokens.
func tokenTestEnv(t *testing.T, apiBase string, tokens map[string]auth.StoredAccessToken) {
	t.Helper()
	switchTestEnv(t)
	prevFormat, prevExplicit := viper.GetString("output-format"), explicitAPIKey
	t.Cleanup(func() { viper.Set("output-format", prevFormat); explicitAPIKey = prevExplicit })
	explicitAPIKey = false
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

func runToken(t *testing.T, format string) (string, error) {
	t.Helper()
	// ORQ_OUTPUT_FORMAT stands in for -o: the command's own flag set has no
	// root to inherit -o from here.
	t.Setenv("ORQ_OUTPUT_FORMAT", format)
	viper.Set("output-format", format)
	var out bytes.Buffer
	previous := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = previous })
	cmd := NewTokenCommand()
	cmd.SetArgs(nil)
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

// The PreRun already exchanged a token for the pair this run resolved
// (--workspace/--project included) into ORQ_API_KEY; auth token must report
// that one, not re-derive the active pair and disagree with it.
func TestAuthTokenReportsThePreRunToken(t *testing.T) {
	exp := time.Now().Add(time.Hour).Format(time.RFC3339)
	tokenTestEnv(t, "http://127.0.0.1:1", map[string]auth.StoredAccessToken{
		"acme#p1":  {Token: "tok-active", ExpiresAt: exp},
		"other#p9": {Token: "tok-override", ExpiresAt: exp, WorkspaceID: "ws-9"},
	})
	t.Setenv("ORQ_API_KEY", "tok-override")

	out, err := runToken(t, "json")
	if err != nil {
		t.Fatal(err)
	}
	r := decodeTokenReport(t, out)
	want := tokenReport{Token: "tok-override", ExpiresAt: exp, Workspace: "other", WorkspaceID: "ws-9", ProjectID: "p9", Server: "http://127.0.0.1:1"}
	if r != want {
		t.Errorf("report = %+v, want %+v", r, want)
	}
}

// Without -o json the output is the token alone, so $(orq auth token) works.
func TestAuthTokenPrintsTheBareToken(t *testing.T) {
	exp := time.Now().Add(time.Hour).Format(time.RFC3339)
	tokenTestEnv(t, "http://127.0.0.1:1", map[string]auth.StoredAccessToken{"acme#p1": {Token: "tok-active", ExpiresAt: exp}})
	t.Setenv("ORQ_API_KEY", "tok-active")

	out, err := runToken(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "tok-active\n" {
		t.Errorf("output = %q, want the bare token and a newline", out)
	}
}

// With an API key in force the PreRun mints nothing, so the active pair's
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

// A token that is expired, or inherited stale in ORQ_API_KEY after the PreRun's
// own exchange failed, is replaced by a fresh exchange, and the new token is
// cached for the next command.
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
	t.Setenv("ORQ_API_KEY", "tok-stale")

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

func TestAuthTokenWithoutALoginSaysSo(t *testing.T) {
	switchTestEnv(t)
	if _, err := runToken(t, "json"); !errors.Is(err, auth.ErrNotLoggedIn) {
		t.Fatalf("err = %v, want ErrNotLoggedIn", err)
	}
}
