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
		DefaultOutputFormat: "toon",
		Version:             "test",
	})
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
	deliberate := map[string]bool{
		"help":           true, // cobra's own, grouped via SetHelpCommandGroupID
		"completion":     true, // cobra's own, grouped via SetCompletionCommandGroupID
		"help-config":    true, // bartolo's config help page
		"help-input":     true, // bartolo's input help page
		"request":        true, // raw-HTTP escape hatch — a utility by definition
		"server":         true, // inspects/persists server-URL defaults
		"default-format": true, // persists the default output format
	}

	root := buildRoot(t)
	for _, cmd := range root.Commands() {
		// IsAvailableCommand, not just Hidden: a parent whose subcommands are all hidden never renders.
		if cmd.Hidden || !cmd.IsAvailableCommand() {
			continue
		}
		name := cmd.Name()
		if _, mapped := commandGroup[name]; mapped || deliberate[name] {
			continue
		}
		t.Errorf("visible command %q is not in commandGroup and not deliberately Utilities — it renders in Utilities by fallback, which is a wrong section until someone chooses one", name)
	}
}
