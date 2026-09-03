package commands

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
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
		viper.Set("json", false)
		viper.Set("output-format", "")
	})
	humanOutput = func() bool { return false }
	var out bytes.Buffer
	bartolocli.Stdout = &out
	bartolocli.Formatter = bartolocli.NewDefaultFormatter(false, false)
	root := &cobra.Command{
		Use:           "orq",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if flag := cmd.Flags().Lookup("json"); flag != nil && flag.Changed && flag.Value.String() == "true" {
				viper.Set("json", true)
				viper.Set("output-format", "json")
			}
			return nil
		},
	}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().StringP("output-format", "o", "table", "")
	root.PersistentFlags().VisitAll(func(flag *pflag.Flag) { _ = viper.BindPFlag(flag.Name, flag) })
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

func TestTracesThreadUsesCanonicalMachineFormatsAndSlices(t *testing.T) {
	formats := []struct {
		name string
		args []string
	}{
		{name: "json", args: []string{"--json"}},
		{name: "yaml", args: []string{"--output-format", "yaml"}},
		{name: "toon", args: []string{"--output-format", "toon"}},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			fake := &fakeTraceAPI{spans: map[string]map[string]any{"chosen": conversationalSpan("first")}}
			out, err := runTracesThread(t, traceAPI(fake), append(format.args, "trace-1", "chosen")...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "## USER") || !strings.Contains(out, "messages") {
				t.Fatalf("machine output = %q", out)
			}
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
