//go:build !windows

package launch

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// forwardSignals wires parent signals for an interactive child: SIGINT and
// SIGQUIT are swallowed (the terminal already delivers them to the child's
// process group); SIGTERM and SIGHUP are forwarded so daemon-style kills reach
// the agent. Returns a stop func.
func forwardSignals(cmd *exec.Cmd) func() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-ch:
				switch sig {
				case syscall.SIGTERM, syscall.SIGHUP:
					if cmd.Process != nil {
						_ = cmd.Process.Signal(sig)
					}
				}
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

// exitCodeFromState maps a finished child to an exit code; signal-killed
// children become 128+signum, matching shell convention.
func exitCodeFromState(err *exec.ExitError) int {
	if status, ok := err.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return err.ExitCode()
}
