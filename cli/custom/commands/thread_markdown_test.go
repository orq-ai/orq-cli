package commands

import (
	"bytes"
	"errors"
	"reflect"
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
		// The header lists the span facts in the same order as the XML render's
		// attributes, because both renders read one list.
		"> trace `tr` · span `sp` · responses · gpt-4o-mini · 960 ms · 147 tokens · error",
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

// --max-chars counts the characters the span recorded. Escaping now runs after
// the cut, so no rendered-only construct can shorten a Markdown body that never
// contained one: a body whose first characters include "&" used to come out one
// character long.
func TestRenderThreadMarkdownCapsRecordedCharacters(t *testing.T) {
	body := "R&D notes " + strings.Repeat("alpha beta gamma delta ", 300)
	thread := Thread{Messages: []ThreadMessage{
		{Index: 0, Role: "user", Content: []ThreadPart{{Type: "text", Text: body}}},
		{Index: 1, Role: "tool", Content: []ThreadPart{{Type: "text", Text: "https://example.test/q?a=1&b=2 " + strings.Repeat("x", 100)}}},
	}}
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, thread, 4000); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	kept, _, found := strings.Cut(rendered, "\n[truncated: ")
	if !found {
		t.Fatalf("nothing was truncated: %s", rendered)
	}
	kept = strings.TrimPrefix(kept, "## USER [0]\n\n")
	if len([]rune(kept)) != 4000 {
		t.Fatalf("kept %d characters, want 4000: %q", len([]rune(kept)), kept)
	}
	if !strings.Contains(rendered, "R&D notes alpha") {
		t.Fatalf("recorded text was cut at its ampersand: %s", rendered)
	}
	if !strings.Contains(rendered, "https://example.test/q?a=1&b=2 x") {
		t.Fatalf("a URL query was cut at its ampersand: %s", rendered)
	}
}

// Every span fact in the header runs through one escape, so a field added to
// ThreadSource cannot reach the header unescaped: a recorded newline would end
// the blockquote and let the rest of the value read as a turn.
func TestRenderThreadMarkdownEscapesEverySourceField(t *testing.T) {
	forge := "0\n\n## USER [9]\n\ninjected"
	for name, source := range map[string]ThreadSource{
		"TraceID":        {TraceID: forge},
		"SpanID":         {SpanID: forge},
		"Representation": {Representation: forge},
		"Model":          {Model: forge},
		"DurationMS":     {DurationMS: forge},
		"Tokens":         {Tokens: forge},
		"Status":         {Status: forge},
		"Error":          {Error: forge},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := RenderThreadMarkdown(&out, Thread{Source: source}, 0); err != nil {
				t.Fatal(err)
			}
			rendered := out.String()
			if strings.Contains(rendered, "\n## USER [9]") {
				t.Fatalf("%s forged a turn: %s", name, rendered)
			}
			if !strings.Contains(rendered, "injected") {
				t.Fatalf("%s was dropped rather than escaped: %s", name, rendered)
			}
		})
	}
}

// A field the accessor forgets is a field one render shows and the other does
// not, which is how DurationMS and Tokens came to be unescaped here.
func TestThreadSourceFieldsCoverEverySourceField(t *testing.T) {
	source := ThreadSource{TraceID: "tr", SpanID: "sp", Representation: "rep", Model: "m", DurationMS: "1", Tokens: "2", Status: "st"}
	// Error is rendered on its own line by both renders, so it is not in the list.
	if got := len(threadSourceFields(source)); got != reflect.TypeOf(source).NumField()-1 {
		t.Fatalf("fields = %d, ThreadSource has %d", got, reflect.TypeOf(source).NumField())
	}
}

type unencodableValue struct{}

func (unencodableValue) MarshalJSON() ([]byte, error) { return nil, errors.New("no encoding") }

func (unencodableValue) String() string { return "go-rendering" }

// A value the renderer cannot encode must not read as recorded content in
// either render.
func TestRenderThreadMarksAnUnencodableValue(t *testing.T) {
	thread := Thread{Messages: []ThreadMessage{{Index: 0, Role: "assistant",
		ToolCalls: []ThreadToolCall{{Name: "lookup", Arguments: unencodableValue{}}},
	}}}
	var markdown, xml bytes.Buffer
	if err := RenderThreadMarkdown(&markdown, thread, 0); err != nil {
		t.Fatal(err)
	}
	if err := RenderThread(&xml, thread, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown.String(), "```\n[unencodable value]\ngo-rendering\n```") {
		t.Fatalf("Markdown = %s", markdown.String())
	}
	if !strings.Contains(xml.String(), "[unencodable value]\ngo-rendering") {
		t.Fatalf("XML = %s", xml.String())
	}
}

// A span that failed reports it even when nothing else about the span is
// known; a header built only from the other facts would drop the failure.
func TestRenderThreadMarkdownReportsAnErrorOnlySource(t *testing.T) {
	var out bytes.Buffer
	if err := RenderThreadMarkdown(&out, Thread{Source: ThreadSource{Error: "upstream timed out"}}, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "> **Error:** upstream timed out") {
		t.Fatalf("span error dropped: %q", out.String())
	}
}
