package auth

import (
	"os"
	"strings"
	"testing"
)

// memStore is an in-memory SecretStore for the session read/write tests, so the
// split/migrate logic is exercised without a real keychain.
type memStore struct{ m map[string]string }

func newMemStore() *memStore { return &memStore{m: map[string]string{}} }

func (s *memStore) Load(host string) (string, bool, error) { v, ok := s.m[host]; return v, ok, nil }
func (s *memStore) Save(host, secret string) error         { s.m[host] = secret; return nil }
func (s *memStore) Delete(host string) error               { delete(s.m, host); return nil }
func (s *memStore) Name() string                           { return "test store" }

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
