package launch

import (
	"os"
	"os/exec"
	"strings"
)

// RunChild execs the agent binary with inherited stdio and the plan's env
// merged over the parent environment (empty-string values are still set —
// claude needs ANTHROPIC_API_KEY=""). Returns the child's exit code;
// signal-killed children map to 128+signum on unix (see signal_unix.go).
func RunChild(binary string, args []string, env map[string]string) (int, error) {
	cmd := exec.Command(binary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = MergeEnv(os.Environ(), env)

	stop := forwardSignals(cmd)
	defer stop()

	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitCodeFromState(exitErr), nil
	}
	return -1, err
}

// MergeEnv overlays overrides onto base ("K=V" pairs), replacing any existing
// key. Empty-string values are preserved as explicit "K=".
func MergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if _, replaced := overrides[key]; !replaced {
			out = append(out, kv)
		}
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}
