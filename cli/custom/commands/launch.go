package commands

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	bartolocli "github.com/orq-ai/bartolo/cli"

	"orq/cli/custom/auth"
	"orq/cli/custom/launch"
)

// NewLaunchCommand builds `orq launch` with one subcommand per agent. Agent
// subcommands disable cobra flag parsing so arbitrary agent flags pass
// through; parsing happens in launch.ParseArgv.
func NewLaunchCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "launch",
		Short: "Launch a coding agent routed through the orq.ai AI Router",
		Long: `Launch a coding-agent CLI preconfigured to route all model calls
through the orq.ai AI Router.

Credentials are resolved in this order, first match wins:

  1. The API-key profile in force (--profile, ORQ_PROFILE or 'orq auth
     profile use') — the login session is not read at all.
  2. ORQ_API_KEY from the environment — likewise, so an exported key
     overrides the workspace picked by 'orq auth login'.
  3. The key 'orq setup' minted, when it belongs to the workspace shown by
     'orq workspace' and has not expired.
  4. The active workspace token from the 'orq auth login' session, for the
     workspace shown by 'orq workspace'.

Unset ORQ_API_KEY to launch against the workspace you selected at login.
Session tokens expire after an hour; run 'orq setup' to mint a durable key.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	for _, def := range launch.Agents() {
		def := def
		root.AddCommand(&cobra.Command{
			Use:                def.Name,
			Short:              "Launch " + def.Label,
			DisableFlagParsing: true,
			// DisableFlagParsing hides our flags from cobra's completion
			// machinery; ValidArgsFunction still runs, so surface them here.
			ValidArgsFunction: func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
				if comps := launch.CompletionFlags(&def, toComplete); comps != nil {
					return comps, cobra.ShellCompDirectiveNoFileComp
				}
				// Non-flag input belongs to the agent — fall back to file
				// completion so its path arguments still complete.
				return nil, cobra.ShellCompDirectiveDefault
			},
			RunE: func(cmd *cobra.Command, args []string) error {
				// Built per invocation (not at registration) so the login flow
				// gets this invocation's context for Ctrl-C during the approval
				// poll. Launch owns every "should I ask" decision; these only
				// supply the flows it cannot import.
				hooks := launch.Hooks{
					Login: func() error {
						_, err := deviceLogin(cmd.Context(), newReporter(false), &setupOptions{})
						return err
					},
					DurableKey:   func() (string, error) { return offerLaunchKey(cmd, &setupOptions{}) },
					PersistedMCP: mcpEntryPresent,
					SkillsUpdate: func() (string, bool) { return skillsUpdateHint(cmd) },
					Interactive:  hasInteractiveTTY,
				}
				code, err := launch.Run(&def, args, hooks)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					if code == 0 {
						code = 1 // never report an error and exit 0
					}
				}
				if code != 0 {
					os.Exit(code)
				}
				return nil
			},
		})
	}

	return root
}

// keyOfferDeclinedDirs lists the directories where the user turned down
// launch's key offer. It is per directory, not global: a scratch checkout that
// never needs a key should stay quiet without silencing every other project.
const keyOfferDeclinedDirs = "launch.key_offer_declined_dirs"

// offerLaunchKey asks to replace the login session's token with a durable key
// and mints it the way connect does. An empty key means the user declined, now
// or earlier in this directory.
func offerLaunchKey(cmd *cobra.Command, opts *setupOptions) (string, error) {
	dir, _ := os.Getwd()
	if keyOfferDeclined(dir) {
		return "", nil
	}
	switch askKey(opts, loginTokenLabel, "Use my login token, and don't ask again in this directory") {
	case keyCreate:
	case keySession, keyAborted:
		return "", nil
	case keyThird:
		if rememberKeyOfferDeclined(dir) == nil {
			fmt.Fprintln(os.Stderr, "Not asking again in this directory; 'orq setup' creates the key whenever you want it.")
		}
		return "", nil
	}
	rep := newReporter(false)
	state, err := resolveAuth(cmd.Context(), rep, opts)
	if err != nil {
		return "", err
	}
	return mintDurableKey(rep, auth.NewClient(state.apiBase), state, opts)
}

// declinedDirs reads the list. Stored as one newline-separated string: viper
// lower-cases and dot-splits map keys, which would mangle paths.
func declinedDirs() []string {
	if bartolocli.Creds == nil {
		return nil
	}
	return strings.Split(bartolocli.Creds.GetString(keyOfferDeclinedDirs), "\n")
}

func keyOfferDeclined(dir string) bool {
	return dir != "" && slices.Contains(declinedDirs(), dir)
}

func rememberKeyOfferDeclined(dir string) error {
	if dir == "" || bartolocli.Creds == nil {
		return errors.New("nowhere to remember the answer")
	}
	dirs := slices.DeleteFunc(append(declinedDirs(), dir), func(d string) bool { return d == "" })
	bartolocli.Creds.Set(keyOfferDeclinedDirs, strings.Join(dirs, "\n"))
	return saveCreds()
}
