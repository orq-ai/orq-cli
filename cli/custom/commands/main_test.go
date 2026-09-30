package commands

import (
	"os"
	"testing"
)

// Tests point HOME at a temp dir; a host CODEX_HOME would still aim codex
// detection at the real machine, so drop it before any test runs.
func TestMain(m *testing.M) {
	os.Unsetenv("CODEX_HOME")
	os.Exit(m.Run())
}
