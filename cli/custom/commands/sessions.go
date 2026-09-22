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

The active host comes from ` + "`--server`" + `, then a profile in force, then ` + "`ORQ_SERVER`" + `,
then the default persisted by ` + "`orq server set <url>`" + `. So switching login means
switching server; a host with no login yet needs one ` + "`orq auth login`" + ` under it.`),
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
				printSessionList(sessions, cmd)
				warnIfActiveSessionShadowed(sessions)
				return nil
			}
			warnIfActiveSessionShadowed(sessions)
			return emit(map[string]any{"sessions": sessions})
		},
	}
}

// printSessionList renders the logins and marks the resolved host.
func printSessionList(rows []auth.SessionListEntry, cmd *cobra.Command) {
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
		fmt.Fprintln(out, paint(ansiDim, fmt.Sprintf(
			"Use another login with `%s server set %s`, or one call at a time with `--server`.",
			cmd.Root().Name(), target)))
	}
}

// switchTarget is the server URL the hint names: a login on another host that
// is usable as it stands. It is the row's stored server rather than its HOST
// cell, which is a file name — a port lands there as `_8080`, which `server
// set` would reject.
func switchTarget(rows []auth.SessionListEntry) string {
	for _, r := range rows {
		if !r.Active && r.Server != "" && usableSessionStatus(r.Status) {
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
