//go:build windows

package skills

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is Win32 STILL_ACTIVE, the exit code a running process reports.
const stillActive = 259

// processAlive reports whether a PID names a live process.
//
// os.FindProcess is not the check it looks like on Windows: it opens the
// process with PROCESS_QUERY_INFORMATION, which another user's process — or a
// more privileged one — refuses with ERROR_ACCESS_DENIED. Reading that as
// "dead" is the one error that matters here, because it lets the sweep pull
// the skills out from under a session that is still running. This is the same
// case the unix build treats as alive on EPERM.
//
// PROCESS_QUERY_LIMITED_INFORMATION is the right of the two: it is granted
// across integrity levels where the fuller one is not. A refusal that is not
// about permission (ERROR_INVALID_PARAMETER, for a PID that does not exist)
// means dead.
//
// The exit code is checked because a PID whose process has exited stays
// openable while any handle to it remains, and such a handle answers
// STILL_ACTIVE only while the process is really running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		// The handle opened, so something is there; keeping its links is the
		// safe direction.
		return true
	}
	return code == stillActive
}
