package auth

import (
	"fmt"
	"io"
	"os"
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
func (s *memStore) Name() string                           { return "test store" }

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
func (s *flakyStore) Name() string             { return "flaky store" }

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
// plaintext refresh token as the secure store. Pin all three states: file mode,
// a store available but the file still holds inline secrets (degraded), and a
// store with a secret-free file.
func TestSecretsLocationReportsHonestly(t *testing.T) {
	isolateHome(t)

	fileEnv := func(k string) string {
		if k == CredentialStoreEnv {
			return "file"
		}
		return ""
	}
	if desc, degraded := SecretsLocation(fileEnv); degraded || !strings.Contains(desc, "plaintext") {
		t.Fatalf("file mode: desc=%q degraded=%v, want plaintext and not degraded", desc, degraded)
	}

	store := ResolveSecretStore(os.Getenv)
	if store == nil {
		t.Skip("no OS credential store on this machine to exercise the store-backed states")
	}
	// Write files directly in file mode so nothing touches the real keychain.
	withStore(t, nil)

	if err := saveSessionTo(SessionFilePath(), validSession("prod")); err != nil {
		t.Fatalf("write inline file: %v", err)
	}
	if desc, degraded := SecretsLocation(os.Getenv); !degraded {
		t.Fatalf("inline file with a store available: desc=%q degraded=false, want degraded", desc)
	}

	secretFree := validSession("prod")
	secretFree.stripSecrets()
	if err := saveSessionTo(SessionFilePath(), secretFree); err != nil {
		t.Fatalf("write secret-free file: %v", err)
	}
	if desc, degraded := SecretsLocation(os.Getenv); degraded || desc != store.Name() {
		t.Fatalf("secret-free file: desc=%q degraded=%v, want %q not degraded", desc, degraded, store.Name())
	}
}

func withStore(t *testing.T, st SecretStore) {
	t.Helper()
	prev := activeStore
	activeStore = func() SecretStore { return st }
	t.Cleanup(func() { activeStore = prev })
}

func sessionFileText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(SessionFilePath())
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
