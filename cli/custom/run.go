package custom

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"orq/cli/custom/commands"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// Run is the shared entrypoint for both mains, which differ only in the
// generated tree they register. It owns signal handling and the exit-code
// contract - 0 ok, 1 command error, 130 SIGINT, 143 SIGTERM, and a second
// signal kills immediately rather than waiting on a stuck cleanup - so the two
// modules cannot drift apart again.
//
// apiVersion is the orq API line the generated commands came from. It stays out
// of root.Version, which install.sh and the update check parse as a bare semver,
// and is surfaced through the version template and `orq version` instead.
func Run(version, apiVersion string, registerGenerated func(root *cobra.Command)) {
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
		SerializationFormat: "toon",
		Version:             version,
	})

	commands.SetAPIVersion(bartolocli.Root, apiVersion)

	registerGenerated(bartolocli.Root)
	Register(bartolocli.Root)
	// Passthrough commands (launch, orqi) disable flag parsing, so cobra
	// parses no flags at all for them — not even the root's own. Take orq's
	// globals typed before the command's own argv and parse them here (see
	// splitPassthroughGlobals).
	globals, rest := splitPassthroughGlobals(bartolocli.Root, os.Args[1:])
	if len(globals) > 0 {
		if err := bartolocli.Root.PersistentFlags().Parse(globals); err != nil {
			fmt.Fprintf(bartolocli.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		bartolocli.Root.SetArgs(rest)
	}

	// ExecuteContextC, not ExecuteContext, for the command that actually ran:
	// the notice's suppression rules are per-command, and root cannot answer
	// which one this was.
	executed, err := bartolocli.Root.ExecuteContextC(ctx)
	if err == nil && executed != nil {
		// After the command's own output, so the notice never interleaves with
		// it, and never on a failing run where the user has a real problem to
		// read. Silent unless a person at a terminal is a day overdue an update.
		commands.MaybePrintUpdateNotice(executed)
	}
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
