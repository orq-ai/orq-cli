//go:build windows

package skills

import "os"

// processAlive on Windows: FindProcess opens the process by handle and fails
// outright for a PID that is not running, which is the whole check.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
