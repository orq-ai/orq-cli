//go:build !windows

package skills

import (
	"os"
	"syscall"
)

// processAlive reports whether a PID names a live process. Signal 0 performs
// the permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
