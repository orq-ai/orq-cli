package custom

import (
	"testing"

	generated "orq/cli/generated"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// buildRoot assembles the real command tree the same way cmd/orq and
// cmd/surface-dump do. Grouping is a property of the ASSEMBLED tree — most of
// it arrives from openapi.yaml — so testing a hand-built root would test
// nothing that can actually break.
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

// Cobra PANICS on a GroupID it has no group for, and that check runs at
// Execute time — i.e. in the user's terminal, not in CI. A tag added to
// openapi.yaml is enough to trip it, so this walks the real tree instead of
// trusting the map.
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

// A typo'd constant in commandGroup would silently send a command to a group
// that does not exist. applyCommandGroups would then assign it verbatim and
// cobra would panic — for a command that IS in the map, the test above only
// catches it if that command exists in this tree.
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

// Cobra prints a group's title even when every command in it is hidden, which
// would leave a bare header over nothing on the first screen a user ever sees.
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
