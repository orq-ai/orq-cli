//go:build windows

package launch

import (
	"os"
	"os/exec"
	"os/signal"
)

// forwardSignals on Windows only ignores Ctrl+C in the parent; the console
// delivers it to the child directly.
func forwardSignals(cmd *exec.Cmd) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

func exitCodeFromState(err *exec.ExitError) int {
	return err.ExitCode()
}
