package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// An explicit `orq update` may wait longer than the passive notice: the user
// asked for this and is watching it happen.
const updateFetchTimeout = 10 * time.Second

// Overridable for tests, which must not shell out to the real npm or run the
// real installer.
//
// The child's stdout is routed to stderr: npm's and install.sh's progress is
// diagnostics, while stdout carries this command's own result, which `--json`
// promises is parseable.
var runUpdateCommand = func(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = bartolocli.Stderr, bartolocli.Stderr
	return cmd.Run()
}

func NewUpdateCommand() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the CLI to the latest published version",
		Long: "Replace this binary with the latest published release, using the " +
			"install method it was installed through: npm, or install.sh. --check reports " +
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
	installMethod, path := detectInstallMethod()

	ctx, cancel := context.WithTimeout(cmd.Context(), updateFetchTimeout)
	latest, err := fetchLatestVersion(ctx, current)
	cancel()
	if err != nil {
		return fmt.Errorf("cannot reach the npm registry to find the latest version: %w", err)
	}
	available := updateAvailable(current, latest)

	// Only a check refreshes the cache. An attempted update must not: a failed
	// one would silence the notice for 24h about the version it failed to
	// install, and a successful one leaves a binary whose version no longer
	// matches the entry anyway.
	if checkOnly || !available {
		writeUpdateCache(current, latest)
	}
	if checkOnly {
		return reportUpdateCheck(cmd, current, latest, installMethod, available)
	}
	if !available {
		if wantsHumanView(cmd) {
			fmt.Fprintf(bartolocli.Stdout, "orq %s is already the latest version.\n", current)
			return nil
		}
		return emit(map[string]any{"install_method": string(installMethod), "from": current, "to": current, "updated": false})
	}

	// Both install methods install this exact version rather than resolving
	// "newest" a second time from their own source. The version reported here is then the
	// version that lands, and an rc build is not silently swapped for the older
	// stable release that "latest" means to npm and to install.sh.
	switch installMethod {
	case methodNPM:
		err = updateViaNPM(cmd.Context(), latest)
	case methodInstaller:
		err = updateViaInstaller(cmd.Context(), latest)
	default:
		return fmt.Errorf("cannot update: this binary was not installed by install.sh or npm (found at %s)\n"+
			"  npm install:       npm install -g %s@latest\n"+
			"  install.sh:        %s", path, npmPackage, installerCmd)
	}
	if err != nil {
		return err
	}
	if !wantsHumanView(cmd) {
		return emit(map[string]any{"install_method": string(installMethod), "from": current, "to": latest, "updated": true})
	}
	fmt.Fprintf(bartolocli.Stdout, "\nUpdated orq %s -> %s\n  Release notes: https://github.com/orq-ai/orq-cli/releases/tag/v%s\n", current, latest, latest)
	return nil
}

func reportUpdateCheck(cmd *cobra.Command, current, latest string, method installMethod, available bool) error {
	if !wantsHumanView(cmd) {
		return emit(map[string]any{
			"install_method":   string(method),
			"current":          current,
			"latest":           latest,
			"update_available": available,
		})
	}
	if available {
		fmt.Fprintf(bartolocli.Stdout, "orq %s (%s); latest %s. Run 'orq update'\n", current, method, latest)
	} else {
		fmt.Fprintf(bartolocli.Stdout, "orq %s (%s); up to date\n", current, method)
	}
	return nil
}

// updateViaNPM hands the work to npm: the package manager owns those files, and
// writing into node_modules behind its back is how a global install becomes
// unrepairable. When npm cannot do it, print the command rather than guessing.
func updateViaNPM(ctx context.Context, version string) error {
	target := npmPackage + "@" + version
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("this binary was installed with npm, but npm is not on PATH. Run:\n  npm install -g %s", target)
	}
	if err := runUpdateCommand(ctx, "npm", "install", "-g", target); err != nil {
		return fmt.Errorf("npm install failed: %w\nA global install needs write access to npm's prefix; run it yourself, with sudo if that is how your npm is set up:\n  npm install -g %s", err, target)
	}
	return nil
}

// updateViaInstaller re-runs install.sh, which already resolves the release,
// verifies its published .sha256 and swaps the binary in atomically.
// Reimplementing that here would be a second copy of the download-and-verify
// path that could drift from the one every install already goes through.
//
// Downloaded to a file and run in two steps rather than `curl | sh`: a pipeline
// reports only the shell's status, so a failed download would feed an empty
// script to sh and be indistinguishable from a successful update.
// ponytail: the installer is the update mechanism; --no-modify-path/--no-setup
// keep it to the one job, since PATH and config are already done.
func updateViaInstaller(ctx context.Context, version string) error {
	for _, bin := range []string{"curl", "sh"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("updating needs %s, which is not on PATH. Install it, or run:\n  %s", bin, installerCmd)
		}
	}
	dir, err := os.MkdirTemp("", "orq-update-")
	if err != nil {
		return fmt.Errorf("cannot create a temporary directory for the installer: %w", err)
	}
	defer os.RemoveAll(dir)

	script := filepath.Join(dir, "install.sh")
	if err := runUpdateCommand(ctx, "curl", "-fsSL", "-o", script, installerURL); err != nil {
		return fmt.Errorf("cannot download the installer from %s: %w", installerURL, err)
	}
	// install.sh reads ORQ_CLI_INSTALL_DIR itself and the child inherits our
	// environment, so a custom install dir needs no argument here.
	if err := runUpdateCommand(ctx, "sh", script, "--no-modify-path", "--no-setup", "--version", "v"+version); err != nil {
		return fmt.Errorf("the installer failed: %w\nRun it yourself to see the full output:\n  %s", err, installerCmd)
	}
	return nil
}
