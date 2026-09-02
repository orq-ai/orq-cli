package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
