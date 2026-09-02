package commands

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestAPIKeyLoginIsAllowedWithAProfileInForce(t *testing.T) {
	credsHarness(t)
	ensureFormatter(t)
	viper.Set("profile", "work")
	t.Cleanup(func() { viper.Set("profile", "") })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	origServer, origSource := auth.Server(), auth.ServerSource()
	auth.SetServer(srv.URL, "flag")
	t.Cleanup(func() { auth.SetServer(origServer, origSource) })

	cmd := NewLoginCommand()
	cmd.SetArgs([]string{"--api-key", "sk-orq-profile"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("API-key login with profile: %v", err)
	}
	if got := bartolocli.Creds.GetString("profiles.work.api_key"); got != "sk-orq-profile" {
		t.Errorf("profiles.work.api_key = %q, want the supplied key", got)
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
		Profile  string `json:"profile"`
		Server   string `json:"server"`
		APIKey   string `json:"api_key"`
		Identity any    `json:"identity"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("whoami output is not JSON: %v\n%s", err, out.String())
	}
	if payload.Profile != "work" || payload.Server == "" || payload.APIKey != maskToken("sk-orq-profile-secret") || payload.Identity != nil {
		t.Errorf("whoami payload = %+v", payload)
	}
}
