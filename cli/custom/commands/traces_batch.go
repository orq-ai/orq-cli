package commands

import (
	"bufio"
	"errors"
	"strings"
	"sync"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// batchFetchConcurrency bounds in-flight requests so a large batch does not open
// one connection per id at once.
const batchFetchConcurrency = 8

// NewTracesGetBatchCommand builds `orq traces get-batch`: fetch many traces (or
// their spans) in one process, instead of spawning `orq traces get` once per id.
func NewTracesGetBatchCommand(api TraceAPI) *cobra.Command {
	var spans bool
	cmd := &cobra.Command{
		Use:   "get-batch [trace-id ...]",
		Short: "Fetch multiple traces (or their spans) in one invocation",
		Long: strings.Join([]string{
			"Fetch many traces at once, in a single process, instead of running `orq traces get` once per id — one process and one connection pool for the whole set.",
			"",
			"Ids come from the arguments, or from stdin (one id per line, blanks ignored) when no argument is given or the only argument is `-`, so piping from `orq traces search` works. With --spans each id's spans are listed instead of the trace.",
			"",
			"The output is an array in input order, one entry per id: {id, trace} (or {id, spans}), or {id, error} when that id could not be read. A failing id does not abort the batch — inspect each entry's error field. Each id is fetched by id with default parameters, like `orq traces get <id>`.",
		}, "\n"),
		Example: strings.Join([]string{
			"  orq traces get-batch tr_1 tr_2 tr_3",
			"  orq traces search --from-file body.json -o json | jq -r '.data[].trace_id' | orq traces get-batch",
			"  orq traces get-batch --spans tr_1 tr_2",
		}, "\n"),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := batchTraceIDs(cmd, args)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				return bartolocli.NewUsageError(errors.New("no trace ids: pass them as arguments or on stdin, one per line"))
			}
			return emit(fetchTraceBatch(api, ids, spans))
		},
	}
	cmd.Flags().BoolVar(&spans, "spans", false, "List each trace's spans instead of fetching the trace")
	return cmd
}

// batchResult is one id's outcome. Exactly one of Trace, Spans, or Error is set.
type batchResult struct {
	ID    string         `json:"id"`
	Trace map[string]any `json:"trace,omitempty"`
	Spans map[string]any `json:"spans,omitempty"`
	Error string         `json:"error,omitempty"`
}

// batchTraceIDs reads ids from the arguments, or from stdin (one per line) when
// no argument is given or the only argument is "-".
func batchTraceIDs(cmd *cobra.Command, args []string) ([]string, error) {
	if len(args) > 0 && !(len(args) == 1 && args[0] == "-") {
		return args, nil
	}
	var ids []string
	sc := bufio.NewScanner(cmd.InOrStdin())
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if id := strings.TrimSpace(sc.Text()); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, sc.Err()
}

// fetchTraceBatch fetches every id concurrently (bounded) and returns the results
// in input order. A per-id failure becomes that entry's Error, never aborting the
// rest. Each goroutine gets its own viper: get-by-id needs no per-request
// parameters, and a shared one would be read concurrently.
func fetchTraceBatch(api TraceAPI, ids []string, spans bool) []batchResult {
	results := make([]batchResult, len(ids))
	sem := make(chan struct{}, batchFetchConcurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			res := batchResult{ID: id}
			if spans {
				if data, err := api.ListSpans(id, viper.New()); err != nil {
					res.Error = err.Error()
				} else {
					res.Spans = data
				}
			} else {
				if data, err := api.GetTrace(id, viper.New()); err != nil {
					res.Error = err.Error()
				} else {
					res.Trace = data
				}
			}
			results[i] = res
		}(i, id)
	}
	wg.Wait()
	return results
}
