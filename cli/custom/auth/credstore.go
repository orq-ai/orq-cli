package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// credstore.go holds the secret half of a login outside the plaintext session
// file. A login's tokens — the refresh token, the bootstrap and workspace
// access tokens, the gateway key — are the crown jewels: the refresh token
// alone re-mints workspace access for every workspace the user can reach.
// A 0600 file such as ~/.orq/sessions/<host>.json stops other users but not
// any process running as the user, nor the backup, sync, and crash-report tools
// that walk $HOME, so they live in the OS secure store, keyed per server host
// exactly like the session files. The non-secret state stays in the JSON so it
// stays inspectable.
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
	// ID is the stable marker a session file records, e.g. "keychain".
	ID() string
	// Name is a short label for `orq doctor`, e.g. "macOS Keychain".
	Name() string
	// ClearHint is the shell command that removes host's entry by hand, printed
	// when logout's Delete could not. Empty when there is nothing to suggest.
	ClearHint(host string) string
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
	// No secure store on this platform (Windows and the rest): secrets stay in
	// the 0600 session JSON. doctor reports the store as unused so this is
	// visible, not silent. A Windows Credential Manager backend is RES-1776.
	return nil
}

// storeNamed returns the store whose ID is id, so a session file's
// SecretStore marker can be turned back into a name and a ClearHint when this
// process resolved no store. nil for an id no build of this CLI writes.
func storeNamed(id string) SecretStore {
	for _, s := range []SecretStore{macKeychainStore{}, secretToolStore{}} {
		if s.ID() == id {
			return s
		}
	}
	return nil
}

// storeLabel names a SecretStore marker for a message: the store's Name when
// this build knows the id, else the id itself.
func storeLabel(id string) string {
	if s := storeNamed(id); s != nil {
		return s.Name()
	}
	return id
}

// storeServes reports whether this process's store is the one a session file's
// marker names, i.e. whether its secrets can be loaded here.
func storeServes(id string) bool {
	s := activeStore()
	return s != nil && s.ID() == id
}

// ---- macOS: security(1) / Keychain ----

type macKeychainStore struct{}

func (macKeychainStore) ID() string   { return "keychain" }
func (macKeychainStore) Name() string { return "macOS Keychain" }

func (macKeychainStore) Load(host string) (string, bool, error) {
	first, ok, err := findKeychainChunk(host, 0)
	if err != nil || !ok {
		return "", false, err
	}
	count, sum, body, framed := parseChunkHeader(first)
	if !framed {
		return "", false, fmt.Errorf("%w: chunk 0 has no header", errKeychainSecretCorrupt)
	}
	var enc strings.Builder
	enc.WriteString(body)
	for i := 1; i < count; i++ {
		chunk, ok, err := findKeychainChunk(host, i)
		if err != nil {
			return "", false, err
		}
		if !ok {
			return "", false, fmt.Errorf("%w: chunk %d of %d is missing", errKeychainSecretCorrupt, i, count)
		}
		enc.WriteString(chunk)
	}
	if chunkChecksum(enc.String()) != sum {
		return "", false, fmt.Errorf("%w: checksum mismatch across %d chunks", errKeychainSecretCorrupt, count)
	}
	dec, derr := base64.StdEncoding.DecodeString(enc.String())
	if derr != nil {
		return "", false, fmt.Errorf("%w: not valid base64: %v", errKeychainSecretCorrupt, derr)
	}
	return string(dec), true, nil
}

// findKeychainChunk reads one chunk; ok=false only on exit 44 (errSecItemNotFound).
func findKeychainChunk(host string, i int) (string, bool, error) {
	service, account := chunkTarget(host, i)
	// -w prints only the password.
	out, stderr, err := runSecretTool("", "security", "find-generic-password",
		"-s", service, "-a", account, "-w")
	if err != nil {
		if exitCode(err) == 44 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("security find-generic-password: %w: %s", err, stderr)
	}
	return strings.TrimRight(out, "\n"), true, nil
}

// errKeychainSecretCorrupt marks a stored value that does not reassemble: a
// chunk missing, or chunks from two different writes. It names no secret.
var errKeychainSecretCorrupt = errors.New("keychain secret is incomplete or corrupt")

// Chunk 0 starts with a fixed-width header, "orq1.<count>.<checksum>.", so Load
// knows how many chunks to read and can tell a complete value from a gap or
// from chunks of two different writes. '.' is not in the base64 alphabet, so
// the header can never be mistaken for the start of the encoded value.
const (
	chunkHeaderPrefix = "orq1."
	chunkHeaderLen    = len(chunkHeaderPrefix) + 3 + 1 + 16 + 1
	maxKeychainChunks = 999
)

func chunkChecksum(enc string) string {
	sum := sha256.Sum256([]byte(enc))
	return hex.EncodeToString(sum[:8])
}

func chunkHeader(count int, enc string) string {
	return fmt.Sprintf("%s%03d.%s.", chunkHeaderPrefix, count, chunkChecksum(enc))
}

func parseChunkHeader(v string) (count int, sum, body string, ok bool) {
	if len(v) < chunkHeaderLen || !strings.HasPrefix(v, chunkHeaderPrefix) {
		return 0, "", "", false
	}
	h := v[len(chunkHeaderPrefix):chunkHeaderLen]
	if h[3] != '.' || h[len(h)-1] != '.' {
		return 0, "", "", false
	}
	n, err := strconv.Atoi(h[:3])
	if err != nil || n < 1 {
		return 0, "", "", false
	}
	return n, h[4 : len(h)-1], v[chunkHeaderLen:], true
}

// maxKeychainCommand bounds a single `security -i` command line. Observed on
// macOS: security's interactive reader takes each line into a 4095-byte buffer,
// and a longer line is split mid-command, so it stores a truncated value and
// runs the tail as a second, failing command that echoes the tail (base64
// secret) to stderr. Reproduce by feeding `security -i` one add-generic-password
// line whose -w value pushes the line past 4095 bytes. A session with many
// workspace tokens can cross this, so Save splits the encoded secret across
// keychain items, each written on a command line kept under this margin (4000,
// leaving headroom under the observed 4095). The secret is never truncated and
// never falls back to plaintext just for being large.
const maxKeychainCommand = 4000

// addGenericPasswordLine builds the exact `security -i` line Save writes for one
// chunk. Both the per-chunk budget (with an empty value) and the real command use
// it, so the overhead the budget subtracts can never drift from the command that
// is actually sent.
func addGenericPasswordLine(service, account, value string) string {
	return fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", service, account, value)
}

// keychainContService holds continuation chunks for a secret too large for one
// keychain item. Chunk 0 stays under keychainService with the bare host account,
// so a one-item secret is read by a plain lookup; chunk i>=1 lives here as
// "<host>.<i>", a separate namespace so it never collides with a primary
// per-host item.
const keychainContService = keychainService + ".cont"

// errSecretTooLarge is returned only in degenerate cases: the service and host
// names are so long that not even an empty value fits one command line, or the
// secret needs more chunks than the header can count. It names no secret.
var errSecretTooLarge = errors.New("secret or keychain names too long for the security -i command line")

// chunkTarget is the (service, account) a given chunk of host's secret is
// stored under. See keychainContService for why chunk 0 is special.
func chunkTarget(host string, i int) (service, account string) {
	if i == 0 {
		return keychainService, host
	}
	return keychainContService, fmt.Sprintf("%s.%d", host, i)
}

func (macKeychainStore) Save(host, secret string) error {
	// base64 so the blob carries no spaces or quotes to break the interactive
	// parser, and the command goes in on stdin via `security -i`, not argv, so
	// the token never appears in the process table. -U updates in place.
	enc := base64.StdEncoding.EncodeToString([]byte(secret))

	// Split so each add-generic-password line stays under the interactive
	// buffer. The per-chunk budget shrinks by the command's fixed overhead,
	// which varies with the service and account for that chunk, and chunk 0
	// also carries the header.
	var chunks []string
	rest := enc
	for i := 0; ; i++ {
		service, account := chunkTarget(host, i)
		overhead := len(addGenericPasswordLine(service, account, ""))
		if i == 0 {
			overhead += chunkHeaderLen
		}
		budget := maxKeychainCommand - overhead
		if budget <= 0 {
			return errSecretTooLarge // names so long no value fits a line
		}
		if len(rest) <= budget {
			chunks = append(chunks, rest)
			break
		}
		chunks = append(chunks, rest[:budget])
		rest = rest[budget:]
	}
	if len(chunks) > maxKeychainChunks {
		return errSecretTooLarge
	}
	chunks[0] = chunkHeader(len(chunks), enc) + chunks[0]

	for i, chunk := range chunks {
		service, account := chunkTarget(host, i)
		cmd := addGenericPasswordLine(service, account, chunk)
		if _, _, err := runSecretTool(cmd, "security", "-i"); err != nil {
			// Best-effort: drop every chunk so Load never finds a half-written
			// secret (the header would reject it) and no stale tail fragment
			// from a previous larger secret is orphaned. Delete the chunks we may
			// have written (0..i), then keep going until a continuation delete
			// fails, absent or not, so logout's Delete — which stops at the first
			// gap — is rarely left with items it cannot reach. The caller then
			// falls back to the 0600 file.
			for j := 0; ; j++ {
				s, a := chunkTarget(host, j)
				_, _, derr := runSecretTool("", "security", "delete-generic-password", "-s", s, "-a", a)
				if derr != nil && j > i {
					break // past our writes and no more leftover chunks
				}
			}
			// Never include security's stderr: on a rejected `-i` line it echoes
			// the command, which carries the base64 secret. Exit code only.
			return fmt.Errorf("security -i add-generic-password failed (exit %d)", exitCode(err))
		}
	}

	// Clear any leftover chunks from a previous, larger secret. Stop at the first
	// not-found (exit 44). Any OTHER delete error (keychain locked, access denied)
	// leaves a stale tail. The header already keeps Load from reading it, but
	// remove what we just wrote and fail anyway, so the caller keeps the secret in
	// the 0600 file rather than relying on a keychain it cannot clean.
	for i := len(chunks); ; i++ {
		service, account := chunkTarget(host, i)
		_, _, err := runSecretTool("", "security", "delete-generic-password", "-s", service, "-a", account)
		if err == nil {
			continue
		}
		if exitCode(err) == 44 {
			break // no more leftover chunks
		}
		msg := fmt.Sprintf("security delete-generic-password: could not clear a leftover keychain chunk (exit %d)", exitCode(err))
		for j := range chunks {
			s, a := chunkTarget(host, j)
			if _, _, derr := runSecretTool("", "security", "delete-generic-password", "-s", s, "-a", a); derr != nil {
				msg += fmt.Sprintf("; rolling back chunk %d also failed (exit %d), so stale orq-cli keychain items may remain", j, exitCode(derr))
				break
			}
		}
		return errors.New(msg)
	}
	return nil
}

func (macKeychainStore) Delete(host string) error {
	// Remove chunk 0 and every continuation chunk. A missing chunk 0 does NOT end
	// the removal: a prior partial write can leave chunk 0 gone while continuations
	// remain, and logout must not orphan base64 secret fragments. Only a missing
	// continuation (i >= 1) ends the tail. A real error (locked, access denied)
	// stops the scan and is returned, since the rest cannot be cleared anyway.
	for i := 0; ; i++ {
		service, account := chunkTarget(host, i)
		_, stderr, err := runSecretTool("", "security", "delete-generic-password",
			"-s", service, "-a", account)
		if err == nil {
			continue
		}
		if exitCode(err) == 44 {
			if i == 0 {
				continue // chunk 0 already gone, but continuations may remain
			}
			return nil // a missing continuation ends the tail
		}
		return fmt.Errorf("security delete-generic-password: %w: %s", err, stderr)
	}
}

// ClearHint removes chunk 0 and then each continuation chunk until one is
// missing, the same walk Delete does. It runs under sh so it also works when
// pasted into fish. Hosts are sanitized to [a-z0-9._-] by SessionHost, so they
// need no shell quoting.
func (macKeychainStore) ClearHint(host string) string {
	return fmt.Sprintf("sh -c 'security delete-generic-password -s %s -a %s; i=1; while security delete-generic-password -s %s -a %s.$i >/dev/null 2>&1; do i=$((i+1)); done'",
		keychainService, host, keychainContService, host)
}

// ---- Linux: secret-tool(1) / libsecret ----

type secretToolStore struct{}

func (secretToolStore) ID() string   { return "secret-service" }
func (secretToolStore) Name() string { return "Linux secret-tool (libsecret)" }

func (secretToolStore) ClearHint(host string) string {
	return fmt.Sprintf("secret-tool clear service %s account %s", keychainService, host)
}

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

// maxSecretToolSecret is the largest value `secret-tool store` keeps whole:
// libsecret reads stdin into an 8192-byte buffer and, on a longer value, prints
// "password is too long" but still stores the truncated prefix and exits 0
// (tool/secret-tool.c, read_password_stdin). A larger secret is refused, so the
// caller keeps it in the 0600 file and says so, rather than strip the file
// against a truncated copy.
const maxSecretToolSecret = 8191

func (secretToolStore) Save(host, secret string) error {
	if len(secret) > maxSecretToolSecret {
		return fmt.Errorf("secret-tool store: the session secrets are %d bytes, over secret-tool's %d", len(secret), maxSecretToolSecret)
	}
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
		// A quiet exit 1 is "nothing removed", the same not-found Load reads.
		if exitCode(err) == 1 && stderr == "" {
			return nil
		}
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
