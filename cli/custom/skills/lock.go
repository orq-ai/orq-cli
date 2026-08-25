package skills

import (
	"crypto/rand"
	"encoding/hex"
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
	// Older than this and the holder is gone whatever the PID says. Covers a
	// lock stamped by a process whose PID has since been reused, and one
	// killed between creating the file and writing its PID into it. Both
	// otherwise wedge every later command for good.
	//
	// The age is measured from the last heartbeat, not from creation: a live
	// holder rewrites the lock file's modification time every
	// lockHeartbeat (see startHeartbeat), so "old" means "nothing has signed
	// for this lock in ten minutes", not "this command started ten minutes
	// ago". Elapsed time alone would not do. Breaking a live holder's lock
	// loses manifest records, which is worse than a wedge that commands wait
	// out, and no bound on wall-clock can tell a slow copy-mode install on
	// Windows-with-antivirus, or a laptop suspended mid-command, from an
	// abandoned lock.
	lockMaxAge = 10 * time.Minute
	// Frequent enough that a holder never approaches lockMaxAge between
	// beats, rare enough to be invisible: four beats per bound.
	lockHeartbeat = lockMaxAge / 4
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
	mine, err := lockToken()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, writeErr := f.WriteString(mine)
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				// An unstamped lock file reads as held by nobody and wedges
				// every later command, so drop it rather than leave it.
				_ = os.Remove(path)
				return nil, fmt.Errorf("could not stamp the skills lock at %s: %w", path, errors.Join(writeErr, closeErr))
			}
			stopBeat := startHeartbeat(path, mine, lockHeartbeat)
			return func() {
				stopBeat()
				releaseLock(path, mine)
			}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		// Checked before the break, so no path through this loop can spin: a
		// lock file we are not allowed to remove is stale forever, and
		// `continue` alone would busy-wait on it at full speed for the rest
		// of the command's life.
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: %s is held by %s. Wait for the other command to finish, or remove that file if no orq process is running",
				ErrManifestLocked, path, holderDescription(path))
		}
		if lockIsStale(path) {
			// Known limitation: observing staleness and removing the file are
			// not one operation, so two breakers can both proceed and a second
			// acquire can land on top of the first. Measured at roughly one
			// overlap per six contended breaks. Removing the break is not the
			// answer either, since a lock left by a killed holder would then
			// wedge every later command. The fix is flock, which the kernel
			// releases on death and which has no stale state to break; that
			// needs a Windows implementation alongside, so it is its own
			// change. Described in the PR body under "Not fixed here".
			//
			// A remove that fails is not a break: falling through to the wait
			// leaves the caller with the ErrManifestLocked message, which
			// already names the file to delete by hand. Retrying it in a tight
			// loop would just hang the command instead.
			if rmErr := os.Remove(path); rmErr == nil {
				continue
			}
		}
		time.Sleep(lockRetryInterval)
	}
}

// startHeartbeat keeps the lock file's modification time current for as long
// as we hold it, and returns a func that stops it. Without this the age bound
// in lockIsStale measures wall-clock since acquisition, so a holder that is
// merely slow — a copy-mode install behind antivirus, a machine suspended
// mid-command — gets its lock broken while it is still writing, which is the
// lost update the whole file exists to prevent.
//
// A failed Chtimes is not reported: the beat is a liveness hint, and the one
// consequence of missing it is that a genuinely live holder can be broken
// after ten minutes, which is exactly the behaviour without a heartbeat at
// all. There is nothing for the caller to do about it and nothing to abort.
// every is a parameter rather than a read of lockHeartbeat so a test can beat
// on a timescale it can wait out.
// The returned func waits for the beat to stop before it returns, so a caller
// that stops and then releases cannot have a beat land in between and refresh
// a lock it no longer holds.
func startHeartbeat(path, mine string, every time.Duration) func() {
	done := make(chan struct{})
	var wg sync.WaitGroup
	var once sync.Once
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				// Only ever touch a lock that is still ours. After a stale
				// break the file belongs to another process, and refreshing
				// its timestamp would keep a lock alive on its behalf.
				data, err := os.ReadFile(path)
				if err != nil || strings.TrimSpace(string(data)) != mine {
					return
				}
				_ = os.Chtimes(path, now, now)
			}
		}
	}()
	return func() {
		once.Do(func() { close(done) })
		wg.Wait()
	}
}

// lockToken identifies one acquisition, not one process. The PID alone cannot:
// a lock stamped by a process that has since exited, whose PID the OS has
// since handed to this one, would read as our own. The release check and the
// heartbeat would then both act on a lock we do not hold. Within one process
// the PID would be enough — manifestMu serializes every acquisition, so two
// are never in flight here.
func lockToken() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(b[:])), nil
}

// lockPID reads the process id out of a lock token.
func lockPID(token string) (int, error) {
	pid, _, _ := strings.Cut(strings.TrimSpace(token), "-")
	return strconv.Atoi(pid)
}

// releaseLock removes the lock only while we still hold it. After a stale
// break the file on disk can belong to another process, and removing that one
// would let a third writer in alongside it.
//
// A read that fails counts as not ours for the same reason: a lock we cannot
// inspect is not one we can prove we hold. That can leave our own lock file
// behind, but not for long — the heartbeat has already stopped by the time
// this runs, so the file ages out at lockMaxAge instead of wedging for good.
func releaseLock(path, mine string) {
	data, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(data)) != mine {
		return
	}
	_ = os.Remove(path)
}

// lockIsStale reports whether the lock can be broken: its holder is gone, or
// nothing has heartbeat for it in lockMaxAge.
//
// The PID check assumes the lock file and its holder live on the same machine,
// which is true for a per-user ~/.orq. A $HOME shared over the network between
// machines would make a live remote PID look dead here; the age bound is what
// keeps that from being permanent in the other direction.
func lockIsStale(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	// Bounded on both sides. A timestamp in the future is not one this
	// machine's clock can reason about — an NTP step, a VM clock jump, a
	// filesystem with coarse or skewed timestamps — and left one-sided it
	// wedges every later command for as long as the clock says so, which for
	// an unstamped lock (below) is forever.
	if age := time.Since(info.ModTime()); age > lockMaxAge || age < -lockMaxAge {
		return true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := lockPID(string(data))
	if err != nil {
		// Unstamped: only the age bound above can retire it.
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
	pid, err := lockPID(string(data))
	if err != nil {
		return "another process"
	}
	return fmt.Sprintf("pid %d", pid)
}
