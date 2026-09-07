package commands

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderThreadMarkdown(t *testing.T) {
	span := loadThreadFixture(t, "chat.json")
	thread, err := NormalizeThread(span, ThreadSource{TraceID: spanString(span, "trace_id"), SpanID: spanString(span, "span_id")})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, thread, 0); err != nil {
		t.Fatal(err)
	}
	want := "> trace `trace-chat` · span `span-chat` · chat_completions\n\n" +
		"## SYSTEM [0]\n\nReply with one short synthetic acknowledgement.\n\n" +
		"## USER [1]\n\nSynthetic fixture request: alpha.\n\n" +
		"## ASSISTANT [2]\n\n### TOOL CALL — synthetic_weather [call-synthetic-weather]\n\n```json\n{\n  \"city\": \"Exampleville\"\n}\n```\n\n" +
		"## TOOL [3] — synthetic_weather\n\n### TOOL RESULT\n\nSynthetic result: clear and 20 C.\n\n" +
		"## ASSISTANT [4]\n\nAcknowledged! The synthetic weather for Exampleville is clear with a temperature of 20°C.\n"
	if got := out.String(); got != want {
		t.Errorf("Markdown =\n%s\nwant:\n%s", got, want)
	}
}

// A cut value keeps its closing fence, so a truncated tool call cannot turn
// every message after it into one code block.
func TestRenderThreadMarkdownCutsInsideTheFence(t *testing.T) {
	thread := Thread{Messages: []ThreadMessage{
		{Index: 0, Role: "assistant", ToolCalls: []ThreadToolCall{{Name: "lookup", Arguments: map[string]any{"query": strings.Repeat("z", 100)}}}},
		{Index: 1, Role: "user", Content: []ThreadPart{{Type: "text", Text: "after"}}},
	}}
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, thread, 20); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "[truncated: ") || strings.Count(rendered, "```") != 2 {
		t.Fatalf("rendered = %s", rendered)
	}
	if !strings.HasSuffix(rendered, "## USER [1]\n\nafter\n") {
		t.Fatalf("rendered = %s", rendered)
	}
}
