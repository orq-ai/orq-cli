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
	// Keep session reads off the real OS keychain. doctor and other commands
	// read the session through auth, which would otherwise shell out to
	// security/secret-tool on a dev box; the file store needs no daemon and
	// prompts for nothing. Store behaviour is covered in the auth package.
	_ = os.Setenv("ORQ_CREDENTIAL_STORE", "file")
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
