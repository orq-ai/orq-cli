package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	bartolocli "github.com/orq-ai/bartolo/cli"
	custom "orq/cli/custom"
	generated "orq/cli/generated"
)

// version is overwritten at release build time via
// `-ldflags "-X main.version=<semver>"`. Local dev builds report "dev".
var version = "dev"

func main() {
	// Cancel the command context on SIGINT/SIGTERM so long-running and
	// streaming commands can clean up. Once the context is cancelled stop()
	// restores default signal handling, so a second Ctrl+C kills the process
	// immediately instead of waiting on a stuck cleanup.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	bartolocli.Init(&bartolocli.Config{
		AppName:             "orq",
		EnvPrefix:           "ORQ",
		APIKeyEnvVar:        "ORQ_API_KEY",
		DefaultOutputFormat: "toon",
		Version:             version,
	})

	generated.Register(bartolocli.Root)
	custom.Register(bartolocli.Root)

	err := bartolocli.Root.ExecuteContext(ctx)
	if ctx.Err() != nil {
		// 128 + SIGINT(2): the conventional exit code for "interrupted".
		os.Exit(130)
	}
	if err != nil {
		// Cobra already printed the error; without this every failure
		// (unknown command, bad flag, API error) exited 0 and broke scripting.
		os.Exit(1)
	}
}
