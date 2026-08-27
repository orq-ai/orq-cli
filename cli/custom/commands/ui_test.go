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
	out := captureStdout(t, func() { printWorkspaceList(rows) })

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

// printTable is the one renderer behind doctor, workspace list and connect
// --status, so its output is pinned byte for byte: an alignment regression in
// here misaligns all three at once, and substring assertions never see it.
func TestPrintTableGoldens(t *testing.T) {
	cases := []struct {
		name    string
		headers []string
		rows    []tableRow
		want    string
	}{
		{name: "no headers", want: ""},
		{
			name:    "headers only",
			headers: []string{"AGENT", "LOCATION"},
			want:    "     AGENT  LOCATION\n",
		},
		{
			name:    "columns grow past their headers",
			headers: []string{"AGENT", "CAPABILITY", "LOCATION"},
			rows: []tableRow{
				{marker: "✓", cells: []string{"opencode", "gateway", "~/.config/opencode/opencode.json"}},
				{cells: []string{"", "skills", "~/.agents/skills"}},
			},
			want: "     AGENT     CAPABILITY  LOCATION\n" +
				"  ✓  opencode  gateway     ~/.config/opencode/opencode.json\n" +
				"     " + "        " + "  skills      ~/.agents/skills\n",
		},
		{
			// Padding counts runes, not bytes: a byte count over-pads any
			// non-ASCII cell and skews every column after it.
			name:    "multi-byte cells",
			headers: []string{"NAME", "KEY"},
			rows: []tableRow{
				{marker: "●", cells: []string{"Ünïcodé", "ws1"}},
				{cells: []string{"Acme", "ws2"}},
			},
			want: "     NAME     KEY\n" +
				"  ●  Ünïcodé  ws1\n" +
				"     Acme     ws2\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printTable(&buf, tc.headers, tc.rows)
			if got := buf.String(); got != tc.want {
				t.Errorf("printTable output\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
