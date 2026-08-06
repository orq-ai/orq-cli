package commands

import (
	"fmt"
	"os"

	isatty "github.com/mattn/go-isatty"
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Human-facing status lines. These go to STDERR so the structured result on
// stdout (toon/json/yaml) stays clean for scripts, and they only render on an
// interactive terminal — a piped or --json invocation sees none of this. Color
// follows the same --no-color / NO_COLOR rules as the rest of the CLI.

const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiDim    = "\033[2m"
)

// humanOutput reports whether stdout is an interactive terminal, i.e. a person
// is watching rather than a pipe or file consuming structured output.
func humanOutput() bool {
	return isatty.IsTerminal(os.Stdout.Fd())
}

// wantsHumanView reports whether a command should render its friendly view
// instead of the structured payload: a person at a terminal who did not ask
// for a machine format. Scripts (non-TTY) and explicit --json/-o always get
// the structured output, so nothing automated changes.
func wantsHumanView(cmd *cobra.Command) bool {
	return humanOutput() && !machineFormatRequested(cmd)
}

// machineFormatRequested reports whether the user explicitly asked for a
// machine format via --json or -o/--output-format.
func machineFormatRequested(cmd *cobra.Command) bool {
	if f := cmd.Flags().Lookup("json"); f != nil && f.Changed {
		return true
	}
	if f := cmd.Flags().Lookup("output-format"); f != nil && f.Changed {
		return true
	}
	return false
}

// bold wraps a string in the bold SGR when color is enabled.
func bold(s string) string { return paint("\033[1m", s) }

// heading prints a bold section title to stdout, the human view's primary sink.
func heading(s string) {
	fmt.Fprintln(bartolocli.Stdout, bold(s))
}

// kv prints an aligned "label: value" row under a heading. width pads the
// label column so a block of rows lines up.
func kv(width int, label, format string, args ...any) {
	value := fmt.Sprintf(format, args...)
	fmt.Fprintf(bartolocli.Stdout, "  %s  %s\n", paint(ansiDim, pad(label+":", width+1)), value)
}

func pad(s string, width int) string {
	for len(s) < width {
		s += " "
	}
	return s
}

func useColor() bool {
	if viper.GetBool("no-color") || os.Getenv("NO_COLOR") != "" {
		return false
	}
	return humanOutput()
}

func paint(color, s string) string {
	if !useColor() {
		return s
	}
	return color + s + ansiReset
}

// success prints a green check line, e.g. after login or workspace switch.
func success(format string, args ...any) {
	if !humanOutput() {
		return
	}
	fmt.Fprintln(bartolocli.Stderr, paint(ansiGreen, "✓ ")+fmt.Sprintf(format, args...))
}

// info prints a dimmed, unmarked line for secondary context.
func info(format string, args ...any) {
	if !humanOutput() {
		return
	}
	fmt.Fprintln(bartolocli.Stderr, paint(ansiDim, fmt.Sprintf(format, args...)))
}

// Warn prints a yellow "warning:" line to stderr. Unlike success/info it is not
// TTY-gated: a shadowed-config warning matters to scripts too. Exported so the
// custom package (PreRun) shares one warning style with the commands.
func Warn(format string, args ...any) {
	fmt.Fprintln(bartolocli.Stderr, paint(ansiYellow, "warning: ")+fmt.Sprintf(format, args...))
}

// statusGlyph maps a doctor check status to a colored marker.
func statusGlyph(status string) string {
	switch status {
	case "pass":
		return paint(ansiGreen, "✓")
	case "warn":
		return paint(ansiYellow, "!")
	case "fail":
		return paint(ansiRed, "✗")
	default:
		return "-"
	}
}
