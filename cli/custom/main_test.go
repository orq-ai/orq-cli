package custom

import (
	"os"
	"testing"

	"orq/cli/custom/auth"
)

// TestMain keeps this package's tests off the developer's real OS keychain: the
// PreRun tests log in, migrate and save sessions, and auth.SaveSession
// externalizes the secrets into the login keyring on darwin and linux unless the
// file store is asked for. Subprocess helpers here build their environment with
// append(os.Environ(), …), so they inherit this.
func TestMain(m *testing.M) {
	os.Setenv(auth.CredentialStoreEnvVar, "file")
	os.Exit(m.Run())
}
