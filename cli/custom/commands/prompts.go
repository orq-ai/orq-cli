package commands

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"orq/cli/custom/auth"

	survey "github.com/AlecAivazis/survey/v2"
	isatty "github.com/mattn/go-isatty"
	"github.com/spf13/viper"
)

// promptStdio routes survey prompts to stderr. A prompt is interaction, not
// output: survey defaults to os.Stdout, so `orq ... --json > out.json` at a
// terminal would write the question into the JSON payload. Read from stdin,
// write the prompt and the typed echo to stderr, leaving stdout for the result.
func promptStdio() survey.AskOpt {
	return survey.WithStdio(os.Stdin, os.Stderr, os.Stderr)
}

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

// userEnvAPIKey is ORQ_API_KEY as the user's shell exported it, snapshotted at
// the same moment. Empty is the honest answer for "the user exported nothing",
// which os.Getenv can no longer give after the injection.
var userEnvAPIKey string

// userEnvAPIKeyTaken distinguishes "the user exported nothing" from "PreRun
// never ran", which is the case in unit tests that set the variable directly.
var userEnvAPIKeyTaken bool

// SetExplicitAPIKey is called once per invocation from custom's PreRun, before
// the session-token injection.
func SetExplicitAPIKey(v bool) { explicitAPIKey = v }

// SetUserEnvAPIKey is called alongside SetExplicitAPIKey, before the injection.
func SetUserEnvAPIKey(v string) { userEnvAPIKey, userEnvAPIKeyTaken = strings.TrimSpace(v), true }

// UserEnvAPIKey reports the key the user exported, never the injected session
// token. Every credential decision must read this rather than the environment.
func UserEnvAPIKey() string {
	if userEnvAPIKeyTaken {
		return userEnvAPIKey
	}
	return strings.TrimSpace(os.Getenv("ORQ_API_KEY"))
}

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
	}, &chosen, promptStdio()); err != nil {
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
