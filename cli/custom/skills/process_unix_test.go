//go:build !windows

package skills

import "testing"

// kill(pid, 0) answers EPERM for a live process owned by another user, and
// reading that as "dead" is the direction that pulls skills out from under a
// running agent. PID 1 is always alive and (unless the test runs as root)
// always somebody else's.
func TestProcessAliveTreatsAnotherUsersProcessAsAlive(t *testing.T) {
	if !processAlive(1) {
		t.Error("pid 1 reported dead; a live process owned by another user would lose its links")
	}
}
