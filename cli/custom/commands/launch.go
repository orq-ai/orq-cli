package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

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
				// Injected here (not at registration) so the login flow gets
				// this invocation's context for Ctrl-C during the approval
				// poll. Launch owns the "offer to log in" decision; this only
				// supplies the flow it cannot import.
				// Same reason as LoginHook: launch cannot import this package,
				// and the registry is the one place that knows where each
				// agent keeps its MCP entry.
				launch.PersistedMCPHook = mcpEntryPresent
				launch.LoginHook = func() error {
					_, err := deviceLogin(cmd.Context(), newReporter(false), &setupOptions{})
					return err
				}
				code, err := launch.Run(&def, args)
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
