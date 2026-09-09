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
		return "\nScope comes from the API key in use."
	}
	session, sessionErr := auth.ReadSession()
	if sessionErr != nil || session == nil {
		return ""
	}
	if session.ActiveProjectID == "" {
		return "\nNo active project; ids are read within one: `orq projects use <key>`."
	}
	name := session.ActiveProjectName
	if name == "" {
		name = session.ActiveProjectID
	}
	return fmt.Sprintf("\nLooked in project %q; ids are read within one: `orq projects use <key>` to switch.", name)
}
