package commands

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// One name for the host. The root PreRun decides it; everything here reads that
// decision, and the session only wins when nothing explicit did.
func TestServerResolution(t *testing.T) {
	t.Cleanup(func() { auth.SetServer("", "default") })
	session := &auth.Session{APIBaseURL: "https://session.example"}

	auth.SetServer("", "default")
	if got := sessionAPIBase(session); got != "https://session.example" {
		t.Fatalf("no override: got %q", got)
	}

	auth.SetServer("https://flag.example", "flag")
	if got := sessionAPIBase(session); got != "https://flag.example" {
		t.Fatalf("resolved server should win: got %q", got)
	}
	if got := auth.ServerSource(); got != "flag" {
		t.Fatalf("source: got %q", got)
	}

	// Provenance is recorded, not inferred: an env var holding exactly the
	// session's host must still report as env, which is what `orq doctor` shows.
	auth.SetServer(session.APIBaseURL, "env")
	if got := auth.ServerSource(); got != "env" {
		t.Fatalf("source for a value equal to the session host: got %q", got)
	}

	auth.SetServer("", "default")
	t.Setenv("ORQ_SERVER", "")
	t.Setenv("ORQ_API_BASE_URL", "https://legacy.example")
	if got := apiBaseFromEnv(); got != "https://legacy.example" {
		t.Fatalf("deprecated env: got %q", got)
	}
	t.Setenv("ORQ_SERVER", "https://env.example")
	if got := apiBaseFromEnv(); got != "https://env.example" {
		t.Fatalf("ORQ_SERVER should beat the deprecated spelling: got %q", got)
	}
	t.Setenv("ORQ_SERVER", "")
	t.Setenv("ORQ_API_BASE_URL", "")
	if got := apiBaseFromEnv(); got != auth.DefaultAPIBaseURL {
		t.Fatalf("default: got %q", got)
	}
}

func TestPersistServerDefaultAcceptsEmptyConfigFormsAndPreservesNumbers(t *testing.T) {
	for _, tc := range []struct{ name, contents string }{
		{"missing", ""},
		{"empty", ""},
		{"whitespace", " \n\t"},
		{"null", "null"},
		{"object", `{"unrelated":123456789012345678901234567890,"server":"https://legacy.example"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prev := viper.GetString("config-directory")
			prevDefault := viper.GetString("server-default")
			viper.Set("config-directory", dir)
			t.Cleanup(func() { viper.Set("config-directory", prev); viper.Set("server-default", prevDefault) })
			filename := filepath.Join(dir, "config.json")
			if tc.name != "missing" {
				if err := os.WriteFile(filename, []byte(tc.contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := persistServerDefault("https://self.example"); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			var host string
			if err := json.Unmarshal(got["server-default"], &host); err != nil || host != "https://self.example" {
				t.Fatalf("server-default=%q, err=%v", host, err)
			}
			if tc.name == "object" {
				if string(got["unrelated"]) != "123456789012345678901234567890" {
					t.Errorf("number precision lost: %s", got["unrelated"])
				}
				if _, ok := got["server"]; ok {
					t.Errorf("legacy server retained: %s", data)
				}
			}
		})
	}
}

func TestPersistServerDefaultRejectsMalformedAndNonObjectWithoutOverwrite(t *testing.T) {
	for _, contents := range []string{"{", "[]", `"text"`} {
		t.Run(contents, func(t *testing.T) {
			dir := t.TempDir()
			prev := viper.GetString("config-directory")
			prevDefault := viper.GetString("server-default")
			viper.Set("config-directory", dir)
			t.Cleanup(func() { viper.Set("config-directory", prev); viper.Set("server-default", prevDefault) })
			filename := filepath.Join(dir, "config.json")
			if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := persistServerDefault("https://self.example"); err == nil {
				t.Fatal("expected invalid config error")
			}
			got, err := os.ReadFile(filename)
			if err != nil || string(got) != contents {
				t.Fatalf("config overwritten: %q, err=%v", got, err)
			}
		})
	}
}

func TestPersistSetupServerRepairsExistingProfileEvenWhenDefaultMatches(t *testing.T) {
	credsHarness(t)
	prevDefault := viper.GetString("server-default")
	t.Cleanup(func() { viper.Set("server-default", prevDefault) })
	viper.Set("profile", "ci")
	bartolocli.Creds.Set("profiles.ci.api_key", "keep-key")
	bartolocli.Creds.Set("profiles.ci.type", "apikey")
	bartolocli.Creds.Set("profiles.other.server", "https://other.example")
	bartolocli.Creds.Set("profiles.ci.server", "https://old.example")
	if err := saveCreds(); err != nil {
		t.Fatal(err)
	}
	viper.Set("server-default", "https://self.example")
	configPath := filepath.Join(viper.GetString("config-directory"), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"server-default":"https://self.example","keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	auth.SetServer("https://self.example", "flag")
	var out strings.Builder
	if err := persistSetupServer(&reporter{w: &out, quiet: true}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "saved https://self.example") || !strings.Contains(out.String(), "orq server clear") {
		t.Errorf("quiet mode must report the saved server and undo guidance: %q", out.String())
	}
	if !strings.Contains(out.String(), "profile ci") {
		t.Errorf("active profile save was not reported: %q", out.String())
	}
	if got := bartolocli.Creds.GetString("profiles.ci.server"); got != "https://self.example" {
		t.Errorf("profile server=%q", got)
	}
	if got := bartolocli.Creds.GetString("profiles.ci.api_key"); got != "keep-key" {
		t.Errorf("profile key changed: %q", got)
	}
	if got := bartolocli.Creds.GetString("profiles.other.server"); got != "https://other.example" {
		t.Errorf("other profile changed: %q", got)
	}
	data, err := os.ReadFile(filepath.Join(viper.GetString("config-directory"), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]map[string]map[string]string
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["profiles"]["ci"]["server"] != "https://self.example" {
		t.Errorf("credentials.json did not persist profile server: %s", data)
	}
	bartolocli.Creds.Set("profiles.ci.server", "")
	if err := saveCreds(); err != nil {
		t.Fatal(err)
	}
	if err := persistSetupServer(&reporter{w: io.Discard}, true); err != nil {
		t.Fatal(err)
	}
	if got := bartolocli.Creds.GetString("profiles.ci.server"); got != "https://self.example" {
		t.Errorf("missing profile server was not repaired: %q", got)
	}
}

func TestPersistSetupServerWriteFailureDoesNotPrintSuccess(t *testing.T) {
	credsHarness(t)
	prevDefault := viper.GetString("server-default")
	t.Cleanup(func() { viper.Set("server-default", prevDefault) })
	viper.Set("server-default", "")
	configPath := filepath.Join(viper.GetString("config-directory"), "config.json")
	if err := os.Mkdir(configPath, 0o700); err != nil {
		t.Fatal(err)
	}
	auth.SetServer("https://self.example", "flag")
	var out strings.Builder
	err := persistSetupServer(&reporter{w: &out}, true)
	if err == nil {
		t.Fatal("expected config write failure")
	}
	if strings.Contains(out.String(), "saved") {
		t.Errorf("printed success after persistence failure: %q", out.String())
	}
}

func TestPersistSetupServerWritesDiskWhenViperDefaultAlreadyMatches(t *testing.T) {
	credsHarness(t)
	prevDefault := viper.GetString("server-default")
	t.Cleanup(func() { viper.Set("server-default", prevDefault) })
	const host = "https://self.example"
	viper.Set("server-default", host)
	configPath := filepath.Join(viper.GetString("config-directory"), "config.json")
	auth.SetServer(host, "flag")
	if err := persistSetupServer(&reporter{w: io.Discard}, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("matching in-memory default skipped disk write: %v", err)
	}
	var config map[string]string
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config["server-default"] != host {
		t.Errorf("persisted server-default=%q", config["server-default"])
	}
}

// Saving an API-key profile records the resolved server on that Bartolo
// profile, so selecting it routes later commands back to the same host.
func TestSaveAPIKeyProfileRecordsResolvedServer(t *testing.T) {
	dir := t.TempDir()
	prevDir := viper.GetString("config-directory")
	viper.Set("config-directory", dir)
	viper.Set("profile", "acme")
	t.Cleanup(func() { viper.Set("config-directory", prevDir); viper.Set("profile", "") })

	prevCreds := bartolocli.Creds
	bartolocli.Creds = newTestCreds(t)
	t.Cleanup(func() { bartolocli.Creds = prevCreds })

	origServer, origSource := auth.Server(), auth.ServerSource()
	auth.SetServer("https://orq.acme.internal", "flag")
	t.Cleanup(func() { auth.SetServer(origServer, origSource) })
	if err := saveAPIKeyProfile("sk-orq-profile"); err != nil {
		t.Fatal(err)
	}
	if got := ProfileServer(); got != "https://orq.acme.internal" {
		t.Fatalf("ProfileServer: got %q", got)
	}

	info, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Credentials, not config: nobody else on the box reads this.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials.json mode: got %o, want 600", perm)
	}

	// The default host is never pinned — it would outlive a change of default.
	auth.SetServer("", "default")
	if err := saveAPIKeyProfile("sk-orq-replacement"); err != nil {
		t.Fatal(err)
	}
	if got := ProfileServer(); got != "https://orq.acme.internal" {
		t.Errorf("a blank host must not overwrite a bound one: got %q", got)
	}
}

func newTestCreds(t *testing.T) *bartolocli.CredentialsFile {
	t.Helper()
	creds, err := bartolocli.NewCredentialsFile(t.TempDir())
	if err != nil {
		t.Fatalf("NewCredentialsFile: %v", err)
	}
	return creds
}
