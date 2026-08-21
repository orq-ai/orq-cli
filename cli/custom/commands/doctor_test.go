package commands

import (
	"strings"
	"testing"
)

// The human view keeps one row per fault and one coding_agents summary;
// healthy per-agent rows only exist in --json. The predicate is the status,
// never the message.
func TestDoctorSummaryCollapsesHealthyAgentRows(t *testing.T) {
	checks := []doctorCheck{
		{ID: "session_file", Status: "pass", Message: "Session file loaded"},
		{ID: "coding_agent_claude", Status: "pass", Message: "Claude Code is wired to orq"},
		{ID: "coding_agent_kimi", Status: "info", Message: "Kimi Code detected but not wired"},
		{ID: "coding_agent_opencode", Status: "warn", Message: "opencode is partially wired"},
		{ID: "coding_agents", Status: "info", Message: "1 of 3 wired: claude"},
	}
	out := captureStdout(t, func() { printDoctorSummary("authenticated", "u@x.dev", checks) })

	for _, dropped := range []string{"coding_agent_claude", "coding_agent_kimi"} {
		if strings.Contains(out, dropped) {
			t.Errorf("healthy row %s survived the collapse:\n%s", dropped, out)
		}
	}
	for _, kept := range []string{"coding_agent_opencode", "coding_agents", "session_file"} {
		if !strings.Contains(out, kept) {
			t.Errorf("row %s missing:\n%s", kept, out)
		}
	}

	// The column adapts to the longest printed ID: every message starts where
	// the header's RESULT does, including on the 21-rune opencode row.
	lines := strings.Split(out, "\n")
	resultCol := strings.Index(lines[0], "RESULT")
	if resultCol < 0 {
		t.Fatalf("no header row:\n%s", out)
	}
	for _, want := range []struct{ id, msg string }{
		{"coding_agent_opencode", "opencode is partially wired"},
		{"coding_agents", "1 of 3 wired: claude"},
	} {
		for _, line := range lines {
			if !strings.Contains(line, want.msg) {
				continue
			}
			if got := strings.Index(line, want.msg); got != resultCol {
				t.Errorf("%s message starts at column %d, header RESULT at %d:\n%s", want.id, got, resultCol, out)
			}
		}
	}
}

func TestCodingAgentsSummaryStates(t *testing.T) {
	if c := codingAgentsSummary(2, 0, nil); c.Status != "info" || !strings.Contains(c.Message, "0 of 2 wired — run 'orq connect'") {
		t.Errorf("none wired: %+v", c)
	}
	if c := codingAgentsSummary(3, 2, []string{"claude", "kimi"}); c.Status != "info" || !strings.Contains(c.Message, "2 of 3 wired: claude, kimi") {
		t.Errorf("some wired: %+v", c)
	}
	if c := codingAgentsSummary(2, 2, []string{"claude", "kimi"}); c.Status != "pass" || !strings.Contains(c.Message, "2 of 2 wired: claude, kimi") {
		t.Errorf("all wired: %+v", c)
	}
}
