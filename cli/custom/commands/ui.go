package commands

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

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

// Brand palette (orq.ai Brand Guidelines v1.0): Pulse Orange #DF5325 and
// Glowing Turquoise #00FFDD carry the identity. Status semantics map onto
// them where the hue still reads right — turquoise ✓ (cool, positive, the
// brand's "optimism"), orange warnings (energy, attention) — and stay
// conventional where they don't: failure keeps red, which the brand does not
// use and a ✗ must be instantly legible.
//
// Truecolor terminals get the exact brand hexes, 256-color terminals the
// nearest xterm cube entries, everything else the basic ANSI neighbours
// (cyan / yellow). --no-color and NO_COLOR strip all of it in paint().
var ansiBrand, ansiOK, ansiWarn = brandPalette(os.Getenv)

func brandPalette(getenv func(string) string) (brand, ok, warn string) {
	colorterm := strings.ToLower(getenv("COLORTERM"))
	switch {
	case strings.Contains(colorterm, "truecolor") || strings.Contains(colorterm, "24bit"):
		return "\033[38;2;223;83;37m", "\033[38;2;0;255;221m", "\033[38;2;223;83;37m"
	case strings.Contains(getenv("TERM"), "256color"):
		return "\033[38;5;166m", "\033[38;5;50m", "\033[38;5;166m"
	default:
		return ansiYellow, "\033[36m", ansiYellow
	}
}

// humanOutput reports whether stdout is an interactive terminal, i.e. a person
// is watching rather than a pipe or file consuming structured output.
// Variable so tests can force the colour path, which no test TTY provides.
var humanOutput = func() bool {
	return isatty.IsTerminal(os.Stdout.Fd())
}

// wantsHumanView reports whether a command should render its friendly view
// instead of the structured payload: a person at a terminal who did not ask
// for a machine format. Scripts (non-TTY) and explicit --json/-o always get
// the structured output, so nothing automated changes.
func wantsHumanView(cmd *cobra.Command) bool {
	return humanOutput() && !machineFormatRequested(cmd)
}

// machineFormatRequested reports whether the user asked for a machine format
// via --json or -o/--output-format. Both flags are viper-bound globals, so the
// request can arrive as a flag, an env var (ORQ_JSON / ORQ_OUTPUT_FORMAT) or a
// config-file entry - Flag.Changed alone misses everything but the flag, which
// silently gave the human view to exactly the users who configured a machine
// format.
func machineFormatRequested(cmd *cobra.Command) bool {
	if viper.GetBool("json") {
		return true
	}
	if f := cmd.Flags().Lookup("output-format"); f != nil {
		if f.Changed {
			return true
		}
		// The resolved viper value differs from the flag default only when an
		// env var or config file set it; the implicit default is not a request.
		if v := strings.TrimSpace(viper.GetString("output-format")); v != "" && !strings.EqualFold(v, f.DefValue) {
			return true
		}
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

// pad right-pads to width in RUNES, not bytes: a byte count over-pads any
// non-ASCII label or workspace name and breaks the table alignment.
func pad(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		s += strings.Repeat(" ", width-n)
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
	fmt.Fprintln(bartolocli.Stderr, paint(ansiOK, "✓ ")+fmt.Sprintf(format, args...))
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
	fmt.Fprintln(bartolocli.Stderr, paint(ansiWarn, "warning: ")+fmt.Sprintf(format, args...))
}

// statusGlyph maps a doctor check status to a colored marker.
func statusGlyph(status string) string {
	switch status {
	case "pass":
		return paint(ansiOK, "✓")
	case "warn":
		return paint(ansiWarn, "!")
	case "fail":
		return paint(ansiRed, "✗")
	default:
		return "-"
	}
}

// ============================================================================
// Tables
// ============================================================================

// tableRow is one line of a table: an optional marker glyph and one cell per
// header. Cells are plain text — only the last cell may carry its own color,
// because every earlier cell is padded and padding a string with ANSI escapes
// in it counts the escape bytes as width and breaks the alignment. That rule
// is unenforced: a painted earlier cell mis-pads only when color is on, which
// no test sees.
type tableRow struct {
	marker string // a single visible rune, already painted, or "" for none
	// One cell per header. A short row renders the missing columns blank
	// rather than panicking; extra cells past the last header are dropped.
	cells []string
}

// printTable renders the CLI's one table shape: a marker column, a dim header
// row, and every column but the last sized to its widest value — the last is
// emitted verbatim, since nothing follows it to line up with. `orq doctor`,
// `orq workspace list` and `orq connect --status` all print through it, so
// their gutters, header style and column alignment cannot drift apart. (The
// setup final screen still formats its own per-agent rows; it is not a table.)
func printTable(w io.Writer, headers []string, rows []tableRow) {
	if len(headers) == 0 {
		return
	}
	// The last column is never padded, so it is also the only one allowed to
	// arrive pre-colored.
	widths := make([]int, len(headers)-1)
	for i := range widths {
		widths[i] = utf8.RuneCountInString(headers[i])
		for _, r := range rows {
			if n := utf8.RuneCountInString(cellAt(r.cells, i)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	// The marker occupies one rune between two-space gutters; the header's
	// blank marker cell keeps its columns over the rows' columns.
	fmt.Fprint(w, "     ")
	printCells(w, widths, headers, true)
	for _, r := range rows {
		marker := r.marker
		if marker == "" {
			marker = " "
		}
		fmt.Fprintf(w, "  %s  ", marker)
		printCells(w, widths, r.cells, false)
	}
}

// printCells writes exactly len(widths)+1 columns, so a row that is short of
// its headers leaves a blank column instead of an index panic mid-render.
func printCells(w io.Writer, widths []int, cells []string, dim bool) {
	for i := 0; i <= len(widths); i++ {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		cell := cellAt(cells, i)
		if i < len(widths) {
			cell = pad(cell, widths[i])
		}
		if dim {
			cell = paint(ansiDim, cell)
		}
		fmt.Fprint(w, cell)
	}
	fmt.Fprintln(w)
}

func cellAt(cells []string, i int) string {
	if i < len(cells) {
		return cells[i]
	}
	return ""
}
