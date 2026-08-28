package main

import (
	generated "orq-rc/cli/generated"
	custom "orq/cli/custom"

	"github.com/spf13/cobra"
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
	custom.Run(version, apiVersion, func(root *cobra.Command) { generated.Register(root) })
}
