package commands

import (
	"bytes"
	"errors"
	"github.com/spf13/viper"
	"orq/cli/custom/auth"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

func TestHumanizedWriteSaysOneSentenceOnATerminalOnly(t *testing.T) {
	origHuman, origOut := humanOutput, bartolocli.Stdout
	t.Cleanup(func() { humanOutput, bartolocli.Stdout = origHuman, origOut })

	newCmd := func() *cobra.Command {
		return &cobra.Command{Use: "clear", RunE: func(*cobra.Command, []string) error {
			_, _ = bartolocli.Stdout.Write([]byte("persisted: true\n"))
			return nil
		}}
	}
	run := func(human bool) string {
		humanOutput = func() bool { return human }
		out := &bytes.Buffer{}
		bartolocli.Stdout = out
		cmd := newCmd()
		wrapWrite(cmd, func() { _, _ = bartolocli.Stdout.Write([]byte("Done.\n")) })
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	if got := run(true); got != "Done.\n" {
		t.Errorf("terminal output = %q, want the sentence only", got)
	}
	if got := run(false); !strings.Contains(got, "persisted: true") {
		t.Errorf("piped output = %q, want bartolo's record untouched", got)
	}
}

func TestHumanizedWriteFailureKeepsTheErrorAndSaysNothing(t *testing.T) {
	origHuman, origOut := humanOutput, bartolocli.Stdout
	t.Cleanup(func() { humanOutput, bartolocli.Stdout = origHuman, origOut })
	humanOutput = func() bool { return true }
	out := &bytes.Buffer{}
	bartolocli.Stdout = out

	cmd := &cobra.Command{Use: "set", RunE: func(*cobra.Command, []string) error { return errors.New("boom") }}
	wrapWrite(cmd, func() { _, _ = out.WriteString("Done.\n") })
	if err := cmd.RunE(cmd, nil); err == nil || out.Len() != 0 {
		t.Errorf("err = %v, output = %q; want the error and no sentence", err, out.String())
	}
	if bartolocli.Stdout != out {
		t.Error("Stdout was not restored after the failed write")
	}
}

func TestServerViews(t *testing.T) {
	origOut := bartolocli.Stdout
	t.Cleanup(func() { bartolocli.Stdout = origOut; auth.SetServer("", "default"); viper.Set("server", "") })
	t.Setenv("NO_COLOR", "1")

	out := &bytes.Buffer{}
	bartolocli.Stdout = out
	auth.SetServer("https://self.example.com", "env")
	viper.Set("server", "https://self.example.com")

	printServerCurrent(out)
	for _, want := range []string{"https://self.example.com", "ORQ_SERVER"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("current = %q, missing %q", out.String(), want)
		}
	}

	out.Reset()
	printServerList(out)
	if !strings.Contains(out.String(), "is not in this list (ORQ_SERVER)") {
		t.Errorf("list = %q, want the off-list note naming the source", out.String())
	}

	if got := overrideNote(); !strings.Contains(got, "ORQ_SERVER still decides") {
		t.Errorf("overrideNote = %q", got)
	}
	auth.SetServer("https://x.example.com", "config")
	if got := overrideNote(); got != "" {
		t.Errorf("overrideNote with only a saved default = %q, want none", got)
	}
}

func TestHumanizeTargetsExist(t *testing.T) {
	root := &cobra.Command{Use: "orq"}
	srv := &cobra.Command{Use: "server"}
	for _, n := range []string{"list", "current", "set", "clear"} {
		srv.AddCommand(&cobra.Command{Use: n, RunE: func(*cobra.Command, []string) error { return nil }})
	}
	authCmd := &cobra.Command{Use: "auth"}
	profile := &cobra.Command{Use: "profile"}
	profile.AddCommand(&cobra.Command{Use: "clear", RunE: func(*cobra.Command, []string) error { return nil }})
	authCmd.AddCommand(profile)
	root.AddCommand(srv, authCmd)

	HumanizeServerAndProfile(root)
	for _, path := range [][]string{{"server", "list"}, {"server", "current"}, {"server", "set"}, {"server", "clear"}, {"auth", "profile", "clear"}} {
		if subcommand(root, path...) == nil {
			t.Errorf("%v not found", path)
		}
	}
}
