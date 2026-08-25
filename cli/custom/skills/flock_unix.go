//go:build !windows

package skills

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on path, creating the file if it
// does not exist, and returns the function that gives it back.
//
// held reports whether the lock is currently taken by somebody: false means
// the caller should retry, not that it failed. Only a genuine error — a path
// it cannot open, a filesystem that cannot lock — comes back as err.
//
// The lock lives on the open descriptor, not on the file's contents, so the
// kernel releases it when this process exits however it exits. That is the
// whole reason to use it: there is no abandoned state to detect and no
// staleness to guess at.
func lockFile(path string) (release func(), held bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		// EWOULDBLOCK is the documented "somebody else holds it" answer, and
		// it is the only one that means retry. EAGAIN is the same errno on
		// every platform Go builds for, and is spelled both ways.
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() {
		// Closing drops the lock. The file itself stays: unlinking it would
		// let a waiter create a fresh one at the same path and lock that
		// instead, so two processes would hold "the lock" at once.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, false, nil
}
