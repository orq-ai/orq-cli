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
func SetAPIVersion(v string) {
	if v != "" {
		apiVersion = v
	}
}

// NewVersionCommand reports the CLI version and the orq API version it was
// generated against. Since the two version lines were split apart, the tag
// alone no longer says which API a binary speaks, and support questions start
// with exactly that — so it has to be printable, and machine-readable.
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
			fmt.Fprintf(bartolocli.Stdout, "orq version %s\n  orq API   %s\n  installed %s\n", cli, apiVersion, channel)
			return nil
		},
	}
}
