package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
// command's own writer, which lands on stdout and corrupts `-o json` output.
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

// persistServerDefault writes host to config.json the way bartolo's `orq server
// set` does (its saveJSONConfig is unexported): `server-default` set, the
// legacy `server` key dropped, every other key preserved.
func persistServerDefault(host string) error {
	filename := path.Join(viper.GetString("config-directory"), "config.json")
	config := map[string]json.RawMessage{}
	data, err := os.ReadFile(filename)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &config); err != nil {
				return fmt.Errorf("config.json is not valid JSON; fix or delete it: %w", err)
			}
			// JSON null is valid but does not describe an object. Treat it like
			// an empty config; arrays and other non-object values still fail above.
			if config == nil {
				config = map[string]json.RawMessage{}
			}
		}
	}
	delete(config, "server")
	encodedHost, err := json.Marshal(host)
	if err != nil {
		return err
	}
	config["server-default"] = encodedHost
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := auth.WriteSecretFile(filename, out); err != nil {
		return err
	}
	viper.Set("server-default", host)
	return nil
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
