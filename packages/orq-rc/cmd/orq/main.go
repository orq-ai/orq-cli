package main

import (
	generated "orq-rc/cli/generated"
	custom "orq/cli/custom"
	"orq/cli/custom/commands"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// version is overwritten at release build time via
// `-ldflags "-X main.version=<semver>"`. Local dev builds report "dev".
var version = "dev"

// apiVersion is the orq API version the generated commands were built from —
// for this module, the staging (prerelease) schema line.
var apiVersion = "unknown"

// main defers to custom.Run (shared with the stable cmd/orq main) so the two
// modules keep identical signal handling and exit-code behavior; only the
// generated command tree differs.
func main() {
	custom.Run(version, apiVersion, traceAPI(), func(root *cobra.Command) { generated.Register(root) })
}

func traceAPI() commands.TraceAPI {
	return commands.TraceAPI{
		GetTrace: func(traceID string, params *viper.Viper) (map[string]any, error) {
			_, decoded, err := generated.OpenapiTracesGet(traceID, params)
			return decoded, err
		},
		GetSpan: func(traceID, spanID string, params *viper.Viper) (map[string]any, error) {
			_, decoded, err := generated.OpenapiTracesGetSpan(traceID, spanID, params)
			return decoded, err
		},
		ListSpans: func(traceID string, params *viper.Viper) (map[string]any, error) {
			_, decoded, err := generated.OpenapiTracesListSpans(traceID, params)
			return decoded, err
		},
	}
}
