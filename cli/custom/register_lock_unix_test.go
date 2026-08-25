//go:build !windows

package custom

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"orq/cli/custom/skills"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// A contended manifest lock must not turn a skills command into a
// multi-second stall: skills.Refresh and skills.SweepDeadSessions each wait
// out lock.go's lockTimeout on their own, and calling both unconditionally
// would make a single contended command pay that timeout twice. This pins
// that the hook skips the sweep once Refresh has already reported the
// manifest locked, so the command pays it at most once.
//
// unix-only, and in its own file rather than behind a runtime skip: the lock
// is the kernel's, so the only way to be a competing holder is to take it
// with the same syscall the package does, and syscall.Flock does not exist on
// Windows. The behaviour under test is platform-independent; only the way to
// contend for the lock is not.
func TestSkillsRefreshHookDoesNotDoubleTheLockWaitWhenContended(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := skills.Install([]string{"claude"}); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	// Refresh compares the fingerprint before it locks anything, so a manifest
	// already on this version never reaches the lock. Age it, or the hook has
	// no reason to contend and this test proves nothing.
	stale(t, filepath.Join(home, ".orq", "materialized-skills.json"))

	// Hold the lock on our own descriptor. flock is held per open file
	// description, not per process, so this contends with the hook's
	// acquisition exactly as another orq process would.
	lockPath := filepath.Join(home, ".orq", "materialized-skills.json.lock")
	held, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("take the manifest lock: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(held.Fd()), syscall.LOCK_UN)
		_ = held.Close()
	})

	// installSkillsRefreshPreRun writes its warnings to bartolocli.Stderr
	// directly (see captureOutput in commands/connect_test.go for the same
	// pattern), not through cobra's root.SetErr, so that is what has to be
	// swapped to observe them.
	var stderr bytes.Buffer
	prevStderr := bartolocli.Stderr
	bartolocli.Stderr = &stderr
	t.Cleanup(func() { bartolocli.Stderr = prevStderr })

	root := buildRoot(t)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"connect", "--status"})

	start := time.Now()
	if err := root.Execute(); err != nil {
		t.Fatalf("connect --status: %v", err)
	}
	elapsed := time.Since(start)

	// One lockTimeout (2s in lock.go) plus generous scheduling slack; two
	// sequential waits would take roughly double that.
	if elapsed > 3*time.Second {
		t.Errorf("command took %s with a contended lock; want at most one lockTimeout wait, not two", elapsed)
	}
	out := stderr.String()
	if !strings.Contains(out, "could not refresh orq skills") {
		t.Errorf("expected a refresh warning, got:\n%s", out)
	}
	if strings.Contains(out, "could not clean up stale session skills") {
		t.Errorf("sweep ran despite the lock already being reported as held, doubling the wait:\n%s", out)
	}
}

// stale rewrites the manifest's fingerprint in place, the one field that
// decides whether refresh has work to do.
func stale(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["fingerprint"] = "a-previous-release"
	data, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
