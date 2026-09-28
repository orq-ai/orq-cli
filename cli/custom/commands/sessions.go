package commands

import (
	"fmt"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// NewSessionsCommand lists OAuth logins stored per server host.
func NewSessionsCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "sessions",
		Aliases: []string{"session"},
		Short:   "List saved logins, one per host",
		Long: bartolocli.Markdown(`Lists the OAuth logins on this machine. There is one per server host, and
the host decides which login authenticates a call — not the workspace, which is
selected inside a login by ` + "`orq switch`" + `.

So switching login means switching server, and ` + "`orq doctor -o json`" + ` reports
which host won and why. A host with no login yet needs one ` + "`orq auth login`" + `
under it.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessions, err := auth.ListSessions()
			if err != nil {
				return err
			}
			if wantsHumanView(cmd) {
				if len(sessions) == 0 {
					Notice("No logins. Use `%s auth login` to create one.", cmd.Root().Name())
					return nil
				}
				printSessionList(sessions, cmd.Root().Name())
				warnIfActiveSessionShadowed(sessions)
				return nil
			}
			warnIfActiveSessionShadowed(sessions)
			return emit(map[string]any{"sessions": sessions})
		},
	}
}

// printSessionList renders the logins and marks the resolved host.
func printSessionList(rows []auth.SessionListEntry, binary string) {
	out := bartolocli.Stdout
	heading("Logins")
	anyActive := false
	table := make([]tableRow, 0, len(rows))
	for _, r := range rows {
		marker := ""
		if r.Active {
			marker = paint(ansiOK, "●")
			anyActive = true
		}
		table = append(table, tableRow{marker: marker, cells: []string{
			r.Host, r.User, r.Workspace, r.Project, paintStatus(r.Status),
		}})
	}
	printTable(out, []string{"HOST", "USER", "WORKSPACE", "PROJECT", "STATE"}, table)
	if anyActive {
		fmt.Fprintln(out, paint(ansiDim, "\n● active"))
	}
	// Nothing else on screen says the server is what selects a login.
	if target := switchTarget(rows); target != "" {
		fmt.Fprintln(out, paint(ansiDim, switchHint(binary, target, auth.ServerSource())))
	}
}

// switchHint says how to reach target, which depends on what put this run on
// the current host: `server set` writes the persisted default, and every
// source above it in resolveServer's order goes on outranking what it wrote.
// Naming it unconditionally sends anyone on ORQ_SERVER or a profile to a
// command that changes nothing.
func switchHint(binary, target, source string) string {
	perCall := fmt.Sprintf("`--server %s` uses it for one call.", target)
	switch source {
	case "env":
		return fmt.Sprintf("Another login lives at %s, but ORQ_SERVER picks the host — unset it to leave this one. %s", target, perCall)
	case "profile":
		return fmt.Sprintf("Another login lives at %s, but the profile in force binds this host — leave that profile to reach it. %s", target, perCall)
	case "flag":
		return fmt.Sprintf("Another login lives at %s. %s", target, perCall)
	default:
		return fmt.Sprintf("Use another login with `%s server set %s`, or one call at a time: %s", binary, target, perCall)
	}
}

// switchTarget is the server URL the hint names: a login on another host that
// is usable as it stands. It is the row's stored server rather than its HOST
// cell, which is a file name — a ported host lands there as
// `self.example.com_8080`, which `server set` does not reject, it persists as
// an `https://` host that resolves to nothing. ListSessions only fills Server
// once validateSession has passed, so a usable row always has one.
func switchTarget(rows []auth.SessionListEntry) string {
	for _, r := range rows {
		if !r.Active && usableSessionStatus(r.Status) {
			return r.Server
		}
	}
	return ""
}

// paintStatus highlights session states that require user action.
func paintStatus(status string) string {
	switch status {
	case auth.SessionStatusInvalid, auth.SessionStatusUnreadable:
		return paint(ansiRed, status)
	default:
		return paint(ansiDim, status)
	}
}

// warnIfActiveSessionShadowed explains when an API key outranks the active login.
func warnIfActiveSessionShadowed(rows []auth.SessionListEntry) {
	if !explicitAPIKey {
		return
	}
	for _, r := range rows {
		if r.Active && usableSessionStatus(r.Status) {
			Warn("an explicit API key (ORQ_API_KEY or a credentials profile) takes precedence, so the active login is not what authenticates API calls until the key is unset")
			return
		}
	}
}

func usableSessionStatus(status string) bool {
	return status == auth.SessionStatusOK || status == auth.SessionStatusNeedsRefresh
}
