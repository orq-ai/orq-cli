package custom

import (
	"os"
	"testing"

	"github.com/spf13/cobra"
)

// fakeTracesSearch mirrors the shape applyDefaultTimeWindow patches: a
// `traces search` with the generated --from/--to body flags.
func fakeTracesSearch(t *testing.T) (root *cobra.Command, got func() (string, string)) {
	t.Helper()
	var from, to string
	search := &cobra.Command{
		Use: "search",
		RunE: func(cmd *cobra.Command, args []string) error {
			from, _ = cmd.Flags().GetString("from")
			to, _ = cmd.Flags().GetString("to")
			return nil
		},
	}
	search.Flags().String("from", "", "")
	search.Flags().String("to", "", "")
	search.Flags().String("from-file", "", "")
	traces := &cobra.Command{Use: "traces"}
	traces.AddCommand(search)
	root = &cobra.Command{Use: "orq"}
	root.AddCommand(traces)
	applyDefaultTimeWindow(root)
	return root, func() (string, string) { return from, to }
}

func TestTracesSearchDefaultsToLastSevenDays(t *testing.T) {
	root, got := fakeTracesSearch(t)
	root.SetArgs([]string{"traces", "search"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if from, to := got(); from != "7d" || to != "now" {
		t.Errorf("window = %q..%q, want 7d..now", from, to)
	}
}

func TestTracesSearchKeepsAnExplicitWindow(t *testing.T) {
	root, got := fakeTracesSearch(t)
	root.SetArgs([]string{"traces", "search", "--from", "24h"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Only the end the user left open is filled in.
	if from, to := got(); from != "24h" || to != "now" {
		t.Errorf("window = %q..%q, want 24h..now", from, to)
	}
}

// A body read from a file is machine-written and sent as given, so neither end
// may be rewritten from under it.
func TestTracesSearchLeavesAFileBodyAlone(t *testing.T) {
	root, got := fakeTracesSearch(t)
	root.SetArgs([]string{"traces", "search", "--from-file", "body.json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if from, to := got(); from != "" || to != "" {
		t.Errorf("window = %q..%q, want both empty", from, to)
	}
}

// The paths are hard-coded, so a generated rename would silently drop the
// default. This is the check that fails when that happens.
func TestEveryTimeWindowedCommandExists(t *testing.T) {
	root := buildRoot(t)
	for _, path := range timeWindowedCommands {
		cmd := root
		for _, name := range path {
			cmd = childCommand(cmd, name)
			if cmd == nil {
				t.Fatalf("%v: no such command", path)
			}
		}
		for _, flag := range []string{"from", "to"} {
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("%v: no --%s flag", path, flag)
			}
		}
	}
}

// TestBodyFromFlagsOnlyReadsStdin covers the branch the default window turns
// on. A pipe on stdin may still deliver a body, so bartolo is left to wait for
// it; a terminal never will.
func TestBodyFromFlagsOnlyReadsStdin(t *testing.T) {
	root, _ := fakeTracesSearch(t)
	search := childCommand(childCommand(root, "traces"), "search")

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	terminal, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer terminal.Close()

	for _, tc := range []struct {
		name  string
		stdin *os.File
		want  bool
	}{
		{name: "a pipe may carry a body", stdin: reader, want: false},
		{name: "a terminal never does", stdin: terminal, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := os.Stdin
			os.Stdin = tc.stdin
			t.Cleanup(func() { os.Stdin = previous })
			if got := bodyFromFlagsOnly(search); got != tc.want {
				t.Fatalf("bodyFromFlagsOnly() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBodyFromFlagsOnlyDefersOnAnUnreadableStdin keeps the unknown case on the
// side that cannot overwrite a body the user supplied.
func TestBodyFromFlagsOnlyDefersOnAnUnreadableStdin(t *testing.T) {
	root, _ := fakeTracesSearch(t)
	search := childCommand(childCommand(root, "traces"), "search")

	closed, _, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	previous := os.Stdin
	os.Stdin = closed
	t.Cleanup(func() { os.Stdin = previous })

	if bodyFromFlagsOnly(search) {
		t.Fatal("bodyFromFlagsOnly() = true on a stdin it could not stat, want false")
	}
}
