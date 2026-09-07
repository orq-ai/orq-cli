package main

import (
	custom "orq/cli/custom"
	"orq/cli/custom/commands"
	generated "orq/cli/generated"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// version is overwritten at release build time via
// `-ldflags "-X main.version=<semver>"`. Local dev builds report "dev".
var version = "dev"

// apiVersion is the orq API version the generated commands were built from,
// stamped the same way. The CLI version no longer encodes it, so this is how a
// build says which API line it speaks.
var apiVersion = "unknown"

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
