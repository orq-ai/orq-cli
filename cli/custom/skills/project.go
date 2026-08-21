package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Result is what a projection did, for the caller to report. Skipped holds
// paths the manifest claims but that something else now occupies; those are
// reported and left untouched rather than overwritten.
type Result struct {
	Added   []string
	Removed []string
	Skipped []string
}

// symlinkSupported is false where an unprivileged symlink cannot be relied on.
// Windows allows them only with developer mode or elevation, and failing
// halfway through an install is worse than copying from the start.
func symlinkSupported() bool { return runtime.GOOS != "windows" }

func linkMode() string {
	if symlinkSupported() {
		return ModeSymlink
	}
	return ModeCopy
}

// Install materializes the current generation and projects it into every
// directory the given agents read. It is the one entry point that may create
// directories that did not exist.
func Install(agents []string) (*Result, error) {
	gen, err := EnsureGeneration()
	if err != nil {
		return nil, err
	}
	targets, err := Targets(agents)
	if err != nil {
		return nil, err
	}
	names, err := Names()
	if err != nil {
		return nil, err
	}
	m, err := LoadManifest()
	if err != nil {
		return nil, err
	}
	if m == nil {
		m = &Manifest{Version: manifestVersion}
	}
	res := &Result{}
	for _, target := range targets {
		if err := os.MkdirAll(target.Dir, 0o755); err != nil {
			return nil, err
		}
		for _, name := range names {
			path := filepath.Join(target.Dir, name)
			owned := ownsPath(m, path)
			switch {
			case !owned && exists(path):
				// Somebody else's skill by the same name. Theirs wins; ours is
				// not installed, and the caller says so.
				res.Skipped = append(res.Skipped, path)
				continue
			case owned:
				if err := removePath(path); err != nil {
					return nil, err
				}
			}
			if err := project(filepath.Join(gen, name), path); err != nil {
				return nil, fmt.Errorf("install %s into %s: %w", name, target.Dir, err)
			}
			m.AddLink(Link{Path: path, Agent: target.Agent, Skill: name, Mode: linkMode()})
			res.Added = append(res.Added, path)
		}
	}
	m.Fingerprint = Fingerprint()
	m.Generation = gen
	// Written last: an interrupted install re-runs cleanly, where a manifest
	// written first would claim links that were never created.
	if err := SaveManifest(m); err != nil {
		return nil, err
	}
	return res, nil
}

// Remove deletes only what the manifest records for the given agents. An empty
// agent list removes everything non-session we own.
func Remove(agents []string) (*Result, error) {
	m, err := LoadManifest()
	if err != nil || m == nil {
		return &Result{}, err
	}
	wanted := map[string]bool{}
	for _, a := range agents {
		wanted[a] = true
	}
	res := &Result{}
	var gone []string
	for _, l := range m.Links {
		if l.Session {
			continue
		}
		if len(agents) > 0 && !wanted[l.Agent] {
			continue
		}
		if exists(l.Path) && !isOurs(l) {
			res.Skipped = append(res.Skipped, l.Path)
			continue
		}
		if err := removePath(l.Path); err != nil {
			return nil, err
		}
		gone = append(gone, l.Path)
		res.Removed = append(res.Removed, l.Path)
	}
	m.RemoveLinks(gone)
	if err := SaveManifest(m); err != nil {
		return nil, err
	}
	return res, nil
}

// Refresh brings already-installed views up to the current fingerprint. It
// creates nothing new: a machine that never connected has no manifest, and
// this returns immediately without touching the filesystem.
func Refresh() (*Result, error) {
	m, err := LoadManifest()
	if err != nil || m == nil {
		return &Result{}, err
	}
	if m.Fingerprint == Fingerprint() {
		return &Result{}, nil
	}
	gen, err := EnsureGeneration()
	if err != nil {
		return nil, err
	}
	names, err := Names()
	if err != nil {
		return nil, err
	}
	inSet := map[string]bool{}
	for _, n := range names {
		inSet[n] = true
	}
	res := &Result{}
	var pruned []string
	for _, l := range m.Links {
		if l.Session {
			continue
		}
		if !inSet[l.Skill] {
			// The skill left the shipped set. Without this the agent keeps
			// loading something we no longer ship, pointing at a generation
			// that collection will eventually delete.
			if err := removePath(l.Path); err != nil {
				return nil, err
			}
			pruned = append(pruned, l.Path)
			res.Removed = append(res.Removed, l.Path)
			continue
		}
		if exists(l.Path) && !isOurs(l) {
			res.Skipped = append(res.Skipped, l.Path)
			continue
		}
		if err := removePath(l.Path); err != nil {
			return nil, err
		}
		if err := project(filepath.Join(gen, l.Skill), l.Path); err != nil {
			return nil, err
		}
		res.Added = append(res.Added, l.Path)
	}
	m.RemoveLinks(pruned)
	m.Fingerprint = Fingerprint()
	m.Generation = gen
	if err := SaveManifest(m); err != nil {
		return nil, err
	}
	return res, nil
}

// project creates one view of one skill: a symlink where the platform allows
// it, a copy where it does not.
func project(src, dest string) error {
	if symlinkSupported() {
		return os.Symlink(src, dest)
	}
	return CopyDir(src, dest)
}

// CopyDir recursively copies src into dest. It is exported because the
// launch package needs the same copier for its own fallback path; keeping one
// implementation avoids two divergent sets of permission decisions.
func CopyDir(src, dest string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, p)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func ownsPath(m *Manifest, path string) bool {
	for _, l := range m.Links {
		if l.Path == path {
			return true
		}
	}
	return false
}

// isOurs guards every deletion. A recorded symlink that is no longer a symlink
// has been replaced by a user, and replacing it back would destroy their work.
func isOurs(l Link) bool {
	info, err := os.Lstat(l.Path)
	if err != nil {
		return false
	}
	if l.Mode == ModeSymlink {
		return info.Mode()&os.ModeSymlink != 0
	}
	return info.IsDir()
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func removePath(path string) error {
	err := os.RemoveAll(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
