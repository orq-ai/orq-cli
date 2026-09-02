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
	if SameDir(cwd, home) {
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
		_, err := os.Lstat(filepath.Join(cur, ".git"))
		if err == nil {
			return cur, true
		}
		if !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
}

// SameDir reports whether two paths name one directory once symlinks are
// resolved, which a symlinked $HOME otherwise defeats.
func SameDir(a, b string) bool {
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

// Belongs is the membership rule for a recorded link: a named agent owns its
// own links, and the shared directory (empty Agent) belongs to the request
// whenever any named agent is a shared reader.
func Belongs(linkAgent string, agents []string) bool {
	for _, id := range agents {
		if id == linkAgent || (linkAgent == "" && sharedReaders[id]) {
			return true
		}
	}
	return false
}

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
// codex, opencode, pi and kilo all walk from cwd up to the root, and the
// caller warns about kimi, which reads the root only.
//
// This is pure path resolution: it never checks whether an agent is actually
// installed on the machine. Gating on detection happens at the caller, which
// passes only the agents the user actually selected.
func Targets(agents []string, scope Scope) ([]Target, error) {
	type base struct {
		dir    string
		global bool
	}
	var bases []base
	if scope != ScopeLocal {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		bases = append(bases, base{home, true})
	}
	if scope != ScopeGlobal {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		// Run from $HOME, both scopes name one directory; a second entry
		// for it would let PlaceOf call a global link local.
		if !(scope == ScopeBoth && SameDir(cwd, bases[0].dir)) {
			bases = append(bases, base{cwd, false})
		}
	}
	var out []Target
	for _, b := range bases {
		shared := false
		for _, id := range agents {
			if sharedReaders[id] {
				shared = true
				continue
			}
			if rel := ownDir[id]; rel != "" {
				out = append(out, Target{Agent: id, Dir: filepath.Join(b.dir, rel), Global: b.global})
			}
		}
		if shared {
			out = append(out, Target{Dir: filepath.Join(b.dir, sharedDir), Global: b.global})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

// allAgents is every agent Targets knows, for a caller that named none.
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
