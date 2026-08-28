package commands

import (
	"bytes"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

func TestVersionCommandReportsBothVersions(t *testing.T) {
	orig, origDetect, origHuman, origAPI := bartolocli.Stdout, detectChannel, humanOutput, apiVersion
	t.Cleanup(func() {
		bartolocli.Stdout, detectChannel, humanOutput, apiVersion = orig, origDetect, origHuman, origAPI
	})
	out := &bytes.Buffer{}
	bartolocli.Stdout = out
	humanOutput = func() bool { return true }
	detectChannel = func() (updateChannel, string) { return channelInstaller, "/somewhere/orq" }
	root := &cobra.Command{Use: "orq", Version: "5.0.0"}
	SetAPIVersion(root, "4.13.22")
	root.AddCommand(NewVersionCommand())
	root.SetArgs([]string{"version"})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("version: %v", err)
	}
	got := out.String()
	for _, want := range []string{"orq version 5.0.0", "built against orq API 4.13.22", "installer"} {
		if !strings.Contains(got, want) {
			t.Errorf("version output %q, want it to contain %q", got, want)
		}
	}
}

func TestVersionFlagPrintsOnlyCLIVersion(t *testing.T) {
	orig := apiVersion
	t.Cleanup(func() { apiVersion = orig })
	out := &bytes.Buffer{}
	root := &cobra.Command{Use: "orq", Version: "5.0.0"}
	root.SetOut(out)
	root.SetArgs([]string{"--version"})
	SetAPIVersion(root, "4.13.22")
	if err := root.Execute(); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if got := out.String(); got != "orq version 5.0.0\n" {
		t.Fatalf("--version printed %q, want only the CLI version", got)
	}
}
