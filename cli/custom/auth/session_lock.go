package auth

import (
	"os"
	"path/filepath"
	"sync"
)

// Serialize refresh read/modify/write cycles both within this process and
// across CLI processes. Atomic rename protects readers from partial JSON, but
// without this lock concurrent refreshes can each save a snapshot that loses
// the other one's token slot.
var sessionUpdateMu sync.Mutex

func lockSessionUpdates() (func(), error) {
	return lockSessionUpdatesAt(SessionFilePath())
}

func lockSessionUpdatesAt(path string) (func(), error) {
	sessionUpdateMu.Lock()
	release, err := acquireSessionUpdateFileLock(path)
	if err != nil {
		sessionUpdateMu.Unlock()
		return nil, err
	}
	return func() {
		release()
		sessionUpdateMu.Unlock()
	}, nil
}

func acquireSessionUpdateFileLock(path string) (func(), error) {
	return acquireFileLock(path + ".lock")
}

// secretStoreMu and the per-host ".secrets.lock" file serialize every read and
// write of a host's secrets across the store and the session file. A macOS
// secret can span several keychain items, written one command at a time, and
// the file and store are two writes; without this a reader can join chunks of
// two writes, and two writers can interleave store A, store B, file B, file A.
// It is a separate lock from lockSessionUpdates, which recordRefresh holds
// around a whole read-modify-save, so taking both never deadlocks.
var secretStoreMu sync.Mutex

// lockSecretStore is best effort: a lock file that cannot be created (a
// read-only ~/.orq) must not take every command down, so the caller proceeds
// with only the in-process lock. A concurrent process may then read a mixed
// multi-chunk write, which the chunk checksum rejects as corrupt.
func lockSecretStore(sessionPath string) func() {
	secretStoreMu.Lock()
	release, err := acquireFileLock(sessionPath + ".secrets.lock")
	if err != nil {
		return secretStoreMu.Unlock
	}
	return func() {
		release()
		secretStoreMu.Unlock()
	}
}

func acquireFileLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	release, err := lockSessionFile(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		release()
		_ = f.Close()
	}, nil
}
