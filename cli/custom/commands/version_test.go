package commands

import (
	"bytes"
	"reflect"
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
	SetAPIVersion("4.13.22")
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
	SetAPIVersion("4.13.22")
	if err := root.Execute(); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if got := out.String(); got != "orq version 5.0.0\n" {
		t.Fatalf("--version printed %q, want only the CLI version", got)
	}
}

// `--json` on stdout is the machine contract, so the key set is pinned: a
// renamed or dropped key is a breaking change and has to show up as a failing
// test rather than in someone's broken script.
func TestVersionCommandJSONShape(t *testing.T) {
	origFormatter, origDetect, origHuman, origAPI := bartolocli.Formatter, detectChannel, humanOutput, apiVersion
	t.Cleanup(func() {
		bartolocli.Formatter, detectChannel, humanOutput, apiVersion = origFormatter, origDetect, origHuman, origAPI
	})
	captured := &capturingFormatter{}
	bartolocli.Formatter = captured
	humanOutput = func() bool { return false }
	detectChannel = func() (updateChannel, string) { return channelInstaller, "/somewhere/orq" }
	root := &cobra.Command{Use: "orq", Version: "5.0.0"}
	SetAPIVersion("4.13.22")
	root.AddCommand(NewVersionCommand())
	root.SetArgs([]string{"version"})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("version: %v", err)
	}
	want := map[string]any{"cli": "5.0.0", "api": "4.13.22", "channel": "installer"}
	if !reflect.DeepEqual(captured.value, want) {
		t.Fatalf("version --json = %#v, want %#v", captured.value, want)
	}
}

type capturingFormatter struct{ value any }

func (c *capturingFormatter) Format(v any) error {
	c.value = v
	return nil
}
