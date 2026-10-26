package commands

import (
	"fmt"
	"io"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// HumanizeServerAndProfile replaces bartolo's machine-shaped records for
// `orq server list|current|set|clear` and `orq auth profile clear` with a
// readable view when a person is at a terminal. A pipe or an explicit -o keeps
// bartolo's output: that is the script contract.
func HumanizeServerAndProfile(root *cobra.Command) {
	if server := subcommand(root, "server"); server != nil {
		wrapView(subcommand(server, "list"), printServerList)
		wrapView(subcommand(server, "current"), printServerCurrent)
		wrapWrite(subcommand(server, "set"), func() {
			fmt.Fprintf(bartolocli.Stdout, "Now talking to %s.\n", bartolocli.ResolveServer())
		})
		wrapWrite(subcommand(server, "clear"), func() {
			fmt.Fprintf(bartolocli.Stdout, "Cleared the server override. Now talking to %s.\n", bartolocli.ResolveServer())
		})
	}
	wrapWrite(subcommand(subcommand(root, "auth"), "profile", "clear"), func() {
		if name := bartolocli.ActiveProfileName(); name != "" {
			fmt.Fprintf(bartolocli.Stdout, "Cleared the saved profile, but %q is still in force for this call (--profile or ORQ_PROFILE).\n", name)
			return
		}
		fmt.Fprintln(bartolocli.Stdout, "No profile active. Using your login session.")
	})
}

// subcommand walks names down from parent, returning nil at the first miss.
func subcommand(parent *cobra.Command, names ...string) *cobra.Command {
	for _, name := range names {
		if parent == nil {
			return nil
		}
		var next *cobra.Command
		for _, c := range parent.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		parent = next
	}
	return parent
}

// wrapView swaps a read command's output for render on a human terminal.
func wrapView(cmd *cobra.Command, render func(io.Writer)) {
	if cmd == nil || cmd.RunE == nil {
		return
	}
	orig := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if !wantsHumanView(c) {
			return orig(c, args)
		}
		render(bartolocli.Stdout)
		return nil
	}
}

// wrapWrite lets a write command run, swallows bartolo's record of it, and
// prints one sentence instead — only when the write succeeded.
func wrapWrite(cmd *cobra.Command, say func()) {
	if cmd == nil || cmd.RunE == nil {
		return
	}
	orig := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if !wantsHumanView(c) {
			return orig(c, args)
		}
		restore := bartolocli.Stdout
		bartolocli.Stdout = io.Discard
		err := orig(c, args)
		bartolocli.Stdout = restore
		if err != nil {
			return err
		}
		say()
		return nil
	}
}

func printServerList(w io.Writer) {
	current := bartolocli.ResolveServer()
	var rows []tableRow
	listed := false
	for i, s := range bartolocli.GetServers() {
		marker := ""
		if s["url"] == current {
			marker, listed = paint(ansiOK, "✓"), true
		}
		rows = append(rows, tableRow{marker: marker, cells: []string{fmt.Sprint(i), s["url"], s["description"]}})
	}
	printTable(w, []string{"#", "URL", "DESCRIPTION"}, rows)
	if !listed {
		fmt.Fprintf(w, "\nCurrent server %s is not in this list (%s).\n", current, serverSource())
	}
}

func printServerCurrent(io.Writer) {
	heading("Server")
	kv(7, "url", "%s", bartolocli.ResolveServer())
	kv(7, "source", "%s", serverSource())
}

// serverSource says in words which layer chose the host. The root PreRun
// records it in auth (custom.resolveServer ranks the layers).
func serverSource() string {
	switch auth.ServerSource() {
	case "flag":
		return "--server flag"
	case "profile":
		return "active profile"
	case "env":
		return "ORQ_SERVER"
	case "config":
		return "saved with `orq server set`"
	case "session":
		return "your login session"
	default:
		return "built-in default"
	}
}
