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
// turns that were never in the conversation. maxChars caps the text each
// message renders, or is zero for no cap.
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
	// One budget for the whole message: a cap that applied to each block
	// separately would let a message with many parts render without limit.
	budget := &threadBudget{remaining: maxChars, capped: maxChars > 0}
	attributes := []string{"index=" + strconv.Quote(strconv.Itoa(message.Index)), "role=" + strconv.Quote(message.Role)}
	if message.Name != "" {
		attributes = append(attributes, "name="+strconv.Quote(message.Name))
	}
	if message.ToolCallID != "" {
		attributes = append(attributes, "tool_call_id="+strconv.Quote(message.ToolCallID))
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
	body := renderThreadParts(ordinary, budget)
	body = appendThreadElement(body, "error", renderThreadParts(errors, budget))
	body = appendThreadElement(body, "exception", renderThreadParts(exceptions, budget))

	var reasoning, summaries []ThreadPart
	for _, part := range message.Reasoning {
		if part.Type == "summary" {
			summaries = append(summaries, part)
		} else {
			reasoning = append(reasoning, part)
		}
	}
	body = appendThreadElement(body, "reasoning", renderThreadParts(reasoning, budget))
	body = appendThreadElement(body, "reasoning_summary", renderThreadParts(summaries, budget))

	for _, call := range message.ToolCalls {
		tag := "tool_call"
		if call.ID != "" {
			tag += " id=" + strconv.Quote(call.ID)
		}
		if call.Name != "" {
			tag += " name=" + strconv.Quote(call.Name)
		}
		body = appendThreadElement(body, tag, budget.take(renderThreadValue(call.Arguments)))
	}
	if body == "" {
		body = "[content unavailable]"
	}
	return "<message " + strings.Join(attributes, " ") + ">\n" + body + "\n</message>"
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
		// Reported only when the span failed; a healthy span says nothing.
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
			attributes = append(attributes, pairs[index]+"="+strconv.Quote(pairs[index+1]))
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

func renderThreadParts(parts []ThreadPart, budget *threadBudget) string {
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
			rendered = "[unsupported content: " + part.UnsupportedType
			if part.Text != "" {
				rendered += " — " + part.Text
			}
			rendered += "]"
		}
		if rendered != "" {
			sections = append(sections, budget.take(rendered))
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

// threadTagPattern matches this renderer's own framing. Only those tags are
// escaped when they appear in recorded content, so HTML a conversation happens
// to discuss survives intact.
var threadTagPattern = regexp.MustCompile(`</?(?:thread|message|reasoning|reasoning_summary|tool_call|error|exception|span_error)\b`)

func escapeThreadTags(text string) string {
	return threadTagPattern.ReplaceAllStringFunc(text, func(match string) string {
		return "&lt;" + strings.TrimPrefix(match, "<")
	})
}

// threadBudget caps how much text one message renders. It shortens the text
// inside elements and never the elements themselves, so a truncated message is
// still well-formed and still shows which tools it called.
type threadBudget struct {
	remaining int
	capped    bool
}

// take returns as much of a rendered block as the message has budget left for,
// saying how much it left out. A trace can hold a single tool result larger
// than the context it is being read in.
func (budget *threadBudget) take(text string) string {
	if !budget.capped {
		return text
	}
	if len(text) <= budget.remaining {
		budget.remaining -= len(text)
		return text
	}
	kept := text[:budget.remaining]
	// Never split a rune; a cut multi-byte character renders as garbage.
	for len(kept) > 0 && !isThreadRuneStart(text[len(kept)]) {
		kept = kept[:len(kept)-1]
	}
	budget.remaining = 0
	return kept + fmt.Sprintf("\n[truncated: %d more characters]", len(text)-len(kept))
}

func isThreadRuneStart(b byte) bool { return b&0xC0 != 0x80 }
