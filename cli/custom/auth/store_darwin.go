//go:build darwin

package auth

import "fmt"

// securityStore keeps one host's session secrets in the macOS keychain as a
// generic password, through /usr/bin/security.
//
// On argv exposure: `security add-generic-password -w <secret>` passes the
// secret on the command line, and there is no stdin form of -w. macOS restricts
// reading another process's arguments (KERN_PROCARGS2) to the same uid or root,
// so the window is same-user — the trust boundary the 0600 session file already
// sits on. Written down here so it is not rediscovered later as a surprise.
type securityStore struct{}

// security(1) reports an OSStatus as its exit code modulo 256.
const (
	securityItemNotFound          = 44 // errSecItemNotFound (-25300)
	securityInteractionNotAllowed = 36 // errSecInteractionNotAllowed (-25308)
)

var securityTool = keychainTool{
	bin:    "security",
	advice: "security(1) is not on PATH, so the macOS keychain cannot be reached",
}

// platformStore is the OS secure store for this platform. It does not probe:
// per-call try-and-fall-back is the model (RES-1269 design discussion, Q3) —
// a read probe proves nothing against a locked keychain and a write probe is a
// dummy item to create and clean up. The first real Set is the probe, and
// SaveSession degrades on its error.
func platformStore() (secretStore, error) { return securityStore{}, nil }

func (securityStore) Name() string { return "macOS Keychain" }

func (securityStore) Get(account string) (string, error) {
	res, err := securityTool.run([]string{
		"find-generic-password", "-s", keychainService, "-a", account, "-w",
	}, "")
	if err != nil {
		return "", err
	}
	switch res.code {
	case 0:
		return res.secret(), nil
	case securityItemNotFound:
		// Not found is not an error, exactly as a missing session file is not.
		return "", nil
	default:
		return "", securityFailure("read", res)
	}
}

// Set updates in place with -U rather than deleting and re-adding: recreating an
// item wipes an ACL the user has approved, which makes macOS prompt again on
// every save.
func (securityStore) Set(account, secret string) error {
	res, err := securityTool.run([]string{
		"add-generic-password", "-U",
		"-s", keychainService,
		"-a", account,
		"-w", secret,
	}, "")
	if err != nil {
		return err
	}
	if res.code != 0 {
		return securityFailure("write", res)
	}
	return nil
}

// Delete treats a missing item as success: a logout has nothing to remove when
// the login's secrets were never externalized, or were removed already.
func (securityStore) Delete(account string) error {
	res, err := securityTool.run([]string{
		"delete-generic-password", "-s", keychainService, "-a", account,
	}, "")
	if err != nil {
		return err
	}
	switch res.code {
	case 0, securityItemNotFound:
		return nil
	default:
		return securityFailure("delete", res)
	}
}

// securityFailure names the cause, and marks the one code that means "there is
// no usable keychain in this session" as such, so ORQ_CREDENTIAL_STORE=keychain
// can tell a policy failure from a store that simply is not there.
func securityFailure(action string, res toolResult) error {
	if res.code == securityInteractionNotAllowed {
		return fmt.Errorf("%w: the keychain is not available in this session "+
			"(errSecInteractionNotAllowed — typically an ssh login or a launchd job)", errNoKeychain)
	}
	return fmt.Errorf("could not %s the keychain item: %s", action, res.detail())
}
