package auth

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// fakeExit carries an exit code the way *exec.ExitError does, so exitCode reads
// it and the stores can tell "not found" from a real failure in tests.
type fakeExit struct{ code int }

func (e fakeExit) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e fakeExit) ExitCode() int { return e.code }

// fakeKeychain stands in for the OS store: a map keyed by account (session
// host), driven through the same argv/stdin the real tools take, including the
// macOS `security -i` stdin command. notFound is the exit code each tool returns
// for a missing item (44 for security, 1 for secret-tool), so the store's own
// not-found handling is what gets exercised.
type fakeKeychain struct {
	items    map[string]string
	notFound int
	// maxStdinLine is the longest `security -i` stdin line the fake has seen,
	// recorded from the real command Save sends (not a test-side rebuild), so a
	// test can assert no line crosses the 4095-byte buffer even if the command
	// format changes.
	maxStdinLine int
}

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func (f *fakeKeychain) run(stdin, name string, args ...string) (string, string, error) {
	switch name {
	case "security":
		if len(args) > 0 && args[0] == "-i" {
			// Interactive: the command (with the secret) arrives on stdin. Record
			// the real line length so a test can pin the 4095-byte buffer.
			if n := len(strings.TrimRight(stdin, "\n")); n > f.maxStdinLine {
				f.maxStdinLine = n
			}
			toks := strings.Fields(stdin)
			if len(toks) > 0 && toks[0] == "add-generic-password" {
				f.items[argValue(toks, "-a")] = argValue(toks, "-w")
				return "", "", nil
			}
			return "", "unknown -i command", fakeExit{1}
		}
		sub := args[0]
		host := argValue(args, "-a")
		switch sub {
		case "find-generic-password":
			if v, ok := f.items[host]; ok {
				return v + "\n", "", nil // security -w appends a newline
			}
			return "", "", fakeExit{f.notFound}
		case "delete-generic-password":
			if _, ok := f.items[host]; !ok {
				return "", "", fakeExit{f.notFound}
			}
			delete(f.items, host)
			return "", "", nil
		}
	case "secret-tool":
		sub := args[0]
		host := argValue(args, "account")
		switch sub {
		case "lookup":
			if v, ok := f.items[host]; ok {
				return v, "", nil // no trailing newline
			}
			return "", "", fakeExit{f.notFound} // quiet: not found
		case "store":
			f.items[host] = stdin
			return "", "", nil
		case "clear":
			delete(f.items, host)
			return "", "", nil
		}
	}
	return "", "", fmt.Errorf("unexpected command: %s %v", name, args)
}

func withFakeKeychain(t *testing.T, notFound int) *fakeKeychain {
	t.Helper()
	fk := &fakeKeychain{items: map[string]string{}, notFound: notFound}
	prev := runSecretTool
	runSecretTool = fk.run
	t.Cleanup(func() { runSecretTool = prev })
	return fk
}

func testRoundTrip(t *testing.T, store SecretStore) {
	t.Helper()
	const host = "api.orq.ai"

	if _, ok, err := store.Load(host); err != nil || ok {
		t.Fatalf("empty store: Load ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	// A real secret blob: braces, quotes, and spaces, the exact shape the macOS
	// base64 + `security -i` path exists to carry safely.
	secret := `{"refreshToken":"rt 1 2","gatewayKey":"sk-a b\"c"}`
	if err := store.Save(host, secret); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := store.Load(host)
	if err != nil || !ok {
		t.Fatalf("after Save: Load ok=%v err=%v, want ok=true", ok, err)
	}
	if got != secret {
		t.Fatalf("round trip: got %q, want %q", got, secret)
	}

	if err := store.Delete(host); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := store.Load(host); ok {
		t.Fatalf("after Delete: Load ok=true, want false")
	}
	// Deleting a missing item is not an error.
	if err := store.Delete(host); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestMacKeychainRoundTrip(t *testing.T) {
	withFakeKeychain(t, 44)
	testRoundTrip(t, macKeychainStore{})
}

func TestSecretToolRoundTrip(t *testing.T) {
	withFakeKeychain(t, 1)
	testRoundTrip(t, secretToolStore{})
}

// A locked or unreachable store returns a non-quiet failure, which must surface
// as an error, not as "not found" — otherwise the caller blanks a live login.
func TestLoadSurfacesStoreErrorNotNotFound(t *testing.T) {
	prev := runSecretTool
	t.Cleanup(func() { runSecretTool = prev })

	// secret-tool: exit 1 WITH stderr means the service could not be reached.
	runSecretTool = func(stdin, name string, args ...string) (string, string, error) {
		return "", "Cannot autolaunch D-Bus without X11 $DISPLAY", fakeExit{1}
	}
	if _, ok, err := (secretToolStore{}).Load("h"); err == nil || ok {
		t.Fatalf("locked secret-tool: want error and ok=false, got ok=%v err=%v", ok, err)
	}

	// macOS: any non-44 failure is an error, not not-found.
	runSecretTool = func(stdin, name string, args ...string) (string, string, error) {
		return "", "User interaction is not allowed.", fakeExit{51}
	}
	if _, ok, err := (macKeychainStore{}).Load("h"); err == nil || ok {
		t.Fatalf("locked keychain: want error and ok=false, got ok=%v err=%v", ok, err)
	}
}

// secretToolReady must accept a clean exit 1 (the probe item is simply absent)
// and reject an exit 1 that carries stderr (a headless box where the Secret
// Service is not answering), the case the probe exists to catch.
func TestSecretToolReady(t *testing.T) {
	prev := runSecretTool
	t.Cleanup(func() { runSecretTool = prev })

	runSecretTool = func(_, _ string, _ ...string) (string, string, error) { return "", "", fakeExit{1} }
	if !secretToolReady() {
		t.Error("exit 1 with empty stderr (absent probe item) should read as ready")
	}

	runSecretTool = func(_, _ string, _ ...string) (string, string, error) {
		return "", "Cannot autolaunch D-Bus without X11 $DISPLAY", fakeExit{1}
	}
	if secretToolReady() {
		t.Error("exit 1 carrying stderr (no Secret Service) must not read as ready")
	}

	runSecretTool = func(_, _ string, _ ...string) (string, string, error) { return "x", "", nil }
	if !secretToolReady() {
		t.Error("a successful probe should read as ready")
	}
}

// A secret large enough to overflow security's interactive line buffer must be
// split across keychain items and round-trip intact, never truncated and never
// dropped to plaintext. Reproduces the multi-workspace blob case.
func TestMacKeychainChunksOversizeSecret(t *testing.T) {
	fk := withFakeKeychain(t, 44)
	const host = "api.orq.ai"
	secret := strings.Repeat("A", 6000) // base64 ~8000 bytes, past the 4095 line buffer

	if err := (macKeychainStore{}).Save(host, secret); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// It spilled into continuation items, and no command line Save actually sent
	// crossed security's 4095-byte buffer. Assert the real recorded line against
	// the literal 4095, not maxKeychainCommand: a constant set too high (say 5000)
	// would then be caught here instead of passing against its own value.
	if _, ok := fk.items[host+".1"]; !ok {
		t.Fatalf("oversize Save did not spill into a continuation chunk; items=%v", keysOf(fk.items))
	}
	if fk.maxStdinLine > 4095 {
		t.Fatalf("Save sent a %d-byte command line, over security's 4095 buffer", fk.maxStdinLine)
	}

	got, ok, err := (macKeychainStore{}).Load(host)
	if err != nil || !ok {
		t.Fatalf("Load after chunked Save: ok=%v err=%v", ok, err)
	}
	if got != secret {
		t.Fatalf("chunked round trip corrupted the secret (len got=%d want=%d)", len(got), len(secret))
	}

	// Saving a small secret clears the leftover continuation chunks.
	if err := (macKeychainStore{}).Save(host, "small"); err != nil {
		t.Fatalf("Save small: %v", err)
	}
	if _, ok := fk.items[host+".1"]; ok {
		t.Fatalf("shrinking Save left a stale continuation chunk; items=%v", keysOf(fk.items))
	}
	if back, _, _ := (macKeychainStore{}).Load(host); back != "small" {
		t.Fatalf("after shrink: Load = %q, want %q", back, "small")
	}

	// Delete removes every chunk.
	if err := (macKeychainStore{}).Delete(host); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(fk.items) != 0 {
		t.Fatalf("Delete left items behind: %v", keysOf(fk.items))
	}
}

// A write that fails partway through a multi-chunk secret must leave nothing
// behind: not a half-written secret, and not stale tail chunks from a previous
// larger secret (logout's Delete stops at the first gap and would orphan them).
func TestMacKeychainSaveFailureLeavesNoOrphanChunks(t *testing.T) {
	fk := withFakeKeychain(t, 44)
	const host = "api.orq.ai"
	big := strings.Repeat("A", 12000) // several chunks

	if err := (macKeychainStore{}).Save(host, big); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	if len(fk.items) < 3 {
		t.Fatalf("seed did not produce multiple chunks: %v", keysOf(fk.items))
	}

	// Re-save, failing the add on the second chunk (index 1). A naive 0..i
	// cleanup would strand the chunks from index 2 up that the seed wrote.
	addCalls := 0
	runSecretTool = func(stdin, name string, args ...string) (string, string, error) {
		if name == "security" && len(args) > 0 && args[0] == "-i" {
			defer func() { addCalls++ }()
			if addCalls == 1 {
				return "", "boom", fakeExit{1}
			}
		}
		return fk.run(stdin, name, args...)
	}
	if err := (macKeychainStore{}).Save(host, big); err == nil {
		t.Fatal("re-save succeeded, want the injected add failure")
	}
	if len(fk.items) != 0 {
		t.Fatalf("failed Save left orphan chunks: %v", keysOf(fk.items))
	}
	if _, ok, err := (macKeychainStore{}).Load(host); ok || err != nil {
		t.Fatalf("Load after cleanup: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

// Shrinking a secret clears the leftover tail chunks. If one of those deletes
// fails for a reason other than not-found (keychain locked), the stale tail must
// not be left where Load would stitch it onto the new, shorter value and decode
// garbage. Save must fail and Load must report no secret, so the caller keeps the
// 0600 file.
func TestMacKeychainShrinkWithUndeletableTailFailsClean(t *testing.T) {
	fk := withFakeKeychain(t, 44)
	const host = "api.orq.ai"
	big := strings.Repeat("A", 12000) // several chunks

	if err := (macKeychainStore{}).Save(host, big); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	// Re-save a one-chunk secret. The add succeeds; the leftover-clear loop then
	// hits a locked keychain (exit 51) deleting the first tail chunk, host.1.
	runSecretTool = func(stdin, name string, args ...string) (string, string, error) {
		if name == "security" && len(args) > 0 && args[0] == "delete-generic-password" {
			if argValue(args, "-a") == host+".1" {
				return "", "User interaction is not allowed.", fakeExit{51}
			}
		}
		return fk.run(stdin, name, args...)
	}
	if err := (macKeychainStore{}).Save(host, "small"); err == nil {
		t.Fatal("shrink Save succeeded, want the injected delete failure")
	}
	// Chunk 0 was rolled back, so Load stops at the missing chunk 0 and never
	// joins the stale tail into a corrupt value.
	if _, ok := fk.items[host]; ok {
		t.Fatalf("chunk 0 survived a failed shrink; Load would read a corrupt join; items=%v", keysOf(fk.items))
	}
	if got, ok, err := (macKeychainStore{}).Load(host); ok || err != nil {
		t.Fatalf("Load after failed shrink: got=%q ok=%v err=%v, want ok=false err=nil", got, ok, err)
	}
}

func keysOf(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// When security rejects the interactive command it can echo the command line,
// which carries the base64 secret. The Save error must never include it.
func TestMacKeychainSaveErrorOmitsSecret(t *testing.T) {
	prev := runSecretTool
	t.Cleanup(func() { runSecretTool = prev })
	const secret = "super-secret-refresh-token-value"
	runSecretTool = func(stdin, name string, args ...string) (string, string, error) {
		// security echoing the rejected command (which contains the base64 blob).
		return "", `security: unknown command "` + stdin + `"`, fakeExit{1}
	}
	err := (macKeychainStore{}).Save("api.orq.ai", secret)
	if err == nil {
		t.Fatal("want an error from a failing security -i")
	}
	enc := base64.StdEncoding.EncodeToString([]byte(secret))
	if strings.Contains(err.Error(), enc) || strings.Contains(err.Error(), secret) {
		t.Fatalf("Save error leaked the secret: %q", err.Error())
	}
}

func TestResolveSecretStoreEscapeHatch(t *testing.T) {
	for _, v := range []string{"file", "FILE", " file "} {
		env := func(k string) string {
			if k == CredentialStoreEnv {
				return v
			}
			return ""
		}
		if s := ResolveSecretStore(env); s != nil {
			t.Fatalf("value %q: want file fallback, got %q", v, s.Name())
		}
	}
}
