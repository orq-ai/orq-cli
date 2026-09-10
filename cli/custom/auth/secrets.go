package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

// CredentialStoreEnvVar names the store a user (or a managed environment) wants
// session secrets kept in.
//
//	unset / "auto"  try the OS keychain, fall back to the session file
//	"keychain"      require the OS keychain; anything else is a hard error
//	"file"          never touch the keychain — a deliberate opt-out, no warning
const CredentialStoreEnvVar = "ORQ_CREDENTIAL_STORE"

// Session layout versions. 1 keeps the secrets inline in the session file, the
// only shape every release before this one wrote. 2 keeps them in the store
// named by Session.SecretStore. An older binary reading a 2 stops at its own
// `Version != 1` check and says "unsupported session version" — which points at
// the real cause — instead of "malformed: missing refresh token", which sends
// people to `rm -rf ~/.orq`.
const (
	sessionVersionInline   = 1
	sessionVersionExternal = 2
)

// errNoKeychain is what a store reports when there is no secure store to talk
// to at all: an unsupported platform, a missing helper binary, or a keyring
// daemon that is not running. Callers in `auto` mode degrade to the file store
// on it; `ORQ_CREDENTIAL_STORE=keychain` surfaces it.
var errNoKeychain = errors.New("no OS keychain available")

// errFileStoreHasNoItems guards a caller that routed a secret into the file
// store instead of writing it inline. Nothing does today — saveSessionTo
// branches on the store type first — and a silent success here would drop the
// user's refresh token on the floor.
var errFileStoreHasNoItems = errors.New("the file store keeps secrets in the session file, not as items")

// SessionSecrets is the four secret fields of a browser login and only those.
// It is marshalled either inline into the session file (file store, the
// pre-existing layout) or as the value of one keychain item.
//
// It is embedded anonymously in Session, so every field access in the codebase
// — session.RefreshToken, session.WorkspaceTokens[k] — is unchanged, and so the
// inline JSON keys are exactly the ones every previous release wrote.
type SessionSecrets struct {
	RefreshToken    string                       `json:"refreshToken"`
	BootstrapToken  StoredAccessToken            `json:"bootstrapToken"`
	WorkspaceTokens map[string]StoredAccessToken `json:"workspaceTokens"`
	GatewayKey      string                       `json:"gatewayKey,omitempty"`
}

// secretStore is one named place a SessionSecrets blob can live.
//
// Get returning ("", nil) means "no such item" — not found is not an error,
// mirroring ReadSession's own (nil, nil) for a missing session. Only a
// genuinely broken store returns a non-nil error. On Linux that distinction is
// the whole reason the store, rather than its caller, has to read secret-tool's
// stderr: a missing item and a missing keyring daemon share one exit code.
type secretStore interface {
	Name() string
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

// fileStore is the "no secure store" marker. Resolving it tells saveSessionTo
// to write the secrets inline, exactly as every release before this one did, so
// a user who opted out (or whose platform has no keychain) sees a session file
// byte-shaped as it has always been.
type fileStore struct{}

func (fileStore) Name() string { return "file" }

// Get answers "no such item", so a session file whose secretStore marker names
// a store this process cannot resolve degrades to `session_secrets_missing`
// rather than to a confusing store error.
func (fileStore) Get(string) (string, error) { return "", nil }

func (fileStore) Set(string, string) error { return errFileStoreHasNoItems }

// Delete succeeds: ClearSession's best-effort item removal has nothing to do
// when the secrets were inline, and removing the file removes them.
func (fileStore) Delete(string) error { return nil }

// unavailableStore stands in for a platform, or a session, with no secure store
// reachable. Every operation reports why, so a caller that demanded the
// keychain gets the cause rather than a bare failure.
type unavailableStore struct{ reason string }

func (u unavailableStore) Name() string               { return "unavailable" }
func (u unavailableStore) Get(string) (string, error) { return "", u.err() }
func (u unavailableStore) Set(string, string) error   { return u.err() }
func (u unavailableStore) Delete(string) error        { return u.err() }
func (u unavailableStore) err() error                 { return fmt.Errorf("%w: %s", errNoKeychain, u.reason) }

// resolveStore picks the store this process writes secrets to. It is a package
// var because that is this repo's test seam — see commands/doctor.go's
// credPermChmod and commands/setup.go's writeSecretFile — and because tests
// must exercise the externalized path on a machine with no keychain.
var resolveStore = func() (secretStore, error) { return storeFor(platformStore) }

// storeFor is resolveStore's actual dispatch, taking the platform store as an
// argument so a test can drive the ORQ_CREDENTIAL_STORE rules against a fake one
// without duplicating them — including that `=file` never reaches it at all.
//
// The error is non-nil only when the caller demanded the keychain and there is
// none: `auto` degrades to the file store instead, which is what keeps a
// headless box, a container and Windows behaving exactly as they do today.
func storeFor(platform func() (secretStore, error)) (secretStore, error) {
	switch credentialStorePreference() {
	case "file":
		return fileStore{}, nil
	case "keychain":
		// No fallback: an org that sets this wants a hard error, not a silent
		// downgrade to the layout it set the variable to get away from.
		return platform()
	default: // "" or "auto"
		store, err := platform()
		if err != nil {
			return fileStore{}, nil
		}
		return store, nil
	}
}

// credentialStorePreference is ORQ_CREDENTIAL_STORE, normalized. Read in one
// place so store resolution and the no-fallback rule below cannot disagree
// about what the user asked for.
func credentialStorePreference() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv(CredentialStoreEnvVar)))
}

// keychainRequired reports whether the secure store was demanded rather than
// preferred. It is what keeps `=keychain` a policy knob once platformStore
// stops failing up front: with a real store resolved, the only place the
// keychain can turn out to be unusable is the write itself, and an org that set
// this wants that write to fail loudly instead of quietly landing the secrets in
// the file it set the variable to avoid.
func keychainRequired() bool { return credentialStorePreference() == "keychain" }

// secretAccount names the keychain item holding one host's secrets. The host is
// the session file's own name (SessionHost), so nothing has to invent a second
// identifier for a login that deliberately has no profile name.
func secretAccount(host string) string { return "session::" + host }

var fallbackWarned sync.Once

// warnFallbackOnce says, once per process, that the secrets could not reach the
// secure store and are being written into the session file instead. Once,
// because every command in a pipeline hitting the same locked keyring would
// otherwise bury its own output; loudly, because the alternative is `gh`'s
// silent downgrade. migrate.go already writes to bartolocli.Stderr with no
// quiet or `-o json` gate, so this matches what is there.
func warnFallbackOnce(err error) {
	fallbackWarned.Do(func() {
		fmt.Fprintf(bartolocli.Stderr,
			"could not store session credentials in the OS keychain (%v). "+
				"They are being written to the session file instead, as they were before. "+
				"Set %s=file to silence this.\n", err, CredentialStoreEnvVar)
	})
}

// resetFallbackWarning lets a test observe the once-per-process warning more
// than once in one test binary.
func resetFallbackWarning() { fallbackWarned = sync.Once{} }
