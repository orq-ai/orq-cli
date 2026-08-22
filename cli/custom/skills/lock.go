package skills

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Manifest mutation is load → mutate → save, which is not atomic across
// processes: two writers that load the same manifest lose one another's
// changes. That was theoretical while only `orq connect` wrote it, and stopped
// being theoretical with session links, which are written by every `orq
// launch` — two launches starting at once, or one starting while another
// exits, are ordinary. A lost update there does not just lose a record: it
// leaves a link nothing claims (a leak the sweep eventually collects) or drops
// a claim another live session depends on.
//
// The lock is advisory, best effort, and deliberately not fatal. It is held
// for the microseconds of a read-modify-write, and a launch must never fail or
// hang because of a lock file.
const (
	lockRetryInterval = 20 * time.Millisecond
	lockTimeout       = 2 * time.Second
)

// manifestMu serializes writers inside one process. The lock file cannot:
// a second acquire from the same process sees its own live PID and would
// simply wait for itself.
var manifestMu sync.Mutex

func lockPath() (string, error) {
	path, err := manifestPath()
	if err != nil {
		return "", err
	}
	return path + ".lock", nil
}

// withManifestLock runs fn with the manifest lock held where it can be taken.
// If it cannot be taken within lockTimeout, fn runs anyway: a stuck lock file
// must not stop the CLI from working, and the unlocked path is exactly the
// behaviour every release before this one had.
func withManifestLock(fn func() error) error {
	manifestMu.Lock()
	defer manifestMu.Unlock()

	release, err := acquireLock()
	if err == nil && release != nil {
		defer release()
	}
	return fn()
}

func acquireLock() (func(), error) {
	path, err := lockPath()
	if err != nil {
		return nil, err
	}
	// Never create the state directory just to lock it: a read-only path like
	// Refresh on a machine that never connected must leave no trace, and
	// where there is no directory there is no manifest to race over yet.
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if holderIsDead(path) {
			// The holder was killed mid-write. Break the lock rather than
			// making every later invocation wait out the timeout.
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, os.ErrDeadlineExceeded
		}
		time.Sleep(lockRetryInterval)
	}
}

// holderIsDead reports whether the process named in the lock file is gone. An
// unreadable or unparsable lock file is treated as held: guessing wrong in
// that direction only costs a wait, guessing wrong the other way lets two
// writers in at once.
//
// This assumes the lock file and its holder live on the same machine, which
// is true for a per-user ~/.orq. A $HOME shared over the network between
// machines would make a live remote PID look dead here.
func holderIsDead(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}
	return !processAlive(pid)
}
