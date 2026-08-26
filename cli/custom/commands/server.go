package commands

import (
	"orq/cli/custom/auth"

	"github.com/spf13/cobra"
)

// DeprecatedAPIBaseFlag re-registers --api-base-url on a command that used to
// own it, as an alias of the global --server. CHANGELOG's stability contract
// gives a removed flag one release of warning before it disappears.
//
// Hidden, but not cobra's MarkDeprecated: pflag prints that notice through the
// command's own writer, which lands on stdout and corrupts `--json` output.
// custom.resolveServer reads the value and warns on stderr instead. Remove the
// flag, that branch and this function together after one release.
func DeprecatedAPIBaseFlag(cmd *cobra.Command) {
	cmd.Flags().String("api-base-url", "", "Deprecated alias for --server")
	_ = cmd.Flags().MarkHidden("api-base-url")
}

// serverURL is the host the root PreRun resolved for this invocation (see
// custom.resolveServer). Empty means nothing overrode the default, which
// auth.ResolveURLs then supplies.
func serverURL() string { return auth.Server() }

// sessionAPIBase prefers the resolved server over the host the session was
// authenticated against, so --server still diverts a single call.
func sessionAPIBase(session *auth.Session) string {
	if v := serverURL(); v != "" {
		return v
	}
	if session != nil {
		return session.APIBaseURL
	}
	return ""
}
