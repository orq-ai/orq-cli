package commands

import (
	"os"
	"testing"
)

// TestMain keeps every test off the real coding agents. `orq connect` shells out
// to an agent's own plugin manager, and a bare connect selects every agent whose
// config directory exists, so without this a test run on a machine that has
// claude installed installs and uninstalls plugins for real. The tests that
// exercise that path replace this with a recorder of their own and assert
// against what it saw.
func TestMain(m *testing.M) {
	runAgentCommand = func(string, ...string) error { return nil }
	os.Exit(m.Run())
}
