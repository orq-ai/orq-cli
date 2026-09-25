package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// RenderThread writes a readable, loss-conscious view of a thread. Messages are
// demarcated with XML tags because a span body is arbitrary recorded text: one
// that happens to contain this renderer's own framing would otherwise forge
// turns that were never in the conversation. It renders what it is given;
// CapThread is where a thread is cut.
func RenderThread(w io.Writer, thread Thread) error {
	sections := []string{threadOpenTag(thread.Source)}
	for _, message := range thread.Messages {
		sections = append(sections, renderThreadMessage(message))
	}
	sections = append(sections, "</thread>")
	_, err := io.WriteString(w, strings.Join(sections, "\n\n")+"\n")
	return err
}

func renderThreadMessage(message ThreadMessage) string {
	attributes := []string{"index=" + threadAttribute(strconv.Itoa(message.Index)), "role=" + threadAttribute(message.Role)}
	if message.Name != "" {
		attributes = append(attributes, "name="+threadAttribute(message.Name))
	}
	if message.ToolCallID != "" {
		attributes = append(attributes, "tool_call_id="+threadAttribute(message.ToolCallID))
	}

	blocks := threadMessageBlocks(message, escapeThreadTags, renderThreadValue)
	rendered := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Section {
		case threadSectionBody, threadSectionToolResult:
			// A tool result is the message's own content here; the role
			// attribute already says whose it is.
			rendered = append(rendered, block.Content)
		case threadSectionToolCall:
			if block.Content == "" {
				rendered = append(rendered, "<"+threadToolCallTag(block.Call)+"/>")
				continue
			}
			rendered = append(rendered, threadElement(threadToolCallTag(block.Call), block.Content))
		default:
			rendered = append(rendered, threadElement(threadElementNames[block.Section], block.Content))
		}
	}
	body := strings.Join(rendered, "\n\n")
	if body == "" {
		body = "[content unavailable]"
	}
	return "<message " + strings.Join(attributes, " ") + ">\n" + body + "\n</message>"
}

// threadSection names a block inside a message. Which blocks a message has, and
// the order they appear in, is the same in both renders — only the label and
// the framing around each differ, so threadMessageBlocks decides what exists
// and each renderer decides how it looks. A section added here reaches both
// views or neither.
type threadSection int

const (
	threadSectionBody threadSection = iota
	threadSectionToolResult
	threadSectionError
	threadSectionException
	threadSectionReasoning
	threadSectionReasoningSummary
	threadSectionToolCall
)

// threadBlock is one rendered block of a message: its section, its escaped
// content, and — for a tool call — the call it came from,
// since both renders label a call with its name and id.
type threadBlock struct {
	Section threadSection
	Call    ThreadToolCall
	Content string
}

// threadMessageBlocks walks a message into the blocks both renders show, in the
// order both show them, dropping the ones with nothing to say. escape and value
// are the caller's framing, threaded down to the part walk.
func threadMessageBlocks(message ThreadMessage, escape func(string) string, value func(any, string) string) []threadBlock {
	parts := func(parts []ThreadPart) string {
		return renderThreadPartsWith(parts, escape, value)
	}
	ordinary, errors, exceptions := partitionThreadParts(message.Content)
	reasoning, summaries := partitionThreadReasoning(message.Reasoning)

	body := threadSectionBody
	if message.Role == "tool" {
		body = threadSectionToolResult
	}

	blocks := make([]threadBlock, 0, 5+len(message.ToolCalls))
	for _, block := range []threadBlock{
		{Section: body, Content: parts(ordinary)},
		{Section: threadSectionError, Content: parts(errors)},
		{Section: threadSectionException, Content: parts(exceptions)},
		{Section: threadSectionReasoning, Content: parts(reasoning)},
		{Section: threadSectionReasoningSummary, Content: parts(summaries)},
	} {
		if block.Content != "" {
			blocks = append(blocks, block)
		}
	}
	// A call shows even with no arguments to show — none recorded, or left
	// out by a filter — since its id is what pairs it with its result.
	for _, call := range message.ToolCalls {
		blocks = append(blocks, threadBlock{Section: threadSectionToolCall, Call: call, Content: value(call.Arguments, call.ArgumentsText)})
	}
	return blocks
}

var threadElementNames = map[threadSection]string{
	threadSectionError:            "error",
	threadSectionException:        "exception",
	threadSectionReasoning:        "reasoning",
	threadSectionReasoningSummary: "reasoning_summary",
}

func threadToolCallTag(call ThreadToolCall) string {
	tag := "tool_call"
	if call.ID != "" {
		tag += " id=" + threadAttribute(call.ID)
	}
	if call.Name != "" {
		tag += " name=" + threadAttribute(call.Name)
	}
	return tag
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
		{Attribute: "response", Label: "response", Value: source.ResponseID, Code: true},
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

func threadElement(tag, content string) string {
	name, _, _ := strings.Cut(tag, " ")
	return "<" + tag + ">\n" + content + "\n</" + name + ">"
}

// renderThreadPartsWith walks the part types once for both renderers. Only the
// framing differs between them — whether recorded text is escaped, how a value
// is delimited — so the walk itself lives in one place and a ThreadPart type
// added to it reaches both views or neither. value frames a recorded value, or
// the cut encoding CapThread left in its place.
func renderThreadPartsWith(parts []ThreadPart, escape func(string) string, value func(any, string) string) string {
	sections := make([]string, 0, len(parts))
	for _, part := range parts {
		var rendered string
		switch part.Type {
		case "text", "summary", "error", "exception":
			rendered = part.Text
		case "json":
			if delimited := value(part.Value, part.Text); delimited != "" {
				sections = append(sections, delimited)
			}
			continue
		case "state":
			state := escape(part.State)
			if part.Count > 1 {
				state += fmt.Sprintf(": %d items", part.Count)
			}
			sections = append(sections, "["+state+"]")
			continue
		case "unavailable":
			sections = append(sections, fmt.Sprintf("[content unavailable: %d items]", part.Count))
			continue
		case "omitted":
			sections = append(sections, fmt.Sprintf("[omitted: %d characters]", part.Omitted))
			continue
		case "unsupported":
			// Both halves are recorded span text, so both can carry framing.
			rendered = "[unsupported content: " + escape(part.UnsupportedType)
			if part.Text != "" {
				rendered += " — " + escape(part.Text)
			}
			sections = append(sections, rendered+"]")
			continue
		}
		if rendered != "" {
			sections = append(sections, escape(rendered))
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

func renderThreadValue(value any, cut string) string {
	if cut != "" {
		return escapeThreadTags(cut)
	}
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return escapeThreadTags(text)
	}
	encoded, ok := encodeThreadValue(value)
	rendered := escapeThreadTags(encoded)
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
