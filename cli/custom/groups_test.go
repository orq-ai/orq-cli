package custom

import (
	"slices"
	"strings"
	"testing"

	"orq/cli/custom/commands"
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
	// A test that Executes runs that PreRun, which snapshots the user's
	// exported key into a package global in commands. Left set, it is what
	// every later test in this binary resolves as the configured credential —
	// and an empty snapshot reads as "the user exported nothing" even where
	// the environment says otherwise.
	t.Cleanup(commands.ResetUserEnvAPIKey)

	root := bartolocli.Root
	RegisterHelpGroups(root)
	generated.Register(root)
	Register(root, commands.TraceAPI{})
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
// Two generated operations sharing an `x-cli-group` + `x-cli-name` pair collide
// the same way (ENG-2798), so the walk covers every parent, not just the root.
// Counted on the assembled tree — attachAuthSubcommands reuses bartolo's `auth`
// parent, so registering the halves separately invents a clash that is not real.
func TestCustomCommandsDoNotCollideWithGenerated(t *testing.T) {
	root := buildRoot(t)
	if len(root.Commands()) == 0 {
		t.Fatal("no commands on the assembled root")
	}

	var walk func(parent *cobra.Command, path string)
	walk = func(parent *cobra.Command, path string) {
		count := map[string]int{}
		for _, cmd := range parent.Commands() {
			count[cmd.Name()]++
			walk(cmd, path+" "+cmd.Name())
		}
		for name, n := range count {
			if n > 1 {
				t.Errorf("%d commands are registered as %q: cobra resolves the name to "+
					"whichever registered first, so the other is unreachable. Either a new "+
					"openapi.yaml tag has taken a name cli/custom owns, or two operations "+
					"share an x-cli-group + x-cli-name pair.", n, strings.TrimSpace(path+" "+name))
			}
		}
	}
	walk(root, "orq")
}

func TestTracesConversationAttachesToGeneratedTracesParent(t *testing.T) {
	root := buildRoot(t)
	traces, _, err := root.Find([]string{"traces"})
	if err != nil || traces == nil {
		t.Fatalf("generated traces parent = %v, %v", traces, err)
	}
	var conversation *cobra.Command
	for _, child := range traces.Commands() {
		if child.Name() == "conversation" {
			conversation = child
			break
		}
	}
	if conversation == nil {
		t.Fatal("traces conversation command is not registered")
	}
	if got := conversation.Use; got != "conversation trace-id [span-id]" {
		t.Errorf("conversation Use = %q", got)
	}
	// `conv` is the spelling people type. `thread`, the old name, is not an
	// alias: cobra would list it in help.
	if got := conversation.Aliases; !slices.Equal(got, []string{"conv"}) {
		t.Errorf("conversation Aliases = %v", got)
	}
	// It is a hidden command of its own for the one deprecation release.
	thread, _, err := traces.Find([]string{"thread"})
	if err != nil || thread == nil || thread.Name() != "thread" {
		t.Fatalf("traces thread = %v, %v; want the deprecated command", thread, err)
	}
	if !thread.Hidden {
		t.Error("deprecated traces thread is listed in help")
	}
}

// headings returns the group headings of the root help screen, in print order.
func headings(root *cobra.Command) []string {
	var out []string
	for _, line := range strings.Split(root.UsageString(), "\n") {
		if strings.HasSuffix(line, ":") && !strings.HasPrefix(line, " ") && line != "Usage:" && line != "Flags:" && line != "Global Flags:" {
			out = append(out, line)
		}
	}
	return out
}

// assertHelpHeadings checks the root help for one heading per section, in docs
// order (unknown trailing sections allowed after the known ones), none empty
// and no "Additional Commands" block.
func assertHelpHeadings(t *testing.T, root *cobra.Command, wantTrailing ...string) {
	t.Helper()
	got := headings(root)
	seen := map[string]bool{}
	for _, h := range got {
		if seen[h] {
			t.Errorf("heading %q printed twice: %v", h, got)
		}
		seen[h] = true
		if h == "Additional Commands:" {
			t.Errorf("root help has an Additional Commands block: %v", got)
		}
	}
	var want []string
	for _, g := range helpGroups {
		if g.ID == groupFactory { // only present when the tree has file-systems; checked via populated below
			if seen[g.Title] {
				want = append(want, g.Title)
			}
			continue
		}
		want = append(want, g.Title)
	}
	for _, s := range wantTrailing {
		want = append(want, s+":")
	}
	// "Available Commands:" is cobra's heading for the ungrouped bucket and must not appear either.
	if !slices.Equal(got, want) {
		t.Errorf("headings = %v, want %v", got, want)
	}
	populated := map[string]int{}
	for _, cmd := range root.Commands() {
		if cmd.IsAvailableCommand() {
			populated[cmd.GroupID]++
		}
	}
	for _, g := range root.Groups() {
		if populated[g.ID] == 0 {
			t.Errorf("group %q renders an empty heading", g.ID)
		}
	}
}

// Today's spec: generated commands have no GroupID, our map places them.
func TestRootHelpTodaysSpecHasOneHeadingPerSectionInDocsOrder(t *testing.T) {
	assertHelpHeadings(t, buildRoot(t))
}

// sectionTitle is the title the upcoming spec will put in x-cli-help-section for a command we map by hand.
func sectionTitle(id string) string { return id }

// The upcoming spec: bartolo has already stamped GroupID == section title (and
// registered the group if it was missing) on generated commands, leaves included.
func TestRootHelpSectionedSpecHasOneHeadingPerSectionInDocsOrder(t *testing.T) {
	root := buildSectionedRoot(t, true, "")
	assertHelpHeadings(t, root)

	// Every generated top-level command kept the spec's section.
	for name, id := range commandGroup {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == nil || cmd == root || cmd.Name() != name || cmd.GroupID == "" {
			continue
		}
		if name == "version" || cmd.Annotations["custom"] == "1" {
			continue
		}
		if cmd.GroupID != sectionTitle(id) {
			t.Errorf("command %q moved to %q, spec section %q should win", name, cmd.GroupID, id)
		}
	}
}

// Same tree but sections registered out of docs order by the generator (the
// bug a reviewer demonstrated) must still print in docs order, because we
// registered first.
func TestRootHelpSectionedSpecIgnoresGeneratedRegisterOrder(t *testing.T) {
	root := buildSectionedRoot(t, true, "")
	var order []string
	for _, g := range root.Groups() {
		order = append(order, g.ID)
	}
	var docs []string
	for _, g := range helpGroups {
		docs = append(docs, g.ID)
	}
	if !slices.Equal(order, docs) {
		t.Errorf("group order = %v, want docs order %v", order, docs)
	}
}

// A section title orq-cli does not know still renders (bartolo registered it), after ours, without panicking.
func TestRootHelpUnknownSectionStillRenders(t *testing.T) {
	root := buildSectionedRoot(t, true, "Brand New Section")
	assertHelpHeadings(t, root, "Brand New Section")
	if !strings.Contains(root.UsageString(), "Brand New Section:") {
		t.Fatal("unknown section did not render")
	}
}

// Subcommands under a sectioned generated group: hand-attached children join
// the group instead of splitting into "Additional Commands".
func TestSectionedLeavesDoNotSplitIntoAdditionalCommands(t *testing.T) {
	root := buildSectionedRoot(t, true, "")
	agents, _, err := root.Find([]string{"agents"})
	if err != nil || agents == nil || agents.Name() != "agents" {
		t.Fatalf("agents = %v, %v", agents, err)
	}
	out := agents.UsageString()
	if strings.Contains(out, "Additional Commands:") {
		t.Errorf("agents help splits into Additional Commands:\n%s", out)
	}
	for _, c := range agents.Commands() {
		if c.GroupID == "" {
			t.Errorf("agents %q has no group", c.Name())
		}
	}
}

// Real generated tree with the sections the upcoming spec adds applied the way
// the generated Register does, via bartolocli.HelpSection on the group and on
// every leaf. unknownFor, when set, moves the "traces" group into that section.
func buildSectionedRoot(t *testing.T, stampLeaves bool, unknownFor string) *cobra.Command {
	t.Helper()
	bartolocli.Init(&bartolocli.Config{AppName: "orq", EnvPrefix: "ORQ", APIKeyEnvVar: "ORQ_API_KEY", SerializationFormat: "toon", Version: "test"})
	prev := bartolocli.PreRun
	bartolocli.PreRun = nil
	t.Cleanup(func() { bartolocli.PreRun = prev })
	t.Cleanup(commands.ResetUserEnvAPIKey)

	root := bartolocli.Root
	RegisterHelpGroups(root)
	generated.Register(root)
	// Stamp in reverse docs order to prove registration order does not matter.
	cmds := slices.Clone(root.Commands())
	slices.Reverse(cmds)
	for _, cmd := range cmds {
		id, ok := commandGroup[cmd.Name()]
		if !ok || id == groupGetStarted || id == groupUtilities {
			continue
		}
		if unknownFor != "" && cmd.Name() == "traces" {
			id = unknownFor
		}
		bartolocli.HelpSection(root, cmd, id)
		if stampLeaves {
			for _, leaf := range cmd.Commands() {
				bartolocli.HelpSection(cmd, leaf, id)
			}
		}
	}
	Register(root, commands.TraceAPI{})
	return root
}
