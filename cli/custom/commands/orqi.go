package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	survey "github.com/AlecAivazis/survey/v2"
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"orq/cli/custom/launch"
)

// orqiFlags are the flags this command owns on `orq orqi`. Everything else is
// orqi's, except orq's own global --profile, which splitPassthroughGlobals
// (cli/custom/launchargs.go) parses onto the root before cobra dispatches.
type orqiFlags struct {
	Help    bool
	Install bool
}

// orqiFlagNames mirrors what parseOrqiArgv consumes;
// TestOrqiCompletionFlagsMatchParser asserts the two agree.
var orqiFlagNames = []string{"-h", "--help", "--install"}

// orqiGlobalFlagNames are orq's own root flags, which work on an orqi line
// because splitPassthroughGlobals lifts them out of argv before cobra
// dispatches. Offered for completion; never seen by parseOrqiArgv.
var orqiGlobalFlagNames = []string{"--no-input", "--profile"}

// parseOrqiArgv recognizes orq's own flags only at the FRONT of argv: the
// first argument orq does not own ends parsing and everything from there
// belongs to orqi verbatim, so a flag orqi grows later can never collide with
// one of ours. A leading `--` ends parsing explicitly. Same convention as
// launch.ParseArgv (cli/custom/launch/args.go), whose flag set and
// GatewayFlags return are gateway-specific and so not reusable here.
//
// cobra parses none of this: `orq orqi` runs with DisableFlagParsing, which
// leaves even the root's persistent --profile unparsed. It arrives at the
// front of argv, which is where this scanner reads it.
func parseOrqiArgv(argv []string) (orqiFlags, []string, error) {
	var flags orqiFlags
	i := 0
scan:
	for ; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--":
			i++
			break scan
		case arg == "-h" || arg == "--help":
			flags.Help = true
		case arg == "--install":
			flags.Install = true
		default:
			break scan
		}
	}
	rest := argv[i:]
	// --install starts no session, so there is no child for trailing
	// arguments to belong to. Refusing beats dropping them silently.
	if flags.Install && len(rest) > 0 {
		return flags, nil, fmt.Errorf("--install takes no arguments, got %q", strings.Join(rest, " "))
	}
	return flags, rest, nil
}

// orqiCompletionFlags returns orq's own flags matching toComplete. Cobra
// cannot enumerate them itself with flag parsing disabled. Anything that does
// not look like a flag belongs to orqi's own CLI. orq's globals are offered
// too, even though this file does not parse them: on an orqi line they work.
func orqiCompletionFlags(toComplete string) []string {
	if !strings.HasPrefix(toComplete, "-") {
		return nil
	}
	var out []string
	for _, f := range append(append([]string{}, orqiFlagNames...), orqiGlobalFlagNames...) {
		if strings.HasPrefix(f, toComplete) {
			out = append(out, f)
		}
	}
	return out
}

// Seams. Tests answer these instead of touching the real PATH or GOOS.
var (
	orqiLookPath = exec.LookPath
	orqiPlatform = func() string { return runtime.GOOS + "/" + runtime.GOARCH }
)

// orqiPlatforms is what the orqi release publishes. Linux arm64 is refused as
// early as Windows: install.sh would reject it too, but only after a prompt
// and a download.
var orqiPlatforms = map[string]bool{
	"darwin/arm64": true,
	"darwin/amd64": true,
	"linux/amd64":  true,
}

func orqiPlatformSupported() bool { return orqiPlatforms[orqiPlatform()] }

// orqiInstallDir is where install.sh will put the binary: the user's own
// ORQI_INSTALL_DIR, or install.sh's default.
func orqiInstallDir() string {
	if dir := strings.TrimSpace(os.Getenv("ORQI_INSTALL_DIR")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "bin")
}

// resolveOrqi returns the orqi binary's path, or "" when there is none.
// PATH is not enough on its own: install.sh writes to ~/.local/bin and only
// prints a hint about it, so a freshly installed orqi is invisible to
// LookPath until the user acts on that hint or opens a new shell.
func resolveOrqi() string {
	if path, err := orqiLookPath("orqi"); err == nil {
		return path
	}
	dir := orqiInstallDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, "orqi")
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return ""
}

const (
	orqiInstallerURL = "https://raw.githubusercontent.com/orq-ai/orqi/main/install.sh"
	orqiInstallerCmd = "curl -fsSL " + orqiInstallerURL + " | sh"
)

// runOrqiCommand is the seam tests replace so they never run curl or the real
// installer. Child output goes to stderr: it is the installer's diagnostics,
// while stdout belongs to orqi itself once it starts.
var runOrqiCommand = func(ctx context.Context, env map[string]string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = bartolocli.Stderr, bartolocli.Stderr
	cmd.Env = launch.MergeEnv(os.Environ(), env)
	return cmd.Run()
}

// installOrqi runs the orqi repo's own install.sh, which resolves the release,
// downloads the right tarball, sheds macOS quarantine by extracting it, and
// verifies the result by running `orqi --version`. Reimplementing that here
// would be a second copy of a path that has to stay in step with the orqi
// release layout. Downloaded to a file and run in two steps rather than
// `curl | sh`, for the reason updateViaInstaller records in update.go.
//
// No timeout: the installer downloads ~25 MB and the user is watching it. The
// command's own context still carries Ctrl-C.
func installOrqi(ctx context.Context, dir string) error {
	for _, bin := range []string{"curl", "sh"} {
		if _, err := orqiLookPath(bin); err != nil {
			return fmt.Errorf("installing orqi needs %s, which is not on PATH. Install it, or run:\n  %s", bin, orqiInstallerCmd)
		}
	}
	tmp, err := os.MkdirTemp("", "orq-orqi-")
	if err != nil {
		return fmt.Errorf("cannot create a temporary directory for the installer: %w", err)
	}
	defer os.RemoveAll(tmp)

	script := filepath.Join(tmp, "install.sh")
	if err := runOrqiCommand(ctx, nil, "curl", "-fsSL", "-o", script, orqiInstallerURL); err != nil {
		return fmt.Errorf("cannot download the orqi installer from %s: %w", orqiInstallerURL, err)
	}
	if err := runOrqiCommand(ctx, map[string]string{"ORQI_INSTALL_DIR": dir}, "sh", script); err != nil {
		return fmt.Errorf("the orqi installer failed: %w\nRun it yourself to see the full output:\n  %s", err, orqiInstallerCmd)
	}
	return nil
}

// orqiConfirm and runOrqiChild are seams; tests answer the prompt and capture
// the launch instead of drawing a terminal or executing a real orqi.
var (
	orqiInteractive = hasInteractiveTTY
	orqiConfirm     = func(message string) bool {
		answer := true
		if err := survey.AskOne(&survey.Confirm{Message: message, Default: true}, &answer, promptStdio()); err != nil {
			return false
		}
		return answer
	}
	runOrqiChild = launch.RunChild
)

// printOrqiHelp writes the help cobra cannot: DisableFlagParsing means the
// wrapper's own flags are never registered, so cmd.Help() would advertise none
// of them. launch/run.go's printAgentHelp exists for the same reason.
func printOrqiHelp() {
	fmt.Fprintf(bartolocli.Stderr, `Run orqi, the orq.ai assistant in your terminal, installing it first if it is missing.

orqi reads the login session this CLI maintains, so 'orq auth login' (or a
valid ORQ_API_KEY) is all the setup it needs.

Usage:
  orq orqi [flags] [--] [prompt or orqi arguments...]

Flags:
  -h, --help            Print this help and exit; never installs anything
      --install         Install or reinstall orqi, then exit without starting a session
      --no-input        Never prompt; fail instead of offering to install (orq global)
      --profile <name>  The login profile orqi should use (orq global)

These flags are recognised only before the first argument orq does not own.
Everything from that argument on is passed to orqi untouched, so
'orq orqi "why did it fail" --install' sends --install to orqi. The two orq
globals also work before the command word: 'orq --profile staging orqi'.
`)
}

func NewOrqiCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "orqi [flags] [--] [prompt or orqi arguments...]",
		Short: "Run orqi, the orq.ai assistant, installing it on first use",
		Long: `Run orqi, the orq.ai assistant in your terminal, installing it first if it is missing.

orqi reads the login session this CLI maintains, so 'orq auth login' (or a
valid ORQ_API_KEY) is all the setup it needs. Everything after the first
argument orq does not own is passed to orqi untouched.`,
		// Disabled so orqi's own flags reach it; parseOrqiArgv reads ours.
		DisableFlagParsing: true,
		ValidArgsFunction: func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if comps := orqiCompletionFlags(toComplete); comps != nil {
				return comps, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveDefault
		},
		// cobra's own help would list none of the wrapper's flags.
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runOrqi(cmd, args)
			if err != nil {
				return err
			}
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
}

func runOrqi(cmd *cobra.Command, argv []string) (int, error) {
	flags, passthrough, err := parseOrqiArgv(argv)
	if err != nil {
		return 1, err
	}
	if flags.Help {
		printOrqiHelp()
		return 0, nil
	}
	if !orqiPlatformSupported() {
		return 1, fmt.Errorf("orqi runs on macOS (arm64, x86_64) and Linux x86_64; this machine is %s. See https://github.com/orq-ai/orqi", orqiPlatform())
	}
	path := resolveOrqi()
	dir := orqiInstallDir()
	if path != "" {
		dir = filepath.Dir(path)
	}

	switch {
	case flags.Install:
		if err := installOrqi(cmd.Context(), dir); err != nil {
			return 1, err
		}
		success("orqi installed in %s", dir)
		return 0, nil
	case path == "":
		if !orqiInteractive() {
			return 1, fmt.Errorf("orqi is not installed. Install it with:\n  %s\nor run: orq orqi --install", orqiInstallerCmd)
		}
		if !orqiConfirm("orqi is not installed. Install it now?") {
			return 0, nil
		}
		if err := installOrqi(cmd.Context(), dir); err != nil {
			return 1, err
		}
		// The installer's PATH hint may not have been acted on yet, so run
		// the path we just wrote rather than looking it up again.
		path = filepath.Join(dir, "orqi")
	}

	// --profile was parsed onto the root before dispatch, by
	// splitPassthroughGlobals — the same path that made installSessionPreRun
	// resolve this profile's token rather than the default one's. Reading it
	// back here keeps the profile orqi is told about and the ORQ_API_KEY it
	// inherits as one answer.
	env := map[string]string{}
	if f := cmd.Root().PersistentFlags().Lookup("profile"); f != nil && f.Changed {
		env["ORQ_PROFILE"] = f.Value.String()
	}
	return runOrqiChild(path, passthrough, env)
}
