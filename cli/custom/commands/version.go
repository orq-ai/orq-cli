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

// apiVersionAnnotation carries APIVersionLine() to the cobra version template
// as template DATA. Concatenating the value into the template string instead
// would parse it: .bartolo.json is written by another repo's workflow, and a
// stray "{{" arriving from there would panic template.Must on every
// `orq --version`.
const apiVersionAnnotation = "orqApiVersionLine"

// SetAPIVersion is called once from custom.Run, before any command runs, and
// installs the `orq --version` template alongside the value so the flag and the
// `orq version` command can only ever render the same sentence.
func SetAPIVersion(root *cobra.Command, v string) {
	if v != "" {
		apiVersion = v
	}
	if root.Annotations == nil {
		root.Annotations = map[string]string{}
	}
	root.Annotations[apiVersionAnnotation] = APIVersionLine()
	root.SetVersionTemplate(
		"{{.Name}} version {{.Version}}\n{{index .Annotations \"" + apiVersionAnnotation + "\"}}\n")
}

// APIVersionLine is the single rendering of which orq API this build speaks,
// used by both `orq --version` and `orq version` so the two cannot drift into
// two different wordings of the same fact.
func APIVersionLine() string {
	return "built against orq API " + apiVersion
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
			fmt.Fprintf(bartolocli.Stdout, "%s version %s\n%s\ninstalled via %s\n",
				cmd.Root().Name(), cli, APIVersionLine(), channel)
			return nil
		},
	}
}
