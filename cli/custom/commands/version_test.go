package commands

import (
	"bytes"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

func TestVersionCommandReportsBothVersions(t *testing.T) {
	orig, origDetect, origHuman := bartolocli.Stdout, detectChannel, humanOutput
	t.Cleanup(func() { bartolocli.Stdout, detectChannel, humanOutput = orig, origDetect, origHuman })
	out := &bytes.Buffer{}
	bartolocli.Stdout = out
	humanOutput = func() bool { return true }
	detectChannel = func() (updateChannel, string) { return channelInstaller, "/somewhere/orq" }
	SetAPIVersion("4.13.22")

	root := &cobra.Command{Use: "orq", Version: "5.0.0"}
	root.AddCommand(NewVersionCommand())
	root.SetArgs([]string{"version"})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatalf("version: %v", err)
	}
	got := out.String()
	for _, want := range []string{"orq version 5.0.0", "4.13.22", "installer"} {
		if !strings.Contains(got, want) {
			t.Errorf("version output %q, want it to contain %q", got, want)
		}
	}
}
