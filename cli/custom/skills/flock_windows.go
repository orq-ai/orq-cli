//go:build windows

package skills

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile is the Windows half of the advisory lock; see flock_unix.go for
// the contract. LockFileEx is the closest equivalent to flock: the lock is
// held by the handle, and Windows releases every lock a process holds when
// its handles close, including on an abnormal exit.
func lockFile(path string) (release func(), held bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	var overlapped windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped,
	)
	if err != nil {
		f.Close()
		if err == windows.ERROR_LOCK_VIOLATION {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() {
		var o windows.Overlapped
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &o)
		_ = f.Close()
	}, false, nil
}
