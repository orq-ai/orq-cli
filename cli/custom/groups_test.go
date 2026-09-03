package custom

import (
	"testing"

	generated "orq/cli/generated"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// buildRoot assembles the real tree because most grouping arrives from openapi.yaml; a hand-built root would test nothing that can break.
func buildRoot(t *testing.T) *cobra.Command {
	t.Helper()
	bartolocli.Init(&bartolocli.Config{
		AppName:             "orq",
		EnvPrefix:           "ORQ",
		APIKeyEnvVar:        "ORQ_API_KEY",
		SerializationFormat: "toon",
		Version:             "test",
	})
	// bartolocli.PreRun is a single package-level var that installSessionPreRun
	// and installSkillsRefreshPreRun (register.go) both chain onto by
	// capturing whatever is already there. A real process calls Register
	// exactly once, but this helper is called once per test within the same
	// test binary, and without resetting first each call would stack another
	// layer onto the last, running every earlier test's hooks again on every
	// later Execute. Save and restore around the call so each test gets
	// exactly the one clean chain a real invocation would have.
	prevPreRun := bartolocli.PreRun
	bartolocli.PreRun = nil
	t.Cleanup(func() { bartolocli.PreRun = prevPreRun })

	root := bartolocli.Root
	generated.Register(root)
	Register(root)
	return root
}

func TestEveryRootCommandResolvesToARegisteredGroup(t *testing.T) {
	root := buildRoot(t)

	for _, cmd := range root.Commands() {
		if cmd.GroupID == "" {
			t.Errorf("command %q has no group: it would render under \"Additional Commands\"", cmd.Name())
			continue
		}
		if !root.ContainsGroup(cmd.GroupID) {
			t.Errorf("command %q has group %q, which is not registered: cobra panics on this at Execute time", cmd.Name(), cmd.GroupID)
		}
	}
}

// Catches a bad group ID for a mapped command this tree does not contain, which the walk above cannot see.
func TestCommandGroupMapUsesRegisteredGroups(t *testing.T) {
	registered := map[string]bool{}
	for _, g := range helpGroups {
		registered[g.ID] = true
	}
	for name, id := range commandGroup {
		if !registered[id] {
			t.Errorf("commandGroup[%q] = %q, which is not one of the registered groups", name, id)
		}
	}
}

// Cobra prints a group's title even when every command in it is hidden.
func TestNoGroupRendersEmpty(t *testing.T) {
	root := buildRoot(t)

	populated := map[string]int{}
	for _, cmd := range root.Commands() {
		if cmd.IsAvailableCommand() {
			populated[cmd.GroupID]++
		}
	}
	for _, g := range helpGroups {
		if populated[g.ID] == 0 {
			t.Errorf("group %q (%s) has no visible commands: its header would render over an empty section", g.ID, g.Title)
		}
	}
}

// The runtime fallback launders an unmapped command into a registered group, so no other test here fails when a new OpenAPI tag adds a top-level command.
func TestEveryVisibleCommandIsMappedOrDeliberatelyUtilities(t *testing.T) {
	for _, name := range UngroupedCommands(buildRoot(t)) {
		t.Errorf("visible command %q is not in commandGroup and not deliberately Utilities — it renders in Utilities by fallback, which is a wrong section until someone chooses one", name)
	}
}

// generated.Register runs first, so an openapi.yaml tag named `setup`,
// `launch` or `doctor` would shadow a command we own: cobra resolves the pair
// by first match rather than reporting it, and no file of ours is overwritten.
// Counted on the assembled tree — attachAuthSubcommands reuses bartolo's `auth`
// parent, so registering the halves separately invents a clash that is not real.
func TestCustomCommandsDoNotCollideWithGenerated(t *testing.T) {
	root := buildRoot(t)

	count := map[string]int{}
	for _, cmd := range root.Commands() {
		count[cmd.Name()]++
	}
	if len(count) == 0 {
		t.Fatal("no commands on the assembled root")
	}
	for name, n := range count {
		if n > 1 {
			t.Errorf("%d commands are registered as %q: cobra resolves the name to "+
				"whichever registered first, so the other is unreachable. A new "+
				"openapi.yaml tag has most likely taken a name cli/custom owns.", n, name)
		}
	}
}

func TestTracesThreadAttachesToGeneratedTracesParent(t *testing.T) {
	root := buildRoot(t)
	traces, _, err := root.Find([]string{"traces"})
	if err != nil || traces == nil {
		t.Fatalf("generated traces parent = %v, %v", traces, err)
	}
	var thread *cobra.Command
	for _, child := range traces.Commands() {
		if child.Name() == "thread" {
			thread = child
			break
		}
	}
	if thread == nil {
		t.Fatal("traces thread command is not registered")
	}
	if got := thread.Use; got != "thread trace-id [span-id]" {
		t.Errorf("thread Use = %q", got)
	}
}
