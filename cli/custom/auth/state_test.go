package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// stateHarness writes a credentials file, loads it the way the CLI does, and
// points the process at that directory for the duration of one test.
func stateHarness(t *testing.T, credentials string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	creds, err := bartolocli.NewCredentialsFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	prev := bartolocli.Creds
	bartolocli.Creds = creds
	t.Cleanup(func() {
		bartolocli.Creds = prev
		viper.Set("profile", "")
		viper.Set("profile-selected", "")
	})
	return dir
}

func readJSON(t *testing.T, path string) map[string]any {
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

// The migration exists because bartolo >=0.8 fails every request when the
// profile in force holds no api_key. A session login writes exactly that shape,
// so the profile has to go and its contents have to survive the move.
func TestMigrationMovesSessionStateAndDropsTheKeylessProfile(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"default":{
		"api_key":"",
		"gateway_key":"sk-orq-GW",
		"gateway_key_id":"KEYID",
		"gateway_key_expires_at":"2027-01-01T00:00:00Z",
		"workspace":"acme",
		"server":"https://self.hosted",
		"type":""
	}}}`)

	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}

	doc := readJSON(t, filepath.Join(dir, "credentials.json"))
	profiles, _ := doc["profiles"].(map[string]any)
	if _, still := profiles["default"]; still {
		t.Errorf("the keyless profile survived: %v", profiles)
	}
	state, _ := doc["state"].(map[string]any)
	entry, _ := state["default"].(map[string]any)
	for field, want := range map[string]string{
		"gateway_key":            "sk-orq-GW",
		"gateway_key_id":         "KEYID",
		"gateway_key_expires_at": "2027-01-01T00:00:00Z",
		"workspace":              "acme",
		// The server binding travels with the profile it belonged to, or
		// `orq --profile default` stops finding its host.
		"server": "https://self.hosted",
	} {
		if got, _ := entry[field].(string); got != want {
			t.Errorf("state[%q] = %q, want %q", field, got, want)
		}
	}

	viper.Set("profile", "default")
	if got := StateValue("gateway_key"); got != "sk-orq-GW" {
		t.Errorf("StateValue after reload = %q, want the migrated key", got)
	}
	if key, ws := SavedAgentKey(); key != "sk-orq-GW" || ws != "acme" {
		t.Errorf("SavedAgentKey = (%q, %q), want the migrated key and workspace", key, ws)
	}
}

// A profile that authenticates is bartolo's: it keeps its key, its type and
// its server, and only the fields this CLI added move out.
func TestMigrationLeavesAnAPIKeyProfileAuthenticating(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"acme":{
		"api_key":"sk-orq-REAL",
		"type":"apikey",
		"server":"https://acme.example",
		"workspace":"acme"
	}}}`)

	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}

	doc := readJSON(t, filepath.Join(dir, "credentials.json"))
	profiles, _ := doc["profiles"].(map[string]any)
	profile, _ := profiles["acme"].(map[string]any)
	if profile == nil {
		t.Fatalf("the profile was removed: %v", profiles)
	}
	if profile["api_key"] != "sk-orq-REAL" || profile["server"] != "https://acme.example" {
		t.Errorf("profile = %v, want the key and server left in place", profile)
	}
	if _, moved := profile["workspace"]; moved {
		t.Errorf("workspace stayed in the profile: %v", profile)
	}
	if got := StateValueOf("acme", "workspace"); got != "acme" {
		t.Errorf("state workspace = %q, want it moved", got)
	}
}

// Running on every command must be free once there is nothing to move, and
// must not rewrite the file a second time.
func TestMigrationIsIdempotent(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"default":{"api_key":"","gateway_key":"sk-orq-GW"}}}`)
	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials.json")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if needsMigration() {
		t.Error("a migrated file still reports work to do")
	}
	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("second run rewrote the file:\n%s\n%s", first, second)
	}
}

// A selection naming a profile this migration removed is the same dead end
// under a different message: bartolo resolves it as in force and every request
// fails with "profile is not configured".
func TestMigrationClearsASelectionItInvalidates(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"default":{"api_key":"","workspace":"acme"}}}`)
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"profile-decided":true,"profile-selected":"default","server-default":"https://keep.me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set("profile-selected", "default")

	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}

	if got := viper.GetString("profile-selected"); got != "" {
		t.Errorf("in-process selection = %q, want it cleared", got)
	}
	doc := readJSON(t, config)
	if _, still := doc["profile-selected"]; still {
		t.Errorf("persisted selection survived: %v", doc)
	}
	if doc["server-default"] != "https://keep.me" {
		t.Errorf("the rest of config.json was lost: %v", doc)
	}
}
