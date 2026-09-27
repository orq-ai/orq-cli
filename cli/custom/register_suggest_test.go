package custom

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestUnknownSubcommandSuggestsWithinAGroup(t *testing.T) {
	root := &cobra.Command{Use: "orq"}
	group := &cobra.Command{Use: "agents"}
	run := func(*cobra.Command, []string) {}
	group.AddCommand(&cobra.Command{Use: "list", Run: run}, &cobra.Command{Use: "retrieve", Run: run})
	root.AddCommand(group, &cobra.Command{Use: "version", Run: run})

	for args, want := range map[string]string{
		"agents lst":   "list",
		"agents get x": "retrieve",
	} {
		err := unknownSubcommand(root, strings.Fields(args))
		if err == nil || !strings.Contains(err.Error(), "Did you mean this?\n\t"+want) {
			t.Errorf("%s: got %v, want a suggestion of %q", args, err, want)
		}
	}
	for _, args := range []string{"agents", "agents --help", "agents list", "version", "nope"} {
		if err := unknownSubcommand(root, strings.Fields(args)); err != nil {
			t.Errorf("%s: unexpected error %v", args, err)
		}
	}
}

func TestLocalCommandsDoNotAnnounceTheWinningKey(t *testing.T) {
	root := &cobra.Command{Use: "orq"}
	auth := &cobra.Command{Use: "auth"}
	profile := &cobra.Command{Use: "profile"}
	use := &cobra.Command{Use: "use"}
	whoami := &cobra.Command{Use: "whoami"}
	agents := &cobra.Command{Use: "agents"}
	versionCmd := &cobra.Command{Use: "version"}
	profile.AddCommand(use)
	auth.AddCommand(profile, whoami)
	root.AddCommand(auth, agents, versionCmd)

	for cmd, quiet := range map[*cobra.Command]bool{use: true, versionCmd: true, whoami: false, agents: false} {
		if got := sendsNoRequest(cmd); got != quiet {
			t.Errorf("%s: sendsNoRequest = %v, want %v", cmd.CommandPath(), got, quiet)
		}
	}
}
