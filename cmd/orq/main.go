package main

import (
	custom "orq/cli/custom"
	generated "orq/cli/generated"

	"github.com/spf13/cobra"
)

// version is overwritten at release build time via
// `-ldflags "-X main.version=<semver>"`. Local dev builds report "dev".
var version = "dev"

func main() {
	custom.Run(version, func(root *cobra.Command) { generated.Register(root) })
}
