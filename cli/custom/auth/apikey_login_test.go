package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// An api-key login round-trips through the host-keyed store and comes back with
// its key and provenance intact. This is the credential `orq auth login
// --api-key` writes; before this fix the key went to an unselected bartolo
// profile that nothing resolved, so no command could read it back.
func TestAPIKeyLoginRoundTrip(t *testing.T) {
	isolateHome(t)

	in := &APIKeyLogin{
		APIBaseURL: "https://api.example",
		APIKey:     "sk-orq-LOGIN",
		Workspaces: []map[string]any{{"key": "acme"}},
	}
	if err := SaveAPIKeyLogin(in); err != nil {
		t.Fatalf("SaveAPIKeyLogin: %v", err)
	}

	got, err := ReadAPIKeyLogin()
	if err != nil {
		t.Fatalf("ReadAPIKeyLogin: %v", err)
	}
	if got == nil {
		t.Fatal("stored api-key login could not be read back")
	}
	if got.APIKey != "sk-orq-LOGIN" {
		t.Errorf("api key = %q, want sk-orq-LOGIN", got.APIKey)
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1 (stamped on write)", got.Version)
	}
	if got.Source != APIKeyLoginSource {
		t.Errorf("source = %q, want %q", got.Source, APIKeyLoginSource)
	}

	// Secret-bearing, so 0600 like the session and credentials files.
	info, err := os.Stat(APIKeyLoginFilePath())
	if err != nil {
		t.Fatalf("stat api-key login: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("api-key login file perm = %o, want 600", perm)
	}
}

func TestClearAPIKeyLoginRemovesIt(t *testing.T) {
	isolateHome(t)
	if err := SaveAPIKeyLogin(&APIKeyLogin{APIKey: "sk-orq-LOGIN"}); err != nil {
		t.Fatalf("SaveAPIKeyLogin: %v", err)
	}
	if err := ClearAPIKeyLogin(); err != nil {
		t.Fatalf("ClearAPIKeyLogin: %v", err)
	}
	got, err := ReadAPIKeyLogin()
	if err != nil {
		t.Fatalf("ReadAPIKeyLogin after clear: %v", err)
	}
	if got != nil {
		t.Error("api-key login survived logout")
	}
	// Clearing an already-absent login is success, not an error.
	if err := ClearAPIKeyLogin(); err != nil {
		t.Errorf("ClearAPIKeyLogin on missing file: %v", err)
	}
}

// A file with no key is a corrupt credential, surfaced as an error rather than
// silently treated as "not logged in".
func TestReadAPIKeyLoginRejectsKeylessFile(t *testing.T) {
	isolateHome(t)
	dir := sessionsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(APIKeyLoginFilePath(), []byte(`{"version":1,"apiKey":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAPIKeyLogin(); err == nil {
		t.Error("a keyless api-key login file must be an error, not a silent nil")
	}
	// Sanity: the file really is where the accessor looks.
	if _, err := os.Stat(filepath.Join(dir, filepath.Base(APIKeyLoginFilePath()))); err != nil {
		t.Fatalf("api-key login file not where expected: %v", err)
	}
}

// A file stamped with a version this build does not write is not one it can
// trust to read: ReadAPIKeyLogin rejects it as an error, matching validateSession's
// version gate, rather than authenticating off a shape that may have moved.
func TestReadAPIKeyLoginRejectsUnsupportedVersion(t *testing.T) {
	isolateHome(t)
	dir := sessionsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(APIKeyLoginFilePath(), []byte(`{"version":2,"apiKey":"sk-orq-LOGIN"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAPIKeyLogin(); err == nil {
		t.Error("an api-key login with an unsupported version must be an error, not a silent read")
	}
}
