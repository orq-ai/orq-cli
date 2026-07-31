package main

import (
	"os"

	bartolocli "github.com/orq-ai/bartolo/cli"
	custom "orq/cli/custom"
	generated "orq/cli/generated"
)

// version is overwritten at release build time via
// `-ldflags "-X main.version=<semver>"`. Local dev builds report "dev".
var version = "dev"

func main() {
	bartolocli.Init(&bartolocli.Config{
		AppName:             "orq",
		EnvPrefix:           "ORQ",
		APIKeyEnvVar:        "ORQ_API_KEY",
		DefaultOutputFormat: "toon",
		Version:             version,
	})

	generated.Register(bartolocli.Root)
	custom.Register(bartolocli.Root)

	if err := bartolocli.Root.Execute(); err != nil {
		os.Exit(1)
	}
}
