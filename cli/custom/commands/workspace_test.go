package commands

import (
	"bytes"
	"io"
	"testing"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// runWorkspaceUse drives `orq workspace use` with its output captured, since
// the JSON branch writes straight to bartolo's stdout.
func runWorkspaceUse(t *testing.T, args ...string) error {
	t.Helper()
	prev := bartolocli.Stdout
	bartolocli.Stdout = &bytes.Buffer{}
	t.Cleanup(func() { bartolocli.Stdout = prev })
	cmd := newWorkspaceUseCommand()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.Execute()
}

// The active project belongs to the workspace it was chosen in. `orq switch`
// cleared it; `orq workspace use` did not, so ws1's project stayed on disk and
// every later command asked for a ws2 token narrowed to a project ws2 does not
// contain.
func TestWorkspaceUseClearsTheProjectOfTheWorkspaceItLeaves(t *testing.T) {
	switchTestEnv(t)
	srv := switchServer(t, []string{"ws1", "ws2"}, "")
	switchSession(t, srv.URL, "ws1", []string{"ws1", "ws2"}, "id-1", "One")

	if err := runWorkspaceUse(t, "ws2"); err != nil {
		t.Fatalf("workspace use: %v", err)
	}

	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.ActiveWorkspaceKey == nil || *session.ActiveWorkspaceKey != "ws2" {
		t.Fatalf("active workspace = %v, want ws2", session.ActiveWorkspaceKey)
	}
	if session.ActiveProjectID != "" || session.ActiveProjectName != "" {
		t.Errorf("ws1's project survived the move to ws2: %q/%q, want none",
			session.ActiveProjectID, session.ActiveProjectName)
	}
}

// Re-asserting the workspace already active is not a move, and dropping a
// deliberately chosen project on it would make a no-op command destructive.
func TestWorkspaceUseKeepsTheProjectWhenTheWorkspaceDoesNotChange(t *testing.T) {
	switchTestEnv(t)
	srv := switchServer(t, []string{"ws1", "ws2"}, "")
	switchSession(t, srv.URL, "ws1", []string{"ws1", "ws2"}, "id-1", "One")

	if err := runWorkspaceUse(t, "ws1"); err != nil {
		t.Fatalf("workspace use: %v", err)
	}

	session, err := auth.ReadSession()
	if err != nil {
		t.Fatal(err)
	}
	if session.ActiveProjectID != "id-1" || session.ActiveProjectName != "One" {
		t.Errorf("active project = %q/%q, want the untouched id-1/One",
			session.ActiveProjectID, session.ActiveProjectName)
	}
}
