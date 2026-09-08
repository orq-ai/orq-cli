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
		Args:    cobra.NoArgs,
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
				printSessionList(sessions)
				warnIfActiveSessionShadowed(sessions)
				return nil
			}
			warnIfActiveSessionShadowed(sessions)
			return emit(map[string]any{"sessions": sessions})
		},
	}
}

// printSessionList renders the logins and marks the resolved host.
func printSessionList(rows []auth.SessionListEntry) {
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
