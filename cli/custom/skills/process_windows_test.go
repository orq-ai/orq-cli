//go:build windows

package skills

import (
	"os"
	"os/exec"
	"testing"
)

// PID 4 is the System process: always running, and owned by a security
// context no test-runner user can open with PROCESS_QUERY_INFORMATION. It is
// the Windows shape of the case processAlive must not read as dead — the one
// that would let the sweep pull skills out from under a running session.
func TestProcessAliveTreatsAProtectedProcessAsAlive(t *testing.T) {
	if !processAlive(4) {
		t.Error("pid 4 (System) reported dead; a live process we cannot open would lose its links")
	}
}

func TestProcessAliveSeesItselfAndNotAnExitedChild(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("this process reported dead")
	}
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	// The child is gone, but this process still holds a handle to it, so the
	// PID stays openable. Only the exit code separates the two.
	if processAlive(pid) {
		t.Errorf("exited pid %d reported alive; its session's links would never be swept", pid)
	}
}
