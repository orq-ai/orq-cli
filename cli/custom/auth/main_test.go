package auth

import (
	"os"
	"testing"
)

// TestMain forces the file store for the whole package. Without it, on a dev
// macOS or Linux box with a real secure store, every test that saves or reads a
// session would shell out to `security`/`secret-tool` — touching the user's
// real keychain and, on macOS, blocking on an access prompt. Store behaviour is
// exercised deliberately: credstore tests drive the stubbed runSecretTool, and
// the session-secret tests override activeStore with a fake.
func TestMain(m *testing.M) {
	activeStore = func() SecretStore { return nil }
	os.Exit(m.Run())
}
