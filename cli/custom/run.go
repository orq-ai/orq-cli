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
// contract - 0 ok, 2 bad invocation (a usage error: bad flag or value, unknown
// command or subcommand, a destructive command without --force), 1 any other
// command error, 130 SIGINT, 143 SIGTERM, and a second signal kills immediately
// rather than waiting on a stuck cleanup - so the two modules cannot drift apart
// again. (orq launch and orq orqi additionally propagate the child's own status.)
//
// apiVersion is the orq API line the generated commands came from. It stays out
// of root.Version, which install.sh and the update check parse as a bare semver,
// and is surfaced through the version template and `orq version` instead.
func Run(version, apiVersion string, traceAPI commands.TraceAPI, registerGenerated func(root *cobra.Command)) {
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
	Register(bartolocli.Root, traceAPI)
	// Passthrough commands (launch, orqi) disable flag parsing, so cobra
	// parses no flags at all for them — not even the root's own. Take orq's
	// globals typed before the command's own argv and parse them here (see
	// splitPassthroughGlobals).
	globals, rest := splitPassthroughGlobals(bartolocli.Root, os.Args[1:])
	if len(globals) > 0 {
		if err := bartolocli.Root.PersistentFlags().Parse(globals); err != nil {
			fmt.Fprintf(bartolocli.Stderr, "Error: %v\n", err)
			// A malformed global flag is bad input, not a failed operation, so it
			// maps to the usage exit code like every other flag error.
			os.Exit(bartolocli.ExitCodeFor(bartolocli.NewUsageError(err)))
		}
		bartolocli.Root.SetArgs(rest)
	}
	if err := unknownSubcommand(bartolocli.Root, rest); err != nil {
		fmt.Fprintf(bartolocli.Stderr, "Error: %v\n", err)
		os.Exit(bartolocli.ExitCodeFor(err))
	}

	// ExecuteContextC, not ExecuteContext, for the command that actually ran:
	// the check's suppression rules are per-command, and root cannot answer
	// which one this was.
	executed, err := bartolocli.Root.ExecuteContextC(ctx)
	if err == nil && executed != nil {
		// Refreshes the cached answer and prints nothing; the notice went out
		// before the command (installUpdateNoticePreRun). The round trip is
		// here so it stays off the command's critical path.
		commands.RefreshUpdateCache(executed)
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
		// Route through bartolo's mapping instead of a hardcoded 1: a
		// *bartolocli.UsageError (bad flag, enum/format rejection, destructive
		// command without --force, unknown subcommand) exits 2, a declined
		// destructive prompt (ErrDestructiveRefused) exits 0, everything else 1,
		// so a script can tell a typo from a failed request.
		os.Exit(bartolocli.ExitCodeFor(err))
	}
}
