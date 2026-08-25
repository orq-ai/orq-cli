package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orq/cli/custom/skills"
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

// refresh converges the skill set, not the files: a skill the user deleted by
// hand stays deleted, because silently recreating it on the next `orq --help`
// would fight the user. doctor is where that state gets named.
func TestSkillsCheck(t *testing.T) {
	newManifest := func(t *testing.T, dir string, present, absent int) {
		t.Helper()
		m := &skills.Manifest{Version: 1, Fingerprint: skills.Fingerprint()}
		for i := 0; i < present; i++ {
			p := filepath.Join(dir, fmt.Sprintf("orq-present-%d", i))
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			m.AddLink(skills.Link{Path: p, Agent: "claude", Skill: filepath.Base(p)})
		}
		for i := 0; i < absent; i++ {
			p := filepath.Join(dir, fmt.Sprintf("orq-absent-%d", i))
			m.AddLink(skills.Link{Path: p, Agent: "claude", Skill: filepath.Base(p)})
		}
		if err := skills.SaveManifest(m); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("no manifest says nothing", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		if _, ok := skillsCheck(); ok {
			t.Error("reported a check on a machine that never connected")
		}
	})

	t.Run("all present passes", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".claude", "skills")
		newManifest(t, dir, 2, 0)
		check, ok := skillsCheck()
		if !ok || check.Status != "pass" {
			t.Fatalf("got ok=%v status=%q, want a pass", ok, check.Status)
		}
	})

	t.Run("missing links warn with the remedy", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".claude", "skills")
		newManifest(t, dir, 1, 2)
		check, ok := skillsCheck()
		if !ok || check.Status != "warn" {
			t.Fatalf("got ok=%v status=%q, want a warn", ok, check.Status)
		}
		if !strings.Contains(check.Message, "orq connect skills") {
			t.Errorf("message names no remedy: %q", check.Message)
		}
		if check.Details["missing"] != 2 || check.Details["recorded"] != 3 {
			t.Errorf("details = %v, want missing=2 recorded=3", check.Details)
		}
	})

	t.Run("an install from an older CLI is stale", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".claude", "skills")
		newManifest(t, dir, 2, 0)
		m, err := skills.LoadManifest()
		if err != nil || m == nil {
			t.Fatal(err)
		}
		m.Fingerprint = "an-older-release"
		if err := skills.SaveManifest(m); err != nil {
			t.Fatal(err)
		}
		check, ok := skillsCheck()
		if !ok || check.Status != "warn" {
			t.Fatalf("got ok=%v status=%q, want a warn", ok, check.Status)
		}
		if !strings.Contains(check.Message, "older CLI version") {
			t.Errorf("message does not say the install is stale: %q", check.Message)
		}
	})

	t.Run("session links are not breakage", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		m := &skills.Manifest{Version: 1}
		m.AddLink(skills.Link{Path: filepath.Join(home, ".claude", "skills", "orq-x"), Skill: "orq-x", Session: true})
		if err := skills.SaveManifest(m); err != nil {
			t.Fatal(err)
		}
		if _, ok := skillsCheck(); ok {
			t.Error("a session link between launches was reported as a problem")
		}
	})
}
