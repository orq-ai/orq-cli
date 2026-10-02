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

// Human output shows epoch-second fields as UTC RFC 3339 and context_window
// as a compact token count (128K, 1M); -o json/yaml keep the API's value.

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
			if k == "context_window" {
				if n, ok := wholeNumber(val); ok && n >= 0 {
					out[k] = compactCount(n)
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

func isEpochKey(k string) bool {
	switch k {
	case "created", "updated", "deprecation":
		return true
	}
	return strings.HasSuffix(k, "_at")
}

// compactCount renders n as 512, 1.5K, 128K, 1M: one decimal, truncated.
func compactCount(n int64) string {
	unit, suffix := int64(1), ""
	switch {
	case n >= 1_000_000:
		unit, suffix = 1_000_000, "M"
	case n >= 1000:
		unit, suffix = 1000, "K"
	default:
		return strconv.FormatInt(n, 10)
	}
	tenths := n / (unit / 10)
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
