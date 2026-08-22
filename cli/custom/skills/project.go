package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
			existing := findLink(m, path)
			switch {
			case existing == nil && exists(path):
				// Somebody else's skill by the same name. Theirs wins; ours is
				// not installed, and the caller says so.
				res.Skipped = append(res.Skipped, path)
				continue
			case existing != nil:
				// The manifest claims this path, but it may have been taken
				// over by the user since: a recorded symlink that is no
				// longer ours (or a copy-mode directory replaced by a real
				// one) must not be blown away and reprojected. isOurs is the
				// same guard Remove and Refresh use before every deletion.
				if exists(path) && !isOurs(*existing) {
					res.Skipped = append(res.Skipped, path)
					continue
				}
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
//
// A link with an empty Agent is the shared agents-spec directory (see
// Targets): it belongs to the request whenever any named agent is one of the
// shared readers, the same membership Targets uses to decide whether to
// write there in the first place.
func Remove(agents []string) (*Result, error) {
	m, err := LoadManifest()
	if err != nil || m == nil {
		return &Result{}, err
	}
	wanted := map[string]bool{}
	sharedWanted := false
	for _, a := range agents {
		wanted[a] = true
		sharedWanted = sharedWanted || sharedReaders[a]
	}
	res := &Result{}
	var gone []string
	for _, l := range m.Links {
		if l.Session {
			continue
		}
		if len(agents) > 0 && !wanted[l.Agent] && !(l.Agent == "" && sharedWanted) {
			continue
		}
		if exists(l.Path) && !isOurs(l) {
			res.Skipped = append(res.Skipped, l.Path)
			continue
		}
		if err := removePath(l.Path); err != nil {
			return &Result{}, err
		}
		gone = append(gone, l.Path)
		res.Removed = append(res.Removed, l.Path)
	}
	m.RemoveLinks(gone)
	if err := SaveManifest(m); err != nil {
		return &Result{}, err
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
		return &Result{}, err
	}
	names, err := Names()
	if err != nil {
		return &Result{}, err
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
				return &Result{}, err
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
			return &Result{}, err
		}
		if err := project(filepath.Join(gen, l.Skill), l.Path); err != nil {
			return &Result{}, err
		}
		res.Added = append(res.Added, l.Path)
	}
	m.RemoveLinks(pruned)
	m.Fingerprint = Fingerprint()
	m.Generation = gen
	if err := SaveManifest(m); err != nil {
		return &Result{}, err
	}
	return res, nil
}

// project creates one view of one skill: a symlink where the platform allows
// it, a copy where it does not. The copy path stages into a sibling temp
// directory and renames into place, the same way EnsureGeneration does, so a
// mid-copy failure never leaves a partial directory at dest for a later run
// to misclassify as foreign.
func project(src, dest string) error {
	if symlinkSupported() {
		return os.Symlink(src, dest)
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := CopyDir(src, staging); err != nil {
		return err
	}
	return os.Rename(staging, dest)
}

// CopyDir recursively copies src into dest. It is exported because the
// launch package needs the same copier for its own fallback path; keeping one
// implementation avoids two divergent sets of permission decisions.
//
// File permission bits are preserved from the source. A symlink inside the
// source tree is recreated as a symlink at the destination (its target is not
// followed), so a skill that itself contains a symlink copies faithfully
// instead of silently dereferencing or erroring out.
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
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			linkTarget, readErr := os.Readlink(p)
			if readErr != nil {
				return readErr
			}
			return os.Symlink(linkTarget, target)
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		default:
			data, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		}
	})
}

func findLink(m *Manifest, path string) *Link {
	for i := range m.Links {
		if m.Links[i].Path == path {
			return &m.Links[i]
		}
	}
	return nil
}

// isOurs guards every deletion. A recorded symlink that is no longer a symlink
// has been replaced by a user, and replacing it back would destroy their work.
//
// For ModeSymlink links this also verifies the symlink still points inside
// our own snapshot tree: a user who replaced our link with their own symlink
// elsewhere has taken this path over just as surely as if they had put a real
// directory there, and a mode/type check alone would miss it.
//
// For ModeCopy links this only checks that a directory still sits at the
// path. It cannot tell a hand-edited copy from an untouched one — that would
// need per-file content hashes, and the frozen v1 manifest schema has no
// field for them. ModeCopy is the Windows-only symlink fallback, so this is a
// known, accepted gap rather than an oversight.
func isOurs(l Link) bool {
	info, err := os.Lstat(l.Path)
	if err != nil {
		return false
	}
	if l.Mode == ModeSymlink {
		if info.Mode()&os.ModeSymlink == 0 {
			return false
		}
		return symlinkPointsIntoSnapshot(l.Path)
	}
	return info.IsDir()
}

// symlinkPointsIntoSnapshot reports whether the symlink at path resolves to
// somewhere inside our own generation snapshots. A Readlink failure is
// treated as not ours: a link we cannot even inspect is not one we can trust.
func symlinkPointsIntoSnapshot(path string) bool {
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	target = filepath.Clean(target)
	home, err := Home()
	if err != nil {
		return false
	}
	snapshot := filepath.Clean(filepath.Join(home, "snapshot"))
	rel, err := filepath.Rel(snapshot, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
