package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// Both tests moved here with the function itself, from the deleted state.go.
// WriteSecretFile writes credentials.json and config.json on the migration
// path, so a lost Chmod leaves a world-readable credential store — invisible
// on a machine whose umask happens to hide it.
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
