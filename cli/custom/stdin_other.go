//go:build !unix

package custom

import "time"

// stdinPending has no non-destructive readiness test to use off unix, so a
// pipe is taken at its word: something may be coming, and reading it is
// bartolo's job either way.
func stdinPending(time.Duration) bool { return true }
