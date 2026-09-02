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
	// Member counts are right-aligned under the MEMBERS header: the table
	// never pads its last column, so only the caller's own sprintf does it.
	if !strings.Contains(out, "ws1       12") || !strings.Contains(out, "ws2        3") {
		t.Errorf("member counts not right-aligned:\n%s", out)
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
// here misaligns all three at once.
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
			headers: []string{"AGENT", "CAPABILITY", "SCOPE", "LOCATION"},
			rows: []tableRow{
				{marker: "✓", cells: []string{"opencode", "gateway", "", "~/.config/opencode/opencode.json"}},
				{cells: []string{"", "skills", "global", "~/.agents/skills"}},
			},
			want: "     AGENT     CAPABILITY  SCOPE   LOCATION\n" +
				"  ✓  opencode  gateway             ~/.config/opencode/opencode.json\n" +
				"     " + "        " + "  skills      global  ~/.agents/skills\n",
		},
		{
			// A row that is short of its headers must leave a blank column,
			// not panic mid-render; extra cells past the last header go
			// nowhere rather than printing an unaligned phantom column.
			name:    "ragged rows",
			headers: []string{"AGENT", "CAPABILITY", "LOCATION"},
			rows: []tableRow{
				{cells: []string{"opencode"}},
				{cells: []string{"pi", "gateway", "~/.pi", "extra"}},
			},
			want: "     AGENT     CAPABILITY  LOCATION\n" +
				"     opencode              \n" +
				"     pi        gateway     ~/.pi\n",
		},
		{
			// Column widths are measured in runes: measured in bytes, the
			// non-ASCII cell sizes its column too wide and skews every column
			// after it.
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

// The agent name printed once per group is invisible to connect_test.go's
// substring assertions: dropping it leaves that suite green.
func TestPrintWiredTable(t *testing.T) {
	rows := func(targets ...wiredTarget) (order []string, byAgent map[string][]wiredTarget) {
		byAgent = map[string][]wiredTarget{}
		for _, w := range targets {
			if _, seen := byAgent[w.agent]; !seen {
				order = append(order, w.agent)
			}
			byAgent[w.agent] = append(byAgent[w.agent], w)
		}
		return order, byAgent
	}
	cases := []struct {
		name    string
		targets []wiredTarget
		want    string
	}{
		{
			name: "name printed once per agent, no SCOPE column",
			targets: []wiredTarget{
				{agent: "opencode", capability: "gateway", path: "/a", status: "pass"},
				{agent: "opencode", capability: "skills", path: "/b", status: "pass"},
				{agent: "pi", capability: "gateway", path: "/c", status: "pass"},
			},
			want: "     AGENT     CAPABILITY  LOCATION\n" +
				"  ✓  opencode  gateway     /a\n" +
				"  ✓            skills      /b\n" +
				"  ✓  pi        gateway     /c\n",
		},
		{
			name: "SCOPE column appears when any row is scoped",
			targets: []wiredTarget{
				{agent: "claude", capability: "mcp", path: "/a", scope: "local", status: "pass"},
				{agent: "claude", capability: "skills", path: "/b", status: "pass"},
			},
			want: "     AGENT   CAPABILITY  SCOPE  LOCATION\n" +
				"  ✓  claude  mcp         local  /a\n" +
				"  ✓          skills             /b\n",
		},
		{
			name: "warn target renders its own glyph",
			targets: []wiredTarget{
				{agent: "claude", capability: "gateway", path: "/a", status: "pass"},
				{agent: "claude", capability: "skills", path: "/b", status: "warn"},
			},
			want: "     AGENT   CAPABILITY  LOCATION\n" +
				"  ✓  claude  gateway     /a\n" +
				"  !          skills      /b\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			order, byAgent := rows(tc.targets...)
			printWiredTable(&reporter{w: &buf}, order, byAgent)
			if got := buf.String(); got != tc.want {
				t.Errorf("printWiredTable output\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
