package commands

import (
	"fmt"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// NewSessionsCommand lists OAuth logins stored per server host.
func NewSessionsCommand() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:     "sessions",
		Aliases: []string{"session"},
		Short:   "List saved logins, one per host",
		Long: bartolocli.Markdown(`Lists the OAuth logins on this machine. There is one per server host, and
the host decides which login authenticates a call — not the workspace, which is
selected inside a login by ` + "`orq switch`" + `.

So switching login means switching server, and ` + "`orq doctor -o json`" + ` reports
which host won and why. A host with no login yet needs one ` + "`orq auth login`" + `
under it.

The status column reflects saved state: a login whose token has
expired shows ` + "`needs-refresh`" + `, which the next call re-mints. Pass
` + "`--check`" + ` to ask each expired login's server whether it still works — a
rejected one then shows ` + "`signed-out`" + ` and is recorded as such.

Statuses: ` + "`ok`" + ` means locally unexpired or successfully refreshed;
` + "`needs-refresh`" + ` means expired, including an inconclusive probe;
` + "`signed-out`" + ` means a recorded refresh rejection; ` + "`invalid`" + ` means
malformed session data; ` + "`unreadable`" + ` means the file could not be read.
An inconclusive probe does not prove that a login is valid or invalid.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessions, err := auth.ListSessions()
			if err != nil {
				return err
			}
			if check {
				// One refresh per expired login: only needs-refresh rows are
				// uncertain (ok is locally unexpired; signed-out/invalid/unreadable are already
				// settled), so this is the minimum network to confirm them.
				for i := range sessions {
					if sessions[i].Status == auth.SessionStatusNeedsRefresh {
						sessions[i].Status = auth.CheckSession(sessions[i].Host)
					}
				}
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
	cmd.Flags().BoolVar(&check, "check", false, "Probe each expired login against its server and report whether it still works")
	return cmd
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
			r.Host, r.User, r.Workspace, r.Project, paintStatus(string(r.Status)),
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
	switch auth.SessionStatus(status) {
	case auth.SessionStatusInvalid, auth.SessionStatusUnreadable, auth.SessionStatusSignedOut:
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
			if activeStoredAPIKeyLogin() != nil {
				Warn("your API-key login takes precedence over the browser session until you log in through the browser again")
			} else {
				Warn("an explicit API key (ORQ_API_KEY or a credentials profile) takes precedence, so the active login is not what authenticates API calls until the key is unset")
			}
			return
		}
	}
}

func usableSessionStatus(status auth.SessionStatus) bool {
	return status == auth.SessionStatusOK || status == auth.SessionStatusNeedsRefresh
}
