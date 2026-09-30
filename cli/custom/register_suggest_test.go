package custom

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestUnknownSubcommandSuggestsWithinAGroup(t *testing.T) {
	newRoot := func() *cobra.Command {
		root := &cobra.Command{Use: "orq", SilenceErrors: true, SilenceUsage: true}
		root.PersistentFlags().StringP("output", "o", "", "")
		group := &cobra.Command{Use: "agents"}
		run := func(*cobra.Command, []string) {}
		group.AddCommand(&cobra.Command{Use: "list", Run: run}, &cobra.Command{Use: "retrieve", Run: run})
		root.AddCommand(group, &cobra.Command{Use: "version", Run: run})
		configureSubcommandSuggestions(root)
		return root
	}
	if group, _, err := newRoot().Find([]string{"agents"}); err != nil || group.Runnable() {
		t.Fatalf("agents group became runnable: command=%v err=%v", group, err)
	}
	execute := func(args string) (string, error) {
		root := newRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		argv := strings.Fields(args)
		if err := unknownSubcommand(root, argv); err != nil {
			return out.String(), err
		}
		root.SetArgs(argv)
		err := root.Execute()
		return out.String(), err
	}

	for args, want := range map[string]string{
		"agents lst":                   "list",
		"agents get x":                 "retrieve",
		"agents -o json lst":           "list",
		"-o json agents lsit":          "list",
		"agents --output=json retrive": "retrieve",
	} {
		_, err := execute(args)
		if err == nil || !strings.Contains(err.Error(), "Did you mean this?\n\t"+want) {
			t.Errorf("%s: got %v, want a suggestion of %q", args, err, want)
		}
	}
	for _, args := range []string{"agents", "agents help", "agents --help", "agents list", "version"} {
		if out, err := execute(args); err != nil {
			t.Errorf("%s: unexpected error %v", args, err)
		} else if args != "agents list" && args != "version" && !strings.Contains(out, "Available Commands") {
			t.Errorf("%s: want the group's help, got %q", args, out)
		}
	}
}

func TestGroupHelpAndTyposSkipPersistentPreRun(t *testing.T) {
	newRoot := func() *cobra.Command {
		root := &cobra.Command{
			Use: "orq",
			PersistentPreRunE: func(*cobra.Command, []string) error {
				return errors.New("persistent pre-run must not run")
			},
		}
		group := &cobra.Command{Use: "agents"}
		group.AddCommand(&cobra.Command{Use: "list", Run: func(*cobra.Command, []string) {}})
		root.AddCommand(group)
		configureSubcommandSuggestions(root)
		return root
	}

	for _, args := range []string{"agents", "agents help", "agents lst"} {
		root := newRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		argv := strings.Fields(args)
		if err := unknownSubcommand(root, argv); err != nil {
			if args == "agents lst" && strings.Contains(err.Error(), "Did you mean this?\n\tlist") {
				continue
			}
			t.Errorf("%s: preflight: %v", args, err)
			continue
		}
		root.SetArgs(argv)
		err := root.Execute()
		if strings.Contains(out.String(), "persistent pre-run") || (err != nil && strings.Contains(err.Error(), "persistent pre-run")) {
			t.Errorf("%s ran PersistentPreRunE before help/argument validation", args)
		}
		if args == "agents lst" {
			if err == nil || !strings.Contains(err.Error(), "Did you mean this?\n\tlist") {
				t.Errorf("%s: got %v, want list suggestion", args, err)
			}
		} else if err != nil {
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
		if got := credentialIrrelevant(cmd); got != quiet {
			t.Errorf("%s: credentialIrrelevant = %v, want %v", cmd.CommandPath(), got, quiet)
		}
	}
}

func TestCredentialIrrelevantInvocationModes(t *testing.T) {
	root := buildRoot(t)
	connect := findCommand(t, root, "connect")
	if err := connect.Flags().Set("status", "true"); err != nil {
		t.Fatal(err)
	}
	if !credentialIrrelevant(connect) {
		t.Error("connect --status should not announce which credential would win")
	}
	if disconnect := findCommand(t, root, "disconnect"); !credentialIrrelevant(disconnect) {
		t.Error("disconnect should not announce which credential would win")
	}
}

// A renamed command would silently drop out of the quiet list and start
// printing the credential note again; pin every entry to the real tree.
func TestQuietCredentialCommandsExist(t *testing.T) {
	root := buildRoot(t)
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	for _, path := range quietCredentialCommands {
		if strings.HasPrefix(path, "__complete") {
			continue // cobra adds these only while executing
		}
		cmd, rest, err := root.Find(strings.Fields(path))
		if err != nil || len(rest) > 0 || cmd == root {
			t.Errorf("quiet command %q is not in the tree", path)
		}
	}
}
