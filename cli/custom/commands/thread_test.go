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
			name:    "chat completions real shaped dotted JSON attributes",
			fixture: "chat.json",
			want: Thread{
				Source: ThreadSource{Representation: "chat_completions", TraceID: "trace-chat", SpanID: "span-chat"},
				Messages: []ThreadMessage{
					{Index: 0, Role: "system", Content: []ThreadPart{{Type: "text", Text: "Reply with one short synthetic acknowledgement."}}},
					{Index: 1, Role: "user", Content: []ThreadPart{{Type: "text", Text: "Synthetic fixture request: alpha."}}},
					{Index: 2, Role: "assistant", ToolCalls: []ThreadToolCall{{ID: "call-synthetic-weather", Name: "synthetic_weather", Arguments: map[string]any{"city": "Exampleville"}}}},
					{Index: 3, Role: "tool", Name: "synthetic_weather", ToolCallID: "call-synthetic-weather", Content: []ThreadPart{{Type: "text", Text: "Synthetic result: clear and 20 C."}}},
					{Index: 4, Role: "assistant", Content: []ThreadPart{{Type: "text", Text: "Acknowledged! The synthetic weather for Exampleville is clear with a temperature of 20°C."}}},
				},
			},
		},
		{
			name:    "responses real value wrappers preserve direct content",
			fixture: "responses.json",
			want: Thread{
				Source: ThreadSource{Representation: "responses", TraceID: "trace-responses", SpanID: "span-responses"},
				Messages: []ThreadMessage{
					{Index: 0, Role: "system", Content: []ThreadPart{{Type: "text", Text: "Reply with exactly: synthetic Responses acknowledgement."}}},
					{Index: 1, Role: "user", Content: []ThreadPart{{Type: "text", Text: "Synthetic Responses fixture request: beta."}}},
					{Index: 2, Role: "assistant", Content: []ThreadPart{{Type: "text", Text: "Synthetic Responses acknowledgement."}}},
				},
			},
		},
		{
			name:    "responses count only output collection is explicitly unavailable",
			fixture: "responses-unavailable.json",
			want: Thread{
				Source: ThreadSource{Representation: "responses", TraceID: "trace-unavailable", SpanID: "span-unavailable"},
				Messages: []ThreadMessage{
					{Index: 0, Role: "user", Content: []ThreadPart{{Type: "text", Text: "Synthetic Responses request with unavailable output."}}},
					{Index: 1, Role: "assistant", Content: []ThreadPart{{Type: "unavailable", Count: 2}}},
				},
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
		Source:   ThreadSource{Representation: "chat_completions"},
		Messages: []ThreadMessage{{Index: 0}, {Index: 1}, {Index: 2}, {Index: 3}, {Index: 4}, {Index: 5}},
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
			if got.Source != thread.Source {
				t.Error("SliceThread() changed source")
			}
		})
	}
}

func TestRenderThreadMarkdown(t *testing.T) {
	tests := []struct{ name, fixture, want string }{
		{"chat", "chat.json", "## SYSTEM [0]\n\nReply with one short synthetic acknowledgement.\n\n## USER [1]\n\nSynthetic fixture request: alpha.\n\n## ASSISTANT [2]\n\n### TOOL CALL — synthetic_weather [call-synthetic-weather]\n\n```json\n{\n  \"city\": \"Exampleville\"\n}\n```\n\n## TOOL [3] — synthetic_weather\n\n### TOOL RESULT\n\nSynthetic result: clear and 20 C.\n\n## ASSISTANT [4]\n\nAcknowledged! The synthetic weather for Exampleville is clear with a temperature of 20°C.\n"},
		{"responses", "responses.json", "## SYSTEM [0]\n\nReply with exactly: synthetic Responses acknowledgement.\n\n## USER [1]\n\nSynthetic Responses fixture request: beta.\n\n## ASSISTANT [2]\n\nSynthetic Responses acknowledgement.\n"},
		{"responses unavailable output", "responses-unavailable.json", "## USER [0]\n\nSynthetic Responses request with unavailable output.\n\n## ASSISTANT [1]\n\n[content unavailable: 2 items]\n"},
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

func TestResponsesFixturesPreserveAvailableContentWithoutInventingUnavailableData(t *testing.T) {
	t.Run("real wrapped content preserves only known response data", func(t *testing.T) {
		span := loadThreadFixture(t, "responses.json")
		attributes := span["attributes"].(map[string]any)
		for _, key := range []string{"openresponses.input", "openresponses.output"} {
			wrapped := attributes[key].(map[string]any)
			if _, ok := wrapped["_value"].(string); !ok {
				t.Fatalf("%s _value = %#v, want JSON string from hydrated span", key, wrapped["_value"])
			}
		}
		thread, err := NormalizeThread(span, ThreadSource{TraceID: spanString(span, "trace_id"), SpanID: spanString(span, "span_id")})
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
		for _, output := range []string{string(encoded), markdown.String()} {
			for _, known := range []string{"Reply with exactly: synthetic Responses acknowledgement.", "Synthetic Responses fixture request: beta.", "Synthetic Responses acknowledgement."} {
				if !strings.Contains(output, known) {
					t.Fatalf("output omitted known available content %q: %s", known, output)
				}
			}
			for _, invented := range []string{"Synthetic masked Responses fixture request: gamma.", "reasoning", "tool", "arguments", "call"} {
				if strings.Contains(strings.ToLower(output), strings.ToLower(invented)) {
					t.Fatalf("output invented unavailable data %q: %s", invented, output)
				}
			}
		}
	})

	t.Run("count-only output represents raw response items without inventing messages", func(t *testing.T) {
		span := loadThreadFixture(t, "responses-unavailable.json")
		attributes := span["attributes"].(map[string]any)
		input := attributes["openresponses.input"].(map[string]any)
		if got := input["items"].(map[string]any)["count"]; got != float64(1) {
			t.Fatalf("input items.count = %#v, want 1", got)
		}
		output := attributes["openresponses.output"].(map[string]any)
		if _, ok := output["_value"]; ok {
			t.Fatalf("output unexpectedly exposes _value: %#v", output)
		}
		if got := output["items"].(map[string]any)["count"]; got != float64(2) {
			t.Fatalf("output items.count = %#v, want 2 raw Responses output items", got)
		}
		thread, err := NormalizeThread(span, ThreadSource{TraceID: spanString(span, "trace_id"), SpanID: spanString(span, "span_id")})
		if err != nil {
			t.Fatal(err)
		}
		want := []ThreadMessage{
			{Index: 0, Role: "user", Content: []ThreadPart{{Type: "text", Text: "Synthetic Responses request with unavailable output."}}},
			{Index: 1, Role: "assistant", Content: []ThreadPart{{Type: "unavailable", Count: 2}}},
		}
		if !reflect.DeepEqual(thread.Messages, want) {
			t.Fatalf("messages = %#v, want %#v", thread.Messages, want)
		}
		if len(thread.Messages) != 2 {
			t.Fatalf("message count = %d, want 2; output items.count must not be treated as a message count", len(thread.Messages))
		}
		var markdown bytes.Buffer
		if err := RenderThreadMarkdown(&markdown, thread); err != nil {
			t.Fatal(err)
		}
		if got, want := markdown.String(), "## USER [0]\n\nSynthetic Responses request with unavailable output.\n\n## ASSISTANT [1]\n\n[content unavailable: 2 items]\n"; got != want {
			t.Fatalf("Markdown = %q, want %q", got, want)
		}
		encoded, err := json.Marshal(thread)
		if err != nil {
			t.Fatal(err)
		}
		for _, invented := range []string{"reasoning", "tool", "arguments", "call", "Synthetic unavailable response text"} {
			if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(invented)) || strings.Contains(strings.ToLower(markdown.String()), strings.ToLower(invented)) {
				t.Fatalf("count-only output invented unavailable data %q", invented)
			}
		}
	})
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
				if len(thread.Messages) != 2 || thread.Messages[1].Index != 1 {
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

func TestNormalizeThreadNeverLeaksProtectedReasoningWrappers(t *testing.T) {
	tests := []struct {
		name  string
		value map[string]any
		state string
	}{
		{name: "encrypted type", value: map[string]any{"type": "encrypted", "text": "secret-encrypted-type"}, state: "encrypted"},
		{name: "encrypted value wrapper", value: map[string]any{"_value": "secret-encrypted-value", "encrypted": true}, state: "encrypted"},
		{name: "redacted string wrapper", value: map[string]any{"string": "secret-redacted-string", "redacted": true}, state: "redacted"},
		{name: "signature sibling", value: map[string]any{"text": "secret-signed-text", "signature": "secret-signature"}, state: "redacted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := map[string]any{"input": []any{map[string]any{"role": "assistant", "content": "answer", "reasoning": tt.value}}}
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
			for _, secret := range []string{"secret-encrypted-type", "secret-encrypted-value", "secret-redacted-string", "secret-signed-text", "secret-signature"} {
				if strings.Contains(string(encoded), secret) || strings.Contains(markdown.String(), secret) {
					t.Fatalf("protected reasoning leaked %q: canonical=%s markdown=%s", secret, encoded, markdown.String())
				}
			}
			if got := thread.Messages[0].Reasoning; !reflect.DeepEqual(got, []ThreadPart{{Type: "state", State: tt.state}}) {
				t.Fatalf("reasoning = %#v, want state %q", got, tt.state)
			}
		})
	}
}

func TestNormalizeThreadResponsesBoundaryAndCountRegressions(t *testing.T) {
	t.Run("deduplicates identical assistant at input output boundary", func(t *testing.T) {
		span := map[string]any{"attributes": map[string]any{
			"openresponses.input":  []any{map[string]any{"type": "message", "role": "assistant", "content": "same"}},
			"openresponses.output": []any{map[string]any{"type": "message", "role": "assistant", "content": "same"}},
		}}
		thread, err := NormalizeThread(span, ThreadSource{})
		if err != nil {
			t.Fatal(err)
		}
		if len(thread.Messages) != 1 || thread.Messages[0].Index != 0 {
			t.Fatalf("messages = %#v", thread.Messages)
		}
	})

	for _, test := range []struct {
		name  string
		input any
		count int
	}{
		{name: "direct JSON count", input: `{"items":{"count":3}}`, count: 3},
		{name: "string wrapped JSON count", input: map[string]any{"string": `{"items":{"count":2}}`}, count: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			span := map[string]any{"attributes": map[string]any{
				"openresponses.input":  test.input,
				"openresponses.output": []any{map[string]any{"type": "message", "role": "assistant", "content": "known output"}},
			}}
			thread, err := NormalizeThread(span, ThreadSource{})
			if err != nil {
				t.Fatal(err)
			}
			if len(thread.Messages) != 2 || thread.Messages[0].Content[0] != (ThreadPart{Type: "unavailable", Count: test.count}) {
				t.Fatalf("messages = %#v", thread.Messages)
			}
			if got := thread.Messages[1].Index; got != 1 {
				t.Fatalf("output index = %d, want dense position 1", got)
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

func TestRenderThreadMarkdownDoesNotInventUnnamedTool(t *testing.T) {
	thread := Thread{Messages: []ThreadMessage{{Index: 0, Role: "assistant", ToolCalls: []ThreadToolCall{{ID: "call-1", Arguments: map[string]any{"ok": true}}}}}}
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, thread); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "unknown") || !strings.Contains(out.String(), "### TOOL CALL [call-1]") {
		t.Fatalf("Markdown = %q", out.String())
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
				if thread.Source.Representation != "responses" || len(thread.Messages) != 1 || thread.Messages[0].Role != "system" {
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
	thread := Thread{Messages: []ThreadMessage{
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
