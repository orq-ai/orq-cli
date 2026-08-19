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
// Execute time — i.e. in the user's terminal, not in CI. New tags fall back
// to Utilities and cannot trip it; the panic needs an unregistered ID in the
// tables (or a fallback regression), so this walks the real tree instead of
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

// The runtime fallback is deliberately forgiving — an unmapped command lands
// in Utilities rather than crashing or vanishing — which means nothing above
// can fail when a new OpenAPI tag generates a new top-level command: it gets
// a registered group, the map-only test never sees it, and Utilities already
// has members. This test is the missing CI signal: every visible top-level
// command must either be mapped in commandGroup or named here as belonging
// in Utilities on purpose. A new tag trips it the moment it lands, and the
// fix is one line in whichever list fits.
func TestEveryVisibleCommandIsMappedOrDeliberatelyUtilities(t *testing.T) {
	// In Utilities because that is where they belong, not by accident.
	deliberate := map[string]bool{
		"help":        true, // cobra's own, grouped via SetHelpCommandGroupID
		"completion":  true, // cobra's own, grouped via SetCompletionCommandGroupID
		"help-config": true, // bartolo's config help page
		"help-input":  true, // bartolo's input help page
		"request":        true, // raw-HTTP escape hatch — a utility by definition
		"server":         true, // inspects/persists server-URL defaults
		"default-format": true, // persists the default output format
	}

	root := buildRoot(t)
	for _, cmd := range root.Commands() {
		// Cobra's own availability rule, not just Hidden: a parent whose
		// subcommands are all hidden never renders, so it cannot sit in a
		// wrong section.
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
