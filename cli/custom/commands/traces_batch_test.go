package commands

import (
	"errors"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// batchTraceAPI is an inline TraceAPI with per-id behaviour: tr_bad fails, every
// other id returns a trace/spans naming itself.
func batchTraceAPI() TraceAPI {
	return TraceAPI{
		GetTrace: func(id string, _ *viper.Viper) (map[string]any, error) {
			if id == "tr_bad" {
				return nil, errors.New("trace not found")
			}
			return map[string]any{"id": id}, nil
		},
		ListSpans: func(id string, _ *viper.Viper) (map[string]any, error) {
			if id == "tr_bad" {
				return nil, errors.New("spans not found")
			}
			return map[string]any{"id": id, "spans": []any{}}, nil
		},
	}
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
	if got[0].Error != "" || got[0].Trace["id"] != "tr_1" {
		t.Errorf("tr_1 = %+v, want the trace and no error", got[0])
	}
	if got[1].Error == "" || got[1].Trace != nil {
		t.Errorf("tr_bad = %+v, want an error and no trace", got[1])
	}
	if got[2].Error != "" || got[2].Trace["id"] != "tr_2" {
		t.Errorf("tr_2 = %+v, want the trace and no error", got[2])
	}
}

func TestFetchTraceBatchSpansUsesListSpans(t *testing.T) {
	got := fetchTraceBatch(batchTraceAPI(), []string{"tr_1"}, true)
	if got[0].Trace != nil {
		t.Errorf("--spans must not set trace: %+v", got[0])
	}
	if got[0].Spans == nil || got[0].Spans["id"] != "tr_1" {
		t.Errorf("--spans must set spans from ListSpans: %+v", got[0])
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
