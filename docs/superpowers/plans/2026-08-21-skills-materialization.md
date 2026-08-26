# Skills Materialization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the orq agent skills inside the CLI binary and install them into every supported coding agent through a new `skills` capability on `orq connect`.

**Architecture:** The skills tree is vendored from `orq-ai/assistant-plugins` at release time and embedded with `go:embed`. A new `cli/custom/skills` package materializes that tree into a content-addressed generation directory under the orq home, then projects it into each agent's skills directory as per-skill symlinks (copies where symlinks are unavailable), recording every link in a manifest. `orq connect skills` / `orq disconnect skills` drive it, the commands that touch skills (`launch`, `connect`, `disconnect`, `setup`) refresh stale views the manifest already records, `orq doctor` names what does not converge on its own, and `orq launch` gets skills for the session only.

**Tech Stack:** Go 1.25, module `orq`, cobra commands under `cli/custom/commands`, launch plans under `cli/custom/launch`, stdlib for the new package (`embed`, `crypto/sha256`, `encoding/json`, `os`, `path/filepath`), plus `golang.org/x/sys` for the Windows file lock — `LockFileEx` has no stdlib binding, and the module was already an indirect dependency.

**Spec:** `.context/RES-1409-spec.md` (Linear RES-1409)

## Global Constraints

- The `skills` capability name is exactly `skills`. The existing `orq skills` generated command group is untouched by this work; it means platform skill entities, which are a different noun.
- Every skill directory keeps its `orq-` prefix in every target directory, exactly as it is named in the vendored tree.
- The CLI removes or replaces only paths recorded in its own manifest. It never clears a target directory and never overwrites a path that is not our own link.
- Materialization failure is fatal in `connect` (non-zero exit) and non-fatal but loudly reported in `launch`.
- The staleness check updates only views the manifest already records and creates nothing new. No manifest means no filesystem writes at all. It runs on the skills-touching commands only (`launch`, `connect`, `disconnect`, `setup`), not on every `orq` invocation: a manifest read and a lock in front of `orq --help` bought convergence for a case `orq doctor` reports instead.
- The manifest is written last, after links are in place, so an interrupted run re-runs cleanly.
- Manifest schema version is `1`. Any manifest with a different version is treated as foreign and left alone.
- Reporter output for skills uses the existing connect line format: `%-8s %-9s %s` (agent, capability, path).
- Tests redirect `HOME` with `t.Setenv("HOME", t.TempDir())`, the convention already used throughout `cli/custom/commands`.
- `orq launch` leaves no session link and no agent-visible state behind: everything it projects lives inside the launcher-owned temp directory that `Cleanup` removes, or is unlinked on exit. The shared `~/.orq/snapshot/gen-<fingerprint>/` generation is the one deliberate exception — the spec mandates it, it is content-addressed, capped at two generations, and holds nothing sensitive, so a launch may create it and must not remove it.

---

## Design Note: two classes of launch agent

Reading `cli/custom/launch` turned up a simplification worth stating before the tasks. `orq launch kimi` already sets `KIMI_CODE_HOME` to a temp directory, and `orq launch pi` already sets `PI_CODING_AGENT_DIR` to a temp directory, each with a `Cleanup` that removes it. For those two agents, session skills are just files written into a directory the CLI already owns and already deletes. There is no link into the user's real home, so no reference count, no process sweep, and no cleanup risk.

Only `claude`, `codex`, `opencode`, and `kilo` need session links in real directories, and only those need the refcount and PID sweep. Tasks 9 and 10 split along that line.

One trap: `LaunchPlan.Cleanup` is a single `func()` field, and `claude.go`, `codex.go`, `kimi.go`, and `pi.go` all assign it directly. Task 10 must chain rather than overwrite.

## File Structure

- Create `cli/custom/skills/embed.go` — the `go:embed` of the vendored tree, the build-time fingerprint variable, and the lazy dev-build fingerprint. One responsibility: what is shipped and what its identity is.
- Create `cli/custom/skills/generation.go` — writing and collecting generation directories under the orq home.
- Create `cli/custom/skills/manifest.go` — the manifest schema and its atomic read/write.
- Create `cli/custom/skills/targets.go` — resolving which directories receive skills for a given set of agents.
- Create `cli/custom/skills/project.go` — creating, pruning, and removing links, including the copy fallback and the ownership rules.
- Create `cli/custom/skills/session.go` — session-scoped links, the reference count, and the dead-process sweep.
- Create `cli/custom/skills/skills_test.go` — unit coverage for target resolution and manifest round-trips, the parts with no command surface.
- Modify `cli/custom/commands/connect.go` — add `capSkills` to the capability list, the dry-run branch, `wiredTargets`, and `removeWiring`.
- Modify `cli/custom/commands/setup.go` — the capability multi-select and the skills step inside `instrumentAgents`.
- Modify `cli/custom/commands/run.go` (or wherever the root command is assembled) — the staleness check.
- Modify `cli/custom/launch/agents.go` — chainable cleanup.
- Modify `cli/custom/launch/kimi.go`, `pi.go`, `claude.go`, `codex.go`, `opencode.go` — session skills.
- Modify `cli/custom/commands/connect_test.go` — command-level tests through the temp-HOME seam.
- Create `scripts/vendor-skills.sh` — the release-time sync from `assistant-plugins`.

---

### Task 1: Vendored tree, embed, and fingerprint

**Files:**
- Create: `scripts/vendor-skills.sh`
- Create: `cli/custom/skills/embed.go`
- Create: `cli/custom/skills/skills_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: nothing.
- Produces: `skills.Fingerprint() string`, `skills.Assets() fs.FS`, `skills.Names() ([]string, error)`, and the test hook `skills.SetFingerprintForTest(t *testing.T, fp string)`.

- [ ] **Step 1: Vendor the skills tree**

The vendored tree lives at `cli/custom/skills/assets/<skill-name>/SKILL.md` and is committed. Write `scripts/vendor-skills.sh`:

```bash
#!/usr/bin/env bash
# Syncs the skills tree from orq-ai/assistant-plugins into the CLI for embedding.
# Run at release time; the result is committed so builds are hermetic.
set -euo pipefail

REPO="${ORQ_SKILLS_REPO:-https://github.com/orq-ai/assistant-plugins.git}"
REF="${1:?usage: vendor-skills.sh <git-ref>}"
DEST="cli/custom/skills/assets"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

git clone --quiet --depth 1 "$REPO" "$tmp/src"
git -C "$tmp/src" fetch --quiet --depth 1 origin "$REF"
git -C "$tmp/src" checkout --quiet FETCH_HEAD

rm -rf "$DEST"
mkdir -p "$DEST"
cp -R "$tmp/src/skills/." "$DEST/"

resolved="$(git -C "$tmp/src" rev-parse HEAD)"
cat > "$DEST/SOURCE.json" <<JSON
{"repo": "$REPO", "ref": "$REF", "commit": "$resolved"}
JSON

echo "vendored $(find "$DEST" -maxdepth 1 -mindepth 1 -type d | wc -l | tr -d ' ') skills from $resolved"
```

Make it executable and run it once against the current pinned SHA so the tree is committed:

```bash
chmod +x scripts/vendor-skills.sh
./scripts/vendor-skills.sh 415edd51ddba3b10d4e3091c6d91b0cbca57566b
```

- [ ] **Step 2: Write the failing test**

Create `cli/custom/skills/skills_test.go`:

```go
package skills

import (
	"strings"
	"testing"
)

func TestNamesAreOrqPrefixedAndNonEmpty(t *testing.T) {
	names, err := Names()
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no skills embedded; run scripts/vendor-skills.sh")
	}
	for _, n := range names {
		if !strings.HasPrefix(n, "orq-") && n != "evaluatorq" {
			t.Errorf("skill %q is neither orq-prefixed nor the known exception", n)
		}
	}
}

func TestFingerprintIsStableAndNonEmpty(t *testing.T) {
	a := Fingerprint()
	if a == "" {
		t.Fatal("empty fingerprint")
	}
	if b := Fingerprint(); a != b {
		t.Errorf("fingerprint not stable: %q then %q", a, b)
	}
}

func TestSetFingerprintForTestOverridesAndRestores(t *testing.T) {
	original := Fingerprint()
	t.Run("override", func(t *testing.T) {
		SetFingerprintForTest(t, "deadbeef")
		if got := Fingerprint(); got != "deadbeef" {
			t.Errorf("Fingerprint() = %q, want deadbeef", got)
		}
	})
	if got := Fingerprint(); got != original {
		t.Errorf("fingerprint not restored: got %q, want %q", got, original)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./cli/custom/skills/ -run TestNames -v`
Expected: FAIL, the package does not compile because `Names` is undefined.

- [ ] **Step 4: Write the implementation**

Create `cli/custom/skills/embed.go`:

```go
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
	"testing"
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

// SetFingerprintForTest makes a test look like a different binary, which is how
// the update path is exercised without rebuilding.
func SetFingerprintForTest(t *testing.T, fp string) {
	t.Helper()
	prev := testOverride
	testOverride = fp
	t.Cleanup(func() { testOverride = prev })
}

// SkillDir is the path of one skill inside the embedded tree.
func SkillDir(name string) string { return path.Join(".", name) }
```

- [ ] **Step 5: Add the ldflags to the release build**

In `Makefile`, extend the release link flags so the fingerprint is compiled in. Find the existing `-X main.version=` flag and add alongside it:

```make
SKILLS_FP := $(shell find cli/custom/skills/assets -type f | sort | xargs shasum -a 256 | shasum -a 256 | cut -c1-16)
LDFLAGS := -X main.version=$(VERSION) -X orq/cli/custom/skills.buildFingerprint=$(SKILLS_FP)
```

The value only has to be stable and change when the tree changes; it does not have to match `hashTree` exactly, because nothing ever compares a build-time fingerprint against a computed one.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./cli/custom/skills/ -v`
Expected: PASS, all three tests.

- [ ] **Step 7: Commit**

```bash
git add scripts/vendor-skills.sh cli/custom/skills Makefile
git commit -m "feat(skills): vendor and embed the orq skills tree"
```

---

### Task 2: Generation directories

**Files:**
- Create: `cli/custom/skills/generation.go`
- Modify: `cli/custom/skills/skills_test.go`

**Interfaces:**
- Consumes: `skills.Fingerprint()`, `skills.Assets()` from Task 1.
- Produces: `skills.Home() (string, error)`, `skills.EnsureGeneration() (string, error)` returning the absolute path of the generation directory for the current fingerprint, and `skills.collectGenerations(keep int) error`.

- [ ] **Step 1: Write the failing test**

Append to `cli/custom/skills/skills_test.go`:

```go
func TestEnsureGenerationIsIdempotentAndComplete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := EnsureGeneration()
	if err != nil {
		t.Fatalf("EnsureGeneration: %v", err)
	}
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, statErr := os.Stat(filepath.Join(first, n, "SKILL.md")); statErr != nil {
			t.Errorf("skill %q missing from generation: %v", n, statErr)
		}
	}

	second, err := EnsureGeneration()
	if err != nil {
		t.Fatalf("second EnsureGeneration: %v", err)
	}
	if first != second {
		t.Errorf("same fingerprint produced two generations: %q then %q", first, second)
	}
}

func TestGenerationCollectionKeepsCurrentAndOnePrevious(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for _, fp := range []string{"aaa", "bbb", "ccc"} {
		SetFingerprintForTest(t, fp)
		if _, err := EnsureGeneration(); err != nil {
			t.Fatalf("EnsureGeneration(%s): %v", fp, err)
		}
	}
	home, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("kept %d generations, want 2", len(entries))
	}
	if _, err := os.Stat(filepath.Join(home, "snapshot", "gen-ccc")); err != nil {
		t.Errorf("current generation collected: %v", err)
	}
}
```

Add `"os"` and `"path/filepath"` to the test imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cli/custom/skills/ -run TestEnsureGeneration -v`
Expected: FAIL, `EnsureGeneration` undefined.

- [ ] **Step 3: Write the implementation**

Create `cli/custom/skills/generation.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cli/custom/skills/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/skills
git commit -m "feat(skills): materialize content-addressed generations"
```

---

### Task 3: The manifest

**Files:**
- Create: `cli/custom/skills/manifest.go`
- Modify: `cli/custom/skills/skills_test.go`

**Interfaces:**
- Consumes: `skills.Home()` from Task 2.
- Produces: types `skills.Manifest`, `skills.Link`, `skills.Session`; functions `skills.LoadManifest() (*Manifest, error)`, `skills.SaveManifest(*Manifest) error`, `(*Manifest).OwnedPaths() []string`, `(*Manifest).AddLink(Link)`, `(*Manifest).RemoveLinks(paths []string)`.

- [ ] **Step 1: Write the failing test**

Append to `cli/custom/skills/skills_test.go`:

```go
func TestManifestRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if m, err := LoadManifest(); err != nil {
		t.Fatalf("LoadManifest on a clean machine: %v", err)
	} else if m != nil {
		t.Fatalf("LoadManifest on a clean machine returned %+v, want nil", m)
	}

	m := &Manifest{Version: manifestVersion, Fingerprint: "aaa", Generation: "/gen-aaa"}
	m.AddLink(Link{Path: "/home/u/.claude/skills/orq-build-agent", Agent: "claude", Skill: "orq-build-agent", Mode: ModeSymlink})
	if err := SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	got, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if got.Fingerprint != "aaa" || len(got.Links) != 1 || got.Links[0].Agent != "claude" {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestManifestOfAnUnknownVersionIsTreatedAsForeign(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	orqHome := filepath.Join(home, ".orq")
	if err := os.MkdirAll(orqHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orqHome, "materialized-skills.json"),
		[]byte(`{"version":99,"links":[{"path":"/somewhere"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m != nil {
		t.Errorf("a version-99 manifest was adopted: %+v", m)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cli/custom/skills/ -run TestManifest -v`
Expected: FAIL, `Manifest` undefined.

- [ ] **Step 3: Write the implementation**

Create `cli/custom/skills/manifest.go`:

```go
package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
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
// lets a later invocation tell a running session from a crashed one.
type Session struct {
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

// LoadManifest returns nil, nil when there is nothing to load: no file, an
// unreadable file, or a version this binary does not understand. Every caller
// treats nil as "this machine has never connected skills", which is the case
// where we must not touch the filesystem at all.
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

func (m *Manifest) AddLink(l Link) {
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
		drop[p] = true
	}
	kept := m.Links[:0]
	for _, l := range m.Links {
		if !drop[l.Path] {
			kept = append(kept, l)
		}
	}
	m.Links = kept
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cli/custom/skills/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/skills
git commit -m "feat(skills): manifest schema and atomic persistence"
```

---

### Task 4: Target resolution

**Files:**
- Create: `cli/custom/skills/targets.go`
- Modify: `cli/custom/skills/skills_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: type `skills.Target{Agent, Dir string}` and `skills.Targets(agents []string) ([]Target, error)`.

Agents that read the shared agents-spec directory are `opencode`, `kilo`, and `pi`. Agents that need their own directory are `claude`, `codex`, and `kimi`.

- [ ] **Step 1: Write the failing test**

Append to `cli/custom/skills/skills_test.go`:

```go
func TestTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("KIMI_CODE_HOME", "")

	dirs := func(agents ...string) []string {
		got, err := Targets(agents)
		if err != nil {
			t.Fatalf("Targets(%v): %v", agents, err)
		}
		var out []string
		for _, tg := range got {
			out = append(out, strings.TrimPrefix(tg.Dir, home))
		}
		sort.Strings(out)
		return out
	}

	if got := dirs("claude"); strings.Join(got, ",") != "/.claude/skills" {
		t.Errorf("claude alone = %v, want only its own directory", got)
	}
	if got := dirs("pi"); strings.Join(got, ",") != "/.agents/skills" {
		t.Errorf("pi alone = %v, want only the shared directory", got)
	}
	if got := dirs("opencode", "pi", "kilo"); strings.Join(got, ",") != "/.agents/skills" {
		t.Errorf("three shared readers = %v, want one shared directory", got)
	}
	if got := dirs("claude", "codex", "kimi"); strings.Join(got, ",") != "/.claude/skills,/.codex/skills,/.kimi-code/skills" {
		t.Errorf("three own-directory agents = %v", got)
	}
}

func TestTargetsHonorAgentHomeEnvironmentVariables(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	kimiHome := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", kimiHome)
	t.Setenv("CODEX_HOME", codexHome)

	got, err := Targets([]string{"kimi", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"kimi":  filepath.Join(kimiHome, "skills"),
		"codex": filepath.Join(codexHome, "skills"),
	}
	for _, tg := range got {
		if want[tg.Agent] != tg.Dir {
			t.Errorf("%s target = %q, want %q", tg.Agent, tg.Dir, want[tg.Agent])
		}
	}
}
```

Add `"sort"` to the test imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cli/custom/skills/ -run TestTargets -v`
Expected: FAIL, `Targets` undefined.

- [ ] **Step 3: Write the implementation**

Create `cli/custom/skills/targets.go`:

```go
package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Target is one directory that receives the skill set. Agent is empty for the
// shared agents-spec directory, which several agents read at once.
type Target struct {
	Agent string
	Dir   string
}

// sharedReaders read the agents-spec directory, so they get no directory of
// their own. Writing both would put the same skill in one agent's index twice.
var sharedReaders = map[string]bool{"opencode": true, "kilo": true, "pi": true}

// ownDir resolves the skills directory for an agent that does not read the
// shared one. Each honors the same home variable the rest of the CLI honors
// for that agent, so a configured home is not silently written past.
var ownDir = map[string]func() (string, error){
	"claude": func() (string, error) { return underHome(".claude", "skills") },
	"codex":  func() (string, error) { return underAgentHome("CODEX_HOME", ".codex", "skills") },
	"kimi":   func() (string, error) { return underAgentHome("KIMI_CODE_HOME", ".kimi-code", "skills") },
}

func underHome(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, parts...)...), nil
}

func underAgentHome(envKey, fallbackRel string, parts ...string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv(envKey)); dir != "" {
		return filepath.Join(append([]string{dir}, parts...)...), nil
	}
	return underHome(append([]string{fallbackRel}, parts...)...)
}

// Targets resolves the directories that serve the given agents. The shared
// agents-spec directory is included only when at least one selected agent
// actually reads it, so a claude-only machine never grows a ~/.agents tree.
func Targets(agents []string) ([]Target, error) {
	var out []Target
	shared := false
	kilo := false
	for _, id := range agents {
		if sharedReaders[id] {
			shared = true
			kilo = kilo || id == "kilo"
			continue
		}
		resolve, ok := ownDir[id]
		if !ok {
			continue
		}
		dir, err := resolve()
		if err != nil {
			return nil, err
		}
		out = append(out, Target{Agent: id, Dir: dir})
	}
	if shared {
		dir, err := underHome(".agents", "skills")
		if err != nil {
			return nil, err
		}
		out = append(out, Target{Dir: dir})
	}
	// kilo documents an XDG location on Linux in addition to ~/.agents, and
	// reads whichever it finds. Writing only the home-relative one leaves kilo
	// silently empty on distributions where it looks in XDG first.
	if kilo && runtime.GOOS == "linux" {
		base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			base = filepath.Join(home, ".config")
		}
		out = append(out, Target{Agent: "kilo", Dir: filepath.Join(base, "agents", "skills")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cli/custom/skills/ -v`
Expected: PASS. Note the `TestTargets` shared-directory cases assert on a non-Linux path shape; on Linux the kilo case adds the XDG directory, so run the suite on the platform you develop on and let CI cover the other.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/skills
git commit -m "feat(skills): resolve per-agent skill directories"
```

---

### Task 5: Projection, pruning, and ownership

**Files:**
- Create: `cli/custom/skills/project.go`
- Modify: `cli/custom/skills/skills_test.go`

**Interfaces:**
- Consumes: `EnsureGeneration`, `Targets`, `Manifest`, `Link`, `LoadManifest`, `SaveManifest`.
- Produces: `skills.Install(agents []string) (*Result, error)`, `skills.Remove(agents []string) (*Result, error)`, `skills.Refresh() (*Result, error)`, and `type Result struct { Added, Removed, Skipped []string }`.

`Skipped` holds paths we own in the manifest but found replaced by something that is not our link. Those are reported and left alone.

- [ ] **Step 1: Write the failing test**

Append to `cli/custom/skills/skills_test.go`:

```go
func TestInstallCreatesOneLinkPerSkillPerTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	res, err := Install([]string{"claude"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	names, _ := Names()
	if len(res.Added) != len(names) {
		t.Errorf("added %d links, want %d", len(res.Added), len(names))
	}
	for _, n := range names {
		p := filepath.Join(home, ".claude", "skills", n)
		if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
			t.Errorf("skill %q not readable through the link: %v", n, err)
		}
	}
}

func TestInstallLeavesForeignEntriesAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(filepath.Join(dir, "my-own-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "my-own-skill", "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "my-own-skill", "SKILL.md"))
	if err != nil || string(data) != "mine" {
		t.Errorf("install disturbed a skill it does not own: %v %q", err, data)
	}

	if _, err := Remove([]string{"claude"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "my-own-skill", "SKILL.md")); err != nil {
		t.Errorf("remove deleted a skill it does not own: %v", err)
	}
}

func TestRefreshPrunesSkillsThatLeftTheSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	dir := filepath.Join(home, ".claude", "skills")
	ghost := filepath.Join(dir, "orq-retired-skill")
	if err := os.Symlink(filepath.Join(home, "nowhere"), ghost); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("LoadManifest: %v %v", m, err)
	}
	m.AddLink(Link{Path: ghost, Agent: "claude", Skill: "orq-retired-skill", Mode: ModeSymlink})
	if err := SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	SetFingerprintForTest(t, "next-release")
	if _, err := Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := os.Lstat(ghost); !os.IsNotExist(err) {
		t.Errorf("a skill no longer in the set survived the refresh: %v", err)
	}
}

func TestRefreshOnANeverConnectedMachineTouchesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Error("refresh created an agent directory on a machine that never connected")
	}
	if _, err := os.Stat(filepath.Join(home, ".orq")); !os.IsNotExist(err) {
		t.Error("refresh created orq state on a machine that never connected")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cli/custom/skills/ -run TestInstall -v`
Expected: FAIL, `Install` undefined.

- [ ] **Step 3: Write the implementation**

Create `cli/custom/skills/project.go`:

```go
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
	return copyDir(src, dest)
}

func copyDir(src, dest string) error {
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cli/custom/skills/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cli/custom/skills
git commit -m "feat(skills): project, prune, and remove skill views"
```

---

### Task 6: The `skills` connect capability

**Files:**
- Modify: `cli/custom/commands/connect.go:16-20` (capability constants and list), `dryRunConnect`, `wiredTargets`, `removeWiring`
- Modify: `cli/custom/commands/setup.go` (`instrumentAgents`)
- Modify: `cli/custom/commands/connect_test.go`

**Interfaces:**
- Consumes: `skills.Install`, `skills.Remove`, `skills.Targets`, `skills.LoadManifest` from Tasks 4 and 5.
- Produces: the constant `capSkills = "skills"` and its presence in `connectCapabilities`.

- [ ] **Step 1: Write the failing test**

Append to `cli/custom/commands/connect_test.go`:

```go
func TestConnectSkillsInstallsAndDisconnectRemoves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect claude skills: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no skills installed: %v %d", err, len(entries))
	}

	d := NewDisconnectCommand()
	d.SetArgs([]string{"claude", "skills", "--yes"})
	if err := d.Execute(); err != nil {
		t.Fatalf("disconnect claude skills: %v", err)
	}
	entries, err = os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err == nil && len(entries) != 0 {
		t.Errorf("disconnect left %d entries behind", len(entries))
	}
}

func TestConnectSkillsDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills", "--dry-run"})
	if err := c.Execute(); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Error("dry run created the skills directory")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cli/custom/commands/ -run TestConnectSkills -v`
Expected: FAIL with an error containing `"skills" is neither an agent`, because the capability does not parse yet.

- [ ] **Step 3: Register the capability**

In `cli/custom/commands/connect.go`, extend the constants and the list:

```go
const (
	capGateway = "gateway"
	capTracing = "tracing"
	capSkills  = "skills"
)

var connectCapabilities = []string{capGateway, capTracing, capSkills}
```

`dropUnavailableCaps` already strips only `capTracing`, so `capSkills` passes through untouched. Leave it alone.

- [ ] **Step 4: Install during connect**

In `cli/custom/commands/setup.go`, inside `instrumentAgents`, after the per-agent loop that writes MCP and provider config, add the skills step. It runs once for the whole selected set, because `skills.Targets` deduplicates the shared directory across agents:

```go
	if hasCap(opts.caps, capSkills) {
		res, err := skills.Install(selected)
		if err != nil {
			// Connect's only job for this capability is the thing that just
			// failed, so it is fatal here. launch degrades instead.
			return nil, fmt.Errorf("installing skills: %w", err)
		}
		for _, target := range skillTargetsFor(selected) {
			rep.ok("%-8s %-9s %s", target.Agent, capSkills, tilde(target.Dir))
		}
		for _, path := range res.Skipped {
			rep.warn("%-8s %-9s %s already exists and is not ours — left alone", "", capSkills, tilde(path))
		}
	}
```

Add a small helper beside it, so the reporting loop reads the same resolution the install used:

```go
// skillTargetsFor is the reporting view of skills.Targets: resolution failures
// are already fatal in Install, so a second failure here is not worth a second
// error path.
func skillTargetsFor(agents []string) []skills.Target {
	targets, err := skills.Targets(agents)
	if err != nil {
		return nil
	}
	return targets
}
```

`setupOptions` needs a `caps []string` field for this. Add it beside `agents`, and set it in `connectSelected` next to the existing `opts.noGateway` assignment:

```go
	opts.agents = agents
	opts.caps = caps
	opts.noGateway = !hasCap(caps, capGateway)
```

For the `orq setup` path, Task 11 sets `opts.caps`. Until then, `setup` leaves it empty and installs no skills, which is the behavior that exists today.

Import `"orq/cli/custom/skills"` in both files.

- [ ] **Step 5: Show skills in the dry run**

In `dryRunConnect`, after the gateway branch inside the agent loop, add a skills branch outside it (once for the whole set, not per agent). Restructure the tail of the function:

```go
	if hasCap(caps, capSkills) {
		for _, target := range skillTargetsFor(agents) {
			rep.info("%-8s skills    %s", target.Agent, tilde(target.Dir))
		}
	}
	return nil
```

- [ ] **Step 6: Report skills in `wiredTargets` and remove them in `removeWiring`**

In `wiredTargets`, after the gateway branch, add the skills branch. Skills are wired when the manifest says so, not when a file happens to exist:

```go
	if hasCap(caps, capSkills) {
		m, err := skills.LoadManifest()
		if err == nil && m != nil {
			seen := map[string]bool{}
			wanted := map[string]bool{}
			for _, id := range agents {
				wanted[id] = true
			}
			for _, l := range m.Links {
				if l.Session {
					continue
				}
				// An empty agent is the shared directory, which serves any
				// selected agent that reads it.
				if l.Agent != "" && !wanted[l.Agent] {
					continue
				}
				dir := filepath.Dir(l.Path)
				if seen[dir] {
					continue
				}
				seen[dir] = true
				out = append(out, wiredTarget{l.Agent, capSkills, dir})
			}
		}
	}
```

Add `"path/filepath"` to the imports of `connect.go`.

In `removeWiring`, the existing `remove` closure is shaped around per-agent provider files, so skills get their own call after the loop over agents. At the end of `removeWiring`, before `return rows, failed`:

```go
	if hasCap(caps, capSkills) {
		res, err := skills.Remove(agents)
		switch {
		case err != nil:
			rep.fail("%-8s %-9s %v", "", capSkills, err)
			failed = true
		case len(res.Removed) > 0:
			rep.ok("orq skills removed (%d entries)", len(res.Removed))
		}
		for _, path := range res.Skipped {
			rep.warn("%s is no longer ours — left alone", tilde(path))
		}
	}
```

Guard the `res` uses against a nil result when `err != nil` by returning early from that branch.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./cli/custom/commands/ -run TestConnectSkills -v`
Expected: PASS, both tests.

- [ ] **Step 8: Run the whole command suite**

Run: `go test ./cli/custom/...`
Expected: PASS. `TestPartitionConnectArgs` has a `"caps only"` case listing the known capabilities; update its expectation to include `skills` if it asserts on the full list.

- [ ] **Step 9: Commit**

```bash
git add cli/custom/commands
git commit -m "feat(connect): add the skills capability"
```

---

### Task 7: Per-agent status

**Files:**
- Modify: `cli/custom/commands/connect.go` (`runConnectStatus`)
- Modify: `cli/custom/commands/connect_test.go`

**Interfaces:**
- Consumes: `wiredTargets` from Task 6.
- Produces: no new exported surface; changes the shape of `--status` output.

Status currently prints one flat line per wired target. It becomes grouped by agent: the agent, its config file, then its live capabilities.

- [ ] **Step 1: Write the failing test**

Append to `cli/custom/commands/connect_test.go`:

```go
func TestConnectStatusGroupsByAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	out := captureOutput(t, func() {
		s := NewConnectCommand()
		s.SetArgs([]string{"claude", "--status"})
		if err := s.Execute(); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "claude") || !strings.Contains(out, "skills") {
		t.Errorf("status did not report claude's skills:\n%s", out)
	}
}
```

If `captureOutput` does not already exist in the package's tests, add it beside this test:

```go
// captureOutput collects what a command writes to stdout, so a test can assert
// on the report rather than on the filesystem.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = prev }()
	fn()
	w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
```

Add `"io"` to the test imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cli/custom/commands/ -run TestConnectStatusGroupsByAgent -v`
Expected: FAIL, status does not mention skills.

- [ ] **Step 3: Group the output**

In `runConnectStatus`, replace the flat printing loop:

```go
	for _, w := range wired {
		rep.info("%-8s %-9s %s", w.agent, w.capability, tilde(w.path))
	}
```

with a grouped one:

```go
	byAgent := map[string][]wiredTarget{}
	var order []string
	for _, w := range wired {
		// The shared agents-spec directory has no single owner, so it is
		// reported under its own heading rather than attributed to one agent.
		key := w.agent
		if key == "" {
			key = "shared"
		}
		if _, seen := byAgent[key]; !seen {
			order = append(order, key)
		}
		byAgent[key] = append(byAgent[key], w)
	}
	for _, agent := range order {
		rep.info("%s", agent)
		for _, w := range byAgent[agent] {
			rep.info("  %-9s %s", w.capability, tilde(w.path))
		}
	}
```

- [ ] **Step 4: Report broken links**

Still in `runConnectStatus`, after the grouped loop, flag manifest entries whose path is gone or no longer ours:

```go
	if m, mErr := skills.LoadManifest(); mErr == nil && m != nil {
		for _, l := range m.Links {
			if l.Session {
				continue
			}
			if _, statErr := os.Lstat(l.Path); statErr != nil {
				rep.warn("skills   %s is recorded but missing — run 'orq connect skills' to restore it", tilde(l.Path))
			}
		}
	}
```

Add `"os"` to the imports of `connect.go`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cli/custom/commands/ -run TestConnectStatus -v`
Expected: PASS, including the existing `TestConnectStatusIsReadOnly`.

- [ ] **Step 6: Commit**

```bash
git add cli/custom/commands
git commit -m "feat(connect): group --status output by agent"
```

---

### Task 8: Refresh on the commands that touch skills

> **Superseded.** This task was implemented as an unconditional
> `PersistentPreRun`, and that put the whole skills path — a manifest read, a
> lock, a directory walk — in front of every `orq` invocation, including
> `orq --help`. Every data-loss bug in the package was reachable from a command
> with nothing to do with skills. The refresh now runs for `launch`, `connect`,
> `disconnect` and `setup` only (see `skillsCommand` in `cli/custom/register.go`),
> and `orq doctor` names the drift that scoping leaves behind. Read the steps
> below for the refresh logic, not for where it is wired.

**Files:**
- Modify: `cli/custom/register.go` (not `run.go`: the hook lives in
  `installSkillsRefreshPreRun`)
- Modify: `cli/custom/commands/connect_test.go`

**Interfaces:**
- Consumes: `skills.Refresh` from Task 5.
- Produces: no new exported surface.

- [ ] **Step 1: Write the failing test**

Append to `cli/custom/commands/connect_test.go`:

```go
func TestAnUpdatedBinaryRelinksOnTheNextCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	resetSetupMemos(t)

	c := NewConnectCommand()
	c.SetArgs([]string{"claude", "skills"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	before, err := skills.LoadManifest()
	if err != nil || before == nil {
		t.Fatalf("manifest after connect: %v %v", before, err)
	}

	skills.SetFingerprintForTest(t, "a-later-release")
	if _, err := skills.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	after, err := skills.LoadManifest()
	if err != nil || after == nil {
		t.Fatalf("manifest after refresh: %v %v", after, err)
	}
	if after.Fingerprint != "a-later-release" {
		t.Errorf("fingerprint = %q, want the new one", after.Fingerprint)
	}
	if after.Generation == before.Generation {
		t.Error("refresh did not move to a new generation")
	}
	names, _ := skills.Names()
	for _, n := range names {
		p := filepath.Join(home, ".claude", "skills", n, "SKILL.md")
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("%s unreadable after refresh: %v", n, statErr)
		}
	}
}
```

Add `"orq/cli/custom/skills"` to the test imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cli/custom/commands/ -run TestAnUpdatedBinary -v`
Expected: FAIL, the package does not compile until the import is used by real code, or the assertion on the new generation fails.

- [ ] **Step 3: Wire the refresh into the root command**

In `cli/custom/run.go`, find where the root command is built and add a `PersistentPreRun` that refreshes before any subcommand runs. It must never fail a command:

```go
	// Skills installed by a previous binary are refreshed here rather than in
	// connect, so someone who updates the CLI and then opens their agent
	// directly is not left on the old set. This only ever updates views the
	// manifest already records: a machine that never connected has no manifest
	// and this returns before touching the filesystem.
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		res, err := skills.Refresh()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not refresh orq skills: %v\n", err)
			return
		}
		if len(res.Added) > 0 || len(res.Removed) > 0 {
			fmt.Fprintf(os.Stderr, "orq skills updated to match this CLI version (%d installed, %d removed)\n",
				len(res.Added), len(res.Removed))
		}
	}
	if err := skills.SweepDeadSessions(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not clean up stale session skills: %v\n", err)
	}
```

`SweepDeadSessions` arrives in Task 10. Until then, comment out that call or land Task 10 first; do not leave a reference to a function that does not exist.

Import `"orq/cli/custom/skills"`, `"fmt"`, and `"os"` as needed.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cli/custom/... -v -run TestAnUpdatedBinary`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cli/custom
git commit -m "feat(skills): refresh installed skills when the binary changes"
```

---

### Task 9: Session skills for the redirected-home agents

**Files:**
- Modify: `cli/custom/launch/kimi.go:70-90`, `cli/custom/launch/pi.go:60-76`
- Modify: `cli/custom/launch/kimi_test.go`, `cli/custom/launch/pi_test.go`

**Interfaces:**
- Consumes: `skills.EnsureGeneration`, `skills.Names` from Tasks 1 and 2.
- Produces: `launch.writeSessionSkills(dir string) error`, used by both agents and by Task 10.

`orq launch kimi` already points `KIMI_CODE_HOME` at a temp directory, and `orq launch pi` already points `PI_CODING_AGENT_DIR` at one, each removed by the existing `Cleanup`. Session skills for these two are files inside a directory the CLI already owns, so there is nothing to unlink and nothing to reference count.

- [ ] **Step 1: Write the failing test**

Append to `cli/custom/launch/pi_test.go`:

```go
func TestPiSessionGetsSkillsInItsTempAgentDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	if err := writeSessionSkills(dir); err != nil {
		t.Fatalf("writeSessionSkills: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no skills written: %v %d", err, len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", entries[0].Name(), "SKILL.md")); err != nil {
		t.Errorf("skill not readable: %v", err)
	}
}

func TestSessionSkillsAreSuppressedByNoSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	ctx := &AgentContext{Flags: Flags{NoSkills: true}, Getenv: func(string) string { return "" }}
	if err := maybeWriteSessionSkills(ctx, dir); err != nil {
		t.Fatalf("maybeWriteSessionSkills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills")); !os.IsNotExist(err) {
		t.Error("--no-skills still wrote skills")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cli/custom/launch/ -run TestPiSession -v`
Expected: FAIL, `writeSessionSkills` undefined.

- [ ] **Step 3: Write the helper**

Add to `cli/custom/launch/mcp.go`, beside the existing `skillsPluginURL`:

```go
// writeSessionSkills copies the shipped skills into an agent directory the
// launcher owns for this session. Copying rather than linking because the
// directory is deleted wholesale on exit; a symlink into a generation that a
// later run collects would dangle for the rest of the session.
func writeSessionSkills(dir string) error {
	gen, err := skills.EnsureGeneration()
	if err != nil {
		return err
	}
	names, err := skills.Names()
	if err != nil {
		return err
	}
	dest := filepath.Join(dir, "skills")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, name := range names {
		if err := os.Symlink(filepath.Join(gen, name), filepath.Join(dest, name)); err != nil {
			return err
		}
	}
	return nil
}

// maybeWriteSessionSkills is the flag-aware wrapper every agent calls.
func maybeWriteSessionSkills(ctx *AgentContext, dir string) error {
	if ctx.Flags.NoSkills {
		return nil
	}
	return writeSessionSkills(dir)
}
```

Import `"orq/cli/custom/skills"`, `"os"`, and `"path/filepath"`.

The symlink here is inside a directory that is removed on exit, and the generation outlives the session because collection keeps the current one. On Windows, `os.Symlink` fails, so fall back:

```go
	for _, name := range names {
		src := filepath.Join(gen, name)
		dst := filepath.Join(dest, name)
		if err := os.Symlink(src, dst); err != nil {
			if copyErr := copyDirForSession(src, dst); copyErr != nil {
				return copyErr
			}
		}
	}
```

with a small `copyDirForSession` mirroring `skills.copyDir`, or export the copier from the skills package and call it here. Exporting is preferable: one implementation, one set of permission decisions.

- [ ] **Step 4: Call it from kimi and pi**

In `cli/custom/launch/kimi.go`, after the MCP config write and before the plan is built:

```go
	if err := maybeWriteSessionSkills(ctx, home); err != nil {
		// Skills are an enhancement; refusing to start the agent because a
		// symlink failed is worse than starting without them.
		defer func() { plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills unavailable this session: %v", err)) }()
	}
```

Because `plan` is built after this point in `kimi.go`, capture the error into a local and append after construction instead:

```go
	skillsErr := maybeWriteSessionSkills(ctx, home)

	plan := &LaunchPlan{ /* unchanged */ }
	if skillsErr != nil {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills unavailable this session: %v", skillsErr))
	}
```

Apply the same two lines in `cli/custom/launch/pi.go`, passing `dir` instead of `home`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cli/custom/launch/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cli/custom/launch
git commit -m "feat(launch): session skills for kimi and pi"
```

---

### Task 10: Session skills for the real-home agents

**Files:**
- Create: `cli/custom/skills/session.go`
- Modify: `cli/custom/launch/agents.go:23-30` (chainable cleanup), `claude.go`, `codex.go`, `opencode.go`
- Modify: `cli/custom/skills/skills_test.go`

**Interfaces:**
- Consumes: `Install`-adjacent internals from Task 5.
- Produces: `skills.InstallSession(agent string) (release func(), err error)` and `skills.SweepDeadSessions() error`.

- [ ] **Step 1: Write the failing test**

Append to `cli/custom/skills/skills_test.go`:

```go
func TestSessionLinksAreReferenceCounted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")

	releaseA, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	releaseB, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("second session: %v", err)
	}

	releaseA()
	if entries, readErr := os.ReadDir(dir); readErr != nil || len(entries) == 0 {
		t.Fatalf("first session's exit removed links the second still needs: %v %d", readErr, len(entries))
	}
	releaseB()
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
		t.Errorf("last session's exit left %d entries", len(entries))
	}
}

func TestSessionLinksSurviveAPermanentInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install([]string{"claude"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	release, err := InstallSession("claude")
	if err != nil {
		t.Fatalf("InstallSession: %v", err)
	}
	release()

	entries, err := os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err != nil || len(entries) == 0 {
		t.Errorf("a session exit removed the permanent install: %v %d", err, len(entries))
	}
}

func TestSweepRemovesLinksOwnedByDeadProcesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills")

	if _, err := InstallSession("claude"); err != nil {
		t.Fatalf("InstallSession: %v", err)
	}
	m, err := LoadManifest()
	if err != nil || m == nil {
		t.Fatalf("manifest: %v %v", m, err)
	}
	// A PID that cannot be running: claim the links for a dead process.
	for i := range m.Sessions {
		m.Sessions[i].PID = 999999
	}
	if err := SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	if err := SweepDeadSessions(); err != nil {
		t.Fatalf("SweepDeadSessions: %v", err)
	}
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
		t.Errorf("sweep left %d entries from a dead session", len(entries))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cli/custom/skills/ -run TestSession -v`
Expected: FAIL, `InstallSession` undefined.

- [ ] **Step 3: Write the implementation**

Create `cli/custom/skills/session.go`:

```go
package skills

import (
	"os"
	"path/filepath"
)

// InstallSession links the skill set for one `orq launch`, and returns the
// function that gives it back. Links a permanent install already owns are
// left exactly as they are: a session must never take them away on exit.
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
	m, err := LoadManifest()
	if err != nil {
		return nil, err
	}
	if m == nil {
		m = &Manifest{Version: manifestVersion}
	}

	pid := os.Getpid()
	var held []string
	for _, target := range targets {
		if err := os.MkdirAll(target.Dir, 0o755); err != nil {
			return nil, err
		}
		for _, name := range names {
			path := filepath.Join(target.Dir, name)
			if exists(path) {
				// Either a permanent install of ours or somebody else's skill.
				// Both cases mean: use what is there, own nothing, remove
				// nothing on exit.
				continue
			}
			if err := project(filepath.Join(gen, name), path); err != nil {
				return nil, err
			}
			m.AddLink(Link{Path: path, Agent: target.Agent, Skill: name, Mode: linkMode(), Session: true})
			held = append(held, path)
		}
	}
	m.Sessions = append(m.Sessions, Session{PID: pid, Paths: held})
	m.Generation = gen
	if err := SaveManifest(m); err != nil {
		return nil, err
	}

	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = releaseSession(pid)
	}, nil
}

// releaseSession drops this process's claim and removes only the links no
// other live session still holds.
func releaseSession(pid int) error {
	m, err := LoadManifest()
	if err != nil || m == nil {
		return err
	}
	var mine []string
	kept := m.Sessions[:0]
	for _, s := range m.Sessions {
		if s.PID == pid {
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
		if err := removePath(p); err != nil {
			return err
		}
		removed = append(removed, p)
	}
	m.RemoveLinks(removed)
	return SaveManifest(m)
}

// SweepDeadSessions releases claims held by processes that are no longer
// running. A session killed rather than exited leaves its links behind, and
// this is what makes the next `orq` command clean them up.
func SweepDeadSessions() error {
	m, err := LoadManifest()
	if err != nil || m == nil {
		return err
	}
	for _, s := range m.Sessions {
		if processAlive(s.PID) {
			continue
		}
		if err := releaseSession(s.PID); err != nil {
			return err
		}
	}
	return nil
}
```

Create `cli/custom/skills/process_unix.go`:

```go
//go:build !windows

package skills

import (
	"os"
	"syscall"
)

// processAlive reports whether a PID names a live process. Signal 0 performs
// the permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
```

Create `cli/custom/skills/process_windows.go`:

```go
//go:build windows

package skills

import "os"

// processAlive on Windows: FindProcess fails outright for a PID that is not
// running, which is the whole check.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}
```

- [ ] **Step 4: Make LaunchPlan cleanup chainable**

`LaunchPlan.Cleanup` is a single `func()` that `claude.go`, `codex.go`, `kimi.go`, and `pi.go` all assign directly, so a second assignment silently drops the first. In `cli/custom/launch/agents.go`, add a method beside the struct:

```go
// AddCleanup chains a cleanup onto whatever the plan already has. Assigning
// Cleanup directly drops the previous one, which is how a temp directory gets
// left behind.
func (p *LaunchPlan) AddCleanup(fn func()) {
	if fn == nil {
		return
	}
	prev := p.Cleanup
	p.Cleanup = func() {
		fn()
		if prev != nil {
			prev()
		}
	}
}
```

Then change the four existing `plan.Cleanup = cleanup` assignments to `plan.AddCleanup(cleanup)`.

- [ ] **Step 5: Call it from claude, codex, and opencode**

In each of `claude.go`, `codex.go`, and `opencode.go`, after the plan is constructed:

```go
	if !ctx.Flags.NoSkills {
		if release, err := skills.InstallSession("claude"); err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills unavailable this session: %v", err))
		} else {
			plan.AddCleanup(release)
		}
	}
```

with `"claude"` replaced by `"codex"` and `"opencode"` respectively. Import `"orq/cli/custom/skills"` in each.

On a copy-fallback platform, skip the session dance entirely and point the user at connect. Add this guard at the top of the same block:

```go
	if !ctx.Flags.NoSkills && runtime.GOOS == "windows" {
		plan.Warnings = append(plan.Warnings,
			"skills are not installed for a single session on Windows — run 'orq connect skills' once to install them")
	} else if !ctx.Flags.NoSkills {
		// ... the InstallSession block above
	}
```

- [ ] **Step 6: Restore the sweep call in the root command**

Uncomment the `skills.SweepDeadSessions()` call added in Task 8 Step 3.

- [ ] **Step 7: Drop `--plugin-url`**

In `cli/custom/launch/claude.go`, remove the `skillsPluginURL` block, now that claude gets real skill files:

```go
	if url := skillsPluginURL(ctx); url != "" {
		plan.PreArgs = append(plan.PreArgs, "--plugin-url", url)
	}
```

Keep `skillsPluginURL` and `ORQ_SKILLS_URL` in `mcp.go` as the opt-in override: when the variable is explicitly set, still pass `--plugin-url`, so anyone pinning their own bundle keeps working.

```go
	// Explicit override only: the default set now ships in the binary, so the
	// network fetch happens when someone asks for a specific bundle, not on
	// every launch.
	if url := strings.TrimSpace(ctx.Getenv("ORQ_SKILLS_URL")); url != "" && !ctx.Flags.NoSkills {
		plan.PreArgs = append(plan.PreArgs, "--plugin-url", url)
	}
```

Update `cli/custom/launch/claude_test.go` where it asserts `--plugin-url` is present by default: the default now has no `--plugin-url`, and setting `ORQ_SKILLS_URL` restores it.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./cli/custom/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add cli/custom
git commit -m "feat(launch): session-scoped skills with refcount and dead-session sweep"
```

---

### Task 11: The setup wizard capability picker

**Files:**
- Modify: `cli/custom/commands/setup.go` (`runSetup`, new `promptForCapabilities`)
- Modify: `cli/custom/commands/setup_test.go`

**Interfaces:**
- Consumes: `capGateway`, `capTracing`, `capSkills` from Task 6; `opts.caps` from Task 6.
- Produces: `promptForCapabilities(rep *reporter) ([]string, error)`.

The picker runs after the agent picker, because the agent list is the concrete question, and choosing tracing before knowing whether a tracing-capable agent was detected is a question people cannot answer.

- [ ] **Step 1: Write the failing test**

Append to `cli/custom/commands/setup_test.go`:

```go
func TestSetupDefaultCapabilitiesIncludeSkills(t *testing.T) {
	caps := defaultCapabilities()
	if !hasCap(caps, capSkills) {
		t.Errorf("defaults = %v, want skills included", caps)
	}
	if !hasCap(caps, capGateway) {
		t.Errorf("defaults = %v, want gateway included", caps)
	}
	if hasCap(caps, capTracing) {
		t.Errorf("defaults = %v, want tracing excluded while it is unbuilt", caps)
	}
}

func TestSetupNonInteractiveUsesTheDefaultCapabilities(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	resetSetupMemos(t)

	opts := &setupOptions{noInput: true}
	caps, err := resolveCapabilities(newReporter(true), opts)
	if err != nil {
		t.Fatalf("resolveCapabilities: %v", err)
	}
	if !hasCap(caps, capSkills) {
		t.Errorf("caps = %v, want skills", caps)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cli/custom/commands/ -run TestSetupDefaultCapabilities -v`
Expected: FAIL, `defaultCapabilities` undefined.

- [ ] **Step 3: Write the implementation**

Add to `cli/custom/commands/setup.go`:

```go
// defaultCapabilities is what a bare `orq setup` connects. Tracing is excluded
// while dropUnavailableCaps still strips it: offering it in the picker would
// be offering something that then prints "not available yet".
func defaultCapabilities() []string {
	return []string{capGateway, capSkills}
}

// resolveCapabilities decides what this run connects. Explicit flags win, a
// non-interactive run takes the defaults, and an interactive one asks.
func resolveCapabilities(rep *reporter, opts *setupOptions) ([]string, error) {
	if len(opts.caps) > 0 {
		return opts.caps, nil
	}
	if opts.noInput || opts.yes {
		return defaultCapabilities(), nil
	}
	return promptForCapabilities(rep)
}

// promptForCapabilities is the multi-select, modeled on promptForAgents so the
// two questions in one wizard behave the same way.
func promptForCapabilities(rep *reporter) ([]string, error) {
	options := []multiSelectOption{
		{Value: capGateway, Label: "gateway — route the agent's model calls through orq", Selected: true},
		{Value: capSkills, Label: "skills — install the orq skills so the agent knows how to use orq", Selected: true},
	}
	chosen, err := rep.multiSelect("What should orq connect?", options)
	if err != nil {
		return nil, err
	}
	if len(chosen) == 0 {
		return nil, nil
	}
	return chosen, nil
}
```

Match `multiSelectOption` and `rep.multiSelect` to whatever `promptForAgents` actually uses in this file. Read `promptForAgents` (around `setup.go:1255`) and mirror its prompt construction exactly rather than introducing a second selection widget.

- [ ] **Step 4: Call it from runSetup**

In `runSetup`, after the agent selection resolves and before `instrumentAgents`:

```go
	caps, err := resolveCapabilities(rep, opts)
	if err != nil {
		return fmt.Errorf("cancelled at the capability selection: %w", err)
	}
	opts.caps = caps
```

- [ ] **Step 5: Add the flag**

In `NewSetupCommand`, add the non-interactive escape hatch beside the existing flags:

```go
	f.StringSliceVar(&opts.caps, "capability", nil, "Capabilities to connect (gateway, skills); repeatable")
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./cli/custom/commands/ -v`
Expected: PASS.

- [ ] **Step 7: Run the full suite and typecheck**

```bash
go vet ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cli/custom/commands
git commit -m "feat(setup): capability picker with skills on by default"
```

---

## Self-Review

**Spec coverage.** Every section of `.context/RES-1409-spec.md` maps to a task: source and embedding to Task 1, generation and collection to Task 2, manifest to Task 3, targets and environment variables to Task 4, projection, pruning and ownership to Task 5, the connect capability and dry run to Task 6, status to Task 7, refresh to Task 8 (scoped to the skills commands rather than every call — see the note on that task), launch to Tasks 9 and 10, and the wizard to Task 11. Two spec items are deliberately absent as tasks: the `--purge` flag on disconnect, which the spec mentions once and which is a one-line addition to Task 6 if wanted, and the verification question about a per-harness "read skills from here" flag, which is research rather than implementation and would only remove work from Task 10.

**Placeholder scan.** Two steps knowingly defer to the reader rather than showing final code: Task 11 Step 3 says to mirror `promptForAgents`'s existing selection widget rather than guessing its type names, and Task 9 Step 3 offers exporting the skills package's copier as the preferred alternative to duplicating it. Both are cases where the plan cannot know a name it has not read; the implementer must read the named function first. Everything else carries the code to write.

**Type consistency.** `Result`, `Link`, `Manifest`, `Session`, and `Target` are defined once in Tasks 3, 4, and 5 and used with those names throughout. `Install`, `Remove`, `Refresh`, `InstallSession`, `SweepDeadSessions`, `Targets`, `Names`, `Fingerprint`, `EnsureGeneration`, and `Home` are the full exported surface. `capSkills` is the single capability constant. `AddCleanup` in Task 10 Step 4 is required before Task 10 Step 5 uses it, and before Task 9 if that lands first, so if the tasks are reordered, Step 4 moves with them.

**One ordering constraint the executor must respect:** Task 8 Step 3 references `skills.SweepDeadSessions`, which Task 10 creates. Either land Task 10 before Task 8, or leave that one call commented out as the step says and restore it in Task 10 Step 6.
