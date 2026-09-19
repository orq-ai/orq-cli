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

// RenderConversation writes a readable, loss-conscious view of a conversation. Messages are
// demarcated with XML tags because a span body is arbitrary recorded text: one
// that happens to contain this renderer's own framing would otherwise forge
// turns that were never in the conversation. maxChars caps each rendered block,
// or is zero for no cap.
func RenderConversation(w io.Writer, conversation Conversation, maxChars int) error {
	sections := []string{conversationOpenTag(conversation.Source)}
	for _, message := range conversation.Messages {
		sections = append(sections, renderConversationMessage(message, maxChars))
	}
	sections = append(sections, "</conversation>")
	_, err := io.WriteString(w, strings.Join(sections, "\n\n")+"\n")
	return err
}

func renderConversationMessage(message ConversationMessage, maxChars int) string {
	attributes := []string{"index=" + conversationAttribute(strconv.Itoa(message.Index)), "role=" + conversationAttribute(message.Role)}
	if message.Name != "" {
		attributes = append(attributes, "name="+conversationAttribute(message.Name))
	}
	if message.ToolCallID != "" {
		attributes = append(attributes, "tool_call_id="+conversationAttribute(message.ToolCallID))
	}

	blocks := conversationMessageBlocks(message, maxChars, escapeConversationTags, renderConversationValue)
	rendered := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Section {
		case conversationSectionBody, conversationSectionToolResult:
			// A tool result is the message's own content here; the role
			// attribute already says whose it is.
			rendered = append(rendered, block.Content)
		case conversationSectionToolCall:
			rendered = append(rendered, conversationElement(conversationToolCallTag(block.Call), block.Content))
		default:
			rendered = append(rendered, conversationElement(conversationElementNames[block.Section], block.Content))
		}
	}
	body := strings.Join(rendered, "\n\n")
	if body == "" {
		body = "[content unavailable]"
	}
	return "<message " + strings.Join(attributes, " ") + ">\n" + body + "\n</message>"
}

// conversationSection names a block inside a message. Which blocks a message has, and
// the order they appear in, is the same in both renders — only the label and
// the framing around each differ, so conversationMessageBlocks decides what exists
// and each renderer decides how it looks. A section added here reaches both
// views or neither.
type conversationSection int

const (
	conversationSectionBody conversationSection = iota
	conversationSectionToolResult
	conversationSectionError
	conversationSectionException
	conversationSectionReasoning
	conversationSectionReasoningSummary
	conversationSectionToolCall
)

// conversationBlock is one rendered block of a message: its section, its already
// capped and escaped content, and — for a tool call — the call it came from,
// since both renders label a call with its name and id.
type conversationBlock struct {
	Section conversationSection
	Call    ConversationToolCall
	Content string
}

// conversationMessageBlocks walks a message into the blocks both renders show, in the
// order both show them, dropping the ones with nothing to say. escape and value
// are the caller's framing, threaded down to the part walk.
func conversationMessageBlocks(message ConversationMessage, maxChars int, escape func(string) string, value func(any, int) string) []conversationBlock {
	parts := func(parts []ConversationPart) string {
		return renderConversationPartsWith(parts, maxChars, escape, value)
	}
	ordinary, errors, exceptions := partitionConversationParts(message.Content)
	reasoning, summaries := partitionConversationReasoning(message.Reasoning)

	body := conversationSectionBody
	if message.Role == "tool" {
		body = conversationSectionToolResult
	}

	blocks := make([]conversationBlock, 0, 5+len(message.ToolCalls))
	for _, block := range []conversationBlock{
		{Section: body, Content: parts(ordinary)},
		{Section: conversationSectionError, Content: parts(errors)},
		{Section: conversationSectionException, Content: parts(exceptions)},
		{Section: conversationSectionReasoning, Content: parts(reasoning)},
		{Section: conversationSectionReasoningSummary, Content: parts(summaries)},
	} {
		if block.Content != "" {
			blocks = append(blocks, block)
		}
	}
	for _, call := range message.ToolCalls {
		if content := value(call.Arguments, maxChars); content != "" {
			blocks = append(blocks, conversationBlock{Section: conversationSectionToolCall, Call: call, Content: content})
		}
	}
	return blocks
}

var conversationElementNames = map[conversationSection]string{
	conversationSectionError:            "error",
	conversationSectionException:        "exception",
	conversationSectionReasoning:        "reasoning",
	conversationSectionReasoningSummary: "reasoning_summary",
}

func conversationToolCallTag(call ConversationToolCall) string {
	tag := "tool_call"
	if call.ID != "" {
		tag += " id=" + conversationAttribute(call.ID)
	}
	if call.Name != "" {
		tag += " name=" + conversationAttribute(call.Name)
	}
	return tag
}

// partitionConversationParts keeps the XML and Markdown renderers' treatment of
// error and exception content identical while allowing each renderer to choose
// its own framing.
func partitionConversationParts(parts []ConversationPart) (ordinary, errors, exceptions []ConversationPart) {
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

func partitionConversationReasoning(parts []ConversationPart) (reasoning, summaries []ConversationPart) {
	for _, part := range parts {
		if part.Type == "summary" {
			summaries = append(summaries, part)
		} else {
			reasoning = append(reasoning, part)
		}
	}
	return reasoning, summaries
}

// conversationOpenTag names the span the conversation was read from, and the span facts a
// reader needs to judge the conversation. Trace-only requests pick one span out
// of many, and a reader who cannot see which one has no way to tell a wrong
// selection from a wrong conversation.
func conversationOpenTag(source ConversationSource) string {
	var attributes []string
	for _, field := range conversationSourceFields(source) {
		attributes = append(attributes, field.Attribute+"="+conversationAttribute(field.Value))
	}
	tag := "<conversation"
	if len(attributes) > 0 {
		tag += " " + strings.Join(attributes, " ")
	}
	if source.Error == "" {
		return tag + ">"
	}
	return tag + ">\n\n<span_error>\n" + escapeConversationTags(source.Error) + "\n</span_error>"
}

// conversationSourceField is one span fact, spelled for both renders: Attribute is
// the XML attribute name, and Label, Unit and Code say how Markdown presents
// the same value. Escaping stays the renderer's job — the Value here is as
// recorded.
type conversationSourceField struct {
	Attribute string
	Label     string
	Unit      string
	Value     string
	Code      bool
}

// conversationSourceFields lists the span facts, in display order, that a reader
// needs to judge the conversation. Both conversationOpenTag and conversationSourceHeader
// consume this one list, so a field cannot be shown by one render and dropped —
// or escaped by one render and forgotten — by the other.
func conversationSourceFields(source ConversationSource) []conversationSourceField {
	all := []conversationSourceField{
		{Attribute: "trace", Label: "trace", Value: source.TraceID, Code: true},
		{Attribute: "span", Label: "span", Value: source.SpanID, Code: true},
		{Attribute: "response", Label: "response", Value: source.ResponseID, Code: true},
		{Attribute: "format", Value: source.Representation},
		{Attribute: "model", Value: source.Model},
		{Attribute: "duration_ms", Unit: " ms", Value: source.DurationMS},
		{Attribute: "tokens", Unit: " tokens", Value: source.Tokens},
		{Attribute: "status", Value: source.Status},
	}
	fields := make([]conversationSourceField, 0, len(all))
	for _, field := range all {
		if field.Value != "" {
			fields = append(fields, field)
		}
	}
	return fields
}

func conversationElement(tag, content string) string {
	name, _, _ := strings.Cut(tag, " ")
	return "<" + tag + ">\n" + content + "\n</" + name + ">"
}

// renderConversationPartsWith walks the part types once for both renderers. Only the
// framing differs between them — whether recorded text is escaped, how a value
// is delimited — so the walk itself lives in one place and a ConversationPart type
// added to it reaches both views or neither. value returns text that is already
// capped, since a renderer that wraps it (a Markdown fence) has to cap inside
// its own delimiters. Text is cut before escape runs, so a cut can never split
// an escape that has not been written yet.
func renderConversationPartsWith(parts []ConversationPart, maxChars int, escape func(string) string, value func(any, int) string) string {
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
			rendered = "[unsupported content: " + escape(truncateConversationText(part.UnsupportedType, maxChars))
			if part.Text != "" {
				rendered += " — " + escape(truncateConversationText(part.Text, maxChars))
			}
			sections = append(sections, rendered+"]")
			continue
		}
		if rendered != "" {
			sections = append(sections, escape(truncateConversationText(rendered, maxChars)))
		}
	}
	return strings.Join(sections, "\n\n")
}

// unencodableConversationValue marks a value the renderer could not encode. A
// rendering failure must not read as recorded content, so the fallback is
// labelled rather than dropped into the body as if the span had said it.
const unencodableConversationValue = "[unencodable value]"

// encodeConversationValue encodes a non-string recorded value as JSON. ok is false
// when encoding failed and the text is Go's own rendering of the value rather
// than the value as it was recorded.
func encodeConversationValue(value any) (string, bool) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value), false
	}
	return string(encoded), true
}

func renderConversationValue(value any, maxChars int) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return escapeConversationTags(truncateConversationText(text, maxChars))
	}
	encoded, ok := encodeConversationValue(value)
	rendered := escapeConversationTags(truncateConversationText(encoded, maxChars))
	if !ok {
		return unencodableConversationValue + "\n" + rendered
	}
	return rendered
}

// conversationAttribute quotes an attribute value. Every one of them — a trace id, a
// model name, a role, a tool call id — is recorded span data, so the markup
// characters are escaped outright rather than only where they spell a tag: an
// attribute is never prose, so nothing legible is lost by escaping all of them.
func conversationAttribute(value string) string {
	return `"` + conversationAttributeEscaper.Replace(value) + `"`
}

var conversationAttributeEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\n", " ", "\r", " ", "\t", " ")

// conversationTagPattern names the tags that count as framing: the ones this
// renderer writes itself.
var conversationTagPattern = regexp.MustCompile(`</?(?:conversation|message|reasoning|reasoning_summary|tool_call|error|exception|span_error)\b`)

// escapeConversationTags is the whole of what this render escapes in recorded body
// text: the opening "<" of a framing tag becomes "&lt;", so a span whose text
// contains "</message>" cannot forge a turn. Nothing else in a body is touched.
// The render is a readable text view with forge-proof framing, not a parseable
// XML document — recorded "<div>", "Tom & Jerry", "a < b" and "?a=1&b=2" are
// reproduced character for character, as the reader recorded them. Recorded
// values that appear inside a tag rather than in a body are escaped more
// heavily; see conversationAttribute for why.
func escapeConversationTags(text string) string {
	return conversationTagPattern.ReplaceAllStringFunc(text, func(match string) string {
		return "&lt;" + strings.TrimPrefix(match, "<")
	})
}

// truncateConversationText caps a block of recorded text, keeping its start and
// saying how much was left out, so a trace holding one tool result larger than
// the context it is read in stays readable. It runs on raw text, before any
// framing escape and inside the caller's own delimiters — an XML element, a
// Markdown fence — so a cut can leave neither a tag, a fence nor an escape
// half-written, and --max-chars counts the characters that were recorded.
func truncateConversationText(text string, maxChars int) string {
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxChars]) + fmt.Sprintf("\n[truncated: %d more characters]", len(runes)-maxChars)
}
