# Skills `--local` / `--global` scopes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `orq connect skills --local` installs the skill set into the current directory's `.claude/skills` and `.agents/skills`; `--global` (the default) into the same two under `$HOME`; `orq launch` links session skills locally; codex, opencode and kilo gain an MCP project scope.

**Architecture:** The skills package owns a `Scope` type and the cwd-vs-`$HOME` and git-root rules, so `commands` and `launch` cannot disagree on what "local" means. `Targets` becomes the single place that maps (agents, scope) to directories; `Install`, `Remove`, `InstallSession`, status and doctor all go through it. The command layer's existing MCP scope machinery (`configScope`, `checkScopeFlags`, `scopedPaths`, `resolveScope`) is switched on for skills by adding `capSkills` to `capScoped`.

**Tech Stack:** Go 1.2x, cobra/pflag, survey; tests are stock `go test` with `t.TempDir`, `t.Setenv`, `t.Chdir`.

**Spec:** `docs/superpowers/specs/2026-08-25-orq-cli-skills-scopes-design.md` — read it first; §9 is the touch-point table this plan implements task by task.

## Global Constraints

- Everything under `cli/custom/` ships on both modules; after every task run `go build ./... && go vet ./...` at the root and `cd packages/orq-rc && go build ./...`.
- CI runs `go test ./... && go vet ./... && gofmt -l $(git ls-files '*.go')`. Run `gofmt -l` before every commit.
- Commit messages are conventional commits, `type(scope): subject`. No `!` and no `BREAKING CHANGE:` footer anywhere — this PR is a `feat`.
- `CHANGELOG.md` gets its entry under `## Unreleased` in the same PR (Task 10).
- No flag is added or renamed, so `surface.json` must not change. Run `go run ./cmd/surface-dump -check` in Task 10 to prove it.
- Local target directories are exactly `<cwd>/.claude/skills` (claude) and `<cwd>/.agents/skills` (everyone else). Global: `~/.claude/skills` and `~/.agents/skills`. Nothing else, on any OS.
- Bare `connect skills` is global. Bare `disconnect skills` removes global and local-at-cwd. No prompt in either direction.
- Nothing edits `.gitignore` and nothing writes codex's `[projects]` trust table.
- Default `os.Getwd()` is the anchor. `filepath.EvalSymlinks` only inside `RepoRoot`.
- Work in worktree `/tmp/res-1437-impl` on branch `Baukebrenninkmeijer/res-1437-skills-scopes`. Never `git stash`.

---

## File structure

| File | Responsibility after this plan |
| --- | --- |
| `cli/custom/skills/targets.go` | `Scope`, `ScopeFor`, `RepoRoot`, `Targets(agents, scope)`, `retiredDirs` |
| `cli/custom/skills/project.go` | `Install(agents, scope)` with retired-directory reconciliation; `Remove(agents, scope)` with the path predicate and `Result.Elsewhere` |
| `cli/custom/skills/status.go` | `Place` classification of a recorded link against cwd |
| `cli/custom/skills/session.go` | `InstallSession` uses `ScopeFor` |
| `cli/custom/launch/mcp.go` | dry-run branch uses `ScopeFor` |
| `cli/custom/commands/connect.go` | `capScoped` += skills; scope conversion; wording; status rows with scope; `--json` scope; local-install notes |
| `cli/custom/commands/setup.go` | `scopeMatters`, `promptForScope`, `skillTargetsFor(agents, scope)` |
| `cli/custom/commands/agents.go` | codex/opencode/kilo project MCP paths; `skillsCheck` buckets |
| `CHANGELOG.md`, `README.md` | user-facing text |

---

### Task 1: `Scope`, `ScopeFor`, `RepoRoot`, and the new target set

**Files:**
- Modify: `cli/custom/skills/targets.go`
- Test: `cli/custom/skills/skills_test.go:252-320` (replace `TestTargets`, `TestTargetsHonorAgentHomeEnvironmentVariables`)

**Interfaces:**
- Produces:
  ```go
  type Scope int
  const (ScopeGlobal Scope = iota; ScopeLocal; ScopeBoth)
  func ScopeFor(cwd string) Scope            // ScopeGlobal when cwd is $HOME, else ScopeLocal
  func RepoRoot(dir string) (string, bool)   // nearest ancestor (inclusive) holding a .git entry
  type Target struct { Agent, Dir string; Global bool }
  func Targets(agents []string, scope Scope) ([]Target, error)
  func retiredDirs() ([]string, error)       // unexported; Task 2 uses it
  ```
- `SharedReader`, `Receives` keep their signatures. `sharedReaders` = opencode, kilo, pi, codex, kimi. `ownDir` = claude only.

- [ ] **Step 1: Replace the two target tests**

Delete `TestTargets` and `TestTargetsHonorAgentHomeEnvironmentVariables` in `skills_test.go` and add:

```go
func TestTargets(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	project := t.TempDir()
	t.Chdir(project)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("KIMI_CODE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	dirs := func(scope Scope, agents ...string) string {
		got, err := Targets(agents, scope)
		if err != nil {
			t.Fatalf("Targets(%v, %v): %v", agents, scope, err)
		}
		var out []string
		for _, tg := range got {
			d := tg.Dir
			d = strings.TrimPrefix(d, home)
			d = strings.TrimPrefix(d, project)
			out = append(out, filepath.ToSlash(d))
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}

	if got := dirs(ScopeGlobal, "claude"); got != "/.claude/skills" {
		t.Errorf("claude global = %q", got)
	}
	if got := dirs(ScopeLocal, "claude"); got != "/.claude/skills" {
		t.Errorf("claude local = %q", got)
	}
	// Every non-claude agent maps to the shared directory and nothing else:
	// codex and kimi read ~/.agents/skills too, and codex does not dedupe, so
	// writing ~/.codex/skills as well listed every skill twice.
	for _, agent := range []string{"codex", "kimi", "opencode", "pi", "kilo"} {
		if got := dirs(ScopeGlobal, agent); got != "/.agents/skills" {
			t.Errorf("%s global = %q, want only the shared directory", agent, got)
		}
		if got := dirs(ScopeLocal, agent); got != "/.agents/skills" {
			t.Errorf("%s local = %q, want only the shared directory", agent, got)
		}
	}
	if got := dirs(ScopeGlobal, "claude", "codex", "kimi", "opencode", "pi", "kilo"); got != "/.agents/skills,/.claude/skills" {
		t.Errorf("everyone global = %q", got)
	}
	if got := dirs(ScopeBoth, "claude", "pi"); got != "/.agents/skills,/.agents/skills,/.claude/skills,/.claude/skills" {
		t.Errorf("both scopes = %q", got)
	}

	got, err := Targets([]string{"claude", "pi"}, ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range got {
		if tg.Global {
			t.Errorf("local target %s flagged global", tg.Dir)
		}
		if !strings.HasPrefix(tg.Dir, project) {
			t.Errorf("local target %s is not under cwd %s", tg.Dir, project)
		}
	}
}

func TestTargetsIgnoreAgentHomeEnvironmentVariables(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	got, err := Targets([]string{"kimi", "codex", "kilo"}, ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Dir != filepath.Join(home, ".agents", "skills") {
		t.Errorf("targets = %+v, want only ~/.agents/skills", got)
	}
}

func TestScopeFor(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if ScopeFor(home) != ScopeGlobal {
		t.Error("$HOME must resolve to the global scope")
	}
	if ScopeFor(t.TempDir()) != ScopeLocal {
		t.Error("a directory that is not $HOME must resolve to the local scope")
	}
	// Symlinked home: the same directory by another name is still $HOME.
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if ScopeFor(link) != ScopeGlobal {
		t.Error("a symlink to $HOME must resolve to the global scope")
	}
}

func TestRepoRoot(t *testing.T) {
	base := t.TempDir()
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{base}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	repo := mk("repo")
	mk("repo", ".git")
	sub := mk("repo", "a", "b")
	if root, ok := RepoRoot(sub); !ok || root != repo {
		t.Errorf("RepoRoot(sub) = %q, %v; want %q", root, ok, repo)
	}
	if root, ok := RepoRoot(repo); !ok || root != repo {
		t.Errorf("RepoRoot(root) = %q, %v", root, ok)
	}
	// A linked worktree has a .git *file*.
	wt := mk("wt")
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if root, ok := RepoRoot(mk("wt", "x")); !ok || root != wt {
		t.Errorf("worktree: RepoRoot = %q, %v", root, ok)
	}
	// Nested repo: nearest wins.
	inner := mk("repo", "vendor", "inner")
	mk("repo", "vendor", "inner", ".git")
	if root, ok := RepoRoot(mk("repo", "vendor", "inner", "pkg")); !ok || root != inner {
		t.Errorf("nested: RepoRoot = %q, %v; want %q", root, ok, inner)
	}
	if _, ok := RepoRoot(mk("plain")); ok {
		t.Error("a directory with no .git ancestor reported a root")
	}
	// Symlinked cwd resolves to the same root.
	link := filepath.Join(base, "sub-link")
	if err := os.Symlink(sub, link); err == nil {
		if root, ok := RepoRoot(link); !ok || root != repo {
			t.Errorf("symlinked cwd: RepoRoot = %q, %v", root, ok)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /tmp/res-1437-impl && go test ./cli/custom/skills -run 'TestTargets|TestScopeFor|TestRepoRoot' 2>&1 | head`
Expected: compile errors (`ScopeGlobal` undefined, `Targets` takes one argument).

- [ ] **Step 3: Rewrite `targets.go`**

Replace the whole file with:

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
// shared agents-spec directory, which several agents read at once. Global
// says which scope produced it, so a caller reporting on targets does not
// re-derive it from the path.
type Target struct {
	Agent  string
	Dir    string
	Global bool
}

// Scope is where a skill set lands. Both is a removal-only scope: an install
// has to pick one.
type Scope int

const (
	ScopeGlobal Scope = iota
	ScopeLocal
	ScopeBoth
)

// ScopeFor is the default for a caller that has no flag to consult — `orq
// launch`. Local unless cwd is $HOME: $HOME is not a project, and a
// ~/.agents/skills written from there is the global install by another name.
func ScopeFor(cwd string) Scope {
	home, err := os.UserHomeDir()
	if err != nil {
		return ScopeLocal
	}
	if sameDir(cwd, home) {
		return ScopeGlobal
	}
	return ScopeLocal
}

// RepoRoot is the nearest ancestor of dir (dir included) holding a .git
// entry — a directory for a normal checkout, a file for a linked worktree.
// dir is resolved through symlinks first, so a linked path and its target
// agree on the root. An unreadable parent ends the walk as "no repo".
func RepoRoot(dir string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	cur := filepath.Clean(resolved)
	for {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return cur, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
}

func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}

// sharedReaders read the agents-spec directory, so they get no directory of
// their own. Codex and kimi read it too; codex does not dedupe against its
// own directory, so writing both listed every skill twice in its catalog.
var sharedReaders = map[string]bool{"opencode": true, "kilo": true, "pi": true, "codex": true, "kimi": true}

// SharedReader reports whether agent reads the shared agents-spec directory,
// so a caller outside the package (connect --status's missing-link warning,
// at the moment) can apply the same membership rule Remove uses for links
// whose Agent is empty: they belong to the request whenever any named agent
// is a shared reader.
func SharedReader(agent string) bool { return sharedReaders[agent] }

// Receives reports whether agent has anywhere to put skills at all — its own
// directory or the shared one. It is what lets a caller ask "which agents can
// take this capability?" without asking the unrelated question "which agents
// have a gateway provider config?": claude answers no to the second and yes to
// this one, and it is the most common machine there is.
func Receives(agent string) bool {
	return sharedReaders[agent] || ownDir[agent] != ""
}

// ownDir is the directory, relative to the scope's base, of the one agent
// that does not read the shared one.
var ownDir = map[string]string{"claude": filepath.Join(".claude", "skills")}

var sharedDir = filepath.Join(".agents", "skills")

// Targets resolves the directories that serve the given agents in the given
// scope. The shared agents-spec directory is included only when at least one
// selected agent actually reads it, so a claude-only machine never grows a
// ~/.agents tree. Local targets are anchored at cwd, not the repository root:
// codex, opencode, pi and kilo all walk from cwd up to the root, and kimi is
// warned about by the caller.
//
// This is pure path resolution: it never checks whether an agent is actually
// installed on the machine. Gating on detection happens at the caller, which
// passes only the agents the user actually selected.
func Targets(agents []string, scope Scope) ([]Target, error) {
	var bases []struct {
		dir    string
		global bool
	}
	if scope != ScopeLocal {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		bases = append(bases, struct {
			dir    string
			global bool
		}{home, true})
	}
	if scope != ScopeGlobal {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		bases = append(bases, struct {
			dir    string
			global bool
		}{cwd, false})
	}
	var out []Target
	for _, base := range bases {
		shared := false
		for _, id := range agents {
			if sharedReaders[id] {
				shared = true
				continue
			}
			if rel := ownDir[id]; rel != "" {
				out = append(out, Target{Agent: id, Dir: filepath.Join(base.dir, rel), Global: base.global})
			}
		}
		if shared {
			out = append(out, Target{Dir: filepath.Join(base.dir, sharedDir), Global: base.global})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

// retiredDirs are the global directories earlier CLI versions wrote and this
// one no longer does: codex's and kimi's own directories (they read the
// shared one) and kilo's XDG path (read by nothing). Install removes still-
// owned links there so an upgraded machine does not keep a double index.
func retiredDirs() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	agentHome := func(envKey, fallback string) string {
		if dir := strings.TrimSpace(os.Getenv(envKey)); dir != "" {
			return dir
		}
		return filepath.Join(home, fallback)
	}
	out := []string{
		filepath.Join(agentHome("CODEX_HOME", ".codex"), "skills"),
		filepath.Join(agentHome("KIMI_CODE_HOME", ".kimi-code"), "skills"),
	}
	if runtime.GOOS == "linux" {
		base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		out = append(out, filepath.Join(base, "agents", "skills"))
	}
	return out, nil
}
```

- [ ] **Step 4: Fix the remaining callers so the package compiles**

The package's own callers are updated for real in Tasks 2 and 4; for now make them compile: in `project.go` change `Targets(agents)` to `Targets(agents, ScopeGlobal)` at line 179 and in `session.go` change `Targets([]string{agent})` to `Targets([]string{agent}, ScopeGlobal)` at line 29. Do the same temporary change in `cli/custom/commands/setup.go:1375` and `cli/custom/launch/mcp.go:64` (`skills.ScopeGlobal`).

Run: `go test ./cli/custom/skills -run 'TestTargets|TestScopeFor|TestRepoRoot' -v 2>&1 | tail -15`
Expected: PASS for all four.

- [ ] **Step 5: Run the whole skills package and fix fallout**

Run: `go test ./cli/custom/skills 2>&1 | tail -30`

Expected failures to fix in the tests themselves (not the code): any test that asserted `~/.codex/skills` or `~/.kimi-code/skills` as a target (search `skills_test.go` for `.codex/skills`, `.kimi-code/skills`, `.config/agents`). Repoint each at `~/.agents/skills`. Tests that used codex or kimi as "an agent with its own directory" should use claude instead.

- [ ] **Step 6: Build both modules and commit**

```bash
cd /tmp/res-1437-impl && go build ./... && go vet ./cli/custom/... && gofmt -l $(git ls-files '*.go') && (cd packages/orq-rc && go build ./...)
git add cli/custom/skills cli/custom/commands/setup.go cli/custom/launch/mcp.go
git commit -m "feat(skills): scope type, repo-root helper, and one shared directory per scope (RES-1437)"
```

---

### Task 2: `Install(agents, scope)` with retired-directory reconciliation

**Files:**
- Modify: `cli/custom/skills/project.go:159-249`
- Test: `cli/custom/skills/skills_test.go`

**Interfaces:**
- Consumes: `Targets`, `retiredDirs` from Task 1.
- Produces: `func Install(agents []string, scope Scope) (*Result, error)`. `ScopeBoth` returns an error. Reconciled links are appended to `Result.Removed` (or `Skipped` when foreign).

- [ ] **Step 1: Write the failing tests**

```go
func TestInstallLocalLinksIntoCwd(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	project := t.TempDir()
	t.Chdir(project)

	res, err := Install([]string{"claude", "codex"}, ScopeLocal)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	names, _ := Names()
	if len(res.Added) != 2*len(names) {
		t.Fatalf("added %d links, want %d", len(res.Added), 2*len(names))
	}
	for _, dir := range []string{filepath.Join(project, ".claude", "skills"), filepath.Join(project, ".agents", "skills")} {
		if _, err := os.Stat(filepath.Join(dir, names[0])); err != nil {
			t.Errorf("%s not projected into %s: %v", names[0], dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Error("a local install touched $HOME")
	}
}

func TestInstallRejectsBothScopes(t *testing.T) {
	setHome(t, t.TempDir())
	if _, err := Install([]string{"claude"}, ScopeBoth); err == nil {
		t.Fatal("Install accepted ScopeBoth")
	}
}

// A manifest written by an earlier CLI records links under ~/.codex/skills
// and ~/.kimi-code/skills. Nothing prunes by directory: refresh prunes by
// skill name only. Install removes what is still ours there.
func TestInstallRemovesLinksInRetiredDirectories(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("KIMI_CODE_HOME", "")
	gen, err := EnsureGeneration()
	if err != nil {
		t.Fatal(err)
	}
	names, _ := Names()
	old := []string{
		filepath.Join(codexHome, "skills", names[0]),
		filepath.Join(home, ".kimi-code", "skills", names[0]),
	}
	m := &Manifest{Version: manifestVersion, Fingerprint: Fingerprint(), Generation: gen}
	for _, p := range old {
		if err := project(filepath.Join(gen, names[0]), p); err != nil {
			t.Fatal(err)
		}
		m.AddLink(Link{Path: p, Agent: "codex", Skill: names[0], Mode: linkMode()})
	}
	// A foreign directory at a retired path is left alone and reported.
	foreign := filepath.Join(home, ".kimi-code", "skills", names[1])
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	m.AddLink(Link{Path: foreign, Agent: "kimi", Skill: names[1], Mode: linkMode()})
	if err := SaveManifest(m); err != nil {
		t.Fatal(err)
	}

	res, err := Install([]string{"codex", "kimi"}, ScopeGlobal)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, p := range old {
		if exists(p) {
			t.Errorf("retired link %s survived the install", p)
		}
	}
	if !exists(foreign) {
		t.Error("a foreign directory at a retired path was deleted")
	}
	if !slices.Contains(res.Skipped, foreign) {
		t.Errorf("foreign retired path not reported as skipped: %+v", res)
	}
	m, _ = LoadManifest()
	for _, l := range m.Links {
		if slices.Contains(old, l.Path) || l.Path == foreign {
			t.Errorf("manifest still records %s", l.Path)
		}
	}
	if !exists(filepath.Join(home, ".agents", "skills", names[0])) {
		t.Error("the shared directory was not written")
	}
}
```

Add `"slices"` to the test imports if missing.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cli/custom/skills -run 'TestInstallLocal|TestInstallRejects|TestInstallRemovesLinksInRetired' 2>&1 | head`
Expected: compile error (`Install` takes one argument).

- [ ] **Step 3: Implement**

In `project.go` replace `Install`/`install` signatures and add the reconciliation before the projection loop:

```go
// Install materializes the current generation and projects it into every
// directory the given agents read in the given scope. It is the one entry
// point that may create directories that did not exist.
// The lock is what keeps a concurrent `orq launch` from losing this install's
// records (see lock.go); the work itself is in install.
func Install(agents []string, scope Scope) (*Result, error) {
	if scope == ScopeBoth {
		return &Result{}, errors.New("an install writes one scope; pick global or local")
	}
	res := &Result{}
	err := withManifestLock(func() error {
		var err error
		res, err = install(agents, scope)
		return err
	})
	return res, err
}

func install(agents []string, scope Scope) (*Result, error) {
	gen, err := EnsureGeneration()
	if err != nil {
		return nil, err
	}
	targets, err := Targets(agents, scope)
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
	if err := reconcileRetired(m, res); err != nil {
		return nil, err
	}
	for _, target := range targets {
		// ... unchanged loop body ...
	}
	// ... unchanged tail ...
}

// reconcileRetired drops links in directories this CLI no longer writes (see
// retiredDirs). Refresh never does this — it prunes by skill name, not by
// directory — so without it an upgraded machine keeps codex double-indexed
// forever. isOurs gates every deletion, as everywhere else; a foreign path is
// disowned and reported, never touched.
func reconcileRetired(m *Manifest, res *Result) error {
	retired, err := retiredDirs()
	if err != nil {
		return err
	}
	isRetired := map[string]bool{}
	for _, d := range retired {
		isRetired[filepath.Clean(d)] = true
	}
	var gone []string
	for _, l := range m.Links {
		if l.Session || !isRetired[filepath.Dir(filepath.Clean(l.Path))] {
			continue
		}
		if exists(l.Path) && !isOurs(l) {
			res.Skipped = append(res.Skipped, l.Path)
			res.Disowned = append(res.Disowned, l.Path)
			gone = append(gone, l.Path)
			continue
		}
		if err := removePath(l.Path); err != nil {
			return err
		}
		res.Removed = append(res.Removed, l.Path)
		gone = append(gone, l.Path)
	}
	m.RemoveLinks(gone)
	return nil
}
```

Add `"errors"` to the imports. Update the doc comment on `Remove` later in Task 3; for now leave `Remove` as is.

- [ ] **Step 4: Run tests**

Run: `go test ./cli/custom/skills 2>&1 | tail -20`
Expected: PASS. Any existing test calling `Install(agents)` gets `, ScopeGlobal` added.

- [ ] **Step 5: Commit**

```bash
gofmt -l $(git ls-files '*.go'); go build ./... && (cd packages/orq-rc && go build ./...)
git add cli/custom/skills
git commit -m "feat(skills): install takes a scope and retires the per-agent directories (RES-1437)"
```

---

### Task 3: `Remove(agents, scope)` selects by agent and path

**Files:**
- Modify: `cli/custom/skills/project.go:251-315`
- Test: `cli/custom/skills/skills_test.go`

**Interfaces:**
- Produces: `func Remove(agents []string, scope Scope) (*Result, error)`; `Result.Elsewhere []string` — non-session links that matched the agent rule but sit outside every directory the scope means at this cwd (local installs in other directories). Never deleted.

- [ ] **Step 1: Write the failing tests**

```go
func TestRemoveHonoursScopeAndCountsOtherDirectories(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	repoA := t.TempDir()
	repoB := t.TempDir()
	names, _ := Names()

	t.Chdir(repoA)
	for _, scope := range []Scope{ScopeGlobal, ScopeLocal} {
		if _, err := Install([]string{"claude"}, scope); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repoB)
	if _, err := Install([]string{"claude"}, ScopeLocal); err != nil {
		t.Fatal(err)
	}
	globalLink := filepath.Join(home, ".claude", "skills", names[0])
	aLink := filepath.Join(repoA, ".claude", "skills", names[0])
	bLink := filepath.Join(repoB, ".claude", "skills", names[0])

	// --local from repoB touches repoB only.
	res, err := Remove([]string{"claude"}, ScopeLocal)
	if err != nil {
		t.Fatal(err)
	}
	if exists(bLink) || !exists(aLink) || !exists(globalLink) {
		t.Errorf("--local removed the wrong links: b=%v a=%v global=%v", exists(bLink), exists(aLink), exists(globalLink))
	}
	if len(res.Elsewhere) != len(names) {
		t.Errorf("elsewhere = %d, want %d (repoA's links)", len(res.Elsewhere), len(names))
	}

	// bare (both) from repoB removes global, leaves repoA, counts it.
	res, err = Remove([]string{"claude"}, ScopeBoth)
	if err != nil {
		t.Fatal(err)
	}
	if exists(globalLink) || !exists(aLink) {
		t.Errorf("bare removal: global=%v a=%v", exists(globalLink), exists(aLink))
	}
	if len(res.Elsewhere) != len(names) {
		t.Errorf("elsewhere = %d, want %d", len(res.Elsewhere), len(names))
	}

	// --global with nothing global left removes nothing and still counts repoA.
	res, err = Remove([]string{"claude"}, ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 || len(res.Elsewhere) != len(names) {
		t.Errorf("--global: removed=%d elsewhere=%d", len(res.Removed), len(res.Elsewhere))
	}

	// From repoA, --local takes the last ones.
	t.Chdir(repoA)
	if _, err := Remove([]string{"claude"}, ScopeLocal); err != nil {
		t.Fatal(err)
	}
	if exists(aLink) {
		t.Error("repoA's local install survived a --local removal run from repoA")
	}
	m, _ := LoadManifest()
	if m != nil && len(m.Links) != 0 {
		t.Errorf("manifest still records %d links", len(m.Links))
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cli/custom/skills -run TestRemoveHonoursScope 2>&1 | head`
Expected: compile error.

- [ ] **Step 3: Implement**

Replace `Remove`/`remove`:

```go
// Remove deletes only what the manifest records for the given agents in the
// given scope. An empty agent list means every agent.
//
// Two rules select a link. Membership: a link with an empty Agent is the
// shared agents-spec directory (see Targets) and belongs to the request
// whenever any named agent is a shared reader. Place: the link's directory
// is one the scope means at this cwd — ScopeBoth is the global set plus the
// local set here. A local install in another directory matches the first
// rule and not the second; it is counted in Elsewhere and left alone, and
// the caller says where to run from to remove it.
//
// The Result is never nil, including when the lock could not be taken and the
// closure never ran: callers report the error and then still range over the
// result, and a nil here crashed `orq disconnect skills` against a live
// `orq launch`.
func Remove(agents []string, scope Scope) (*Result, error) {
	res := &Result{}
	err := withManifestLock(func() error {
		var err error
		res, err = remove(agents, scope)
		return err
	})
	return res, err
}

func remove(agents []string, scope Scope) (*Result, error) {
	m, err := LoadManifest()
	if err != nil || m == nil {
		return &Result{}, err
	}
	all := agents
	if len(all) == 0 {
		all = allAgents()
	}
	targets, err := Targets(all, scope)
	if err != nil {
		return &Result{}, err
	}
	inScope := map[string]bool{}
	for _, tg := range targets {
		inScope[filepath.Clean(tg.Dir)] = true
	}
	wanted := map[string]bool{}
	sharedWanted := false
	for _, a := range all {
		wanted[a] = true
		sharedWanted = sharedWanted || sharedReaders[a]
	}
	res := &Result{}
	var gone []string
	for _, l := range m.Links {
		if l.Session {
			continue
		}
		if !wanted[l.Agent] && !(l.Agent == "" && sharedWanted) {
			continue
		}
		if !inScope[filepath.Dir(filepath.Clean(l.Path))] {
			res.Elsewhere = append(res.Elsewhere, l.Path)
			continue
		}
		if exists(l.Path) && !isOurs(l) {
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

// allAgents is every agent Targets knows, for a removal that named none.
func allAgents() []string {
	out := make([]string, 0, len(sharedReaders)+len(ownDir))
	for id := range sharedReaders {
		out = append(out, id)
	}
	for id := range ownDir {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
```

Add to `Result`:

```go
	// Elsewhere holds links that belong to the requested agents but sit in a
	// directory the scope does not reach from this cwd — local installs made
	// in other directories. They are never touched; the caller reports the
	// count and where to run from.
	Elsewhere []string
```

Add `"sort"` to `project.go` imports.

- [ ] **Step 4: Run the package**

Run: `go test ./cli/custom/skills 2>&1 | tail -20`
Expected: PASS. Existing `Remove(agents)` calls in tests get `, ScopeBoth` (that is the old behaviour: everything).

- [ ] **Step 5: Commit**

```bash
gofmt -l $(git ls-files '*.go'); go build ./... && (cd packages/orq-rc && go build ./...)
git add cli/custom/skills
git commit -m "feat(skills): remove selects by agent and scope, counts links elsewhere (RES-1437)"
```

---

### Task 4: `Place` on status links; `InstallSession` and launch dry-run use `ScopeFor`

**Files:**
- Modify: `cli/custom/skills/status.go`, `cli/custom/skills/session.go:29`, `cli/custom/launch/mcp.go:64`
- Test: `cli/custom/skills/skills_test.go`, `cli/custom/launch/session_skills_test.go`

**Interfaces:**
- Produces:
  ```go
  type Place string
  const (PlaceGlobal Place = "global"; PlaceLocal Place = "local"; PlaceElsewhere Place = "elsewhere")
  type LinkStatus struct { Path, Agent string; State LinkState; Place Place }
  func PlaceOf(dir string) Place   // against $HOME and cwd
  ```

- [ ] **Step 1: Failing tests**

In `skills_test.go`:

```go
func TestReadStatusPlacesLinksAgainstCwd(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	repoA := t.TempDir()
	repoB := t.TempDir()
	t.Chdir(repoA)
	if _, err := Install([]string{"claude"}, ScopeGlobal); err != nil {
		t.Fatal(err)
	}
	if _, err := Install([]string{"claude"}, ScopeLocal); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoB)
	if _, err := Install([]string{"claude"}, ScopeLocal); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus()
	if err != nil {
		t.Fatal(err)
	}
	got := map[Place]int{}
	for _, l := range status.Links {
		got[l.Place]++
	}
	names, _ := Names()
	want := map[Place]int{PlaceGlobal: len(names), PlaceLocal: len(names), PlaceElsewhere: len(names)}
	for p, n := range want {
		if got[p] != n {
			t.Errorf("%s: got %d links, want %d", p, got[p], n)
		}
	}
}

func TestInstallSessionLinksLocallyOutsideHome(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	project := t.TempDir()
	t.Chdir(project)
	release, err := InstallSession("claude")
	if err != nil {
		t.Fatal(err)
	}
	names, _ := Names()
	local := filepath.Join(project, ".claude", "skills", names[0])
	if !exists(local) {
		t.Fatalf("session link not created at %s", local)
	}
	if exists(filepath.Join(home, ".claude", "skills", names[0])) {
		t.Error("a session launched from a project wrote into $HOME")
	}
	release()
	if exists(local) {
		t.Error("session link survived release")
	}
}

func TestInstallSessionLinksGloballyFromHome(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Chdir(home)
	release, err := InstallSession("claude")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	names, _ := Names()
	if !exists(filepath.Join(home, ".claude", "skills", names[0])) {
		t.Error("a session launched from $HOME did not link globally")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cli/custom/skills -run 'TestReadStatusPlaces|TestInstallSessionLinks' 2>&1 | head`
Expected: `Place` undefined; the session tests fail on the local path.

- [ ] **Step 3: Implement**

`status.go` additions:

```go
// Place is where a recorded link sits relative to the current directory:
// in the global set, in this directory's local set, or in some other
// directory's local set. It is computed at read time, not stored: the
// manifest holds absolute paths, and the same path is "local" from one cwd
// and "elsewhere" from the next.
type Place string

const (
	PlaceGlobal    Place = "global"
	PlaceLocal     Place = "local"
	PlaceElsewhere Place = "elsewhere"
)

// PlaceOf classifies a directory. It asks Targets for every agent in both
// scopes, so it cannot disagree with what Install would write here.
func PlaceOf(dir string) Place {
	targets, err := Targets(allAgents(), ScopeBoth)
	if err != nil {
		return PlaceElsewhere
	}
	dir = filepath.Clean(dir)
	for _, tg := range targets {
		if filepath.Clean(tg.Dir) != dir {
			continue
		}
		if tg.Global {
			return PlaceGlobal
		}
		return PlaceLocal
	}
	return PlaceElsewhere
}
```

Add `Place Place` to `LinkStatus`, and in `ReadStatus` fill it: `Place: PlaceOf(filepath.Dir(l.Path))`. Add `"path/filepath"` to the imports.

`session.go:29`: 
```go
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	targets, err := Targets([]string{agent}, ScopeFor(cwd))
```

`launch/mcp.go:64`:
```go
		cwd, err := os.Getwd()
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("skills unavailable this session: %v", err))
			return
		}
		targets, err := skills.Targets([]string{agent}, skills.ScopeFor(cwd))
```

Update the doc comment on `maybeInstallSessionSkills`: add one sentence — "Links land in the current directory's local set, or the global set when launched from $HOME (skills.ScopeFor)."

- [ ] **Step 4: Launch test**

In `cli/custom/launch/session_skills_test.go` add:

```go
func TestDryRunNamesTheLocalSkillsDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	project := t.TempDir()
	t.Chdir(project)
	ctx := &AgentContext{Flags: LaunchFlags{DryRun: true}}
	plan := &LaunchPlan{}
	maybeInstallSessionSkills(ctx, plan, "claude")
	want := filepath.Join(project, ".claude", "skills")
	found := false
	for _, n := range plan.Notes {
		found = found || strings.Contains(n, want)
	}
	if !found {
		t.Errorf("dry-run notes do not name %s:\n%v", want, plan.Notes)
	}
}
```

Adjust the `AgentContext`/`LaunchFlags` construction to match how sibling tests in that file build them (copy the first test's setup). On Windows the function returns early; guard with `if runtime.GOOS == "windows" { t.Skip() }`.

- [ ] **Step 5: Run both packages**

Run: `go test ./cli/custom/skills ./cli/custom/launch 2>&1 | tail -20`
Expected: PASS. Existing launch tests that assert a session link under `$HOME` while cwd is elsewhere must now `t.Chdir(home)` first, or assert the local path — read each failure and pick the one matching its intent (a test about "launch from home" chdirs home; a test about "links exist during session" asserts the local path).

- [ ] **Step 6: Commit**

```bash
gofmt -l $(git ls-files '*.go'); go build ./... && (cd packages/orq-rc && go build ./...)
git add cli/custom/skills cli/custom/launch
git commit -m "feat(skills): classify links by place; sessions link into the launch directory (RES-1437)"
```

---

### Task 5: Command layer — flags reach skills; connect writes and removes by scope

**Files:**
- Modify: `cli/custom/commands/connect.go` (`checkScopeFlags` 1381-1401, `capScoped` 1407, `removeWiring` 1287-1300, `skillsPayload` 757-772, dry-run preview 843), `cli/custom/commands/setup.go` (`instrumentAgents` 1301-1322, `skillTargetsFor` 1374)
- Test: `cli/custom/commands/connect_test.go`, `cli/custom/commands/setup_test.go`

**Interfaces:**
- Produces in `commands`:
  ```go
  func skillsWriteScope(opts *setupOptions) skills.Scope   // Local when --local, else Global
  func skillsRemoveScope(opts *setupOptions) skills.Scope  // Local/Global when named, else Both
  func skillTargetsFor(agents []string, scope skills.Scope) []skills.Target
  ```

- [ ] **Step 1: Replace `TestLocalWarnsWhenNothingInTheRunHasAScope`** (connect_test.go:2432) with:

```go
// --local on a skills run installs into the current directory and touches
// nothing under $HOME.
func TestLocalSkillsInstallIntoTheCurrentDirectory(t *testing.T) {
	home, project := mcpMachine(t, ".claude")
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")

	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"claude", "skills", "--local"})
		if err := c.Execute(); err != nil {
			t.Fatalf("connect: %v", err)
		}
	})
	if strings.Contains(out, "nothing to scope") {
		t.Errorf("--local was reported as a no-op for skills:\n%s", out)
	}
	names, _ := skills.Names()
	if _, err := os.Stat(filepath.Join(project, ".claude", "skills", names[0])); err != nil {
		t.Errorf("no local link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Error("--local wrote into $HOME")
	}
	if !strings.Contains(out, ".gitignore") {
		t.Errorf("no gitignore hint on a local install:\n%s", out)
	}
}

// gateway is the one capability without a project scope; the warning names it
// and does not pretend mcp is the only scoped capability.
func TestLocalWithGatewayAndSkillsWarnsAboutGatewayOnly(t *testing.T) {
	mcpMachine(t, ".claude")
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"claude", "gateway", "skills", "--local", "--no-gateway"})
		_ = c.Execute()
	})
	if strings.Contains(out, "only the mcp capability") || strings.Contains(out, "scopes mcp only") {
		t.Errorf("stale mcp-only wording:\n%s", out)
	}
}

func TestBareDisconnectRemovesSkillsFromBothScopesAndCountsOthers(t *testing.T) {
	home, project := mcpMachine(t, ".claude")
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	other := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Chdir(dir)
		return captureOutput(t, func() {
			c := NewConnectCommand()
			c.SetArgs(args)
			if err := c.Execute(); err != nil {
				t.Fatalf("connect %v: %v", args, err)
			}
		})
	}
	run(project, "claude", "skills")
	run(project, "claude", "skills", "--local")
	run(other, "claude", "skills", "--local")

	t.Chdir(project)
	out := captureOutput(t, func() {
		d := NewDisconnectCommand()
		d.SetArgs([]string{"claude", "skills", "--yes"})
		if err := d.Execute(); err != nil {
			t.Fatalf("disconnect: %v", err)
		}
	})
	names, _ := skills.Names()
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", names[0])); !os.IsNotExist(err) {
		t.Error("global link survived a bare disconnect")
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "skills", names[0])); !os.IsNotExist(err) {
		t.Error("local link survived a bare disconnect")
	}
	if _, err := os.Stat(filepath.Join(other, ".claude", "skills", names[0])); err != nil {
		t.Error("a local install in another directory was removed")
	}
	if !strings.Contains(out, "other director") {
		t.Errorf("links elsewhere were not counted:\n%s", out)
	}
}
```

Check how existing disconnect tests pass consent (`--yes` or `-y`) and use the same.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cli/custom/commands -run 'TestLocalSkillsInstall|TestLocalWithGateway|TestBareDisconnectRemovesSkills' 2>&1 | head -20`
Expected: FAIL (local link missing / stale wording / compile error on `skillTargetsFor`).

- [ ] **Step 3: Implement**

`connect.go`:

```go
// capScoped reports whether a capability has a project scope at all. Scope is
// a property of the capability, not of the run, so `orq disconnect codex
// --local` cannot narrow to a project and then delete the machine-wide
// gateway anyway.
func capScoped(cap string) bool { return cap == capMCP || cap == capSkills }
```

`checkScopeFlags` wording:

```go
			return fmt.Errorf("--local writes project config into the current directory, and %s is your home directory, not a project — config written there applies to every session you start from home rather than to one project. Use --global", tilde(home))
	...
	switch {
	case len(unscoped) == len(caps):
		rep.warn("--local has nothing to scope here: %s %s machine-wide", strings.Join(unscoped, " and "), plural(len(unscoped), "is", "are"))
	case len(unscoped) > 0:
		rep.warn("--local scopes mcp and skills only: %s %s machine-wide either way", strings.Join(unscoped, " and "), plural(len(unscoped), "is", "are"))
	}
```

Scope conversions (next to `mcpWriteScope`):

```go
// skillsWriteScope mirrors mcpWriteScope for the skills package: a write picks
// one scope, and scopeUnset means global.
func skillsWriteScope(opts *setupOptions) skills.Scope {
	if opts.scope == scopeLocal {
		return skills.ScopeLocal
	}
	return skills.ScopeGlobal
}

// skillsRemoveScope mirrors scopedPaths: a named scope removes from that one,
// none removes from both.
func skillsRemoveScope(opts *setupOptions) skills.Scope {
	switch opts.scope {
	case scopeLocal:
		return skills.ScopeLocal
	case scopeGlobal:
		return skills.ScopeGlobal
	}
	return skills.ScopeBoth
}
```

`removeWiring` skills block: `res, err := skills.Remove(agents, skillsRemoveScope(opts))`, and after the existing `Disowned` reporting add:

```go
		if n := len(res.Elsewhere); n > 0 {
			dirs := map[string]bool{}
			for _, p := range res.Elsewhere {
				dirs[filepath.Dir(p)] = true
			}
			rep.info("%-8s %-9s left %d links in %d other director%s — run 'orq disconnect skills --local' from there", "", capSkills, n, len(dirs), plural(len(dirs), "y", "ies"))
		}
```

`skillsPayload(agents, scope)`: add `entry["scope"] = scopeLabel(t.Global)`; update its caller at line 602 to pass `skillsWriteScope(opts)`. Dry-run preview at 843: `skillTargetsFor(agents, skillsWriteScope(opts))`.

`setup.go`:

```go
func skillTargetsFor(agents []string, scope skills.Scope) []skills.Target {
	targets, err := skills.Targets(agents, scope)
	if err != nil {
		return nil
	}
	return targets
}
```

`instrumentAgents`:

```go
	if len(selected) > 0 && hasCap(opts.caps, capSkills) {
		scope := skillsWriteScope(opts)
		res, err := skills.Install(selected, scope)
		if err != nil {
			return nil, fmt.Errorf("installing skills: %w", err)
		}
		if !opts.finalScreen {
			for _, target := range skillTargetsFor(selected, scope) {
				rep.ok("%-8s %-9s %s", target.Agent, capSkills, tilde(target.Dir))
			}
		}
		for _, path := range res.Skipped {
			rep.warn("%-8s %-9s %s already exists and is not ours — left alone", "", capSkills, tilde(path))
		}
		if scope == skills.ScopeLocal {
			reportLocalSkillsNotes(rep, selected, skillTargetsFor(selected, scope))
		}
		for _, id := range selected {
			for _, t := range skillTargetsFor([]string{id}, scope) {
				skillDirs[id] = t.Dir
			}
		}
	}
```

Add to `setup.go`:

```go
// reportLocalSkillsNotes is what a local install has to say and the global
// one does not: the directories are untracked files in the repo, kimi reads
// project skills from the repository root only, and pi loads them only for a
// trusted project. Each line prints only when it applies.
func reportLocalSkillsNotes(rep *reporter, agents []string, targets []skills.Target) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	root, inRepo := skills.RepoRoot(cwd)
	if inRepo && !sameDir(root, cwd) && slices.Contains(agents, "kimi") {
		rep.warn("--local writes into %s, but the repository root is %s; kimi reads project skills from the root only", tilde(cwd), tilde(root))
	}
	if inRepo {
		var rels []string
		for _, t := range targets {
			if rel, err := filepath.Rel(cwd, t.Dir); err == nil {
				rels = append(rels, rel+"/")
			}
		}
		rep.info("add %s to .gitignore — the links point into ~/.orq and mean nothing to anyone else", strings.Join(rels, " and "))
	}
	if slices.Contains(agents, "pi") {
		rep.info("%-8s %-9s pi loads project skills only for a trusted project — approve it in pi once", "pi", capSkills)
	}
}
```

Spec §3 says the kimi warning prints when kimi could read it — print it when kimi is among the selected agents; a run without kimi has nobody to warn about.

- [ ] **Step 4: Run and fix the rest of the package**

Run: `go test ./cli/custom/commands 2>&1 | tail -40`

Expected breakage to resolve:
- Every `skillTargetsFor(x)` call gets a scope (`skillsWriteScope(opts)` where opts exists, `skills.ScopeGlobal` in the final-screen path if no opts is in reach).
- `TestScopeFlagsRejectWhatTheyCannotMean` may assert the `~/.mcp.json` wording; update to the new sentence.
- `setup_test.go:1605` names `/home/u/.codex/skills` as a codex skills dir in a fixture; change to `/home/u/.agents/skills`.

- [ ] **Step 5: Commit**

```bash
gofmt -l $(git ls-files '*.go'); go build ./... && (cd packages/orq-rc && go build ./...)
git add cli/custom/commands
git commit -m "feat(connect): --local and --global reach skills (RES-1437)"
```

---

### Task 6: `--status`, `--json` and `doctor` classify by place

**Files:**
- Modify: `cli/custom/commands/connect.go:1100-1129` (skills rows), `:1139-1220` (`reportBrokenSkillLinks`), `cli/custom/commands/agents.go:1159-1220` (`skillsCheck`)
- Test: `cli/custom/commands/connect_test.go`, `cli/custom/commands/doctor_test.go`, `cli/custom/commands/ui_test.go:95-99`

- [ ] **Step 1: Failing tests**

`connect_test.go`:

```go
func TestStatusShowsSkillsScopeAndHidesOtherDirectories(t *testing.T) {
	_, project := mcpMachine(t, ".claude")
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	other := t.TempDir()
	connect := func(dir string, args ...string) {
		t.Chdir(dir)
		c := NewConnectCommand()
		c.SetArgs(args)
		if err := c.Execute(); err != nil {
			t.Fatalf("connect %v: %v", args, err)
		}
	}
	connect(project, "claude", "skills")
	connect(project, "claude", "skills", "--local")
	connect(other, "claude", "skills", "--local")

	t.Chdir(project)
	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"--status"})
		if err := c.Execute(); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "global") || !strings.Contains(out, "local") {
		t.Errorf("status rows carry no scope:\n%s", out)
	}
	if strings.Contains(out, other) || strings.Contains(out, tilde(other)) {
		t.Errorf("status listed a local install from another directory:\n%s", out)
	}
	if !strings.Contains(out, "other director") {
		t.Errorf("status did not count the install elsewhere:\n%s", out)
	}
}
```

`ui_test.go:95-99`: change the fixture row to `{cells: []string{"", "skills", "global", "~/.agents/skills"}}` with headers `AGENT, CAPABILITY, SCOPE, LOCATION` and the matching `want` string (recompute column widths by hand: `CAPABILITY` is 10 wide, `SCOPE` 6).

`doctor_test.go` — find the existing skills doctor test (search `skillsCheck` / `"skills"`) and add:

```go
func TestSkillsCheckBucketsByPlace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	project := t.TempDir()
	other := t.TempDir()
	t.Chdir(other)
	if _, err := skills.Install([]string{"claude"}, skills.ScopeLocal); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	if _, err := skills.Install([]string{"claude"}, skills.ScopeLocal); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(project, ".claude")); err != nil {
		t.Fatal(err)
	}
	check, ok := skillsCheck()
	if !ok || check.Status != "warn" {
		t.Fatalf("check = %+v, %v", check, ok)
	}
	if !strings.Contains(check.Message, "--local") {
		t.Errorf("a missing local install must point at connect --local: %s", check.Message)
	}
	if check.Details["elsewhere"] != len(mustNames(t)) {
		t.Errorf("elsewhere = %v", check.Details["elsewhere"])
	}
}
```

(`mustNames` = `skills.Names()` with a fatal on error; define it in the test file if there is no such helper.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cli/custom/commands -run 'TestStatusShowsSkillsScope|TestSkillsCheckBuckets|TestRenderTable' 2>&1 | head -30`

- [ ] **Step 3: Implement**

`wiredTargets` skills block:

```go
	if hasCap(caps, capSkills) {
		status, err := skills.ReadStatus()
		if err == nil && status != nil {
			idx := map[string]int{}
			wanted := map[string]bool{}
			sharedWanted := false
			for _, id := range agents {
				wanted[id] = true
				sharedWanted = sharedWanted || skills.SharedReader(id)
			}
			for _, l := range status.Links {
				if l.Agent == "" && !sharedWanted || l.Agent != "" && !wanted[l.Agent] {
					continue
				}
				if l.Place == skills.PlaceElsewhere {
					continue // counted by reportBrokenSkillLinks
				}
				dir := filepath.Dir(l.Path)
				i, seen := idx[dir]
				if !seen {
					out = append(out, wiredTarget{agent: l.Agent, capability: capSkills, path: dir, scope: string(l.Place), status: "pass"})
					i = len(out) - 1
					idx[dir] = i
				}
				if l.State != skills.LinkInstalled || status.Stale {
					out[i].status = "warn"
				}
			}
		}
	}
```

Update the comment on `printWiredTable` ("Only mcp carries a scope") to "gateway carries no scope".

`reportBrokenSkillLinks`: extend `inScope` to also require `l.Place != skills.PlaceElsewhere`, and at the end:

```go
	elsewhere := map[string]int{}
	for _, l := range status.Links {
		if l.Place == skills.PlaceElsewhere && (l.Agent == "" && sharedWanted || wanted[l.Agent]) {
			elsewhere[filepath.Dir(l.Path)]++
		}
	}
	if n := len(elsewhere); n > 0 {
		total := 0
		for _, c := range elsewhere {
			total += c
		}
		rep.info("skills   %d links in %d other director%s are not shown — run from there to see or remove them", total, n, plural(n, "y", "ies"))
	}
```

The missing-link remedy text: when the directory's place is local, say `run 'orq connect skills --local' from here`; keep the existing text for global. Compute `place := skills.PlaceOf(dir)` inside the loop.

`skillsCheck` in `agents.go`: count per place.

```go
	var missingGlobal, missingLocal, elsewhere int
	for _, l := range status.Links {
		switch {
		case l.Place == skills.PlaceElsewhere:
			elsewhere++
		case l.State == skills.LinkMissing && l.Place == skills.PlaceGlobal:
			missingGlobal++
		case l.State == skills.LinkMissing:
			missingLocal++
		}
	}
	check.Details["elsewhere"] = elsewhere
	switch {
	case missingGlobal == 0 && missingLocal == 0 && foreign == 0 && !status.Stale:
		pass...
	case missingGlobal > 0:
		"%d of %d recorded orq skills are not installed in %s — run 'orq connect skills' to install them"
	case missingLocal > 0:
		"%d of %d recorded orq skills are not installed in %s — run 'orq connect skills --local' from that directory"
	case foreign > 0: unchanged
	default: unchanged
	}
```

`recorded` stays the full count; `missing` in Details stays the sum. Never fail on any of them.

- [ ] **Step 4: Run the package**

Run: `go test ./cli/custom/commands 2>&1 | tail -30`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l $(git ls-files '*.go'); go build ./... && (cd packages/orq-rc && go build ./...)
git add cli/custom/commands
git commit -m "feat(connect): status, --json and doctor report the skills scope for this directory (RES-1437)"
```

---

### Task 7: `orq setup` asks about scope for skills too

**Files:**
- Modify: `cli/custom/commands/setup.go:1662-1700` (`scopeMatters`, `promptForScope`)
- Test: `cli/custom/commands/setup_test.go:2877-2910`

- [ ] **Step 1: Rewrite the two tests**

```go
// gateway is the only capability that never reads a scope.
func TestSetupScopeIsNotAskedWhenNothingScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if scopeMatters([]string{capGateway}) {
		t.Error("a gateway-only run asked about scope")
	}
	if !scopeMatters([]string{capGateway, capSkills}) {
		t.Error("skills have a project scope and were not asked about")
	}
	opts := &setupOptions{}
	if err := resolveScope(newReporter(true), opts, []string{capGateway}); err != nil {
		t.Fatalf("resolveScope: %v", err)
	}
	if opts.scope != scopeUnset {
		t.Errorf("an unasked question still answered itself: %+v", opts)
	}
}

// mcp asks only when a detected agent has two config paths; skills ask when a
// detected agent receives them at all.
func TestSetupScopeMattersPerCapability(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if scopeMatters([]string{capMCP}) || scopeMatters([]string{capSkills}) {
		t.Error("scope mattered on a machine with no agent at all")
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !scopeMatters([]string{capMCP}) {
		t.Error("claude has two MCP scopes and was not asked about")
	}
	if !scopeMatters([]string{capSkills}) {
		t.Error("claude receives skills and was not asked about")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cli/custom/commands -run 'TestSetupScopeIsNotAsked|TestSetupScopeMattersPerCapability' 2>&1 | head`

- [ ] **Step 3: Implement**

```go
// scopeMatters answers "would either answer change what this run writes?".
// Skills: any detected agent that receives them has two places to put them.
// MCP: only an agent with two config paths does.
func scopeMatters(caps []string) bool {
	for _, spec := range agentRegistry() {
		if !spec.detect() {
			continue
		}
		if hasCap(caps, capSkills) && skills.Receives(spec.ID) {
			return true
		}
		if hasCap(caps, capMCP) && spec.writeMCP != nil && mcpScopeAware(spec) {
			return true
		}
	}
	return false
}
```

`promptForScope`: `Message: "Where should the MCP entry and skills go?"`, and rewrite the doc comment's last two sentences: "One question covers both scoped capabilities; the answer is the same kind of answer."

- [ ] **Step 4: Run the package, commit**

```bash
go test ./cli/custom/commands 2>&1 | tail -5
gofmt -l $(git ls-files '*.go'); go build ./... && (cd packages/orq-rc && go build ./...)
git add cli/custom/commands
git commit -m "feat(setup): the scope question covers skills (RES-1437)"
```

---

### Task 8: MCP project scope for codex, opencode and kilo

**Files:**
- Modify: `cli/custom/commands/agents.go:97-150, 238-250`, `cli/custom/commands/connect.go:690-700` (trust line)
- Test: `cli/custom/commands/agents_mcp_test.go`, `cli/custom/commands/connect_test.go:2357-2432`

- [ ] **Step 1: Rewrite the two global-only tests and add a round trip**

Replace `TestLocalScopeAgainstAGlobalOnlyAgentWarnsAndWritesGlobal` (connect_test.go:2357) with:

```go
func TestLocalMCPWritesCodexProjectConfigAndPrintsTheTrustLine(t *testing.T) {
	home, project := mcpMachine(t, ".codex")
	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"codex", "mcp", "--local"})
		if err := c.Execute(); err != nil {
			t.Fatalf("connect: %v", err)
		}
	})
	local := filepath.Join(project, ".codex", "config.toml")
	if !strings.Contains(string(mustRead(t, local)), launch.MCPServerName) {
		t.Error("the entry did not land in the project config")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Error("--local also wrote the global config")
	}
	if !strings.Contains(out, "trust_level") {
		t.Errorf("no trust line for a local codex write:\n%s", out)
	}
	assertNoCredential(t, local)
}
```

Replace `TestScopedDisconnectWarnsForAGlobalOnlyAgent` (connect_test.go:2412) with a scoped removal that leaves the other scope alone:

```go
func TestScopedDisconnectLeavesTheOtherScope(t *testing.T) {
	home, project := mcpMachine(t, ".codex")
	for _, args := range [][]string{{"codex", "mcp"}, {"codex", "mcp", "--local"}} {
		c := NewConnectCommand()
		c.SetArgs(args)
		if err := c.Execute(); err != nil {
			t.Fatalf("connect %v: %v", args, err)
		}
	}
	d := NewDisconnectCommand()
	d.SetArgs([]string{"codex", "mcp", "--local", "--yes"})
	if err := d.Execute(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(project, ".codex", "config.toml"))), launch.MCPServerName) {
		t.Error("the project entry survived --local removal")
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(home, ".codex", "config.toml"))), launch.MCPServerName) {
		t.Error("--local removed the global entry too")
	}
}
```

In `agents_mcp_test.go` add:

```go
func TestProjectScopeForCodexOpencodeAndKilo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	project := t.TempDir()
	t.Chdir(project)
	want := map[string][2]string{
		"codex":    {filepath.Join(project, ".codex", "config.toml"), filepath.Join(home, ".codex", "config.toml")},
		"opencode": {filepath.Join(project, "opencode.json"), filepath.Join(home, ".config", "opencode", "opencode.json")},
		"kilo":     {filepath.Join(project, "kilo.json"), filepath.Join(home, ".config", "kilo", "kilo.json")},
	}
	for id, paths := range want {
		spec, _ := lookupAgent(id)
		local, _ := spec.mcpConfig(false)
		global, _ := spec.mcpConfig(true)
		if local != paths[0] || global != paths[1] {
			t.Errorf("%s: local=%q global=%q; want %q, %q", id, local, global, paths[0], paths[1])
		}
		if !mcpScopeAware(spec) {
			t.Errorf("%s is not scope-aware", id)
		}
		if err := spec.writeMCP(local, "https://example.test/mcp"); err != nil {
			t.Fatalf("%s: write: %v", id, err)
		}
		if !spec.mcpPresent(local) {
			t.Errorf("%s: entry not present after write", id)
		}
		if _, err := os.Stat(global); !os.IsNotExist(err) {
			t.Errorf("%s: project write touched the global file", id)
		}
		assertNoCredential(t, local)
	}
}

func TestCodexProjectScopeHonoursCodexHomeForGlobalOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	project := t.TempDir()
	t.Chdir(project)
	spec, _ := lookupAgent("codex")
	global, _ := spec.mcpConfig(true)
	local, _ := spec.mcpConfig(false)
	if global != filepath.Join(codexHome, "config.toml") {
		t.Errorf("global = %q", global)
	}
	if local != filepath.Join(project, ".codex", "config.toml") {
		t.Errorf("local = %q", local)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cli/custom/commands -run 'TestProjectScopeFor|TestCodexProjectScope|TestLocalMCPWritesCodex|TestScopedDisconnectLeaves' 2>&1 | head -20`

- [ ] **Step 3: Implement**

`agents.go`:

```go
// codexMCPPath is codex's MCP config in either scope: the project
// .codex/config.toml at cwd, or config.toml under $CODEX_HOME (~/.codex).
// projectOrGlobalPath cannot express the env fallback and codexPath cannot
// express the project side, so this is its own resolver.
func codexMCPPath() func(bool) (string, error) {
	global := codexPath("config.toml")
	return func(isGlobal bool) (string, error) {
		if isGlobal {
			return global(true)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".codex", "config.toml"), nil
	}
}
```

Registry: codex `mcpConfig: codexMCPPath()`; opencode `mcpConfig: projectOrGlobalPath("opencode.json", ".config/opencode/opencode.json")`; kilo `mcpConfig: projectOrGlobalPath("kilo.json", ".config/kilo/kilo.json")`. Leave `providerConfig` on all three global (`alwaysGlobalPath`) — the env-reference restriction is real for the provider entry. Move the "Global only" comments so they sit on the `providerConfig` lines, not above `detect`. Update `projectOrGlobalPath`'s comment: "claude, kimi, opencode and kilo have two scopes; codex has its own resolver".

`kiloMCPPresent`/`kiloRemoveMCP` currently take the `.json` path and also look at `.jsonc` beside it — confirm by reading them; they work unchanged for `<cwd>/kilo.json`.

`connect.go` in `connectMCP`, after the `mcpLoginLine` block:

```go
			if id == "codex" && !global {
				rep.info("%-8s %-9s codex loads a project config only when %s marks this repository trusted: [projects.\"<repo root>\"] trust_level = \"trusted\"", id, capMCP, tilde(codexBaseConfigPath()))
			}
```

Delete `warnGlobalOnlyMCPScope` and its two call sites (connect.go ~662 and ~1279) if no agent with `writeMCP` is global-only any more (pi has no MCP). Keep `mcpScopeAware`: it is still what `scopeMatters` and the scope label use.

- [ ] **Step 4: Run the package**

Run: `go test ./cli/custom/commands 2>&1 | tail -30`
Expected: PASS. `TestMCPRegistryRoundTrip` and siblings iterate `mcpConfig(true)`; unaffected.

- [ ] **Step 5: Commit**

```bash
gofmt -l $(git ls-files '*.go'); go build ./... && (cd packages/orq-rc && go build ./...)
git add cli/custom/commands
git commit -m "feat(connect): project-scoped MCP config for codex, opencode and kilo (RES-1437)"
```

---

### Task 9: Golden output for `connect skills --local`, and the full suite

**Files:**
- Test: `cli/custom/commands/connect_test.go`

- [ ] **Step 1: Write the golden test**

```go
// The whole local-install summary in one place, so the warning, the
// gitignore line and the pi line are reviewed in a diff rather than by hand.
func TestLocalSkillsSummaryGolden(t *testing.T) {
	_, project := mcpMachine(t, ".claude", ".pi/agent", ".kimi-code")
	t.Setenv("ORQ_API_KEY", "sk-orq-TEST")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(project, "packages", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"claude", "pi", "kimi", "skills", "--local"})
		if err := c.Execute(); err != nil {
			t.Fatalf("connect: %v", err)
		}
	})
	for _, want := range []string{
		"repository root is",
		"kimi reads project skills from the root only",
		"add .agents/skills/ and .claude/skills/ to .gitignore",
		"pi loads project skills only for a trusted project",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "trust_level") {
		t.Errorf("a skills run printed codex's MCP trust line:\n%s", out)
	}
}
```

If `mcpMachine` cannot create nested dirs like `.pi/agent`, create them with `os.MkdirAll` after the call. Sorting: `Targets` sorts by path, so `.agents` precedes `.claude`; the gitignore line must match that order.

- [ ] **Step 2: Run, then the whole CI set**

```bash
go test ./cli/custom/commands -run TestLocalSkillsSummaryGolden -v 2>&1 | tail -5
go test ./... 2>&1 | tail -20
go vet ./... && gofmt -l $(git ls-files '*.go')
cd packages/orq-rc && go build ./... && go vet ./... && cd ../..
go run ./cmd/surface-dump -check
```

Expected: all PASS, `gofmt -l` prints nothing, surface check reports no change.

- [ ] **Step 3: Commit**

```bash
git add cli/custom/commands
git commit -m "test(connect): golden summary for a local skills install (RES-1437)"
```

---

### Task 10: Changelog, README, and PR

**Files:**
- Modify: `CHANGELOG.md` (under `## Unreleased`), `README.md` (where `orq connect` / `--local` is documented — search `grep -n "connect\|--local" README.md`)

- [ ] **Step 1: Changelog entry**

Under `## Unreleased`:

```markdown
- **Added:** `orq connect skills --local` installs the skill set into the current
  directory (`./.claude/skills` for Claude Code, `./.agents/skills` for every
  other agent) instead of `$HOME`; `--global` stays the default. A bare
  `orq disconnect skills` removes both scopes and counts local installs made
  from other directories. `orq connect --status`, `--json` and `orq doctor`
  label each skills directory `global` or `local` for the directory you run
  them from. `orq setup` asks the scope question when skills are selected.
- **Changed:** `orq launch` links session skills into the current directory,
  or into `$HOME` when launched from there. Kimi and Windows are unchanged.
- **Changed:** skills are written to `~/.agents/skills` for codex and kimi, not
  to `~/.codex/skills` and `~/.kimi-code/skills` as well. Both agents read the
  shared directory, and codex listed every orq skill twice. The next
  `orq connect skills` removes the old links. The Linux-only
  `~/.config/agents/skills` directory, which no agent reads, is no longer written.
- **Added:** `orq connect <agent> mcp --local` writes a project config for codex
  (`.codex/config.toml`), opencode (`opencode.json`) and kilo (`kilo.json`).
  Codex loads its project config only for a repository marked trusted in
  `~/.codex/config.toml`; connect prints the line to add.
```

- [ ] **Step 2: README**

Add one short paragraph next to the existing `--local` / `--global` mention for MCP (or next to `orq connect skills` if MCP scope is not documented) stating the two local directories and the `.gitignore` recommendation. Keep it to four lines.

- [ ] **Step 3: Final checks and push**

```bash
go test ./... && go vet ./... && gofmt -l $(git ls-files '*.go')
git add CHANGELOG.md README.md
git commit -m "docs: changelog and README for skills scopes (RES-1437)"
git push -u origin Baukebrenninkmeijer/res-1437-skills-scopes
```

Open the PR with title `feat(skills): --local and --global installation scopes (RES-1437)`. Body: the spec path, the Linear ticket, the four changelog bullets, and a note that `surface.json` is unchanged because no flag was added.

---

## Self-review

**Spec coverage:** §1 flags → Task 5; §2 two dirs, registry fixes, reconciliation → Tasks 1–2; §3 anchor, warning, root helper → Tasks 1, 5; §4 Scope type, Remove predicate, elsewhere count → Tasks 1, 3, 5; §5 launch → Task 4 (kimi and Windows paths untouched by construction); §6 status/json/doctor → Task 6; §7 output lines → Task 5 (notes), Task 9 (golden); §8 MCP project scope, trust line → Task 8; §9 touch points → each task names its lines; sibling-spec correction already landed with the spec commit. Tests list: root-helper cases (T1), `$HOME` refusal for skills (existing test + `capScoped`, T5), gateway+skills warning (T5), non-repo cwd (T2's `TestInstallLocalLinksIntoCwd` uses a plain temp dir), subdirectory warning (T9), reconciliation incl. `$CODEX_HOME` (T2), Remove scopes (T3), launch local/home (T4), Windows early return unchanged (existing test), status/json/doctor (T6), `scopeMatters` (T7), MCP round trips incl. `$CODEX_HOME` and the trust line (T8), golden (T9).

**Deviation from the spec, deliberate:** §2 says reconciliation removes links "whose directory is not in the current global target set". Taken literally that deletes every local install from other directories on the next global connect. Task 2 reconciles only the explicit retired directories (`retiredDirs`), which is what the sentence was for. Record this in the PR body.

**Type consistency:** `Scope`/`ScopeGlobal`/`ScopeLocal`/`ScopeBoth`, `ScopeFor(cwd)`, `RepoRoot(dir)`, `Targets(agents, scope)`, `Install(agents, scope)`, `Remove(agents, scope)`, `Result.Elsewhere`, `Place`/`PlaceOf`, `LinkStatus.Place`, `Target.Global`, `skillsWriteScope`, `skillsRemoveScope`, `skillTargetsFor(agents, scope)`, `codexMCPPath()`, `reportLocalSkillsNotes` — used with the same names in every task.
