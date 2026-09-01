package commands

import (
	"fmt"

	"orq/cli/custom/auth"
)

// resolveProjectStep picks the project this machine works in and persists it on
// the session, so every later command mints its access token scoped to that
// project. Returns nil when the step was skipped, which is never fatal: a
// project-less session falls back to the workspace default.
//
// Runs before the key step on purpose. The selected project also scopes the
// key setup mints for the coding agents, and by the time that key exists it has
// already replaced the session token as the bearer.
func resolveProjectStep(rep *reporter, client *auth.Client, state *authState, opts *setupOptions) (*auth.Project, error) {
	if opts.noProject {
		rep.ok("skipped choosing a project")
		return nil, nil
	}
	// An --api-key / ORQ_API_KEY run has no session to persist into, and the
	// key carries its own scope anyway.
	if state.session == nil {
		rep.note("no session to record a project on — run `orq projects use <project>` after logging in")
		return nil, nil
	}

	projects, err := client.ListProjects(state.bearer)
	if err != nil {
		// A workspace we cannot list is not a reason to abandon a setup that
		// otherwise works; the session simply stays on the workspace default.
		rep.warn("could not list projects (%v) — continuing without one", err)
		return nil, nil
	}
	projects = selectableProjects(projects)

	var chosen *auth.Project
	switch {
	case len(projects) == 0:
		// Free-tier workspaces cannot create projects, so offering to make one
		// here would dead-end the wizard.
		rep.ok("no projects in this workspace")
		return nil, nil
	case opts.project != "":
		chosen, err = auth.ResolveProject(projects, opts.project)
		if err != nil {
			return nil, err
		}
	case len(projects) == 1:
		chosen = &projects[0]
	case opts.interactive:
		chosen, err = pickProject(projects)
		if err != nil {
			return nil, err
		}
	default:
		// Non-interactive with nothing named: the workspace default is the
		// same answer the server would have picked anyway, so recording it
		// costs nothing and makes the choice visible.
		chosen = auth.DefaultProject(projects)
		if chosen == nil {
			rep.ok("no default project — continuing without one")
			return nil, nil
		}
	}

	state.session.ActiveProjectID = chosen.ProjectID
	state.session.ActiveProjectName = chosen.Name
	if err := auth.SaveSession(state.session); err != nil {
		return nil, fmt.Errorf("could not record the active project: %w", err)
	}
	state.projectID = chosen.ProjectID
	rep.ok("project %s", chosen.Key)
	return chosen, nil
}
