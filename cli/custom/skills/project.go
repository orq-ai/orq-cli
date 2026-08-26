package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Result is what a projection did, for the caller to report. Skipped holds
// paths the manifest claims but that something else now occupies; the files
// there are always left untouched, whether or not the record survives.
type Result struct {
	Added   []string
	Removed []string
	Skipped []string
	// Disowned is the subset of Skipped whose manifest record was also
	// dropped. It exists because that is the one outcome the user cannot
	// discover afterwards: nothing tracks the path any more, so no later
	// command will mention it and it is theirs to remove by hand. Every
	// producer that drops a record must fill this, or the fact is lost.
	Disowned []string
	// Failed holds paths a refresh could not reproject. One agent's broken
	// directory must not abandon every other agent's links, so refresh records
	// the failure and carries on; the caller summarises it once rather than
	// repeating a raw Go error on every command forever.
	Failed []string
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
// The lock is what keeps a concurrent `orq launch` from losing this install's
// records (see lock.go); the work itself is in install.
func Install(agents []string) (*Result, error) {
	res := &Result{}
	err := withManifestLock(func() error {
		var err error
		res, err = install(agents)
		return err
	})
	return res, err
}

func install(agents []string) (*Result, error) {
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
				if !ourOrphan(path) {
					// Somebody else's skill by the same name. Theirs wins;
					// ours is not installed, and the caller says so.
					res.Skipped = append(res.Skipped, path)
					continue
				}
				// Our own link from a run that died before it could write the
				// manifest. Reporting it as foreign would leave it recorded
				// nowhere, so nothing could ever remove it — and once
				// generation collection retires the snapshot underneath it,
				// the user is left with a dangling symlink in their real home.
				// Adopt it: drop it and reproject onto the current generation,
				// which is what the manifest is about to claim.
				if err := removePath(path); err != nil {
					return nil, err
				}
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
//
// The Result is never nil, including when the lock could not be taken and the
// closure never ran: callers report the error and then still range over the
// result, and a nil here crashed `orq disconnect skills` against a live
// `orq launch`.
func Remove(agents []string) (*Result, error) {
	res := &Result{}
	err := withManifestLock(func() error {
		var err error
		res, err = remove(agents)
		return err
	})
	return res, err
}

func remove(agents []string) (*Result, error) {
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
			// Left in place, but the record goes: disconnect means stop
			// managing this path. Keeping it would strand an entry no command
			// can ever clear — refresh only drops a foreign path once its
			// skill also leaves the shipped set, and every later connect and
			// disconnect would skip it again in silence.
			res.Skipped = append(res.Skipped, l.Path)
			res.Disowned = append(res.Disowned, l.Path)
			gone = append(gone, l.Path)
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
//
// The fingerprint is compared before the lock is taken. Nothing is mutated by
// that comparison, and on every command but the one right after a CLI update
// it is the whole answer — so the common case neither creates the state
// directory nor waits out a lock another orq process happens to hold. refresh
// re-checks under the lock, where the decision to write is actually made.
func Refresh() (*Result, error) {
	m, err := LoadManifest()
	if err != nil || m == nil {
		return &Result{}, err
	}
	if m.Fingerprint == Fingerprint() {
		return &Result{}, nil
	}
	res := &Result{}
	err = withManifestLock(func() error {
		var err error
		res, err = refresh()
		return err
	})
	return res, err
}

func refresh() (*Result, error) {
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
		// Ownership is checked before anything is removed, including on the
		// prune path: the manifest records a path, not a promise that we still
		// own what sits there.
		if exists(l.Path) && !isOurs(l) {
			res.Skipped = append(res.Skipped, l.Path)
			if !inSet[l.Skill] {
				pruned = append(pruned, l.Path)
				res.Disowned = append(res.Disowned, l.Path)
			}
			continue
		}
		if !inSet[l.Skill] {
			// The skill left the shipped set. Without this the agent keeps
			// loading something we no longer ship, pointing at a generation
			// that collection will eventually delete.
			//
			// Safe to delete because the guard above already proved this is
			// ours, in both link modes: a symlink resolves into our own
			// snapshot, a copy carries our marker.
			if err := removePath(l.Path); err != nil {
				return &Result{}, err
			}
			pruned = append(pruned, l.Path)
			res.Removed = append(res.Removed, l.Path)
			continue
		}
		// Per-link tolerance from here down. A link that cannot be reprojected
		// — a directory the user replaced with a file, a permission change —
		// is one broken link, not a reason to leave every other agent on a
		// stale skill set. The manifest keeps the record either way, so the
		// missing-link warning still points the user at the repair.
		if err := removePath(l.Path); err != nil {
			res.Failed = append(res.Failed, l.Path)
			continue
		}
		if err := project(filepath.Join(gen, l.Skill), l.Path); err != nil {
			res.Failed = append(res.Failed, l.Path)
			continue
		}
		res.Added = append(res.Added, l.Path)
	}
	m.RemoveLinks(pruned)
	// The fingerprint is what Refresh compares to decide there is nothing to
	// do, so advancing it past a failed link makes that failure permanent: the
	// link is never retried and the warning naming it is printed once, ever.
	// A failure here is transient by nature — a directory the user replaced
	// with a file, a permission change — so leaving the fingerprint stale is
	// what retries it, and what keeps warning until the user repairs it.
	//
	// A skipped link is the opposite: the path is not ours, which is a
	// standing state, not drift. Retrying would reproject every healthy link
	// on every skills command to no purpose, so the fingerprint advances and
	// the state is reported instead — by the pre-run and by `orq doctor`.
	// Recording the generation is right either way: the links that did land
	// point into it.
	if len(res.Failed) == 0 {
		m.Fingerprint = Fingerprint()
	}
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
//
// Both branches create the parent directory first. The symlink branch used to
// assume it existed, which held for install (it calls MkdirAll per target) but
// not for refresh: a user who deleted their skills directory — which the spec
// promises is safe — left every later `orq` command failing on the same raw
// symlink error, with nothing that could ever converge.
func project(src, dest string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if symlinkSupported() {
		return os.Symlink(src, dest)
	}
	return projectCopy(src, dest, parent)
}

// projectCopy is the copy branch of project, split out so the platform it
// only ever runs on is not the only platform that can test it.
func projectCopy(src, dest, parent string) error {
	staging, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := CopyDir(src, staging); err != nil {
		return err
	}
	// Written into the staging tree, so it lands with the rename and a copy
	// at dest either has it or does not exist.
	if err := os.WriteFile(filepath.Join(staging, ownerMarker), []byte(ownerMarkerBody), 0o644); err != nil {
		return err
	}
	return os.Rename(staging, dest)
}

// ownerMarker is how a copy-mode projection proves it is ours. A symlink
// proves it by pointing into our own snapshot; a copy has no such property,
// and without a marker the only available check is "is a directory here",
// which every directory passes — including one the user put there.
//
// It lives inside the skill directory — that is what makes it land with the
// rename and survive a wholesale replace — so an agent enumerating the
// directory does see it; the leading dot only keeps it out of a casual
// listing. It says plainly what it is: the directory is replaced wholesale on
// every update, so anything the user writes into it is lost the next time a
// command that touches skills (`launch`, `connect`, `disconnect`, `setup`)
// sees a new skill set.
const (
	ownerMarker     = ".orq-owned"
	ownerMarkerBody = `This directory is created and owned by the orq CLI.

DO NOT EDIT ANYTHING IN IT. The orq CLI replaces this directory wholesale
whenever the shipped skill set changes, and your changes go with it.

Deleting this file makes the orq CLI treat the directory as yours: it will
stop updating it and stop removing it, and 'orq disconnect skills' will
leave it behind.
`
)

// ownsCopy reports whether the copy at path carries our marker.
func ownsCopy(path string) bool {
	_, err := os.Stat(filepath.Join(path, ownerMarker))
	return err == nil
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

// ourOrphan reports whether an unrecorded path is one this CLI created. There
// is no record here, so the evidence has to come from the path itself: a
// symlink into our own snapshot tree, or a copy carrying our marker. Anything
// else is left alone — treating a real directory as ours on a hunch is how a
// user's own work gets deleted.
func ourOrphan(path string) bool { return symlinkPointsIntoSnapshot(path) || ownsCopy(path) }

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
// For ModeCopy links it requires our own marker file inside the directory
// (see ownerMarker), which is what a copy has instead of a symlink's target.
// A user who put their own directory at one of our paths has no marker in it,
// so ownership means the same thing on both platforms.
//
// It still cannot tell a hand-edited copy from an untouched one — that would
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
	return info.IsDir() && ownsCopy(l.Path)
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
