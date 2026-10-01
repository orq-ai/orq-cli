//go:build windows

package auth

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockSessionFile(f *os.File) (func(), error) {
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0, &overlapped,
	); err != nil {
		return nil, err
	}
	return func() {
		var o windows.Overlapped
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &o)
	}, nil
}
