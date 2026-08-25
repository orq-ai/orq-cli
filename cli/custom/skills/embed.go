// Package skills ships the orq agent skills inside the binary and installs
// them into the directories each supported coding agent reads.
package skills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"path"
	"sort"
	"sync"
)

// assets is the vendored skills tree. `all:` keeps dot-prefixed files, which
// the default embed pattern would silently drop.
//
//go:embed all:assets
var assets embed.FS

// buildFingerprint is set at release build time via
// -ldflags "-X orq/cli/custom/skills.buildFingerprint=<hex>". Dev builds leave
// it empty and pay for one hash of the embedded tree, once per process.
var buildFingerprint string

var (
	fingerprintOnce sync.Once
	computed        string
	testOverride    string
)

// Assets is the embedded tree rooted at the skills directory, so callers see
// skill names at the top level rather than an "assets" wrapper.
func Assets() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		// Impossible: the directory is embedded above and verified by tests.
		panic(err)
	}
	return sub
}

// Names lists the embedded skills, sorted, excluding the vendoring metadata.
func Names() ([]string, error) {
	entries, err := fs.ReadDir(Assets(), ".")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Fingerprint identifies this binary's skill set. It is a compiled-in constant
// on release builds, which is what makes the per-command staleness check a
// string comparison rather than a hash of the tree.
func Fingerprint() string {
	if testOverride != "" {
		return testOverride
	}
	if buildFingerprint != "" {
		return buildFingerprint
	}
	fingerprintOnce.Do(func() { computed = hashTree() })
	return computed
}

// hashTree hashes every embedded path and its contents in sorted order, so the
// result depends on names and bytes but not on walk order.
func hashTree() string {
	h := sha256.New()
	_ = fs.WalkDir(Assets(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
		data, readErr := fs.ReadFile(Assets(), p)
		if readErr != nil {
			return readErr
		}
		h.Write(data)
		h.Write([]byte{0})
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// SkillDir is the path of one skill inside the embedded tree.
func SkillDir(name string) string { return path.Join(".", name) }
