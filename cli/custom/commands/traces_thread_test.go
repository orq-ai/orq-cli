package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	toon "github.com/toon-format/toon-go"
	yaml "go.yaml.in/yaml/v3"
)

type fakeTraceAPI struct {
	trace     map[string]any
	traceErr  error
	spans     map[string]map[string]any
	spanErr   map[string]error
	pages     map[string]map[string]any
	listErr   error
	getCalls  []string
	listCalls []string
	responses map[string]map[string]any
	respErr   map[string]error
	respCalls []string
}

func (api *fakeTraceAPI) getTrace(traceID string, _ *viper.Viper) (map[string]any, error) {
	api.getCalls = append(api.getCalls, "trace:"+traceID)
	return api.trace, api.traceErr
}

func (api *fakeTraceAPI) getSpan(traceID, spanID string, _ *viper.Viper) (map[string]any, error) {
	api.getCalls = append(api.getCalls, "span:"+traceID+":"+spanID)
	if err := api.spanErr[spanID]; err != nil {
		return nil, err
	}
	return api.spans[spanID], nil
}

func (api *fakeTraceAPI) listSpans(traceID string, params *viper.Viper) (map[string]any, error) {
	api.listCalls = append(api.listCalls, traceID+":"+params.GetString("page-token"))
	return api.pages[params.GetString("page-token")], api.listErr
}

func (api *fakeTraceAPI) getResponse(responseID string, _ *viper.Viper) (map[string]any, error) {
	api.respCalls = append(api.respCalls, responseID)
	if err := api.respErr[responseID]; err != nil {
		return nil, err
	}
	return api.responses[responseID], nil
}

func traceAPI(fake *fakeTraceAPI) TraceAPI {
	return TraceAPI{GetTrace: fake.getTrace, GetSpan: fake.getSpan, ListSpans: fake.listSpans, GetResponse: fake.getResponse}
}

// storedResponseSpan is a Responses span as the API returns one: the item
// counts of a conversation, and the id of the payload holding the items.
func storedResponseSpan(responseID string, inputItems int) map[string]any {
	return map[string]any{"span": map[string]any{"attributes": map[string]any{
		"gen_ai": map[string]any{"response": map[string]any{"id": responseID}},
		"openresponses": map[string]any{
			"input":  map[string]any{"items": map[string]any{"count": inputItems}},
			"output": map[string]any{"items": map[string]any{"count": 1}},
		},
	}}}
}

func storedResponsePayload(question, answer string) map[string]any {
	return map[string]any{
		"input":  []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": question}}}},
		"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": answer}}}},
	}
}

func conversationalSpan(text string) map[string]any {
	return map[string]any{"span": map[string]any{"attributes": map[string]any{
		"gen_ai.input":  []any{map[string]any{"role": "user", "content": text}},
		"gen_ai.output": map[string]any{"role": "assistant", "content": "answer"},
	}}}
}

func runTracesThread(t *testing.T, api TraceAPI, args ...string) (string, error) {
	t.Helper()
	oldOut, oldFormatter, oldRoot := bartolocli.Stdout, bartolocli.Formatter, bartolocli.Root
	oldHuman := humanOutput
	t.Cleanup(func() {
		bartolocli.Stdout, bartolocli.Formatter, bartolocli.Root = oldOut, oldFormatter, oldRoot
		humanOutput = oldHuman
	})
	humanOutput = func() bool { return false }
	var out bytes.Buffer
	bartolocli.Stdout = &out
	bartolocli.Formatter = bartolocli.NewDefaultFormatter(false, false)
	root := &cobra.Command{
		Use:           "orq",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// The global -o as the binary registers it, unbound: binding it would leave
	// package-global viper pointing at a flag on a root this test is about to
	// throw away, which is how `go test -run X` and a full-package run stop
	// being the same experiment. Nothing under test reads the bound value —
	// the command reads the flag, the environment and the config file itself.
	root.PersistentFlags().StringP("output-format", "o", "table", "")
	bartolocli.Root = root
	root.AddCommand(NewTracesThreadCommand(api))
	root.SetArgs(append([]string{"thread"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestTracesThreadUsesExplicitSpanOnly(t *testing.T) {
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("explicit")}}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"span:trace-1:chosen"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !strings.Contains(out, "explicit") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadUsesLeadingSpan(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{
			"leading_span_id": "lead",
			"root_span_id":    "root",
		}},
		spans: map[string]map[string]any{
			"lead": conversationalSpan("leading"),
			"root": conversationalSpan("root must not win"),
		},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:lead"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if got, want := fake.listCalls, []string{"trace-1:"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("list calls = %v, want %v", got, want)
	}
	if strings.Contains(out, "root must not win") || !strings.Contains(out, "leading") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadUsesLeadingSpanWhenListingFails(t *testing.T) {
	fake := &fakeTraceAPI{
		trace:   map[string]any{"trace": map[string]any{"leading_span_id": "lead"}},
		spans:   map[string]map[string]any{"lead": conversationalSpan("leading fallback")},
		listErr: errors.New("listing unavailable"),
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:lead"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !strings.Contains(out, "leading fallback") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadUsesRootSpanWhenLeadingSpanIsAbsent(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"root_span_id": "root"}},
		spans: map[string]map[string]any{"root": conversationalSpan("root fallback")},
		pages: map[string]map[string]any{"": {"data": []any{}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:root"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !strings.Contains(out, "root fallback") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadUsesRootSpanAfterUnsupportedLeadingSpan(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{
			"leading_span_id": "lead",
			"root_span_id":    "root",
		}},
		spans: map[string]map[string]any{
			"lead": {"span": map[string]any{"attributes": map[string]any{"unrelated": true}}},
			"root": conversationalSpan("root fallback"),
		},
		pages: map[string]map[string]any{"": {"data": []any{}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:lead", "span:trace-1:root"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !strings.Contains(out, "root fallback") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadUsesRootSpanAfterLeadingSpanAPIError(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{
			"leading_span_id": "stale-lead",
			"root_span_id":    "root",
		}},
		spans: map[string]map[string]any{
			"root": conversationalSpan("root fallback"),
		},
		spanErr: map[string]error{"stale-lead": errors.New("not found")},
		pages:   map[string]map[string]any{"": {"data": []any{}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:stale-lead", "span:trace-1:root"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !strings.Contains(out, "root fallback") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadReturnsFirstFallbackAPIErrorAfterTryingAllFallbacks(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{
			"leading_span_id": "lead",
			"root_span_id":    "root",
		}},
		spanErr: map[string]error{
			"lead": errors.New("leading unavailable"),
			"root": errors.New("root unavailable"),
		},
		listErr: errors.New("listing unavailable"),
	}
	_, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err == nil || !strings.Contains(err.Error(), `get span "lead"`) || !strings.Contains(err.Error(), "leading unavailable") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "root unavailable") {
		t.Fatalf("returned later fallback error = %v", err)
	}
	if strings.Contains(err.Error(), "listing unavailable") {
		t.Fatalf("returned list error instead of fallback error = %v", err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:lead", "span:trace-1:root"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestTracesThreadDoesNotHydrateListedLeadingSpanTwice(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "lead"}},
		spans: map[string]map[string]any{
			"lead": {"span": map[string]any{"attributes": map[string]any{"unrelated": true}}},
		},
		pages: map[string]map[string]any{
			"": {"data": []any{map[string]any{"span_id": "lead", "has_detail": true}}},
		},
	}
	_, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err == nil || !strings.Contains(err.Error(), "no supported conversation") {
		t.Fatalf("error = %v", err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:lead"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestTracesThreadDoesNotHydrateListedRootSpanTwice(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"root_span_id": "root"}},
		spans: map[string]map[string]any{
			"root": {"span": map[string]any{"attributes": map[string]any{"unrelated": true}}},
		},
		pages: map[string]map[string]any{
			"": {"data": []any{map[string]any{"span_id": "root", "has_detail": true}}},
		},
	}
	_, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err == nil || !strings.Contains(err.Error(), "no supported conversation") {
		t.Fatalf("error = %v", err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:root"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestTracesThreadReturnsFallbackSpanAPIErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		pages map[string]map[string]any
		spans map[string]map[string]any
	}{
		{
			name:  "empty listing",
			pages: map[string]map[string]any{"": {"data": []any{}}},
		},
		{
			name: "unsupported listed span",
			pages: map[string]map[string]any{"": {"data": []any{
				map[string]any{"span_id": "candidate", "has_detail": true},
			}}},
			spans: map[string]map[string]any{
				"candidate": {"span": map[string]any{"attributes": map[string]any{"unrelated": true}}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeTraceAPI{
				trace:   map[string]any{"trace": map[string]any{"leading_span_id": "lead"}},
				spans:   test.spans,
				spanErr: map[string]error{"lead": errors.New("network unavailable")},
				pages:   test.pages,
			}
			_, err := runTracesThread(t, traceAPI(fake), "trace-1")
			if err == nil || !strings.Contains(err.Error(), `get span "lead"`) || !strings.Contains(err.Error(), "network unavailable") {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "no supported conversation") {
				t.Fatalf("misleading error = %v", err)
			}
		})
	}
}

func TestTracesThreadPrefersNewestConversationalSpanOverLeadingSpan(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "lead"}},
		spans: map[string]map[string]any{
			"lead":  conversationalSpan("incomplete leading context"),
			"child": conversationalSpan("complete child context"),
		},
		pages: map[string]map[string]any{
			"": {"data": []any{
				map[string]any{"span_id": "lead", "has_detail": true, "started_at": "2025-01-01T00:00:00Z"},
				map[string]any{"span_id": "child", "parent_span_id": "lead", "has_detail": true, "started_at": "2025-01-02T00:00:00Z"},
			}},
		},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:child"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if got, want := fake.listCalls, []string{"trace-1:"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("list calls = %v, want %v", got, want)
	}
	if strings.Contains(out, "incomplete leading context") || !strings.Contains(out, "complete child context") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadUsesListedCandidateBeforeMissingLeadingSpan(t *testing.T) {
	fake := &fakeTraceAPI{
		trace:   map[string]any{"trace": map[string]any{"leading_span_id": "missing"}},
		spanErr: map[string]error{"missing": errors.New("not found")},
		spans:   map[string]map[string]any{"candidate": conversationalSpan("fallback")},
		pages: map[string]map[string]any{
			"": {"data": []any{map[string]any{"span_id": "candidate", "has_detail": true, "started_at": "2025-01-01T00:00:00Z"}}},
		},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:candidate"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if got, want := fake.listCalls, []string{"trace-1:"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("list calls = %v, want %v", got, want)
	}
	if !strings.Contains(out, "fallback") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadFallsBackToNewestNonEvaluatorDetailedSpanAcrossPages(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "missing"}},
		spans: map[string]map[string]any{
			"missing":        {"span": map[string]any{"attributes": map[string]any{"unrelated": true}}},
			"new":            conversationalSpan("newest"),
			"old":            conversationalSpan("older"),
			"evalkid":        conversationalSpan("must skip"),
			"evalgrandchild": conversationalSpan("also skip"),
		},
		pages: map[string]map[string]any{
			"": {"data": []any{
				map[string]any{"span_id": "eval", "type": "EVALUATOR", "has_detail": true, "started_at": "2025-01-03T00:00:00Z"},
				map[string]any{"span_id": "evalkid", "parent_span_id": "eval", "has_detail": true, "started_at": "2025-01-04T00:00:00Z"},
				map[string]any{"span_id": "evalgrandchild", "parent_span_id": "evalkid", "has_detail": true, "started_at": "2025-01-06T00:00:00Z"},
				map[string]any{"span_id": "nodetail", "has_detail": false, "started_at": "2025-01-05T00:00:00Z"},
				map[string]any{"span_id": "old", "has_detail": true, "started_at": "2025-01-01T00:00:00Z"},
			}, "next_page_token": "next"},
			"next": {"data": []any{map[string]any{"span_id": "new", "has_detail": true, "started_at": "2025-01-02T00:00:00Z"}}},
		},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.listCalls, []string{"trace-1:", "trace-1:next"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("list calls = %v, want %v", got, want)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:new"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !strings.Contains(out, "newest") {
		t.Fatalf("Markdown = %q", out)
	}
}

// An evaluator subtree is skipped so a judge's conversation is never returned
// in place of the conversation it judged. When the judge is the whole trace
// there is no such substitution to make, and skipping it renders nothing at
// all — so the exclusion lifts, and says that it did.
func TestTracesThreadReadsATraceThatIsOnlyAnEvaluator(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "evalkid", "root_span_id": "eval"}},
		spans: map[string]map[string]any{
			"eval":    conversationalSpan("must not render evaluator"),
			"evalkid": conversationalSpan("must not render evaluator child"),
		},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "eval", "type": "evaluator", "has_detail": true},
			map[string]any{"span_id": "evalkid", "parent_span_id": "eval", "has_detail": true},
		}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if !strings.Contains(out, "must not render evaluator child") {
		t.Fatalf("the deepest evaluator span is all this trace holds, got:\n%s", out)
	}
}

func TestTracesThreadRecoversAfterListedCandidateAPIError(t *testing.T) {
	fake := &fakeTraceAPI{
		trace:   map[string]any{"trace": map[string]any{}},
		spanErr: map[string]error{"new": errors.New("temporary hydration failure")},
		spans:   map[string]map[string]any{"old": conversationalSpan("older recovery")},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "old", "has_detail": true, "started_at": "2025-01-01T00:00:00Z"},
			map[string]any{"span_id": "new", "has_detail": true, "started_at": "2025-01-02T00:00:00Z"},
		}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:new", "span:trace-1:old"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !strings.Contains(out, "older recovery") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadRecoversFromCandidateAPIErrorWithFallback(t *testing.T) {
	fake := &fakeTraceAPI{
		trace:   map[string]any{"trace": map[string]any{"root_span_id": "root"}},
		spanErr: map[string]error{"candidate": errors.New("temporary hydration failure")},
		spans:   map[string]map[string]any{"root": conversationalSpan("root recovery")},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "candidate", "has_detail": true, "started_at": "2025-01-02T00:00:00Z"},
		}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:candidate", "span:trace-1:root"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !strings.Contains(out, "root recovery") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadHydratesDuplicateListedSpanOnce(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{}},
		spans: map[string]map[string]any{"duplicate": {"span": map[string]any{"attributes": map[string]any{"unrelated": true}}}},
		pages: map[string]map[string]any{
			"":     {"data": []any{map[string]any{"span_id": "duplicate", "has_detail": true}}, "next_page_token": "next"},
			"next": {"data": []any{map[string]any{"span_id": "duplicate", "has_detail": true}}},
		},
	}
	_, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err == nil || !strings.Contains(err.Error(), "no supported conversation") {
		t.Fatalf("error = %v", err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:duplicate"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestTracesThreadReturnsClearErrors(t *testing.T) {
	t.Run("no supported payload", func(t *testing.T) {
		fake := &fakeTraceAPI{
			trace: map[string]any{"trace": map[string]any{"leading_span_id": "unsupported"}},
			spans: map[string]map[string]any{
				"unsupported": {"span": map[string]any{"attributes": map[string]any{"unrelated": true}}},
			},
			pages: map[string]map[string]any{"": {"data": []any{}}},
		}
		_, err := runTracesThread(t, traceAPI(fake), "trace-1")
		if err == nil || !strings.Contains(err.Error(), "no supported conversation") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("list error after unsupported fallback", func(t *testing.T) {
		fake := &fakeTraceAPI{
			trace: map[string]any{"trace": map[string]any{"leading_span_id": "unsupported"}},
			spans: map[string]map[string]any{
				"unsupported": {"span": map[string]any{"attributes": map[string]any{"unrelated": true}}},
			},
			listErr: errors.New("listing unavailable"),
		}
		_, err := runTracesThread(t, traceAPI(fake), "trace-1")
		if err == nil || !strings.Contains(err.Error(), "list spans") || !strings.Contains(err.Error(), "listing unavailable") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("API errors retain context", func(t *testing.T) {
		fake := &fakeTraceAPI{traceErr: errors.New("offline")}
		_, err := runTracesThread(t, traceAPI(fake), "trace-1")
		if err == nil || !strings.Contains(err.Error(), "get trace") || !strings.Contains(err.Error(), "offline") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("explicit span API errors are returned", func(t *testing.T) {
		fake := &fakeTraceAPI{spanErr: map[string]error{"chosen": errors.New("offline")}}
		_, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen")
		if err == nil || !strings.Contains(err.Error(), "get span") || !strings.Contains(err.Error(), "offline") {
			t.Fatalf("error = %v", err)
		}
		if got, want := fake.getCalls, []string{"span:trace-1:chosen"}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("calls = %v, want %v", got, want)
		}
	})
}

// Each format gets its own decoder, not a shared substring: the three are
// different serializations of one document, so asserting that the output
// merely mentions "messages" passes every subtest even if all three emit YAML.
func TestTracesThreadUsesCanonicalMachineFormatsAndSlices(t *testing.T) {
	formats := []struct {
		name   string
		args   []string
		decode func([]byte, any) error
		// wantJSON pins the syntax, which the decoder alone cannot: YAML is a
		// superset of JSON, so yaml.Unmarshal accepts the JSON render and the
		// yaml subtest would pass on output that is really JSON.
		wantJSON bool
	}{
		{name: "json", args: []string{"--output-format", "json"}, decode: json.Unmarshal, wantJSON: true},
		{name: "yaml", args: []string{"--output-format", "yaml"}, decode: yaml.Unmarshal},
		{name: "toon", args: []string{"--output-format", "toon"}, decode: func(data []byte, v any) error { return toon.Unmarshal(data, v) }},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
			out, err := runTracesThread(t, traceAPI(fake), append(format.args, "trace-1", "chosen")...)
			if err != nil {
				t.Fatal(err)
			}
			if got := json.Valid([]byte(out)); got != format.wantJSON {
				t.Fatalf("output is JSON = %v, want %v for %s:\n%s", got, format.wantJSON, format.name, out)
			}
			var document map[string]any
			if err := format.decode([]byte(out), &document); err != nil {
				t.Fatalf("output is not %s (%v):\n%s", format.name, err, out)
			}
			assertCanonicalThread(t, document)
		})
	}
	t.Run("slice", func(t *testing.T) {
		span := conversationalSpan("first")
		span["span"].(map[string]any)["attributes"].(map[string]any)["gen_ai.input"] = []any{
			map[string]any{"role": "user", "content": "first"},
			map[string]any{"role": "user", "content": "second"},
		}
		fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": span}}
		out, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen", "--slice", "1:")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "first") || !strings.Contains(out, "second") {
			t.Fatalf("slice output = %q", out)
		}
	})
}

// assertCanonicalThread checks a decoded machine document against the thread
// conversationalSpan("first") describes: the source that identifies the span,
// and both turns with their text where the schema says it lives.
func assertCanonicalThread(t *testing.T, document map[string]any) {
	t.Helper()
	source, ok := document["source"].(map[string]any)
	if !ok {
		t.Fatalf("no source object: %#v", document)
	}
	if fmt.Sprint(source["trace_id"]) != "trace-1" || fmt.Sprint(source["span_id"]) != "chosen" {
		t.Fatalf("source = %#v", source)
	}
	messages, ok := document["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v", document["messages"])
	}
	wantRoles, wantText := []string{"user", "assistant"}, []string{"first", "answer"}
	for index, message := range messages {
		fields, ok := message.(map[string]any)
		if !ok {
			t.Fatalf("message %d = %#v", index, message)
		}
		if fmt.Sprint(fields["index"]) != fmt.Sprint(index) || fmt.Sprint(fields["role"]) != wantRoles[index] {
			t.Fatalf("message %d = %#v", index, fields)
		}
		content, ok := fields["content"].([]any)
		if !ok || len(content) != 1 {
			t.Fatalf("message %d content = %#v", index, fields["content"])
		}
		part, ok := content[0].(map[string]any)
		if !ok || fmt.Sprint(part["type"]) != "text" || fmt.Sprint(part["text"]) != wantText[index] {
			t.Fatalf("message %d part = %#v", index, content[0])
		}
	}
}

func partialSpan() map[string]any {
	return map[string]any{"span": map[string]any{"attributes": map[string]any{
		"openresponses.instructions": "be brief",
		"openresponses.input":        map[string]any{"items": map[string]any{"count": 3}},
		"openresponses.output":       []any{map[string]any{"type": "message", "role": "assistant", "content": "answer"}},
	}}}
}

// Asking for `table` is not the same as asking for nothing, and the command
// used to answer the two differently by accident: it branched on whether the
// flag was set rather than on the value, so an explicit `-o table` swapped the
// reading view for a structured dump. A conversation has no columns, so a
// per-invocation ask for one is an error.
func TestTracesThreadRejectsTableFromEverySource(t *testing.T) {
	routes := []struct {
		name  string
		args  []string
		setup func(t *testing.T)
	}{
		{name: "flag", args: []string{"--output-format", "table"}},
		{name: "shorthand", args: []string{"-o", "table"}},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			if route.setup != nil {
				route.setup(t)
			}
			fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
			_, err := runTracesThread(t, traceAPI(fake), append(route.args, "trace-1", "chosen")...)
			if err == nil || !strings.Contains(err.Error(), "xml, markdown, json, yaml, toon") {
				t.Fatalf("err = %v, want the supported formats named", err)
			}
			var usage *bartolocli.UsageError
			if !errors.As(err, &usage) {
				t.Fatalf("err = %v (%T), want a value error so the exit code says input", err, err)
			}
		})
	}
	// Nobody named a format, so the reading view is what the command owes.
	t.Run("unset renders xml", func(t *testing.T) {
		fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
		out, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "<thread ") || !strings.Contains(out, "first") {
			t.Fatalf("output = %q", out)
		}
	})
}

// writeOutputFormatConfig puts the key in viper's config tier, the one
// viper.InConfig reports on — viper.Set writes the override tier above it and
// would not exercise the config route at all.
func writeOutputFormatConfig(t *testing.T, value string) {
	t.Helper()
	// Other tests in this package leave an empty override on this key, and an
	// override outranks the config tier — drop it for the duration so the
	// config is genuinely what answers. viper.Set(key, nil) is how an override
	// is removed; there is no Unset.
	previous := viper.Get("output-format")
	t.Cleanup(func() {
		viper.Set("output-format", previous)
		viper.SetConfigType("yaml")
		if err := viper.ReadConfig(strings.NewReader("")); err != nil {
			t.Fatalf("clearing config: %v", err)
		}
	})
	viper.Set("output-format", nil)
	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("output-format: " + value + "\n")); err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if !viper.InConfig("output-format") {
		t.Fatal("config did not take")
	}
}

// The two standing sources — an exported ORQ_OUTPUT_FORMAT and the config
// file — answer for every command in a shell or a tree. This command reads
// neither: a session that pinned a machine format for a pipeline should not
// have this render swapped out from under it, and a `markdown` written to the
// config would be refused for every other command before this one ran. -o is
// how this command is asked.
func TestTracesThreadIgnoresStandingDefaults(t *testing.T) {
	for _, value := range []string{"table", "json", "markdown", "csv"} {
		t.Run("environment "+value, func(t *testing.T) {
			t.Setenv(outputFormatEnvVar, value)
			assertThreadRendersXML(t)
		})
		t.Run("config "+value, func(t *testing.T) {
			writeOutputFormatConfig(t, value)
			assertThreadRendersXML(t)
		})
	}
	// Both at once, in case one is only ever masking the other.
	t.Run("both", func(t *testing.T) {
		writeOutputFormatConfig(t, "markdown")
		t.Setenv(outputFormatEnvVar, "json")
		assertThreadRendersXML(t)
	})
}

func assertThreadRendersXML(t *testing.T) {
	t.Helper()
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<thread ") || !strings.Contains(out, "first") {
		t.Fatalf("output = %q, want the render an unset environment and config get", out)
	}
}

// The flag is the only source, so it answers over a config that named
// something else.
func TestTracesThreadFlagOutranksTheConfigFile(t *testing.T) {
	writeOutputFormatConfig(t, "json")
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
	out, err := runTracesThread(t, traceAPI(fake), "-o", "markdown", "trace-1", "chosen")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## USER [0]") {
		t.Fatalf("markdown = %q", out)
	}
}

func TestRenderThreadMarkdownEscapesStructuralMetadata(t *testing.T) {
	thread := Thread{
		Source:   ThreadSource{TraceID: "trace`evil", SpanID: "span\nforged", Error: "bad\n> ## forged"},
		Messages: []ThreadMessage{{Index: 0, Role: "assistant\n## forged", Name: "name\n## forged", ToolCalls: []ThreadToolCall{{Name: "tool\n## forged", ID: "id`x", Arguments: "ok"}}}},
	}
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, thread, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\n## forged") || strings.Contains(out.String(), "\n### forged") {
		t.Fatalf("metadata injected Markdown structure: %s", out.String())
	}
	if !strings.Contains(out.String(), "trace `trace\\`evil`") {
		t.Fatalf("backtick metadata was not escaped: %s", out.String())
	}
}

func TestTracesThreadOutputFormat(t *testing.T) {
	t.Run("markdown", func(t *testing.T) {
		fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
		out, err := runTracesThread(t, traceAPI(fake), "-o", "markdown", "trace-1", "chosen")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "## USER [0]") || !strings.Contains(out, "first") || strings.Contains(out, "<thread") {
			t.Fatalf("markdown = %q", out)
		}
	})
	t.Run("yaml", func(t *testing.T) {
		fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
		out, err := runTracesThread(t, traceAPI(fake), "-o", "yaml", "trace-1", "chosen")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "messages:") || strings.Contains(out, "<thread") {
			t.Fatalf("yaml = %q", out)
		}
	})
	t.Run("rejects an unknown format", func(t *testing.T) {
		fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
		_, err := runTracesThread(t, traceAPI(fake), "-o", "csv", "trace-1", "chosen")
		if err == nil || !strings.Contains(err.Error(), "xml, markdown, json, yaml, toon") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("caps both renders", func(t *testing.T) {
		for _, format := range []string{"xml", "markdown"} {
			span := conversationalSpan(strings.Repeat("q", 100))
			fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": span}}
			out, err := runTracesThread(t, traceAPI(fake), "-o", format, "--max-chars", "20", "trace-1", "chosen")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "[truncated: 80 more characters]") {
				t.Fatalf("%s ignored --max-chars: %q", format, out)
			}
		}
	})
}

func TestTracesThreadReportsAnOutOfRangeSliceIndex(t *testing.T) {
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
	_, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen", "--slice", "99999999999999999999")
	// The expression is grammatical, so repeating the grammar would send the
	// reader back to retype what they typed.
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(fmt.Sprint(err), "2, 2:, :-1") || strings.Contains(fmt.Sprint(err), "strconv") {
		t.Fatalf("err = %v", err)
	}
}

func TestTracesThreadReportsSliceGrammar(t *testing.T) {
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
	_, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen", "--slice", "nonsense")
	if err == nil || !strings.Contains(err.Error(), "2, 2:, :-1 or 1:3") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(fmt.Sprint(err), "strconv") {
		t.Fatalf("error leaks Go internals: %v", err)
	}
}

func TestTracesThreadPrefersASpanThatKeptTheDroppedContent(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "root", "root_span_id": "root"}},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "newest", "started_at": "2026-09-03T10:00:02Z"},
			map[string]any{"span_id": "root", "started_at": "2026-09-03T10:00:00Z"},
		}}},
		spans: map[string]map[string]any{
			"newest": partialSpan(),
			"root":   conversationalSpan("the question the newest span lost"),
		},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "the question the newest span lost") || strings.Contains(out, "content unavailable") {
		t.Fatalf("Markdown = %q", out)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:newest", "span:trace-1:root"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestTracesThreadKeepsThePartialThreadWhenNoSpanKeptMore(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "root", "root_span_id": "root"}},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "newest", "started_at": "2026-09-03T10:00:02Z"},
			map[string]any{"span_id": "root", "started_at": "2026-09-03T10:00:00Z"},
		}}},
		spans: map[string]map[string]any{"newest": partialSpan(), "root": partialSpan()},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "content unavailable: 3 items") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadStopsAtTheNewestWholeConversation(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "root", "root_span_id": "root"}},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "newest", "started_at": "2026-09-03T10:00:02Z"},
			map[string]any{"span_id": "root", "started_at": "2026-09-03T10:00:00Z"},
		}}},
		spans: map[string]map[string]any{
			"newest": conversationalSpan("newest"),
			"root":   conversationalSpan("root must not be hydrated"),
		},
	}
	if _, err := runTracesThread(t, traceAPI(fake), "trace-1"); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:newest"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestTracesThreadKeepsScanningPastAShorterWholeSpan(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "root", "root_span_id": "root"}},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "newest", "started_at": "2026-09-03T10:00:03Z"},
			map[string]any{"span_id": "middle", "started_at": "2026-09-03T10:00:02Z"},
			map[string]any{"span_id": "root", "started_at": "2026-09-03T10:00:00Z"},
		}}},
		spans: map[string]map[string]any{
			"newest": partialSpan(),
			"middle": {"span": map[string]any{"attributes": map[string]any{
				"gen_ai.output": map[string]any{"role": "assistant", "content": "answer only"},
			}}},
			"root": conversationalSpan("the question every later span lost"),
		},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "the question every later span lost") {
		t.Fatalf("Markdown = %q", out)
	}
}

func TestTracesThreadOmitsReasoningOnRequest(t *testing.T) {
	span := map[string]any{"span": map[string]any{"attributes": map[string]any{"gen_ai.input": []any{
		map[string]any{"role": "assistant", "reasoning_content": "step by step", "content": "Done."},
	}}}}
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": span}}
	kept, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kept, "step by step") {
		t.Fatalf("Markdown = %q, want the reasoning by default", kept)
	}
	dropped, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen", "--reasoning=false")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dropped, "step by step") || !strings.Contains(dropped, "Done.") {
		t.Fatalf("Markdown = %q", dropped)
	}
}

// Whether a machine format was asked for is a question about the sources, not
// about a value: this command registers its own -o with its own default, and
// comparing the resolved value against that default made every run of it a
// machine-format request — which drops the which-credential notice and the
// update check on a person who is reading the XML view at a terminal.
//
// On this command the flag is the only source, because the flag is all the
// command itself reads: a standing default counted here would suppress those
// notices on the very run that renders the readable thread.
func TestMachineFormatRequestedFollowsTheSourceNotTheFlagDefault(t *testing.T) {
	// What the bound global -o resolves to when nobody names a format: the
	// CLI-wide default. It is the value the comparison used to read as a
	// request on this command.
	previous := viper.Get("output-format")
	t.Cleanup(func() { viper.Set("output-format", previous) })
	viper.Set("output-format", outputFormatTable)

	cases := []struct {
		name        string
		flag        string
		env         string
		config      string
		wantMachine bool
	}{
		{name: "nobody named one"},
		{name: "the flag named a serialization", flag: "json", wantMachine: true},
		// -o table is a per-invocation ask for the layout bartolo lays out,
		// which is the structured view, not this CLI's friendly one.
		{name: "the flag named the table layout", flag: "table", wantMachine: true},
		// The reading views this command adds are for a person, so naming one
		// is not a reason to drop the notices written for that person.
		{name: "the flag named a reading view", flag: "markdown"},
		// Neither standing source reaches this command's render, so neither
		// says anything about what this run is producing.
		{name: "the environment named a serialization", env: "json"},
		{name: "the environment named a reading view", env: "markdown"},
		{name: "the environment named the CLI-wide default", env: "table"},
		{name: "the config named a serialization", config: "json"},
		{name: "the config named a reading view", config: "markdown"},
		{name: "the config named the CLI-wide default", config: "table"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(outputFormatEnvVar, tc.env)
			if tc.config != "" {
				writeOutputFormatConfig(t, tc.config)
			}
			cmd := NewTracesThreadCommand(TraceAPI{})
			if tc.flag != "" {
				if err := cmd.Flags().Set("output-format", tc.flag); err != nil {
					t.Fatal(err)
				}
			}
			if got := machineFormatRequested(cmd); got != tc.wantMachine {
				t.Fatalf("machineFormatRequested = %v, want %v", got, tc.wantMachine)
			}
		})
	}
}

// Every other command does read the standing sources, and a user who
// configured a machine format there wants the structured output rather than
// the friendly view. Only the annotated command is classified from its flag.
func TestMachineFormatRequestedStillReadsStandingDefaultsElsewhere(t *testing.T) {
	previous := viper.Get("output-format")
	t.Cleanup(func() { viper.Set("output-format", previous) })
	viper.Set("output-format", outputFormatTable)

	cases := []struct {
		name        string
		env         string
		config      string
		wantMachine bool
	}{
		{name: "the environment named a serialization", env: "json", wantMachine: true},
		{name: "the environment named the CLI-wide default", env: "table"},
		{name: "the config named a serialization", config: "json", wantMachine: true},
		{name: "the config named the CLI-wide default", config: "table"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(outputFormatEnvVar, tc.env)
			if tc.config != "" {
				writeOutputFormatConfig(t, tc.config)
			}
			cmd := &cobra.Command{Use: "other"}
			cmd.Flags().StringP("output-format", "o", "", "")
			if got := machineFormatRequested(cmd); got != tc.wantMachine {
				t.Fatalf("machineFormatRequested = %v, want %v", got, tc.wantMachine)
			}
		})
	}
}

// A paging failure still leaves candidates and the trace's own fallback IDs to
// hydrate, so the command can return an older span and exit 0. Say on stderr
// that the pool was partial rather than presenting it as the whole trace.
func TestTracesThreadReportsAPartialSpanListing(t *testing.T) {
	previous := bartolocli.Stderr
	var stderr bytes.Buffer
	bartolocli.Stderr = &stderr
	t.Cleanup(func() { bartolocli.Stderr = previous })

	fake := &fakeTraceAPI{
		trace:   map[string]any{"trace": map[string]any{"leading_span_id": "lead"}},
		spans:   map[string]map[string]any{"lead": conversationalSpan("leading fallback")},
		listErr: errors.New("listing unavailable"),
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "leading fallback") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(stderr.String(), "listing unavailable") || !strings.Contains(stderr.String(), "0 span(s)") {
		t.Fatalf("stderr = %q, want the listing failure and how many spans were seen", stderr.String())
	}
}

func TestTracesThreadListsSpansWithTheOrderSelectionWouldTry(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "new"}},
		spans: map[string]map[string]any{"new": conversationalSpan("answered here")},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "old", "has_detail": true, "started_at": "2025-01-01T00:00:00Z", "name": "generation"},
			map[string]any{"span_id": "new", "has_detail": true, "started_at": "2025-01-02T00:00:00Z", "name": "generation"},
			map[string]any{"span_id": "eval", "type": "EVALUATOR", "has_detail": true, "started_at": "2025-01-03T00:00:00Z"},
			map[string]any{"span_id": "thin", "has_detail": false, "started_at": "2025-01-04T00:00:00Z"},
		}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans")
	if err != nil {
		t.Fatal(err)
	}
	// Newest first among the candidates, the selected one marked, then the
	// skipped ones with a reason.
	var rows [][]string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		rows = append(rows, strings.Fields(line))
	}
	want := [][]string{
		{"*", "1", "new", "2025-01-02T00:00:00Z", "2", "generation"},
		{"2", "old", "2025-01-01T00:00:00Z", "generation", "no", "conversation", "recorded"},
		{"-", "eval", "EVALUATOR", "2025-01-03T00:00:00Z", "evaluator"},
		{"-", "thin", "2025-01-04T00:00:00Z", "no", "recorded", "detail"},
	}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Fatalf("--spans rows = %v, want %v", rows, want)
	}
	// The listing and the trace are read once between the table and the
	// selection it reports, not once for each. Selection stops at the span
	// that answers; the other candidate is read only for its turn count.
	if got, want := fake.getCalls, []string{"trace:trace-1", "span:trace-1:new", "span:trace-1:old"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if len(fake.listCalls) != 1 {
		t.Fatalf("listed spans %d times, want 1", len(fake.listCalls))
	}
}

// Selection is not the try order: a first span that hydrates with content
// dropped loses to a later one that kept the turns, and the mark has to follow
// the answer rather than the position.
func TestTracesThreadMarksTheSelectedSpanNotTheFirstTried(t *testing.T) {
	dropped := map[string]any{"span": map[string]any{"attributes": map[string]any{
		"openresponses.input": map[string]any{"items": map[string]any{"count": 3}},
	}}}
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "new"}},
		spans: map[string]map[string]any{"new": dropped, "old": conversationalSpan("the whole conversation")},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "new", "has_detail": true, "started_at": "2025-01-02T00:00:00Z"},
			map[string]any{"span_id": "old", "has_detail": true, "started_at": "2025-01-01T00:00:00Z"},
		}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Spans []ThreadSpan `json:"spans"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	want := []string{
		"new order=1 turns=1 note=" + threadNoteUnstored,
		"old order=2 turns=2 selected",
	}
	if got := describeThreadSpans(payload.Spans); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("spans = %v, want %v", got, want)
	}
}

func TestTracesThreadListsSpansAsJSON(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "new"}},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "new", "has_detail": true, "started_at": "2025-01-02T00:00:00Z"},
			map[string]any{"span_id": "eval", "type": "evaluator", "has_detail": true},
		}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Spans []ThreadSpan `json:"spans"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if len(payload.Spans) != 2 {
		t.Fatalf("spans = %+v", payload.Spans)
	}
	if payload.Spans[0].SpanID != "new" || payload.Spans[0].Order != 1 {
		t.Fatalf("first span = %+v", payload.Spans[0])
	}
	if payload.Spans[1].Skipped != "evaluator" || payload.Spans[1].Order != 0 {
		t.Fatalf("skipped span = %+v", payload.Spans[1])
	}
}

func TestTracesThreadRefusesASpanArgumentWithSpans(t *testing.T) {
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("explicit")}}
	_, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen", "--spans")
	if err == nil || !strings.Contains(err.Error(), "no span-id argument") {
		t.Fatalf("error = %v, want the --spans argument refusal", err)
	}
}

func TestTracesThreadMatchesTheRenderedThread(t *testing.T) {
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("the question")}}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen", "--match", "question")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "the question") || strings.Contains(out, "answer") {
		t.Fatalf("--match kept the wrong messages: %q", out)
	}
}

// --slice indexes the thread as recorded, so it runs before --match: a slice of
// the matches would move under the pattern, which is not what a position means.
func TestTracesThreadSlicesBeforeItMatches(t *testing.T) {
	span := map[string]any{"span": map[string]any{"attributes": map[string]any{
		"gen_ai.input": []any{
			map[string]any{"role": "user", "content": "alpha"},
			map[string]any{"role": "assistant", "content": "bravo"},
			map[string]any{"role": "user", "content": "charlie"},
		},
		"gen_ai.output": map[string]any{"role": "assistant", "content": "delta"},
	}}}
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": span}}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen", "--slice", "2:", "--match", "bravo|charlie")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "charlie") || strings.Contains(out, "bravo") {
		t.Fatalf("slice ran after the match: %q", out)
	}
}

// A wrong id is the usual reason a trace lists no spans, so --spans says so on
// stderr and still emits a list a script can parse rather than a null.
func TestTracesThreadReportsATraceWithNoSpans(t *testing.T) {
	fake := &fakeTraceAPI{pages: map[string]map[string]any{"": {"data": []any{}}}}
	out, err := runTracesThread(t, traceAPI(fake), "01M1W1HGV4HR3MAZWD0N222D1E", "--spans", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"spans": []`) {
		t.Fatalf("--spans = %q, want an empty list", out)
	}
}

// The trace's own leading and root span are tried when nothing in the listing
// hydrates, no-detail spans included, so the try order has to show them rather
// than call them skipped.
func TestTracesThreadOrdersTheTraceFallbackAfterTheListing(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "thin", "root_span_id": "unlisted"}},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "listed", "has_detail": true, "started_at": "2025-01-02T00:00:00Z"},
			map[string]any{"span_id": "thin", "has_detail": false, "started_at": "2025-01-01T00:00:00Z"},
		}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Spans []ThreadSpan `json:"spans"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	want := []string{
		"listed order=1 note=no conversation recorded",
		"thin order=2 note=tried anyway: no recorded detail",
		"unlisted order=3 note=named by the trace, not in its span listing",
	}
	if got := describeThreadSpans(payload.Spans); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("spans = %v, want %v", got, want)
	}
}

// Depth decides the try order before time does. The conversation lives in the
// most specific span that recorded one, and a clock that says the parent
// started last — skew between two services, or a root closed after its
// children — must not put the trace span ahead of the model call under it.
func TestTracesThreadTriesTheDeepestSpanFirstDespiteTheClock(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "root"}},
		spans: map[string]map[string]any{"completion": conversationalSpan("the conversation")},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "root", "type": "trace", "has_detail": true, "started_at": "2025-01-01T00:00:09Z"},
			map[string]any{"span_id": "agent", "type": "span.agent_execution", "parent_span_id": "root", "has_detail": true, "started_at": "2025-01-01T00:00:05Z"},
			map[string]any{"span_id": "completion", "type": "span.chat_completion", "parent_span_id": "agent", "has_detail": true, "started_at": "2025-01-01T00:00:01Z"},
		}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Spans []ThreadSpan `json:"spans"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	order := []string{}
	for _, span := range payload.Spans {
		order = append(order, span.SpanID)
	}
	if fmt.Sprint(order) != fmt.Sprint([]string{"completion", "agent", "root"}) {
		t.Fatalf("try order = %v, want the deepest span first", order)
	}
	if !payload.Spans[0].Selected {
		t.Fatalf("selected = %+v, want the model call", payload.Spans)
	}
	if got, want := fake.getCalls[:2], []string{"trace:trace-1", "span:trace-1:completion"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want the deepest span read first", got)
	}
}

// describeThreadSpans renders the fields --spans is asserted on, since Messages
// is a pointer and a formatted struct would compare addresses.
func describeThreadSpans(spans []ThreadSpan) []string {
	described := make([]string, 0, len(spans))
	for _, span := range spans {
		text := span.SpanID
		if span.Order > 0 {
			text += fmt.Sprintf(" order=%d", span.Order)
		}
		if span.Messages != nil {
			text += fmt.Sprintf(" turns=%d", *span.Messages)
		}
		if span.Skipped != "" {
			text += " skipped=" + span.Skipped
		}
		if span.Note != "" {
			text += " note=" + span.Note
		}
		if span.Selected {
			text += " selected"
		}
		described = append(described, text)
	}
	return described
}

func TestTracesThreadReadsTheStoredResponseASpanOnlyCounted(t *testing.T) {
	fake := &fakeTraceAPI{
		trace:     map[string]any{"trace": map[string]any{"root_span_id": "counted"}},
		spans:     map[string]map[string]any{"counted": storedResponseSpan("resp_1", 1)},
		responses: map[string]map[string]any{"resp_1": storedResponsePayload("what changed", "the schema did")},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	for _, want := range []string{"what changed", "the schema did", `response="resp_1"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in the render, got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "content unavailable") {
		return
	}
	t.Fatalf("the stored response was read, so nothing should be reported unavailable:\n%s", out)
}

func TestTracesThreadKeepsTheSpanWhenNoStoredResponseAnswers(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"root_span_id": "counted"}},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "counted", "type": "span.responses", "has_detail": true, "started_at": "2026-09-08T22:09:03.148Z"},
		}}},
		spans:   map[string]map[string]any{"counted": storedResponseSpan("resp_gone", 1)},
		respErr: map[string]error{"resp_gone": errors.New("HTTP 404")},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans")
	if err != nil {
		t.Fatalf("--spans: %v", err)
	}
	if !strings.Contains(out, threadNoteStoredGone) {
		t.Fatalf("expected the unreadable stored response to be named, got:\n%s", out)
	}
}

func TestTracesThreadSaysWhenASpanNamesNoStoredResponse(t *testing.T) {
	span := storedResponseSpan("", 1)
	delete(span["span"].(map[string]any)["attributes"].(map[string]any), "gen_ai")
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"root_span_id": "counted"}},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "counted", "type": "span.responses", "has_detail": true, "started_at": "2026-09-08T22:09:03.148Z"},
		}}},
		spans: map[string]map[string]any{"counted": span},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans")
	if err != nil {
		t.Fatalf("--spans: %v", err)
	}
	if !strings.Contains(out, threadNoteUnstored) {
		t.Fatalf("expected the missing stored response to be named, got:\n%s", out)
	}
	if len(fake.respCalls) != 0 {
		t.Fatalf("a span naming no response id should cost no request, got %v", fake.respCalls)
	}
}

func TestTracesThreadDoesNotFetchAProviderResponseID(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"root_span_id": "counted"}},
		spans: map[string]map[string]any{"counted": storedResponseSpan("252ef0cc-8dd6-46e2-a92a-60f617cde01f", 1)},
	}
	if _, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans"); err != nil {
		t.Fatalf("--spans: %v", err)
	}
	if len(fake.respCalls) != 0 {
		t.Fatalf("only gateway resp_ ids resolve, so none should be requested, got %v", fake.respCalls)
	}
}

func TestTracesThreadReadsAnEvaluatorRootedTrace(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"root_span_id": "root"}},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "root", "type": "span.evaluator", "name": "InvokeEvaluator", "has_detail": true, "started_at": "2026-09-08T22:09:03.140Z"},
			map[string]any{"span_id": "judged", "parent_span_id": "root", "type": "span.responses", "has_detail": true, "started_at": "2026-09-08T22:09:03.148Z"},
		}}},
		spans: map[string]map[string]any{"judged": conversationalSpan("the judged turn")},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if !strings.Contains(out, "the judged turn") {
		t.Fatalf("an evaluator at the root leaves no other subtree to read, got:\n%s", out)
	}
}

func TestTracesThreadStillSkipsAnEvaluatorBesideAConversation(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"root_span_id": "root"}},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "root", "type": "trace", "has_detail": true, "started_at": "2026-09-08T22:09:03.140Z"},
			map[string]any{"span_id": "chat", "parent_span_id": "root", "type": "span.chat_completion", "has_detail": true, "started_at": "2026-09-08T22:09:03.141Z"},
			map[string]any{"span_id": "judge", "parent_span_id": "root", "type": "span.evaluator", "has_detail": true, "started_at": "2026-09-08T22:09:03.900Z"},
		}}},
		spans: map[string]map[string]any{"chat": conversationalSpan("the real turn"), "judge": conversationalSpan("the judge's turn")},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if strings.Contains(out, "the judge's turn") {
		t.Fatalf("the evaluator subtree must stay excluded when another span holds the conversation:\n%s", out)
	}
}

// A 404 the workspace search cannot explain still says which project the read
// looked in, so "not found" is never read as "does not exist".
func TestTracesThreadNamesProjectScopingOnAMissingTrace(t *testing.T) {
	switchTestEnv(t)
	srv := switchServer(t, []string{"acme"}, "")
	switchSession(t, srv.URL, "acme", []string{"acme"}, "id-1", "Banking")
	fake := &fakeTraceAPI{traceErr: errors.New("HTTP 404: trace not found")}
	_, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err == nil {
		t.Fatal("expected the missing trace to fail")
	}
	if !strings.Contains(err.Error(), `project "Banking"`) || !strings.Contains(err.Error(), "orq projects use") {
		t.Fatalf("a trace read is project-scoped and the 404 does not say so, got: %v", err)
	}
}

// A stored response that answers 404 is gone; anything else means the turns
// are probably still there and the read failed. A reader deciding whether to
// retry needs the two spelled differently.
func TestTracesThreadTellsAGoneStoredResponseFromAnUnreadableOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"gone", errors.New("HTTP 404:\n{\"error\":\"not found\"}"), threadNoteStoredGone},
		{"unreadable", errors.New("HTTP 401:\nunauthorized"), "the stored response this span names could not be read: HTTP 401:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTraceAPI{
				trace: map[string]any{"trace": map[string]any{"leading_span_id": "a"}},
				spans: map[string]map[string]any{"a": storedResponseSpan("resp_1", 2)},
				pages: map[string]map[string]any{"": {"data": []any{
					map[string]any{"span_id": "a", "has_detail": true},
				}}},
				respErr: map[string]error{"resp_1": tc.err},
			}
			out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans", "-o", "json")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("--spans = %s, want note %q", out, tc.want)
			}
		})
	}
}

// A span naming the provider's own response id named something real; it is
// this API that cannot read it. Saying it "names no stored response" would
// send a reader looking for a recording bug that is not there.
func TestTracesThreadSaysWhenAResponseIDIsTheProvidersOwn(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "a"}},
		spans: map[string]map[string]any{"a": storedResponseSpan("2f0d0c22-8a2f-4c1e-9c0a-1d2e3f405162", 2)},
		pages: map[string]map[string]any{"": {"data": []any{map[string]any{"span_id": "a", "has_detail": true}}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, threadNoteProviderID) {
		t.Fatalf("--spans = %s, want note %q", out, threadNoteProviderID)
	}
	if len(fake.respCalls) != 0 {
		t.Fatalf("fetched %v, want no stored-response read", fake.respCalls)
	}
}

// A rejected call records no conversation, and "no conversation recorded"
// alone reads as a gap in this command rather than a request that failed.
func TestTracesThreadNamesTheErrorOnAFailedSpan(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "bad"}},
		spans: map[string]map[string]any{
			"bad": {"span": map[string]any{"attributes": map[string]any{"error.type": "BadRequestError"}}},
			"ok":  conversationalSpan("hello"),
		},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "bad", "has_detail": true, "started_at": "2025-01-02T00:00:00Z"},
			map[string]any{"span_id": "ok", "has_detail": true, "started_at": "2025-01-01T00:00:00Z"},
		}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "BadRequestError") {
		t.Fatalf("--spans = %s, want the error type named", out)
	}
}

// The orq agent runtime records the opening turn on the span and keeps the
// reply out of the trace. A span with a prompt and no answer is not the whole
// conversation, so selection has to keep looking at its siblings.
func TestTracesThreadKeepsLookingPastASpanWithNoReply(t *testing.T) {
	fake := &fakeTraceAPI{
		trace: map[string]any{"trace": map[string]any{"leading_span_id": "prompt"}},
		spans: map[string]map[string]any{
			"prompt": {"span": map[string]any{"attributes": map[string]any{
				"gen_ai.input": []any{map[string]any{"role": "user", "content": "hello"}},
			}}},
			"full": conversationalSpan("hello"),
		},
		pages: map[string]map[string]any{"": {"data": []any{
			map[string]any{"span_id": "prompt", "has_detail": true, "started_at": "2025-01-02T00:00:00Z"},
			map[string]any{"span_id": "full", "has_detail": true, "started_at": "2025-01-01T00:00:00Z"},
		}}},
	}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "--spans", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, threadNoteNoAnswer) {
		t.Fatalf("--spans = %s, want note %q", out, threadNoteNoAnswer)
	}
	var payload struct {
		Spans []ThreadSpan `json:"spans"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	for _, span := range payload.Spans {
		if span.Selected && span.SpanID != "full" {
			t.Fatalf("selected %q, want the span holding the reply", span.SpanID)
		}
	}
}
