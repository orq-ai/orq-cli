package custom

import (
	"os"
	"testing"
)

// TestMain keeps session reads off the real OS keychain. register.go resolves
// the session through auth, which would otherwise shell out to
// security/secret-tool on a developer's machine and, on macOS, block on an
// access prompt. The file store needs no daemon and prompts for nothing; store
// behaviour itself is covered in the auth package.
func TestMain(m *testing.M) {
	os.Setenv("ORQ_CREDENTIAL_STORE", "file")
	os.Exit(m.Run())
}
