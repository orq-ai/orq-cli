package commands

import (
	"errors"

	"orq/cli/custom/auth"

	"github.com/spf13/cobra"
)

// NewSwitchCommand walks the whole identity in one go: workspace, then the
// projects inside it. `workspace use` and `projects use` each do half, and
// doing the half you did not mean leaves the CLI pointing somewhere real but
// wrong — which is the state this exists to make hard to reach.
//
// Two stages on purpose. The project list needs a token for the workspace it
// belongs to, so filling one picker per workspace up front would mint a token
// for every workspace the user can see.
func NewSwitchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "switch [workspace] [project]",
		Short: "Switch the active workspace and project",
		Args:  cobra.MaximumNArgs(2),
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
			if len(args) > 0 {
				workspaceKey = args[0]
			} else if hasInteractiveTTY() {
				workspaceKey, err = selectWorkspace(session.Workspaces, "Choose the workspace")
				if err != nil {
					return err
				}
			} else if session.ActiveWorkspaceKey != nil {
				workspaceKey = *session.ActiveWorkspaceKey
			}
			if workspaceKey == "" {
				return errors.New("no workspace is available for this user")
			}
			session, err = client.UseWorkspace(workspaceKey)
			if err != nil {
				return err
			}

			bearer, err := client.WorkspaceToken(session, workspaceKey)
			if err != nil {
				return err
			}
			projects, err := client.ListProjects(bearer)
			if err != nil {
				return err
			}
			projects = selectableProjects(projects)

			var chosen *auth.Project
			switch {
			case len(projects) == 0:
				// A workspace with no projects is a complete answer, not a failure.
			case len(args) > 1:
				chosen, err = auth.ResolveProject(projects, args[1])
			case len(projects) == 1:
				chosen = &projects[0]
			case hasInteractiveTTY():
				chosen, err = pickProject(projects)
			default:
				chosen = auth.DefaultProject(projects)
			}
			if err != nil {
				return err
			}

			session.ActiveProjectID = ""
			session.ActiveProjectName = ""
			if chosen != nil {
				session.ActiveProjectID = chosen.ProjectID
				session.ActiveProjectName = chosen.Name
			}
			if err := auth.SaveSession(session); err != nil {
				return err
			}

			out := map[string]any{"active_workspace_key": workspaceKey}
			if chosen != nil {
				out["active_project_id"] = chosen.ProjectID
				out["active_project_name"] = chosen.Name
				out["active_project_key"] = chosen.Key
			}
			if wantsHumanView(cmd) {
				if chosen != nil {
					success("Active: %s / %s", workspaceKey, chosen.Name)
				} else {
					success("Active workspace: %s (no projects)", workspaceKey)
				}
				warnIfShadowed()
				return nil
			}
			warnIfShadowed()
			return emit(out)
		},
	}
	return cmd
}
