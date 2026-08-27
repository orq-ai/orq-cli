package commands

import (
	"errors"
	"fmt"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

type workspaceRow struct {
	ID           string `json:"id"`
	Key          string `json:"key"`
	Name         string `json:"name"`
	TotalMembers int    `json:"total_members"`
	Active       bool   `json:"active"`
}

func NewWorkspaceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage CLI workspace context",
	}
	cmd.AddCommand(newWorkspaceListCommand())
	cmd.AddCommand(newWorkspaceUseCommand())
	return cmd
}

func newWorkspaceListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			if session == nil {
				return errors.New("you are not logged in")
			}
			client := auth.NewClient(sessionAPIBase(session)).WithContext(cmd.Context())
			session, err = client.WhoAmI()
			if err != nil {
				return err
			}
			activeKey := ""
			if session.ActiveWorkspaceKey != nil {
				activeKey = *session.ActiveWorkspaceKey
			}
			rows := make([]workspaceRow, 0, len(session.Workspaces))
			for _, w := range session.Workspaces {
				ws := workspaceFromMap(w)
				rows = append(rows, workspaceRow{
					ID:           ws.ID,
					Key:          ws.Key,
					Name:         ws.Name,
					TotalMembers: ws.TotalMembers,
					Active:       ws.Key == activeKey,
				})
			}
			if wantsHumanView(cmd) {
				printWorkspaceList(rows)
				return nil
			}
			return emit(map[string]any{
				"active_workspace_key": session.ActiveWorkspaceKey,
				"workspaces":           rows,
			})
		},
	}
	DeprecatedAPIBaseFlag(cmd)
	return cmd
}

func newWorkspaceUseCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use [key]",
		Short: "Switch the active workspace",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			if session == nil {
				return errors.New("you are not logged in")
			}
			client := auth.NewClient(sessionAPIBase(session)).WithContext(cmd.Context())
			session, err = client.WhoAmI()
			if err != nil {
				return err
			}
			workspaceKey := ""
			switch {
			case len(args) > 0:
				workspaceKey = args[0]
			case hasInteractiveTTY():
				// No argument at a terminal means "let me choose". Do NOT
				// pre-fill the active workspace here: that made the picker
				// unreachable for anyone who already had one active, so `use`
				// with no argument silently re-selected the current workspace
				// instead of switching.
				workspaceKey, err = selectWorkspace(session.Workspaces, "Choose the workspace to activate")
				if err != nil {
					return err
				}
			case session.ActiveWorkspaceKey != nil:
				// Non-interactive with no argument: re-assert the active
				// workspace rather than fail, so scripts stay deterministic.
				workspaceKey = *session.ActiveWorkspaceKey
			}
			if workspaceKey == "" {
				return errors.New("no workspace is available for this user")
			}
			session, err = client.UseWorkspace(workspaceKey)
			if err != nil {
				return err
			}
			report := BuildIdentityReport(session, &client.URLs)
			activeName := workspaceKey
			for _, w := range report.Workspaces {
				if w.Key == workspaceKey {
					activeName = w.Name
					break
				}
			}
			// Snapshotted before PreRun's session-token injection; reading the
			// env here would always see our own injected key and cry wolf.
			shadowed := explicitAPIKey
			if wantsHumanView(cmd) {
				success("Active workspace: %s (%s)", activeName, workspaceKey)
				if shadowed {
					Warn("an explicit API key takes precedence, so this switch will not affect API calls until it is unset")
				}
				return nil
			}
			if shadowed {
				Warn("an explicit API key (ORQ_API_KEY or a credentials profile) is configured and takes precedence over the session, so this workspace switch will not affect API calls until the key is unset")
			}
			return emit(report)
		},
	}
	DeprecatedAPIBaseFlag(cmd)
	return cmd
}

// printWorkspaceList renders the workspace roster through the shared table: a
// dim header row, a dot on the active workspace, and right-aligned member
// counts.
func printWorkspaceList(rows []workspaceRow) {
	const memHdr = "MEMBERS"
	out := bartolocli.Stdout
	heading("Workspaces")
	anyActive := false
	table := make([]tableRow, 0, len(rows))
	for _, r := range rows {
		marker := ""
		if r.Active {
			marker = paint(ansiOK, "●")
			anyActive = true
		}
		// MEMBERS is the last column, so it is never padded and may carry its
		// own color; the count is right-aligned under its header.
		members := paint(ansiDim, fmt.Sprintf("%*d", len(memHdr), r.TotalMembers))
		table = append(table, tableRow{marker: marker, cells: []string{r.Name, r.Key, members}})
	}
	printTable(out, []string{"NAME", "KEY", memHdr}, table)
	if anyActive {
		fmt.Fprintln(out, paint(ansiDim, "\n● active"))
	}
}
