package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// A profile is an API key. Logging into a browser under one would create a
// second thing with the same name, which is the confusion this release ends.
func TestBrowserLoginRefusesAProfile(t *testing.T) {
	prev := bartolocli.Creds
	creds, err := bartolocli.NewCredentialsFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bartolocli.Creds = creds
	viper.Set("profile", "work")
	t.Cleanup(func() { bartolocli.Creds = prev; viper.Set("profile", "") })

	cmd := NewLoginCommand()
	cmd.SetArgs([]string{"--no-open"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `profile "work" is an API key`) {
		t.Fatalf("err = %v, want the profile refusal", err)
	}
}

func TestAPIKeyLoginWritesSelectedProfile(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	viper.Set("profile", "work")
	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("profile", ""); viper.Set("output-format", "") })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	origServer, origSource := auth.Server(), auth.ServerSource()
	auth.SetServer(srv.URL, "flag")
	t.Cleanup(func() { auth.SetServer(origServer, origSource) })
	var out bytes.Buffer
	previous := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = previous })

	cmd := NewLoginCommand()
	cmd.SetArgs([]string{"--api-key", "sk-orq-profile"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("API-key login with profile: %v", err)
	}
	if got := bartolocli.Creds.GetString("profiles.work.api_key"); got != "sk-orq-profile" {
		t.Errorf("profiles.work.api_key = %q, want the supplied key", got)
	}
	login, err := auth.ReadAPIKeyLogin()
	if err != nil {
		t.Fatalf("ReadAPIKeyLogin: %v", err)
	}
	if login != nil {
		t.Errorf("selected profile also created host-keyed login: %+v", login)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("login output is not JSON: %v\n%s", err, out.String())
	}
	if payload["profile"] != "work" {
		t.Errorf("login profile = %v, want work", payload["profile"])
	}
}

func TestAPIKeyLoginWithoutProfileKeepsJSONProfileField(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	viper.Set("profile", "")
	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("output-format", "") })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	origServer, origSource := auth.Server(), auth.ServerSource()
	auth.SetServer(srv.URL, "flag")
	t.Cleanup(func() { auth.SetServer(origServer, origSource) })
	var out bytes.Buffer
	previous := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = previous })

	cmd := NewLoginCommand()
	cmd.SetArgs([]string{"--api-key", "sk-orq-login"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("API-key login without profile: %v", err)
	}
	login, err := auth.ReadAPIKeyLogin()
	if err != nil || login == nil || login.APIKey != "sk-orq-login" {
		t.Fatalf("host-keyed login = %+v, err = %v", login, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("login output is not JSON: %v\n%s", err, out.String())
	}
	if profile, exists := payload["profile"]; !exists || profile != "" {
		t.Errorf("login profile field = %v (present: %t), want empty string", profile, exists)
	}
}

func statusKeyProbe(t *testing.T, status int, keepSession bool) {
	t.Helper()
	var session *auth.Session
	if keepSession {
		var err error
		session, err = auth.ReadSession()
		if err != nil || session == nil {
			t.Fatalf("ReadSession: %v, session=%v", err, session)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/projects" {
			t.Errorf("unexpected status probe path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			fmt.Fprint(w, `{"data":[],"has_more":false}`)
		} else {
			fmt.Fprint(w, `{"message":"key revoked"}`)
		}
	}))
	t.Cleanup(srv.Close)
	previousServer, previousSource := auth.Server(), auth.ServerSource()
	auth.SetServer(srv.URL, "flag")
	t.Cleanup(func() { auth.SetServer(previousServer, previousSource) })
	if session != nil {
		urls := auth.ResolveURLs(srv.URL)
		session.APIBaseURL, session.V1BaseURL = urls.APIBaseURL, urls.V1BaseURL
		session.AuthBaseURL, session.ProfileBaseURL = urls.AuthBaseURL, urls.ProfileBaseURL
		if err := auth.SaveSession(session); err != nil {
			t.Fatal(err)
		}
	}
}

// After an api-key login with no browser session, `orq status` must report the
// login, not claim the user is logged out.
func TestWhoAmIReportsAPIKeyLogin(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	for _, name := range APIKeyEnvVars {
		t.Setenv(name, "")
	}
	viper.Set("profile", "")
	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("output-format", "") })

	// credsHarness writes a browser session; remove it so this is the api-key-only path.
	if err := auth.ClearSession(); err != nil {
		t.Fatal(err)
	}
	statusKeyProbe(t, http.StatusOK, false)
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{
		APIBaseURL: auth.ResolveURLs("").APIBaseURL,
		APIKey:     "sk-orq-LOGIN",
		Workspaces: []map[string]any{{"key": "acme"}},
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	origStdout := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = origStdout })

	cmd := NewWhoAmICommand()
	if err := cmd.Execute(); err != nil {
		t.Fatalf("whoami with an api-key login: %v", err)
	}
	var payload struct {
		Method          string `json:"method"`
		Workspace       string `json:"workspace"`
		APIKey          string `json:"api_key"`
		APIKeyLoginFile string `json:"api_key_login_file"`
		SessionFile     string `json:"session_file"`
		Authenticated   bool   `json:"authenticated"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("whoami output is not JSON: %v\n%s", err, out.String())
	}
	if payload.Method != "api_key" || payload.Workspace != "acme" || payload.APIKey != maskToken("sk-orq-LOGIN") || !payload.Authenticated {
		t.Errorf("whoami payload = %+v", payload)
	}
	if payload.APIKeyLoginFile != auth.APIKeyLoginFilePath() || payload.SessionFile != auth.SessionFilePath() {
		t.Errorf("whoami credential file = %q, session file = %q", payload.APIKeyLoginFile, payload.SessionFile)
	}
}

func TestWhoAmIReportsRejectedStoredAPIKeyLogin(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	viper.Set("profile", "")
	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("output-format", "") })
	if err := auth.ClearSession(); err != nil {
		t.Fatal(err)
	}
	statusKeyProbe(t, http.StatusUnauthorized, false)
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{APIKey: "sk-orq-REVOKED"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	previous := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = previous })
	if err := NewWhoAmICommand().Execute(); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Authenticated bool   `json:"authenticated"`
		AuthError     string `json:"auth_error"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("whoami output is not JSON: %v\n%s", err, out.String())
	}
	if payload.Authenticated || !strings.Contains(payload.AuthError, "key revoked") {
		t.Errorf("payload = %+v, want the rejected key reported", payload)
	}
}

func TestWhoAmIReportsActiveAPIKeyLoginOverBrowserSession(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	viper.Set("profile", "")
	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("output-format", "") })
	statusKeyProbe(t, http.StatusOK, true)
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{APIKey: "sk-orq-LOGIN"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORQ_API_KEY", "sk-orq-LOGIN")

	var out bytes.Buffer
	previous := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = previous })
	if err := NewWhoAmICommand().Execute(); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Method          string `json:"method"`
		APIKey          string `json:"api_key"`
		APIKeyLoginFile string `json:"api_key_login_file"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("whoami output is not JSON: %v\n%s", err, out.String())
	}
	if payload.Method != "api_key" || payload.APIKey != maskToken("sk-orq-LOGIN") || payload.APIKeyLoginFile != auth.APIKeyLoginFilePath() {
		t.Errorf("whoami did not report the active API-key login: %+v", payload)
	}
}

func TestWhoAmIReportsEnvironmentKeyOverStoredLogin(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	for _, name := range APIKeyEnvVars {
		t.Setenv(name, "")
	}
	viper.Set("profile", "")
	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("output-format", ""); ResetUserEnvAPIKey() })
	if err := auth.ClearSession(); err != nil {
		t.Fatal(err)
	}
	statusKeyProbe(t, http.StatusOK, false)
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{APIKey: "sk-orq-STORED"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORQ_API_KEY", "sk-orq-EXPORTED")
	SetUserEnvAPIKey("sk-orq-EXPORTED")

	var out bytes.Buffer
	previous := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = previous })
	if err := NewWhoAmICommand().Execute(); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Source          string `json:"source"`
		APIKey          string `json:"api_key"`
		APIKeyLoginFile string `json:"api_key_login_file"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("whoami output is not JSON: %v\n%s", err, out.String())
	}
	if payload.Source != "ORQ_API_KEY" || payload.APIKey != maskToken("sk-orq-EXPORTED") || payload.APIKeyLoginFile != "" {
		t.Errorf("whoami reported the stored login instead of the effective environment key: %+v", payload)
	}
}

func TestWhoAmIReportsEnvironmentKeyOverStoredLoginAndSession(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	viper.Set("profile", "")
	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("output-format", ""); ResetUserEnvAPIKey() })
	statusKeyProbe(t, http.StatusOK, true)
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{APIKey: "sk-orq-STORED"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORQ_API_KEY", "sk-orq-EXPORTED")
	SetUserEnvAPIKey("sk-orq-EXPORTED")
	previousExplicit := explicitAPIKey
	SetExplicitAPIKey(true)
	t.Cleanup(func() { SetExplicitAPIKey(previousExplicit) })
	var out bytes.Buffer
	previous := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = previous })
	if err := NewWhoAmICommand().Execute(); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Source          string `json:"source"`
		APIKey          string `json:"api_key"`
		APIKeyLoginFile string `json:"api_key_login_file"`
		Authenticated   bool   `json:"authenticated"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("whoami output is not JSON: %v\n%s", err, out.String())
	}
	if payload.Source != "ORQ_API_KEY" || payload.APIKey != maskToken("sk-orq-EXPORTED") || payload.APIKeyLoginFile != "" || !payload.Authenticated {
		t.Errorf("whoami did not report the effective environment key: %+v", payload)
	}
}

// Logout clears an api-key login even with no browser session, so the next
// command is not silently re-authenticated after a "signed out".
func TestLogoutClearsAPIKeyLogin(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	for _, name := range APIKeyEnvVars {
		t.Setenv(name, "")
	}
	viper.Set("profile", "")
	if err := auth.ClearSession(); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveAPIKeyLogin(&auth.APIKeyLogin{APIKey: "sk-orq-LOGIN"}); err != nil {
		t.Fatal(err)
	}

	cmd := NewLogoutCommand()
	cmd.SetArgs([]string{"--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("logout with an api-key login: %v", err)
	}
	login, err := auth.ReadAPIKeyLogin()
	if err != nil {
		t.Fatalf("ReadAPIKeyLogin after logout: %v", err)
	}
	if login != nil {
		t.Error("api-key login survived logout")
	}
}

func TestLogoutRefusesAProfileBeforeReadingTheSession(t *testing.T) {
	credsHarness(t)
	viper.Set("profile", "work")
	t.Cleanup(func() { viper.Set("profile", "") })
	if err := os.WriteFile(auth.SessionFilePath(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewLogoutCommand()
	cmd.SetArgs([]string{"--yes"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `profile "work" is an API key`) {
		t.Fatalf("err = %v, want the profile refusal before session parsing", err)
	}
	if strings.Contains(err.Error(), "session_invalid") {
		t.Fatalf("logout read the corrupt session before refusing the profile: %v", err)
	}
}

func TestLogoutReportsGatewayKeyAfterClearingItsSession(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	for _, name := range APIKeyEnvVars {
		t.Setenv(name, "")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v2/auth/refresh-token" {
			t.Errorf("logout request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	session, err := auth.ReadSession()
	if err != nil || session == nil {
		t.Fatalf("session = %+v, err = %v", session, err)
	}
	urls := auth.ResolveURLs(srv.URL)
	session.APIBaseURL = urls.APIBaseURL
	session.V1BaseURL = urls.V1BaseURL
	session.AuthBaseURL = urls.AuthBaseURL
	session.ProfileBaseURL = urls.ProfileBaseURL
	session.GatewayKeyID = "gateway-key-id"
	if err := auth.SaveSession(session); err != nil {
		t.Fatal(err)
	}

	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("output-format", "") })
	var out bytes.Buffer
	previous := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = previous })

	cmd := NewLogoutCommand()
	cmd.SetArgs([]string{"--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("logout output is not JSON: %v\n%s", err, out.String())
	}
	if payload["gateway_key_id"] != "gateway-key-id" {
		t.Errorf("gateway_key_id = %v, want the surviving key handle", payload["gateway_key_id"])
	}
	if remaining, err := auth.ReadSession(); err != nil || remaining != nil {
		t.Errorf("session after logout = %+v, err = %v", remaining, err)
	}
}

func TestLogoutCleanupErrorReportsSurvivingGatewayKey(t *testing.T) {
	credsHarness(t)
	for _, name := range APIKeyEnvVars {
		t.Setenv(name, "")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	session, err := auth.ReadSession()
	if err != nil || session == nil {
		t.Fatalf("session = %+v, err = %v", session, err)
	}
	urls := auth.ResolveURLs(srv.URL)
	session.APIBaseURL, session.V1BaseURL = urls.APIBaseURL, urls.V1BaseURL
	session.AuthBaseURL, session.ProfileBaseURL = urls.AuthBaseURL, urls.ProfileBaseURL
	session.GatewayKeyID = "gateway-key-survives"
	if err := auth.SaveSession(session); err != nil {
		t.Fatal(err)
	}
	dir := viper.GetString("config-directory")
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte("export ORQ_API_KEY=sk-orq-LIVE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := writeSecretFile
	want := errors.New("disk full")
	writeSecretFile = func(string, []byte) error { return want }
	t.Cleanup(func() { writeSecretFile = previous })

	cmd := NewLogoutCommand()
	cmd.SetArgs([]string{"--yes"})
	err = cmd.Execute()
	if !errors.Is(err, want) {
		t.Fatalf("logout error = %v, want wrapped disk error", err)
	}
	if !strings.Contains(err.Error(), "gateway-key-survives") {
		t.Errorf("logout error lost the surviving gateway key ID: %v", err)
	}
	if remaining, readErr := auth.ReadSession(); readErr != nil || remaining != nil {
		t.Errorf("session after logout = %+v, err = %v", remaining, readErr)
	}
}

func TestWhoAmIStructuredOutputForAPIKeyProfile(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-orq-profile-secret" {
			t.Errorf("profile probe used %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"data":[],"has_more":false}`)
	}))
	t.Cleanup(srv.Close)
	oldServer, oldSource := auth.Server(), auth.ServerSource()
	auth.SetServer(srv.URL, "flag")
	t.Cleanup(func() { auth.SetServer(oldServer, oldSource) })
	viper.Set("profile", "work")
	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("profile", ""); viper.Set("output-format", "") })
	if err := saveAPIKeyProfile("sk-orq-profile-secret"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	origStdout := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = origStdout })

	cmd := NewWhoAmICommand()
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Profile       string `json:"profile"`
		Server        string `json:"server"`
		APIKey        string `json:"api_key"`
		SessionFile   string `json:"session_file"`
		Identity      any    `json:"identity"`
		Authenticated bool   `json:"authenticated"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("whoami output is not JSON: %v\n%s", err, out.String())
	}
	if payload.Profile != "work" || payload.Server != srv.URL || payload.APIKey != maskToken("sk-orq-profile-secret") || payload.Identity != nil || !payload.Authenticated {
		t.Errorf("whoami payload = %+v", payload)
	}
	// orqi reads session_file off this payload (RES-1500), and a profile in
	// force does not make the session file stop existing.
	if payload.SessionFile == "" {
		t.Errorf("whoami payload has no session_file: %+v", payload)
	}
}

// A profile that loads fine says nothing about the workspace token, so a
// server that rejects that token has to turn `authenticated` false (RES-1636).
func TestWhoAmIReportsRejectedWorkspaceToken(t *testing.T) {
	switchTestEnv(t)
	ensureFormatter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "ProfileService") {
			fmt.Fprint(w, `{"profile":{"id":"u1","email":"a@b.c","workspaces":[{"key":"ws"}]}}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"token revoked"}`)
	}))
	t.Cleanup(srv.Close)
	switchSession(t, srv.URL, "ws", []string{"ws"}, "", "")
	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("output-format", "") })

	var out bytes.Buffer
	origStdout := bartolocli.Stdout
	bartolocli.Stdout = &out
	t.Cleanup(func() { bartolocli.Stdout = origStdout })

	if err := NewWhoAmICommand().Execute(); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Authenticated bool   `json:"authenticated"`
		AuthError     string `json:"auth_error"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("whoami output is not JSON: %v\n%s", err, out.String())
	}
	if payload.Authenticated || !strings.Contains(payload.AuthError, "token revoked") {
		t.Errorf("payload = %+v, want authenticated false with the server's message", payload)
	}
}

func TestWhoAmIProbesCredentialInForce(t *testing.T) {
	for _, tc := range []struct {
		name, exportedKey, injectedKey string
		status                         int
		wantBearer                     string
		wantAuthenticated              bool
		wantCheckError                 bool
	}{
		{name: "session succeeds", status: http.StatusOK, wantBearer: "tok-ws", wantAuthenticated: true},
		{name: "server unavailable", status: http.StatusServiceUnavailable, wantBearer: "tok-ws", wantAuthenticated: true, wantCheckError: true},
		{name: "probe route forbidden", status: http.StatusForbidden, wantBearer: "tok-ws", wantAuthenticated: true, wantCheckError: true},
		{name: "pre-run scoped token", injectedKey: "tok-ws-scoped", status: http.StatusOK, wantBearer: "tok-ws-scoped", wantAuthenticated: true},
		{name: "explicit key wins", exportedKey: "user-key", status: http.StatusOK, wantBearer: "user-key", wantAuthenticated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			switchTestEnv(t)
			ensureFormatter(t)
			previousExplicit := explicitAPIKey
			SetExplicitAPIKey(tc.exportedKey != "")
			SetUserEnvAPIKey(tc.exportedKey)
			t.Cleanup(func() { explicitAPIKey = previousExplicit; ResetUserEnvAPIKey() })
			envKey := tc.exportedKey
			if tc.injectedKey != "" {
				envKey = tc.injectedKey
			}
			t.Setenv("ORQ_API_KEY", envKey)
			var gotBearer string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "ProfileService") {
					fmt.Fprint(w, `{"profile":{"id":"u1","email":"a@b.c","workspaces":[{"key":"ws"}]}}`)
					return
				}
				gotBearer = r.Header.Get("Authorization")
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					fmt.Fprint(w, `{"data":[],"has_more":false}`)
				} else {
					fmt.Fprint(w, `{"message":"temporarily unavailable"}`)
				}
			}))
			t.Cleanup(srv.Close)
			previousServer, previousSource := auth.Server(), auth.ServerSource()
			auth.SetServer(srv.URL, "flag")
			t.Cleanup(func() { auth.SetServer(previousServer, previousSource) })
			switchSession(t, srv.URL, "ws", []string{"ws"}, "", "")
			viper.Set("output-format", "json")
			t.Cleanup(func() { viper.Set("output-format", "") })
			var out bytes.Buffer
			previousStdout := bartolocli.Stdout
			bartolocli.Stdout = &out
			t.Cleanup(func() { bartolocli.Stdout = previousStdout })
			if err := NewWhoAmICommand().Execute(); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Authenticated  bool   `json:"authenticated"`
				AuthError      string `json:"auth_error"`
				AuthCheckError string `json:"auth_check_error"`
			}
			if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
				t.Fatalf("whoami output is not JSON: %v\n%s", err, out.String())
			}
			if gotBearer != "Bearer "+tc.wantBearer || payload.Authenticated != tc.wantAuthenticated ||
				(payload.AuthCheckError != "") != tc.wantCheckError || payload.AuthError != "" {
				t.Errorf("bearer=%q payload=%+v", gotBearer, payload)
			}
		})
	}
}
