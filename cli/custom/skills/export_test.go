package skills

import "testing"

// SetFingerprintForTest makes a test look like a different binary, which is how
// the update path is exercised without rebuilding. It lives in a _test.go file
// (rather than embed.go) so the "testing" package it depends on for *testing.T
// and t.Cleanup never gets linked into the shipped release binary.
func SetFingerprintForTest(t *testing.T, fp string) {
	t.Helper()
	prev := testOverride
	testOverride = fp
	t.Cleanup(func() { testOverride = prev })
}
