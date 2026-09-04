package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RenderThreadMarkdown writes a readable, loss-conscious Markdown thread view.
func RenderThreadMarkdown(w io.Writer, thread Thread) error {
	var sections []string
	for _, message := range thread.Messages {
		heading := "## " + strings.ToUpper(message.Role) + fmt.Sprintf(" [%d]", message.Index)
		if message.Role == "tool" && message.Name != "" {
			heading += " — " + message.Name
		}
		var ordinary, errors, exceptions []ThreadPart
		for _, part := range message.Content {
			switch part.Type {
			case "error":
				errors = append(errors, part)
			case "exception":
				exceptions = append(exceptions, part)
			default:
				ordinary = append(ordinary, part)
			}
		}
		body := renderThreadParts(ordinary)
		if message.Role == "tool" && body != "" {
			body = "### TOOL RESULT\n\n" + body
		}
		if rendered := renderThreadParts(errors); rendered != "" {
			body = appendMarkdownSection(body, "### ERROR\n\n"+rendered)
		}
		if rendered := renderThreadParts(exceptions); rendered != "" {
			body = appendMarkdownSection(body, "### EXCEPTION\n\n"+rendered)
		}
		if len(message.Reasoning) > 0 {
			var ordinary, summaries []ThreadPart
			for _, part := range message.Reasoning {
				if part.Type == "summary" {
					summaries = append(summaries, part)
				} else {
					ordinary = append(ordinary, part)
				}
			}
			reasoning := renderThreadParts(ordinary)
			if reasoning != "" {
				body = appendMarkdownSection(body, "### REASONING\n\n"+reasoning)
			}
			if summary := renderThreadParts(summaries); summary != "" {
				body = appendMarkdownSection(body, "### REASONING SUMMARY\n\n"+summary)
			}
		}
		for _, call := range message.ToolCalls {
			callHeading := "### TOOL CALL"
			if call.Name != "" {
				callHeading += " — " + call.Name
			}
			if call.ID != "" {
				callHeading += " [" + call.ID + "]"
			}
			body = appendMarkdownSection(body, callHeading+"\n\n"+renderThreadValue(call.Arguments))
		}
		if body == "" {
			body = "[content unavailable]"
		}
		sections = append(sections, heading+"\n\n"+body)
	}
	_, err := io.WriteString(w, strings.Join(sections, "\n\n")+"\n")
	return err
}

func appendMarkdownSection(body, section string) string {
	if body == "" {
		return section
	}
	return body + "\n\n" + section
}

func renderThreadParts(parts []ThreadPart) string {
	sections := make([]string, 0, len(parts))
	for _, part := range parts {
		var rendered string
		switch part.Type {
		case "text", "summary", "error", "exception":
			rendered = part.Text
		case "json":
			rendered = renderThreadValue(part.Value)
		case "state":
			rendered = "[" + part.State + "]"
		case "unavailable":
			rendered = fmt.Sprintf("[content unavailable: %d items]", part.Count)
		case "unsupported":
			rendered = "[unsupported content: " + part.UnsupportedType + "]"
		}
		if rendered != "" {
			sections = append(sections, rendered)
		}
	}
	return strings.Join(sections, "\n\n")
}

func renderThreadValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return "```json\n" + string(encoded) + "\n```"
}
