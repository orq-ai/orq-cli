//go:build !windows

package launch

import "testing"

func TestRunChildExitCode(t *testing.T) {
	code, err := RunChild("/bin/sh", []string{"-c", "exit 2"}, nil)
	if err != nil || code != 2 {
		t.Fatalf("code=%d err=%v", code, err)
	}

	code, err = RunChild("/bin/sh", []string{"-c", "exit 0"}, map[string]string{"X": "1"})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestRunChildEnvOverride(t *testing.T) {
	// Child sees the override, including explicit empty values.
	code, err := RunChild("/bin/sh", []string{"-c", `[ "$FOO" = "bar" ] && [ -z "$EMPTY" ] && [ "${EMPTY+set}" = "set" ]`},
		map[string]string{"FOO": "bar", "EMPTY": ""})
	if err != nil || code != 0 {
		t.Fatalf("env not delivered: code=%d err=%v", code, err)
	}
}
