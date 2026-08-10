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
	// streaming commands can clean up. After the first signal, default
	// handling is restored, so a second Ctrl+C kills the process immediately
	// instead of waiting on a stuck cleanup. The signal is recorded so the
	// exit code can distinguish an interactive interrupt (SIGINT -> 130)
	// from a supervisor shutdown (SIGTERM -> 143), per the 128+N convention.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	received := make(chan os.Signal, 1)
	go func() {
		s := <-sigCh
		received <- s
		signal.Stop(sigCh)
		cancel()
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
	select {
	case s := <-received:
		if s == syscall.SIGTERM {
			os.Exit(143) // 128 + SIGTERM(15)
		}
		os.Exit(130) // 128 + SIGINT(2)
	default:
	}
	if err != nil {
		// Cobra already printed the error; without this every failure
		// (unknown command, bad flag, API error) exited 0 and broke scripting.
		os.Exit(1)
	}
}
