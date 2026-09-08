package commands

import (
	"errors"
	"fmt"
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
	var slice, format string
	maxChars := 4000
	reasoning := true
	params := viper.New()
	cmd := &cobra.Command{
		Use:   "thread trace-id [span-id]",
		Short: "Render a trace conversation as a thread",
		Long:  "Render a trace's conversational span as XML-demarcated text, Markdown, or a canonical machine-readable thread.",
		Example: strings.Join([]string{
			"  orq traces thread tr_123 --slice 2",
			"  orq traces thread tr_123 --slice 2:",
			"  orq traces thread tr_123 --slice :-1",
			"  orq traces thread tr_123 --format markdown",
			"  orq traces thread tr_123 --reasoning=false",
			"  orq traces thread tr_123 --max-chars 0",
		}, "\n"),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			bartolocli.MarkPassedFlags(cmd, params)
			resolved, err := resolveThreadFormat(format)
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
					return err
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
			// --format names the serialization for this render only; without
			// this the formatter would still encode with the global -o value.
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
	cmd.Flags().StringVar(&format, "format", "", fmt.Sprintf("Thread output format [%s] (default %s; -o/--json select a serialization when unset)", strings.Join(threadFormats, ", "), threadFormatXML))
	cmd.Flags().IntVar(&maxChars, "max-chars", 4000, "Cut each rendered block to this many characters, noting how much was left out (0 for no cap)")
	return cmd
}

const (
	threadFormatXML      = "xml"
	threadFormatMarkdown = "markdown"
)

// threadFormats are the explicit --format values. XML is also the command's
// implicit default.
var threadFormats = []string{threadFormatXML, threadFormatMarkdown, "json", "yaml", "toon"}

// resolveThreadFormat picks one render from one resolved value. A conversation
// is nested, so there is no table to lay out: `table` — the CLI-wide default,
// whether it arrived from -o, ORQ_OUTPUT_FORMAT, a config file, or nothing at
// all — resolves to the XML render, so all four routes agree. --format is the
// per-command override, and the only way to ask for Markdown, which the global
// flag does not accept.
func resolveThreadFormat(format string) (string, error) {
	if strings.TrimSpace(format) == "" {
		if resolved := bartolocli.OutputFormat(); resolved != "table" {
			return resolved, nil
		}
		return threadFormatXML, nil
	}
	normalized := strings.ToLower(strings.TrimSpace(format))
	if normalized == "md" {
		normalized = threadFormatMarkdown
	}
	if slices.Contains(threadFormats, normalized) {
		return normalized, nil
	}
	return "", bartolocli.NewValueError(fmt.Errorf("--format: %q is not one of [%s]", format, strings.Join(threadFormats, ", ")))
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
