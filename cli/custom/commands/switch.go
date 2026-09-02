package commands

import (
	"errors"
	"fmt"

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
			switch {
			case len(args) > 0:
				workspaceKey = args[0]
			case hasInteractiveTTY():
				workspaceKey, err = selectWorkspace(session.Workspaces, "Choose the workspace")
				if err != nil {
					return err
				}
			default:
				// `switch` rewrites BOTH halves of the identity, so a bare run
				// with nobody to ask is a question, not an instruction:
				// re-asserting the active workspace looked harmless but ran the
				// project half too, replacing a deliberately chosen project
				// with the workspace default and reporting success. `workspace
				// use` answers the same situation differently because it only
				// ever writes the one thing it was named for.
				return errors.New("no workspace given and no terminal to ask on: run `orq switch <workspace> [project]`")
			}
			if workspaceKey == "" {
				return errors.New("no workspace is available for this user")
			}
			// Everything that can fail happens before the first write. Calling
			// UseWorkspace first persisted the new workspace immediately, so a
			// failure in the token exchange or the project list left the new
			// workspace on disk beside the old workspace's project — a session
			// every later command would try to narrow to a project that is not
			// in the active workspace.
			if !workspaceAvailable(session, workspaceKey) {
				// Checked here rather than left to UseWorkspace (which makes the
				// same check) so an unreachable key still reports itself as one,
				// instead of as an opaque token-exchange failure.
				return fmt.Errorf("workspace %q is not available to this user", workspaceKey)
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
				// A workspace with no project named is half an instruction, and
				// switch writes both halves: falling back to the workspace
				// default replaced a deliberately chosen project and reported
				// success. Anyone who only means to move workspace has
				// `orq workspace use`, which leaves the project alone.
				err = fmt.Errorf("no project given and no terminal to ask on: run `orq switch %s <project>`, or `orq workspace use %s` to move workspace only", workspaceKey, workspaceKey)
			}
			if err != nil {
				return err
			}

			// Both halves are decided; commit them together. UseWorkspace
			// writes the workspace, and the save below writes the project it
			// belongs to, with nothing fallible in between.
			session, err = client.UseWorkspace(workspaceKey)
			if err != nil {
				return err
			}
			// Assigned unconditionally, even though UseWorkspace already clears
			// the project when the workspace moved: switching WITHIN the active
			// workspace to a workspace that has no projects must still end with
			// no active project, and that case is not a workspace change.
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

// workspaceAvailable reports whether the profile this session last fetched
// lists the given workspace key.
func workspaceAvailable(session *auth.Session, key string) bool {
	for _, w := range session.Workspaces {
		if k, ok := w["key"].(string); ok && k == key {
			return true
		}
	}
	return false
}
