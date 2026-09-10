package commands

import (
	"os"
	"testing"

	"orq/cli/custom/auth"
)

// TestMain keeps this package's tests off the developer's real OS keychain. Many
// of them save a session (setup, switch, logout, doctor), and auth.SaveSession
// externalizes the secrets into the login keyring on darwin and linux unless the
// file store is asked for — which would both pollute the machine and make
// assertions about the session file's contents depend on it.
//
// A test that wants the keychain path uses the seams in cli/custom/auth, where
// the store lives; this only pins the default.
func TestMain(m *testing.M) {
	os.Setenv(auth.CredentialStoreEnvVar, "file")
	os.Exit(m.Run())
}
