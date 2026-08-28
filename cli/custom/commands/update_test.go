package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// updateCmdEnv wires the fake registry and scratch HOME from updateTestEnv,
// then pins the install method and captures whatever the command would have executed
// instead of really running npm or the installer.
func updateCmdEnv(t *testing.T, method installMethod, latest string) (stdout *bytes.Buffer, ran *[]string) {
	t.Helper()
	return updateCmdEnvTags(t, method, map[string]string{"latest": latest})
}

func updateCmdEnvTags(t *testing.T, method installMethod, tags map[string]string) (stdout *bytes.Buffer, ran *[]string) {
	t.Helper()
	if _, hits := updateTestEnv(t, tags); hits == nil {
		t.Fatal("test env not wired")
	}
	stdout = &bytes.Buffer{}
	origOut, origDetect, origRun := bartolocli.Stdout, detectInstallMethod, runUpdateCommand
	t.Cleanup(func() { bartolocli.Stdout, detectInstallMethod, runUpdateCommand = origOut, origDetect, origRun })
	bartolocli.Stdout = stdout
	detectInstallMethod = func() (installMethod, string) { return method, "/somewhere/orq" }
	executed := []string{}
	runUpdateCommand = func(_ context.Context, name string, args ...string) error {
		executed = append(executed, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	return stdout, &executed
}

func runUpdateCmd(t *testing.T, version string, args ...string) error {
	t.Helper()
	root := &cobra.Command{Use: "orq", Version: version}
	cmd := NewUpdateCommand()
	root.AddCommand(cmd)
	root.SetArgs(append([]string{"update"}, args...))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	return root.Execute()
}

func TestUpdateRefusesDevBuild(t *testing.T) {
	_, ran := updateCmdEnv(t, methodInstaller, "4.13.22")
	err := runUpdateCmd(t, "dev")
	if err == nil || !strings.Contains(err.Error(), "rebuilding from source") {
		t.Fatalf("dev build error = %v, want a refusal naming the rebuild", err)
	}
	if len(*ran) != 0 {
		t.Errorf("dev build executed %v, want nothing", *ran)
	}
}

func TestUpdateViaNPMInstallMethod(t *testing.T) {
	stdout, ran := updateCmdEnv(t, methodNPM, "4.13.22")
	stubOnPath(t, "npm")

	if err := runUpdateCmd(t, "4.13.18"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(*ran) != 1 || (*ran)[0] != "npm install -g @orq-ai/cli@4.13.22" {
		t.Fatalf("executed %v, want the npm global install", *ran)
	}
	if got := stdout.String(); !strings.Contains(got, "4.13.18 -> 4.13.22") {
		t.Errorf("output %q does not report the versions", got)
	}
}

func TestUpdateViaNPMWithoutNPMPrintsTheCommand(t *testing.T) {
	_, ran := updateCmdEnv(t, methodNPM, "4.13.22")
	t.Setenv("PATH", t.TempDir()) // no npm here

	err := runUpdateCmd(t, "4.13.18")
	if err == nil || !strings.Contains(err.Error(), "npm install -g @orq-ai/cli@4.13.22") {
		t.Fatalf("error = %v, want the npm command printed for the user to run", err)
	}
	if len(*ran) != 0 {
		t.Errorf("executed %v without npm, want nothing", *ran)
	}
}

// stubOnPath puts an executable of the given name on a PATH containing nothing
// else, so LookPath finds it and the real tool is never a factor.
func stubOnPath(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func TestUpdateViaInstallerInstallMethod(t *testing.T) {
	_, ran := updateCmdEnv(t, methodInstaller, "4.13.22")
	stubOnPath(t, "curl", "sh")

	if err := runUpdateCmd(t, "4.13.18"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(*ran) != 2 {
		t.Fatalf("executed %v, want a download then a run - a curl|sh pipeline hides a failed download", *ran)
	}
	if download := (*ran)[0]; !strings.HasPrefix(download, "curl ") || !strings.Contains(download, installerURL) {
		t.Errorf("first command %q is not a download of the installer", download)
	}
	for _, want := range []string{"--no-modify-path", "--no-setup", "--version v4.13.22"} {
		if !strings.Contains((*ran)[1], want) {
			t.Errorf("installer run %q missing %q", (*ran)[1], want)
		}
	}
	if strings.Contains((*ran)[1], "|") {
		t.Errorf("installer run %q still pipes; curl's exit status would be lost", (*ran)[1])
	}
}

func TestUpdateAbortsWhenTheInstallerCannotBeDownloaded(t *testing.T) {
	_, ran := updateCmdEnv(t, methodInstaller, "4.13.22")
	stubOnPath(t, "curl", "sh")
	runUpdateCommand = func(_ context.Context, name string, args ...string) error {
		*ran = append(*ran, name)
		if name == "curl" {
			return errors.New("exit status 22")
		}
		return nil
	}

	err := runUpdateCmd(t, "4.13.18")
	if err == nil || !strings.Contains(err.Error(), "cannot download the installer") {
		t.Fatalf("error = %v, want the download failure surfaced", err)
	}
	for _, name := range *ran {
		if name == "sh" {
			t.Fatal("ran the installer after the download failed")
		}
	}
}

func TestUpdateInstallsTheRCLineForAnRCBuild(t *testing.T) {
	_, ran := updateCmdEnvTags(t, methodNPM, map[string]string{"latest": "4.13.22", "rc": "4.14.0-rc.48"})
	stubOnPath(t, "npm")

	if err := runUpdateCmd(t, "4.14.0-rc.47"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(*ran) != 1 || !strings.HasSuffix((*ran)[0], "@4.14.0-rc.48") {
		t.Fatalf("executed %v, want the rc version installed, not the older stable", *ran)
	}
}

func TestUpdateUnknownInstallMethodRefuses(t *testing.T) {
	_, ran := updateCmdEnv(t, methodUnknown, "4.13.22")
	err := runUpdateCmd(t, "4.13.18")
	if err == nil {
		t.Fatal("unknown install method updated silently, want a refusal")
	}
	for _, want := range []string{"/somewhere/orq", "npm install -g", "install.sh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
	if len(*ran) != 0 {
		t.Errorf("executed %v after refusing, want nothing", *ran)
	}
}

func TestUpdateUpToDateDoesNothing(t *testing.T) {
	stdout, ran := updateCmdEnv(t, methodInstaller, "4.13.22")
	if err := runUpdateCmd(t, "4.13.22"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(*ran) != 0 {
		t.Errorf("executed %v while already current, want nothing", *ran)
	}
	if got := stdout.String(); !strings.Contains(got, "already the latest") {
		t.Errorf("output %q does not say it is current", got)
	}
}

func TestUpdateCheckReportsWithoutInstalling(t *testing.T) {
	stdout, ran := updateCmdEnv(t, methodInstaller, "4.13.22")
	if err := runUpdateCmd(t, "4.13.18", "--check"); err != nil {
		t.Fatalf("update --check: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("--check executed %v, want nothing", *ran)
	}
	got := stdout.String()
	for _, want := range []string{"4.13.18", "installer", "4.13.22", "orq update"} {
		if !strings.Contains(got, want) {
			t.Errorf("--check output %q missing %q", got, want)
		}
	}
}

func TestUpdatePropagatesInstallerFailure(t *testing.T) {
	_, _ = updateCmdEnv(t, methodInstaller, "4.13.22")
	stubOnPath(t, "curl", "sh")
	runUpdateCommand = func(_ context.Context, name string, _ ...string) error {
		if name == "sh" {
			return errors.New("exit status 1")
		}
		return nil
	}
	err := runUpdateCmd(t, "4.13.18")
	if err == nil || !strings.Contains(err.Error(), "installer failed") {
		t.Fatalf("error = %v, want the installer failure surfaced", err)
	}
	if !strings.Contains(err.Error(), installerCmd) {
		t.Errorf("error %q does not tell the user how to run it themselves", err.Error())
	}
}

func TestUpdateHintFollowsInstallMethod(t *testing.T) {
	orig := detectInstallMethod
	t.Cleanup(func() { detectInstallMethod = orig })

	for _, c := range []struct {
		method installMethod
		want   string
	}{
		{methodNPM, "orq update"},
		{methodInstaller, "orq update"},
		{methodUnknown, installerCmd},
	} {
		detectInstallMethod = func() (installMethod, string) { return c.method, "/somewhere/orq" }
		if got := updateHint(); got != c.want {
			t.Errorf("updateHint() on %s = %q, want %q", c.method, got, c.want)
		}
	}
}

func TestDetectInstallMethod(t *testing.T) {
	home := t.TempDir()
	origHome, origExec, origDetect := updateHomeDir, osExecutable, detectInstallMethod
	t.Cleanup(func() { updateHomeDir, osExecutable, detectInstallMethod = origHome, origExec, origDetect })
	updateHomeDir = func() (string, error) { return home, nil }
	detectInstallMethod = origDetect

	cases := []struct {
		name       string
		path       string
		installDir string
		want       installMethod
	}{
		{"npm shim", filepath.Join(home, "n/lib/node_modules/@orq-ai/cli-darwin-arm64/bin/orq"), "", methodNPM},
		{"default install dir", filepath.Join(home, ".orq", "bin", "orq"), "", methodInstaller},
		{"custom install dir", filepath.Join(home, "opt", "orq"), filepath.Join(home, "opt"), methodInstaller},
		{"hand-copied binary", filepath.Join(home, "usr-local-bin", "orq"), "", methodUnknown},
		{"go build output", filepath.Join(home, "src", "orq-cli", "orq"), "", methodUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(c.path, []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ORQ_CLI_INSTALL_DIR", c.installDir)
			osExecutable = func() (string, error) { return c.path, nil }
			if got, _ := detectInstallMethod(); got != c.want {
				t.Errorf("detectInstallMethod() for %s = %s, want %s", c.path, got, c.want)
			}
		})
	}
}

func TestUpdateCheckMachineOutputCarriesUpdateAvailable(t *testing.T) {
	stdout, _ := updateCmdEnv(t, methodInstaller, "4.13.22")
	ensureFormatter(t)
	origHuman := humanOutput
	t.Cleanup(func() { humanOutput = origHuman })
	humanOutput = func() bool { return false } // a script, not a terminal

	if err := runUpdateCmd(t, "4.13.18", "--check"); err != nil {
		t.Fatalf("update --check: %v", err)
	}
	got := stdout.String()
	for _, want := range []string{"install_method", "update_available", "true", "4.13.22", "installer"} {
		if !strings.Contains(got, want) {
			t.Errorf("machine-format payload %q missing %q", got, want)
		}
	}
	if strings.Contains(got, `"channel"`) {
		t.Errorf("machine-format payload %q still carries the old channel key", got)
	}
	if strings.Contains(got, "Run 'orq update'") {
		t.Errorf("machine-format payload %q carries the human sentence", got)
	}
}

func TestUpdateFailureLeavesTheNoticeArmed(t *testing.T) {
	_, _ = updateCmdEnv(t, methodInstaller, "4.13.22")
	stubOnPath(t, "curl", "sh")
	runUpdateCommand = func(context.Context, string, ...string) error { return errors.New("exit status 1") }

	if err := runUpdateCmd(t, "4.13.18"); err == nil {
		t.Fatal("expected the update to fail")
	}
	if readUpdateCache("4.13.18") != nil {
		t.Error("a failed update wrote the cache, silencing the notice for 24h about an update that never happened")
	}
}

func TestUpdateCommandSuppressesItsOwnNotice(t *testing.T) {
	stderr, _ := updateTestEnv(t, map[string]string{"latest": "4.13.22"})
	root := &cobra.Command{Use: "orq", Version: "4.13.18"}
	root.AddCommand(NewUpdateCommand())

	MaybePrintUpdateNotice(root.Commands()[0])
	if got := stderr.String(); got != "" {
		t.Errorf("orq update printed the passive notice too: %q", got)
	}
}
