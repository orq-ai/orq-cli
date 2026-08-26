package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// manifestVersion is bumped when the schema changes incompatibly. A manifest
// this binary does not understand is left strictly alone: guessing at a future
// schema risks deleting paths whose meaning we do not know.
const manifestVersion = 1

const (
	ModeSymlink = "symlink"
	ModeCopy    = "copy"
)

// Link is one skill installed into one directory. Agent is empty for the
// shared agents-spec directory, which serves several agents at once.
type Link struct {
	Path    string `json:"path"`
	Agent   string `json:"agent,omitempty"`
	Skill   string `json:"skill"`
	Mode    string `json:"mode"`
	Session bool   `json:"session,omitempty"`
}

// Session is one live `orq launch` holding session-scoped links. PID is what
// lets a later invocation tell a running session from a crashed one; ID is
// what tells two sessions of the same process apart, so one cannot release
// the other's links. Both are optional in the schema (a manifest written
// before sessions existed has neither), so this stays version 1.
type Session struct {
	ID    string   `json:"id,omitempty"`
	PID   int      `json:"pid"`
	Paths []string `json:"paths"`
}

type Manifest struct {
	Version     int       `json:"version"`
	Fingerprint string    `json:"fingerprint"`
	Generation  string    `json:"generation"`
	Links       []Link    `json:"links,omitempty"`
	Sessions    []Session `json:"sessions,omitempty"`
}

func manifestPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "materialized-skills.json"), nil
}

// LoadManifest returns nil, nil when there is nothing to load: no file or a
// version this binary does not understand. Every caller treats nil as "this
// machine has never connected skills", which is the case where we must not
// touch the filesystem at all. Returns (nil, err) for other I/O errors (e.g.,
// the file is a directory) or JSON parse errors, which are real problems that
// callers must handle.
func LoadManifest() (*Manifest, error) {
	path, err := manifestPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil
	}
	if m.Version != manifestVersion {
		return nil, nil
	}
	return &m, nil
}

// SaveManifest writes atomically. Callers must call it only after their links
// are in place, so an interrupted run re-runs cleanly rather than leaving the
// manifest claiming links that were never created.
func SaveManifest(m *Manifest) error {
	path, err := manifestPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	m.Version = manifestVersion
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Use CreateTemp for a unique temp file per writer to avoid concurrent
	// writers colliding on the same .tmp name.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".materialized-skills-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Clean up temp file on error paths.
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Rename atomically; on success, clear tmpName so defer does not delete it.
	if err := replaceFile(tmpName, path); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

// OwnedPaths is every path this CLI created, session-scoped or not. It is the
// allow-list for removal: nothing outside it is ever deleted.
func (m *Manifest) OwnedPaths() []string {
	out := make([]string, 0, len(m.Links))
	for _, l := range m.Links {
		out = append(out, l.Path)
	}
	return out
}

// AddLink records a link, replacing any record of the same path. It records
// what the caller decided and nothing more: whether a path may be claimed
// session-scoped is InstallSession's switch to make, and whether a recorded
// link may be deleted is releaseLocked's. A third copy of that rule here
// would be a rule nobody reads, silently correcting the two that they do.
func (m *Manifest) AddLink(l Link) {
	// Normalize the path to prevent duplicates from formatting variations.
	l.Path = filepath.Clean(l.Path)
	for i, existing := range m.Links {
		if existing.Path == l.Path {
			m.Links[i] = l
			return
		}
	}
	m.Links = append(m.Links, l)
}

func (m *Manifest) RemoveLinks(paths []string) {
	drop := make(map[string]bool, len(paths))
	for _, p := range paths {
		// Normalize paths to match AddLink's normalization.
		drop[filepath.Clean(p)] = true
	}
	kept := m.Links[:0]
	for _, l := range m.Links {
		if !drop[l.Path] {
			kept = append(kept, l)
		}
	}
	m.Links = kept
}

// replaceFile is os.Rename with a bounded retry, for one Windows behaviour:
// replacing a file that any other handle has open without FILE_SHARE_DELETE
// fails with ERROR_ACCESS_DENIED instead of waiting. Concurrent writers hit
// it, and so does an antivirus scanner or the search indexer reading the
// manifest at the wrong moment — the single-process case, which no lock can
// prevent. The holders are all momentary, so a short retry is the whole fix.
// On unix the first attempt always succeeds and this costs nothing.
//
// ponytail: fixed schedule, ~250ms total. Long enough for a scanner's read,
// short enough that a genuinely stuck file still reports rather than hangs.
func replaceFile(from, to string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return err
}
