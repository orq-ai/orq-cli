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

// ProfileServer is the host bound to the active credentials profile, or "" when
// that profile has none. A profile is the more specific statement of intent
// than a host persisted globally with `orq server set`, so custom.resolveServer
// ranks it above the config layer and below the env vars and flags.
func ProfileServer() string {
	if bartolocli.Creds == nil {
		return ""
	}
	return strings.TrimSpace(bartolocli.Creds.GetString("profiles." + auth.ActiveProfile() + ".server"))
}

// BindProfileServer records a host on a profile, so `orq --profile acme ...`
// routes to acme's backend with no flag and no session read. Only an explicit
// host is bound: pinning the default would survive a change of default and
// silently keep an old one alive.
func BindProfileServer(profile, server string) error {
	server = strings.TrimSpace(server)
	if bartolocli.Creds == nil || profile == "" || server == "" {
		return nil
	}
	if bartolocli.Creds.GetString("profiles."+profile+".server") == server {
		return nil
	}
	bartolocli.Creds.Set("profiles."+profile+".server", server)
	return saveCreds()
}

// saveCreds persists the credentials file with owner-only permissions.
func saveCreds() error {
	filename := path.Join(viper.GetString("config-directory"), "credentials.json")
	if err := bartolocli.Creds.WriteConfigAs(filename); err != nil {
		return err
	}
	return chmodOwnerOnly(filename)
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
