package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	survey "github.com/AlecAivazis/survey/v2"
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"orq/cli/custom/launch"
)

// orqiFlags are the flags this command owns on `orq orqi`. Everything behind
// the command word that is not one of them is orqi's. orq's own globals go in
// front of it, where splitPassthroughGlobals (cli/custom/launchargs.go) parses
// them onto the root before cobra dispatches.
type orqiFlags struct {
	Help    bool
	Install bool
}

// orqiFlagNames mirrors what parseOrqiArgv consumes;
// TestOrqiCompletionFlagsMatchParser asserts the two agree.
var orqiFlagNames = []string{"-h", "--help", "--install"}

// parseOrqiArgv recognizes orq's own flags only at the FRONT of argv: the
// first argument orq does not own ends parsing and everything from there
// belongs to orqi verbatim. A leading `--` ends parsing explicitly. Same
// convention as launch.ParseArgv (cli/custom/launch/args.go), whose flag set
// and GatewayFlags return are gateway-specific and so not reusable here.
//
// The set orq recognizes at the front is larger than the two flags this
// scanner owns: splitPassthroughGlobals (cli/custom/launchargs.go) lifts
// every root persistent flag off the front of an orqi line before cobra
// dispatches — --profile, --no-input, --json, --server, --workspace,
// --verbose, --no-color, --raw, -o and -j. So a flag orqi grows later that is
// named like one of those, or like -h/--help/--install, is shadowed unless the
// user writes it after a positional argument or after `--`.
//
// cobra parses none of this: `orq orqi` runs with DisableFlagParsing, which
// leaves even the root's persistent --profile unparsed. It arrives at the
// front of argv, which is where this scanner reads it.
func parseOrqiArgv(argv []string) (orqiFlags, []string, error) {
	var flags orqiFlags
	install := -1
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
			flags.Install, install = true, i
		default:
			break scan
		}
	}
	// --install starts no session, so there is no child for later arguments to
	// belong to. Measured from --install's own position rather than from what
	// survived the scan, so `--install --help` is refused too: the scanner owns
	// --help, and counting only unrecognized leftovers would silently swallow
	// the flags it does own.
	if rest := argv[install+1:]; flags.Install && len(rest) > 0 {
		return flags, nil, fmt.Errorf("--install takes no arguments, got %q", strings.Join(rest, " "))
	}
	return flags, argv[i:], nil
}

// orqiCompletionFlags returns orq's own flags matching toComplete. Cobra
// cannot enumerate them itself with flag parsing disabled. Anything that does
// not look like a flag belongs to orqi's own CLI, and so does every root
// global typed here: they only count in front of the command word.
func orqiCompletionFlags(toComplete string) []string {
	if !strings.HasPrefix(toComplete, "-") {
		return nil
	}
	var out []string
	for _, f := range orqiFlagNames {
		if strings.HasPrefix(f, toComplete) {
			out = append(out, f)
		}
	}
	return out
}

// Seams. Tests answer these instead of touching the real PATH or GOOS.
var (
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
	if path, err := lookPath("orqi"); err == nil {
		return path
	}
	dir := orqiInstallDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, "orqi")
	// An execute bit is part of "installed": a truncated download or a
	// chmod-644 file would otherwise resolve, and the user would get a raw
	// exec failure instead of being offered the install again.
	if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
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
var runOrqiCommand = realRunOrqiCommand

func realRunOrqiCommand(ctx context.Context, env map[string]string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = bartolocli.Stderr, bartolocli.Stderr
	// The installer has no use for orq credentials, and install.sh is fetched
	// unpinned from main. Two of them would otherwise reach it: the live
	// workspace bearer token installSessionPreRun (cli/custom/register.go)
	// puts in ORQ_API_KEY before this runs, and any key the user exported
	// themselves under any of the three names bartolo reads.
	cmd.Env = launch.MergeEnv(withoutOrqCredentials(os.Environ()), env)
	return cmd.Run()
}

// withoutOrqCredentials drops orq's credential variables from a "K=V" environ.
func withoutOrqCredentials(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if slices.Contains(APIKeyEnvVars, key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// installOrqi runs the orqi repo's own install.sh, which resolves the release,
// downloads the right tarball, sheds macOS quarantine by extracting it, and
// verifies the result by running `orqi --version`. Reimplementing that here
// would be a second copy of a path that has to stay in step with the orqi
// release layout.
//
// tar is preflighted where `orq update` needs only curl and sh: orqi's script
// unpacks a tarball, orq's does not.
//
// No timeout: the installer downloads ~25 MB and the user is watching it. The
// command's own context still carries Ctrl-C.
func installOrqi(ctx context.Context, dir string) error {
	return runShellInstaller(ctx, runOrqiCommand, installerSpec{
		URL:        orqiInstallerURL,
		Env:        map[string]string{"ORQI_INSTALL_DIR": dir},
		Needs:      []string{"curl", "sh", "tar"},
		TempPrefix: "orq-orqi-",
		Subject:    "installing orqi",
		Manual:     orqiInstallerCmd,
	})
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
	fmt.Fprintf(bartolocli.Stdout, `Run orqi, the orq.ai assistant in your terminal, installing it first if it is missing.

orqi reads the login session this CLI maintains, so 'orq auth login' (or a
valid ORQ_API_KEY) is all the setup it needs.

Usage:
  orq orqi [flags] [--] [prompt or orqi arguments...]

Flags:
  -h, --help     Print this help and exit; never installs anything
      --install  Install or reinstall orqi, then exit without starting a session

They are recognised only before the first argument orq does not own, so
'orq orqi "why did it fail" --install' sends --install to orqi. A leading --
ends orq's parsing explicitly.

orq's own global flags go in FRONT of the command word:

  orq --profile staging orqi "why did it fail?"
  orq --no-input orqi --install

Behind it they belong to orqi, which is what keeps a prompt that opens with
--workspace or --verbose from being read as one of ours.
`)
}

// NewOrqiCommand builds `orq orqi`, which installs orqi on first use and then
// runs it. It disables cobra flag parsing so orqi's own flags pass through
// untouched; parseOrqiArgv reads the handful orq itself owns off the front of
// argv.
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
			// Only a code the child chose justifies os.Exit: it is the one
			// value cobra cannot carry. Everything else goes back to
			// custom.Run, which owns 130 and 143 — exiting here on our own
			// errors reported 1 for a Ctrl-C mid-install.
			if code > 1 {
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				os.Exit(code)
			}
			return err
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
	// Without a directory the install target would be the empty string and
	// the binary a bare "orqi" — a PATH lookup rather than the file we wrote.
	if dir == "" {
		return 1, fmt.Errorf("orq cannot tell where to install orqi: your home directory is not resolvable. Set ORQI_INSTALL_DIR to the directory orqi should live in")
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
