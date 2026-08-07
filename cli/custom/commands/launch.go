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
through the orq.ai AI Router. Authenticate with 'orq auth login' or ORQ_API_KEY.`,
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
			RunE: func(_ *cobra.Command, args []string) error {
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
