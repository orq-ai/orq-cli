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
