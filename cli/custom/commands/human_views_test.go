package commands

import (
	"bytes"
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
