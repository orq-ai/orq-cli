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
					{Index: 2, Role: "assistant", Reasoning: []ThreadPart{{Type: "summary", Text: "A short poem is needed."}, {Type: "state", State: "encrypted"}, {Type: "state", State: "redacted"}}, Content: []ThreadPart{}, ToolCalls: []ThreadToolCall{{ID: "call-poem", Name: "compose", Arguments: map[string]any{"form": "haiku"}}}},
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
		{"chat", "chat.json", "# INSTRUCTIONS\n\n## SYSTEM\n\nBe concise.\n\n## DEVELOPER\n\nUse metric units.\n\n## USER [2]\n\nWhat is the weather?\n\nAmsterdam\n\n## ASSISTANT [3]\n\nI will check.\n\n### REASONING\n\nNeed a weather lookup.\n\n### TOOL CALL — weather [call-weather]\n\n```json\n{\n  \"city\": \"Amsterdam\"\n}\n```\n\n## TOOL [4] — weather\n\n### TOOL RESULT\n\n18 C and sunny\n\n## ASSISTANT [5]\n\nIt is 18 C and sunny.\n"},
		{"responses", "responses.json", "# INSTRUCTIONS\n\n## SYSTEM\n\nAnswer in haiku.\n\n## USER [0]\n\nDescribe rain.\n\n## ASSISTANT [2]\n\n### REASONING\n\n[encrypted]\n\n[redacted]\n\n### REASONING SUMMARY\n\nA short poem is needed.\n\n### TOOL CALL — compose [call-poem]\n\n```json\n{\n  \"form\": \"haiku\"\n}\n```\n\n## TOOL [3] — compose\n\n### TOOL RESULT\n\nSoft rain taps the glass\n\n## ASSISTANT [4]\n\nSoft rain taps the glass\n"},
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

func TestNormalizeThreadRegressions(t *testing.T) {
	tests := []struct {
		name  string
		span  map[string]any
		check func(t *testing.T, thread Thread)
	}{
		{
			name: "empty Responses primary falls back to usable Chat fields",
			span: map[string]any{"attributes": map[string]any{
				"openresponses.instructions": "",
				"gen_ai.input":               `[{"role":"user","content":"hello"}]`,
				"gen_ai.output":              `{"role":"assistant","content":"hi"}`,
			}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if thread.Source.Representation != "chat_completions" || len(thread.Messages) != 2 || thread.Messages[1].Content[0].Text != "hi" {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "malformed Responses primary falls back to usable Chat fields",
			span: map[string]any{"attributes": map[string]any{
				"openresponses.input": `{not JSON`,
				"gen_ai.input":        `[{"role":"user","content":"hello"}]`,
			}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if thread.Source.Representation != "chat_completions" || len(thread.Messages) != 1 || thread.Messages[0].Content[0].Text != "hello" {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "usable Responses input combines with usable Chat output",
			span: map[string]any{"attributes": map[string]any{
				"openresponses.input": []any{map[string]any{"type": "message", "role": "user", "content": "from Responses"}},
				"gen_ai.output":       `{"role":"assistant","content":"from Chat"}`,
			}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if thread.Source.Representation != "responses" || len(thread.Messages) != 2 || thread.Messages[0].Content[0].Text != "from Responses" || thread.Messages[1].Content[0].Text != "from Chat" {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "only the first duplicate assistant output at the boundary is removed",
			span: map[string]any{"input": []any{map[string]any{"role": "assistant", "content": "same"}}, "output": map[string]any{"choices": []any{
				map[string]any{"message": map[string]any{"role": "assistant", "content": "same"}},
				map[string]any{"message": map[string]any{"role": "assistant", "content": "same"}},
			}}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 2 || thread.Messages[1].Index != 2 {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "a duplicate non assistant output is preserved",
			span: map[string]any{"input": []any{map[string]any{"role": "user", "content": "same"}}, "output": map[string]any{"role": "user", "content": "same"}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 2 || thread.Messages[1].Role != "user" {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "input reasoning attaches to following function call",
			span: map[string]any{"attributes": map[string]any{"openresponses.input": []any{
				map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "plan"}}},
				map[string]any{"type": "function_call", "call_id": "call-1", "name": "lookup", "arguments": "{}"},
			}}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 1 || len(thread.Messages[0].Reasoning) != 1 || thread.Messages[0].Reasoning[0].Text != "plan" {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "count is retained through value wrappers",
			span: map[string]any{"attributes": map[string]any{"openresponses.input": map[string]any{"_value": map[string]any{"items": map[string]any{"count": 3}}}}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 1 || thread.Messages[0].Content[0] != (ThreadPart{Type: "unavailable", Count: 3}) {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			thread, err := NormalizeThread(tt.span, ThreadSource{})
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, thread)
		})
	}
}

func TestNormalizeThreadNeverLeaksSecretOnlyReasoning(t *testing.T) {
	for _, field := range []string{"encrypted_content", "redacted_content", "signature"} {
		t.Run(field, func(t *testing.T) {
			secret := "do-not-render-" + field
			span := map[string]any{"input": []any{map[string]any{"role": "assistant", "content": "answer", "reasoning": map[string]any{field: secret}}}}
			thread, err := NormalizeThread(span, ThreadSource{})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := RenderThreadMarkdown(&out, thread); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), secret) {
				t.Fatalf("rendered secret reasoning payload: %s", out.String())
			}
			if field != "signature" && (len(thread.Messages[0].Reasoning) != 1 || thread.Messages[0].Reasoning[0].Type != "state") {
				t.Fatalf("reasoning = %#v", thread.Messages[0].Reasoning)
			}
		})
	}
}

func TestRenderThreadMarkdownUsesSummaryAndToolResultIndicators(t *testing.T) {
	thread := Thread{Messages: []ThreadMessage{
		{Index: 0, Role: "assistant", Content: []ThreadPart{}, Reasoning: []ThreadPart{{Type: "summary", Text: "short rationale"}}},
		{Index: 1, Role: "tool", Name: "lookup", Content: []ThreadPart{{Type: "text", Text: "result"}}},
	}}
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, thread); err != nil {
		t.Fatal(err)
	}
	want := "## ASSISTANT [0]\n\n### REASONING SUMMARY\n\nshort rationale\n\n## TOOL [1] — lookup\n\n### TOOL RESULT\n\nresult\n"
	if out.String() != want {
		t.Errorf("Markdown =\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestNormalizeThreadReReviewRegressions(t *testing.T) {
	tests := []struct {
		name  string
		span  map[string]any
		check func(*testing.T, Thread)
	}{
		{
			name: "instructions only is a Responses thread",
			span: map[string]any{"attributes": map[string]any{"openresponses.instructions": "Only these instructions."}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if thread.Source.Representation != "responses" || len(thread.Instructions) != 1 || len(thread.Messages) != 0 {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "trailing Responses input reasoning is retained",
			span: map[string]any{"attributes": map[string]any{"openresponses.input": []any{map[string]any{"type": "reasoning", "content": "keep me"}}}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 1 || thread.Messages[0].Role != "assistant" || thread.Messages[0].Reasoning[0].Text != "keep me" {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "Responses input reasoning survives a Chat output hybrid",
			span: map[string]any{"attributes": map[string]any{
				"openresponses.input": []any{map[string]any{"type": "reasoning", "content": "keep hybrid"}},
				"gen_ai.output":       `{"role":"assistant","content":"chat answer"}`,
			}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 1 || thread.Messages[0].Content[0].Text != "chat answer" || thread.Messages[0].Reasoning[0].Text != "keep hybrid" {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "nameless Chat tool result inherits the called tool name",
			span: map[string]any{"input": []any{
				map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-lookup", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}},
				map[string]any{"role": "tool", "tool_call_id": "call-lookup", "content": "found"},
			}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 2 || thread.Messages[1].Name != "lookup" {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "Responses errors and exceptions keep valid message roles",
			span: map[string]any{"attributes": map[string]any{"openresponses.output": []any{
				map[string]any{"type": "error", "error": map[string]any{"message": "rate limited"}},
				map[string]any{"type": "exception", "content": "upstream unavailable"},
			}}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 2 || thread.Messages[0].Role != "assistant" || thread.Messages[0].Content[0] != (ThreadPart{Type: "error", Text: "rate limited"}) || thread.Messages[1].Content[0] != (ThreadPart{Type: "exception", Text: "upstream unavailable"}) {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			thread, err := NormalizeThread(tt.span, ThreadSource{})
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, thread)
		})
	}
}

func TestNormalizeThreadSanitizesNestedSecretReasoning(t *testing.T) {
	for _, field := range []string{"signature", "encrypted_content", "redacted_content"} {
		t.Run(field, func(t *testing.T) {
			secret := "nested-secret-" + field
			span := map[string]any{"input": []any{map[string]any{"role": "assistant", "reasoning": []any{map[string]any{"content": map[string]any{field: secret}}}}}}
			thread, err := NormalizeThread(span, ThreadSource{})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(thread)
			if err != nil {
				t.Fatal(err)
			}
			var markdown bytes.Buffer
			if err := RenderThreadMarkdown(&markdown, thread); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) || strings.Contains(markdown.String(), secret) {
				t.Fatalf("secret leaked: canonical=%s markdown=%s", encoded, markdown.String())
			}
		})
	}
}

func TestRenderThreadMarkdownReReviewIndicators(t *testing.T) {
	thread := Thread{Instructions: []ThreadInstruction{{Role: "system", Content: nil}}, Messages: []ThreadMessage{
		{Index: 0, Role: "assistant", Reasoning: []ThreadPart{{Type: "summary", Text: "chat summary"}}},
		{Index: 1, Role: "assistant", Content: []ThreadPart{{Type: "error", Text: "rate limited"}, {Type: "exception", Text: "upstream unavailable"}}},
	}}
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, thread); err != nil {
		t.Fatal(err)
	}
	want := "## ASSISTANT [0]\n\n### REASONING SUMMARY\n\nchat summary\n\n## ASSISTANT [1]\n\n### ERROR\n\nrate limited\n\n### EXCEPTION\n\nupstream unavailable\n"
	if out.String() != want {
		t.Errorf("Markdown =\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestNormalizeThreadRendersChatReasoningSummary(t *testing.T) {
	thread, err := NormalizeThread(map[string]any{"input": []any{map[string]any{"role": "assistant", "summary": "a short summary"}}}, ThreadSource{})
	if err != nil {
		t.Fatal(err)
	}
	if got := thread.Messages[0].Reasoning; !reflect.DeepEqual(got, []ThreadPart{{Type: "summary", Text: "a short summary"}}) {
		t.Fatalf("reasoning = %#v", got)
	}
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, thread); err != nil {
		t.Fatal(err)
	}
	want := "## ASSISTANT [0]\n\n### REASONING SUMMARY\n\na short summary\n"
	if out.String() != want {
		t.Errorf("Markdown = %q, want %q", out.String(), want)
	}
}

func TestNormalizeThreadThirdReviewHybridRegressions(t *testing.T) {
	tests := []struct {
		name  string
		span  map[string]any
		check func(*testing.T, Thread)
	}{
		{
			name: "Responses reasoning attaches to following Chat assistant tool call",
			span: map[string]any{"attributes": map[string]any{
				"openresponses.input": []any{map[string]any{"type": "reasoning", "content": "plan before call"}},
				"gen_ai.output":       `{"role":"assistant","tool_calls":[{"id":"chat-call","function":{"name":"lookup","arguments":"{}"}}]}`,
			}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 1 || len(thread.Messages[0].Reasoning) != 1 || thread.Messages[0].Reasoning[0].Text != "plan before call" || len(thread.Messages[0].ToolCalls) != 1 {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "Chat input call names a Responses output result",
			span: map[string]any{"attributes": map[string]any{"openresponses.output": []any{map[string]any{"type": "function_call_output", "call_id": "cross-call", "output": "result"}}}, "input": []any{
				map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "cross-call", "function": map[string]any{"name": "cross lookup", "arguments": "{}"}}}},
			}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 2 || thread.Messages[1].Role != "tool" || thread.Messages[1].Name != "cross lookup" {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
		{
			name: "Responses input call names a Chat output result",
			span: map[string]any{"attributes": map[string]any{
				"openresponses.input": []any{map[string]any{"type": "function_call", "call_id": "reverse-call", "name": "reverse lookup", "arguments": "{}"}},
				"gen_ai.output":       `{"role":"tool","tool_call_id":"reverse-call","content":"result"}`,
			}},
			check: func(t *testing.T, thread Thread) {
				t.Helper()
				if len(thread.Messages) != 2 || thread.Messages[1].Role != "tool" || thread.Messages[1].Name != "reverse lookup" {
					t.Fatalf("thread = %#v", thread)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			thread, err := NormalizeThread(tt.span, ThreadSource{})
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, thread)
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
