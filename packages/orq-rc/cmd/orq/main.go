package main

import (
	generated "orq-rc/cli/generated"
	custom "orq/cli/custom"

	"github.com/spf13/cobra"
)

// version is overwritten at release build time via
// `-ldflags "-X main.version=<semver>"`. Local dev builds report "dev".
var version = "dev"

// main defers to custom.Run (shared with the stable cmd/orq main) so the two
// modules keep identical signal handling and exit-code behavior; only the
// generated command tree differs.
func main() {
	custom.Run(version, func(root *cobra.Command) { generated.Register(root) })
}
