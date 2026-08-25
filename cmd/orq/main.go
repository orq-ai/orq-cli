package main

import (
	custom "orq/cli/custom"
	generated "orq/cli/generated"

	"github.com/spf13/cobra"
)

// version is overwritten at release build time via
// `-ldflags "-X main.version=<semver>"`. Local dev builds report "dev".
var version = "dev"

// apiVersion is the orq API version the generated commands were built from,
// stamped the same way. The CLI version no longer encodes it, so this is how a
// build says which API line it speaks.
var apiVersion = "unknown"

func main() {
	custom.Run(version, apiVersion, func(root *cobra.Command) { generated.Register(root) })
}
