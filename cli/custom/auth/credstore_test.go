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
// host), driven through the same argv/stdin the real tools take. notFound is
// the exit code each tool returns for a missing item (44 for security, 1 for
// secret-tool), so the store's own not-found handling is what gets exercised.
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

// positional returns the value following key in a `key value key value` list
// (secret-tool's attribute form).
func positional(args []string, key string) string {
	for i, a := range args {
		if a == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func (f *fakeKeychain) run(stdin, name string, args ...string) (string, error) {
	switch name {
	case "security":
		sub := args[0]
		host := argValue(args, "-a")
		switch sub {
		case "find-generic-password":
			if v, ok := f.items[host]; ok {
				return v + "\n", nil // security -w appends a newline
			}
			return "", fakeExit{f.notFound}
		case "add-generic-password":
			f.items[host] = argValue(args, "-w")
			return "", nil
		case "delete-generic-password":
			if _, ok := f.items[host]; !ok {
				return "", fakeExit{f.notFound}
			}
			delete(f.items, host)
			return "", nil
		}
	case "secret-tool":
		sub := args[0]
		host := positional(args, "account")
		switch sub {
		case "lookup":
			if v, ok := f.items[host]; ok {
				return v, nil // no trailing newline
			}
			return "", fakeExit{f.notFound}
		case "store":
			f.items[host] = stdin
			return "", nil
		case "clear":
			delete(f.items, host)
			return "", nil
		}
	}
	return "", fmt.Errorf("unexpected command: %s %v", name, args)
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

	secret := `{"refreshToken":"rt-123","gatewayKey":"sk-abc"}`
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

func TestMacKeychainTrimsTrailingNewline(t *testing.T) {
	withFakeKeychain(t, 44)
	store := macKeychainStore{}
	if err := store.Save("h", "secret-value"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Load("h")
	if got != "secret-value" || strings.Contains(got, "\n") {
		t.Fatalf("got %q, want the value with no trailing newline", got)
	}
}

func TestResolveSecretStoreEscapeHatch(t *testing.T) {
	env := func(k string) string {
		if k == CredentialStoreEnv {
			return "file"
		}
		return ""
	}
	if s := ResolveSecretStore(env); s != nil {
		t.Fatalf("ORQ_CREDENTIAL_STORE=file should force the file fallback, got %q", s.Name())
	}
	// Case-insensitive, trimmed.
	for _, v := range []string{"FILE", " file "} {
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
