package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	isatty "github.com/mattn/go-isatty"
	bartolocli "github.com/orq-ai/bartolo/cli"
)

// splashWidth is the inner width of the setup box. Content lines are padded to
// it so the right border stays aligned whatever the version string looks like.
const splashWidth = 50

// printSplash draws the setup header. It is decoration: every failure mode
// degrades to less output, never to an error.
func printSplash(w io.Writer, version string) {
	if os.Getenv("ORQ_NO_SPLASH") != "" || os.Getenv("ORQ_SETUP_FROM_INSTALLER") != "" {
		return
	}
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return
	}
	if !supportsBoxDrawing() {
		fmt.Fprintf(w, "* orq.ai CLI %s - setup\n\n", version)
		return
	}
	lines := []string{
		fmt.Sprintf("  ◆  orq.ai CLI  ·  %s", version),
		"",
		"  Let's get you set up — 4 steps, ~30 seconds.",
		"  Ctrl-C any time. Nothing is written until a",
		"  step reports ✓.",
	}
	fmt.Fprintf(w, " ╭%s╮\n", strings.Repeat("─", splashWidth))
	for _, line := range lines {
		fmt.Fprintf(w, " │%s│\n", padDisplay(line, splashWidth))
	}
	fmt.Fprintf(w, " ╰%s╯\n\n", strings.Repeat("─", splashWidth))
}

// padDisplay right-pads to width counting runes, not bytes, so the multi-byte
// glyphs in the box do not shift the border.
func padDisplay(s string, width int) string {
	n := len([]rune(s))
	if n >= width {
		return string([]rune(s)[:width])
	}
	return s + strings.Repeat(" ", width-n)
}

// supportsBoxDrawing reports whether the terminal can be expected to render the
// UTF-8 art without turning it into mojibake.
func supportsBoxDrawing() bool {
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
