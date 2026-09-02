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

// A session file that exists but cannot be parsed must not cost the fields
// the caller is about to delete their only other copy of: MigrateLayout fails
// closed and credentials.json is left exactly as it was on disk.
func TestMigrateFailsClosedWhenSessionIsUnreadable(t *testing.T) {
	dir := layoutHarness(t, `{"profiles":{"default":{"api_key":"","workspace":"acme","gateway_key":"sk-orq-GW"}}}`, nil)
	corrupt := filepath.Join(dir, "sessions", "my.orq.ai.json")
	if err := os.WriteFile(corrupt, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := readDoc(t, filepath.Join(dir, "credentials.json"))

	if err := MigrateLayout(dir); err == nil {
		t.Fatal("expected an error, got nil")
	}

	after := readDoc(t, filepath.Join(dir, "credentials.json"))
	profilesBefore, _ := before["profiles"].(map[string]any)
	profilesAfter, _ := after["profiles"].(map[string]any)
	if len(profilesAfter) != len(profilesBefore) {
		t.Fatalf("credentials.json changed on a failed migration: before=%v after=%v", before, after)
	}
	profile, _ := profilesAfter["default"].(map[string]any)
	if profile == nil || profile["gateway_key"] != "sk-orq-GW" {
		t.Errorf("the profile carrying the only copy of the fields was altered despite the failure: %v", after)
	}
}

// A prior interrupted run can leave profile-selected naming a profile that is
// already gone, with nothing in the current run to say a profile was just
// removed. reconcileSelectedProfile must catch this unconditionally, not only
// for names this run's migrateCredentials happened to delete.
func TestMigrateRecoversAStaleSelectionAfterProfileAlreadyGone(t *testing.T) {
	dir := layoutHarness(t, `{}`, nil)
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"profile-decided":true,"profile-selected":"default"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.Set("profile-selected", "default")

	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}

	if viper.GetString("profile-selected") != "" {
		t.Error("in-process selection not cleared on the recovery path")
	}
	doc := readDoc(t, config)
	if _, still := doc["profile-selected"]; still {
		t.Errorf("persisted selection survived: %v", doc)
	}
	if doc["profile-decided"] != true {
		t.Errorf("rest of config.json damaged: %v", doc)
	}
}

// A real #63 tree never has a companion keyless profile once state.<name> is
// written (that migration deleted it as part of writing state), but if one
// survives anyway a state entry is proof enough of ownership to remove it —
// closing the hazard rather than relying on it being unreachable.
func TestMigrateRemovesCompanionKeylessProfileForStateEntry(t *testing.T) {
	dir := layoutHarness(t, `{"profiles":{"default":{"api_key":""}},"state":{"default":{"gateway_key":"sk-orq-GW","workspace":"acme"}}}`,
		map[string]*Session{"default.json": sessionOn("https://api.orq.ai", "acme")})
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	s := readDoc(t, filepath.Join(dir, "sessions", "my.orq.ai.json"))
	if s["gatewayKey"] != "sk-orq-GW" {
		t.Errorf("state fields did not reach the session: %v", s)
	}
	creds := readDoc(t, filepath.Join(dir, "credentials.json"))
	if profiles, _ := creds["profiles"].(map[string]any); len(profiles) != 0 {
		t.Errorf("companion keyless profile survived: %v", profiles)
	}
}

// A prior interrupted run can leave sessions/session.json already occupying
// the legacy fold-in's target name. Neither login may be lost to a silent
// os.Rename overwrite, and the incoming one must still end up under its host
// name rather than parked forever under a name the scan will never revisit.
func TestMigrateLegacyFoldInDoesNotClobberAnExistingSessionDotJSON(t *testing.T) {
	dir := layoutHarness(t, `{}`, map[string]*Session{
		"session.json": sessionOn("https://existing.example", "kept"),
	})
	legacy := filepath.Join(dir, "session.json") // ~/.orq/session.json, not sessions/session.json
	raw, _ := json.Marshal(sessionOn("https://incoming.example", "incoming"))
	if err := os.WriteFile(legacy, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}

	kept := readDoc(t, filepath.Join(dir, "sessions", "existing.example.json"))
	if kept["activeWorkspaceKey"] != "kept" {
		t.Errorf("pre-existing session was overwritten: %v", kept)
	}
	incoming := readDoc(t, filepath.Join(dir, "sessions", "incoming.example.json"))
	if incoming["activeWorkspaceKey"] != "incoming" {
		t.Errorf("legacy session did not survive the fold-in: %v", incoming)
	}
}

// Three or more logins to one host: the winner loop and multi-loser demotion
// must handle more than a single pair.
func TestMigrateHandlesThreeOrMoreSessionsForOneHost(t *testing.T) {
	dir := layoutHarness(t, `{}`, map[string]*Session{
		"a.json": sessionOn("https://api.orq.ai", "one"),
		"b.json": sessionOn("https://api.orq.ai", "two"),
		"c.json": sessionOn("https://api.orq.ai", "three"),
	})
	past := time.Now().Add(-2 * time.Hour)
	mid := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "sessions", "a.json"), past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "sessions", "b.json"), mid, mid); err != nil {
		t.Fatal(err)
	}
	// c.json keeps "now": the freshest of the three.

	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}

	winner := readDoc(t, filepath.Join(dir, "sessions", "my.orq.ai.json"))
	if winner["activeWorkspaceKey"] != "three" {
		t.Errorf("winner = %v, want the freshest of the three", winner["activeWorkspaceKey"])
	}
	for _, loser := range []string{"a.json.deprecated", "b.json.deprecated"} {
		if _, err := os.Stat(filepath.Join(dir, "sessions", loser)); err != nil {
			t.Errorf("%s missing after migration: %v", loser, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "c.json")); err == nil {
		t.Error("winner's original name still present")
	}
}

// Resuming after a prior run already renamed the winner into place: the
// host-named file already exists and is freshest, and a stale duplicate under
// its old name is still lying around. The already-correct file must not be
// rewritten (the winner == target branch), and the stale one still gets
// demoted.
func TestMigrateResumesWhenHostFileAlreadyExists(t *testing.T) {
	dir := layoutHarness(t, `{}`, map[string]*Session{
		"my.orq.ai.json": sessionOn("https://api.orq.ai", "current"),
		"default.json":   sessionOn("https://api.orq.ai", "stale"),
	})
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "sessions", "default.json"), past, past); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "sessions", "my.orq.ai.json")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}

	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Error("the already-correct session file was rewritten")
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "default.json.deprecated")); err != nil {
		t.Errorf("stale duplicate was not deprecated: %v", err)
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
