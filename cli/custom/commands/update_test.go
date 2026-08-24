package commands

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// updateCmdEnv wires the fake registry and scratch HOME from updateTestEnv,
// then pins the channel and captures whatever the command would have executed
// instead of really running npm or the installer.
func updateCmdEnv(t *testing.T, channel updateChannel, latest string) (stdout *bytes.Buffer, ran *[]string) {
	t.Helper()
	tags := map[string]string{"latest": latest}
	if _, hits := updateTestEnv(t, tags); hits == nil {
		t.Fatal("test env not wired")
	}
	stdout = &bytes.Buffer{}
	origOut, origDetect, origRun := bartolocli.Stdout, detectChannel, runUpdateCommand
	t.Cleanup(func() { bartolocli.Stdout, detectChannel, runUpdateCommand = origOut, origDetect, origRun })
	bartolocli.Stdout = stdout
	detectChannel = func() (updateChannel, string) { return channel, "/somewhere/orq" }
	executed := []string{}
	runUpdateCommand = func(name string, args ...string) error {
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
	_, ran := updateCmdEnv(t, channelInstaller, "4.13.22")
	err := runUpdateCmd(t, "dev")
	if err == nil || !strings.Contains(err.Error(), "rebuilding from source") {
		t.Fatalf("dev build error = %v, want a refusal naming the rebuild", err)
	}
	if len(*ran) != 0 {
		t.Errorf("dev build executed %v, want nothing", *ran)
	}
}

func TestUpdateViaNPMChannel(t *testing.T) {
	stdout, ran := updateCmdEnv(t, channelNPM, "4.13.22")
	stubOnPath(t, "npm")

	if err := runUpdateCmd(t, "4.13.18"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(*ran) != 1 || (*ran)[0] != "npm install -g @orq-ai/cli@latest" {
		t.Fatalf("executed %v, want the npm global install", *ran)
	}
	if got := stdout.String(); !strings.Contains(got, "4.13.18 -> 4.13.22") {
		t.Errorf("output %q does not report the versions", got)
	}
}

func TestUpdateViaNPMWithoutNPMPrintsTheCommand(t *testing.T) {
	_, ran := updateCmdEnv(t, channelNPM, "4.13.22")
	t.Setenv("PATH", t.TempDir()) // no npm here

	err := runUpdateCmd(t, "4.13.18")
	if err == nil || !strings.Contains(err.Error(), "npm install -g @orq-ai/cli@latest") {
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

func TestUpdateViaInstallerChannel(t *testing.T) {
	_, ran := updateCmdEnv(t, channelInstaller, "4.13.22")
	stubOnPath(t, "curl", "sh")

	if err := runUpdateCmd(t, "4.13.18"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(*ran) != 1 {
		t.Fatalf("executed %v, want one installer run", *ran)
	}
	got := (*ran)[0]
	for _, want := range []string{"sh -c", installerURL, "--no-modify-path", "--no-setup"} {
		if !strings.Contains(got, want) {
			t.Errorf("installer command %q missing %q", got, want)
		}
	}
}

func TestUpdateUnknownChannelRefuses(t *testing.T) {
	_, ran := updateCmdEnv(t, channelUnknown, "4.13.22")
	err := runUpdateCmd(t, "4.13.18")
	if err == nil {
		t.Fatal("unknown channel updated silently, want a refusal")
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
	stdout, ran := updateCmdEnv(t, channelInstaller, "4.13.22")
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
	stdout, ran := updateCmdEnv(t, channelInstaller, "4.13.22")
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
	_, _ = updateCmdEnv(t, channelInstaller, "4.13.22")
	stubOnPath(t, "curl", "sh")
	runUpdateCommand = func(string, ...string) error { return errors.New("exit status 1") }
	err := runUpdateCmd(t, "4.13.18")
	if err == nil || !strings.Contains(err.Error(), "installer failed") {
		t.Fatalf("error = %v, want the installer failure surfaced", err)
	}
	if !strings.Contains(err.Error(), installerCmd) {
		t.Errorf("error %q does not tell the user how to run it themselves", err.Error())
	}
}

func TestUpdateHintFollowsChannel(t *testing.T) {
	orig := detectChannel
	t.Cleanup(func() { detectChannel = orig })

	for _, c := range []struct {
		channel updateChannel
		want    string
	}{
		{channelNPM, "orq update"},
		{channelInstaller, "orq update"},
		{channelUnknown, installerCmd},
	} {
		detectChannel = func() (updateChannel, string) { return c.channel, "/somewhere/orq" }
		if got := updateHint(); got != c.want {
			t.Errorf("updateHint() on %s = %q, want %q", c.channel, got, c.want)
		}
	}
}

func TestDetectChannel(t *testing.T) {
	home := t.TempDir()
	origHome, origExec, origDetect := updateHomeDir, osExecutable, detectChannel
	t.Cleanup(func() { updateHomeDir, osExecutable, detectChannel = origHome, origExec, origDetect })
	updateHomeDir = func() (string, error) { return home, nil }
	detectChannel = origDetect

	cases := []struct {
		name       string
		path       string
		installDir string
		want       updateChannel
	}{
		{"npm shim", filepath.Join(home, "n/lib/node_modules/@orq-ai/cli-darwin-arm64/bin/orq"), "", channelNPM},
		{"default install dir", filepath.Join(home, ".orq", "bin", "orq"), "", channelInstaller},
		{"custom install dir", filepath.Join(home, "opt", "orq"), filepath.Join(home, "opt"), channelInstaller},
		{"hand-copied binary", filepath.Join(home, "usr-local-bin", "orq"), "", channelUnknown},
		{"go build output", filepath.Join(home, "src", "orq-cli", "orq"), "", channelUnknown},
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
			if got, _ := detectChannel(); got != c.want {
				t.Errorf("detectChannel() for %s = %s, want %s", c.path, got, c.want)
			}
		})
	}
}
