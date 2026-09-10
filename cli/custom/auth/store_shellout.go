//go:build darwin || linux

package auth

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// keychainService is the service name every item this CLI stores is filed
// under, on both platforms that have a store. It mirrors bt's
// com.braintrust.bt.cli. Renaming it after release orphans every item already
// written, so it is one constant read from one place.
const keychainService = "ai.orq.cli"

// keychainItemLabel is what the item is called in Keychain Access and in
// Seahorse, for a user who goes looking for it by hand.
const keychainItemLabel = "orq CLI session"

// keychainTool is one platform helper binary — security(1) on macOS,
// secret-tool(1) on Linux — plus the one line of advice to give when it is not
// installed. Shelling out rather than linking Security.framework or libsecret
// is what keeps CGO_ENABLED=0 across all five release targets
// (scripts/release-build.sh) and adds no dependency to either module.
type keychainTool struct {
	bin    string
	advice string
}

// toolResult is one completed invocation.
//
// A non-zero code is deliberately not an error: on both platforms it is also
// how the tool says "no such item", which the store has to translate into
// ("", nil). Only the store knows which codes mean what, so run hands the raw
// result back rather than guessing here.
type toolResult struct {
	stdout string
	stderr string
	code   int
}

// run invokes the helper with the given arguments, feeding secret (when
// non-empty) on stdin, and returns an error only when the tool could not be run
// at all — which is a store that does not exist, hence errNoKeychain.
func (t keychainTool) run(args []string, stdin string) (toolResult, error) {
	if _, err := exec.LookPath(t.bin); err != nil {
		return toolResult{}, fmt.Errorf("%w: %s", errNoKeychain, t.advice)
	}
	cmd := exec.Command(t.bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err := cmd.Run()
	res := toolResult{stdout: out.String(), stderr: strings.TrimSpace(errOut.String())}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			// Not "the tool said no" but "the tool never ran": a permission
			// problem on the binary, a fork failure. There is no store here
			// either, so it degrades the same way.
			return res, fmt.Errorf("%w: could not run %s: %v", errNoKeychain, t.bin, err)
		}
		res.code = exit.ExitCode()
	}
	return res, nil
}

// secret is the item's value as the tool printed it. security(1) always appends
// a newline to `-w` output and secret-tool appends one only on a TTY; a secrets
// blob is JSON, which never ends in a newline, so trimming cannot truncate one.
func (r toolResult) secret() string {
	return strings.TrimRight(r.stdout, "\r\n")
}

// detail is the most useful thing the tool said about a failure, so a message
// reaching the user names a cause rather than a bare exit code.
func (r toolResult) detail() string {
	if r.stderr != "" {
		return r.stderr
	}
	return fmt.Sprintf("exit status %d", r.code)
}
