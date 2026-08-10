package commands

import (
	"errors"
	"fmt"
	"os"

	"orq/cli/custom/auth"

	survey "github.com/AlecAivazis/survey/v2"
	isatty "github.com/mattn/go-isatty"
	"github.com/spf13/viper"
)

// hasInteractiveTTY gates every prompt in the CLI. --no-input/ORQ_NO_INPUT
// forces the non-interactive path even on a real TTY, so scripts can rely on
// "fail instead of hang" regardless of how they are invoked.
func hasInteractiveTTY() bool {
	if viper.GetBool("no-input") {
		return false
	}
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

// explicitAPIKey records whether the USER configured an API key (env var or
// credentials profile), snapshotted by the custom package's PreRun BEFORE it
// injects the session token into ORQ_API_KEY. Reading the env after that
// injection always finds a key, which made every `workspace use` warn about a
// shadow that did not exist. A single snapshot also removes the duplicated
// apiKeyConfigured logic this package used to carry (import-cycle workaround)
// that let the two copies drift.
var explicitAPIKey bool

// SetExplicitAPIKey is called once per invocation from custom's PreRun, before
// the session-token injection.
func SetExplicitAPIKey(v bool) { explicitAPIKey = v }

func selectWorkspace(workspaces []map[string]any, message string) (string, error) {
	if len(workspaces) == 0 {
		return "", errors.New("no workspaces available")
	}
	if len(workspaces) == 1 {
		if k, ok := workspaces[0]["key"].(string); ok {
			return k, nil
		}
		return "", errors.New("workspace is missing a key")
	}
	options := make([]string, 0, len(workspaces))
	keys := make([]string, 0, len(workspaces))
	for _, w := range workspaces {
		name, _ := w["name"].(string)
		key, _ := w["key"].(string)
		options = append(options, fmt.Sprintf("%s (%s)", name, key))
		keys = append(keys, key)
	}
	var chosen string
	if err := survey.AskOne(&survey.Select{
		Message: message,
		Options: options,
	}, &chosen); err != nil {
		return "", err
	}
	for i, opt := range options {
		if opt == chosen {
			return keys[i], nil
		}
	}
	return "", errors.New("no workspace selected")
}

func sessionAPIBase(override string, session *auth.Session) string {
	if override != "" {
		return override
	}
	if session != nil {
		return session.APIBaseURL
	}
	return ""
}
