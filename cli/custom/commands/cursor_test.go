package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"orq/cli/custom/launch"
)

func cursorConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("%s is not JSON: %v", path, err)
	}
	return cfg
}

// Cursor's remote server entry is a URL and nothing else: its "type" key is for
// stdio servers, and the login is OAuth, so no header and no credential belong here.
func TestConnectCursorWritesTheMCPEntryCursorReads(t *testing.T) {
	for _, scope := range []string{"--local", "--global"} {
		t.Run(scope, func(t *testing.T) {
			home, project := mcpMachine(t, ".cursor")
			want := filepath.Join(project, ".cursor", "mcp.json")
			other := filepath.Join(home, ".cursor", "mcp.json")
			if scope == "--global" {
				want, other = other, want
			}
			if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
				t.Fatal(err)
			}
			existing := `{"mcpServers":{"linear":{"url":"https://mcp.linear.app/mcp"}},"keep":true}`
			if err := os.WriteFile(want, []byte(existing), 0o600); err != nil {
				t.Fatal(err)
			}

			c := NewConnectCommand()
			c.SetArgs([]string{"cursor", "mcp", scope})
			if err := c.Execute(); err != nil {
				t.Fatalf("connect: %v", err)
			}

			cfg := cursorConfig(t, want)
			servers, _ := cfg["mcpServers"].(map[string]any)
			entry, _ := servers[launch.MCPServerName].(map[string]any)
			url, _ := entry["url"].(string)
			if !strings.HasPrefix(url, "https://") || !reflect.DeepEqual(entry, map[string]any{"url": url}) {
				t.Fatalf("entry = %#v, want only an https url", entry)
			}
			if !reflect.DeepEqual(servers["linear"], map[string]any{"url": "https://mcp.linear.app/mcp"}) || cfg["keep"] != true {
				t.Fatalf("the user's own config was not preserved: %#v", cfg)
			}
			if _, err := os.Stat(other); err == nil {
				t.Fatalf("%s wrote the other scope too: %s", scope, other)
			}

			spec, _ := lookupAgent("cursor")
			if !spec.mcpPresent(want) {
				t.Fatal("status would not see the entry that was just written")
			}
			removed, err := spec.removeMCP(want)
			if err != nil || !removed {
				t.Fatalf("remove: removed=%v err=%v", removed, err)
			}
			after := cursorConfig(t, want)
			left, _ := after["mcpServers"].(map[string]any)
			if _, still := left[launch.MCPServerName]; still || left["linear"] == nil || after["keep"] != true {
				t.Fatalf("remove took the wrong keys: %#v", after)
			}
		})
	}
}

// Cursor reads the shared agents-spec directory, so skills for it land there and nowhere else.
func TestConnectCursorInstallsSkillsIntoTheSharedDirectory(t *testing.T) {
	_, project := mcpMachine(t, ".cursor")

	c := NewConnectCommand()
	c.SetArgs([]string{"cursor", "skills", "--local"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(project, ".agents", "skills"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no skills in the shared directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".cursor", "skills")); err == nil {
		t.Fatal("skills were also written to .cursor/skills, which would list each one twice")
	}
}

func TestConnectStatusListsCursor(t *testing.T) {
	mcpMachine(t, ".cursor")
	c := NewConnectCommand()
	c.SetArgs([]string{"cursor", "mcp", "--global"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	out := captureOutput(t, func() {
		s := NewConnectCommand()
		s.SetArgs([]string{"--status"})
		if err := s.Execute(); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	row := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "cursor") {
			row = line
		}
	}
	for _, want := range []string{"✓", "mcp", "global", "~/.cursor/mcp.json"} {
		if !strings.Contains(row, want) {
			t.Fatalf("status row for cursor lacks %q:\n%s", want, out)
		}
	}
}

// The entry carries no credential, so the hint is the only thing that tells the
// user to finish the OAuth login. It has to name the server and both ways in.
func TestCursorLoginHintNamesTheServerAndBothWaysToLogIn(t *testing.T) {
	spec, ok := lookupAgent("cursor")
	if !ok {
		t.Fatal("cursor is not in the registry")
	}
	for _, want := range []string{"Customize > MCPs", "enable", "Tools & MCP", "agent mcp login " + launch.MCPServerName} {
		if !strings.Contains(spec.mcpLogin, want) {
			t.Errorf("login hint %q lacks %q", spec.mcpLogin, want)
		}
	}

	mcpMachine(t, ".cursor")
	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"cursor", "mcp", "--global"})
		if err := c.Execute(); err != nil {
			t.Fatalf("connect: %v", err)
		}
	})
	if !strings.Contains(out, spec.mcpLogin) {
		t.Fatalf("connect did not print the login hint:\n%s", out)
	}
}

func connectCursorMCP(t *testing.T) (string, error) {
	t.Helper()
	var err error
	out := captureOutput(t, func() {
		c := NewConnectCommand()
		c.SetArgs([]string{"cursor", "mcp", "--global"})
		err = c.Execute()
	})
	return out, err
}

func TestConnectCursorSaysWhenCommentsAreWhyTheFileIsRefused(t *testing.T) {
	home, _ := mcpMachine(t, ".cursor")
	path := filepath.Join(home, ".cursor", "mcp.json")
	withComments := "{\n // c\n \"mcpServers\": {\"x\": {\"url\":\"https://x\"},}\n}"
	if err := os.WriteFile(path, []byte(withComments), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := connectCursorMCP(t)

	if err == nil {
		t.Fatal("connect succeeded on a file it cannot rewrite without dropping the comments")
	}
	if !strings.Contains(out, "has comments or trailing commas") {
		t.Fatalf("the refusal does not name comments as the cause:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != withComments {
		t.Fatalf("the file was changed: %s", got)
	}
}

func TestConnectCursorStillCallsBrokenJSONInvalid(t *testing.T) {
	home, _ := mcpMachine(t, ".cursor")
	if err := os.WriteFile(filepath.Join(home, ".cursor", "mcp.json"), []byte(`{"mcpServers": `), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := connectCursorMCP(t)

	if err == nil || !strings.Contains(out, "is not valid JSON") || strings.Contains(out, "comments") {
		t.Fatalf("err = %v, output:\n%s", err, out)
	}
}

func TestStripJSONCLeavesStringsAlone(t *testing.T) {
	in := `{"url": "https://x/*y*/", /* gone */ "a": [1, 2,], // gone
	"b": "c,}",}`
	var got map[string]any
	if err := json.Unmarshal(stripJSONC([]byte(in)), &got); err != nil {
		t.Fatalf("stripped text does not parse: %v", err)
	}
	if got["url"] != "https://x/*y*/" || got["b"] != "c,}" {
		t.Fatalf("a string was changed: %#v", got)
	}
}

func TestConnectCursorWarnsWhenItReplacesADifferentEntry(t *testing.T) {
	home, _ := mcpMachine(t, ".cursor")
	path := filepath.Join(home, ".cursor", "mcp.json")
	own := `{"mcpServers":{"` + launch.MCPServerName + `":{"url":"https://old","headers":{"Authorization":"Bearer x"}}}}`
	if err := os.WriteFile(path, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := connectCursorMCP(t)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !strings.Contains(out, "replaced the "+launch.MCPServerName+" entry") || !strings.Contains(out, ".orq-bak") {
		t.Fatalf("no warning that the user's entry was replaced:\n%s", out)
	}

	again, err := connectCursorMCP(t)
	if err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if strings.Contains(again, "replaced the") {
		t.Fatalf("a rerun over orq's own entry warned:\n%s", again)
	}
}

func disconnectCursor(t *testing.T, args ...string) string {
	t.Helper()
	return captureOutput(t, func() {
		c := NewDisconnectCommand()
		c.SetArgs(append([]string{"cursor"}, args...))
		if err := c.Execute(); err != nil {
			t.Fatalf("disconnect: %v", err)
		}
	})
}

func TestDisconnectCursorNamesTheAgentsThatShareItsSkills(t *testing.T) {
	for _, flag := range []string{"--dry-run", "--yes"} {
		t.Run(flag, func(t *testing.T) {
			mcpMachine(t, ".cursor", ".codex")
			c := NewConnectCommand()
			c.SetArgs([]string{"cursor", "skills", "--global"})
			if err := c.Execute(); err != nil {
				t.Fatalf("connect: %v", err)
			}

			out := disconnectCursor(t, flag)

			if !strings.Contains(out, "the skills directory is shared: codex reads it too") {
				t.Fatalf("%s did not say codex loses its skills:\n%s", flag, out)
			}
		})
	}
}

func TestDisconnectCursorAloneGivesNoSharedSkillsWarning(t *testing.T) {
	mcpMachine(t, ".cursor")
	c := NewConnectCommand()
	c.SetArgs([]string{"cursor", "skills", "--global"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if out := disconnectCursor(t, "--yes"); strings.Contains(out, "is shared") {
		t.Fatalf("warned with no other agent on the machine:\n%s", out)
	}
}

func TestDisconnectCursorAndEveryOtherSharedReaderGivesNoWarning(t *testing.T) {
	mcpMachine(t, ".cursor", ".codex")
	c := NewConnectCommand()
	c.SetArgs([]string{"cursor", "skills", "--global"})
	if err := c.Execute(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if out := disconnectCursor(t, "codex", "--dry-run"); strings.Contains(out, "is shared") {
		t.Fatalf("warned although codex is being disconnected too:\n%s", out)
	}
}

func TestConnectCursorCreatesAProjectFileTheTeamCanReadAndAPrivateGlobalOne(t *testing.T) {
	for scope, want := range map[string]os.FileMode{"--local": 0o644, "--global": 0o600} {
		t.Run(scope, func(t *testing.T) {
			home, project := mcpMachine(t, ".cursor")
			path := filepath.Join(home, ".cursor", "mcp.json")
			if scope == "--local" {
				path = filepath.Join(project, ".cursor", "mcp.json")
			}
			c := NewConnectCommand()
			c.SetArgs([]string{"cursor", "mcp", scope})
			if err := c.Execute(); err != nil {
				t.Fatalf("connect: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Fatalf("mode = %o, want %o", got, want)
			}
		})
	}
}
