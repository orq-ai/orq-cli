package commands

import (
	"bytes"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// captureStdout swaps bartolocli.Stdout for a buffer for the duration of fn.
// Color is off here because the test process's stdout is not a TTY, so the
// assertions match plain text.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	prev := bartolocli.Stdout
	var buf bytes.Buffer
	bartolocli.Stdout = &buf
	defer func() { bartolocli.Stdout = prev }()
	fn()
	return buf.String()
}

func TestPrintWorkspaceListTable(t *testing.T) {
	rows := []workspaceRow{
		{Key: "ws1", Name: "Acme", TotalMembers: 12, Active: false},
		{Key: "ws2", Name: "Long Workspace Name", TotalMembers: 3, Active: true},
	}
	out := captureStdout(t, func() { printWorkspaceList(rows, 19) })

	if !strings.Contains(out, "Workspaces") {
		t.Fatalf("missing heading:\n%s", out)
	}
	// Column header row is present.
	for _, h := range []string{"NAME", "KEY", "MEMBERS"} {
		if !strings.Contains(out, h) {
			t.Errorf("missing column header %q:\n%s", h, out)
		}
	}
	// The active workspace carries the '●' marker; the inactive one does not.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ws2") && !strings.Contains(line, "●") {
			t.Errorf("active workspace ws2 not marked:\n%s", line)
		}
		if strings.Contains(line, "ws1") && strings.Contains(line, "●") {
			t.Errorf("inactive workspace ws1 wrongly marked:\n%s", line)
		}
	}
	// The short name is padded to the widest name so the KEY column lines up.
	if !strings.Contains(out, "Acme               ") {
		t.Errorf("short name not padded to column width:\n%s", out)
	}
	// Member counts are right-aligned under the MEMBERS header.
	if !strings.Contains(out, "12") || !strings.Contains(out, "3") {
		t.Errorf("member counts missing:\n%s", out)
	}
}

func TestKVPadsLabelColumn(t *testing.T) {
	out := captureStdout(t, func() {
		kv(9, "name", "Karina")
		kv(9, "workspace", "Acme (ws1)")
	})
	// "name:" padded to the same width as "workspace:" so values align.
	if !strings.Contains(out, "name:      ") {
		t.Errorf("label not padded:\n%q", out)
	}
	if !strings.Contains(out, "Karina") || !strings.Contains(out, "Acme (ws1)") {
		t.Errorf("values missing:\n%s", out)
	}
}
