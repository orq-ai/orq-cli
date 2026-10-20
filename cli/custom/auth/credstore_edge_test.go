package auth

import (
	"encoding/base64"
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
	// base64 of N raw bytes is 4*ceil(N/3); edge is where the first chunk fills
	// and the secret spills into a continuation.
	edge := chunk0Budget(host) / 4 * 3
	for _, n := range []int{0, 1, 2, 3, 100, edge - 1, edge, edge + 1, edge + 2, edge + 3, 6000, 20000} {
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

// chunk0Budget is the base64 characters chunk 0 carries for host: the line limit
// less the command's overhead and the header.
func chunk0Budget(host string) int {
	return maxKeychainCommand - len(addGenericPasswordLine(keychainService, host, "")) - chunkHeaderLen
}

// TestMacKeychainDegenerateNamesRefused pins the exact budget-zero boundary: a
// host whose chunk-0 overhead is exactly maxKeychainCommand (budget 0) is refused
// even for an empty secret, and one byte shorter (budget 1) saves. The empty
// secret is what catches a guard loosened to budget < 0: at budget 0 an empty
// value would otherwise fit and save.
func TestMacKeychainDegenerateNamesRefused(t *testing.T) {
	withFakeKeychain(t, 44)
	// Budget with an empty host; the host length then dials it precisely.
	fixed := chunk0Budget("")

	atLimit := strings.Repeat("a", fixed) // budget 0
	for _, secret := range []string{"", "secret"} {
		if err := (macKeychainStore{}).Save(atLimit, secret); !errors.Is(err, errSecretTooLarge) {
			t.Fatalf("budget 0, secret %q: got %v, want errSecretTooLarge", secret, err)
		}
	}

	justUnder := strings.Repeat("a", fixed-1) // budget 1
	if err := (macKeychainStore{}).Save(justUnder, ""); err != nil {
		t.Fatalf("budget 1: Save failed: %v", err)
	}
}

// TestMacKeychainChunkSplitIsInclusive pins the <= at the chunk split: a secret
// whose base64 is exactly the chunk-0 budget must stay ONE item. If the split
// used < instead of <=, that exact-fit secret would spill a second chunk.
func TestMacKeychainChunkSplitIsInclusive(t *testing.T) {
	// Pick a host whose chunk-0 budget is a multiple of 4, so a base64 payload can
	// land exactly on it. base64 of 3*k raw bytes is 4*k chars with no padding.
	host := "orq.test1"
	for chunk0Budget(host)%4 != 0 {
		host += "x"
	}
	budget := chunk0Budget(host)
	raw := budget / 4 * 3 // base64 is exactly `budget` chars

	fk := withFakeKeychain(t, 44)
	if err := (macKeychainStore{}).Save(host, strings.Repeat("x", raw)); err != nil {
		t.Fatal(err)
	}
	if _, ok := fk.items[host+".1"]; ok {
		t.Fatal("exact-budget secret spilled into a continuation; the split must be <=, not <")
	}
	if fk.maxStdinLine != maxKeychainCommand-1 { // the line less its newline
		t.Fatalf("exact-budget line is %d bytes, want %d", fk.maxStdinLine, maxKeychainCommand-1)
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

	// A gap (a missing middle chunk) must never read back, whatever chunk 0's
	// length: the header's count catches it, not a lucky base64 decode. h.tes
	// gives a chunk 0 that decodes on its own; h.test one that does not.
	for _, host := range []string{"h.test", "h.tes"} {
		fk2 := withFakeKeychain(t, 44)
		if err := (macKeychainStore{}).Save(host, strings.Repeat("y", 20000)); err != nil {
			t.Fatal(err)
		}
		delete(fk2.items, host+".1") // punch a hole right after chunk 0
		got, ok, err := (macKeychainStore{}).Load(host)
		if !errors.Is(err, errKeychainSecretCorrupt) || ok {
			t.Fatalf("%s: a mid-store gap read back (len=%d ok=%v err=%v); want errKeychainSecretCorrupt", host, len(got), ok, err)
		}
	}

	// Chunks of two writes of the same size (a token refresh) join into valid
	// base64 of the right length, so only the checksum can catch them.
	fk3 := withFakeKeychain(t, 44)
	const host = "api.orq.ai"
	if err := (macKeychainStore{}).Save(host, strings.Repeat("a", 9000)); err != nil {
		t.Fatal(err)
	}
	oldTail := fk3.items[host+".1"]
	if err := (macKeychainStore{}).Save(host, strings.Repeat("b", 9000)); err != nil {
		t.Fatal(err)
	}
	fk3.items[host+".1"] = oldTail
	if _, ok, err := (macKeychainStore{}).Load(host); !errors.Is(err, errKeychainSecretCorrupt) || ok {
		t.Fatalf("chunks from two writes read back: ok=%v err=%v; want errKeychainSecretCorrupt", ok, err)
	}
}

// A one-item value written before the header existed still loads, alone: a
// stray continuation item is never joined onto it.
func TestMacKeychainLoadsAHeaderlessItem(t *testing.T) {
	fk := withFakeKeychain(t, 44)
	const host = "api.orq.ai"
	fk.items[host] = base64.StdEncoding.EncodeToString([]byte(`{"refreshToken":"r"}`))
	fk.items[host+".1"] = "QUJD"
	got, ok, err := (macKeychainStore{}).Load(host)
	if err != nil || !ok || got != `{"refreshToken":"r"}` {
		t.Fatalf("headerless item: got=%q ok=%v err=%v", got, ok, err)
	}
}

// When the leftover-chunk delete fails and so does the rollback of chunk 0, the
// error says stale items may remain, rather than dropping the rollback failure.
func TestMacKeychainShrinkRollbackFailureIsReported(t *testing.T) {
	fk := withFakeKeychain(t, 44)
	const host = "api.orq.ai"
	if err := (macKeychainStore{}).Save(host, strings.Repeat("A", 12000)); err != nil {
		t.Fatal(err)
	}
	runSecretTool = func(stdin, name string, args ...string) (string, string, error) {
		if name == "security" && len(args) > 0 && args[0] == "delete-generic-password" {
			return "", "User interaction is not allowed.", fakeExit{51}
		}
		return fk.run(stdin, name, args...)
	}
	err := (macKeychainStore{}).Save(host, "small")
	if err == nil || !strings.Contains(err.Error(), "stale orq-cli keychain items may remain") {
		t.Fatalf("want the rollback failure named, got %v", err)
	}
	// The header keeps the leftover tail out of any read: chunk 0 is the new
	// one-chunk value and Load never looks further.
	if got, ok, err := (macKeychainStore{}).Load(host); err != nil || !ok || got != "small" {
		t.Fatalf("Load after failed rollback: got=%q ok=%v err=%v", got, ok, err)
	}
}

// TestMacKeychainDeleteClearsOrphanedContinuations: a prior partial write can
// leave chunk 0 gone while continuation chunks remain. Delete must not stop at
// the missing chunk 0, it must still clear the continuations so logout never
// orphans base64 secret fragments.
func TestMacKeychainDeleteClearsOrphanedContinuations(t *testing.T) {
	fk := withFakeKeychain(t, 44)
	const host = "api.orq.ai"
	if err := (macKeychainStore{}).Save(host, strings.Repeat("z", 12000)); err != nil {
		t.Fatal(err)
	}
	if len(fk.items) < 3 {
		t.Fatalf("seed did not produce continuation chunks: %v", keysOf(fk.items))
	}
	delete(fk.items, host) // chunk 0 vanishes; continuations remain
	if err := (macKeychainStore{}).Delete(host); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(fk.items) != 0 {
		t.Fatalf("Delete left orphaned continuation chunks after chunk 0 was gone: %v", keysOf(fk.items))
	}
}
