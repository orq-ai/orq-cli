package commands

import (
	"fmt"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// NewSessionsCommand lists the logins on disk. A session is not a profile —
// a profile is an API key in credentials.json, a session is an OAuth login in
// ~/.orq/sessions/<host>.json — so `auth profile list` cannot show these, and
// before this command nothing could.
//
// List-only, deliberately. `orq whoami` already reports the current session
// and `orq auth logout` already ends one; a `current` and a `clear` here would
// be a second way to say each. There is no `use` either: a session is selected
// by the host it belongs to, via --server, ORQ_SERVER or `orq server set`, and
// a second persisted selection would compete with that one.
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

// printSessionList renders the logins, with a dot on the one this invocation
// would use. The on-disk path is left to the structured output: it is the
// widest column and the least useful one to read.
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

// paintStatus colors a session state: only the two that need a human to do
// something are highlighted. "needs-refresh" is not one of them — the next call
// re-mints the token by itself.
func paintStatus(status string) string {
	switch status {
	case auth.SessionStatusInvalid, auth.SessionStatusUnreadable:
		return paint(ansiRed, status)
	default:
		return paint(ansiDim, status)
	}
}

// warnIfActiveSessionShadowed says so when the row marked active is not the
// credential in use: an explicit API key (ORQ_API_KEY or a --profile) outranks
// the session, so "active" would otherwise read as "this is what authenticates
// your calls" when nothing here does.
func warnIfActiveSessionShadowed(rows []auth.SessionListEntry) {
	if !explicitAPIKey {
		return
	}
	for _, r := range rows {
		if r.Active {
			Warn("an explicit API key (ORQ_API_KEY or a credentials profile) takes precedence, so the active login is not what authenticates API calls until the key is unset")
			return
		}
	}
}
