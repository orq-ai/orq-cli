package launch

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"orq/cli/custom/skills"
)

// TestMain isolates HOME for the whole package. Resolving an agent now
// materializes session skills into the user's real skills directory, and a
// plain `go test` run must not write into the developer's (or CI runner's)
// home to do it.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "orq-launch-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

func sessionSkillsCtx(flags GatewayFlags) *AgentContext {
	return &AgentContext{
		Creds:     &Credentials{APIKey: "orq-key", APIBaseURL: DefaultGatewayAPIBaseURL, Kind: CredentialAPIKey},
		Getenv:    env(nil),
		Flags:     flags,
		Fetch:     fetcherOf("openai/gpt-5-mini"),
		ExecProbe: okProbe,
	}
}

// The agents whose skills directory is the user's real home: nothing the
// launcher can redirect, so the links are installed there for the session and
// released when it exits.
func realHomeSkillAgents() map[string]string {
	return map[string]string{
		"claude":   filepath.Join(".claude", "skills"),
		"codex":    filepath.Join(".codex", "skills"),
		"opencode": filepath.Join(".agents", "skills"),
		"pi":       filepath.Join(".agents", "skills"),
	}
}

func TestSessionSkillsLandInTheAgentsRealDirectoryAndAreReleased(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("session skills are not installed on the copy-fallback platform")
	}
	names, err := skills.Names()
	if err != nil || len(names) == 0 {
		t.Fatalf("Names: %v %d", err, len(names))
	}

	for agent, rel := range realHomeSkillAgents() {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			def := FindAgent(agent)
			if def == nil {
				t.Fatalf("no such agent: %s", agent)
			}
			plan, err := def.Resolve(sessionSkillsCtx(GatewayFlags{MCP: true}))
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, rel)
			for _, n := range names {
				if _, statErr := os.Lstat(filepath.Join(dir, n)); statErr != nil {
					t.Fatalf("skill %q not installed for the session: %v", n, statErr)
				}
			}
			if plan.Cleanup == nil {
				t.Fatal("no cleanup, so the session's links would outlive it")
			}
			plan.Cleanup()
			if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
				t.Errorf("cleanup left %d entries behind", len(entries))
			}
			// AddCleanup must chain, not replace: the config temp dirs each
			// agent declares have to go too.
			for _, td := range plan.TempDirs {
				if _, statErr := os.Stat(td.HostPath); statErr == nil {
					t.Errorf("cleanup left temp dir %s behind", td.HostPath)
				}
			}
		})
	}
}

func TestNoSkillsLeavesTheRealHomeAlone(t *testing.T) {
	for agent, rel := range realHomeSkillAgents() {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			plan, err := FindAgent(agent).Resolve(sessionSkillsCtx(GatewayFlags{MCP: true, NoSkills: true}))
			if err != nil {
				t.Fatal(err)
			}
			if plan.Cleanup != nil {
				plan.Cleanup()
			}
			if _, statErr := os.Stat(filepath.Join(home, rel)); !os.IsNotExist(statErr) {
				t.Errorf("--no-skills still wrote %s: %v", rel, statErr)
			}
		})
	}
}

func TestAddCleanupRunsEveryCleanup(t *testing.T) {
	plan := &LaunchPlan{}
	var order []string
	plan.AddCleanup(func() { order = append(order, "first") })
	plan.AddCleanup(func() { order = append(order, "second") })
	plan.AddCleanup(nil)
	plan.Cleanup()
	if len(order) != 2 {
		t.Fatalf("chained cleanups ran %v, want both", order)
	}
}
