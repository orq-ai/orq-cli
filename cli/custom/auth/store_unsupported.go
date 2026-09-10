//go:build !darwin && !linux

package auth

import "runtime"

// platformStore is the OS secure store for the platform this binary was built
// for — and on this one there is none, so every session file keeps the shape it
// has always had, with the secrets inline. `auto` resolves to the file store
// silently: this is not a degradation to warn about on every login, it is the
// documented state of the platform.
//
// Windows is expected to stay here. It has a usable API (CredWriteW) but caps a
// credential blob at CRED_MAX_CREDENTIAL_BLOB_SIZE = 2560 bytes, which a refresh
// token plus a bootstrap JWT plus one JWT per (workspace × project) the user has
// touched exceeds after two or three workspaces; `cmdkey` cannot read a secret
// back at all, so the Win32 API would be mandatory, and nothing in this package
// is covered by the Windows CI job — see the RES-1269 design discussion, Q2.
// Adding it later is one file, store_windows.go, and this constraint.
func platformStore() (secretStore, error) {
	store := unavailableStore{reason: "no OS keychain support on " + runtime.GOOS}
	return store, store.err()
}
