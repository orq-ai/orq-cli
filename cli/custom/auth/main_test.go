package auth

import (
	"os"
	"testing"
)

// TestMain keeps this package's tests off the developer's real OS keychain, the
// same way launch's TestMain keeps them out of the developer's real HOME.
//
// platformStore is a live store on darwin and linux now, so an unguarded
// SaveSession on a desktop would write an item named after whatever host the
// test invented into the user's login keyring, and every assertion about what is
// in the session file would be an assertion about that machine's keychain state.
// The externalized path is exercised by the in-memory fake store instead (see
// useStore), and the shell-out layer by a stub script on an empty PATH.
//
// A test that wants the other behavior overrides this with t.Setenv, which
// restores it afterwards.
func TestMain(m *testing.M) {
	os.Setenv(CredentialStoreEnvVar, "file")
	os.Exit(m.Run())
}
