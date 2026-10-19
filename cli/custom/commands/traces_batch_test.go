package commands

import (
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// batchTraceAPI is an inline TraceAPI with per-id behaviour: tr_bad fails, every
// other id returns a trace/spans naming itself. The shapes mirror the real API:
// GetTrace returns the {trace: {...}} envelope, ListSpans the {data: [...]} one.
func batchTraceAPI() TraceAPI {
	return TraceAPI{
		GetTrace: func(id string, _ *viper.Viper) (map[string]any, error) {
			if id == "tr_bad" {
				return nil, errors.New("trace not found")
			}
			return map[string]any{"trace": map[string]any{"id": id}}, nil
		},
		ListSpans: func(id string, _ *viper.Viper) (map[string]any, error) {
			if id == "tr_bad" {
				return nil, errors.New("spans not found")
			}
			return map[string]any{"data": []any{map[string]any{"span_id": "sp_" + id}}}, nil
		},
	}
}

// traceID unwraps a batchResult.Trace (an `any` holding the trace map) to its id.
func traceID(r batchResult) any {
	m, _ := r.Trace.(map[string]any)
	return m["id"]
}

func TestFetchTraceBatchKeepsOrderAndIsolatesFailures(t *testing.T) {
	ids := []string{"tr_1", "tr_bad", "tr_2"}
	got := fetchTraceBatch(batchTraceAPI(), ids, false)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, id := range ids {
		if got[i].ID != id {
			t.Fatalf("result[%d].ID = %q, want %q (order not preserved)", i, got[i].ID, id)
		}
	}
	// The trace is unwrapped from its envelope: {id, trace: {...}}, not {trace: {trace}}.
	if got[0].Error != "" || traceID(got[0]) != "tr_1" {
		t.Errorf("tr_1 = %+v, want the unwrapped trace and no error", got[0])
	}
	if got[1].Error == "" || got[1].Trace != nil {
		t.Errorf("tr_bad = %+v, want an error and no trace", got[1])
	}
	if got[2].Error != "" || traceID(got[2]) != "tr_2" {
		t.Errorf("tr_2 = %+v, want the unwrapped trace and no error", got[2])
	}
}

func TestFetchTraceBatchSpansFlattensEnvelope(t *testing.T) {
	got := fetchTraceBatch(batchTraceAPI(), []string{"tr_1"}, true)
	if got[0].Trace != nil {
		t.Errorf("--spans must not set trace: %+v", got[0])
	}
	spans, ok := got[0].Spans.([]map[string]any)
	if !ok || len(spans) != 1 || spans[0]["span_id"] != "sp_tr_1" {
		t.Errorf("--spans must flatten ListSpans data to the span array, got %+v", got[0].Spans)
	}
}

// --spans pages through all span pages, not just the first (RES-1255 review P2).
func TestFetchTraceBatchSpansPaginates(t *testing.T) {
	api := TraceAPI{
		ListSpans: func(id string, params *viper.Viper) (map[string]any, error) {
			if params.GetString("page-token") == "" {
				return map[string]any{
					"data":            []any{map[string]any{"span_id": "sp_1"}},
					"next_page_token": "p2",
				}, nil
			}
			return map[string]any{"data": []any{map[string]any{"span_id": "sp_2"}}}, nil
		},
	}
	got := fetchTraceBatch(api, []string{"tr_1"}, true)
	spans, ok := got[0].Spans.([]map[string]any)
	if !ok || len(spans) != 2 {
		t.Fatalf("want both pages (2 spans), got %+v", got[0].Spans)
	}
}

// An empty response (no trace, or a nil body) must become the id's error, not a
// bare {id} that a consumer filtering on .error == null would miscount as fetched.
func TestFetchTraceBatchEmptyTraceIsError(t *testing.T) {
	api := TraceAPI{
		GetTrace: func(id string, _ *viper.Viper) (map[string]any, error) {
			switch id {
			case "tr_nil":
				return nil, nil // no error, but nothing came back
			case "tr_empty":
				return map[string]any{}, nil // bare envelope, no trace
			}
			return map[string]any{"trace": map[string]any{"id": id}}, nil
		},
	}
	got := fetchTraceBatch(api, []string{"tr_nil", "tr_empty", "tr_ok"}, false)
	for i, id := range []string{"tr_nil", "tr_empty"} {
		if got[i].Error == "" || got[i].Trace != nil {
			t.Errorf("%s = %+v, want an error and no trace", id, got[i])
		}
	}
	if got[2].Error != "" || traceID(got[2]) != "tr_ok" {
		t.Errorf("tr_ok = %+v, want the trace and no error", got[2])
	}
}

// A spans response with no `data` array is malformed and must error, not print as
// a trace with zero spans. A present-but-empty `data` is a genuine zero-span trace.
func TestFetchTraceBatchSpansDistinguishesEmptyFromMalformed(t *testing.T) {
	api := TraceAPI{
		ListSpans: func(id string, _ *viper.Viper) (map[string]any, error) {
			if id == "tr_malformed" {
				return map[string]any{"next_page_token": ""}, nil // no data key
			}
			return map[string]any{"data": []any{}}, nil // genuinely zero spans
		},
	}
	got := fetchTraceBatch(api, []string{"tr_malformed", "tr_zero"}, true)
	if got[0].Error == "" || got[0].Spans != nil {
		t.Errorf("malformed spans response = %+v, want an error and no spans", got[0])
	}
	spans, ok := got[1].Spans.([]map[string]any)
	if got[1].Error != "" || !ok || len(spans) != 0 {
		t.Errorf("zero-span trace = %+v, want an empty span list and no error", got[1])
	}
}

// The semaphore must bound concurrent requests. Delete `sem` and this fails:
// all ids would run at once and maxInFlight would exceed the limit.
func TestFetchTraceBatchBoundsConcurrency(t *testing.T) {
	var inFlight, maxInFlight int64
	api := TraceAPI{
		GetTrace: func(id string, _ *viper.Viper) (map[string]any, error) {
			cur := atomic.AddInt64(&inFlight, 1)
			for {
				old := atomic.LoadInt64(&maxInFlight)
				if cur <= old || atomic.CompareAndSwapInt64(&maxInFlight, old, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond) // hold the slot so requests overlap
			atomic.AddInt64(&inFlight, -1)
			return map[string]any{"trace": map[string]any{"id": id}}, nil
		},
	}
	ids := make([]string, batchFetchConcurrency*3)
	for i := range ids {
		ids[i] = "tr_" + strconv.Itoa(i)
	}
	fetchTraceBatch(api, ids, false)
	if maxInFlight > batchFetchConcurrency {
		t.Fatalf("max concurrent = %d, want <= %d (semaphore not bounding)", maxInFlight, batchFetchConcurrency)
	}
	if maxInFlight < 2 {
		t.Fatalf("max concurrent = %d, want >1 (not running concurrently at all)", maxInFlight)
	}
}

func TestBatchTraceIDsArgsAndStdin(t *testing.T) {
	// Args win when present.
	cmd := &cobra.Command{}
	if ids, _ := batchTraceIDs(cmd, []string{"a", "b"}); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("args mode = %v, want [a b]", ids)
	}
	// No args -> stdin, one id per line, blanks and whitespace trimmed.
	cmd = &cobra.Command{}
	cmd.SetIn(strings.NewReader("tr_1\n  tr_2  \n\n\ttr_3\n"))
	ids, err := batchTraceIDs(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "tr_1,tr_2,tr_3" {
		t.Fatalf("stdin mode = %v, want [tr_1 tr_2 tr_3]", ids)
	}
	// A sole "-" also means stdin.
	cmd = &cobra.Command{}
	cmd.SetIn(strings.NewReader("only\n"))
	if ids, _ := batchTraceIDs(cmd, []string{"-"}); len(ids) != 1 || ids[0] != "only" {
		t.Fatalf(`"-" mode = %v, want [only]`, ids)
	}
}

func TestGetBatchNoIDsIsUsageError(t *testing.T) {
	cmd := NewTracesGetBatchCommand(batchTraceAPI())
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetIn(strings.NewReader("")) // empty stdin, no args
	cmd.SetArgs(nil)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("want a usage error when no ids are given")
	}
	if code := bartolocli.ExitCodeFor(err); code != bartolocli.ExitUsage {
		t.Errorf("exit code for no-ids error = %d, want %d (usage)", code, bartolocli.ExitUsage)
	}
}
