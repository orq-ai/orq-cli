package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// An explicit `orq update` may wait longer than the passive notice: the user
// asked for this and is watching it happen.
const updateFetchTimeout = 10 * time.Second

// Overridable for tests, which must not shell out to the real npm or run the
// real installer.
var runUpdateCommand = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, bartolocli.Stdout, bartolocli.Stderr
	return cmd.Run()
}

func NewUpdateCommand() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the CLI to the latest published version",
		Long: "Replace this binary with the latest published release, using the " +
			"channel it was installed through: npm, or install.sh. --check reports " +
			"what is available and changes nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd, checkOnly)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Report the available version without installing it")
	return cmd
}

func runUpdate(cmd *cobra.Command, checkOnly bool) error {
	current := currentVersion(cmd)
	if _, ok := parseSemver(current); !ok {
		return fmt.Errorf("this is a %s build, not a published release; update it by rebuilding from source", current)
	}
	channel, path := detectChannel()

	ctx, cancel := context.WithTimeout(cmd.Context(), updateFetchTimeout)
	defer cancel()
	latest, err := fetchLatestVersion(ctx, current)
	if err != nil {
		return fmt.Errorf("cannot reach the npm registry to find the latest version: %w", err)
	}
	// An explicit check is also a real check: refresh the cache so the passive
	// notice does not repeat what the user just read.
	writeUpdateCache(current, latest)
	available := updateAvailable(current, latest)

	if checkOnly {
		return reportUpdateCheck(cmd, current, latest, channel, available)
	}
	if !available {
		if wantsHumanView(cmd) {
			fmt.Fprintf(bartolocli.Stdout, "orq %s is already the latest version.\n", current)
			return nil
		}
		return emit(map[string]any{"channel": string(channel), "from": current, "to": current, "updated": false})
	}

	switch channel {
	case channelNPM:
		err = updateViaNPM()
	case channelInstaller:
		err = updateViaInstaller()
	default:
		return fmt.Errorf("cannot update: this binary was not installed by install.sh or npm (found at %s)\n"+
			"  npm install:       npm install -g %s@latest\n"+
			"  install.sh:        %s", path, npmPackage, installerCmd)
	}
	if err != nil {
		return err
	}
	if !wantsHumanView(cmd) {
		return emit(map[string]any{"channel": string(channel), "from": current, "to": latest, "updated": true})
	}
	fmt.Fprintf(bartolocli.Stdout, "\nUpdated orq %s -> %s\n  Release notes: https://github.com/orq-ai/orq-cli/releases/tag/v%s\n", current, latest, latest)
	return nil
}

func reportUpdateCheck(cmd *cobra.Command, current, latest string, channel updateChannel, available bool) error {
	if !wantsHumanView(cmd) {
		return emit(map[string]any{
			"channel":          string(channel),
			"current":          current,
			"latest":           latest,
			"update_available": available,
		})
	}
	if available {
		fmt.Fprintf(bartolocli.Stdout, "orq %s (%s); latest %s. Run 'orq update'\n", current, channel, latest)
	} else {
		fmt.Fprintf(bartolocli.Stdout, "orq %s (%s); up to date\n", current, channel)
	}
	return nil
}

// updateViaNPM hands the work to npm: the package manager owns those files, and
// writing into node_modules behind its back is how a global install becomes
// unrepairable. When npm cannot do it, print the command rather than guessing.
func updateViaNPM() error {
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("this binary was installed with npm, but npm is not on PATH. Run:\n  npm install -g %s@latest", npmPackage)
	}
	if err := runUpdateCommand("npm", "install", "-g", npmPackage+"@latest"); err != nil {
		return fmt.Errorf("npm install failed: %w\nIf this is a permissions problem, run it yourself:\n  npm install -g %s@latest", err, npmPackage)
	}
	return nil
}

// updateViaInstaller re-runs install.sh, which already resolves the latest
// release, verifies its published .sha256 and swaps the binary in atomically.
// Reimplementing that here would be a second copy of the download-and-verify
// path that could drift from the one every install already goes through.
// ponytail: the installer is the update mechanism; --no-modify-path/--no-setup
// keep it to the one job, since PATH and config are already done.
func updateViaInstaller() error {
	if _, err := exec.LookPath("curl"); err != nil {
		return fmt.Errorf("updating needs curl, which is not on PATH. Install curl, or run:\n  %s", installerCmd)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		return errors.New("updating needs a POSIX shell, which is not on PATH")
	}
	// No --install-dir: install.sh reads ORQ_CLI_INSTALL_DIR itself and the child
	// inherits our environment, so the new binary lands where this one lives.
	script := "curl -fsSL " + installerURL + " | sh -s -- --no-modify-path --no-setup"
	if err := runUpdateCommand("sh", "-c", script); err != nil {
		return fmt.Errorf("the installer failed: %w\nRun it yourself to see the full output:\n  %s", err, installerCmd)
	}
	return nil
}
