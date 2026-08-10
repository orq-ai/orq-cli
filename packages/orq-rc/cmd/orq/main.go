package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	bartolocli "github.com/orq-ai/bartolo/cli"
	generated "orq-rc/cli/generated"
	custom "orq/cli/custom"
)

// version is overwritten at release build time via
// `-ldflags "-X main.version=<semver>"`. Local dev builds report "dev".
var version = "dev"

// main mirrors cmd/orq/main.go in the stable module: same signal handling and
// the same exit-code contract (0 ok, 1 error, 130 SIGINT, 143 SIGTERM). RC
// builds previously discarded Execute()'s error and exited 0 on every failure.
func main() {
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
		os.Exit(1)
	}
}
