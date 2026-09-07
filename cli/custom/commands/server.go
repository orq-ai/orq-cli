package commands

import (
	"path"
	"strings"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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

// ProfileServer is the host bound to the bartolo profile in force, or "" when
// there is none. Selecting a profile is how you select a backend, so
// custom.resolveServer ranks it above `orq server set`.
func ProfileServer() string {
	if bartolocli.Creds == nil || !profileInForce() {
		return ""
	}
	return strings.TrimSpace(bartolocli.GetProfile()["server"])
}

// saveCreds persists the credentials file through bartolo's 0600 write.
func saveCreds() error {
	return bartolocli.Creds.Save(path.Join(viper.GetString("config-directory"), "credentials.json"))
}

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
