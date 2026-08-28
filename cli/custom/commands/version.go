package commands

import (
	"fmt"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// apiVersion is the orq API line this build's generated commands came from,
// stamped at build time and handed over by custom.Run. "unknown" in a build
// that was not produced by the release pipeline.
var apiVersion = "unknown"

// SetAPIVersion is called once from custom.Run, before any command runs.
func SetAPIVersion(_ *cobra.Command, v string) {
	if v != "" {
		apiVersion = v
	}
}

// NewVersionCommand reports the CLI version and the orq API version it was
// generated against. The API line is intentionally not duplicated in
// `orq --version`; that flag remains the stable, semver-only parser contract.
func NewVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the CLI version and the orq API version it was built against",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := currentVersion(cmd)
			channel, _ := detectChannel()
			if !wantsHumanView(cmd) {
				return emit(map[string]any{
					"cli":     cli,
					"api":     apiVersion,
					"channel": string(channel),
				})
			}
			fmt.Fprintf(bartolocli.Stdout, "%s version %s\nbuilt against orq API %s\ninstalled via %s\n",
				cmd.Root().Name(), cli, apiVersion, channel)
			return nil
		},
	}
}
