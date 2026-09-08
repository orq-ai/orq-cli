package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RenderThreadMarkdown writes a plain Markdown view of a thread, for a reader
// or a pipeline that wants headings rather than the XML render's tags. Recorded
// body content is written through as Markdown; structural metadata is escaped
// so trace values cannot inject headings or break the renderer's framing.
// `--format xml` remains the render to trust when a span's own text may imitate
// the surrounding structure. maxChars caps
// each rendered block, or is zero for no cap.
func RenderThreadMarkdown(w io.Writer, thread Thread, maxChars int) error {
	var sections []string
	if header := threadSourceHeader(thread.Source); header != "" {
		sections = append(sections, header)
	}
	for _, message := range thread.Messages {
		sections = append(sections, renderMarkdownMessage(message, maxChars))
	}
	_, err := io.WriteString(w, strings.Join(sections, "\n\n")+"\n")
	return err
}

func renderMarkdownMessage(message ThreadMessage, maxChars int) string {
	heading := fmt.Sprintf("## %s [%d]", markdownInline(strings.ToUpper(message.Role)), message.Index)
	if message.Name != "" {
		heading += " — " + markdownInline(message.Name)
	}

	ordinary, errors, exceptions := partitionThreadParts(message.Content)
	body := renderMarkdownParts(ordinary, maxChars)
	if message.Role == "tool" && body != "" {
		body = "### TOOL RESULT\n\n" + body
	}
	body = appendMarkdownSection(body, "### ERROR", renderMarkdownParts(errors, maxChars))
	body = appendMarkdownSection(body, "### EXCEPTION", renderMarkdownParts(exceptions, maxChars))

	reasoning, summaries := partitionThreadReasoning(message.Reasoning)
	body = appendMarkdownSection(body, "### REASONING", renderMarkdownParts(reasoning, maxChars))
	body = appendMarkdownSection(body, "### REASONING SUMMARY", renderMarkdownParts(summaries, maxChars))

	for _, call := range message.ToolCalls {
		callHeading := "### TOOL CALL"
		if call.Name != "" {
			callHeading += " — " + markdownInline(call.Name)
		}
		if call.ID != "" {
			callHeading += " [" + markdownInline(call.ID) + "]"
		}
		body = appendMarkdownSection(body, callHeading, renderMarkdownValue(call.Arguments, maxChars))
	}
	if body == "" {
		body = "[content unavailable]"
	}
	return heading + "\n\n" + body
}

// threadSourceHeader names the span the thread was read from, and the span
// facts a reader needs to judge the conversation. Trace-only requests pick one
// span out of many, and a reader who cannot see which one has no way to tell a
// wrong selection from a wrong conversation.
func threadSourceHeader(source ThreadSource) string {
	var fields []string
	if source.TraceID != "" {
		fields = append(fields, "trace `"+markdownInline(source.TraceID)+"`")
	}
	if source.SpanID != "" {
		fields = append(fields, "span `"+markdownInline(source.SpanID)+"`")
	}
	for _, value := range []string{source.Representation, source.Model, source.Status} {
		if value != "" {
			fields = append(fields, markdownInline(value))
		}
	}
	if source.DurationMS != "" {
		fields = append(fields, source.DurationMS+" ms")
	}
	if source.Tokens != "" {
		fields = append(fields, source.Tokens+" tokens")
	}
	if len(fields) == 0 {
		return ""
	}
	header := "> " + strings.Join(fields, " · ")
	if source.Error != "" {
		header += "\n>\n> **Error:** " + markdownInline(source.Error)
	}
	return header
}

func markdownInline(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "`", "\\`")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

func appendMarkdownSection(body, heading, content string) string {
	if content == "" {
		return body
	}
	section := heading + "\n\n" + content
	if body == "" {
		return section
	}
	return body + "\n\n" + section
}

func renderMarkdownParts(parts []ThreadPart, maxChars int) string {
	sections := make([]string, 0, len(parts))
	for _, part := range parts {
		var rendered string
		switch part.Type {
		case "text", "summary", "error", "exception":
			rendered = part.Text
		case "json":
			// Already capped inside its fence; the shared cap below would cut
			// the closing one off.
			if fenced := renderMarkdownValue(part.Value, maxChars); fenced != "" {
				sections = append(sections, fenced)
			}
			continue
		case "state":
			rendered = "[" + part.State + "]"
		case "unavailable":
			rendered = fmt.Sprintf("[content unavailable: %d items]", part.Count)
		case "unsupported":
			rendered = "[unsupported content: " + part.UnsupportedType
			if part.Text != "" {
				rendered += " — " + part.Text
			}
			rendered += "]"
		}
		if rendered != "" {
			sections = append(sections, truncateThreadText(rendered, maxChars))
		}
	}
	return strings.Join(sections, "\n\n")
}

// renderMarkdownValue fences an encoded value. The cap applies to the encoded
// text rather than the fenced block, so a truncated value cannot swallow its
// own closing fence and turn the rest of the thread into code.
func renderMarkdownValue(value any, maxChars int) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return truncateThreadText(text, maxChars)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return truncateThreadText(fmt.Sprint(value), maxChars)
	}
	return "```json\n" + truncateThreadText(string(encoded), maxChars) + "\n```"
}
