# `--profile` is an API key; sessions are keyed by server — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `--profile` selects only a bartolo API-key profile; a browser login lives in `~/.orq/sessions/<host>.json` chosen by the resolved server; the gateway key `orq setup` mints lives in that session file; `state.go` and the custom profile resolvers are deleted.

**Architecture:** bartolo owns `credentials.json` and profile resolution end to end. The `auth` package owns sessions, now addressed by the host `custom.resolveServer` decided, and one idempotent migration (`auth.MigrateLayout`) that renames session files to their host and moves this CLI's fields out of `credentials.json` into them. The root PreRun no longer second-guesses bartolo: it resolves the server, runs the migration, and injects the session's workspace token only when no API key is in force.

**Tech Stack:** Go 1.24, cobra, viper, bartolo v0.9.0 (`bartolocli.ActiveProfileName()`, `GetProfile()`, `ProfileExists()`), `encoding/json`, `os.Rename`.

**Spec:** `docs/superpowers/specs/2026-09-02-profile-is-an-api-key-design.md`. Branch `Baukebrenninkmeijer/profile-is-an-api-key`, stacked on `Baukebrenninkmeijer/upgrade-bartolo-version` (PR #63). Draft PR #65.

## Global Constraints

- Anything under `cli/custom/` ships on both modules; after every task also run `cd packages/orq-rc && go build ./... && go vet ./...`.
- `cli/generated/` is never edited. `surface.json` is regenerated with `go run ./cmd/surface-dump -write` and the diff reviewed.
- Never `cat` `~/.orq/config.json`, `credentials.json` or `sessions/*.json`; smoke tests use a throwaway `HOME`.
- Never run `git stash`, `git reset`, `git checkout .`, `git restore`, `git clean` — the worktree is shared.
- Hosted service has two interchangeable hostnames, `my.orq.ai` (`auth.DefaultAPIBaseURL`) and `api.orq.ai` (`auth.LegacyDefaultAPIBaseURL`). Both map to **one** session file, `my.orq.ai.json`. (Spec correction: the spec's examples said `api.orq.ai.json`; the canonical hosted file is `my.orq.ai.json`.)
- Spec correction: `mirrorServerToViper` **stays** — it is how the generated commands see the resolved server. Only the session→server bridge in the PreRun is deleted.
- Commit after every task with a conventional-commit message; `git add` the exact files, never `-A`.

## File structure

| File | Responsibility after this plan |
|---|---|
| `cli/custom/auth/session.go` | `Session` (now with gateway fields), host rule, path resolution from the resolved server, read/save/clear |
| `cli/custom/auth/secretfile.go` | `WriteSecretFile` (moved out of `state.go`) |
| `cli/custom/auth/migrate.go` | `MigrateLayout`: session-file renames + moving our fields out of `credentials.json` |
| `cli/custom/auth/state.go` | **deleted** |
| `cli/custom/register.go` | PreRun: repair profile type, resolve server, migrate, inject session token; nothing else |
| `cli/custom/commands/setup.go` | gateway key read/write goes through `auth.Session` |
| `cli/custom/commands/auth.go` | login/logout/whoami honour "a profile in force means no session" |
| `cli/custom/commands/server.go` | `ProfileServer` reads bartolo's profile; `BindProfileServer` deleted |
| `cli/custom/commands/profiles.go` | **deleted** (bartolo's `auth profile list` / deprecated `list-profiles` take over) |
| `cli/custom/commands/doctor.go` | reports profile or session, names the session host |
| `cli/custom/commands/orqi.go` | passes `ORQ_SERVER` to the child |

---

### Task 1: Host rule and session path from the resolved server

**Files:**
- Modify: `cli/custom/auth/session.go:15-20, 66-91, 119-136`
- Test: `cli/custom/auth/session_test.go`

**Interfaces:**
- Produces: `func SessionHost(apiBase string) string`, `func sessionPathFor(host string) string`, `func SessionFilePath() string` (now derived from `ResolveURLs("").APIBaseURL`), `func sessionFilesDir() string` (unexported alias of `sessionsDir`). `ActiveProfile()` is **removed** from this package.

- [ ] **Step 1: Write the failing tests**

Append to `cli/custom/auth/session_test.go`:

```go
func TestSessionHostRule(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.orq.ai":          "my.orq.ai",
		"https://my.orq.ai/":          "my.orq.ai",
		"https://My.Staging.ORQ.ai":   "my.staging.orq.ai",
		"http://localhost:8080":       "localhost_8080",
		"http://127.0.0.1:3000/v2":    "127.0.0.1_3000",
		"https://orq.acme.internal":   "orq.acme.internal",
		"https://[::1]:4200":          "__1_4200",
	} {
		if got := SessionHost(in); got != want {
			t.Errorf("SessionHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// The session in play is the one for the server this invocation resolved:
// --server, ORQ_SERVER, `orq server set`, else the hosted default.
func TestSessionFilePathFollowsTheResolvedServer(t *testing.T) {
	isolateHome(t)
	t.Cleanup(func() { SetServer("", "default") })

	SetServer("", "default")
	if got := filepath.Base(SessionFilePath()); got != "my.orq.ai.json" {
		t.Errorf("default session file = %q, want my.orq.ai.json", got)
	}
	SetServer("https://my.staging.orq.ai", "flag")
	if got := filepath.Base(SessionFilePath()); got != "my.staging.orq.ai.json" {
		t.Errorf("staging session file = %q, want my.staging.orq.ai.json", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cli/custom/auth/ -run 'TestSessionHostRule|TestSessionFilePathFollowsTheResolvedServer' -v`
Expected: FAIL — `undefined: SessionHost`.

- [ ] **Step 3: Implement the host rule and the new path**

In `cli/custom/auth/session.go` replace the constants block and the `ActiveProfile`/`sessionsDir`/`SessionFilePath` functions:

```go
const (
	sessionDirName     = ".orq"
	sessionsSubdirName = "sessions"
	legacyFileName     = "session.json"
)
```

```go
// SessionHost names the session file for a server: the host, lowercased, with
// `_<port>` when one is present and anything outside [a-z0-9.-] replaced by
// `_`. No scheme — http and https to one host are one login. The hosted
// service answers under two names, and those are one login too.
func SessionHost(apiBase string) string {
	apiBase = strings.TrimSpace(apiBase)
	if IsHostedAPIBase(apiBase) {
		apiBase = DefaultAPIBaseURL
	}
	u, err := url.Parse(apiBase)
	if err != nil || u.Hostname() == "" {
		return sanitizeHost(apiBase)
	}
	name := u.Hostname()
	if p := u.Port(); p != "" {
		name += "_" + p
	}
	return sanitizeHost(name)
}

func sanitizeHost(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func sessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, sessionDirName, sessionsSubdirName)
}

func sessionPathFor(host string) string {
	return filepath.Join(sessionsDir(), host+".json")
}

// SessionFilePath is the session for the server this invocation resolved
// (custom.resolveServer → SetServer), so `--server https://my.staging.orq.ai`
// reads the staging login and a bare `orq` reads the hosted one.
func SessionFilePath() string {
	return sessionPathFor(SessionHost(ResolveURLs("").APIBaseURL))
}
```

Add `"net/url"` to the imports. Delete `defaultProfile`, `ActiveProfile`, and `migrateLegacySession` (the legacy `~/.orq/session.json` move is re-done host-aware in Task 4). In `InspectSession` delete the `migrateLegacySession()` call. Leave `legacySessionFilePath`, `LegacySessionFilePath`, `SessionsDir`, `ensureSessionDir` as they are.

- [ ] **Step 4: Run the tests**

Run: `go test ./cli/custom/auth/ -run 'TestSessionHostRule|TestSessionFilePathFollowsTheResolvedServer' -v`
Expected: PASS. The package will not fully build yet (`state.go`, `SavedAgentKey` still reference `ActiveProfile`) — that is Task 2/3. If `go test` refuses to compile, temporarily stub `func ActiveProfile() string { return "" }` at the bottom of `session.go` and delete it in Task 3.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/auth/session.go cli/custom/auth/session_test.go
git commit -m "feat(auth): address a session by the server it was issued by"
```

---

### Task 2: Gateway-key fields on `Session`; `SavedAgentKey` reads them

**Files:**
- Modify: `cli/custom/auth/session.go:35-49, 256-271`
- Test: `cli/custom/auth/session_test.go`

**Interfaces:**
- Produces: `Session.GatewayKey`, `Session.GatewayKeyID`, `Session.GatewayKeyExpiresAt`, `Session.GatewayWorkspace` (JSON `gatewayKey`, `gatewayKeyId`, `gatewayKeyExpiresAt`, `gatewayWorkspace`, all `omitempty`); `func SavedAgentKey() (key, workspace string)` unchanged signature.

- [ ] **Step 1: Write the failing test**

```go
// The credential agents are wired with is the minted gateway key, recorded on
// the login it was minted from. A bartolo profile in force is the fallback: its
// key is what the agents got, and its workspace is unknowable.
func TestSavedAgentKeyReadsTheSession(t *testing.T) {
	isolateHome(t)
	t.Cleanup(func() { SetServer("", "default") })
	s := validSession("acme")
	s.GatewayKey, s.GatewayWorkspace = "sk-orq-GW", "acme"
	if err := SaveSession(s); err != nil {
		t.Fatal(err)
	}
	if key, ws := SavedAgentKey(); key != "sk-orq-GW" || ws != "acme" {
		t.Errorf("SavedAgentKey = (%q, %q), want the session's gateway key", key, ws)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cli/custom/auth/ -run TestSavedAgentKeyReadsTheSession -v`
Expected: FAIL — `s.GatewayKey undefined`.

- [ ] **Step 3: Add the fields and rewrite `SavedAgentKey`**

In the `Session` struct, after `WorkspaceTokens`:

```go
	// The gateway key `orq setup` minted from this login for coding agents,
	// its id (the handle for revoking it), its expiry, and the workspace it
	// was minted for. Not a credential for the platform API, so never a
	// bartolo profile.
	GatewayKey          string `json:"gatewayKey,omitempty"`
	GatewayKeyID        string `json:"gatewayKeyId,omitempty"`
	GatewayKeyExpiresAt string `json:"gatewayKeyExpiresAt,omitempty"`
	GatewayWorkspace    string `json:"gatewayWorkspace,omitempty"`
```

Replace `SavedAgentKey`:

```go
// SavedAgentKey returns the credential agent configs are wired with, and the
// workspace it was minted for: the gateway key on the login session, else the
// API key of the bartolo profile in force (a key the user brought, whose
// workspace is unknowable). It lives here because launch needs it too and
// launch cannot import commands.
func SavedAgentKey() (key, workspace string) {
	if session, err := ReadSession(); err == nil && session != nil && session.GatewayKey != "" {
		return session.GatewayKey, session.GatewayWorkspace
	}
	if bartolocli.Creds == nil {
		return "", ""
	}
	return strings.TrimSpace(bartolocli.GetProfile()["api_key"]), ""
}
```

- [ ] **Step 4: Run the test**

Run: `go test ./cli/custom/auth/ -run TestSavedAgentKeyReadsTheSession -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/auth/session.go cli/custom/auth/session_test.go
git commit -m "feat(auth): record the minted gateway key on the login session"
```

---

### Task 3: Move `WriteSecretFile` out, delete `state.go`

**Files:**
- Create: `cli/custom/auth/secretfile.go`
- Delete: `cli/custom/auth/state.go`, `cli/custom/auth/state_test.go`

**Interfaces:**
- Produces: `func WriteSecretFile(path string, data []byte) error` (identical body to the one in `state.go`).

- [ ] **Step 1: Create `secretfile.go`**

```go
package auth

import (
	"os"
	"path/filepath"
)

// WriteSecretFile replaces a secret-bearing file through a temp file in the
// same directory, so a crash or a concurrent reader never sees a half-written
// credentials.json — truncating the real file in place can lose every stored
// credential, not only the one being written. bartolo's own credentials writer
// does the same; it is unexported there, hence the copy.
func WriteSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 2: Delete `state.go` and `state_test.go`, remove any `ActiveProfile` stub**

```bash
git rm -q cli/custom/auth/state.go cli/custom/auth/state_test.go
```

- [ ] **Step 3: Confirm the auth package builds and its tests pass**

Run: `go build ./cli/custom/auth/ && go test ./cli/custom/auth/`
Expected: `ok`. (`go build ./...` fails for now — `commands` and `register.go` still call the deleted symbols; Tasks 5–9 fix them.)

- [ ] **Step 4: Commit**

```bash
git add cli/custom/auth/secretfile.go
git commit -m "refactor(auth): drop the per-profile state section"
```

---

### Task 4: `MigrateLayout` — rename session files, move our fields into them

**Files:**
- Create: `cli/custom/auth/migrate.go`
- Test: `cli/custom/auth/migrate_test.go`

**Interfaces:**
- Consumes: `SessionHost`, `sessionPathFor`, `sessionsDir`, `legacySessionFilePath`, `WriteSecretFile`, `bartolocli.NewCredentialsFile`.
- Produces: `func MigrateLayout(configDir string) error` — idempotent, called from the root PreRun with `viper.GetString("config-directory")`. Also `var ownedFields = []string{"gateway_key", "gateway_key_id", "gateway_key_expires_at", "workspace"}`.

- [ ] **Step 1: Write the failing tests**

Create `cli/custom/auth/migrate_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cli/custom/auth/ -run TestMigrate -v`
Expected: FAIL — `undefined: MigrateLayout`.

- [ ] **Step 3: Implement `migrate.go`**

```go
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// ownedFields are the fields only this CLI ever wrote into a bartolo profile.
// Their presence is what marks a profile as ours to migrate; a keyless profile
// without any of them belongs to someone else and is left alone.
var ownedFields = []string{"gateway_key", "gateway_key_id", "gateway_key_expires_at", "workspace"}

// MigrateLayout brings an older ~/.orq up to the current layout in one pass:
// session files are named by host, and everything this CLI records about a
// login lives in that login's file rather than in bartolo's credentials.json.
// It returns an error rather than warning: a migration that did not run leaves
// a keyless profile bartolo fails every request on.
//
// Idempotent and cheap once done — both halves check before touching disk.
func MigrateLayout(configDir string) error {
	renamed, err := migrateSessionFiles()
	if err != nil {
		return err
	}
	return migrateCredentials(configDir, renamed)
}

// migrateSessionFiles renames sessions/<name>.json to sessions/<host>.json and
// reports old name → host. The pre-multi-profile ~/.orq/session.json joins in.
// Two files for one host: the newest by mtime wins, the other is kept as
// <name>.json.deprecated so nothing a user might still want is deleted.
func migrateSessionFiles() (map[string]string, error) {
	dir := sessionsDir()
	renamed := map[string]string{}

	if legacy := legacySessionFilePath(); fileExists(legacy) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := os.Rename(legacy, filepath.Join(dir, "session.json")); err != nil {
			return nil, err
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return renamed, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") || strings.HasPrefix(n, ".") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(dir, name)
		s, err := readSessionFile(path)
		if err != nil || s == nil || s.APIBaseURL == "" {
			continue // not a session of ours; leave it where it is
		}
		host := SessionHost(s.APIBaseURL)
		stem := strings.TrimSuffix(name, ".json")
		if stem == host {
			continue
		}
		target := sessionPathFor(host)
		renamed[stem] = host
		if !fileExists(target) {
			if err := os.Rename(path, target); err != nil {
				return nil, err
			}
			continue
		}
		if newerThan(path, target) {
			// This file is the fresher login: demote the current holder.
			if err := os.Rename(target, deprecatedName(target)); err != nil {
				return nil, err
			}
			if err := os.Rename(path, target); err != nil {
				return nil, err
			}
			fmt.Fprintf(bartolocli.Stderr, "kept the newer login for %s; the other is at %s\n", host, deprecatedName(target))
			continue
		}
		if err := os.Rename(path, deprecatedName(path)); err != nil {
			return nil, err
		}
		fmt.Fprintf(bartolocli.Stderr, "kept the newer login for %s; %s is at %s\n", host, name, deprecatedName(path))
	}
	return renamed, nil
}

// migrateCredentials moves our fields out of credentials.json — both the
// pre-#63 shape (inside profiles.<name>) and #63's `state.<name>` — onto the
// session of the host they belong to, and deletes a profile of ours that is
// left with no api_key. The file is rewritten only when something moved.
func migrateCredentials(configDir string, renamed map[string]string) error {
	if bartolocli.Creds == nil || !credentialsNeedMigration() {
		return nil
	}
	path := filepath.Join(configDir, "credentials.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}

	profiles, _ := doc["profiles"].(map[string]any)
	state, _ := doc["state"].(map[string]any)
	var removed []string

	for name, value := range profiles {
		profile, ok := value.(map[string]any)
		if !ok || !ownedProfile(profile) {
			continue
		}
		fields := collectOwned(profile)
		for _, f := range ownedFields {
			delete(profile, f)
		}
		if stringField(profile, "api_key") == "" {
			// A session login's profile: its server travels with the fields,
			// and the entry itself has no reason to exist.
			fields["server"] = stringField(profile, "server")
			delete(profiles, name)
			removed = append(removed, name)
			if err := attachToSession(name, fields, renamed); err != nil {
				return err
			}
		}
		// An API-key profile keeps its key, type and server; the workspace we
		// recorded next to it is dropped — a brought key has no known workspace.
	}
	for name, value := range state {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if err := attachToSession(name, stringMap(entry), renamed); err != nil {
			return err
		}
	}
	delete(doc, "state")
	if len(profiles) == 0 {
		delete(doc, "profiles")
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := WriteSecretFile(path, out); err != nil {
		return err
	}
	reloaded, err := bartolocli.NewCredentialsFile(configDir)
	if err != nil {
		return err
	}
	bartolocli.Creds = reloaded
	return dropRemovedSelection(configDir, removed)
}

// attachToSession writes gateway fields onto the session for the host a
// profile named: its own server, else the session that used to carry its
// name, else the hosted default. No session there means nothing to attach the
// key to, and it is dropped — a gateway key without its login is dead weight.
func attachToSession(profileName string, fields map[string]string, renamed map[string]string) error {
	host := ""
	if server := strings.TrimSpace(fields["server"]); server != "" {
		host = SessionHost(server)
	} else if h, ok := renamed[profileName]; ok {
		host = h
	} else if fileExists(sessionPathFor(profileName)) {
		host = profileName
	} else {
		host = SessionHost(DefaultAPIBaseURL)
	}
	path := sessionPathFor(host)
	s, err := readSessionFile(path)
	if err != nil || s == nil {
		return nil
	}
	changed := false
	set := func(dst *string, v string) {
		if v != "" && *dst == "" {
			*dst = v
			changed = true
		}
	}
	set(&s.GatewayKey, fields["gateway_key"])
	set(&s.GatewayKeyID, fields["gateway_key_id"])
	set(&s.GatewayKeyExpiresAt, fields["gateway_key_expires_at"])
	set(&s.GatewayWorkspace, fields["workspace"])
	if !changed {
		return nil
	}
	return saveSessionTo(path, s)
}

func credentialsNeedMigration() bool {
	if len(bartolocli.Creds.GetStringMap("state")) > 0 {
		return true
	}
	for _, value := range bartolocli.Creds.GetStringMap("profiles") {
		if profile, ok := value.(map[string]any); ok && ownedProfile(profile) {
			return true
		}
	}
	return false
}

func ownedProfile(profile map[string]any) bool {
	for _, f := range ownedFields {
		if stringField(profile, f) != "" {
			return true
		}
	}
	return false
}

func collectOwned(profile map[string]any) map[string]string {
	out := map[string]string{}
	for _, f := range ownedFields {
		out[f] = stringField(profile, f)
	}
	return out
}

func stringMap(m map[string]any) map[string]string {
	out := map[string]string{}
	for k := range m {
		out[k] = stringField(m, k)
	}
	return out
}

func stringField(m map[string]any, field string) string {
	v, _ := m[field].(string)
	return strings.TrimSpace(v)
}

// dropRemovedSelection clears a persisted `auth profile use` that names a
// profile this migration deleted; left behind, bartolo resolves it as in force
// and fails every request with "profile is not configured". profile-decided is
// kept, or bartolo re-adopts `default` on the next run.
func dropRemovedSelection(configDir string, removed []string) error {
	selected := strings.TrimSpace(viper.GetString("profile-selected"))
	if selected == "" {
		return nil
	}
	gone := false
	for _, name := range removed {
		if strings.EqualFold(name, selected) {
			gone = true
		}
	}
	if !gone {
		return nil
	}
	viper.Set("profile-selected", "")
	path := filepath.Join(configDir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	delete(doc, "profile-selected")
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return WriteSecretFile(path, out)
}

func readSessionFile(path string) (*Session, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func newerThan(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	return errA == nil && errB == nil && ai.ModTime().After(bi.ModTime())
}

func deprecatedName(path string) string { return path + ".deprecated" }
```

In `session.go`, split `SaveSession` so the migration can write to a named path:

```go
func SaveSession(s *Session) error {
	return saveSessionTo(SessionFilePath(), s)
}

// saveSessionTo writes atomically: temp file in the same directory, then
// rename, so a concurrent reader never sees a torn file and two writers end
// with one intact winner.
func saveSessionTo(path string, s *Session) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
```

Delete `ensureSessionDir` (folded into `saveSessionTo`).

- [ ] **Step 4: Run the tests**

Run: `go test ./cli/custom/auth/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: every `TestMigrate*` and the earlier session tests PASS.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/auth/migrate.go cli/custom/auth/migrate_test.go cli/custom/auth/session.go
git commit -m "feat(auth): migrate sessions to host names and our fields out of credentials.json"
```

---

### Task 5: `commands/setup.go` — gateway key through the session; API-key profiles are bartolo's

**Files:**
- Modify: `cli/custom/commands/setup.go:534, 730-825, 898-937, 964-968, 1894-1916`
- Modify: `cli/custom/commands/server.go:30-63`

**Interfaces:**
- Produces (package `commands`): `func profileInForce() bool`, `func bartoloProfileName() string`, `func saveAPIKeyProfile(key string) error` (workspace parameter dropped), `func saveGatewayKeyProfile(key, keyID string, expiresAt time.Time, workspace string) error`, `func gatewayKeyExpiry() (time.Time, bool)`, `func savedGatewayKeyID() string`, `func storedAPIKeyProfile() bool`, `func ProfileServer() string`. Deleted: `clearGatewayKeyFields`, `clearAPIKeyProfile`, `writeAPIKeyProfile`, `writeGatewayKeyProfile`, `writeCredsProfile`, `BindProfileServer`.

- [ ] **Step 1: Add the two profile helpers** near `BartoloAuthType` in `setup.go`:

```go
// profileInForce reports whether bartolo will authenticate with a saved API
// key on this call (--profile, ORQ_PROFILE or `auth profile use`). When it
// does, the login session is not consulted at all.
func profileInForce() bool { return bartolocli.ActiveProfileName() != "" }

// bartoloProfileName is the profile an API-key write lands in: the one in
// force, else `default`.
func bartoloProfileName() string {
	if name := bartolocli.ActiveProfileName(); name != "" {
		return name
	}
	return "default"
}
```

- [ ] **Step 2: Replace the credential writers** (`setup.go:730-825`, `898-937`) with:

```go
// saveAPIKeyProfile stores a key the user brought as a bartolo profile: key,
// handler type and the resolved server, nothing of ours. bartolo resolves it
// from here on.
func saveAPIKeyProfile(key string) error {
	name := bartoloProfileName()
	bartolocli.Creds.Set("profiles."+name+".api_key", key)
	bartolocli.Creds.Set("profiles."+name+".type", BartoloAuthType())
	if server := auth.Server(); server != "" {
		bartolocli.Creds.Set("profiles."+name+".server", server)
	}
	return saveCreds()
}

// saveGatewayKeyProfile records the minted key on the login it was minted
// from. It is gateway-scoped, so it never becomes a bartolo profile: a profile
// that cannot authenticate the platform API is exactly what bartolo refuses to
// fall back from.
func saveGatewayKeyProfile(key, keyID string, expiresAt time.Time, workspace string) error {
	session, err := auth.ReadSession()
	if err != nil {
		return err
	}
	if session == nil {
		return errors.New("no login session to record the gateway key on")
	}
	session.GatewayKey = key
	session.GatewayKeyID = keyID
	session.GatewayKeyExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	session.GatewayWorkspace = workspace
	return auth.SaveSession(session)
}

// gatewayKeyExpiry reports when the saved key expires. Not-ok means no expiry
// is recorded, and callers must treat that as "unknown", never as "expired".
func gatewayKeyExpiry() (time.Time, bool) {
	session, err := auth.ReadSession()
	if err != nil || session == nil || session.GatewayKeyExpiresAt == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, session.GatewayKeyExpiresAt)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

func gatewayKeyDueForRenewal(now time.Time) bool {
	at, ok := gatewayKeyExpiry()
	return ok && at.Sub(now) < gatewayKeyRenewWindow
}

func savedAPIKey() (key, workspace string) { return auth.SavedAgentKey() }

// savedGatewayKeyID is the handle for revoking the minted key; logout prints
// it because logout cannot revoke the key itself.
func savedGatewayKeyID() string {
	session, err := auth.ReadSession()
	if err != nil || session == nil {
		return ""
	}
	return session.GatewayKeyID
}
```

Delete `clearGatewayKeyFields`, `clearAPIKeyProfile`, `writeAPIKeyProfile`, `writeGatewayKeyProfile`, `writeCredsProfile`. Replace `storedAPIKeyProfile`:

```go
// storedAPIKeyProfile reports whether a bartolo profile in force holds a key.
func storedAPIKeyProfile() bool {
	return bartolocli.Creds != nil && strings.TrimSpace(bartolocli.GetProfile()["api_key"]) != ""
}
```

Replace `recordKeyWorkspace`'s write (line ~1909, the `auth.SetStateValue(...)` + `saveCreds()` block):

```go
	session, err := auth.ReadSession()
	if err != nil || session == nil {
		return
	}
	session.GatewayWorkspace = keyWS
	if err := auth.SaveSession(session); err != nil {
		rep.warn("could not record the key's workspace: %v", err)
	}
```

Update the two callers of `saveAPIKeyProfile(key, "")` (`setup.go:402` in `resolveAuth`, `auth.go:91` in `apiKeyLogin`) to `saveAPIKeyProfile(key)`. Delete line `setup.go:534` (`BindProfileServer(auth.ActiveProfile(), auth.Server())` and its `if err` wrapper) — the session's host is its file name now.

- [ ] **Step 3: `server.go`** — replace `ProfileServer` and delete `BindProfileServer`:

```go
// ProfileServer is the host bound to the bartolo profile in force, or "" when
// there is none. Selecting a profile is how you select a backend, so
// custom.resolveServer ranks it above `orq server set`.
func ProfileServer() string {
	if bartolocli.Creds == nil || !profileInForce() {
		return ""
	}
	return strings.TrimSpace(bartolocli.GetProfile()["server"])
}
```

Remove the now-unused `"orq/cli/custom/auth"` import from `server.go` only if nothing else in the file uses it (`serverURL`, `sessionAPIBase` still do — keep it).

- [ ] **Step 4: Build the commands package**

Run: `go build ./cli/custom/commands/ 2>&1 | head -20`
Expected: remaining errors only in `auth.go` (`clearAPIKeyProfile`, `auth.ActiveProfile`), `doctor.go`, `profiles.go` — Tasks 6–8.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/commands/setup.go cli/custom/commands/server.go
git commit -m "refactor(setup): keep the gateway key on the session, API keys on bartolo's profile"
```

---

### Task 6: `auth login` / `auth logout` / `whoami` under "a profile in force means no session"

**Files:**
- Modify: `cli/custom/commands/auth.go`
- Test: `cli/custom/commands/auth_test.go` (create)

**Interfaces:**
- Consumes: `profileInForce()`, `bartoloProfileName()`, `saveAPIKeyProfile(key)`, `savedGatewayKeyID()`.
- Produces: `var errProfileInForce` message used by login and logout.

- [ ] **Step 1: Write the failing test**

Create `cli/custom/commands/auth_test.go`:

```go
package commands

import (
	"strings"
	"testing"

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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cli/custom/commands/ -run TestBrowserLoginRefusesAProfile -v`
Expected: FAIL (compile errors from Task 5 leftovers, or the login attempts a network call).

- [ ] **Step 3: Implement**

In `auth.go`, add near the top:

```go
// profileInForceError is what login and logout say when --profile names an
// API key: the session they act on is not what that flag selects.
func profileInForceError(verb string) error {
	return fmt.Errorf("profile %q is an API key, not a login; %s without --profile, or pass --profile \"\" for this call", bartolocli.ActiveProfileName(), verb)
}
```

Add `bartolocli "github.com/orq-ai/bartolo/cli"` to the imports.

In `NewLoginCommand`'s `RunE`, after `method` is decided and before the `if method == "API key"` branch:

```go
			if method != "API key" && profileInForce() {
				return profileInForceError("log in to another server with --server")
			}
```

In `apiKeyLogin`, replace both `auth.ActiveProfile()` with `bartoloProfileName()` and the save call with `saveAPIKeyProfile(key)`.

In `NewLogoutCommand`'s `RunE`, first lines:

```go
			if profileInForce() {
				return profileInForceError("log out")
			}
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
```

In the `session == nil` branch delete the `clearAPIKeyProfile()` call and `keyCleared`; the human view prints `info("Not logged in - nothing to clear.")`, and the payload drops `"cleared"`/`"api_key_profile_cleared"` in favour of `"cleared": false`. In the main branch delete the `keyCleared, err := clearAPIKeyProfile()` block and the `"api_key_profile_cleared"` payload key. Everything else (revoke, `ClearLocalSession`, env-file clearing, disconnect, surviving gateway key note) stays.

In `NewWhoAmICommand`'s `RunE`, first lines:

```go
			if profileInForce() {
				if wantsHumanView(cmd) {
					success("Using API-key profile %s", bartolocli.ActiveProfileName())
					kv(9, "server", "%s", auth.ResolveURLs(serverURL()).APIBaseURL)
					kv(9, "api_key", "%s", maskToken(bartolocli.GetProfile()["api_key"]))
					return nil
				}
				return emit(map[string]any{
					"profile":  bartolocli.ActiveProfileName(),
					"server":   auth.ResolveURLs(serverURL()).APIBaseURL,
					"api_key":  maskToken(bartolocli.GetProfile()["api_key"]),
					"identity": nil,
				})
			}
```

- [ ] **Step 4: Run the test and the package build**

Run: `go test ./cli/custom/commands/ -run TestBrowserLoginRefusesAProfile -v && go build ./cli/custom/commands/`
Expected: PASS; build errors left only in `doctor.go` and `profiles.go`.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/commands/auth.go cli/custom/commands/auth_test.go
git commit -m "feat(auth): login, logout and whoami treat a profile as an API key"
```

---

### Task 7: Delete the custom `list-profiles`; bartolo's deprecated aliases take over

**Files:**
- Delete: `cli/custom/commands/profiles.go`, `cli/custom/commands/profiles_test.go`
- Modify: `cli/custom/register.go:552-589` (`attachAuthSubcommands`)

bartolo 0.9 already registers `auth list-profiles` and `auth add-profile` with a deprecation notice ("use `auth profile list` instead", printed to its Stderr). Nothing to build; we only stop replacing them.

- [ ] **Step 1: Remove the files and the replacement**

```bash
git rm -q cli/custom/commands/profiles.go cli/custom/commands/profiles_test.go
```

In `attachAuthSubcommands` delete the block that removes bartolo's `list-profiles` and adds `commands.NewListProfilesCommand()` (the comment starting "Bartolo's profile command emits…" through `authParent.AddCommand(commands.NewListProfilesCommand())`).

- [ ] **Step 2: Confirm `maskProfileSecret` has no other callers**

Run: `grep -rn "maskProfileSecret\|renderProfileTable\|listAuthProfiles\|profileTableColumns" cli/custom`
Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add cli/custom/register.go
git commit -m "refactor(auth): let bartolo own auth list-profiles and auth profile"
```

---

### Task 8: `doctor` reports the profile or the session, and names the session host

**Files:**
- Modify: `cli/custom/commands/doctor.go:60-200, 232-235, 470-495`
- Modify: `cli/custom/commands/doctor_test.go:440`

- [ ] **Step 1: Repoint the test's session path**

`doctor_test.go:440`: replace `filepath.Join(sessionsDir, auth.ActiveProfile()+".json")` with `auth.SessionFilePath()` (and drop the now-unused `sessionsDir` variable if it was only used there — check with `go vet`).

- [ ] **Step 2: Implement**

Where doctor calls `auth.InspectSession()` (before line 80), gate it:

```go
			inspect := auth.InspectSession()
			if profileInForce() {
				// The session is not consulted while a profile is in force;
				// reporting it would describe a login this call never uses.
				inspect = auth.SessionInspectResult{Status: auth.StatusMissing, Path: auth.SessionFilePath()}
			}
```

In the `authStatus` ladder, change the `storedAPIKeyProfile()` branch to:

```go
			} else if storedAPIKeyProfile() {
				authStatus, authSource = "authenticated", "credentials.json:"+bartolocli.ActiveProfileName()
			}
```

In the `Config` map replace `"profile": auth.ActiveProfile()` with `"profile": bartolocli.ActiveProfileName()` and add `"session_host": auth.SessionHost(client.URLs.APIBaseURL)`. At line ~235 (the bug-report block) replace `auth.ActiveProfile()` with `bartolocli.ActiveProfileName()`.

In `gatewayKeyShadowsSessionCheck` replace `gatewayKey := auth.StateValueOf(auth.ActiveProfile(), "gateway_key")` with `gatewayKey := inspect.Session.GatewayKey`.

Update the `Output` map's `"default_format": "toon"` → `"table"` and add `"table"` to `supported_formats` (a stale leftover from #63; doctor should say what the binary does).

- [ ] **Step 3: Build and run the doctor tests**

Run: `go build ./cli/custom/commands/ && go test ./cli/custom/commands/ -run Doctor -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: package builds; doctor tests PASS.

- [ ] **Step 4: Commit**

```bash
git add cli/custom/commands/doctor.go cli/custom/commands/doctor_test.go
git commit -m "feat(doctor): report the profile in force or the session, never both"
```

---

### Task 9: The root PreRun — resolve, migrate, inject; delete the rest

**Files:**
- Modify: `cli/custom/register.go:29-45, 145-226, 283-372, 379-389`
- Modify: `cli/custom/server_test.go:227-end`

**Interfaces:**
- Consumes: `auth.MigrateLayout(configDir)`, `commands.ProfileServer()`.
- Deletes: `profileExemptCommands`, `rejectUnknownProfile`, `applyProfileAPIKey`; the session-host bridge.

- [ ] **Step 1: Delete `TestProfileAPIKeyBeatsTheEnvironment`** from `cli/custom/server_test.go` (it tested `applyProfileAPIKey`; bartolo ranks the profile above the environment itself since 0.8, and its own tests cover that).

- [ ] **Step 2: Rewrite the PreRun**

Replace the body of `installSessionPreRun` and its doc comment:

```go
// installSessionPreRun runs once per invocation, after cobra parsed flags and
// before the handler. It decides the server, brings ~/.orq up to date, and —
// only when no API key is in force — authenticates the generated commands as
// the login session by minting the active workspace's token into ORQ_API_KEY
// (bartolo's apikey handler reads it and adds the Bearer prefix). A profile
// or an exported key is left alone: bartolo resolves those itself.
func installSessionPreRun() {
	prev := bartolocli.PreRun
	bartolocli.PreRun = func(cmd *cobra.Command, args []string) error {
		if prev != nil {
			if err := prev(cmd, args); err != nil {
				return err
			}
		}
		repairAuthProfileType()
		applyNoColor()
		applyJSONAlias(cmd)
		commands.SetExplicitAPIKey(apiKeyConfigured())
		commands.SetUserEnvAPIKey(os.Getenv("ORQ_API_KEY"))
		if viper.GetBool("no-input") && interactiveWizardCommands[commandPath(cmd)] {
			return fmt.Errorf(
				"`%s` is an interactive wizard and --no-input/ORQ_NO_INPUT is set; "+
					"use `orq auth login` or set ORQ_API_KEY instead",
				commandPath(cmd),
			)
		}
		resolveServer(cmd)
		// Fail closed: a layout that did not migrate leaves a keyless profile
		// bartolo rejects, so continuing trades this error for a worse one.
		if err := auth.MigrateLayout(viper.GetString("config-directory")); err != nil {
			return fmt.Errorf("could not migrate ~/.orq: %w", err)
		}
		override := strings.TrimSpace(viper.GetString("workspace"))
		if override != "" && apiKeyConfigured() {
			commands.Warn("--workspace has no effect because an explicit API key (ORQ_API_KEY or a credentials profile) is configured and takes precedence")
		}
		if apiKeyConfigured() {
			return nil
		}
		session, err := auth.ReadSession()
		if err != nil || session == nil {
			return nil
		}
		if override != "" {
			client := auth.NewClient(session.APIBaseURL).WithContext(cmd.Context())
			token, err := client.WorkspaceToken(session, override)
			if err != nil {
				return fmt.Errorf("workspace %q: %w", override, err)
			}
			os.Setenv("ORQ_API_KEY", token)
			return nil
		}
		if token := activeWorkspaceToken(cmd.Context()); token != "" {
			os.Setenv("ORQ_API_KEY", token)
		}
		return nil
	}
}
```

Delete `profileExemptCommands` (and its comment), `rejectUnknownProfile`, `applyProfileAPIKey`. Keep `resolveServer`, `persistedServer`, `mirrorServerToViper`, `applyNoColor`, `apiKeyConfigured`, `activeWorkspaceToken`. Update `resolveServer`'s doc comment: drop its last paragraph ("The session's own host is layered on afterwards…"). Replace `repairAuthProfileType`:

```go
func repairAuthProfileType() {
	profile := bartolocli.ActiveProfileName()
	if profile == "" || strings.TrimSpace(bartolocli.Creds.GetString("profiles."+profile+".api_key")) == "" {
		return
	}
	stored := bartolocli.Creds.GetString("profiles." + profile + ".type")
	if _, ok := bartolocli.AuthHandlers[stored]; ok {
		return
	}
	bartolocli.Creds.Set("profiles."+profile+".type", commands.BartoloAuthType())
}
```

Fix the two stale `TODO(ENG-2902, …)` comments that mention `applyProfileAPIKey`/`mirrorServerToViper` if they still reference deleted code (`grep -n "ENG-2902" cli/custom/register.go`).

- [ ] **Step 3: Whole-tree build, vet, tests**

Run: `go build ./... && go vet ./... && go test ./cli/... 2>&1 | tail -8`
Expected: build clean; failures only in tests that set `viper.Set("profile", "default")` — Task 10.

- [ ] **Step 4: Commit**

```bash
git add cli/custom/register.go cli/custom/server_test.go
git commit -m "refactor(cli): the PreRun resolves, migrates and injects; bartolo resolves profiles"
```

---

### Task 10: Tests that pretended `default` was a session profile

**Files:**
- Modify: `cli/custom/commands/setup_test.go` (lines 387-391, 751-753, 864-865, 1068-1071, 1242-1243, 1314-1317, 1820-1821, 2046-2050, 2145, 2188, 2513-2516, 2589-2611)
- Modify: `cli/custom/launch/auth_test.go:106-107`, `cli/custom/launch/shadow_test.go:38-39, 65-66, 90-91`

`viper.Set("profile", "default")` now means "bartolo profile `default` is in force", which turns the session off. Every such line was there to name the session file; the session file is named by server now.

- [ ] **Step 1: Delete every `viper.Set("profile", "default")` and its cleanup twin** in the three files:

```bash
grep -n 'viper.Set("profile", "default")' cli/custom/commands/setup_test.go cli/custom/launch/auth_test.go cli/custom/launch/shadow_test.go
```

Remove each listed line and the matching `viper.Set("profile", "")` inside the same `t.Cleanup`. Where the cleanup closure becomes empty, delete the `t.Cleanup` call.

- [ ] **Step 2: Repoint the state assertions in `setup_test.go`**

- `:2046` and `:2188` — `stored := auth.StateOf(auth.ActiveProfile())` followed by field checks: read the session instead:

```go
	session, err := auth.ReadSession()
	if err != nil || session == nil {
		t.Fatalf("session missing after setup: %v", err)
	}
```
and assert on `session.GatewayKey`, `session.GatewayKeyID`, `session.GatewayKeyExpiresAt`, `session.GatewayWorkspace` where the old test read `stored["gateway_key"]` etc.
- `:2145` — `auth.SetStateValue("default", "gateway_key_expires_at", tc.expiresAt)`: write it on the session the test already saved:

```go
			session, _ := auth.ReadSession()
			session.GatewayKeyExpiresAt = tc.expiresAt
			if err := auth.SaveSession(session); err != nil {
				t.Fatal(err)
			}
```
- `:2513-2516`, `:2589-2611` — `storedAPIKeyProfile()` assertions stand as written when the test's premise is "an API-key login happened" (it writes bartolo profile `default`, which `GetProfile()` returns only when in force). Where a test expects `storedAPIKeyProfile()` true after `apiKeyLogin`, set `viper.Set("profile", "default")` **after** the login and clear it in cleanup — that is the one legitimate use left: asserting bartolo would pick the key up.

- [ ] **Step 3: Run the whole suite**

Run: `go test ./cli/... 2>&1 | tail -8 && cd packages/orq-rc && go build ./... && go vet ./... && cd ../..`
Expected: all `ok`.

- [ ] **Step 4: Commit**

```bash
git add cli/custom/commands/setup_test.go cli/custom/launch/auth_test.go cli/custom/launch/shadow_test.go
git commit -m "test: sessions are no longer selected by a profile name"
```

---

### Task 11: Children get `ORQ_SERVER`

**Files:**
- Modify: `cli/custom/commands/orqi.go:334-342`
- Modify: `cli/custom/commands/orqi_test.go:403` (the passthrough test)
- Modify: `cli/custom/launch/*.go` — each agent's `Env` map (`claude.go:71`, `codex.go:51`, `kimi.go:85`, `opencode.go:109`, `pi.go:76`)

- [ ] **Step 1: Extend the orqi passthrough test** — where it asserts `ORQ_PROFILE=staging` reached the child, also call `auth.SetServer("https://my.staging.orq.ai", "flag")` (with `t.Cleanup(func(){ auth.SetServer("", "default") })`) before running and assert `env["ORQ_SERVER"] == "https://my.staging.orq.ai"`.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cli/custom/commands/ -run Orqi -v 2>&1 | grep -E '^(--- |FAIL)'`
Expected: the passthrough test FAILs on `ORQ_SERVER`.

- [ ] **Step 3: Implement**

`orqi.go`, replace the env block:

```go
	// The child must land on the same login as the parent: the profile it was
	// told about, and the server that selects the session.
	env := map[string]string{}
	if f := cmd.Root().PersistentFlags().Lookup("profile"); f != nil && f.Changed {
		env["ORQ_PROFILE"] = f.Value.String()
	}
	if server := auth.Server(); server != "" {
		env["ORQ_SERVER"] = server
	}
	return runOrqiChild(path, passthrough, env)
```

For launch, find where each agent's `Env` map is built and add one entry: `"ORQ_SERVER": ctx.Creds.APIBaseURL,`. `ctx.Creds.APIBaseURL` is already the resolved host (`launch/auth.go` `ResolveCredentials`).

- [ ] **Step 4: Run the tests**

Run: `go test ./cli/custom/commands/ ./cli/custom/launch/ 2>&1 | tail -3`
Expected: `ok` for both.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/commands/orqi.go cli/custom/commands/orqi_test.go cli/custom/launch/claude.go cli/custom/launch/codex.go cli/custom/launch/kimi.go cli/custom/launch/opencode.go cli/custom/launch/pi.go
git commit -m "feat(launch): pass the resolved server to child processes"
```

---

### Task 12: Surface, docs, changelog

**Files:**
- Modify: `surface.json` (regenerated), `README.md:131-175, 244-262`, `AGENTS.md:68-80`, `CHANGELOG.md` (Unreleased)
- Modify: `docs/superpowers/specs/2026-09-02-profile-is-an-api-key-design.md` (two corrections)

- [ ] **Step 1: Surface**

Run: `go run ./cmd/surface-dump -write && git diff --stat surface.json`
Expected: only `auth list-profiles` changes (bartolo's deprecated command replaces ours; cobra hides deprecated commands from help, so it may leave the surface). Review the diff; no other command moves.

- [ ] **Step 2: README** — replace the "Authentication" intro sentence and the whole "Profiles" section:

```markdown
## Authentication

Two ways in. A browser login is an account on a server and sees every workspace
that account can; a profile is one saved API key. `--profile` selects a profile
and nothing else.

### OAuth device login (interactive)

```sh
orq auth login
```

Walks you through a browser device-authorization flow, stores the login in
`~/.orq/sessions/<host>.json` (`my.orq.ai.json` for the hosted service), and
picks an active workspace. Re-running `orq auth login` refreshes it. Sign out
with `orq auth logout`.

A second server is a second login, selected the way every other command selects
a server:

```sh
orq auth login --server https://orq.acme.internal
orq --server https://orq.acme.internal prompts list
orq server set https://orq.acme.internal     # make it the default
```

### API key (headless / CI)

```sh
export ORQ_API_KEY=sk_live_...
orq agents list
```

For several keys, save each as a profile and pick one per call, or persist the
pick:

```sh
orq auth profile add apikey ci <api-key>
orq --profile ci agents list
orq auth profile use ci
orq auth profile current
orq auth profile clear
```

While a profile is in force the login session is not consulted: `--workspace`
has no effect and `orq whoami` reports the profile. `--profile ""` turns a
persisted pick off for one call.
```

In the command table replace the `auth add-profile` / `auth list-profiles` rows with `orq auth profile add|list|current|use|clear` — "Save and select API-key profiles". Delete the `orq --profile work auth login` examples wherever they appear (`grep -n "profile" README.md`).

- [ ] **Step 3: AGENTS.md** — replace the "Who owns what in `~/.orq/credentials.json`" paragraph:

```markdown
**Who owns what under `~/.orq`.** `credentials.json` is bartolo's: `profiles.<name>`
holds a saved API key, its handler type and its server, written only through
bartolo's `auth profile add` path (`commands.saveAPIKeyProfile`). Never add a
field there. A browser login lives in `sessions/<host>.json` — host from
`auth.SessionHost`, selected by the server `custom.resolveServer` decided — and
everything this CLI records about that login (the gateway key `orq setup` minted,
its id, expiry and workspace) is a field on `auth.Session`. `auth.MigrateLayout`
brings older trees up to this on the next command. `state.go`'s dependency on
bartolo's `profile-selected` / `profile-decided` config keys moved to
`auth/migrate.go`; check them when bumping the generator.
```

- [ ] **Step 4: CHANGELOG** — in Unreleased, replace the `state` bullet from #63 and add:

```markdown
- **Breaking:** `--profile` names a saved API-key profile and nothing else. A
  browser login belongs to a server: `~/.orq/sessions/<host>.json`
  (`my.orq.ai.json` for the hosted service), chosen by `--server`, `ORQ_SERVER`
  or `orq server set`. `orq auth login --profile x` is an error; use `--server`
  for another host. While a profile is in force the session is not consulted,
  and `orq whoami` / `orq doctor` report the profile. Existing session files
  are renamed to their host on the next command; a second file for the same
  host is kept as `<name>.json.deprecated`. The gateway key `orq setup` minted
  moves from `credentials.json` into the session file, and a keyless profile
  this CLI wrote is deleted. A keyless profile with none of this CLI's fields is
  left alone.
- **Deprecated:** `auth list-profiles` and `auth add-profile` print a notice;
  use `auth profile list` and `auth profile add`. Removed in the next minor.
```

Run: `python3 scripts/stamp-changelog.py --self-test`
Expected: passes.

- [ ] **Step 5: Spec corrections** — in the spec, change the hosted example to `my.orq.ai.json`, the default server to `https://my.orq.ai`, and drop `mirrorServerToViper` from the "Deleted" list (it stays; only the session-host bridge goes).

- [ ] **Step 6: Commit**

```bash
git add surface.json README.md AGENTS.md CHANGELOG.md docs/superpowers/specs/2026-09-02-profile-is-an-api-key-design.md
git commit -m "docs: --profile is an API key, sessions are per server"
```

---

### Task 13: Live smoke test and PR

- [ ] **Step 1: Build and stage a 5.2.1-shaped HOME**

```bash
go build -o /tmp/orq-p2 ./cmd/orq
H=$(mktemp -d); mkdir -p "$H/.orq/sessions"
cat > "$H/.orq/credentials.json" <<'EOF'
{"profiles":{"default":{"api_key":"","gateway_key":"sk-orq-GW","workspace":"acme"},"staged":{"api_key":"","server":"https://staged.example"}}}
EOF
cp ~/.orq/sessions/default.json "$H/.orq/sessions/default.json"   # a real hosted login; never print it
cp ~/.orq/sessions/staging-oauth.json "$H/.orq/sessions/staging-oauth.json"
```

- [ ] **Step 2: Run and check the layout**

```bash
HOME=$H /tmp/orq-p2 --json whoami | python3 -c 'import json,sys; d=json.load(sys.stdin); print("ok" if d.get("user") else d)'
ls "$H/.orq/sessions"
python3 -c "import json;d=json.load(open('$H/.orq/sessions/my.orq.ai.json'));print({k:d.get(k) for k in ('gatewayKey','gatewayWorkspace')})"
python3 -c "import json;d=json.load(open('$H/.orq/credentials.json'));print(sorted((d.get('profiles') or {}).keys()), 'state' in d)"
```

Expected: `ok`; `my.orq.ai.json my.staging.orq.ai.json`; `{'gatewayKey': 'sk-orq-GW', 'gatewayWorkspace': 'acme'}`; `['staged'] False`.

- [ ] **Step 3: Profile semantics**

```bash
HOME=$H /tmp/orq-p2 --profile nope agents list --limit 1 2>&1 | tail -1     # bartolo: profile "nope" is not configured
HOME=$H /tmp/orq-p2 --server https://my.staging.orq.ai whoami 2>&1 | head -1  # the staging login
HOME=$H /tmp/orq-p2 auth login --profile x 2>&1 | tail -1                     # profile "x" is an API key, not a login
```

- [ ] **Step 4: Full verification**

```bash
go test ./... && go vet ./... && gofmt -l $(git ls-files '*.go') && go run ./cmd/surface-dump -check
(cd packages/orq-rc && go build ./... && go vet ./...)
rm -rf "$H"
```

- [ ] **Step 5: Push and un-draft**

```bash
git push -u origin Baukebrenninkmeijer/profile-is-an-api-key
gh pr ready 65
```

Update the PR #65 body's "Status" line to point at this plan and list the smoke-test results. Post to RES-1500 (orqi) that the host rule is implemented and the file names are final.
