package commands

import (
	"fmt"
	"os"

	isatty "github.com/mattn/go-isatty"
	bartolocli "github.com/orq-ai/bartolo/cli"
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
