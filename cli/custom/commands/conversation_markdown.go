package commands

import (
	"fmt"
	"io"
	"strings"
)

// RenderConversationMarkdown writes a plain Markdown view of a conversation, for a reader
// or a pipeline that wants headings rather than the XML render's tags.
// Structural metadata — roles, names, ids, the source header — is escaped, so a
// trace value cannot inject a heading. Recorded body content deliberately is
// not: escaping every `##` and code fence in it would defeat the readable view
// that is this render's whole purpose. The consequence is that a span whose own
// text contains `## ASSISTANT [1]` produces something that reads like a turn,
// which is exactly what `escapeConversationTags` stops on the XML side — so
// `-o xml` stays the render to trust when the recorded text is not.
// maxChars caps each rendered block, or is zero for no cap.
func RenderConversationMarkdown(w io.Writer, conversation Conversation, maxChars int) error {
	var sections []string
	if header := conversationSourceHeader(conversation.Source); header != "" {
		sections = append(sections, header)
	}
	for _, message := range conversation.Messages {
		sections = append(sections, renderMarkdownMessage(message, maxChars))
	}
	_, err := io.WriteString(w, strings.Join(sections, "\n\n")+"\n")
	return err
}

func renderMarkdownMessage(message ConversationMessage, maxChars int) string {
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

	blocks := conversationMessageBlocks(message, maxChars, markdownText, renderMarkdownValue)
	rendered := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Section {
		case conversationSectionBody:
			rendered = append(rendered, block.Content)
		case conversationSectionToolCall:
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
var markdownHeadings = map[conversationSection]string{
	conversationSectionToolResult:       "### TOOL RESULT",
	conversationSectionError:            "### ERROR",
	conversationSectionException:        "### EXCEPTION",
	conversationSectionReasoning:        "### REASONING",
	conversationSectionReasoningSummary: "### REASONING SUMMARY",
}

func markdownToolCallHeading(call ConversationToolCall) string {
	heading := "### TOOL CALL"
	if call.Name != "" {
		heading += " — " + markdownInline(call.Name)
	}
	if call.ID != "" {
		heading += " [" + markdownInline(call.ID) + "]"
	}
	return heading
}

// conversationSourceHeader is conversationOpenTag's Markdown counterpart; see there for why
// the span facts are shown at all.
func conversationSourceHeader(source ConversationSource) string {
	var fields []string
	for _, field := range conversationSourceFields(source) {
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
func markdownSourceField(field conversationSourceField) string {
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
// turn the rest of the conversation into code. A value that would not encode keeps
// the fence but is labelled, so its Go rendering cannot be mistaken for
// recorded text.
func renderMarkdownValue(value any, maxChars int) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return truncateConversationText(text, maxChars)
	}
	encoded, ok := encodeConversationValue(value)
	body := truncateConversationText(encoded, maxChars)
	if !ok {
		body = unencodableConversationValue + "\n" + body
		fence := markdownFence(body)
		return fence + "\n" + body + "\n" + fence
	}
	fence := markdownFence(body)
	return fence + "json\n" + body + "\n" + fence
}

// markdownFence sizes a fence to outrun its content. A recorded value can
// contain a run of backticks of its own — a JSON string holding a fenced code
// block, say — and a fence no longer than that run ends the block early, which
// spills the rest of the value, and the rest of the conversation, back into prose.
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
