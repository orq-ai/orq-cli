package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// RenderThread writes a readable, loss-conscious view of a thread. Messages are
// demarcated with XML tags because a span body is arbitrary recorded text: one
// that happens to contain this renderer's own framing would otherwise forge
// turns that were never in the conversation. maxChars caps each rendered block,
// or is zero for no cap.
func RenderThread(w io.Writer, thread Thread, maxChars int) error {
	sections := []string{threadOpenTag(thread.Source)}
	for _, message := range thread.Messages {
		sections = append(sections, renderThreadMessage(message, maxChars))
	}
	sections = append(sections, "</thread>")
	_, err := io.WriteString(w, strings.Join(sections, "\n\n")+"\n")
	return err
}

func renderThreadMessage(message ThreadMessage, maxChars int) string {
	attributes := []string{"index=" + threadAttribute(strconv.Itoa(message.Index)), "role=" + threadAttribute(message.Role)}
	if message.Name != "" {
		attributes = append(attributes, "name="+threadAttribute(message.Name))
	}
	if message.ToolCallID != "" {
		attributes = append(attributes, "tool_call_id="+threadAttribute(message.ToolCallID))
	}

	ordinary, errors, exceptions := partitionThreadParts(message.Content)
	body := renderThreadParts(ordinary, maxChars)
	body = appendThreadElement(body, "error", renderThreadParts(errors, maxChars))
	body = appendThreadElement(body, "exception", renderThreadParts(exceptions, maxChars))

	reasoning, summaries := partitionThreadReasoning(message.Reasoning)
	body = appendThreadElement(body, "reasoning", renderThreadParts(reasoning, maxChars))
	body = appendThreadElement(body, "reasoning_summary", renderThreadParts(summaries, maxChars))

	for _, call := range message.ToolCalls {
		tag := "tool_call"
		if call.ID != "" {
			tag += " id=" + threadAttribute(call.ID)
		}
		if call.Name != "" {
			tag += " name=" + threadAttribute(call.Name)
		}
		body = appendThreadElement(body, tag, renderThreadValue(call.Arguments, maxChars))
	}
	if body == "" {
		body = "[content unavailable]"
	}
	return "<message " + strings.Join(attributes, " ") + ">\n" + body + "\n</message>"
}

// partitionThreadParts keeps the XML and Markdown renderers' treatment of
// error and exception content identical while allowing each renderer to choose
// its own framing.
func partitionThreadParts(parts []ThreadPart) (ordinary, errors, exceptions []ThreadPart) {
	for _, part := range parts {
		switch part.Type {
		case "error":
			errors = append(errors, part)
		case "exception":
			exceptions = append(exceptions, part)
		default:
			ordinary = append(ordinary, part)
		}
	}
	return ordinary, errors, exceptions
}

func partitionThreadReasoning(parts []ThreadPart) (reasoning, summaries []ThreadPart) {
	for _, part := range parts {
		if part.Type == "summary" {
			summaries = append(summaries, part)
		} else {
			reasoning = append(reasoning, part)
		}
	}
	return reasoning, summaries
}

// threadOpenTag names the span the thread was read from, and the span facts a
// reader needs to judge the conversation. Trace-only requests pick one span out
// of many, and a reader who cannot see which one has no way to tell a wrong
// selection from a wrong conversation.
func threadOpenTag(source ThreadSource) string {
	var attributes []string
	for _, field := range threadSourceFields(source) {
		attributes = append(attributes, field.Attribute+"="+threadAttribute(field.Value))
	}
	tag := "<thread"
	if len(attributes) > 0 {
		tag += " " + strings.Join(attributes, " ")
	}
	if source.Error == "" {
		return tag + ">"
	}
	return tag + ">\n\n<span_error>\n" + escapeThreadTags(source.Error) + "\n</span_error>"
}

// threadSourceField is one span fact, spelled for both renders: Attribute is
// the XML attribute name, and Label, Unit and Code say how Markdown presents
// the same value. Escaping stays the renderer's job — the Value here is as
// recorded.
type threadSourceField struct {
	Attribute string
	Label     string
	Unit      string
	Value     string
	Code      bool
}

// threadSourceFields lists the span facts, in display order, that a reader
// needs to judge the conversation. Both threadOpenTag and threadSourceHeader
// consume this one list, so a field cannot be shown by one render and dropped —
// or escaped by one render and forgotten — by the other.
func threadSourceFields(source ThreadSource) []threadSourceField {
	all := []threadSourceField{
		{Attribute: "trace", Label: "trace", Value: source.TraceID, Code: true},
		{Attribute: "span", Label: "span", Value: source.SpanID, Code: true},
		{Attribute: "format", Value: source.Representation},
		{Attribute: "model", Value: source.Model},
		{Attribute: "duration_ms", Unit: " ms", Value: source.DurationMS},
		{Attribute: "tokens", Unit: " tokens", Value: source.Tokens},
		{Attribute: "status", Value: source.Status},
	}
	fields := make([]threadSourceField, 0, len(all))
	for _, field := range all {
		if field.Value != "" {
			fields = append(fields, field)
		}
	}
	return fields
}

func appendThreadElement(body, tag, content string) string {
	if content == "" {
		return body
	}
	name, _, _ := strings.Cut(tag, " ")
	element := "<" + tag + ">\n" + content + "\n</" + name + ">"
	if body == "" {
		return element
	}
	return body + "\n\n" + element
}

func renderThreadParts(parts []ThreadPart, maxChars int) string {
	return renderThreadPartsWith(parts, maxChars, escapeThreadTags, renderThreadValue)
}

// renderThreadPartsWith walks the part types once for both renderers. Only the
// framing differs between them — whether recorded text is escaped, how a value
// is delimited — so the walk itself lives in one place and a ThreadPart type
// added to it reaches both views or neither. value returns text that is already
// capped, since a renderer that wraps it (a Markdown fence) has to cap inside
// its own delimiters. Text is cut before escape runs, so a cut can never split
// an escape that has not been written yet.
func renderThreadPartsWith(parts []ThreadPart, maxChars int, escape func(string) string, value func(any, int) string) string {
	sections := make([]string, 0, len(parts))
	for _, part := range parts {
		var rendered string
		switch part.Type {
		case "text", "summary", "error", "exception":
			rendered = part.Text
		case "json":
			if delimited := value(part.Value, maxChars); delimited != "" {
				sections = append(sections, delimited)
			}
			continue
		case "state":
			rendered = "[" + part.State + "]"
		case "unavailable":
			rendered = fmt.Sprintf("[content unavailable: %d items]", part.Count)
		case "unsupported":
			// Both halves are recorded span text, so both can carry framing —
			// and both are capped here rather than after the brackets are
			// added, so the cap counts what the span recorded and a cut can
			// never land inside this label and leave it unclosed.
			rendered = "[unsupported content: " + escape(truncateThreadText(part.UnsupportedType, maxChars))
			if part.Text != "" {
				rendered += " — " + escape(truncateThreadText(part.Text, maxChars))
			}
			sections = append(sections, rendered+"]")
			continue
		}
		if rendered != "" {
			sections = append(sections, escape(truncateThreadText(rendered, maxChars)))
		}
	}
	return strings.Join(sections, "\n\n")
}

// unencodableThreadValue marks a value the renderer could not encode. A
// rendering failure must not read as recorded content, so the fallback is
// labelled rather than dropped into the body as if the span had said it.
const unencodableThreadValue = "[unencodable value]"

// encodeThreadValue encodes a non-string recorded value as JSON. ok is false
// when encoding failed and the text is Go's own rendering of the value rather
// than the value as it was recorded.
func encodeThreadValue(value any) (string, bool) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value), false
	}
	return string(encoded), true
}

func renderThreadValue(value any, maxChars int) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return escapeThreadTags(truncateThreadText(text, maxChars))
	}
	encoded, ok := encodeThreadValue(value)
	rendered := escapeThreadTags(truncateThreadText(encoded, maxChars))
	if !ok {
		return unencodableThreadValue + "\n" + rendered
	}
	return rendered
}

// threadAttribute quotes an attribute value. Every one of them — a trace id, a
// model name, a role, a tool call id — is recorded span data, so the markup
// characters are escaped outright rather than only where they spell a tag: an
// attribute is never prose, so nothing legible is lost by escaping all of them.
func threadAttribute(value string) string {
	return `"` + threadAttributeEscaper.Replace(value) + `"`
}

var threadAttributeEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\n", " ", "\r", " ", "\t", " ")

// threadTagPattern names the tags that count as framing: the ones this
// renderer writes itself.
var threadTagPattern = regexp.MustCompile(`</?(?:thread|message|reasoning|reasoning_summary|tool_call|error|exception|span_error)\b`)

// escapeThreadTags is the whole of what this render escapes in recorded body
// text: the opening "<" of a framing tag becomes "&lt;", so a span whose text
// contains "</message>" cannot forge a turn. Nothing else in a body is touched.
// The render is a readable text view with forge-proof framing, not a parseable
// XML document — recorded "<div>", "Tom & Jerry", "a < b" and "?a=1&b=2" are
// reproduced character for character, as the reader recorded them. Recorded
// values that appear inside a tag rather than in a body are escaped more
// heavily; see threadAttribute for why.
func escapeThreadTags(text string) string {
	return threadTagPattern.ReplaceAllStringFunc(text, func(match string) string {
		return "&lt;" + strings.TrimPrefix(match, "<")
	})
}

// truncateThreadText caps a block of recorded text, keeping its start and
// saying how much was left out, so a trace holding one tool result larger than
// the context it is read in stays readable. It runs on raw text, before any
// framing escape and inside the caller's own delimiters — an XML element, a
// Markdown fence — so a cut can leave neither a tag, a fence nor an escape
// half-written, and --max-chars counts the characters that were recorded.
func truncateThreadText(text string, maxChars int) string {
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxChars]) + fmt.Sprintf("\n[truncated: %d more characters]", len(runes)-maxChars)
}
