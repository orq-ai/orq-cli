package commands

import (
	"fmt"

	"orq/cli/custom/auth"
)

// NotFoundScopeHint names the scope a 404 was answered within. Reads by id
// answer inside one project, so an id from a sibling project comes back as
// "not found" — indistinguishable, in the bare message, from an id that never
// existed. Returns "" for any other error, and for a 404 there is nothing to
// say about.
func NotFoundScopeHint(err error) string {
	if err == nil || !threadNotFound(err) {
		return ""
	}
	if explicitAPIKey {
		return "\nIds are read within one project: the API key in use decides which. Check it belongs to the project this id was recorded in."
	}
	session, sessionErr := auth.ReadSession()
	if sessionErr != nil || session == nil {
		return ""
	}
	if session.ActiveProjectID == "" {
		return "\nIds are read within one project and no project is active. Pick the one this id belongs to with `orq projects use <key>`."
	}
	name := session.ActiveProjectName
	if name == "" {
		name = session.ActiveProjectID
	}
	return fmt.Sprintf("\nThis looked in project %q, which is the active one. Ids are read within one project: if it belongs to another, switch with `orq projects use <key>` and try again.", name)
}
