package commands

import (
	"errors"
	"fmt"

	"orq/cli/custom/auth"

	survey "github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"
)

// NewProjectsUseCommand builds `orq projects use`, which persists the active
// project into the session. Every later invocation mints its access token
// scoped to that project, so the server narrows both reads and creates to it.
func NewProjectsUseCommand() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "use [project]",
		Short: "Switch the active project",
		Long: "Switch the active project. The project can be given by id, key or name.\n\n" +
			"With no argument at a terminal you get a picker; otherwise the active project is printed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			if session == nil {
				return errors.New("you are not logged in")
			}
			if clear {
				if len(args) > 0 {
					return errors.New("--clear takes no argument")
				}
				return clearActiveProject(cmd, session)
			}
			client := auth.NewClient(sessionAPIBase(session)).WithContext(cmd.Context())
			active, err := client.GetActiveWorkspaceAccessToken()
			if err != nil {
				return err
			}
			session = active.Session
			// No argument and no TTY: report rather than guess.
			if len(args) == 0 && !hasInteractiveTTY() {
				return reportActiveProject(cmd, session)
			}
			projects, err := client.ListProjects(active.AccessToken)
			if err != nil {
				return err
			}
			projects = selectableProjects(projects)
			if len(projects) == 0 {
				return errors.New("this workspace has no projects")
			}
			var chosen *auth.Project
			if len(args) > 0 {
				chosen, err = auth.ResolveProject(projects, args[0])
			} else {
				chosen, err = pickProject(projects)
			}
			if err != nil {
				return err
			}
			session.ActiveProjectID = chosen.ProjectID
			session.ActiveProjectName = chosen.Name
			if err := auth.SaveSession(session); err != nil {
				return err
			}
			if wantsHumanView(cmd) {
				success("Active project: %s (%s)", chosen.Name, chosen.Key)
				warnIfShadowed()
				return nil
			}
			warnIfShadowed()
			return emit(map[string]any{
				"active_project_id":   chosen.ProjectID,
				"active_project_name": chosen.Name,
				"active_project_key":  chosen.Key,
			})
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "Unset the active project instead of switching")
	return cmd
}

// warnIfShadowed says so when an explicit API key means the switch changes
// nothing: the key carries its own scope and outranks the session.
func warnIfShadowed() {
	if explicitAPIKey {
		Warn("an explicit API key (ORQ_API_KEY or a credentials profile) is configured and takes precedence over the session, so this project switch will not affect API calls until the key is unset")
	}
}

func clearActiveProject(cmd *cobra.Command, session *auth.Session) error {
	session.ActiveProjectID = ""
	session.ActiveProjectName = ""
	if err := auth.SaveSession(session); err != nil {
		return err
	}
	if wantsHumanView(cmd) {
		success("Active project cleared")
		return nil
	}
	return emit(map[string]any{"active_project_id": nil, "active_project_name": nil})
}

func reportActiveProject(cmd *cobra.Command, session *auth.Session) error {
	if wantsHumanView(cmd) {
		if session.ActiveProjectID == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "No active project; the workspace default is used.")
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Active project: %s (%s)\n", session.ActiveProjectName, session.ActiveProjectID)
		return nil
	}
	return emit(map[string]any{
		"active_project_id":   session.ActiveProjectID,
		"active_project_name": session.ActiveProjectName,
	})
}

// selectableProjects drops archived projects: they are still returned by the
// API but cannot be worked in, so offering them only produces a dead session.
func selectableProjects(projects []auth.Project) []auth.Project {
	out := make([]auth.Project, 0, len(projects))
	for _, p := range projects {
		if !p.IsArchived {
			out = append(out, p)
		}
	}
	return out
}

func pickProject(projects []auth.Project) (*auth.Project, error) {
	if len(projects) == 1 {
		return &projects[0], nil
	}
	options := make([]string, 0, len(projects))
	for _, p := range projects {
		options = append(options, fmt.Sprintf("%s (%s)", p.Name, p.Key))
	}
	var chosen string
	if err := survey.AskOne(&survey.Select{
		Message: "Choose the project to activate",
		Options: options,
	}, &chosen, promptStdio()); err != nil {
		return nil, err
	}
	for i, opt := range options {
		if opt == chosen {
			return &projects[i], nil
		}
	}
	return nil, errors.New("no project selected")
}
