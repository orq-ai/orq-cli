package custom

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orq/cli/custom/commands"

	"github.com/spf13/cobra"
)

// A generated operation returns bartolo's bare `HTTP 404`, which says nothing
// about the project the read was scoped to. The decoration is applied to the
// whole tree so those commands carry the scope too, and skipped where the
// command already named it.
func TestNotFoundNamesTheActiveProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORQ_API_KEY", "")
	// A process global shared with every other test in this package.
	prevExplicit := commands.UsingExplicitAPIKey()
	commands.SetExplicitAPIKey(false)
	t.Cleanup(func() { commands.SetExplicitAPIKey(prevExplicit) })
	dir := filepath.Join(home, ".orq", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	session := `{"version":1,"apiBaseUrl":"https://api.orq.ai","v1BaseUrl":"https://api.orq.ai/v1","authBaseUrl":"https://api.orq.ai/v2/auth","profileBaseUrl":"https://api.orq.ai/v2/auth/profile","user":{"id":"u"},"workspaces":[],"activeWorkspaceKey":"acme","activeProjectId":"id-1","activeProjectName":"Banking","refreshToken":"r","bootstrapToken":{"token":"t","expiresAt":"2099-01-01T00:00:00Z"},"workspaceTokens":{}}`
	if err := os.WriteFile(filepath.Join(dir, "my.orq.ai.json"), []byte(session), 0o600); err != nil {
		t.Fatal(err)
	}

	root := &cobra.Command{Use: "root"}
	generated := &cobra.Command{Use: "generated", RunE: func(*cobra.Command, []string) error {
		return errors.New("error calling operation: HTTP 404:\n{\"message\":\"trace span not found\"}")
	}}
	explains := &cobra.Command{Use: "explains", RunE: func(*cobra.Command, []string) error {
		return errors.New("HTTP 404: this trace is in project X: run `orq projects use x`")
	}}
	fine := &cobra.Command{Use: "fine", RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(generated, explains, fine)
	explainNotFoundScope(root)

	err := generated.RunE(generated, nil)
	if err == nil || !strings.Contains(err.Error(), `project "Banking"`) {
		t.Fatalf("generated 404 = %v, want the active project named", err)
	}
	err = explains.RunE(explains, nil)
	if strings.Count(err.Error(), "orq projects use") != 1 {
		t.Fatalf("a command that named the scope itself got a second copy: %v", err)
	}
	if err := fine.RunE(fine, nil); err != nil {
		t.Fatalf("success path = %v, want nil", err)
	}
}
