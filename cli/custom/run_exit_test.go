package custom

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

// run.go must route the top-level error through bartolo's exit-code mapping, so
// a usage error (bad flag value, unknown command) exits 2 and a genuine request
// failure exits 1 — the distinction scripts branch on. Pins RES-1502 so a future
// run.go edit cannot flatten usage errors back to 1.
func TestExitCodeUsageVsFailure(t *testing.T) {
	bin := buildOrqBinary(t)
	home := t.TempDir()

	exitCodeOf := func(t *testing.T, env []string, args ...string) int {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		err := cmd.Run()
		if err == nil {
			return 0
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		t.Fatalf("running %v: %v", args, err)
		return -1
	}

	noAuth := append(os.Environ(), "HOME="+home, "ORQ_API_KEY=", "ORQ_TOKEN=", "ORQ_AUTHORIZATION=", "ORQ_PROFILE=")

	// Usage errors -> 2. A bad --from value fails bartolo's local validation
	// before any request (the ExecuteContextC path), and an unknown command is
	// cobra's own usage error.
	if code := exitCodeOf(t, noAuth, "traces", "search", "--from", "banana", "--to", "now"); code != 2 {
		t.Errorf("bad --from value exit = %d, want 2 (usage error)", code)
	}
	if code := exitCodeOf(t, noAuth, "definitely-not-a-real-command"); code != 2 {
		t.Errorf("unknown command exit = %d, want 2 (usage error)", code)
	}
	// A destructive command without --force in a non-interactive shell is
	// refused before any request (a *UsageError), so it exits 2. This is the
	// RES-1502 acceptance path most likely to regress, since it depends on the
	// refusal staying a usage error rather than a plain failure.
	if code := exitCodeOf(t, noAuth, "datasets", "delete", "abc", "--no-input"); code != 2 {
		t.Errorf("destructive delete without --force exit = %d, want 2 (usage error)", code)
	}

	// A genuine request failure (dead server, with a key so it skips the session
	// and calls the API) stays 1 — not flattened into the usage code.
	dead := append(os.Environ(), "HOME="+home, "ORQ_API_KEY=sk-dummy",
		"ORQ_SERVER=http://127.0.0.1:1", "ORQ_TOKEN=", "ORQ_AUTHORIZATION=", "ORQ_PROFILE=")
	if code := exitCodeOf(t, dead, "-o", "json", "projects", "list"); code != 1 {
		t.Errorf("connection-refused exit = %d, want 1 (request failure)", code)
	}
}
