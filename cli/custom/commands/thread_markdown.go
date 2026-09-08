package commands

import (
	"fmt"
	"io"
	"strings"
)

// RenderThreadMarkdown writes a plain Markdown view of a thread, for a reader
// or a pipeline that wants headings rather than the XML render's tags.
// Structural metadata — roles, names, ids, the source header — is escaped, so a
// trace value cannot inject a heading. Recorded body content deliberately is
// not: escaping every `##` and code fence in it would defeat the readable view
// that is this render's whole purpose. The consequence is that a span whose own
// text contains `## ASSISTANT [1]` produces something that reads like a turn,
// which is exactly what `escapeThreadTags` stops on the XML side — so
// `-o xml` stays the render to trust when the recorded text is not.
// maxChars caps each rendered block, or is zero for no cap.
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
	// The id the XML render carries as tool_call_id. Two parallel calls to one
	// tool produce two identical headings without it, and a reader pairing a
	// result to its call has nothing to pair on.
	if message.ToolCallID != "" {
		heading += " [" + markdownInline(message.ToolCallID) + "]"
	}

	blocks := threadMessageBlocks(message, maxChars, markdownText, renderMarkdownValue)
	rendered := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Section {
		case threadSectionBody:
			rendered = append(rendered, block.Content)
		case threadSectionToolCall:
			rendered = append(rendered, markdownSection(markdownToolCallHeading(block.Call), block.Content))
		default:
			rendered = append(rendered, markdownSection(markdownHeadings[block.Section], block.Content))
		}
	}
	body := strings.Join(rendered, "\n\n")
	if body == "" {
		body = "[content unavailable]"
	}
	return heading + "\n\n" + body
}

// markdownHeadings labels the sections the XML render names with a tag. A tool
// result gets a heading here and none there: the XML message carries role as an
// attribute a reader can see, while these headings are all the structure the
// Markdown render has.
var markdownHeadings = map[threadSection]string{
	threadSectionToolResult:       "### TOOL RESULT",
	threadSectionError:            "### ERROR",
	threadSectionException:        "### EXCEPTION",
	threadSectionReasoning:        "### REASONING",
	threadSectionReasoningSummary: "### REASONING SUMMARY",
}

func markdownToolCallHeading(call ThreadToolCall) string {
	heading := "### TOOL CALL"
	if call.Name != "" {
		heading += " — " + markdownInline(call.Name)
	}
	if call.ID != "" {
		heading += " [" + markdownInline(call.ID) + "]"
	}
	return heading
}

// threadSourceHeader is threadOpenTag's Markdown counterpart; see there for why
// the span facts are shown at all.
func threadSourceHeader(source ThreadSource) string {
	var fields []string
	for _, field := range threadSourceFields(source) {
		fields = append(fields, markdownSourceField(field))
	}
	var header string
	if len(fields) > 0 {
		header = "> " + strings.Join(fields, " · ")
	}
	if source.Error != "" {
		// A span that failed reports it even when nothing else about the span
		// is known: the failure is the fact the reader most needs.
		if header != "" {
			header += "\n>\n"
		}
		header += "> **Error:** " + markdownInline(source.Error)
	}
	return header
}

// markdownSourceField renders one span fact for the header. Every field goes
// through markdownInline here — a recorded value carrying a newline would
// otherwise end the blockquote and let the rest of it be read as a turn.
func markdownSourceField(field threadSourceField) string {
	rendered := markdownInline(field.Value)
	if field.Code {
		rendered = "`" + rendered + "`"
	}
	if field.Label != "" {
		rendered = field.Label + " " + rendered
	}
	return rendered + field.Unit
}

func markdownInline(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "`", "\\`")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

func markdownSection(heading, content string) string {
	return heading + "\n\n" + content
}

// markdownText is the Markdown render's escaper: recorded text is written as
// it was recorded, where the XML render has to escape it.
func markdownText(text string) string { return text }

// renderMarkdownValue fences an encoded value; a value that was recorded as a
// string is written as prose instead, since fencing it would claim a structure
// it does not have. The cap applies to the encoded text rather than to the
// fenced block, so a truncated value cannot swallow its own closing fence and
// turn the rest of the thread into code. A value that would not encode keeps
// the fence but is labelled, so its Go rendering cannot be mistaken for
// recorded text.
func renderMarkdownValue(value any, maxChars int) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return truncateThreadText(text, maxChars)
	}
	encoded, ok := encodeThreadValue(value)
	body := truncateThreadText(encoded, maxChars)
	if !ok {
		body = unencodableThreadValue + "\n" + body
		fence := markdownFence(body)
		return fence + "\n" + body + "\n" + fence
	}
	fence := markdownFence(body)
	return fence + "json\n" + body + "\n" + fence
}

// markdownFence sizes a fence to outrun its content. A recorded value can
// contain a run of backticks of its own — a JSON string holding a fenced code
// block, say — and a fence no longer than that run ends the block early, which
// spills the rest of the value, and the rest of the thread, back into prose.
func markdownFence(content string) string {
	longest, run := 0, 0
	for _, character := range content {
		if character != '`' {
			run = 0
			continue
		}
		run++
		if run > longest {
			longest = run
		}
	}
	if longest < 3 {
		return "```"
	}
	return strings.Repeat("`", longest+1)
}
