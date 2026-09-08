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

func traceAPI(fake *fakeTraceAPI) TraceAPI {
	return TraceAPI{GetTrace: fake.getTrace, GetSpan: fake.getSpan, ListSpans: fake.listSpans}
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

func TestTracesThreadNeverUsesExcludedEvaluatorFallback(t *testing.T) {
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
	_, err := runTracesThread(t, traceAPI(fake), "trace-1")
	if err == nil || !strings.Contains(err.Error(), "no supported conversation") {
		t.Fatalf("error = %v", err)
	}
	if got, want := fake.getCalls, []string{"trace:trace-1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v, want %v", got, want)
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
// per-invocation ask for one is an error, whichever of the two sources that ask
// arrives from.
func TestTracesThreadRejectsTableFromEverySource(t *testing.T) {
	routes := []struct {
		name  string
		args  []string
		setup func(t *testing.T)
	}{
		{name: "flag", args: []string{"--output-format", "table"}},
		{name: "shorthand", args: []string{"-o", "table"}},
		{name: "environment", setup: func(t *testing.T) { t.Setenv(outputFormatEnvVar, "table") }},
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
	// The config file is not an ask, it is the standing default: `orq
	// default-format table` writes the CLI-wide one, and answering it with an
	// error would tell the user to ask for nothing when asking for nothing is
	// exactly what they did at the command line.
	t.Run("config renders xml", func(t *testing.T) {
		writeOutputFormatConfig(t, "table")
		fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
		out, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen")
		if err != nil {
			t.Fatalf("a config-file default bricked the command: %v", err)
		}
		if !strings.Contains(out, "<thread ") || !strings.Contains(out, "first") {
			t.Fatalf("output = %q", out)
		}
	})
	// Every other format the config names is still honoured.
	t.Run("config selects a format it can render", func(t *testing.T) {
		writeOutputFormatConfig(t, "json")
		fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
		out, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `"messages"`) || strings.Contains(out, "<thread") {
			t.Fatalf("config json = %q", out)
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

// The environment must be genuinely what answers, so pin a value neither the
// flag default nor the config can supply.
func TestTracesThreadSerializesFromTheEnvironment(t *testing.T) {
	t.Setenv(outputFormatEnvVar, "json")
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"messages"`) || strings.Contains(out, "<thread") {
		t.Fatalf("environment json = %q", out)
	}
}

// The environment is a per-invocation ask and the config file a standing
// default, so the environment answers over it. Swapping the two branches in
// resolveThreadFormat breaks nothing without this.
func TestTracesThreadEnvironmentOutranksTheConfigFile(t *testing.T) {
	writeOutputFormatConfig(t, "markdown")
	t.Setenv(outputFormatEnvVar, "json")
	fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
	out, err := runTracesThread(t, traceAPI(fake), "trace-1", "chosen")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"messages"`) || strings.Contains(out, "## USER") {
		t.Fatalf("environment json = %q", out)
	}
}

// The flag is the nearest source, so it answers over an environment or a
// config that named something else.
func TestTracesThreadFlagOutranksTheEnvironment(t *testing.T) {
	t.Setenv(outputFormatEnvVar, "table")
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

// The two renders this command adds are not values bartolo's root will accept
// in viper, so an environment or config file naming one has to be let past that
// check — see RelaxOutputFormat, which run.go wraps the check with.
func TestRelaxOutputFormat(t *testing.T) {
	thread := NewTracesThreadCommand(TraceAPI{})
	cases := []struct {
		name    string
		cmd     *cobra.Command
		value   string
		relaxed bool
	}{
		{name: "markdown", cmd: thread, value: "markdown", relaxed: true},
		{name: "xml", cmd: thread, value: "xml", relaxed: true},
		// Unknown values are relaxed too: the command's own error names the
		// formats it renders, where bartolo's would name a list that has
		// neither xml nor markdown on it.
		{name: "unknown", cmd: thread, value: "csv", relaxed: true},
		{name: "a format bartolo accepts", cmd: thread, value: "json"},
		{name: "unset", cmd: thread, value: ""},
		{name: "another command", cmd: &cobra.Command{Use: "other"}, value: "markdown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previous := viper.Get("output-format")
			t.Cleanup(func() { viper.Set("output-format", previous) })
			viper.Set("output-format", tc.value)
			restore, relaxed := RelaxOutputFormat(tc.cmd)
			if relaxed != tc.relaxed {
				t.Fatalf("relaxed = %v, want %v", relaxed, tc.relaxed)
			}
			if !relaxed {
				return
			}
			if got := viper.GetString("output-format"); got != outputFormatTable {
				t.Fatalf("masked value = %q, want %q so bartolo's check passes", got, outputFormatTable)
			}
			restore()
			if got := viper.GetString("output-format"); got != tc.value {
				t.Fatalf("after restore = %q, want %q", got, tc.value)
			}
		})
	}
}

// The relaxed value came from a tier below the override, and restore() has to
// put it back in that tier rather than re-Set it: viper.Set writes the override
// tier, which outranks the environment and the config file, so restoring by Set
// promotes the value above the source it came from and pins it there for the
// rest of the process.
func TestRelaxOutputFormatRestoresTheTierTheValueCameFrom(t *testing.T) {
	thread := NewTracesThreadCommand(TraceAPI{})
	writeOutputFormatConfig(t, "markdown")

	restore, relaxed := RelaxOutputFormat(thread)
	if !relaxed {
		t.Fatal("a config-file markdown was not relaxed, so bartolo's check would reject it")
	}
	if got := viper.GetString("output-format"); got != outputFormatTable {
		t.Fatalf("masked value = %q, want %q so bartolo's check passes", got, outputFormatTable)
	}
	restore()
	if got := viper.GetString("output-format"); got != "markdown" {
		t.Fatalf("after restore = %q, want the config file's value back", got)
	}

	// Reading a new config is what tells the tiers apart: the config tier
	// answers with the new value, an override left behind keeps shadowing it.
	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("output-format: xml\n")); err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if got := viper.GetString("output-format"); got != "xml" {
		t.Fatalf("after restore the config file no longer answers (%q): the value was promoted into viper's override tier", got)
	}
}

// Whether a machine format was asked for is a question about the sources, not
// about a value: this command registers its own -o with its own default, and
// comparing the resolved value against that default made every run of it a
// machine-format request — which drops the which-credential notice and the
// update check on a person who is reading the XML view at a terminal.
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
		{name: "the environment named a serialization", env: "json", wantMachine: true},
		{name: "the environment named a reading view", env: "markdown"},
		{name: "the environment named the xml render", env: "xml"},
		// An exported ORQ_OUTPUT_FORMAT=table is a standing default like the
		// config file's, not a request; usually it just repeats what the CLI
		// already does.
		{name: "the environment named the CLI-wide default", env: "table"},
		{name: "the config named a serialization", config: "json", wantMachine: true},
		{name: "the config named a reading view", config: "markdown"},
		// `orq default-format table` writes the value the CLI already defaults
		// to. Reading that as a request would take the human view away from
		// every command for a user who changed nothing.
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
