package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The migrate-failure and save-failure notices each hold to once per process, but
// through their OWN sync.Once: one firing must not silence the other. With a single
// shared Once, the migrate warning below would be swallowed by the earlier save one.
func TestStoreFailureWarningsAreIndependent(t *testing.T) {
	migrateFailureWarned = sync.Once{}
	saveFailureWarned = sync.Once{}
	t.Cleanup(func() { migrateFailureWarned = sync.Once{}; saveFailureWarned = sync.Once{} })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	warnStoreFailureOnce(&saveFailureWarned, "SAVE_WARN\n")
	warnStoreFailureOnce(&saveFailureWarned, "SAVE_WARN_AGAIN\n") // same Once: suppressed
	warnStoreFailureOnce(&migrateFailureWarned, "MIGRATE_WARN\n") // different Once: must appear
	os.Stderr = prev
	w.Close()
	out, _ := io.ReadAll(r)
	s := string(out)

	if !strings.Contains(s, "SAVE_WARN\n") {
		t.Errorf("save warning missing: %q", s)
	}
	if strings.Contains(s, "SAVE_WARN_AGAIN") {
		t.Errorf("save warning fired twice; the Once did not dedupe: %q", s)
	}
	if !strings.Contains(s, "MIGRATE_WARN") {
		t.Errorf("migrate warning was suppressed by the save Once; the two are not independent: %q", s)
	}
}

// memStore is an in-memory SecretStore for the session read/write tests, so the
// split/migrate logic is exercised without a real keychain.
type memStore struct{ m map[string]string }

func newMemStore() *memStore { return &memStore{m: map[string]string{}} }

func (s *memStore) Load(host string) (string, bool, error) { v, ok := s.m[host]; return v, ok, nil }
func (s *memStore) Save(host, secret string) error         { s.m[host] = secret; return nil }
func (s *memStore) Delete(host string) error               { delete(s.m, host); return nil }
func (s *memStore) ID() string                             { return "test" }
func (s *memStore) Name() string                           { return "test store" }
func (s *memStore) ClearHint(string) string                { return "" }

// flakyStore can be made to fail Save or Load, to exercise the inline fallback
// (Save failure keeps the login working) and the error path (Load failure must
// not read as a blank session).
type flakyStore struct {
	m        map[string]string
	failSave bool
	failLoad bool
}

func (s *flakyStore) Load(host string) (string, bool, error) {
	if s.failLoad {
		return "", false, fmt.Errorf("credential store unreachable")
	}
	v, ok := s.m[host]
	return v, ok, nil
}

func (s *flakyStore) Save(host, secret string) error {
	if s.failSave {
		return fmt.Errorf("credential store write failed")
	}
	s.m[host] = secret
	return nil
}

func (s *flakyStore) Delete(host string) error { delete(s.m, host); return nil }
func (s *flakyStore) ID() string               { return "flaky" }
func (s *flakyStore) Name() string             { return "flaky store" }
func (s *flakyStore) ClearHint(string) string  { return "" }

func TestSaveFallsBackToInlineWhenStoreFails(t *testing.T) {
	isolateHome(t)
	withStore(t, &flakyStore{m: map[string]string{}, failSave: true})

	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession should still succeed via the inline fallback: %v", err)
	}
	// The login must be preserved: secrets written inline rather than lost.
	if !strings.Contains(sessionFileText(t), "refresh-abc") {
		t.Fatal("store write failed but the refresh token was not written inline; login would be lost")
	}
	// And it still reads back (migration retries, fails, keeps the inline secrets).
	got, err := ReadSession()
	if err != nil || got == nil || got.RefreshToken != "refresh-abc" {
		t.Fatalf("ReadSession after fallback: err=%v session=%+v", err, got)
	}
}

func TestReadSurfacesStoreLoadErrorNotMissingToken(t *testing.T) {
	isolateHome(t)
	fs := &flakyStore{m: map[string]string{}}
	withStore(t, fs)

	// Save with a working store: the file becomes secret-free, secret in store.
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if strings.Contains(sessionFileText(t), "refresh-abc") {
		t.Fatal("precondition: the file should be secret-free when the store works")
	}

	// Now the store cannot answer: the read must error, not report a blank login.
	fs.failLoad = true
	if _, err := ReadSession(); err == nil {
		t.Fatal("a store Load failure must surface as an error, not a usable-looking blank session")
	} else if strings.Contains(err.Error(), "missing refresh token") {
		t.Fatalf("Load failure was reported as a missing token: %v", err)
	}
}

// The two notices must fire from the real call sites, not just the helper: the
// migrate path (hydrateSecrets on read) and the save path (saveSessionTo on
// write), each with a failing store, in one process. A failed migration must
// also leave the file untouched with its inline secrets.
func TestStoreFailureWarningsFireFromBothCallSites(t *testing.T) {
	isolateHome(t)
	migrateFailureWarned = sync.Once{}
	saveFailureWarned = sync.Once{}
	t.Cleanup(func() { migrateFailureWarned = sync.Once{}; saveFailureWarned = sync.Once{} })

	// A pre-store (inline) file, written in file mode.
	withStore(t, nil)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession (file mode): %v", err)
	}
	if !strings.Contains(sessionFileText(t), "refresh-abc") {
		t.Fatal("precondition: file-mode write should leave the token inline")
	}

	// Now a store that fails every write is active.
	withStore(t, &flakyStore{m: map[string]string{}, failSave: true})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w

	// Migrate path: the read hydrates, tries to migrate, the store write fails.
	if _, err := ReadSession(); err != nil {
		os.Stderr = prev
		t.Fatalf("ReadSession (migrate path): %v", err)
	}
	// Save path: the store write fails, secrets fall back to inline.
	if err := SaveSession(validSession("prod")); err != nil {
		os.Stderr = prev
		t.Fatalf("SaveSession (save path): %v", err)
	}
	os.Stderr = prev
	w.Close()
	out, _ := io.ReadAll(r)
	s := string(out)

	if !strings.Contains(s, "could not move") {
		t.Errorf("migrate-path warning missing from stderr: %q", s)
	}
	if !strings.Contains(s, "in plaintext") {
		t.Errorf("save-path warning missing from stderr: %q", s)
	}
	// The failed migration must not have stripped the file.
	if !strings.Contains(sessionFileText(t), "refresh-abc") {
		t.Error("failed migration did not keep the inline secrets in the file")
	}
}

// SecretsLocation is what `orq doctor` reads, so a regression here would report a
// plaintext refresh token as the secure store. Pin the states: file mode, a store
// available but a file still holding inline secrets (degraded, named), and a
// store with every file secret-free.
func TestSecretsLocationReportsHonestly(t *testing.T) {
	isolateHome(t)
	t.Setenv(CredentialStoreEnv, "file")

	withStore(t, nil)
	if desc, degraded := SecretsLocation(); degraded || !strings.Contains(desc, "plaintext") {
		t.Fatalf("file mode: desc=%q degraded=%v, want plaintext and not degraded", desc, degraded)
	}
	// Written in file mode, so the active host's file holds inline tokens.
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}

	mem := newMemStore()
	withStore(t, mem)
	desc, degraded := SecretsLocation()
	if !degraded || !strings.Contains(desc, filepath.Base(SessionFilePath())) {
		t.Fatalf("inline file with a store available: desc=%q degraded=%v, want degraded naming the file", desc, degraded)
	}

	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	if desc, degraded := SecretsLocation(); degraded || desc != mem.Name() {
		t.Fatalf("secret-free file: desc=%q degraded=%v, want %q not degraded", desc, degraded, mem.Name())
	}

	// Another host's file that never migrated is named too, as is a .deprecated copy.
	other := validSession("prod")
	other.APIBaseURL = "https://staging.example"
	raw, _ := json.Marshal(other)
	for _, name := range []string{"staging.example.json", "x.json.deprecated"} {
		if err := os.WriteFile(filepath.Join(SessionsDir(), name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	desc, degraded = SecretsLocation()
	if !degraded || !strings.Contains(desc, "staging.example.json") || !strings.Contains(desc, "x.json.deprecated") {
		t.Fatalf("plaintext in other files: desc=%q degraded=%v, want both named", desc, degraded)
	}
}

func withStore(t *testing.T, st SecretStore) {
	t.Helper()
	prev := activeStore
	activeStore = func() SecretStore { return st }
	t.Cleanup(func() { activeStore = prev })
}

// When logout's best-effort store Delete fails (an unreachable or locked store),
// ClearSession must not fail the logout, and it must print the exact manual
// command the user can run to remove the orphaned secret by hand.
func TestClearSessionPrintsManualClearHintOnDeleteFailure(t *testing.T) {
	isolateHome(t)
	withStore(t, macKeychainStore{})
	prev := runSecretTool
	t.Cleanup(func() { runSecretTool = prev })
	runSecretTool = func(_, _ string, _ ...string) (string, string, error) {
		return "", "User interaction is not allowed.", fakeExit{51} // locked keychain
	}
	host := hostFromSessionPath(SessionFilePath())

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevStderr := os.Stderr
	os.Stderr = w
	clearErr := ClearSession()
	os.Stderr = prevStderr
	w.Close()
	out, _ := io.ReadAll(r)

	if clearErr != nil {
		t.Fatalf("ClearSession must not fail on a store delete error: %v", clearErr)
	}
	// The hint must reach the continuation chunks too, not just chunk 0.
	for _, want := range []string{
		"security delete-generic-password -s " + keychainService + " -a " + host + ";",
		"-s " + keychainContService + " -a " + host + ".$i",
	} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("logout did not print the manual clear command.\nstderr: %q\nwant substring: %q", out, want)
		}
	}
}

func sessionFileText(t *testing.T) string {
	t.Helper()
	return sessionText(t, SessionFilePath())
}

func sessionText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	return string(data)
}

func TestStoreKeepsSessionFileSecretFree(t *testing.T) {
	isolateHome(t)
	mem := newMemStore()
	withStore(t, mem)

	in := validSession("prod")
	in.GatewayKey = "sk-gateway-secret"
	in.WorkspaceTokens["prod:proj"] = StoredAccessToken{Token: "ws-token-secret", ExpiresAt: "2099-01-01T00:00:00Z", WorkspaceID: "ws1"}
	if err := SaveSession(in); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	file := sessionFileText(t)
	for _, secret := range []string{"refresh-abc", "sk-gateway-secret", "ws-token-secret", `"boot"`} {
		if strings.Contains(file, secret) {
			t.Errorf("secret %q leaked into the session file", secret)
		}
	}
	// Non-secret state is still in the file.
	for _, want := range []string{"api.example", "ws1", "2099-01-01", `"prod:proj"`} {
		if !strings.Contains(file, want) {
			t.Errorf("non-secret field %q missing from the session file", want)
		}
	}
	host := hostFromSessionPath(SessionFilePath())
	if _, ok := mem.m[host]; !ok {
		t.Fatalf("store has no entry for host %q", host)
	}

	got, err := ReadSession()
	if err != nil || got == nil {
		t.Fatalf("ReadSession: %v (session %v)", err, got)
	}
	if got.RefreshToken != "refresh-abc" || got.BootstrapToken.Token != "boot" || got.GatewayKey != "sk-gateway-secret" {
		t.Fatalf("hydrated session missing secrets: refresh=%q boot=%q gw=%q", got.RefreshToken, got.BootstrapToken.Token, got.GatewayKey)
	}
	wt := got.WorkspaceTokens["prod:proj"]
	if wt.Token != "ws-token-secret" || wt.WorkspaceID != "ws1" || wt.ExpiresAt != "2099-01-01T00:00:00Z" {
		t.Fatalf("workspace token not round-tripped: %+v", wt)
	}
}

func TestMigrateOnReadMovesSecretsIntoStore(t *testing.T) {
	isolateHome(t)

	// Write a pre-store file: activeStore defaults to nil (file mode) here.
	in := validSession("prod")
	in.WorkspaceTokens["prod:proj"] = StoredAccessToken{Token: "ws-token-secret", ExpiresAt: "2099-01-01T00:00:00Z"}
	if err := SaveSession(in); err != nil {
		t.Fatalf("SaveSession (file mode): %v", err)
	}
	if !strings.Contains(sessionFileText(t), "refresh-abc") {
		t.Fatal("file-mode write should have left the refresh token inline")
	}

	// Now a store is available: the next read migrates.
	mem := newMemStore()
	withStore(t, mem)

	got, err := ReadSession()
	if err != nil || got == nil {
		t.Fatalf("ReadSession (migrating): %v", err)
	}
	if got.RefreshToken != "refresh-abc" || got.WorkspaceTokens["prod:proj"].Token != "ws-token-secret" {
		t.Fatalf("migrated session lost secrets: %+v", got)
	}
	// File rewritten without secrets, store now holds them.
	if strings.Contains(sessionFileText(t), "refresh-abc") {
		t.Error("migration did not strip the refresh token from the file")
	}
	host := hostFromSessionPath(SessionFilePath())
	if _, ok := mem.m[host]; !ok {
		t.Error("migration did not write the store")
	}

	// Second read takes the load path (no inline secrets) and still works.
	again, err := ReadSession()
	if err != nil || again == nil || again.RefreshToken != "refresh-abc" {
		t.Fatalf("second read after migration: %v / %+v", err, again)
	}
}

func TestClearSessionDeletesStoreEntry(t *testing.T) {
	isolateHome(t)
	mem := newMemStore()
	withStore(t, mem)

	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	host := hostFromSessionPath(SessionFilePath())
	if _, ok := mem.m[host]; !ok {
		t.Fatal("precondition: store should hold the secret")
	}
	if err := ClearSession(); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	if _, ok := mem.m[host]; ok {
		t.Error("ClearSession left the secret in the store")
	}
	if _, err := os.Stat(SessionFilePath()); !os.IsNotExist(err) {
		t.Error("ClearSession left the session file")
	}
}

func TestStoreBackedSessionListsAsValid(t *testing.T) {
	isolateHome(t)
	mem := newMemStore()
	withStore(t, mem)

	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	// The file is secret-free; ListSessions must still report it ok, reading the
	// bootstrap expiry metadata that stays in the file.
	rows, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != SessionStatusOK {
		t.Fatalf("want one ok session, got %+v", rows)
	}
}

// The manual-clear hint is a real shell command: run it against the fake
// keychain's semantics by checking it names every chunk a split secret uses.
func TestMacClearHintCoversEveryChunk(t *testing.T) {
	fk := withFakeKeychain(t, 44)
	const host = "api.orq.ai"
	if err := (macKeychainStore{}).Save(host, strings.Repeat("q", 12000)); err != nil {
		t.Fatal(err)
	}
	hint := (macKeychainStore{}).ClearHint(host)
	for acct := range fk.items {
		if acct == host {
			continue
		}
		if !strings.HasPrefix(acct, host+".") || !strings.Contains(hint, keychainContService+" -a "+host+".$i") {
			t.Fatalf("hint %q does not reach continuation item %q", hint, acct)
		}
	}
}

// A file whose tokens moved to the store, read by a process that resolves no
// store (ORQ_CREDENTIAL_STORE=file, or SSH with no Secret Service), must say
// where the secrets are rather than report a missing refresh token.
func TestMigratedSessionInFileModeSaysWhereTheSecretsAre(t *testing.T) {
	for _, tc := range []struct {
		name, forced, reason, remedy string
	}{
		{"forced file", "file", CredentialStoreEnv + "=file is set", "unset " + CredentialStoreEnv},
		{"store unreachable", "", "reachable from this process", "make the store reachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateHome(t)
			withStore(t, newMemStore())
			if err := SaveSession(validSession("prod")); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(sessionFileText(t), `"secretStore": "test"`) {
				t.Fatalf("stripped file does not record its store's id: %s", sessionFileText(t))
			}

			withStore(t, nil)
			t.Setenv(CredentialStoreEnv, tc.forced)
			_, err := ReadSession()
			if err == nil {
				t.Fatal("a migrated session read without its store must error")
			}
			for _, want := range []string{tc.reason, tc.remedy, "orq auth login"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "missing refresh token") {
				t.Errorf("reported as a missing token: %v", err)
			}
		})
	}
}

// A file whose marker names another machine's store (a home directory synced
// from a Mac to Linux) says where the secrets are, not that they were removed.
func TestSessionFromAnotherStoreSaysWhichOne(t *testing.T) {
	isolateHome(t)
	withStore(t, newMemStore())
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	withStore(t, &flakyStore{})
	_, err := ReadSession()
	if err == nil || !strings.Contains(err.Error(), "but this machine uses the flaky store") {
		t.Fatalf("err = %v, want it to name both stores", err)
	}
}

// A file that says its tokens are in the store, where the store holds nothing,
// is a login removed outside orq: say so and point at login.
func TestMigratedSessionWithEmptyStoreSaysSo(t *testing.T) {
	isolateHome(t)
	mem := newMemStore()
	withStore(t, mem)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	mem.m = map[string]string{}
	_, err := ReadSession()
	if err == nil || !strings.Contains(err.Error(), "holds no session secrets") {
		t.Fatalf("want a store-is-empty error, got %v", err)
	}
}

// Saving in file mode, or falling back after a failed store write, must drop the
// marker: the tokens are inline again.
func TestInlineSaveClearsTheStoreMarker(t *testing.T) {
	isolateHome(t)
	withStore(t, newMemStore())
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	withStore(t, &flakyStore{m: map[string]string{}, failSave: true})
	saveFailureWarned = sync.Once{}
	t.Cleanup(func() { saveFailureWarned = sync.Once{} })
	if err := SaveSession(got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sessionFileText(t), "secretStore") {
		t.Fatalf("inline fallback kept the store marker: %s", sessionFileText(t))
	}
}

// Every host's file migrates, not just the active one: a login to a server no
// longer in use must not keep its refresh token in plaintext indefinitely.
func TestMigrateSessionSecretsSweepsEveryHost(t *testing.T) {
	isolateHome(t)
	withStore(t, nil)
	// The sweep only takes files named for their own host, as MigrateLayout
	// leaves them, so the active file's API base must match its name.
	if err := SaveSession(sessionOn(DefaultAPIBaseURL, "prod")); err != nil {
		t.Fatal(err)
	}
	misnamed := filepath.Join(SessionsDir(), "default.json")
	if err := saveSessionTo(misnamed, validSession("prod")); err != nil {
		t.Fatal(err)
	}
	other := validSession("prod")
	other.APIBaseURL = "https://staging.example"
	other.RefreshToken = "refresh-staging"
	otherPath := sessionPathFor(SessionHost(other.APIBaseURL))
	if err := saveSessionTo(otherPath, other); err != nil {
		t.Fatal(err)
	}

	mem := newMemStore()
	withStore(t, mem)
	MigrateSessionSecrets()

	for _, path := range []string{SessionFilePath(), otherPath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "refresh-") {
			t.Errorf("%s still holds a refresh token after the sweep", filepath.Base(path))
		}
		if _, ok := mem.m[hostFromSessionPath(path)]; !ok {
			t.Errorf("store has no entry for %s", hostFromSessionPath(path))
		}
	}
	if !strings.Contains(sessionText(t, misnamed), "refresh-abc") {
		t.Error("a file not named for its host was migrated; renaming it later would orphan its store entry")
	}
	if !strings.Contains(mem.m[hostFromSessionPath(otherPath)], "refresh-staging") {
		t.Errorf("staging secrets not stored under its own host: %q", mem.m[hostFromSessionPath(otherPath)])
	}
}

// A login whose secrets are in a store this process cannot use fails every
// command, so the listing must not call it ok.
func TestStoredSessionListsAsUnreadableWithoutItsStore(t *testing.T) {
	isolateHome(t)
	withStore(t, newMemStore())
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	withStore(t, nil)
	rows, err := ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != SessionStatusUnreadable {
		t.Fatalf("want one unreadable session, got %+v", rows)
	}
}

// A signed-out mark is merged into the current login by its refresh token. For
// a stored login the file has none, so the merge must hydrate first, and the
// write must keep the stored secrets.
func TestSignedOutMarkPersistsOnAStoredLogin(t *testing.T) {
	isolateHome(t)
	mem := newMemStore()
	withStore(t, mem)
	// Named for its own host, as markSessionSignedOut addresses it.
	if err := SaveSession(sessionOn(DefaultAPIBaseURL, "prod")); err != nil {
		t.Fatal(err)
	}
	session, err := ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	markSessionSignedOut(session)
	if !strings.Contains(sessionFileText(t), `"signedOut": true`) {
		t.Fatalf("signed-out mark not written: %s", sessionFileText(t))
	}
	if !strings.Contains(mem.m[hostFromSessionPath(SessionFilePath())], "refresh-abc") {
		t.Fatalf("the merge dropped the stored refresh token: %q", mem.m[hostFromSessionPath(SessionFilePath())])
	}
}

// A save whose store write landed but whose file write failed leaves the
// store one save ahead of the file. Reading must refuse the pair rather than
// put the newer login's tokens on the older file's account and workspace.
func TestReadRefusesAStoreAndFileFromDifferentSaves(t *testing.T) {
	isolateHome(t)
	mem := newMemStore()
	withStore(t, mem)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	host := hostFromSessionPath(SessionFilePath())
	mem.m[host] = `{"refreshToken":"refresh-other-account","generation":"0000000000000000"}`
	_, err := ReadSession()
	if err == nil || !strings.Contains(err.Error(), "different saves") {
		t.Fatalf("err = %v, want the mismatch reported", err)
	}
}

// A store that refuses the write stops the sweep: on macOS a refusal can be
// the user clicking Deny, which must not come back once per host.
func TestMigrateSessionSecretsStopsAfterARefusedWrite(t *testing.T) {
	isolateHome(t)
	withStore(t, nil)
	for _, base := range []string{DefaultAPIBaseURL, "https://staging.example"} {
		s := sessionOn(base, "prod")
		if err := saveSessionTo(sessionPathFor(SessionHost(base)), s); err != nil {
			t.Fatal(err)
		}
	}
	refusing := &countingStore{flakyStore: flakyStore{m: map[string]string{}, failSave: true}}
	withStore(t, refusing)
	captureStderr(t, MigrateSessionSecrets)
	if refusing.saves != 1 {
		t.Fatalf("store asked %d times, want once before the sweep stops", refusing.saves)
	}
}

type countingStore struct {
	flakyStore
	saves int
}

func (s *countingStore) Save(host, secret string) error {
	s.saves++
	return s.flakyStore.Save(host, secret)
}

// hydrateSecrets re-reads the file under the lock. A session read inline before
// another process migrated and then saved newer secrets must end up with the
// newer ones, not migrate its stale copy over them.
func TestHydrateUsesTheFileAsOfTheLock(t *testing.T) {
	isolateHome(t)
	withStore(t, nil)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	stale, err := readSessionFile(SessionFilePath())
	if err != nil {
		t.Fatal(err)
	}
	mem := newMemStore()
	withStore(t, mem)
	newer := validSession("prod")
	newer.RefreshToken = "refresh-rotated"
	if err := SaveSession(newer); err != nil {
		t.Fatal(err)
	}
	if err := hydrateSecrets(stale, SessionFilePath()); err != nil {
		t.Fatal(err)
	}
	if stale.RefreshToken != "refresh-rotated" || !strings.Contains(mem.m[hostFromSessionPath(SessionFilePath())], "refresh-rotated") {
		t.Fatalf("hydrated %q, store %q; want the rotated token in both", stale.RefreshToken, mem.m[hostFromSessionPath(SessionFilePath())])
	}
}

// attachToSession adds legacy gateway fields to a host's session and saves it.
// On a file whose tokens are already in the store it must hydrate first, or the
// save replaces the stored refresh and bootstrap tokens with a gateway-key-only
// blob and the login is gone.
func TestAttachToSessionKeepsStoredTokens(t *testing.T) {
	dir := layoutHarness(t, `{"profiles":{"default":{
		"api_key":"",
		"gateway_key":"sk-orq-GW",
		"gateway_key_id":"KEYID",
		"workspace":"acme"
	}}}`, nil)
	mem := newMemStore()
	withStore(t, mem)
	path := sessionPathFor(SessionHost("https://api.orq.ai"))
	if err := saveSessionTo(path, sessionOn("https://api.orq.ai", "acme")); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLayout(dir); err != nil {
		t.Fatal(err)
	}
	blob := mem.m[hostFromSessionPath(path)]
	for _, want := range []string{"refresh-abc", `"bootstrapToken":"boot"`, "sk-orq-GW"} {
		if !strings.Contains(blob, want) {
			t.Errorf("stored secrets lost %q after attaching the gateway key: %s", want, blob)
		}
	}
}

// lockedKeychain makes each fake command atomic, as the real keychain is per
// item, so the race left is the one between the chunks of one Save.
func lockedKeychain(t *testing.T) *fakeKeychain {
	fk := withFakeKeychain(t, 44)
	var mu sync.Mutex
	runSecretTool = func(stdin, name string, args ...string) (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		return fk.run(stdin, name, args...)
	}
	return fk
}

// Concurrent saves and reads of a multi-chunk secret: without the store lock a
// reader joins chunks of two writes, and two writers interleave chunks and leave
// a value that never decodes. Every read must succeed and see one whole write.
func TestConcurrentChunkedSavesAndReadsNeverTear(t *testing.T) {
	isolateHome(t)
	lockedKeychain(t)
	withStore(t, macKeychainStore{})

	session := func(n int) *Session {
		s := validSession("prod")
		s.RefreshToken = fmt.Sprintf("refresh-%d-%s", n, strings.Repeat(fmt.Sprint(n%10), 9000))
		return s
	}
	if err := SaveSession(session(0)); err != nil {
		t.Fatal(err)
	}
	var writers, readers sync.WaitGroup
	done := make(chan struct{})
	errs := make(chan error, 1000)
	for w := 0; w < 4; w++ {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			for i := 0; i < 5; i++ {
				if err := SaveSession(session(w*100 + i)); err != nil {
					errs <- err
				}
			}
		}(w)
		readers.Add(1)
		// Readers spin for the whole write window, so a read lands mid-Save.
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				s, err := ReadSession()
				if err != nil {
					select {
					case errs <- err:
					default:
					}
					continue
				}
				// One whole write: the prefix number matches the repeated digit.
				var n int
				var body string
				if _, err := fmt.Sscanf(s.RefreshToken, "refresh-%d-%s", &n, &body); err != nil ||
					body != strings.Repeat(fmt.Sprint(n%10), 9000) {
					select {
					case errs <- fmt.Errorf("read a torn refresh token (len %d)", len(s.RefreshToken)):
					default:
					}
				}
			}
		}()
	}
	writers.Wait()
	close(done)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// MigrateLayout runs before every command. When a legacy gateway key must be
// attached to a session whose secrets sit in a store this process cannot read,
// it must leave credentials.json alone and let the command run, not fail every
// command including `orq auth login` and `orq doctor`.
func TestMigrateLayoutDefersWhenSessionSecretsAreUnreachable(t *testing.T) {
	dir := layoutHarness(t, `{"profiles":{"default":{"api_key":"","gateway_key":"sk-orq-GW","workspace":"acme"}}}`, nil)
	withStore(t, newMemStore())
	path := sessionPathFor(SessionHost(DefaultAPIBaseURL))
	if err := saveSessionTo(path, sessionOn(DefaultAPIBaseURL, "acme")); err != nil {
		t.Fatal(err)
	}
	withStore(t, nil)
	t.Setenv(CredentialStoreEnv, "file")
	if err := MigrateLayout(dir); err != nil {
		t.Fatalf("MigrateLayout must not fail the command: %v", err)
	}
	if !strings.Contains(sessionText(t, filepath.Join(dir, "credentials.json")), "sk-orq-GW") {
		t.Fatal("the gateway key's only copy was dropped from credentials.json")
	}
}

// Replacing or removing a file whose secrets are in a store this process is
// not using names what is left behind and how to remove it.
func TestFileModeNamesOrphanedStoreSecrets(t *testing.T) {
	isolateHome(t)
	withStore(t, macKeychainStore{})
	withFakeKeychain(t, 44)
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	withStore(t, nil)
	host := hostFromSessionPath(SessionFilePath())
	want := "security delete-generic-password -s " + keychainService + " -a " + host

	stderr := captureStderr(t, func() {
		if err := SaveSession(validSession("prod")); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, want) {
		t.Fatalf("file-mode login over a stored session did not name the leftover: %q", stderr)
	}

	withStore(t, macKeychainStore{})
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	withStore(t, nil)
	stderr = captureStderr(t, func() {
		if err := ClearSession(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, want) {
		t.Fatalf("file-mode logout of a stored session did not name the leftover: %q", stderr)
	}
}

// With no store resolved, doctor flags a login whose secrets are in a store.
func TestSecretsLocationFlagsStoredSessionsInFileMode(t *testing.T) {
	isolateHome(t)
	withStore(t, newMemStore())
	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatal(err)
	}
	withStore(t, nil)
	t.Setenv(CredentialStoreEnv, "file")
	desc, degraded := SecretsLocation()
	if !degraded || !strings.Contains(desc, filepath.Base(SessionFilePath())) {
		t.Fatalf("desc=%q degraded=%v, want degraded naming the stored session", desc, degraded)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = prev }()
	fn()
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}
