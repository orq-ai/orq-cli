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

// SharedReader reports whether agent reads the shared agents-spec directory,
// so a caller outside the package (connect --status's missing-link warning,
// at the moment) can apply the same membership rule Remove uses for links
// whose Agent is empty: they belong to the request whenever any named agent
// is a shared reader.
func SharedReader(agent string) bool { return sharedReaders[agent] }

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
//
// This is pure path resolution: it never checks whether an agent is actually
// installed on the machine. Gating on detection happens at the caller, which
// passes only the agents the user actually selected.
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
