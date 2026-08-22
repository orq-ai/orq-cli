package skills

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// InstallSession links the skill set for one `orq launch`, and returns the
// function that gives it back. Links a permanent install already owns are
// left exactly as they are: a session must never take them away on exit.
// Neither is anything the CLI did not create — a directory somebody else put
// at one of our paths is used as it stands and never touched.
//
// Several sessions can want the same path at once: two claude launches, or an
// opencode and a pi launch, which both read the shared ~/.agents/skills. The
// second one to arrive joins the first one's claim rather than creating
// anything, and the path survives until the last holder releases it.
//
// The returned release is safe to call more than once.
func InstallSession(agent string) (func(), error) {
	gen, err := EnsureGeneration()
	if err != nil {
		return nil, err
	}
	targets, err := Targets([]string{agent})
	if err != nil {
		return nil, err
	}
	names, err := Names()
	if err != nil {
		return nil, err
	}

	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	pid := os.Getpid()

	err = withManifestLock(func() error {
		m, err := LoadManifest()
		if err != nil {
			return err
		}
		if m == nil {
			m = &Manifest{Version: manifestVersion}
		}

		var held []string
		for _, target := range targets {
			if err := os.MkdirAll(target.Dir, 0o755); err != nil {
				return err
			}
			for _, name := range names {
				path := filepath.Clean(filepath.Join(target.Dir, name))
				existing := findLink(m, path)
				switch {
				case existing != nil && existing.Session && exists(path) && isOurs(*existing):
					// Another live session already put this here. Join its
					// claim instead of reprojecting: the link stays until the
					// last of us is gone.
					held = append(held, path)
				case exists(path):
					// Either a permanent install of ours or somebody else's
					// skill. Both cases mean: use what is there, own nothing,
					// remove nothing on exit.
					continue
				default:
					if err := project(filepath.Join(gen, name), path); err != nil {
						if os.IsExist(err) {
							// Another writer got here between the check and
							// the create — possible only when the manifest
							// lock could not be taken. Theirs is on disk and
							// recorded in their manifest write: use it, own
							// nothing, and leave it alone on exit.
							continue
						}
						return fmt.Errorf("install %s into %s: %w", name, target.Dir, err)
					}
					m.AddLink(Link{Path: path, Agent: target.Agent, Skill: name, Mode: linkMode(), Session: true})
					held = append(held, path)
				}
			}
		}
		m.Sessions = append(m.Sessions, Session{ID: id, PID: pid, Paths: held})
		m.Generation = gen
		// Written last, after the links are in place: an interrupted install
		// leaves orphans the sweep collects, where a manifest written first
		// would claim links that were never created.
		return SaveManifest(m)
	})
	if err != nil {
		return nil, err
	}

	// Released only on success. A release that could not take the manifest
	// lock has NOT given the links back: dropping the claim silently would
	// leave links on disk that nothing records, so the claim stays put and
	// either a later call retries it or SweepDeadSessions collects it once
	// this process is gone. Calling the returned function again after a
	// successful release is a no-op.
	var mu sync.Mutex
	released := false
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if released {
			return
		}
		if err := ReleaseSession(id); err == nil {
			released = true
		}
	}, nil
}

// ReleaseSession drops one session's claim and removes only the links no
// other session still holds. It reports an error rather than dropping the
// claim when the manifest cannot be locked; the claim then outlives the
// process and SweepDeadSessions collects it.
func ReleaseSession(id string) error {
	return withManifestLock(func() error {
		m, err := LoadManifest()
		if err != nil || m == nil {
			return err
		}
		if err := releaseLocked(m, func(s Session) bool { return s.ID == id }); err != nil {
			return err
		}
		return SaveManifest(m)
	})
}

// SweepDeadSessions releases claims held by processes that are no longer
// running. It is housekeeping: a caller that gets an error (another orq
// process holds the manifest, most likely mid-launch) should carry on, since
// the next invocation sweeps again. A session killed rather than exited leaves its links behind, and
// this is what makes the next `orq` command clean them up.
func SweepDeadSessions() error {
	return withManifestLock(func() error {
		m, err := LoadManifest()
		if err != nil || m == nil {
			return err
		}
		dead := false
		for _, s := range m.Sessions {
			if !processAlive(s.PID) {
				dead = true
				break
			}
		}
		if !dead {
			return nil
		}
		if err := releaseLocked(m, func(s Session) bool { return !processAlive(s.PID) }); err != nil {
			return err
		}
		return SaveManifest(m)
	})
}

// releaseLocked drops every session matching claimed from m and deletes the
// paths they held that nothing else still holds. It mutates m in place; the
// caller owns the manifest lock and the save.
//
// Two guards stand between this and a permanent install: a path is only
// considered when the session that recorded it is one of ours to drop, and
// the link at that path must still be recorded as session-scoped. A path that
// `orq connect` has since claimed permanently is left alone, and so is one a
// user has replaced with their own directory.
func releaseLocked(m *Manifest, claimed func(Session) bool) error {
	var mine []string
	kept := make([]Session, 0, len(m.Sessions))
	for _, s := range m.Sessions {
		if claimed(s) {
			mine = append(mine, s.Paths...)
			continue
		}
		kept = append(kept, s)
	}
	m.Sessions = kept

	stillHeld := map[string]bool{}
	for _, s := range m.Sessions {
		for _, p := range s.Paths {
			stillHeld[p] = true
		}
	}
	var removed []string
	for _, p := range mine {
		if stillHeld[p] {
			continue
		}
		link := findLink(m, p)
		if link == nil || !link.Session {
			// Not ours to remove any more: either the manifest never recorded
			// it, or a permanent install took the path over while we ran.
			continue
		}
		if exists(p) && !isOurs(*link) {
			// Replaced by the user since we linked it. Drop the record, keep
			// their file.
			removed = append(removed, p)
			continue
		}
		if err := removePath(p); err != nil {
			return err
		}
		removed = append(removed, p)
	}
	m.RemoveLinks(removed)
	return nil
}

// newSessionID identifies one launch. The PID alone cannot: two sessions in
// the same process (tests, and any future in-process launcher) would release
// each other's links, and a PID is reused by the OS soon enough that a stale
// manifest entry could name a live unrelated process.
func newSessionID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(b[:])), nil
}
