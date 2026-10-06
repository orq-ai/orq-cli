package auth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// credstore.go holds the secret half of a login outside the plaintext session
// file. A login's tokens — the refresh token, the bootstrap and workspace
// access tokens, the gateway key — are the crown jewels: the refresh token
// alone re-mints workspace access for every workspace the user can reach.
// Until now they sat in ~/.orq/sessions/<host>.json at 0600, which stops other
// users but not any process running as the user, nor the backup, sync, and
// crash-report tools that walk $HOME. This moves them into the OS secure store,
// keyed per server host exactly like the session files, and leaves the
// non-secret state in the JSON so it stays inspectable.
//
// Shelling out to the platform tool is deliberate: it needs no cgo and no new
// dependency, and it is the same approach Braintrust's bt CLI takes.

// keychainService is the service name every orq-cli secret is filed under. The
// account is the session host, so each server's login is a separate entry.
const keychainService = "orq-cli"

// CredentialStoreEnv forces the store choice. "file" keeps secrets in the
// session JSON (CI, containers, headless Linux with no Secret Service); any
// secure-store value is a no-op since detection already prefers one.
const CredentialStoreEnv = "ORQ_CREDENTIAL_STORE"

// SecretStore persists the secret blob for one login, keyed by session host.
// Load reports ok=false (with a nil error) when nothing is stored for the host,
// so a missing entry reads the same as a never-written one.
type SecretStore interface {
	Load(host string) (secret string, ok bool, err error)
	Save(host, secret string) error
	Delete(host string) error
	// Name is a short label for `orq doctor`, e.g. "macOS Keychain".
	Name() string
}

// runSecretTool is the single hook tests replace to stand in for the platform
// binary without a real keychain. stdin is fed to the command; stdout is
// returned. A command that exits non-zero comes back as an *exec.ExitError,
// which the stores read to tell "not found" from a real failure.
var runSecretTool = func(stdin string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Preserve the exit code (stores interpret it) but attach stderr so
			// a real failure is not an opaque "exit status 1".
			if msg := strings.TrimSpace(errBuf.String()); msg != "" {
				return out.String(), fmt.Errorf("%s: %w: %s", name, err, msg)
			}
		}
		return out.String(), err
	}
	return out.String(), nil
}

// exitCoder is satisfied by *exec.ExitError (ExitCode() int) and by the fake a
// test injects, so the stores read a command's exit code without caring whether
// a real process produced it.
type exitCoder interface{ ExitCode() int }

// exitCode returns the process exit code carried by err, or -1 if err does not
// carry one (tool missing, I/O error).
func exitCode(err error) int {
	var ec exitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return -1
}

// toolInstalled reports whether name resolves on PATH, so detection can fall
// back to the file store rather than failing every command when the secure
// store's binary is absent.
func toolInstalled(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// ResolveSecretStore chooses the store for this invocation. It returns nil when
// secrets should stay in the session JSON: the escape hatch is set to "file",
// or no secure-store binary is available on this platform. getenv is injected
// so callers and tests can supply the environment.
func ResolveSecretStore(getenv func(string) string) SecretStore {
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.EqualFold(strings.TrimSpace(getenv(CredentialStoreEnv)), "file") {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		if toolInstalled("security") {
			return macKeychainStore{}
		}
	case "linux":
		// secret-tool needs a running Secret Service (gnome-keyring, KWallet).
		// A probe keeps a headless box from picking a store every call fails on.
		if toolInstalled("secret-tool") && secretToolReady() {
			return secretToolStore{}
		}
	}
	return nil
}

// ---- macOS: security(1) / Keychain ----

type macKeychainStore struct{}

func (macKeychainStore) Name() string { return "macOS Keychain" }

func (macKeychainStore) Load(host string) (string, bool, error) {
	// -w prints only the password; exit 44 is errSecItemNotFound.
	out, err := runSecretTool("", "security", "find-generic-password",
		"-s", keychainService, "-a", host, "-w")
	if err != nil {
		if exitCode(err) == 44 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimRight(out, "\n"), true, nil
}

func (macKeychainStore) Save(host, secret string) error {
	// -U updates the item in place when it already exists instead of erroring.
	_, err := runSecretTool("", "security", "add-generic-password",
		"-U", "-s", keychainService, "-a", host, "-w", secret)
	return err
}

func (macKeychainStore) Delete(host string) error {
	_, err := runSecretTool("", "security", "delete-generic-password",
		"-s", keychainService, "-a", host)
	if err != nil && exitCode(err) == 44 {
		return nil // already gone
	}
	return err
}

// ---- Linux: secret-tool(1) / libsecret ----

type secretToolStore struct{}

func (secretToolStore) Name() string { return "Linux secret-tool (libsecret)" }

func (secretToolStore) Load(host string) (string, bool, error) {
	// lookup prints the secret with no trailing newline, exit 0; a missing
	// item is exit 1 with empty output.
	out, err := runSecretTool("", "secret-tool", "lookup",
		"service", keychainService, "account", host)
	if err != nil {
		if exitCode(err) >= 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return out, true, nil
}

func (secretToolStore) Save(host, secret string) error {
	// The secret is fed on stdin, never argv.
	_, err := runSecretTool(secret, "secret-tool", "store",
		"--label", "orq-cli "+host,
		"service", keychainService, "account", host)
	return err
}

func (secretToolStore) Delete(host string) error {
	_, err := runSecretTool("", "secret-tool", "clear",
		"service", keychainService, "account", host)
	return err
}

// secretToolReady confirms a Secret Service is actually answering, not just
// that the binary exists: on a headless box the daemon is often absent, and a
// lookup then blocks or errors. A lookup for a name nothing stores returns
// quickly — exit 1 (not found) means the service is up and reachable.
func secretToolReady() bool {
	_, err := runSecretTool("", "secret-tool", "lookup", "service", keychainService, "account", "__orq_probe__")
	if err == nil {
		return true // the (empty) probe somehow exists; service is up
	}
	return exitCode(err) == 1
}
