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
// multi-second stall: skills.SweepDeadSessions and skills.Refresh each wait
// out lock.go's lockTimeout on their own, and calling both unconditionally
// would make a single contended command pay that timeout twice. The sweep
// runs first (it runs on every command; the refresh does not), so this pins
// that the hook skips the refresh once the sweep has already reported the
// manifest locked, and the command pays the timeout at most once.
//
// Both halves have to actually reach the lock for that to be under test: the
// sweep returns before locking unless a dead session is recorded, and Refresh
// returns before locking unless the fingerprint is stale. The setup arranges
// both.
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
	// Same for the sweep: with no dead session it returns before locking, and
	// the refresh below would never be the one that got skipped.
	recordDeadSession(t, home)

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
	if !strings.Contains(out, "could not clean up stale session skills") {
		t.Errorf("expected a sweep warning, got:\n%s", out)
	}
	if strings.Contains(out, "could not refresh orq skills") {
		t.Errorf("refresh ran despite the lock already being reported as held, doubling the wait:\n%s", out)
	}
}

// The sweep is the half that runs everywhere. A launch killed without
// releasing its claim leaves links in the user's real skills directory, and
// nothing else collects them — doctor excludes session links by design — so
// an unrelated command has to be enough.
func TestSkillsSweepRunsOnACommandThatHasNothingToDoWithSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	// A real symlink into a real snapshot, because the sweep only removes what
	// it can still prove is ours.
	orq, err := skills.Home()
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(orq, "snapshot", "gen-test", "orq-leaked")
	leaked := filepath.Join(home, ".claude", "skills", "orq-leaked")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(leaked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, leaked); err != nil {
		t.Fatal(err)
	}
	m := &skills.Manifest{Version: 1, Fingerprint: skills.Fingerprint()}
	m.AddLink(skills.Link{Path: leaked, Skill: "orq-leaked", Agent: "claude", Mode: skills.ModeSymlink, Session: true})
	m.Sessions = append(m.Sessions, skills.Session{ID: "dead", PID: -1, Paths: []string{leaked}})
	if err := skills.SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	root := buildRoot(t)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"man-pages", "--dir", filepath.Join(t.TempDir(), "man")})
	if err := root.Execute(); err != nil {
		t.Fatalf("man-pages: %v", err)
	}

	after, err := skills.LoadManifest()
	if err != nil || after == nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(after.Sessions) != 0 {
		t.Errorf("a dead session survived an unrelated command: %d left", len(after.Sessions))
	}
	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		t.Errorf("the dead session's link was left on disk: %v", err)
	}
}

// recordDeadSession gives the sweep something to collect, so it reaches the
// lock instead of returning on its pre-lock check.
func recordDeadSession(t *testing.T, home string) {
	t.Helper()
	m, err := skills.LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: %v %v", m, err)
	}
	m.Sessions = append(m.Sessions, skills.Session{
		ID: "dead",
		// Non-positive PIDs are defined as dead by processAlive, so this
		// fixture cannot collide with a real process on a hosted runner.
		PID:   -1,
		Paths: []string{filepath.Join(home, ".claude", "skills", "orq-gone")},
	})
	if err := skills.SaveManifest(m); err != nil {
		t.Fatal(err)
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
