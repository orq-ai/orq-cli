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

func TestPrintWorkspaceListAlignsAndMarksActive(t *testing.T) {
	rows := []workspaceRow{
		{Key: "ws1", Name: "Acme", TotalMembers: 12, Active: false},
		{Key: "ws2", Name: "Long Workspace Name", TotalMembers: 3, Active: true},
	}
	out := captureStdout(t, func() { printWorkspaceList(rows, 19) })

	if !strings.Contains(out, "Workspaces") {
		t.Fatalf("missing heading:\n%s", out)
	}
	// The active workspace carries the '*' marker; the inactive one does not.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ws2") && !strings.Contains(line, "*") {
			t.Errorf("active workspace ws2 not marked:\n%s", line)
		}
		if strings.Contains(line, "ws1") && strings.Contains(line, "*") {
			t.Errorf("inactive workspace ws1 wrongly marked:\n%s", line)
		}
	}
	// The short name is padded to the widest name so keys line up.
	if !strings.Contains(out, "Acme               ") {
		t.Errorf("short name not padded to column width:\n%s", out)
	}
	if !strings.Contains(out, "12 members") || !strings.Contains(out, "3 members") {
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
