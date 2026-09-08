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
		body = appendThreadElement(body, tag, truncateThreadText(renderThreadValue(call.Arguments), maxChars))
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
	attributes := threadTagAttributes(
		"trace", source.TraceID,
		"span", source.SpanID,
		"format", source.Representation,
		"model", source.Model,
		"duration_ms", source.DurationMS,
		"tokens", source.Tokens,
		"status", source.Status,
	)
	tag := "<thread"
	if attributes != "" {
		tag += " " + attributes
	}
	if source.Error == "" {
		return tag + ">"
	}
	return tag + ">\n\n<span_error>\n" + escapeThreadTags(source.Error) + "\n</span_error>"
}

func threadTagAttributes(pairs ...string) string {
	var attributes []string
	for index := 0; index+1 < len(pairs); index += 2 {
		if pairs[index+1] != "" {
			attributes = append(attributes, pairs[index]+"="+threadAttribute(pairs[index+1]))
		}
	}
	return strings.Join(attributes, " ")
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
	sections := make([]string, 0, len(parts))
	for _, part := range parts {
		var rendered string
		switch part.Type {
		case "text", "summary", "error", "exception":
			rendered = escapeThreadTags(part.Text)
		case "json":
			rendered = renderThreadValue(part.Value)
		case "state":
			rendered = "[" + part.State + "]"
		case "unavailable":
			rendered = fmt.Sprintf("[content unavailable: %d items]", part.Count)
		case "unsupported":
			// Both halves are recorded span text, so both can carry framing.
			rendered = "[unsupported content: " + escapeThreadTags(part.UnsupportedType)
			if part.Text != "" {
				rendered += " — " + escapeThreadTags(part.Text)
			}
			rendered += "]"
		}
		if rendered != "" {
			sections = append(sections, truncateThreadText(rendered, maxChars))
		}
	}
	return strings.Join(sections, "\n\n")
}

func renderThreadValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return escapeThreadTags(text)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return escapeThreadTags(fmt.Sprint(value))
	}
	return escapeThreadTags(string(encoded))
}

// threadAttribute quotes an attribute value. Every one of them — a trace id, a
// model name, a role, a tool call id — is recorded span data, so the markup
// characters are escaped outright rather than only where they spell a tag: an
// attribute is never prose, so nothing legible is lost by escaping all of them.
func threadAttribute(value string) string {
	return `"` + threadAttributeEscaper.Replace(value) + `"`
}

var threadAttributeEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\n", " ", "\r", " ", "\t", " ")

// threadTagPattern matches this renderer's own framing. Only those tags are
// escaped when they appear in recorded content, so HTML a conversation happens
// to discuss survives intact.
var threadTagPattern = regexp.MustCompile(`</?(?:thread|message|reasoning|reasoning_summary|tool_call|error|exception|span_error)\b`)

func escapeThreadTags(text string) string {
	// XML text cannot contain a raw ampersand. Escape it before inserting the
	// framing escapes so the generated entities are not escaped a second time.
	text = strings.ReplaceAll(text, "&", "&amp;")
	return threadTagPattern.ReplaceAllStringFunc(text, func(match string) string {
		return "&lt;" + strings.TrimPrefix(match, "<")
	})
}

// truncateThreadText caps a rendered block, keeping its start and saying how
// much was left out. Only text inside an element is shortened, so a truncated
// message stays well-formed. A trace can hold one tool result larger than the
// context it is being read in.
func truncateThreadText(text string, maxChars int) string {
	if maxChars <= 0 || len([]rune(text)) <= maxChars {
		return text
	}
	kept := string([]rune(text)[:maxChars])
	// XML framing is escaped before this function is called. Do not leave a
	// generated entity such as "&lt;" half-written in the output.
	if ampersand := strings.LastIndex(kept, "&"); ampersand >= 0 && !strings.Contains(kept[ampersand:], ";") {
		kept = kept[:ampersand]
	}
	remaining := len([]rune(text)) - len([]rune(kept))
	return kept + fmt.Sprintf("\n[truncated: %d more characters]", remaining)
}
