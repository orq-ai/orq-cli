package auth

import (
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
			// Interactive: the command (with the secret) arrives on stdin.
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
