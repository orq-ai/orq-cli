//go:build !windows

package skills

import (
	"errors"
	"os"
	"syscall"
)

// processAlive reports whether a PID names a live process. Signal 0 performs
// the permission and existence checks without delivering anything.
//
// EPERM means the process exists and belongs to another user, so it counts as
// alive. Reading it as dead is the one error that matters here: it would let
// the sweep release the links of a session that is still running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
