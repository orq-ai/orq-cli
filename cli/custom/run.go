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

// Run is the shared entrypoint for both the stable (cmd/orq) and RC
// (packages/orq-rc/cmd/orq) mains, which differ only in which generated command
// tree they register. It owns the signal handling and the exit-code contract so
// the two modules cannot drift apart again:
//
//   - SIGINT/SIGTERM cancel the command context so streaming/long-running
//     commands clean up; a second signal restores default handling and kills
//     the process immediately instead of waiting on a stuck cleanup.
//   - Exit codes: 0 ok, 1 on a command error (cobra already printed it), 130 on
//     SIGINT, 143 on SIGTERM (the 128+N convention), so scripts and supervisors
//     can tell a failure and an interrupt apart.
//
// registerGenerated attaches the module's own generated commands onto the root
// before the custom commands are wired on top.
//
// apiVersion is the orq API line the generated commands came from. It is kept
// out of root.Version — that string is parsed as a bare semver by the update
// check, by install.sh and by anything else reading `orq --version` — and is
// surfaced through the version template and `orq version` instead.
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
		DefaultOutputFormat: "toon",
		Version:             version,
	})

	commands.SetAPIVersion(apiVersion)
	bartolocli.Root.SetVersionTemplate(
		"{{.Name}} version {{.Version}}\nbuilt against orq API " + apiVersion + "\n")

	registerGenerated(bartolocli.Root)
	Register(bartolocli.Root)
	// `launch <agent>` disables flag parsing, so cobra parses no flags at all
	// for that invocation — not even the root's own. Take orq's globals typed
	// before the agent name and parse them here (see splitLaunchGlobals).
	globals, rest := splitLaunchGlobals(bartolocli.Root, os.Args[1:])
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
