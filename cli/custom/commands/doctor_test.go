package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"orq/cli/custom/auth"
	"orq/cli/custom/skills"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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

func TestMCPCheckPassNamesEntryAndLoginCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"orq-workspace":{"type":"http","url":"https://api.orq.ai/v2/mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	check, ok := mcpCheck()
	if !ok || check.Status != "pass" {
		t.Fatalf("got ok=%v status=%q, want a pass", ok, check.Status)
	}
	if !strings.Contains(check.Message, "entry present") {
		t.Errorf("pass message does not use entry-present wording: %q", check.Message)
	}
	if strings.Contains(check.Message, "MCP works") {
		t.Errorf("pass message makes an MCP health claim: %q", check.Message)
	}
	if !strings.Contains(check.Message, "run /mcp in Claude Code, or 'claude mcp login orq-workspace'") {
		t.Errorf("pass message does not name Claude's login command: %q", check.Message)
	}
}

func TestMCPCheckReadsProjectScope(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(project)

	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".mcp.json"), []byte(`{"mcpServers":{"orq-workspace":{"type":"http","url":"https://api.orq.ai/v2/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	check, ok := mcpCheck()
	if !ok || check.Status != "pass" {
		t.Fatalf("got ok=%v status=%q, want a pass from project-scoped entry", ok, check.Status)
	}
}

func TestMCPCheckWarnsUnwiredAgentAndOmitsPi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}

	check, ok := mcpCheck()
	if !ok || check.Status != "warn" {
		t.Fatalf("got ok=%v status=%q, want an unwired-agent warning", ok, check.Status)
	}
	if !strings.Contains(check.Message, "orq connect claude mcp") {
		t.Errorf("warning does not name the remedy: %q", check.Message)
	}
	if strings.Contains(check.Message, "pi") {
		t.Errorf("warning reports pi even though it has no MCP support: %q", check.Message)
	}
}

// refresh converges the skill set, not the files: a skill the user deleted by
// hand stays deleted, because silently recreating it on the next `orq --help`
// would fight the user. doctor is where that state gets named.
func TestSkillsCheck(t *testing.T) {
	// A present link is a real symlink into a real snapshot, because that is
	// what ownership means. A bare directory at the path is what a user who
	// took the path over leaves behind, and doctor has to tell the two apart.
	newManifest := func(t *testing.T, dir string, present, absent, foreign int) {
		t.Helper()
		orq, err := skills.Home()
		if err != nil {
			t.Fatal(err)
		}
		gen := filepath.Join(orq, "snapshot", "gen-test")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		m := &skills.Manifest{Version: 1, Fingerprint: skills.Fingerprint()}
		for i := 0; i < present; i++ {
			name := fmt.Sprintf("orq-present-%d", i)
			src := filepath.Join(gen, name)
			if err := os.MkdirAll(src, 0o755); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, name)
			if err := os.Symlink(src, p); err != nil {
				t.Fatal(err)
			}
			m.AddLink(skills.Link{Path: p, Agent: "claude", Skill: name, Mode: skills.ModeSymlink})
		}
		for i := 0; i < absent; i++ {
			p := filepath.Join(dir, fmt.Sprintf("orq-absent-%d", i))
			m.AddLink(skills.Link{Path: p, Agent: "claude", Skill: filepath.Base(p), Mode: skills.ModeSymlink})
		}
		for i := 0; i < foreign; i++ {
			p := filepath.Join(dir, fmt.Sprintf("orq-foreign-%d", i))
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			m.AddLink(skills.Link{Path: p, Agent: "claude", Skill: filepath.Base(p), Mode: skills.ModeSymlink})
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
		newManifest(t, dir, 2, 0, 0)
		check, ok := skillsCheck()
		if !ok || check.Status != "pass" {
			t.Fatalf("got ok=%v status=%q, want a pass", ok, check.Status)
		}
	})

	t.Run("missing links warn with the remedy", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".claude", "skills")
		newManifest(t, dir, 1, 2, 0)
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
		newManifest(t, dir, 2, 0, 0)
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

	// The state that used to read as healthy: the path exists, so an
	// existence check passes it, but refresh will never touch it again.
	t.Run("a path taken over by the user is not installed", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".claude", "skills")
		newManifest(t, dir, 1, 0, 2)
		check, ok := skillsCheck()
		if !ok || check.Status != "warn" {
			t.Fatalf("got ok=%v status=%q, want a warn", ok, check.Status)
		}
		if check.Details["foreign"] != 2 {
			t.Errorf("details = %v, want foreign=2", check.Details)
		}
		if !strings.Contains(check.Message, "no longer ours") {
			t.Errorf("message does not say the paths are not ours: %q", check.Message)
		}
	})

	// A manifest that will not load breaks every skills command. doctor is
	// where the user comes to find out why, so it is the one place that must
	// not answer by saying nothing at all.
	t.Run("an unreadable manifest is reported, not swallowed", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		orq, err := skills.Home()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(orq, "materialized-skills.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		check, ok := skillsCheck()
		if !ok || check.Status != "fail" {
			t.Fatalf("got ok=%v status=%q, want a fail", ok, check.Status)
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

// TestCredentialPermsCheck exercises the loose-permission diagnostic on
// Unix; the check is entirely absent on Windows, where these bits do not
// mean anything.
func TestCredentialPermsCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credentialPermsCheck is absent on windows")
	}

	// setupConfig points both viper's config-directory and the auth
	// package's HOME-derived sessions dir at a fresh temp tree, and returns
	// the config directory and the path where the active profile's session
	// file would live.
	setupConfig := func(t *testing.T) (dir, sessionsDir string) {
		t.Helper()
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir = t.TempDir()
		viper.Set("config-directory", dir)
		t.Cleanup(func() { viper.Set("config-directory", "") })
		return dir, auth.SessionsDir()
	}

	t.Run("loose credentials.json warns and names the fix", func(t *testing.T) {
		dir, _ := setupConfig(t)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		credPath := filepath.Join(dir, "credentials.json")
		if err := os.WriteFile(credPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Explicit chmod: the process umask can mask bits requested via
		// WriteFile's mode argument, so this pins the fixture's actual mode
		// rather than trusting whatever the umask happened to leave.
		if err := os.Chmod(credPath, 0o644); err != nil {
			t.Fatal(err)
		}
		check, ok, _ := credentialPermsCheck(false)
		if !ok || check.Status != "warn" {
			t.Fatalf("got ok=%v status=%q, want a warn", ok, check.Status)
		}
		if !strings.Contains(check.Message, credPath) {
			t.Errorf("message does not name %s: %q", credPath, check.Message)
		}
		if !strings.Contains(check.Message, "chmod 600 "+credPath) {
			t.Errorf("message missing the chmod 600 remedy: %q", check.Message)
		}
		if !strings.Contains(check.Message, "revoke it in the orq dashboard") || !strings.Contains(check.Message, "orq setup") {
			t.Errorf("message does not mention revoking the key before rotating via orq setup: %q", check.Message)
		}
		// Only a symlink has a second path worth naming.
		if strings.Contains(check.Message, "a symlink to") {
			t.Errorf("a plain file grew a resolved-path disclosure: %q", check.Message)
		}
		loose, _ := check.Details["loose"].([]map[string]any)
		if len(loose) == 0 {
			t.Fatalf("details[loose] = %v, want the finding", check.Details["loose"])
		}
		for _, entry := range loose {
			if _, ok := entry["resolved_path"]; ok {
				t.Errorf("a plain file carries a resolved_path: %v", entry)
			}
		}
	})

	t.Run("0600 credentials.json in a 0700 dir passes", func(t *testing.T) {
		dir, _ := setupConfig(t)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		credPath := filepath.Join(dir, "credentials.json")
		if err := os.WriteFile(credPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(credPath, 0o600); err != nil {
			t.Fatal(err)
		}
		if check, ok, _ := credentialPermsCheck(false); ok {
			t.Fatalf("a clean tree still reported: ok=%v check=%+v", ok, check)
		}
	})

	t.Run("a loose session file is reported", func(t *testing.T) {
		dir, sessionsDir := setupConfig(t)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		sessionPath := filepath.Join(sessionsDir, auth.ActiveProfile()+".json")
		if err := os.WriteFile(sessionPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sessionPath, 0o644); err != nil {
			t.Fatal(err)
		}
		check, ok, _ := credentialPermsCheck(false)
		if !ok || check.Status != "warn" {
			t.Fatalf("got ok=%v status=%q, want a warn", ok, check.Status)
		}
		// The message prints the human-facing (tilde'd, shell-quoted) path;
		// the raw absolute path is what --json's Details carries.
		if !strings.Contains(check.Message, tilde(sessionPath)) {
			t.Errorf("message does not name the loose session file %s: %q", tilde(sessionPath), check.Message)
		}
		loose, ok := check.Details["loose"].([]map[string]any)
		if !ok || len(loose) != 1 || loose[0]["path"] != sessionPath {
			t.Errorf("details[loose] = %v, want raw path %s", check.Details["loose"], sessionPath)
		}
		// A session file holds refresh and access tokens, not an API key:
		// logout revokes them, `orq setup` does not.
		if !strings.Contains(check.Message, "orq auth logout") {
			t.Errorf("session finding does not point at logout: %q", check.Message)
		}
		if strings.Contains(check.Message, "orq setup") {
			t.Errorf("session finding offers the API-key rotation route: %q", check.Message)
		}
	})

	t.Run("a loose config directory is reported with chmod 700", func(t *testing.T) {
		dir, _ := setupConfig(t)
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		check, ok, _ := credentialPermsCheck(false)
		if !ok || check.Status != "warn" {
			t.Fatalf("got ok=%v status=%q, want a warn", ok, check.Status)
		}
		if !strings.Contains(check.Message, "chmod 700 "+dir) {
			t.Errorf("message missing the chmod 700 remedy for %s: %q", dir, check.Message)
		}
	})

	t.Run("an empty config dir with no credential files passes", func(t *testing.T) {
		dir, _ := setupConfig(t)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if check, ok, _ := credentialPermsCheck(false); ok {
			t.Fatalf("an empty config dir still reported: ok=%v check=%+v", ok, check)
		}
	})

	t.Run("two loose files are both named in the one message", func(t *testing.T) {
		dir, _ := setupConfig(t)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		credPath := filepath.Join(dir, "credentials.json")
		envPath := filepath.Join(dir, "env")
		if err := os.WriteFile(credPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(credPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(envPath, []byte(""), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(envPath, 0o640); err != nil {
			t.Fatal(err)
		}
		check, ok, _ := credentialPermsCheck(false)
		if !ok || check.Status != "warn" {
			t.Fatalf("got ok=%v status=%q, want a warn", ok, check.Status)
		}
		for _, p := range []string{credPath, envPath} {
			if !strings.Contains(check.Message, p) {
				t.Errorf("message does not name %s: %q", p, check.Message)
			}
		}
		loose, ok := check.Details["loose"].([]map[string]any)
		if !ok || len(loose) != 2 {
			t.Fatalf("details[loose] = %v, want 2 entries", check.Details["loose"])
		}
	})

	t.Run("a symlinked credentials.json is judged on its target", func(t *testing.T) {
		dir, _ := setupConfig(t)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		targetDir := t.TempDir()
		target := filepath.Join(targetDir, "creds.json")
		if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o644); err != nil {
			t.Fatal(err)
		}
		credPath := filepath.Join(dir, "credentials.json")
		if err := os.Symlink(target, credPath); err != nil {
			t.Fatal(err)
		}
		check, ok, _ := credentialPermsCheck(false)
		if !ok || check.Status != "warn" {
			t.Fatalf("got ok=%v status=%q, want a warn for the symlink target", ok, check.Status)
		}
		// chmod follows the link, so the printed remedy stays on the path the
		// user recognizes — but the file actually judged has to be named too.
		if !strings.Contains(check.Message, "chmod 600 "+credPath) {
			t.Errorf("message does not name the symlink path: %q", check.Message)
		}
		real, err := filepath.EvalSymlinks(target)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(check.Message, real) {
			t.Errorf("message hides the resolved target %s: %q", real, check.Message)
		}
		loose, _ := check.Details["loose"].([]map[string]any)
		if len(loose) != 1 || loose[0]["resolved_path"] != real {
			t.Errorf("details[loose] = %v, want resolved_path=%s", check.Details["loose"], real)
		}
	})

	t.Run("a broken symlink is skipped silently", func(t *testing.T) {
		dir, _ := setupConfig(t)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "gone.json"), filepath.Join(dir, "credentials.json")); err != nil {
			t.Fatal(err)
		}
		if check, ok, _ := credentialPermsCheck(false); ok {
			t.Fatalf("a broken symlink reported: %+v", check)
		}
	})

	t.Run("fix chmods the loose paths and still says rotate", func(t *testing.T) {
		dir, _ := setupConfig(t)
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		credPath := filepath.Join(dir, "credentials.json")
		if err := os.WriteFile(credPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(credPath, 0o644); err != nil {
			t.Fatal(err)
		}
		check, ok, _ := credentialPermsCheck(true)
		if !ok {
			t.Fatal("fix run reported nothing")
		}
		if check.Status != "warn" {
			t.Errorf("status = %q, want warn", check.Status)
		}
		if !strings.Contains(check.Message, "revoke it in the orq dashboard") {
			t.Errorf("repaired run dropped the rotation advice: %q", check.Message)
		}
		for _, want := range []struct {
			path string
			mode os.FileMode
		}{{credPath, 0o600}, {dir, 0o700}} {
			info, err := os.Stat(want.path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want.mode {
				t.Errorf("%s is mode %04o after --fix, want %04o", want.path, info.Mode().Perm(), want.mode)
			}
		}
		fixed, ok := check.Details["fixed"].([]map[string]any)
		if !ok || len(fixed) != 2 {
			t.Fatalf("details[fixed] = %v, want 2 entries", check.Details["fixed"])
		}
		loose, ok := check.Details["loose"].([]map[string]any)
		if !ok || len(loose) != 0 {
			t.Errorf("details[loose] = %v, want empty after a successful fix", check.Details["loose"])
		}
		if _, again, _ := credentialPermsCheck(false); again {
			t.Error("a re-run after --fix still reports a finding")
		}
	})

	t.Run("a credentials.json that is a directory is a finding, not silence", func(t *testing.T) {
		dir, _ := setupConfig(t)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		credPath := filepath.Join(dir, "credentials.json")
		if err := os.Mkdir(credPath, 0o700); err != nil {
			t.Fatal(err)
		}
		check, ok, _ := credentialPermsCheck(true)
		if !ok || check.Status != "warn" {
			t.Fatalf("got ok=%v status=%q, want a warn naming the wrong-typed path", ok, check.Status)
		}
		if !strings.Contains(check.Message, credPath) || !strings.Contains(check.Message, "a directory") {
			t.Errorf("message does not name the path and what it is: %q", check.Message)
		}
		if _, ok := check.Details["invalid_type"].([]string); !ok {
			t.Errorf("details[invalid_type] = %v, want the finding", check.Details["invalid_type"])
		}
		if fixed, ok := check.Details["fixed"]; ok {
			t.Errorf("--fix chmodded a non-regular target: %v", fixed)
		}
	})

	t.Run("a config directory with a space prints a runnable chmod", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(t.TempDir(), "space dir")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		viper.Set("config-directory", dir)
		t.Cleanup(func() { viper.Set("config-directory", "") })
		credPath := filepath.Join(dir, "credentials.json")
		if err := os.WriteFile(credPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(credPath, 0o644); err != nil {
			t.Fatal(err)
		}
		check, ok, _ := credentialPermsCheck(false)
		if !ok {
			t.Fatal("a loose file under a path with a space reported nothing")
		}
		// The chmod is printed unwrapped: the path already carries its own
		// quotes, and a second layer of quotes makes it unrunnable.
		want := "run chmod 600 '" + credPath + "'"
		if !strings.Contains(check.Message, want) {
			t.Errorf("message = %q, want it to contain %q", check.Message, want)
		}
		if strings.Contains(check.Message, "run '") {
			t.Errorf("message double-quotes the chmod: %q", check.Message)
		}
	})

	t.Run("a sessions directory that cannot be listed is reported", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root can read a directory with no read bit")
		}
		dir, sessionsDir := setupConfig(t)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		// Write-and-search only: the mode is tight, so the directory itself is
		// not a finding — only the listing that failed is.
		if err := os.Chmod(sessionsDir, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(sessionsDir, 0o700) })
		check, ok, _ := credentialPermsCheck(false)
		if !ok {
			t.Fatal("an unlistable sessions directory reported nothing")
		}
		if !strings.Contains(check.Message, "could not be inspected") || !strings.Contains(check.Message, tilde(sessionsDir)) {
			t.Errorf("message does not report the failed listing: %q", check.Message)
		}
	})

	t.Run("fix on a symlink chmods the target", func(t *testing.T) {
		dir, _ := setupConfig(t)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "creds.json")
		if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "credentials.json")); err != nil {
			t.Fatal(err)
		}
		check, ok, _ := credentialPermsCheck(true)
		if !ok {
			t.Fatal("fix run reported nothing")
		}
		// The repaired file is outside the config directory: a message that
		// named only ~/.orq/credentials.json would hide which file was chmodded.
		real, err := filepath.EvalSymlinks(target)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(check.Message, filepath.Join(dir, "credentials.json")) || !strings.Contains(check.Message, real) {
			t.Errorf("message does not name both the link and its target %s: %q", real, check.Message)
		}
		fixed, _ := check.Details["fixed"].([]map[string]any)
		var disclosed bool
		for _, entry := range fixed {
			if entry["path"] == filepath.Join(dir, "credentials.json") && entry["resolved_path"] == real {
				disclosed = true
			}
		}
		if !disclosed {
			t.Errorf("details[fixed] does not disclose the resolved target %s: %v", real, check.Details["fixed"])
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("symlink target is mode %04o after --fix, want 0600", info.Mode().Perm())
		}
	})
}

// runDoctorJSON runs the real `orq doctor` the way a script does — through
// cobra, with the process-wide formatter — and returns the decoded report.
// No --json: that flag lives on bartolo's root command, and a non-TTY run
// already gets the structured report, which is the contract scripts use.
func runDoctorJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	return runDoctor(t, false, args...)
}

// runDoctor is runDoctorJSON with an expectation about the returned error, so
// a test can assert both the exit-code behaviour and the report that had to be
// printed before it.
func runDoctor(t *testing.T, wantErr bool, args ...string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	origStdout, origFormatter := bartolocli.Stdout, bartolocli.Formatter
	t.Cleanup(func() { bartolocli.Stdout, bartolocli.Formatter = origStdout, origFormatter })
	bartolocli.Stdout = &out
	bartolocli.Formatter = bartolocli.NewDefaultFormatter(false)
	viper.Set("output-format", "json")
	t.Cleanup(func() { viper.Set("output-format", "") })

	// doctor stamps the report with bartolo's root command version, so the
	// command under test hangs off that same root, as it does in the binary.
	root := &cobra.Command{Use: "orq", Version: "test"}
	origRoot := bartolocli.Root
	bartolocli.Root = root
	t.Cleanup(func() { bartolocli.Root = origRoot })
	root.AddCommand(NewDoctorCommand())
	root.SetArgs(append([]string{"doctor"}, args...))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	runErr := root.Execute()
	if runErr != nil && !wantErr {
		t.Fatalf("orq doctor %v: %v", args, runErr)
	}
	if runErr == nil && wantErr {
		t.Fatalf("orq doctor %v returned nil, want an error", args)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("doctor output is not JSON (%v): %s", err, out.String())
	}
	return report
}

// findCheck returns the check with the given id, or nil when the report has none.
func findCheck(t *testing.T, report map[string]any, id string) map[string]any {
	t.Helper()
	checks, ok := report["checks"].([]any)
	if !ok {
		t.Fatalf("report has no checks array: %v", report["checks"])
	}
	for _, raw := range checks {
		check, ok := raw.(map[string]any)
		if ok && check["id"] == id {
			return check
		}
	}
	return nil
}

// TestDoctorReportsAndFixesCredentialPermissions drives the actual command,
// not credentialPermsCheck: a check that never got wired into RunE, or a
// --fix flag that was never bound to it, passes every direct-call test in
// this file and fails this one.
func TestDoctorReportsAndFixesCredentialPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credentialPermsCheck is absent on windows")
	}
	// The report probes its base URLs; pointing them at a local server keeps
	// the test off the network without stubbing the probe itself out.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	t.Setenv("ORQ_SERVER", srv.URL)
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	viper.Set("config-directory", dir)
	t.Cleanup(func() { viper.Set("config-directory", "") })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(credPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(credPath, 0o644); err != nil {
		t.Fatal(err)
	}

	// The real binary initializes the credentials file at startup; the
	// gateway-key checks in the same report dereference it.
	creds, err := bartolocli.NewCredentialsFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	origCreds := bartolocli.Creds
	bartolocli.Creds = creds
	t.Cleanup(func() { bartolocli.Creds = origCreds })

	check := findCheck(t, runDoctorJSON(t), "credential_permissions")
	if check == nil {
		t.Fatal("orq doctor carries no credential_permissions check")
	}
	if check["status"] != "warn" {
		t.Errorf("status = %v, want warn", check["status"])
	}
	details, _ := check["details"].(map[string]any)
	loose, _ := details["loose"].([]any)
	if len(loose) != 1 {
		t.Fatalf("details.loose = %v, want 1 entry", details["loose"])
	}
	if entry, _ := loose[0].(map[string]any); entry["path"] != credPath {
		t.Errorf("loose entry = %v, want path=%s", loose[0], credPath)
	}

	fixed := findCheck(t, runDoctorJSON(t, "--fix"), "credential_permissions")
	if fixed == nil {
		t.Fatal("orq doctor --fix carries no credential_permissions check")
	}
	if info, err := os.Stat(credPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials.json is %04o after --fix, want 0600", info.Mode().Perm())
	}

	if again := findCheck(t, runDoctorJSON(t), "credential_permissions"); again != nil {
		t.Errorf("a repaired tree still reports: %v", again)
	}
}

func TestShellQuotePathKeepsTildeExpandable(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"~/.orq/credentials.json", "~/.orq/credentials.json"},
		{"~/.orq dir/credentials.json", "~/'.orq dir/credentials.json'"},
		{"/Users/Alice Smith/.orq/env", "'/Users/Alice Smith/.orq/env'"},
	} {
		if got := shellQuotePath(tc.in); got != tc.want {
			t.Errorf("shellQuotePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A --fix run is an action, and an action that failed must not report success:
// scripts chaining `orq doctor --fix && ...` would carry on over an
// unrepaired credential. Every other doctor outcome still exits 0.
func TestDoctorFixExitCodeOnlyFollowsAFailedRepair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credentialPermsCheck is absent on windows")
	}
	// The chmod is indirected rather than staged on disk: no path makes chmod
	// fail for its owner on every platform these tests run on, and a test that
	// only fails on one is worse than none.
	failChmod := func(t *testing.T) {
		t.Helper()
		orig := credPermChmod
		credPermChmod = func(*os.File, os.FileMode) error { return errors.New("chmod refused") }
		t.Cleanup(func() { credPermChmod = orig })
	}
	loose := func(t *testing.T) string {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		viper.Set("config-directory", dir)
		t.Cleanup(func() { viper.Set("config-directory", "") })
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		credPath := filepath.Join(dir, "credentials.json")
		if err := os.WriteFile(credPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(credPath, 0o644); err != nil {
			t.Fatal(err)
		}
		return credPath
	}

	t.Run("failed fix errors", func(t *testing.T) {
		loose(t)
		failChmod(t)
		check, ok, err := credentialPermsCheck(true)
		if !ok || err == nil {
			t.Fatalf("ok=%v err=%v, want a reported failure", ok, err)
		}
		if check.Status != "fail" || !strings.Contains(check.Message, "chmod refused") {
			t.Errorf("check does not carry the failed repair: %+v", check)
		}
	})

	t.Run("successful fix and a plain report do not", func(t *testing.T) {
		loose(t)
		if _, _, err := credentialPermsCheck(false); err != nil {
			t.Errorf("a report run returned %v, want nil", err)
		}
		if _, _, err := credentialPermsCheck(true); err != nil {
			t.Errorf("a successful --fix returned %v, want nil", err)
		}
	})

	t.Run("the command prints the report before failing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
		t.Cleanup(srv.Close)
		t.Setenv("ORQ_SERVER", srv.URL)
		credPath := loose(t)
		creds, err := bartolocli.NewCredentialsFile(filepath.Dir(credPath))
		if err != nil {
			t.Fatal(err)
		}
		origCreds := bartolocli.Creds
		bartolocli.Creds = creds
		t.Cleanup(func() { bartolocli.Creds = origCreds })
		failChmod(t)

		check := findCheck(t, runDoctor(t, true, "--fix"), "credential_permissions")
		if check == nil {
			t.Fatal("the failing --fix run emitted no credential_permissions check")
		}
		if check["status"] != "fail" {
			t.Errorf("status = %v, want fail", check["status"])
		}
	})
}

// The flag is registered everywhere so the platform-neutral surface manifest
// stays true; Windows rejects it at run time instead.
func TestDoctorFixFlagIsRegisteredOnEveryPlatform(t *testing.T) {
	if NewDoctorCommand().Flags().Lookup("fix") == nil {
		t.Error("doctor has no --fix flag")
	}
}
