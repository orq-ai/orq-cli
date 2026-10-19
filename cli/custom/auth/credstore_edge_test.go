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
		// Assert the longest line Save actually sent, recorded by the fake from the
		// real command, not a line rebuilt from a test-side copy of the format.
		if fk.maxStdinLine > 4095 {
			t.Fatalf("n=%d: Save sent a %d-byte command line, over the 4095 buffer", n, fk.maxStdinLine)
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

// TestMacKeychainDegenerateNamesRefused pins the exact budget-zero boundary: a
// host whose chunk-0 overhead is exactly maxKeychainCommand (budget 0) is refused,
// and one byte shorter (budget 1) saves. A far-too-long host would pass a sloppy
// budget < 0 guard too, so it would not catch the off-by-one the == 0 case does.
func TestMacKeychainDegenerateNamesRefused(t *testing.T) {
	withFakeKeychain(t, 44)
	// Overhead with an empty host; the host length then dials budget precisely.
	fixed := len(addGenericPasswordLine(keychainService, "", ""))

	atLimit := strings.Repeat("a", maxKeychainCommand-fixed) // overhead == limit, budget 0
	if err := (macKeychainStore{}).Save(atLimit, "secret"); !errors.Is(err, errSecretTooLarge) {
		t.Fatalf("overhead == maxKeychainCommand (budget 0): got %v, want errSecretTooLarge", err)
	}

	justUnder := strings.Repeat("a", maxKeychainCommand-fixed-1) // budget 1
	if err := (macKeychainStore{}).Save(justUnder, ""); err != nil {
		t.Fatalf("overhead == maxKeychainCommand-1 (budget 1): Save failed: %v", err)
	}
}

// TestMacKeychainChunkSplitIsInclusive pins the <= at the chunk split: a secret
// whose base64 is exactly the chunk-0 budget must stay ONE item. If the split
// used < instead of <=, that exact-fit secret would spill a second chunk.
func TestMacKeychainChunkSplitIsInclusive(t *testing.T) {
	// Pick a host whose chunk-0 budget is a multiple of 4, so a base64 payload can
	// land exactly on it. base64 of 3*k raw bytes is 4*k chars with no padding.
	host := "orq.test1" // 9 chars
	budget := maxKeychainCommand - len(addGenericPasswordLine(keychainService, host, ""))
	if budget%4 != 0 {
		t.Fatalf("test host gives budget %d; pick one whose budget is a multiple of 4", budget)
	}
	raw := budget / 4 * 3 // base64 is exactly `budget` chars

	fk := withFakeKeychain(t, 44)
	if err := (macKeychainStore{}).Save(host, strings.Repeat("x", raw)); err != nil {
		t.Fatal(err)
	}
	if _, ok := fk.items[host+".1"]; ok {
		t.Fatal("exact-budget secret spilled into a continuation; the split must be <=, not <")
	}
	if got, ok, err := (macKeychainStore{}).Load(host); err != nil || !ok || len(got) != raw {
		t.Fatalf("exact-budget round trip: ok=%v err=%v len=%d want=%d", ok, err, len(got), raw)
	}

	// One raw byte more pushes base64 past the budget and must spill.
	fk2 := withFakeKeychain(t, 44)
	if err := (macKeychainStore{}).Save(host, strings.Repeat("x", raw+1)); err != nil {
		t.Fatal(err)
	}
	if _, ok := fk2.items[host+".1"]; !ok {
		t.Fatal("budget+1 secret did not spill into a continuation chunk")
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
	// A gap must never yield a successful read: not the full secret, and not a
	// silent partial either. Load stops at the missing chunk 1 and is left with
	// chunk 0 alone, whose length (the chunk-0 budget) is not a multiple of 4, so
	// the base64 decode fails rather than returning a short, wrong-but-plausible
	// value. Assert the safe outcome directly (err or ok=false), not an equality
	// that only holds because of this host's chunk-0 length.
	if got, ok, err := (macKeychainStore{}).Load("h.test"); err == nil && ok {
		t.Fatalf("a mid-store gap produced a successful read (len=%d); want an error or ok=false", len(got))
	}
}
