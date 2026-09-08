package commands

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// TraceAPI isolates the schema-generated trace operations from the shared
// command package. Stable and rc binaries inject wrappers for their own
// generated client, which lets this command stay portable between schemas.
type TraceAPI struct {
	GetTrace  func(traceID string, params *viper.Viper) (map[string]any, error)
	GetSpan   func(traceID, spanID string, params *viper.Viper) (map[string]any, error)
	ListSpans func(traceID string, params *viper.Viper) (map[string]any, error)
}

// NewTracesThreadCommand builds `orq traces thread`, rendering the newest
// conversational span selected from a trace as a portable Thread.
func NewTracesThreadCommand(api TraceAPI) *cobra.Command {
	var slice string
	maxChars := 4000
	reasoning := true
	params := viper.New()
	cmd := &cobra.Command{
		Use:   "thread trace-id [span-id]",
		Short: "Render a trace conversation as a thread",
		Long: strings.Join([]string{
			"Render a trace's conversational span as XML-demarcated text, Markdown, or a canonical machine-readable thread.",
			"",
			"The default xml render neutralises the framing tag names in recorded content, so a span cannot forge a turn. The markdown render trades that for readability.",
		}, "\n"),
		Example: strings.Join([]string{
			"  orq traces thread tr_123 --slice 2",
			"  orq traces thread tr_123 --slice 2:",
			"  orq traces thread tr_123 --slice :-1",
			"  orq traces thread tr_123 -o markdown",
			"  orq traces thread tr_123 -o json",
			"  orq traces thread tr_123 --reasoning=false",
			"  orq traces thread tr_123 --max-chars 0",
		}, "\n"),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			bartolocli.MarkPassedFlags(cmd, params)
			resolved, err := resolveThreadFormat(cmd)
			if err != nil {
				return err
			}
			thread, err := resolveTraceThread(api, args[0], optionalArg(args, 1), params)
			if err != nil {
				return err
			}
			if slice != "" {
				thread, err = SliceThread(thread, slice)
				if err != nil {
					// A malformed --slice is a typed-it-wrong error, the same
					// class as an output format the command does not know.
					return bartolocli.NewValueError(err)
				}
			}
			if !reasoning {
				for index := range thread.Messages {
					thread.Messages[index].Reasoning = nil
				}
			}
			switch resolved {
			case threadFormatXML:
				return RenderThread(bartolocli.Stdout, thread, maxChars)
			case threadFormatMarkdown:
				return RenderThreadMarkdown(bartolocli.Stdout, thread, maxChars)
			}
			// The local flag is not the one viper is bound to, so the shared
			// formatter still holds the global value; point it at what this
			// run asked for, for this run only.
			restore, err := bartolocli.SetOutputFormat(resolved)
			if err != nil {
				return err
			}
			defer restore()
			return emit(thread)
		},
	}
	cmd.Flags().StringVar(&slice, "slice", "", "Select messages with a Python-style slice (for example 2:, :-1, or -1)")
	cmd.Flags().BoolVar(&reasoning, "reasoning", true, "Include recorded reasoning and thinking (--reasoning=false to omit)")
	// A local -o shadowing the global one: same flag, two extra values. Cobra
	// merges a parent's persistent flags only where the name is free, so this
	// one answers here and the global keeps every other command. It stays out
	// of viper deliberately — bartolo validates the bound value against its own
	// list before this command runs, and `xml` is not on it.
	// The annotation is how run.go recognizes this command before it runs; see
	// RelaxOutputFormat.
	cmd.Annotations = map[string]string{threadFormatAnnotation: "true"}
	cmd.Flags().StringP("output-format", "o", "", fmt.Sprintf("Output format [%s] (default %s; table is refused here) [env: %s]", strings.Join(threadFormats, ", "), threadFormatXML, outputFormatEnvVar))
	cmd.Flags().IntVar(&maxChars, "max-chars", 4000, "Cut each rendered block to this many characters, noting how much was left out (0 for no cap)")
	return cmd
}

const (
	threadFormatXML      = "xml"
	threadFormatMarkdown = "markdown"
	// threadFormatAnnotation marks the command whose -o takes the two renders
	// bartolo's own list does not have.
	threadFormatAnnotation = "orq.thread-output-format"
)

// RelaxOutputFormat hands this command the job of judging an output format
// that came from the environment or a config file. Both feed viper's
// `output-format`, which bartolo's root validates against its own list before
// any command runs — a list without `xml` or `markdown`, so an
// `ORQ_OUTPUT_FORMAT=markdown` shell would otherwise be told markdown is not a
// format, by the one command that renders it. A value bartolo does accept is
// left alone; anything else is answered by this command, which knows both the
// renders it adds and the one it takes away.
//
// The substituted value is the CLI-wide default, which is what the rest of the
// CLI would have used anyway; this command reads the environment and the config
// itself rather than the merged value, so nothing downstream reads the
// substitute as a request. restore() puts viper back, and the process-local
// format bartolo resolved from the substitute stays substituted — it is RunE's
// own SetOutputFormat, called before every emit, that decides what this command
// serializes. It returns a restore func, and false when there is nothing to
// relax.
func RelaxOutputFormat(cmd *cobra.Command) (func(), bool) {
	if cmd == nil || cmd.Annotations[threadFormatAnnotation] == "" {
		return nil, false
	}
	value := strings.ToLower(strings.TrimSpace(viper.GetString("output-format")))
	if value == "" || slices.Contains(bartolocli.OutputFormats, value) {
		return nil, false
	}
	// Restore the tier the value came from, not just its text: viper.Set writes
	// the override tier, so re-Setting a value that arrived from the
	// environment or the config file would promote it above them, where it
	// would outrank the very sources it came from. Dropping the override first
	// is what tells the two apart — viper.Set(key, nil) is how an override is
	// dropped, there is no Unset — since a value that survives the drop was
	// never in the override tier.
	previous := viper.Get("output-format")
	viper.Set("output-format", nil)
	overridden := strings.ToLower(strings.TrimSpace(viper.GetString("output-format"))) != value
	viper.Set("output-format", outputFormatTable)
	return func() {
		if overridden {
			viper.Set("output-format", previous)
			return
		}
		viper.Set("output-format", nil)
	}, true
}

// threadFormats are the values -o takes on this command: the two reading views,
// then the serializations of bartolo's OutputFormats — that list minus `table`,
// which names a layout this command has no render for.
var threadFormats = []string{threadFormatXML, threadFormatMarkdown, "json", "yaml", "toon"}

// resolveThreadFormat reads the format from the highest-precedence source that
// actually named one — the flag, then the environment, then the config file —
// and defaults to the XML render when none did.
//
// It reads those sources rather than the resolved viper value because the two
// answer differently for exactly one word. `table` is the CLI-wide default, so
// the resolved value cannot tell "the user asked for a table" from "the user
// asked for nothing"; a conversation has no columns, and rendering something
// else in silence for the first case is the bug this command had. Asked for, it
// is an error. Left at the default, it is the reading view.
//
// The flag and the environment ask per invocation, so `table` from either is an
// error. The config file states a standing default instead — `orq default-format
// table` is the documented way to write the CLI-wide one — and a standing
// `table` is not a request for a layout this command cannot give, so it falls
// through to the same render an unset config gets.
func resolveThreadFormat(cmd *cobra.Command) (string, error) {
	if flag := cmd.Flags().Lookup("output-format"); flag != nil && flag.Changed {
		return normalizeThreadFormat(flag.Value.String(), "--output-format")
	}
	if value := strings.TrimSpace(os.Getenv(outputFormatEnvVar)); value != "" {
		return normalizeThreadFormat(value, outputFormatEnvVar)
	}
	if value := configuredOutputFormat(); value != "" && value != outputFormatTable {
		return normalizeThreadFormat(value, "output-format in the config file")
	}
	return threadFormatXML, nil
}

func normalizeThreadFormat(value, source string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if slices.Contains(threadFormats, normalized) {
		return normalized, nil
	}
	if normalized == outputFormatTable {
		return "", bartolocli.NewValueError(fmt.Errorf(
			"%s: %q is the CLI-wide default layout, and a conversation has no columns to lay out. This command takes [%s]; %s is what it renders when you ask for nothing",
			source, value, strings.Join(threadFormats, ", "), threadFormatXML))
	}
	return "", bartolocli.NewValueError(fmt.Errorf("%s: %q is not one of [%s]", source, value, strings.Join(threadFormats, ", ")))
}

func optionalArg(args []string, index int) string {
	if len(args) > index {
		return args[index]
	}
	return ""
}

func resolveTraceThread(api TraceAPI, traceID, spanID string, params *viper.Viper) (Thread, error) {
	if api.GetSpan == nil {
		return Thread{}, fmt.Errorf("trace API is unavailable")
	}
	if spanID != "" {
		return hydrateThread(api, traceID, spanID, params)
	}
	if api.GetTrace == nil {
		return Thread{}, fmt.Errorf("trace API is unavailable")
	}

	traceResponse, err := api.GetTrace(traceID, params)
	if err != nil {
		return Thread{}, fmt.Errorf("get trace %q: %w", traceID, err)
	}
	trace := unwrapThreadEnvelope(traceResponse, "trace")
	fallbackIDs := uniqueThreadIDs(
		threadString(trace["leading_span_id"]),
		threadString(trace["root_span_id"]),
	)

	candidates, excluded, listErr := listThreadCandidates(api, traceID, params)
	if listErr != nil {
		// Every path below can still return a span, from the partial listing or
		// from the trace's own fallback IDs, so the paging failure would
		// otherwise leave exit 0 saying an older conversation is the whole
		// answer. Say which pool the selection came from.
		Warn("%v; selecting from the %d span(s) listed before the failure", listErr, len(candidates))
	}
	tried := make(map[string]bool, len(candidates))
	var operationalErr error
	var best *Thread
	degraded := false
	// The newest span usually holds the whole history, so it returns as soon as
	// it hydrates; once one is missing content, a sibling that kept it is worth
	// finding, and it is not always the next span tried.
	consider := func(spanID string) *Thread {
		thread, err := hydrateThread(api, traceID, spanID, params)
		if err != nil {
			if !errors.Is(err, ErrUnsupportedConversation) && operationalErr == nil {
				operationalErr = err
			}
			return nil
		}
		if best == nil || betterThread(thread, *best) {
			best = &thread
		}
		if !threadIsWhole(thread) {
			degraded = true
		} else if !degraded {
			return best
		}
		return nil
	}
	for _, candidate := range candidates {
		if tried[candidate.id] {
			continue
		}
		tried[candidate.id] = true
		if thread := consider(candidate.id); thread != nil {
			return *thread, nil
		}
	}
	for _, fallbackID := range fallbackIDs {
		if tried[fallbackID] || excluded[fallbackID] {
			continue
		}
		tried[fallbackID] = true
		if thread := consider(fallbackID); thread != nil {
			return *thread, nil
		}
	}
	if best != nil {
		return *best, nil
	}
	if operationalErr != nil {
		return Thread{}, operationalErr
	}
	if listErr != nil {
		return Thread{}, listErr
	}
	return Thread{}, fmt.Errorf("no supported conversation found in trace %q", traceID)
}

// betterThread prefers the thread with more readable messages, and on a tie the
// one with no dropped content, so an explicit gap never wins over a span that
// kept the same turns intact.
func betterThread(candidate, best Thread) bool {
	candidateCount, bestCount := threadContentMessages(candidate), threadContentMessages(best)
	if candidateCount != bestCount {
		return candidateCount > bestCount
	}
	return threadIsWhole(candidate) && !threadIsWhole(best)
}

// threadIsWhole reports a thread with no content the collector dropped.
func threadIsWhole(thread Thread) bool {
	for _, message := range thread.Messages {
		for _, part := range message.Content {
			if part.Type == "unavailable" {
				return false
			}
		}
	}
	return true
}

// threadContentMessages counts the messages that carry something to read. A
// message whose content the collector dropped does not count, so a span that
// kept a turn outranks one that only reports the turn missing.
func threadContentMessages(thread Thread) int {
	total := 0
	for _, message := range thread.Messages {
		if len(message.ToolCalls) > 0 || len(message.Reasoning) > 0 {
			total++
			continue
		}
		for _, part := range message.Content {
			if part.Type != "unavailable" {
				total++
				break
			}
		}
	}
	return total
}

func uniqueThreadIDs(ids ...string) []string {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	return unique
}

func hydrateThread(api TraceAPI, traceID, spanID string, params *viper.Viper) (Thread, error) {
	spanResponse, err := api.GetSpan(traceID, spanID, params)
	if err != nil {
		return Thread{}, fmt.Errorf("get span %q for trace %q: %w", spanID, traceID, err)
	}
	thread, err := NormalizeThread(unwrapThreadEnvelope(spanResponse, "span"), ThreadSource{TraceID: traceID, SpanID: spanID})
	if err != nil {
		return Thread{}, fmt.Errorf("span %q: %w", spanID, err)
	}
	return thread, nil
}

type threadCandidate struct {
	id        string
	startedAt time.Time
	order     int
}

func listThreadCandidates(api TraceAPI, traceID string, params *viper.Viper) ([]threadCandidate, map[string]bool, error) {
	if api.ListSpans == nil {
		// Listing improves selection but is not required: the trace response
		// still provides leading/root fallback IDs.
		return nil, map[string]bool{}, nil
	}
	initialPageToken := params.GetString("page-token")
	defer params.Set("page-token", initialPageToken)
	var spans []map[string]any
	seenTokens := map[string]bool{}
	var listErr error
	for {
		response, err := api.ListSpans(traceID, params)
		if err != nil {
			listErr = fmt.Errorf("list spans for trace %q: %w", traceID, err)
			break
		}
		spans = append(spans, listSpanData(response)...)
		next := threadString(response["next_page_token"])
		if next == "" || seenTokens[next] {
			break
		}
		seenTokens[next] = true
		params.Set("page-token", next)
	}

	excluded := evaluatorExclusions(spans)
	candidates := make([]threadCandidate, 0, len(spans))
	seenIDs := make(map[string]bool, len(spans))
	for index, span := range spans {
		id := threadString(span["span_id"])
		if id == "" || seenIDs[id] || excluded[id] || span["has_detail"] == false {
			continue
		}
		seenIDs[id] = true
		startedAt, _ := time.Parse(time.RFC3339Nano, threadString(span["started_at"]))
		candidates = append(candidates, threadCandidate{id: id, startedAt: startedAt, order: index})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].startedAt.Equal(candidates[j].startedAt) {
			return candidates[i].order < candidates[j].order
		}
		return candidates[i].startedAt.After(candidates[j].startedAt)
	})
	return candidates, excluded, listErr
}

func listSpanData(response map[string]any) []map[string]any {
	data, _ := response["data"].([]any)
	spans := make([]map[string]any, 0, len(data))
	for _, value := range data {
		if span, ok := threadMap(value); ok {
			spans = append(spans, span)
		}
	}
	return spans
}

func evaluatorExclusions(spans []map[string]any) map[string]bool {
	children := map[string][]string{}
	excluded := map[string]bool{}
	for _, span := range spans {
		id := threadString(span["span_id"])
		if id == "" {
			continue
		}
		if parent := threadString(span["parent_span_id"]); parent != "" {
			children[parent] = append(children[parent], id)
		}
		if evaluatorSpan(span) {
			excluded[id] = true
		}
	}
	queue := make([]string, 0, len(excluded))
	for id := range excluded {
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, child := range children[id] {
			if excluded[child] {
				continue
			}
			excluded[child] = true
			queue = append(queue, child)
		}
	}
	return excluded
}

func evaluatorSpan(span map[string]any) bool {
	for _, key := range []string{"type", "name", "operation"} {
		value := strings.ToLower(threadString(span[key]))
		if strings.Contains(value, "evaluator") || hasEvalToken(value) {
			return true
		}
	}
	return false
}

func hasEvalToken(value string) bool {
	return containsString(strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), "eval")
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func unwrapThreadEnvelope(response map[string]any, key string) map[string]any {
	if value, ok := threadMap(response[key]); ok {
		return value
	}
	return response
}
