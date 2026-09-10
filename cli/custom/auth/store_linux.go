//go:build linux

package auth

import "fmt"

// secretToolStore keeps one host's session secrets in whatever Secret Service
// implementation is running — gnome-keyring, KWallet's Secret Service bridge,
// KeePassXC — through secret-tool(1) from libsecret.
//
// Unlike security(1), secret-tool takes the secret on stdin, so nothing secret
// ever reaches argv here.
type secretToolStore struct{}

var secretTool = keychainTool{
	bin: "secret-tool",
	advice: "secret-tool is not installed — install libsecret-tools (Debian/Ubuntu) " +
		"or libsecret (Fedora/Arch/Alpine)",
}

// platformStore is the OS secure store for this platform. It does not probe:
// per-call try-and-fall-back is the model (RES-1269 design discussion, Q3), and
// on Linux a probe is especially pointless — a lookup against a machine with no
// keyring daemon is indistinguishable from a lookup for an item that is simply
// not there until the store reads stderr. The first real Set is the probe, and
// SaveSession degrades on its error.
func platformStore() (secretStore, error) { return secretToolStore{}, nil }

func (secretToolStore) Name() string { return "libsecret" }

// itemAttrs is the attribute pair that identifies one host's item. secret-tool
// has no notion of a service or an account; both are just attributes, and the
// pair must match exactly between store, lookup and clear.
func itemAttrs(account string) []string {
	return []string{"service", keychainService, "account", account}
}

func (secretToolStore) Get(account string) (string, error) {
	res, err := secretTool.run(append([]string{"lookup"}, itemAttrs(account)...), "")
	if err != nil {
		return "", err
	}
	if res.code == 0 {
		return res.secret(), nil
	}
	// The one exit code that means two things. secret-tool returns 1 both for
	// "no such item" — which is not an error — and for "there is no Secret
	// Service to ask", which it reports as `secret-tool: <message>` on stderr.
	// Nothing but stderr tells them apart, which is why this mapping lives in
	// the store rather than in its callers.
	if res.stderr == "" {
		return "", nil
	}
	return "", secretToolFailure("read", res)
}

func (secretToolStore) Set(account, secret string) error {
	args := append([]string{"store", "--label=" + keychainItemLabel}, itemAttrs(account)...)
	res, err := secretTool.run(args, secret)
	if err != nil {
		return err
	}
	if res.code != 0 {
		return secretToolFailure("write", res)
	}
	return nil
}

// Delete treats "nothing was removed" as success, the same way ClearSession
// treats a session file that is already gone.
func (secretToolStore) Delete(account string) error {
	res, err := secretTool.run(append([]string{"clear"}, itemAttrs(account)...), "")
	if err != nil {
		return err
	}
	if res.code == 0 || res.stderr == "" {
		return nil
	}
	return secretToolFailure("delete", res)
}

// secretToolFailure marks anything secret-tool complained about on stderr as
// "there is no keychain here": a missing daemon, a locked keyring and a D-Bus
// that will not autolaunch are all of that shape, and all of them mean the same
// thing to a caller deciding whether to fall back.
func secretToolFailure(action string, res toolResult) error {
	return fmt.Errorf("%w: could not %s the keyring item: %s", errNoKeychain, action, res.detail())
}
