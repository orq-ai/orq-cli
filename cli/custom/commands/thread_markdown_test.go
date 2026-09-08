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
		"## TOOL [3] — synthetic_weather [call-synthetic-weather]\n\n### TOOL RESULT\n\nSynthetic result: clear and 20 C.\n\n" +
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

// The two renderers carry the same part-type switch. This drives the Markdown
// half of it over every arm at once, so a part type added to one renderer and
// not the other shows up as a failure rather than as content that quietly
// vanishes from one view.
func TestRenderThreadMarkdownRendersEveryPartType(t *testing.T) {
	thread := Thread{
		Source: ThreadSource{TraceID: "tr", SpanID: "sp", Representation: "responses", Model: "gpt-4o-mini", DurationMS: "960", Tokens: "147", Status: "error", Error: "span failed"},
		Messages: []ThreadMessage{{Index: 0, Role: "assistant",
			Content: []ThreadPart{
				{Type: "text", Text: "spoken"},
				{Type: "json", Value: map[string]any{"k": "v"}},
				{Type: "state", State: "in_progress"},
				{Type: "unavailable", Count: 2},
				{Type: "unsupported", UnsupportedType: "image", Text: "a png"},
				{Type: "error", Text: "went wrong"},
				{Type: "exception", Text: "boom"},
			},
			Reasoning: []ThreadPart{{Type: "text", Text: "thinking"}, {Type: "summary", Text: "briefly"}},
		}},
	}
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, thread, 0); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"> trace `tr` · span `sp` · responses · gpt-4o-mini · error · 960 ms · 147 tokens",
		"> **Error:** span failed",
		"spoken", "```json\n{\n  \"k\": \"v\"\n}\n```", "[in_progress]",
		"[content unavailable: 2 items]", "[unsupported content: image — a png]",
		"### ERROR\n\nwent wrong", "### EXCEPTION\n\nboom",
		"### REASONING\n\nthinking", "### REASONING SUMMARY\n\nbriefly",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("Markdown omitted %q:\n%s", want, out.String())
		}
	}
}

// A message the collector emptied still has to appear: a thread that silently
// skips it reads as a shorter conversation than the one that was recorded.
func TestRenderThreadMarkdownKeepsAnEmptyMessage(t *testing.T) {
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, Thread{Messages: []ThreadMessage{{Index: 0, Role: "assistant"}}}, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "## ASSISTANT [0]\n\n[content unavailable]") {
		t.Fatalf("Markdown = %s", out.String())
	}
}

func TestRenderThreadMarkdownRendersRealFixtures(t *testing.T) {
	for _, tt := range []struct{ fixture, want string }{
		{"responses.json", "## ASSISTANT [2]\n\nSynthetic Responses acknowledgement.\n"},
		{"responses-unavailable.json", "## ASSISTANT [1]\n\n[content unavailable: 2 items]\n"},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			span := loadThreadFixture(t, tt.fixture)
			thread, err := NormalizeThread(span, ThreadSource{TraceID: spanString(span, "trace_id"), SpanID: spanString(span, "span_id")})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := RenderThreadMarkdown(&out, thread, 0); err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(out.String(), tt.want) {
				t.Fatalf("Markdown =\n%s\nwant suffix:\n%s", out.String(), tt.want)
			}
		})
	}
}
