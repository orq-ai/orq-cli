package auth

import (
	"encoding/base64"
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
// dependency. The one cost is that the write must not pass the secret in argv,
// where a same-user process could read it from the process table — the very
// threat the file permissions already fail against. macOS takes the command on
// stdin via `security -i`; Linux feeds the secret to `secret-tool` on stdin.

// keychainService is the service name every orq-cli secret is filed under. The
// account is the session host, so each server's login is a separate entry.
const keychainService = "orq-cli"

// CredentialStoreEnv forces the store choice. "file" keeps secrets in the
// session JSON (CI, containers, headless Linux with no Secret Service); any
// secure-store value is a no-op since detection already prefers one.
const CredentialStoreEnv = "ORQ_CREDENTIAL_STORE"

// SecretStore persists the secret blob for one login, keyed by session host.
// Load reports ok=false (with a nil error) only when the store positively holds
// nothing for the host. A store that cannot answer (locked keyring, unreachable
// service) returns an error, so a transient failure never reads as a missing
// login.
type SecretStore interface {
	Load(host string) (secret string, ok bool, err error)
	Save(host, secret string) error
	Delete(host string) error
	// Name is a short label for `orq doctor`, e.g. "macOS Keychain".
	Name() string
}

// runSecretTool is the single hook tests replace to stand in for the platform
// binary without a real keychain. stdin is fed to the command; stdout and the
// trimmed stderr come back alongside the error, so a store can tell a positive
// "not found" (quiet non-zero exit) from a real failure (exit with a message).
var runSecretTool = func(stdin string, name string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return out.String(), strings.TrimSpace(errBuf.String()), err
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
	out, stderr, err := runSecretTool("", "security", "find-generic-password",
		"-s", keychainService, "-a", host, "-w")
	if err != nil {
		if exitCode(err) == 44 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("security find-generic-password: %w: %s", err, stderr)
	}
	// The stored value is base64 (see Save). Decode before handing it back.
	dec, derr := base64.StdEncoding.DecodeString(strings.TrimRight(out, "\n"))
	if derr != nil {
		return "", false, fmt.Errorf("keychain secret is not valid base64: %w", derr)
	}
	return string(dec), true, nil
}

func (macKeychainStore) Save(host, secret string) error {
	// base64 so the blob carries no spaces or quotes to break the interactive
	// parser, and the command goes in on stdin via `security -i`, not argv, so
	// the token never appears in the process table. -U updates in place.
	enc := base64.StdEncoding.EncodeToString([]byte(secret))
	cmd := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", keychainService, host, enc)
	_, stderr, err := runSecretTool(cmd, "security", "-i")
	if err != nil {
		return fmt.Errorf("security -i add-generic-password: %w: %s", err, stderr)
	}
	return nil
}

func (macKeychainStore) Delete(host string) error {
	_, stderr, err := runSecretTool("", "security", "delete-generic-password",
		"-s", keychainService, "-a", host)
	if err != nil {
		if exitCode(err) == 44 {
			return nil // already gone
		}
		return fmt.Errorf("security delete-generic-password: %w: %s", err, stderr)
	}
	return nil
}

// ---- Linux: secret-tool(1) / libsecret ----

type secretToolStore struct{}

func (secretToolStore) Name() string { return "Linux secret-tool (libsecret)" }

func (secretToolStore) Load(host string) (string, bool, error) {
	out, stderr, err := runSecretTool("", "secret-tool", "lookup",
		"service", keychainService, "account", host)
	if err != nil {
		// secret-tool exits 1 for a missing item (quiet) and also when it cannot
		// reach the Secret Service (stderr set). Only the quiet exit 1 is "not
		// found"; anything with a message is a real error the caller must see,
		// not a blank session.
		if exitCode(err) == 1 && stderr == "" {
			return "", false, nil
		}
		return "", false, fmt.Errorf("secret-tool lookup: %w: %s", err, stderr)
	}
	return out, true, nil
}

func (secretToolStore) Save(host, secret string) error {
	// The secret is fed on stdin, never argv.
	_, stderr, err := runSecretTool(secret, "secret-tool", "store",
		"--label", "orq-cli "+host,
		"service", keychainService, "account", host)
	if err != nil {
		return fmt.Errorf("secret-tool store: %w: %s", err, stderr)
	}
	return nil
}

func (secretToolStore) Delete(host string) error {
	_, stderr, err := runSecretTool("", "secret-tool", "clear",
		"service", keychainService, "account", host)
	if err != nil {
		return fmt.Errorf("secret-tool clear: %w: %s", err, stderr)
	}
	return nil
}

// secretToolReady confirms a Secret Service is actually answering, not just that
// the binary exists: on a headless box the daemon is often absent, and a lookup
// then errors. A lookup for a name nothing stores returns a quiet exit 1 when
// the service is up ("no such item"); an exit 1 with stderr, or any other code,
// means it could not be reached, so the file store is the safer pick.
func secretToolReady() bool {
	_, stderr, err := runSecretTool("", "secret-tool", "lookup", "service", keychainService, "account", "__orq_probe__")
	if err == nil {
		return true // the (empty) probe somehow exists; service is up
	}
	return exitCode(err) == 1 && stderr == ""
}
