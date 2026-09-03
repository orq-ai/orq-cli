package commands

import (
	"fmt"
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
	params := viper.New()
	cmd := &cobra.Command{
		Use:   "thread trace-id [span-id]",
		Short: "Render a trace conversation as a thread",
		Long:  "Render a trace's conversational span as Markdown, or a canonical machine-readable thread.",
		Example: strings.Join([]string{
			"  orq traces thread tr_123 --slice 2",
			"  orq traces thread tr_123 --slice 2:",
			"  orq traces thread tr_123 --slice :-1",
		}, "\n"),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			bartolocli.MarkPassedFlags(cmd, params)
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
			if machineFormatRequested(cmd) {
				return emit(thread)
			}
			return RenderThreadMarkdown(bartolocli.Stdout, thread)
		},
	}
	cmd.Flags().StringVar(&slice, "slice", "", "Select messages with a Python-style slice (for example 2:, :-1, or -1)")
	return cmd
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
	if api.GetTrace == nil || api.ListSpans == nil {
		return Thread{}, fmt.Errorf("trace API is unavailable")
	}

	traceResponse, err := api.GetTrace(traceID, params)
	if err != nil {
		return Thread{}, fmt.Errorf("get trace %q: %w", traceID, err)
	}
	trace := traceEnvelope(traceResponse)
	fallbackIDs := uniqueThreadIDs(
		threadString(trace["leading_span_id"]),
		threadString(trace["root_span_id"]),
	)

	candidates, listErr := listThreadCandidates(api, traceID, params)
	tried := make(map[string]bool, len(candidates))
	if listErr == nil {
		for _, candidate := range candidates {
			tried[candidate.id] = true
			thread, err := hydrateThread(api, traceID, candidate.id, params)
			if err == nil {
				return thread, nil
			}
			if !isUnsupportedConversation(err) {
				return Thread{}, err
			}
		}
	}
	for _, fallbackID := range fallbackIDs {
		if tried[fallbackID] {
			continue
		}
		tried[fallbackID] = true
		thread, err := hydrateThread(api, traceID, fallbackID, params)
		if err == nil {
			return thread, nil
		}
		if !isUnsupportedConversation(err) {
			return Thread{}, err
		}
	}
	if listErr != nil {
		return Thread{}, listErr
	}
	return Thread{}, fmt.Errorf("no supported conversation found in trace %q", traceID)
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
	thread, err := NormalizeThread(spanEnvelope(spanResponse), ThreadSource{TraceID: traceID, SpanID: spanID})
	if err != nil {
		return Thread{}, err
	}
	return thread, nil
}

func isUnsupportedConversation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "does not contain a supported")
}

type threadCandidate struct {
	id        string
	startedAt time.Time
	order     int
}

func listThreadCandidates(api TraceAPI, traceID string, params *viper.Viper) ([]threadCandidate, error) {
	var spans []map[string]any
	seenTokens := map[string]bool{}
	for {
		response, err := api.ListSpans(traceID, params)
		if err != nil {
			return nil, fmt.Errorf("list spans for trace %q: %w", traceID, err)
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
	for index, span := range spans {
		id := threadString(span["span_id"])
		if id == "" || excluded[id] || span["has_detail"] == false {
			continue
		}
		startedAt, _ := time.Parse(time.RFC3339Nano, threadString(span["started_at"]))
		candidates = append(candidates, threadCandidate{id: id, startedAt: startedAt, order: index})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].startedAt.Equal(candidates[j].startedAt) {
			return candidates[i].order < candidates[j].order
		}
		return candidates[i].startedAt.After(candidates[j].startedAt)
	})
	return candidates, nil
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

func traceEnvelope(response map[string]any) map[string]any {
	if trace, ok := threadMap(response["trace"]); ok {
		return trace
	}
	return response
}

func spanEnvelope(response map[string]any) map[string]any {
	if span, ok := threadMap(response["span"]); ok {
		return span
	}
	return response
}
