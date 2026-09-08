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
// interactive terminal — a piped or `-o json` invocation sees none of this. Color
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

// StdoutIsTerminal exposes the shared terminal decision to the root package,
// which rebuilds bartolo's formatter after applying NO_COLOR.
func StdoutIsTerminal() bool { return humanOutput() }

// stderrIsTerminal is the gate for a line that goes to stderr only: whether a
// person is watching THAT stream. `orq x | jq` keeps stdout for the pipe and
// stderr for the human, and stdout's TTY says nothing about the second.
var stderrIsTerminal = func() bool {
	return isatty.IsTerminal(os.Stderr.Fd())
}

// StderrIsTerminal exposes the stderr decision to the root package.
func StderrIsTerminal() bool { return stderrIsTerminal() }

// wantsHumanView reports whether a command should render its friendly view
// instead of the structured payload: a person at a terminal who did not ask
// for a machine format. Scripts (non-TTY) and an explicit -o always get the
// structured output, so nothing automated changes.
func wantsHumanView(cmd *cobra.Command) bool {
	return humanOutput() && !machineFormatRequested(cmd)
}

const (
	// outputFormatEnvVar is the environment spelling of -o, from bartolo's ORQ
	// prefix and its `-` to `_` replacer.
	outputFormatEnvVar = "ORQ_OUTPUT_FORMAT"
	// outputFormatTable is bartolo's CLI-wide default. It is the one value in
	// OutputFormats that names a layout rather than a serialization.
	outputFormatTable = "table"
)

// machineFormatRequested reports whether the user asked for a machine format
// via -o/--output-format, from any source that can name one: the flag, the
// environment (ORQ_OUTPUT_FORMAT) or a config-file entry. Flag.Changed alone
// misses everything but the flag, which silently gave the human view to
// exactly the users who configured a machine format.
//
// It asks which source named a format, and what it named, rather than comparing
// the resolved value against the flag's default: that default is per-command —
// `orq traces thread` registers its own -o — so a comparison reads every one of
// that command's runs as a request.
//
// The flag names a format for one invocation. The environment and the config
// file state a standing default instead, so the value the whole CLI already
// defaults to - `table`, what `orq default-format table` writes and what an
// exported ORQ_OUTPUT_FORMAT usually repeats - is not a request there.
//
// A command that reads only its own -o answers separately, in
// ownFormatRequested.
func machineFormatRequested(cmd *cobra.Command) bool {
	if cmd.Annotations[threadFormatAnnotation] != "" {
		return ownFormatRequested(cmd)
	}
	f := cmd.Flags().Lookup("output-format")
	if f != nil && f.Changed {
		return namesMachineFormat(f.Value.String())
	}
	if value := strings.TrimSpace(os.Getenv(outputFormatEnvVar)); value != "" {
		return standingDefaultIsMachineFormat(value)
	}
	return standingDefaultIsMachineFormat(configuredOutputFormat())
}

// ownFormatRequested answers for a command that resolves its format from its
// own -o and reads no other source. That flag is all it reads, so a standing
// default cannot be a request here: counting one would drop the notices written
// for a person on the very run that renders them the readable thread.
func ownFormatRequested(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("output-format")
	return f != nil && f.Changed && namesMachineFormat(f.Value.String())
}

// namesMachineFormat reports whether a named format is a serialization for a
// program to read. `xml` and `markdown`, the two renders `orq traces thread`
// adds, are reading views for a person: naming one is not a reason to drop the
// notices and friendly views that exist for the person doing the reading.
func namesMachineFormat(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", threadFormatXML, threadFormatMarkdown:
		return false
	}
	return true
}

// standingDefaultIsMachineFormat answers for the two sources that state a
// default rather than ask per invocation. `table` standing in one of those says
// nothing about this run; passed to -o it is a request for the table layout,
// which is why the flag does not go through here.
func standingDefaultIsMachineFormat(value string) bool {
	return !strings.EqualFold(strings.TrimSpace(value), outputFormatTable) && namesMachineFormat(value)
}

// configuredOutputFormat is viper's merged value, lowercased, gated on the
// config file having named the key at all: InConfig is what distinguishes a
// written entry from the default underneath it, and GetString then returns
// whichever tier won rather than the file's own text.
func configuredOutputFormat() string {
	if !viper.InConfig("output-format") {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(viper.GetString("output-format")))
}

// MachineFormatRequested exposes the shared human/machine output decision to
// the root package for notices emitted by request middleware.
func MachineFormatRequested(cmd *cobra.Command) bool { return machineFormatRequested(cmd) }

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
	Notice(format, args...)
}

// Notice is info's dimmed stderr line without info's stdout gate: the caller
// has already decided a person is reading this stderr. Exported so the custom
// package's PreRun shares one secondary-context style with the commands, as
// Warn does for warnings.
func Notice(format string, args ...any) {
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
// header. Only the last cell may arrive pre-colored: every earlier cell is
// padded, and pad counts the characters of an ANSI escape as visible width.
type tableRow struct {
	marker string // a single visible rune, already painted, or "" for none
	// One cell per header. A short row renders the missing columns blank
	// rather than panicking; extra cells past the last header are dropped.
	cells []string
}

// printTable renders the CLI's one table shape: a marker column, a dim header
// row, and every column but the last sized to its widest value — the last is
// emitted verbatim, since nothing follows it to line up with.
func printTable(w io.Writer, headers []string, rows []tableRow) {
	if len(headers) == 0 {
		return
	}
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
