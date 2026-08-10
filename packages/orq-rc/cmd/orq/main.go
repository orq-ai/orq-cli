package main

import (
	bartolocli "github.com/orq-ai/bartolo/cli"
	generated "orq-rc/cli/generated"
	custom "orq/cli/custom"
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

	bartolocli.Root.Execute()
}
