package commands

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	isatty "github.com/mattn/go-isatty"
	bartolocli "github.com/orq-ai/bartolo/cli"
)

// orqLogo is the same block mark install.sh prints, so the binary the installer
// drops in keeps the identity the installer introduced. The trailing line takes
// a suffix (product name, version) the way install.sh appends "CLI installer".
var orqLogo = []string{
	`  ██████╗ ██████╗  ██████╗`,
	` ██╔═══██╗██╔══██╗██╔═══██╗`,
	` ██║   ██║██████╔╝██║   ██║`,
	` ██║   ██║██╔══██╗██║▄▄ ██║`,
	` ╚██████╔╝██║  ██║╚██████╔╝`,
	`  ╚═════╝ ╚═╝  ╚═╝ ╚══▀▀═╝`,
}

// printSplash draws the setup header. It is decoration: every failure mode
// degrades to less output, never to an error.
func printSplash(w io.Writer, version string) {
	if os.Getenv("ORQ_NO_SPLASH") != "" || os.Getenv("ORQ_SETUP_FROM_INSTALLER") != "" {
		return
	}
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return
	}
	if !supportsArt() {
		fmt.Fprintf(w, "* orq.ai CLI %s - setup\n\n", version)
		return
	}
	fmt.Fprintln(w)
	for i, line := range orqLogo {
		if i == len(orqLogo)-1 {
			fmt.Fprintf(w, "%s   CLI  ·  %s\n", line, version)
			continue
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "\n  Let's get you set up — %d steps.\n", setupSteps)
	fmt.Fprint(w, "  Ctrl-C any time. Nothing is written until a step reports ✓.\n\n")
}

// supportsArt reports whether the terminal can be expected to render the UTF-8
// art without turning it into mojibake. Mirrors install.sh's supports_art.
func supportsArt() bool {
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		return false
	}
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := os.Getenv(key); v != "" {
			return strings.Contains(strings.ToUpper(v), "UTF-8") ||
				strings.Contains(strings.ToUpper(v), "UTF8")
		}
	}
	return false
}

// ============================================================================
// Step reporting
// ============================================================================

// reporter writes the human-facing progress lines. Everything it prints goes to
// stderr so the emit() payload on stdout stays machine-readable.
type reporter struct {
	w     io.Writer
	quiet bool
}

func newReporter(quiet bool) *reporter {
	return &reporter{w: bartolocli.Stderr, quiet: quiet}
}

func (r *reporter) step(n int, total int, title string) {
	if r.quiet {
		return
	}
	fmt.Fprintf(r.w, "\nStep %d/%d  %s\n", n, total, title)
}

func (r *reporter) ok(format string, args ...any) {
	if r.quiet {
		return
	}
	fmt.Fprintf(r.w, "✓ "+format+"\n", args...)
}

func (r *reporter) warn(format string, args ...any) {
	fmt.Fprintf(r.w, "! "+format+"\n", args...)
}

func (r *reporter) fail(format string, args ...any) {
	fmt.Fprintf(r.w, "✗ "+format+"\n", args...)
}

func (r *reporter) note(format string, args ...any) {
	if r.quiet {
		return
	}
	fmt.Fprintf(r.w, "  "+format+"\n", args...)
}

// busy animates a spinner line until the returned stop func is called, then
// erases it so the caller's ✓/! line takes its place. Non-TTY (CI, pipes)
// degrades to a single static line.
func (r *reporter) busy(format string, args ...any) (stop func()) {
	msg := fmt.Sprintf(format, args...)
	if r.quiet {
		return func() {}
	}
	if f, ok := r.w.(*os.File); !ok || !isatty.IsTerminal(f.Fd()) {
		fmt.Fprintf(r.w, "  %s\n", msg)
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		fmt.Fprintf(r.w, "%c %s", frames[0], msg)
		for {
			select {
			case <-done:
				fmt.Fprintf(r.w, "\r%s\r", strings.Repeat(" ", len([]rune(msg))+2))
				return
			case <-ticker.C:
				i++
				fmt.Fprintf(r.w, "\r%c %s", frames[i%len(frames)], msg)
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
