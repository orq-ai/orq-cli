package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeThread(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    Thread
	}{
		{
			name:    "chat completions from dotted JSON attributes",
			fixture: "chat.json",
			want: Thread{
				Source: ThreadSource{Representation: "chat_completions", TraceID: "trace-chat", SpanID: "span-chat"},
				Instructions: []ThreadInstruction{
					{Role: "system", Content: []ThreadPart{{Type: "text", Text: "Be concise."}}},
					{Role: "developer", Content: []ThreadPart{{Type: "text", Text: "Use metric units."}}},
				},
				Messages: []ThreadMessage{
					{Index: 2, Role: "user", Content: []ThreadPart{{Type: "text", Text: "What is the weather?"}, {Type: "text", Text: "Amsterdam"}}},
					{Index: 3, Role: "assistant", Content: []ThreadPart{{Type: "text", Text: "I will check."}}, Reasoning: []ThreadPart{{Type: "text", Text: "Need a weather lookup."}}, ToolCalls: []ThreadToolCall{{ID: "call-weather", Name: "weather", Arguments: map[string]any{"city": "Amsterdam"}}}},
					{Index: 4, Role: "tool", Name: "weather", ToolCallID: "call-weather", Content: []ThreadPart{{Type: "text", Text: "18 C and sunny"}}},
					{Index: 5, Role: "assistant", Content: []ThreadPart{{Type: "text", Text: "It is 18 C and sunny."}}},
				},
			},
		},
		{
			name:    "responses items attach reasoning and decode wrappers",
			fixture: "responses.json",
			want: Thread{
				Source:       ThreadSource{Representation: "responses", TraceID: "trace-responses", SpanID: "span-responses"},
				Instructions: []ThreadInstruction{{Role: "system", Content: []ThreadPart{{Type: "text", Text: "Answer in haiku."}}}},
				Messages: []ThreadMessage{
					{Index: 0, Role: "user", Content: []ThreadPart{{Type: "text", Text: "Describe rain."}}},
					{Index: 2, Role: "assistant", Reasoning: []ThreadPart{{Type: "text", Text: "A short poem is needed."}, {Type: "state", State: "encrypted"}, {Type: "state", State: "redacted"}}, Content: []ThreadPart{}, ToolCalls: []ThreadToolCall{{ID: "call-poem", Name: "compose", Arguments: map[string]any{"form": "haiku"}}}},
					{Index: 3, Role: "tool", Name: "compose", ToolCallID: "call-poem", Content: []ThreadPart{{Type: "text", Text: "Soft rain taps the glass"}}},
					{Index: 4, Role: "assistant", Content: []ThreadPart{{Type: "text", Text: "Soft rain taps the glass"}}},
				},
			},
		},
		{
			name:    "responses count only content is explicitly unavailable",
			fixture: "responses-unavailable.json",
			want: Thread{
				Source:   ThreadSource{Representation: "responses", TraceID: "trace-unavailable", SpanID: "span-unavailable"},
				Messages: []ThreadMessage{{Index: 0, Role: "user", Content: []ThreadPart{{Type: "unavailable", Count: 2}}}},
			},
		},
		{
			name:    "legacy fallback retains malformed arguments and unsupported content type without payload",
			fixture: "malformed-fallback.json",
			want: Thread{
				Source: ThreadSource{Representation: "chat_completions", TraceID: "trace-fallback", SpanID: "span-fallback"},
				Messages: []ThreadMessage{
					{Index: 0, Role: "user", Content: []ThreadPart{{Type: "unsupported", UnsupportedType: "image_url"}}},
					{Index: 1, Role: "assistant", Content: []ThreadPart{{Type: "text", Text: "I cannot view that image."}}, ToolCalls: []ThreadToolCall{{ID: "call-bad", Name: "inspect", Arguments: "{not json"}}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := loadThreadFixture(t, tt.fixture)
			got, err := NormalizeThread(span, ThreadSource{TraceID: spanString(span, "trace_id"), SpanID: spanString(span, "span_id")})
			if err != nil {
				t.Fatalf("NormalizeThread() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NormalizeThread() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSliceThread(t *testing.T) {
	thread := Thread{
		Source:       ThreadSource{Representation: "chat_completions"},
		Instructions: []ThreadInstruction{{Role: "system", Content: []ThreadPart{{Type: "text", Text: "keep"}}}},
		Messages:     []ThreadMessage{{Index: 0}, {Index: 1}, {Index: 2}, {Index: 3}, {Index: 4}, {Index: 5}},
	}
	tests := []struct {
		expression string
		indices    []int
		wantErr    string
	}{
		{"-5:", []int{1, 2, 3, 4, 5}, ""}, {":10", []int{0, 1, 2, 3, 4, 5}, ""},
		{"2:4", []int{2, 3}, ""}, {"-4:-1", []int{2, 3, 4}, ""}, {"3", []int{3}, ""},
		{"-20:20", []int{0, 1, 2, 3, 4, 5}, ""}, {"20:", []int{}, ""}, {"4:2", []int{}, ""},
		{" 2 : 4 ", []int{2, 3}, ""}, {"nope", nil, "invalid slice"}, {"1:two", nil, "invalid slice"},
		{"1:2:3", nil, "stride"}, {"::2", nil, "stride"},
	}
	for _, tt := range tests {
		t.Run(tt.expression, func(t *testing.T) {
			got, err := SliceThread(thread, tt.expression)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("SliceThread() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SliceThread() error = %v", err)
			}
			indices := []int{}
			for _, message := range got.Messages {
				indices = append(indices, message.Index)
			}
			if !reflect.DeepEqual(indices, tt.indices) {
				t.Errorf("indices = %v, want %v", indices, tt.indices)
			}
			if !reflect.DeepEqual(got.Instructions, thread.Instructions) || got.Source != thread.Source {
				t.Error("SliceThread() changed instructions or source")
			}
		})
	}
}

func TestRenderThreadMarkdown(t *testing.T) {
	tests := []struct{ name, fixture, want string }{
		{"chat", "chat.json", "# INSTRUCTIONS\n\n## SYSTEM\n\nBe concise.\n\n## DEVELOPER\n\nUse metric units.\n\n## USER [2]\n\nWhat is the weather?\n\nAmsterdam\n\n## ASSISTANT [3]\n\nI will check.\n\n### REASONING\n\nNeed a weather lookup.\n\n### TOOL CALL — weather [call-weather]\n\n```json\n{\n  \"city\": \"Amsterdam\"\n}\n```\n\n## TOOL [4] — weather\n\n18 C and sunny\n\n## ASSISTANT [5]\n\nIt is 18 C and sunny.\n"},
		{"responses", "responses.json", "# INSTRUCTIONS\n\n## SYSTEM\n\nAnswer in haiku.\n\n## USER [0]\n\nDescribe rain.\n\n## ASSISTANT [2]\n\n### REASONING\n\nA short poem is needed.\n\n[encrypted]\n\n[redacted]\n\n### TOOL CALL — compose [call-poem]\n\n```json\n{\n  \"form\": \"haiku\"\n}\n```\n\n## TOOL [3] — compose\n\nSoft rain taps the glass\n\n## ASSISTANT [4]\n\nSoft rain taps the glass\n"},
		{"responses", "responses-unavailable.json", "## USER [0]\n\n[content unavailable: 2 items]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := loadThreadFixture(t, tt.fixture)
			thread, err := NormalizeThread(span, ThreadSource{TraceID: spanString(span, "trace_id"), SpanID: spanString(span, "span_id")})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := RenderThreadMarkdown(&out, thread); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != tt.want {
				t.Errorf("Markdown =\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func loadThreadFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "thread", name))
	if err != nil {
		t.Fatal(err)
	}
	var span map[string]any
	if err := json.Unmarshal(data, &span); err != nil {
		t.Fatal(err)
	}
	return span
}

func spanString(span map[string]any, key string) string { value, _ := span[key].(string); return value }
