package custom

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	bartolocli "github.com/orq-ai/bartolo/cli"
)

func TestHumanizeEpochsConvertsOnlyEpochSecondFields(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{"string seconds", map[string]any{"created": "1712345678"}, map[string]any{"created": "2024-04-05T19:34:38Z"}},
		{"float seconds", map[string]any{"created": float64(1712345678)}, map[string]any{"created": "2024-04-05T19:34:38Z"}},
		{"nested", map[string]any{"data": []any{map[string]any{"deprecation": "1798761600"}}}, map[string]any{"data": []any{map[string]any{"deprecation": "2027-01-01T00:00:00Z"}}}},
		{"iso unchanged", map[string]any{"created_at": "2024-04-05T19:34:38Z"}, map[string]any{"created_at": "2024-04-05T19:34:38Z"}},
		{"zero unchanged", map[string]any{"created": "0"}, map[string]any{"created": "0"}},
		{"key not allowlisted", map[string]any{"count": float64(1712345678)}, map[string]any{"count": float64(1712345678)}},
		{"ms scale unchanged", map[string]any{"created_at": float64(1712345678123)}, map[string]any{"created_at": float64(1712345678123)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := humanize(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}

	in := map[string]any{"data": []any{map[string]any{"created": "1712345678"}}}
	humanize(in)
	if got := in["data"].([]any)[0].(map[string]any)["created"]; got != "1712345678" {
		t.Fatalf("input mutated: %v", got)
	}
}

func TestHumanizeContextWindow(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"below 1000", float64(512), "512"},
		{"4096", float64(4096), "4K"},
		{"1500", 1500, "1.5K"},
		{"128000 string", "128000", "128K"},
		{"131072", int64(131072), "131K"},
		{"999999 truncates", json.Number("999999"), "999.9K"},
		{"1000000", "1000000", "1M"},
		{"1048576", float64(1048576), "1M"},
		{"1500000", float64(1500000), "1.5M"},
		{"2000000", float64(2000000), "2M"},
		{"null unchanged", nil, nil},
		{"negative unchanged", float64(-5), float64(-5)},
		{"non-numeric unchanged", "large", "large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := humanize(map[string]any{"metadata": map[string]any{"context_window": tc.in}})
			want := map[string]any{"metadata": map[string]any{"context_window": tc.want}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, want %#v", got, want)
			}
		})
	}
}

func TestHumanFormatterRespectsOutputFormat(t *testing.T) {
	previousFormatter := bartolocli.Formatter
	previousStdout := bartolocli.Stdout
	t.Cleanup(func() {
		bartolocli.Formatter = previousFormatter
		bartolocli.Stdout = previousStdout
	})
	bartolocli.Formatter = humanFormatter{inner: bartolocli.NewDefaultFormatter(false, false)}

	render := func(format string) string {
		var out bytes.Buffer
		bartolocli.Stdout = &out
		restore, err := bartolocli.SetOutputFormat(format)
		if err != nil {
			t.Fatal(err)
		}
		defer restore()
		data := map[string]any{"data": []any{map[string]any{"id": "openai/gpt-4o", "created": "1712345678", "context_window": "128000"}}}
		if err := bartolocli.FormatList(data); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	if got := render("toon"); !strings.Contains(got, "2024-04-05T19:34:38Z") || strings.Contains(got, "1712345678") ||
		!strings.Contains(got, "128K") || strings.Contains(got, "128000") {
		t.Fatalf("toon output: %q", got)
	}
	if got := render("json"); !strings.Contains(got, `"1712345678"`) || strings.Contains(got, "2024-04-05") ||
		!strings.Contains(got, `"128000"`) || strings.Contains(got, "128K") {
		t.Fatalf("json output: %q", got)
	}
}
