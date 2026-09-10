package auth

import "runtime"

// platformStore is the OS secure store for the platform this binary was built
// for. No build tag yet: no platform has one wired up, so every platform
// resolves to the file store and every session file on disk keeps the shape it
// has always had. Wiring macOS and Linux is the next step, and it is this
// function returning something real plus a `//go:build !darwin && !linux`
// constraint on this file — nothing above it changes.
//
// Windows is expected to stay here permanently. It has a usable API
// (CredWriteW) but caps a credential blob at CRED_MAX_CREDENTIAL_BLOB_SIZE =
// 2560 bytes, which a refresh token plus a bootstrap JWT plus one JWT per
// (workspace × project) the user has touched exceeds after two or three
// workspaces — see the RES-1269 design discussion, Q2.
func platformStore() (secretStore, error) {
	store := unavailableStore{reason: "no OS keychain support on " + runtime.GOOS}
	return store, store.err()
}
