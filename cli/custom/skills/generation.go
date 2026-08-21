package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// generationsKept is the current generation plus one previous. One previous
// makes a manual rollback possible while this is young, and the whole tree is
// under a megabyte.
const generationsKept = 2

// Home is the orq state directory. It is not the install directory: the
// installer puts the binary in ~/.orq/bin, and this sits beside it.
func Home() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".orq"), nil
}

func generationDir(fingerprint string) (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "snapshot", "gen-"+fingerprint), nil
}

// EnsureGeneration unpacks the embedded tree for the current fingerprint and
// returns its directory. It is idempotent: an existing generation is returned
// untouched, which is the common path on every invocation after the first.
//
// The tree is written to a sibling temp directory and renamed into place, so a
// crash mid-write leaves no half-populated generation for anything to link to.
func EnsureGeneration() (string, error) {
	dir, err := generationDir(Fingerprint())
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)

	if err := copyTree(Assets(), staging); err != nil {
		return "", err
	}
	if err := os.Rename(staging, dir); err != nil {
		// A concurrent process winning the same race is success, not failure:
		// the directory it created holds the same content-addressed bytes.
		if _, statErr := os.Stat(dir); statErr == nil {
			return dir, nil
		}
		return "", err
	}
	if err := collectGenerations(generationsKept); err != nil {
		// Collection is housekeeping. Failing it must not fail the install.
		return dir, nil
	}
	return dir, nil
}

func copyTree(src fs.FS, dest string) error {
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, readErr := fs.ReadFile(src, p)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// collectGenerations removes all but the newest `keep` generations by
// modification time. The current one is newest by construction, so it survives.
func collectGenerations(keep int) error {
	home, err := Home()
	if err != nil {
		return err
	}
	root := filepath.Join(home, "snapshot")
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	type gen struct {
		name string
		mod  int64
	}
	var gens []gen
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "gen-") {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		gens = append(gens, gen{e.Name(), info.ModTime().UnixNano()})
	}
	sort.Slice(gens, func(i, j int) bool { return gens[i].mod > gens[j].mod })
	for _, g := range gens[min(keep, len(gens)):] {
		if err := os.RemoveAll(filepath.Join(root, g.name)); err != nil {
			return err
		}
	}
	return nil
}
