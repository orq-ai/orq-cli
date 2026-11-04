package commands

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// stub is a bartolo-shaped command: it emits one record through the formatter.
func stub(name string, rec map[string]any, err error) *cobra.Command {
	return &cobra.Command{Use: name, RunE: func(*cobra.Command, []string) error {
		if err != nil {
			return err
		}
		return bartolocli.Formatter.Format(rec)
	}}
}

// runHumanized wraps cmd and runs it on a terminal or a pipe, returning stdout
// and what reached the real formatter.
func runHumanized(t *testing.T, cmd *cobra.Command, render func(record), human bool) (stdout string, emitted any, err error) {
	t.Helper()
	origHuman, origOut, origFmt := humanOutput, bartolocli.Stdout, bartolocli.Formatter
	t.Cleanup(func() { humanOutput, bartolocli.Stdout, bartolocli.Formatter = origHuman, origOut, origFmt })
	t.Setenv("NO_COLOR", "1")
	humanOutput = func() bool { return human }
	out := &bytes.Buffer{}
	bartolocli.Stdout = out
	real := &recorder{}
	bartolocli.Formatter = real

	humanize(cmd, render)
	err = cmd.RunE(cmd, nil)
	if bartolocli.Formatter != real {
		t.Error("Formatter was not restored")
	}
	return out.String(), real.got, err
}

func TestHumanizeRendersOnATerminalAndKeepsTheRecordOnAPipe(t *testing.T) {
	render := func(rec record) { _, _ = bartolocli.Stdout.Write([]byte("Done " + rec.str("x") + ".\n")) }

	out, emitted, err := runHumanized(t, stub("c", map[string]any{"x": "1"}, nil), render, true)
	if err != nil || out != "Done 1.\n" || emitted != nil {
		t.Errorf("terminal: out=%q emitted=%v err=%v; want the sentence and no record", out, emitted, err)
	}
	out, emitted, err = runHumanized(t, stub("c", map[string]any{"x": "1"}, nil), render, false)
	if err != nil || out != "" || emitted == nil {
		t.Errorf("pipe: out=%q emitted=%v err=%v; want bartolo's record untouched", out, emitted, err)
	}
}

func TestHumanizeFailureKeepsTheErrorAndSaysNothing(t *testing.T) {
	render := func(record) { t.Error("rendered after a failed command") }
	out, _, err := runHumanized(t, stub("c", nil, errors.New("boom")), render, true)
	if err == nil || out != "" {
		t.Errorf("err=%v out=%q; want the error and no output", err, out)
	}
}

func TestServerViews(t *testing.T) {
	t.Cleanup(func() { auth.SetServer("", auth.SourceDefault) })
	auth.SetServer("https://self.example.com", auth.SourceEnv)

	out, _, _ := runHumanized(t, stub("current", map[string]any{"server": "https://self.example.com"}, nil), printServerCurrent, true)
	for _, want := range []string{"https://self.example.com", "ORQ_SERVER"} {
		if !strings.Contains(out, want) {
			t.Errorf("current = %q, missing %q", out, want)
		}
	}

	list := map[string]any{"selected_server": "https://self.example.com", "servers": []map[string]any{
		{"index": 0, "url": "https://my.orq.ai", "description": "", "selected": false},
	}}
	out, _, _ = runHumanized(t, stub("list", list, nil), printServerList, true)
	if !strings.Contains(out, "is not in this list (ORQ_SERVER)") {
		t.Errorf("list = %q, want the off-list note naming the source", out)
	}

	if got := overrideNote(); !strings.Contains(got, "ORQ_SERVER still decides this call's host (https://self.example.com)") {
		t.Errorf("overrideNote = %q", got)
	}
	auth.SetServer("https://x.example.com", auth.SourceConfig)
	if got := overrideNote(); got != "" {
		t.Errorf("overrideNote with only a saved default = %q, want none", got)
	}
}

func TestProfileViews(t *testing.T) {
	out, _, _ := runHumanized(t, stub("list", map[string]any{"profiles": []map[string]any{}, "message": "x"}, nil), printProfileList, true)
	if !strings.Contains(out, "orq auth profile add") {
		t.Errorf("empty list = %q, want the command that adds a profile", out)
	}

	list := map[string]any{"profiles": []any{
		map[string]any{"name": "b", "active": false, "api_key": "sk-o****08f7"},
		map[string]any{"name": "a", "active": true, "server": "https://aim.orq.ai", "api_key": "eyJh****kIIo"},
	}}
	out, _, _ = runHumanized(t, stub("list", list, nil), printProfileList, true)
	if !strings.Contains(out, "✓  a ") || strings.Index(out, "  a ") > strings.Index(out, "  b ") || !strings.Contains(out, "sk-o****08f7") {
		t.Errorf("list = %q, want sorted rows, the active marker and the masked key", out)
	}

	cur := map[string]any{"active_profile": "ghost", "source": "flag", "exists": false, "profile_server": ""}
	out, _, _ = runHumanized(t, stub("current", cur, nil), printProfileCurrent, true)
	for _, want := range []string{"ghost", "--profile flag", `No profile named "ghost"`} {
		if !strings.Contains(out, want) {
			t.Errorf("current = %q, missing %q", out, want)
		}
	}
	out, _, _ = runHumanized(t, stub("current", map[string]any{"active_profile": "", "source": "none"}, nil), printProfileCurrent, true)
	if !strings.Contains(out, "No profile active") {
		t.Errorf("current with none = %q", out)
	}
}
