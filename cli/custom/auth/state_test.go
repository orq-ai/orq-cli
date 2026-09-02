package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestMigrationCollectsPostSplitHusk(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"default":{"api_key":""}},"state":{"default":{"gateway_key":"gk"}}}`)

	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}

	doc := readJSON(t, filepath.Join(dir, "credentials.json"))
	profiles, _ := doc["profiles"].(map[string]any)
	if _, still := profiles["default"]; still {
		t.Errorf("the post-split keyless husk survived: %v", profiles)
	}
	state, _ := doc["state"].(map[string]any)
	entry, _ := state["default"].(map[string]any)
	if entry["gateway_key"] != "gk" {
		t.Errorf("state[default].gateway_key = %v, want gk", entry["gateway_key"])
	}
}

func TestMigrationCorrelatesProfileAndStateCaseInsensitively(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"Default":{"api_key":""}},"state":{"default":{"gateway_key":"gk"}}}`)

	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}
	doc := readJSON(t, filepath.Join(dir, "credentials.json"))
	profiles, _ := doc["profiles"].(map[string]any)
	if _, still := profiles["Default"]; still {
		t.Errorf("mixed-case keyless profile survived: %v", profiles)
	}
	state, _ := doc["state"].(map[string]any)
	entry, _ := state["default"].(map[string]any)
	if entry["gateway_key"] != "gk" {
		t.Errorf("state was not preserved: %v", state)
	}
}

// A machine that ran `orq auth logout` on a version older than the state split
// carries the husk that version left: an empty api_key beside a type and a
// blanked workspace, with nothing under `state` to prove it is ours. bartolo
// adopts it and fails every request, so the migration has to reach it too.
func TestMigrationCollectsAPreSplitLogoutHusk(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"default":{
		"api_key":"",
		"type":"apikey",
		"workspace":"",
		"server":"https://my.orq.ai"
	}}}`)

	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}

	doc := readJSON(t, filepath.Join(dir, "credentials.json"))
	profiles, _ := doc["profiles"].(map[string]any)
	if _, still := profiles["default"]; still {
		t.Errorf("the pre-split logout husk survived: %v", profiles)
	}
	state, _ := doc["state"].(map[string]any)
	entry, _ := state["default"].(map[string]any)
	if entry["server"] != "https://my.orq.ai" {
		t.Errorf("state[default].server = %v, want the host to travel with the removed profile", entry["server"])
	}
}

// A profile that authenticates is bartolo's: it keeps its key, its type and
// its server, and only the fields this CLI added move out.
func TestMigrationLeavesAnAPIKeyProfileAuthenticating(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"acme":{
		"api_key":"sk-orq-REAL",
		"type":"apikey",
		"server":"https://acme.example",
		"workspace":"acme",
		"gateway_key":"sk-orq-GW"
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
	viper.Set("profile", "acme")
	if key, ws := SavedAgentKey(); key != "sk-orq-GW" || ws != "acme" {
		t.Errorf("SavedAgentKey = (%q, %q), want gateway key and workspace", key, ws)
	}
}

// Running on every command must be free once there is nothing to move, and
// must not rewrite the file a second time.
func TestMigrationIsIdempotent(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"default":{"api_key":""}},"state":{"default":{"gateway_key":"sk-orq-GW"}}}`)
	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials.json")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Stat(path)
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
	secondInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(firstInfo, secondInfo) {
		t.Error("second migration replaced credentials.json despite having nothing to change")
	}
}

// A keyless profile with only generic workspace and server fields was written
// by something else — another tool, or a user halfway through configuring one.
// bartolo rejects it if it is ever selected; deleting it is not this
// migration's call.
func TestMigrationLeavesAForeignKeylessProfileAlone(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{
		"staged":{"api_key":"","workspace":"staged","server":"https://staged.example"},
		"default":{"api_key":"","gateway_key":"sk-orq-GW"}
	}}`)

	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}

	doc := readJSON(t, filepath.Join(dir, "credentials.json"))
	profiles, _ := doc["profiles"].(map[string]any)
	staged, _ := profiles["staged"].(map[string]any)
	if staged == nil || !reflect.DeepEqual(staged, map[string]any{
		"api_key":   "",
		"workspace": "staged",
		"server":    "https://staged.example",
	}) {
		t.Errorf("a profile this CLI never wrote was touched: %v", profiles)
	}
	if _, still := profiles["default"]; still {
		t.Errorf("our own keyless profile survived: %v", profiles)
	}
}

func TestMigrationMovesNonStringStateField(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"default":{"api_key":"","gateway_key":"gk","workspace":3}}}`)

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
	if entry["workspace"] != float64(3) {
		t.Errorf("state[default].workspace = %v, want numeric 3", entry["workspace"])
	}
}

// A selection naming a profile this migration removed is the same dead end
// under a different message: bartolo v0.9.0 resolves it as in force and every
// request fails with `profile "default" is not configured`.
func TestMigrationClearsASelectionItInvalidates(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"default":{"api_key":"","gateway_key":"gk","workspace":"acme"}}}`)
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
	// bartolo re-adopts a `default` profile unless profile-decided says the
	// choice was already made, so clearing the selection must not clear that.
	if doc["profile-decided"] != true {
		t.Errorf("profile-decided was lost, bartolo will re-adopt: %v", doc)
	}
	if doc["server-default"] != "https://keep.me" {
		t.Errorf("the rest of config.json was lost: %v", doc)
	}
}

func TestRepairProfileSelectionRepairsDanglingSelectionAndPreservesConfig(t *testing.T) {
	dir := stateHarness(t, `{"profiles":{"acme":{"api_key":"sk-orq-REAL"}}}`)
	config := filepath.Join(dir, "config.json")
	original := `{"profile-decided":true,"profile-selected":"gone","server-default":"https://keep.me","other":{"enabled":true}}`
	if err := os.WriteFile(config, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set("profile-selected", "gone")

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
	if doc["profile-decided"] != true || doc["server-default"] != "https://keep.me" {
		t.Errorf("config.json keys were lost: %v", doc)
	}
	other, _ := doc["other"].(map[string]any)
	if other["enabled"] != true {
		t.Errorf("config.json nested key was lost: %v", doc)
	}

	valid := `{"profile-decided":true,"profile-selected":"ACME","server-default":"https://keep.me","other":{"enabled":true}}`
	if err := os.WriteFile(config, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set("profile-selected", "ACME")
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateProfileState(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || viper.GetString("profile-selected") != "ACME" {
		t.Errorf("valid selection was changed: before %q, after %q, selected %q", before, after, viper.GetString("profile-selected"))
	}
}

func TestWriteSecretFileUsesPrivateModeAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := WriteSecretFile(path, []byte(`{"secret":"value"}`)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".tmp-*")); err != nil || len(matches) != 0 {
		t.Errorf("temporary files after success = %v, err %v", matches, err)
	}
}

func TestWriteSecretFileFailurePreservesExistingFileAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := WriteSecretFile(path, []byte("after")); err == nil {
		t.Fatal("WriteSecretFile succeeded in a non-writable directory")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "before" {
		t.Errorf("existing file = %q, err %v; want unchanged", got, err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".tmp-*")); err != nil || len(matches) != 0 {
		t.Errorf("temporary files after failure = %v, err %v", matches, err)
	}
}
