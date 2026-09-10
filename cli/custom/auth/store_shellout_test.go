//go:build darwin || linux

package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// No CI runner reaches a real keychain (RES-1269 research, §12), so the part of
// the shell-out most likely to be wrong — which exit code means "no such item"
// and which means "there is no keychain here" — is pinned against a stub script
// on an otherwise-empty PATH, the shape commands/update_test.go:92-103 already
// uses for npm and curl.

const (
	shelloutHost = "prod.example"
	shelloutBlob = `{"refreshToken":"r-1"}`
)

// shelloutFixture is one platform's helper: what it is called, the scripts that
// make it answer the way the real tool does, and the argument lists the store is
// expected to build. A table rather than one test file per platform, because the
// questions are identical on both and only the answers differ.
type shelloutFixture struct {
	bin string
	// notFound is the tool saying "no such item". security(1) has a code of its
	// own for it; secret-tool exits 1 with nothing on stderr — the same status a
	// real failure exits with, minus the message.
	notFound string
	// noStore is the tool saying there is no usable keychain in this session,
	// and the phrase the resulting error must carry so the user can act on it.
	noStore       string
	noStoreDetail string
	// missingBinAdvice is what a user gets when the helper is not installed.
	missingBinAdvice string
	getArgs          []string
	setArgs          []string
	deleteArgs       []string
	// secretOnStdin: secret-tool takes the secret on stdin, security(1) has no
	// stdin form of -w and takes it in argv.
	secretOnStdin bool
}

var shelloutFixtures = map[string]shelloutFixture{
	"darwin": {
		bin:              "security",
		notFound:         "exit 44", // errSecItemNotFound
		noStore:          "exit 36", // errSecInteractionNotAllowed
		noStoreDetail:    "not available in this session",
		missingBinAdvice: "security(1) is not on PATH",
		getArgs:          []string{"find-generic-password", "-s", keychainService, "-a", secretAccount(shelloutHost), "-w"},
		setArgs:          []string{"add-generic-password", "-U", "-s", keychainService, "-a", secretAccount(shelloutHost), "-w", shelloutBlob},
		deleteArgs:       []string{"delete-generic-password", "-s", keychainService, "-a", secretAccount(shelloutHost)},
	},
	"linux": {
		bin:              "secret-tool",
		notFound:         "exit 1",
		noStore:          "echo 'secret-tool: Cannot autolaunch D-Bus without X11 $DISPLAY' >&2\nexit 1",
		noStoreDetail:    "Cannot autolaunch D-Bus",
		missingBinAdvice: "libsecret-tools",
		getArgs:          []string{"lookup", "service", keychainService, "account", secretAccount(shelloutHost)},
		setArgs:          []string{"store", "--label=" + keychainItemLabel, "service", keychainService, "account", secretAccount(shelloutHost)},
		deleteArgs:       []string{"clear", "service", keychainService, "account", secretAccount(shelloutHost)},
		secretOnStdin:    true,
	},
}

func fixture(t *testing.T) shelloutFixture {
	t.Helper()
	f, ok := shelloutFixtures[runtime.GOOS]
	if !ok {
		t.Skipf("no keychain helper is wired on %s", runtime.GOOS)
	}
	return f
}

// stubTool writes a shell script named after this platform's helper onto a PATH
// containing nothing else, so the real tool is never a factor and no test ever
// touches a real keyring. The script records its arguments and stdin, so a test
// can pin the command line the store built as well as the mapping of its exit.
func stubTool(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "invocations")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do printf 'arg:%s\\n' \"$arg\" >> '" + log + "'; done\n" +
		"printf 'stdin:%s\\n' \"$(cat)\" >> '" + log + "'\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(dir, fixture(t).bin), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return log
}

// emptyPath simulates the helper not being installed at all.
func emptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func logLines(t *testing.T, log string) []string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the stub was never invoked: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func loggedArgs(t *testing.T, log string) []string {
	t.Helper()
	args := []string{}
	for _, line := range logLines(t, log) {
		if rest, ok := strings.CutPrefix(line, "arg:"); ok {
			args = append(args, rest)
		}
	}
	return args
}

func loggedStdin(t *testing.T, log string) string {
	t.Helper()
	for _, line := range logLines(t, log) {
		if rest, ok := strings.CutPrefix(line, "stdin:"); ok {
			return rest
		}
	}
	return ""
}

func platformStoreOrFail(t *testing.T) secretStore {
	t.Helper()
	store, err := platformStore()
	if err != nil {
		t.Fatalf("platformStore on %s: %v", runtime.GOOS, err)
	}
	return store
}

// Resolution must not talk to the keychain: the model is try-and-fall-back per
// call, so a probe would be one more subprocess on every command and would go
// stale the moment the user unlocked their keyring.
func TestPlatformStoreResolvesWithoutProbingTheKeychain(t *testing.T) {
	f := fixture(t)
	log := stubTool(t, "exit 1")

	store := platformStoreOrFail(t)
	if _, inline := store.(fileStore); inline {
		t.Fatalf("platformStore on %s resolved to the file store", runtime.GOOS)
	}
	if store.Name() == "" || store.Name() == (fileStore{}).Name() {
		t.Errorf("store name = %q, want the platform store's own name", store.Name())
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("resolving the store ran %s; resolution must not probe", f.bin)
	}
}

// Not found is not an error — the rule ReadSession already follows for a missing
// session file. On Linux this is the case that only stderr distinguishes from a
// genuine failure.
func TestGetTreatsAMissingItemAsNoItem(t *testing.T) {
	f := fixture(t)
	log := stubTool(t, f.notFound)

	secret, err := platformStoreOrFail(t).Get(secretAccount(shelloutHost))
	if err != nil {
		t.Fatalf("a missing item must not be an error: %v", err)
	}
	if secret != "" {
		t.Errorf("secret = %q, want empty", secret)
	}
	if got := loggedArgs(t, log); !slices.Equal(got, f.getArgs) {
		t.Errorf("argv =\n  %q\nwant\n  %q", got, f.getArgs)
	}
}

func TestGetReportsAnUnreachableKeychainAsErrNoKeychain(t *testing.T) {
	f := fixture(t)
	stubTool(t, f.noStore)

	_, err := platformStoreOrFail(t).Get(secretAccount(shelloutHost))
	if !errors.Is(err, errNoKeychain) {
		t.Fatalf("err = %v, want errNoKeychain so `auto` degrades and `keychain` reports", err)
	}
	if !strings.Contains(err.Error(), f.noStoreDetail) {
		t.Errorf("err = %q, want it to name the cause (%q)", err, f.noStoreDetail)
	}
}

func TestGetReturnsTheStoredSecretWithoutATrailingNewline(t *testing.T) {
	fixture(t)
	// Both tools may append a newline to the value they print; the blob is JSON
	// and never ends in one, so the store has to strip it or every read of a
	// written item is a JSON parse error.
	stubTool(t, "printf '%s\\n' '"+shelloutBlob+"'")

	secret, err := platformStoreOrFail(t).Get(secretAccount(shelloutHost))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if secret != shelloutBlob {
		t.Fatalf("secret = %q, want %q", secret, shelloutBlob)
	}
	var secrets SessionSecrets
	if err := json.Unmarshal([]byte(secret), &secrets); err != nil {
		t.Errorf("what came back is not a secrets blob: %v", err)
	}
}

func TestSetPassesTheSecretTheWayThePlatformTakesIt(t *testing.T) {
	f := fixture(t)
	log := stubTool(t, "exit 0")

	if err := platformStoreOrFail(t).Set(secretAccount(shelloutHost), shelloutBlob); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := loggedArgs(t, log); !slices.Equal(got, f.setArgs) {
		t.Errorf("argv =\n  %q\nwant\n  %q", got, f.setArgs)
	}
	stdin := loggedStdin(t, log)
	if f.secretOnStdin {
		if stdin != shelloutBlob {
			t.Errorf("stdin = %q, want the secret (it must never reach argv here)", stdin)
		}
		if slices.Contains(f.setArgs, shelloutBlob) {
			t.Errorf("argv carries the secret on a platform that takes it on stdin: %q", f.setArgs)
		}
	} else if stdin != "" {
		t.Errorf("stdin = %q, want nothing", stdin)
	}
}

func TestSetReportsAnUnreachableKeychain(t *testing.T) {
	f := fixture(t)
	stubTool(t, f.noStore)

	err := platformStoreOrFail(t).Set(secretAccount(shelloutHost), shelloutBlob)
	if !errors.Is(err, errNoKeychain) {
		t.Fatalf("err = %v, want errNoKeychain so SaveSession falls back and `keychain` reports", err)
	}
	if !strings.Contains(err.Error(), f.noStoreDetail) {
		t.Errorf("err = %q, want it to name the cause (%q)", err, f.noStoreDetail)
	}
}

// A logout must succeed when there is nothing to remove, exactly as ClearSession
// treats a session file that is already gone.
func TestDeleteToleratesAMissingItem(t *testing.T) {
	f := fixture(t)
	log := stubTool(t, f.notFound)

	if err := platformStoreOrFail(t).Delete(secretAccount(shelloutHost)); err != nil {
		t.Fatalf("deleting an item that is not there must succeed: %v", err)
	}
	if got := loggedArgs(t, log); !slices.Equal(got, f.deleteArgs) {
		t.Errorf("argv =\n  %q\nwant\n  %q", got, f.deleteArgs)
	}
}

func TestAnAbsentHelperBinaryIsErrNoKeychainWithAdvice(t *testing.T) {
	f := fixture(t)
	emptyPath(t)
	store := platformStoreOrFail(t)

	if _, err := store.Get(secretAccount(shelloutHost)); !errors.Is(err, errNoKeychain) {
		t.Errorf("Get err = %v, want errNoKeychain", err)
	}
	if err := store.Delete(secretAccount(shelloutHost)); !errors.Is(err, errNoKeychain) {
		t.Errorf("Delete err = %v, want errNoKeychain", err)
	}
	err := store.Set(secretAccount(shelloutHost), shelloutBlob)
	if !errors.Is(err, errNoKeychain) {
		t.Fatalf("Set err = %v, want errNoKeychain", err)
	}
	if !strings.Contains(err.Error(), f.missingBinAdvice) {
		t.Errorf("err = %q, want the remediation (%q)", err, f.missingBinAdvice)
	}
}

// The headless case, end to end through the real platform store: no helper on
// PATH, so the secrets go back into the session file, the user stays logged in,
// and the one warning names both the cause and the way to silence it.
func TestAutoFallsBackToTheFileAndWarnsWhenTheHelperIsAbsent(t *testing.T) {
	f := fixture(t)
	isolateHome(t)
	t.Setenv(CredentialStoreEnvVar, "auto")
	emptyPath(t)
	resetFallbackWarning()
	t.Cleanup(resetFallbackWarning)
	out := captureStderr(t)

	if err := SaveSession(validSession("prod")); err != nil {
		t.Fatalf("a machine with no keychain must still be able to log in: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(readSessionBytes(t), &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk["refreshToken"] != "refresh-abc" || onDisk["version"] != float64(sessionVersionInline) {
		t.Errorf("on disk: version %v, refreshToken %v — want the pre-existing inline layout",
			onDisk["version"], onDisk["refreshToken"])
	}
	if n := strings.Count(out.String(), "could not store session credentials"); n != 1 {
		t.Errorf("warned %d times, want exactly 1:\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), f.missingBinAdvice) {
		t.Errorf("the warning does not name the cause:\n%s", out.String())
	}
	if !strings.Contains(out.String(), CredentialStoreEnvVar+"=file") {
		t.Errorf("the warning does not name the escape hatch:\n%s", out.String())
	}
}

// ORQ_CREDENTIAL_STORE=keychain is a demand, not a preference: with no keychain
// to write to, the save fails and nothing is written — the point being that the
// secrets must not quietly land in the file the variable was set to avoid.
func TestKeychainModeFailsTheSaveRatherThanWritingSecretsInline(t *testing.T) {
	fixture(t)
	isolateHome(t)
	t.Setenv(CredentialStoreEnvVar, "keychain")
	emptyPath(t)

	err := SaveSession(validSession("prod"))
	if err == nil {
		t.Fatal("SaveSession succeeded with no keychain under ORQ_CREDENTIAL_STORE=keychain")
	}
	if !errors.Is(err, errNoKeychain) {
		t.Errorf("err = %v, want it to carry errNoKeychain", err)
	}
	if _, statErr := os.Stat(SessionFilePath()); !os.IsNotExist(statErr) {
		t.Errorf("a session file was written anyway (%v); the secrets must not be in it", statErr)
	}
}
