//go:build unix

package custom

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// stdinPending reports whether a pipe, FIFO or socket on stdin is ready to be
// read within grace. It polls rather than reads: bartolo still does the
// reading, and a byte this took would be a byte missing from the body.
//
// An open pipe nobody has written to yet is what CI runners, task runners and
// subprocess.Popen hand a child by default, and it is not a body. A writer
// that closed without writing reads as ready here, the same way bartolo reads
// it as an empty body — poll cannot tell that case from a pending byte, and
// the ioctl that could is not portable across the platforms this ships on.
func stdinPending(grace time.Duration) bool {
	fds := []unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}
	ready, err := unix.Poll(fds, int(grace.Milliseconds()))
	if err != nil {
		// A poll this call could not complete says nothing about the body;
		// assume one rather than rewriting what may be there.
		return true
	}
	return ready > 0
}
