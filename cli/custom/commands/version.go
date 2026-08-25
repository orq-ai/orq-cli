package commands

import (
	"fmt"
	"strings"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// apiVersion is the orq API line this build's generated commands came from,
// stamped at build time and handed over by custom.Run. "unknown" in a build
// that was not produced by the release pipeline.
var apiVersion = "unknown"

// SetAPIVersion is called once from custom.Run, before any command runs.
//
// The value is scrubbed to the characters a version can contain: it ends up
// inside the cobra version TEMPLATE, which is parsed with template.Must, so a
// stray "{{" arriving from .bartolo.json would panic every `orq --version`
// rather than print a strange string.
func SetAPIVersion(v string) {
	v = strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			return r
		case r == '.', r == '-', r == '+', r == '_':
			return r
		}
		return -1
	}, v)
	if v != "" {
		apiVersion = v
	}
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
