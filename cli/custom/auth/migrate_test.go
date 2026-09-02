package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// layoutHarness gives a test its own HOME, a credentials.json under it loaded
// the way the CLI loads it, and any session files it names.
func layoutHarness(t *testing.T, credentials string, sessions map[string]*Session) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".orq")
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, s := range sessions {
		raw, _ := json.Marshal(s)
		if err := os.WriteFile(filepath.Join(dir, "sessions", name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	creds, err := bartolocli.NewCredentialsFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	prev := bartolocli.Creds
	bartolocli.Creds = creds
	t.Cleanup(func() {
		bartolocli.Creds = prev
		viper.Set("profile-selected", "")
		SetServer("", "default")
	})
	return dir
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func sessionOn(apiBase, workspace string) *Session {
	s := validSession(workspace)
	s.APIBaseURL = apiBase
	return s
}

func TestMigrateRenamesSessionFilesToTheirHost(t *testing.T) {
	dir := layoutHarness(t, `{}`, map[string]*Session{
		"default.json":       sessionOn("https://api.orq.ai", "orq-research"),
		"staging-oauth.json": sessionOn("https://my.staging.orq.ai", "bauke-staging"),
	})
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"my.orq.ai.json", "my.staging.orq.ai.json"} {
		if _, err := os.Stat(filepath.Join(dir, "sessions", want)); err != nil {
			t.Errorf("%s missing after migration: %v", want, err)
		}
	}
	for _, gone := range []string{"default.json", "staging-oauth.json"} {
		if _, err := os.Stat(filepath.Join(dir, "sessions", gone)); err == nil {
			t.Errorf("%s still present after migration", gone)
		}
	}
}

// Two logins to one host: the newer file wins, the other is kept, renamed so
// it is recognisable, and nothing is deleted.
func TestMigrateKeepsTheLoserAsDeprecated(t *testing.T) {
	dir := layoutHarness(t, `{}`, map[string]*Session{
		"default.json": sessionOn("https://api.orq.ai", "old"),
		"staging.json": sessionOn("https://api.orq.ai", "new"),
	})
	old := filepath.Join(dir, "sessions", "default.json")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	winner := readDoc(t, filepath.Join(dir, "sessions", "my.orq.ai.json"))
	if winner["activeWorkspaceKey"] != "new" {
		t.Errorf("winner = %v, want the newer file", winner["activeWorkspaceKey"])
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "default.json.deprecated")); err != nil {
		t.Errorf("loser was not kept as .deprecated: %v", err)
	}
}

// The pre-#63 layout: our fields inside bartolo's profile, no api_key. They
// move onto the session of the host the profile named, and the profile goes.
func TestMigrateMovesLegacyProfileFieldsIntoTheSession(t *testing.T) {
	dir := layoutHarness(t, `{"profiles":{"default":{
		"api_key":"",
		"gateway_key":"sk-orq-GW",
		"gateway_key_id":"KEYID",
		"gateway_key_expires_at":"2027-01-01T00:00:00Z",
		"workspace":"acme"
	}}}`, map[string]*Session{
		"default.json": sessionOn("https://api.orq.ai", "acme"),
	})
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	s := readDoc(t, filepath.Join(dir, "sessions", "my.orq.ai.json"))
	for field, want := range map[string]string{
		"gatewayKey": "sk-orq-GW", "gatewayKeyId": "KEYID",
		"gatewayKeyExpiresAt": "2027-01-01T00:00:00Z", "gatewayWorkspace": "acme",
	} {
		if s[field] != want {
			t.Errorf("session[%q] = %v, want %q", field, s[field], want)
		}
	}
	creds := readDoc(t, filepath.Join(dir, "credentials.json"))
	if profiles, _ := creds["profiles"].(map[string]any); len(profiles) != 0 {
		t.Errorf("keyless profile survived: %v", profiles)
	}
}

// The #63 layout (never released): a `state` section keyed by profile name.
func TestMigrateMovesStateSectionIntoTheSession(t *testing.T) {
	dir := layoutHarness(t, `{"state":{"default":{"gateway_key":"sk-orq-GW","workspace":"acme"}}}`,
		map[string]*Session{"default.json": sessionOn("https://api.orq.ai", "acme")})
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	s := readDoc(t, filepath.Join(dir, "sessions", "my.orq.ai.json"))
	if s["gatewayKey"] != "sk-orq-GW" || s["gatewayWorkspace"] != "acme" {
		t.Errorf("state did not reach the session: %v", s)
	}
	creds := readDoc(t, filepath.Join(dir, "credentials.json"))
	if _, still := creds["state"]; still {
		t.Errorf("state section survived: %v", creds)
	}
}

// An API-key profile is bartolo's. Only our fields leave it; the key, type and
// server stay, and a workspace we recorded is dropped rather than moved (an
// API key's workspace is not something a session owns).
func TestMigrateLeavesAnAPIKeyProfileAuthenticating(t *testing.T) {
	dir := layoutHarness(t, `{"profiles":{"acme":{
		"api_key":"sk-orq-REAL","type":"apikey","server":"https://acme.example","workspace":"acme"
	}}}`, nil)
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	creds := readDoc(t, filepath.Join(dir, "credentials.json"))
	profiles, _ := creds["profiles"].(map[string]any)
	profile, _ := profiles["acme"].(map[string]any)
	if profile == nil || profile["api_key"] != "sk-orq-REAL" || profile["server"] != "https://acme.example" {
		t.Fatalf("profile = %v, want key and server intact", profiles)
	}
	if _, still := profile["workspace"]; still {
		t.Errorf("our field stayed on bartolo's profile: %v", profile)
	}
}

// A keyless profile carrying none of our fields was written by something else.
func TestMigrateLeavesAForeignKeylessProfileAlone(t *testing.T) {
	dir := layoutHarness(t, `{"profiles":{"staged":{"api_key":"","server":"https://staged.example"}}}`, nil)
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	creds := readDoc(t, filepath.Join(dir, "credentials.json"))
	profiles, _ := creds["profiles"].(map[string]any)
	if _, kept := profiles["staged"]; !kept {
		t.Errorf("a profile this CLI never wrote was deleted: %v", creds)
	}
}

func TestMigrateClearsASelectionItInvalidates(t *testing.T) {
	dir := layoutHarness(t, `{"profiles":{"default":{"api_key":"","workspace":"acme"}}}`,
		map[string]*Session{"default.json": sessionOn("https://api.orq.ai", "acme")})
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"profile-decided":true,"profile-selected":"default","server-default":"https://keep.me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set("profile-selected", "default")
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	if viper.GetString("profile-selected") != "" {
		t.Error("in-process selection not cleared")
	}
	doc := readDoc(t, config)
	if _, still := doc["profile-selected"]; still {
		t.Errorf("persisted selection survived: %v", doc)
	}
	if doc["profile-decided"] != true || doc["server-default"] != "https://keep.me" {
		t.Errorf("rest of config.json damaged: %v", doc)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	dir := layoutHarness(t, `{"profiles":{"default":{"api_key":"","gateway_key":"sk-orq-GW"}}}`,
		map[string]*Session{"default.json": sessionOn("https://api.orq.ai", "acme")})
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions", "my.orq.ai.json")
	first, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	again, _ := os.Stat(path)
	if string(first) != string(second) || !info.ModTime().Equal(again.ModTime()) {
		t.Error("second run rewrote the session file")
	}
}
