package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// lookPath is the seam tests replace so a preflight can be told a binary is
// missing without touching the real PATH.
var lookPath = exec.LookPath

// installerSpec is one fetch-and-run of a POSIX shell installer: `orq update`
// re-running orq's own install.sh, and `orq orqi` running orqi's.
type installerSpec struct {
	URL        string            // the script to download
	Args       []string          // arguments passed to `sh <script>`
	Env        map[string]string // added to the child's environment
	Needs      []string          // binaries the script needs on PATH
	TempPrefix string            // os.MkdirTemp prefix, for a recognisable stray dir
	Subject    string            // what is being done, for the PATH error: "updating"
	Manual     string            // the one-liner to run by hand when this fails
}

// runShellInstaller downloads an installer to a temp file and runs it.
//
// Downloaded to a file and run in two steps rather than `curl | sh`: a pipeline
// reports only the shell's status, so a failed download would feed an empty
// script to sh and be indistinguishable from success. Both installers need
// that, and both need the same preflight and the same cleanup — they were two
// hand-kept copies until the second one grew a `tar` check the first never
// heard about.
//
// curl rather than Go's http client, so every download this CLI makes has one
// proxy and cert-store story, including the tarball the script itself fetches.
//
// run is the caller's own seam, which owns the child's stdio and environment
// policy: `orq orqi` keeps orq's credentials away from a script it fetches
// unpinned from a third-party main branch, and `orq update` does not, because
// the script it runs is this project's own.
func runShellInstaller(ctx context.Context, run func(context.Context, map[string]string, string, ...string) error, spec installerSpec) error {
	for _, bin := range spec.Needs {
		if _, err := lookPath(bin); err != nil {
			return fmt.Errorf("%s needs %s, which is not on PATH. Install it, or run:\n  %s", spec.Subject, bin, spec.Manual)
		}
	}
	dir, err := os.MkdirTemp("", spec.TempPrefix)
	if err != nil {
		return fmt.Errorf("cannot create a temporary directory for the installer: %w", err)
	}
	// Before either command, so it covers a failed download as well as a
	// failed install.
	defer os.RemoveAll(dir)

	script := filepath.Join(dir, "install.sh")
	if err := run(ctx, nil, "curl", "-fsSL", "-o", script, spec.URL); err != nil {
		return fmt.Errorf("cannot download the installer from %s: %w", spec.URL, err)
	}
	if err := run(ctx, spec.Env, "sh", append([]string{script}, spec.Args...)...); err != nil {
		return fmt.Errorf("the installer failed: %w\nRun it yourself to see the full output:\n  %s", err, spec.Manual)
	}
	return nil
}
