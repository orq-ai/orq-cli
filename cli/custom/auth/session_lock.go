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
	sessionUpdateMu.Lock()
	release, err := acquireSessionUpdateFileLock()
	if err != nil {
		sessionUpdateMu.Unlock()
		return nil, err
	}
	return func() {
		release()
		sessionUpdateMu.Unlock()
	}, nil
}

func acquireSessionUpdateFileLock() (func(), error) {
	path := SessionFilePath() + ".lock"
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
