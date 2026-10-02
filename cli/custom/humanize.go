package custom

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

// Human output shows epoch-second fields as UTC RFC 3339, token sizes and
// limits as compact counts (128K, 1M) and usage token counts with one decimal
// (31.4K, 1.3M); -o json/yaml keep the API's value.

const (
	minEpochSeconds = 1_000_000_000
	maxEpochSeconds = 9_999_999_999
)

func humanize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if isEpochKey(k) {
				if n, ok := wholeNumber(val); ok && n >= minEpochSeconds && n <= maxEpochSeconds {
					out[k] = time.Unix(n, 0).UTC().Format(time.RFC3339)
					continue
				}
			}
			if sizeKeys[k] {
				if n, ok := wholeNumber(val); ok && n >= 1000 {
					out[k] = compactCount(n)
					continue
				}
			}
			if usageKeys[k] {
				if n, ok := wholeNumber(val); ok && n >= 1000 {
					out[k] = compactUsage(n)
					continue
				}
			}
			out[k] = humanize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, elem := range t {
			out[i] = humanize(elem)
		}
		return out
	default:
		return v
	}
}

var sizeKeys = map[string]bool{
	"context_window":             true,
	"max_input_tokens":           true,
	"max_output_tokens":          true,
	"extended_context_threshold": true,
	"token_limit":                true,
}

var usageKeys = map[string]bool{
	"prompt_tokens":                         true,
	"completion_tokens":                     true,
	"total_tokens":                          true,
	"input_tokens":                          true,
	"output_tokens":                         true,
	"reasoning_tokens":                      true,
	"cached_tokens":                         true,
	"audio_tokens":                          true,
	"text_tokens":                           true,
	"image_tokens":                          true,
	"cache_creation_tokens":                 true,
	"cache_write_tokens":                    true,
	"accepted_prediction_tokens":            true,
	"rejected_prediction_tokens":            true,
	"prompt_cached_tokens":                  true,
	"prompt_audio_tokens":                   true,
	"completion_reasoning_tokens":           true,
	"completion_audio_tokens":               true,
	"completion_accepted_prediction_tokens": true,
	"completion_rejected_prediction_tokens": true,
}

func isEpochKey(k string) bool {
	switch k {
	case "created", "updated", "deprecation":
		return true
	}
	return strings.HasSuffix(k, "_at")
}

// compactCount renders n as 512, 128K, 1.5M: whole thousands rounded half up
// below a million, one truncated decimal above.
func compactCount(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	if n < 999_500 {
		return strconv.FormatInt((n+500)/1000, 10) + "K"
	}
	if n < 1_000_000 {
		// Rounds up to a thousand K, which reads as 1M.
		return "1M"
	}
	tenths := n / 100_000
	s := strconv.FormatInt(tenths/10, 10)
	if d := tenths % 10; d != 0 {
		s += "." + strconv.FormatInt(d, 10)
	}
	return s + "M"
}

// compactUsage renders n as 512, 31.4K, 1.3M: one decimal rounded half up,
// trailing .0 trimmed.
func compactUsage(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	unit, suffix := int64(100), "K"
	if n >= 999_950 {
		unit, suffix = 100_000, "M"
	}
	tenths := (n + unit/2) / unit
	s := strconv.FormatInt(tenths/10, 10)
	if d := tenths % 10; d != 0 {
		s += "." + strconv.FormatInt(d, 10)
	}
	return s + suffix
}

func wholeNumber(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		if t != math.Trunc(t) || t < math.MinInt64 || t > math.MaxInt64 {
			return 0, false
		}
		return int64(t), true
	case int:
		return int64(t), true
	case int64:
		return t, true
	case json.Number:
		p, err := t.Int64()
		return p, err == nil
	case string:
		if t == "" {
			return 0, false
		}
		for i := range len(t) {
			if t[i] < '0' || t[i] > '9' {
				return 0, false
			}
		}
		p, err := strconv.ParseInt(t, 10, 64)
		return p, err == nil
	default:
		return 0, false
	}
}

type humanFormatter struct{ inner bartolocli.ResponseFormatter }

func (f humanFormatter) Format(data any) error {
	return f.inner.Format(f.prepare(data))
}

func (f humanFormatter) FormatList(data any, columns ...string) error {
	if lf, ok := f.inner.(interface {
		FormatList(any, ...string) error
	}); ok {
		return lf.FormatList(f.prepare(data), columns...)
	}
	return f.inner.Format(f.prepare(data))
}

func (humanFormatter) prepare(data any) any {
	switch bartolocli.OutputFormat() {
	case "toon", "table":
		return humanize(data)
	}
	return data
}

func installHumanFormatter() {
	chainPreRun(func(cmd *cobra.Command, args []string) error {
		if bartolocli.Formatter == nil {
			return nil
		}
		if _, ok := bartolocli.Formatter.(humanFormatter); !ok {
			bartolocli.Formatter = humanFormatter{inner: bartolocli.Formatter}
		}
		return nil
	})
}
