package commands

import (
	"os"
	"testing"
)

// Keep tests that exercise a bare `orq connect` away from the invoking user's
// Claude settings, even when an individual test forgets to redirect HOME.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "orq-commands-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("USERPROFILE", home)
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
