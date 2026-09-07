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
			if len(sessions) == 0 {
				// To stderr, so `orq auth sessions --json | jq` still gets a
				// well-formed empty list on stdout.
				fmt.Fprintf(bartolocli.Stderr, "No logins. Use `%s auth login` to create one.\n", cmd.Root().Name())
				return emit(map[string]any{"sessions": []any{}})
			}
			return emit(map[string]any{"sessions": sessions})
		},
	}
}
