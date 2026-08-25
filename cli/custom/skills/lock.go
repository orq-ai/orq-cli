package skills

import (
	"errors"
	"fmt"
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
// The lock is advisory but it is not optional: a writer that gives up waiting
// and writes anyway loses the other writer's records, and since the manifest
// is the deletion allow-list, links it forgets can never be removed by
// anything again — not release, not the sweep, not `orq disconnect skills`.
// So a writer that cannot take the lock does not write. Callers turn that into
// a warning (launch) or a failed command (connect), which is recoverable;
// unrecorded links in the user's home are not.
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

// ErrManifestLocked reports that another orq process holds the manifest lock.
// It is wrapped, not returned bare, so callers can both recognise the case
// (errors.Is) and print something that says what actually happened.
var ErrManifestLocked = errors.New("another orq process is using the skills manifest")

// withManifestLock runs fn with the manifest lock held. If the lock cannot be
// taken, fn does not run and the error says why: writing without it would
// silently drop whatever the other writer recorded.
func withManifestLock(fn func() error) error {
	manifestMu.Lock()
	defer manifestMu.Unlock()

	release, err := acquireLock()
	if err != nil {
		return err
	}
	if release != nil {
		defer release()
	}
	return fn()
}

// acquireLock returns a release func, or (nil, nil) when there is no state
// directory yet and therefore no manifest to race over. Any other failure is
// an error: a lock file we cannot create next to the manifest means we cannot
// safely write the manifest either.
func acquireLock() (func(), error) {
	path, err := lockPath()
	if err != nil {
		return nil, err
	}
	// Never create the state directory just to lock it: a read-only path like
	// Refresh on a machine that never connected must leave no trace, and
	// where there is no directory there is no manifest to race over yet.
	if _, statErr := os.Stat(filepath.Dir(path)); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, nil
		}
		return nil, statErr
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
			return nil, fmt.Errorf("%w: %s is held by %s. Wait for the other command to finish, or remove that file if no orq process is running",
				ErrManifestLocked, path, holderDescription(path))
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

// holderDescription names the process in the lock file for the error message.
func holderDescription(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "another process"
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return "another process"
	}
	return fmt.Sprintf("pid %d", pid)
}
