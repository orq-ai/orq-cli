package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
//
// The lock itself is the kernel's (flock on unix, LockFileEx on Windows), held
// on an open descriptor rather than written into a file. That is what makes
// this small: a descriptor lock is released when the process exits however it
// exits, so there is no abandoned lock to detect, no PID to check for
// liveness, no age bound to guess at, no heartbeat to prove a slow holder is
// still alive, and no non-atomic break where two processes can both decide a
// lock is free. A file-contents lock needed all five, and each was its own
// way to either wedge every command or admit two writers at once.
const (
	lockRetryInterval = 20 * time.Millisecond
	lockTimeout       = 2 * time.Second
)

// manifestMu serializes writers inside one process. The file lock alone would
// not: flock is held per open file description, so a second acquisition here
// opens its own descriptor and blocks against the one this process already
// holds — a self-deadlock that waits out the full timeout and then reports
// another process is using the manifest, naming ourselves.
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
		release, held, err := lockFile(path)
		if err != nil {
			return nil, fmt.Errorf("could not lock the skills manifest at %s: %w", path, err)
		}
		if !held {
			return release, nil
		}
		if time.Now().After(deadline) {
			// No remedy about deleting the file: unlike a contents-based
			// lock, this one cannot outlive its holder, so a lock that is
			// held means a live orq process is holding it.
			return nil, fmt.Errorf("%w: %s. Wait for the other command to finish", ErrManifestLocked, path)
		}
		time.Sleep(lockRetryInterval)
	}
}
