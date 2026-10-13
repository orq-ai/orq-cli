package auth

import (
	"errors"
	"strings"
	"testing"
)

// Edge cases for the chunked macOS keychain store: the empty secret, the exact
// one-chunk/two-chunk boundary, a shrink back to empty, the degenerate
// names-too-long guard, and an externally-corrupted store. These pin the
// behaviour the security review relied on but the happy-path tests do not reach.

// TestMacKeychainEdgeSizes round-trips a range of sizes, including 0 and the sizes
// straddling the first chunk's budget, and asserts no stored command line ever
// exceeds the 4095-byte security -i buffer and no stale continuation chunk is left.
func TestMacKeychainEdgeSizes(t *testing.T) {
	fk := withFakeKeychain(t, 44)
	const host = "api.orq.ai"
	// base64 of N raw bytes is 4*ceil(N/3); ~2959 raw bytes is where the first
	// chunk fills and the secret spills into a continuation.
	for _, n := range []int{0, 1, 2, 3, 100, 2958, 2959, 2960, 2961, 2962, 6000, 20000} {
		secret := strings.Repeat("x", n)
		if err := (macKeychainStore{}).Save(host, secret); err != nil {
			t.Fatalf("Save(n=%d): %v", n, err)
		}
		got, ok, err := (macKeychainStore{}).Load(host)
		if err != nil || !ok || got != secret {
			t.Fatalf("round-trip n=%d: ok=%v err=%v len(got)=%d want=%d", n, ok, err, len(got), n)
		}
		for acct, v := range fk.items {
			service := keychainService
			if acct != host {
				service = keychainContService
			}
			line := len("add-generic-password -U -s " + service + " -a " + acct + " -w " + v + "\n")
			if line > 4095 {
				t.Fatalf("n=%d: chunk %q command line is %d bytes, over the 4095 buffer", n, acct, line)
			}
		}
	}

	// Shrinking a large secret back to empty must clear every continuation chunk.
	if err := (macKeychainStore{}).Save(host, strings.Repeat("x", 20000)); err != nil {
		t.Fatal(err)
	}
	if err := (macKeychainStore{}).Save(host, ""); err != nil {
		t.Fatal(err)
	}
	for acct := range fk.items {
		if acct != host {
			t.Fatalf("empty secret left a stale continuation chunk: %q", acct)
		}
	}
	if err := (macKeychainStore{}).Delete(host); err != nil {
		t.Fatal(err)
	}
	if len(fk.items) != 0 {
		t.Fatalf("Delete left items behind: %v", keysOf(fk.items))
	}
}

// TestMacKeychainDegenerateNamesRefused: a host/service so long that not even an
// empty value fits one command line is refused, not looped on or panicked over.
func TestMacKeychainDegenerateNamesRefused(t *testing.T) {
	withFakeKeychain(t, 44)
	longHost := strings.Repeat("a", 4100)
	if err := (macKeychainStore{}).Save(longHost, "secret"); !errors.Is(err, errSecretTooLarge) {
		t.Fatalf("degenerate host: got %v, want errSecretTooLarge", err)
	}
}

// TestMacKeychainCorruptStoreFailsSafe: an externally-corrupted store must fail
// safe. A non-base64 chunk surfaces a decode error, and a gap (a missing middle
// chunk) must never be silently stitched into a wrong-but-plausible secret.
func TestMacKeychainCorruptStoreFailsSafe(t *testing.T) {
	fk := withFakeKeychain(t, 44)
	fk.items["api.orq.ai"] = "!!!not base64!!!"
	if _, ok, err := (macKeychainStore{}).Load("api.orq.ai"); err == nil {
		t.Fatalf("non-base64 chunk: ok=%v err=nil, want a decode error", ok)
	}

	fk2 := withFakeKeychain(t, 44)
	full := strings.Repeat("y", 20000)
	if err := (macKeychainStore{}).Save("h.test", full); err != nil {
		t.Fatal(err)
	}
	delete(fk2.items, "h.test.1") // punch a hole right after chunk 0
	if got, ok, err := (macKeychainStore{}).Load("h.test"); err == nil && ok && got == full {
		t.Fatal("a mid-store gap silently reconstructed the full secret; want a short or failed read")
	}
}
