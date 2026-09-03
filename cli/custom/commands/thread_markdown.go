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
	if len(thread.Instructions) > 0 {
		instructionSections := []string{"# INSTRUCTIONS"}
		for _, instruction := range thread.Instructions {
			body := renderThreadParts(instruction.Content)
			if body != "" {
				instructionSections = append(instructionSections, "## "+strings.ToUpper(instruction.Role), body)
			}
		}
		sections = append(sections, strings.Join(instructionSections, "\n\n"))
	}
	for _, message := range thread.Messages {
		heading := "## " + strings.ToUpper(message.Role) + fmt.Sprintf(" [%d]", message.Index)
		if message.Role == "tool" && message.Name != "" {
			heading += " — " + message.Name
		}
		body := renderThreadParts(message.Content)
		if len(message.Reasoning) > 0 {
			reasoning := renderThreadParts(message.Reasoning)
			if reasoning != "" {
				body = appendMarkdownSection(body, "### REASONING\n\n"+reasoning)
			}
		}
		for _, call := range message.ToolCalls {
			name := call.Name
			if name == "" {
				name = "unknown"
			}
			callHeading := "### TOOL CALL — " + name
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
		case "text":
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
