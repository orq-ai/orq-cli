package custom

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// MigrateLayout runs in the PreRun of every command, so anything it treats as
// fatal takes the whole CLI down — including the commands someone with a
// broken ~/.orq would reach for. A stray unreadable file in sessions/ is the
// cheapest way to get there, so prove the binary still runs with one.
func TestACorruptSessionFileDoesNotStopEveryCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	sessions := filepath.Join(home, ".orq", "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "my.orq.ai.json"), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := buildRoot(t)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("an unreadable session file stopped `orq version`: %v", err)
	}
}
