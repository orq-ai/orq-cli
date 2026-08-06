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
	var apiBase string
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
			client := auth.NewClient(sessionAPIBase(apiBase, session))
			session, err = client.WhoAmI()
			if err != nil {
				return err
			}
			activeKey := ""
			if session.ActiveWorkspaceKey != nil {
				activeKey = *session.ActiveWorkspaceKey
			}
			rows := make([]workspaceRow, 0, len(session.Workspaces))
			nameWidth := 0
			for _, w := range session.Workspaces {
				ws := workspaceFromMap(w)
				rows = append(rows, workspaceRow{
					ID:           ws.ID,
					Key:          ws.Key,
					Name:         ws.Name,
					TotalMembers: ws.TotalMembers,
					Active:       ws.Key == activeKey,
				})
				if len(ws.Name) > nameWidth {
					nameWidth = len(ws.Name)
				}
			}
			if wantsHumanView(cmd) {
				printWorkspaceList(rows, nameWidth)
				return nil
			}
			return emit(map[string]any{
				"active_workspace_key": session.ActiveWorkspaceKey,
				"workspaces":           rows,
			})
		},
	}
	cmd.Flags().StringVar(&apiBase, "api-base-url", "", "Override API base URL")
	return cmd
}

func newWorkspaceUseCommand() *cobra.Command {
	var apiBase string
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
			client := auth.NewClient(sessionAPIBase(apiBase, session))
			session, err = client.WhoAmI()
			if err != nil {
				return err
			}
			workspaceKey := ""
			if len(args) > 0 {
				workspaceKey = args[0]
			} else if session.ActiveWorkspaceKey != nil {
				workspaceKey = *session.ActiveWorkspaceKey
			}
			if workspaceKey == "" && hasInteractiveTTY() {
				workspaceKey, err = selectWorkspace(session.Workspaces, "Choose the workspace to activate")
				if err != nil {
					return err
				}
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
			shadowed := envAPIKeyConfigured()
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
	cmd.Flags().StringVar(&apiBase, "api-base-url", "", "Override API base URL")
	return cmd
}

// printWorkspaceList renders the workspace roster with a colored marker on the
// active one and an aligned member count.
func printWorkspaceList(rows []workspaceRow, nameWidth int) {
	heading("Workspaces")
	for _, r := range rows {
		marker := "  "
		if r.Active {
			marker = paint(ansiGreen, "* ")
		}
		label := fmt.Sprintf("%s (%s)", pad(r.Name, nameWidth), r.Key)
		fmt.Fprintf(bartolocli.Stdout, "  %s%s  %s\n", marker, label, paint(ansiDim, fmt.Sprintf("%d members", r.TotalMembers)))
	}
}
