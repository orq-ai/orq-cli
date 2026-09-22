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

The active host comes from ` + "`--server`" + `, ` + "`ORQ_SERVER`" + `, a profile in force, or the
default persisted by ` + "`orq server set <url>`" + `. So switching login means switching
server; a host with no login yet needs one ` + "`orq auth login`" + ` under that server.`),
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
	// The table shows hosts you cannot reach from here without saying so: the
	// login is selected by server, and nothing else on screen says that.
	if len(rows) > 1 {
		fmt.Fprintln(out, paint(ansiDim, fmt.Sprintf(
			"Use another login with `%s server set https://<host>`, or one call at a time with `--server`.", binary)))
	}
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
