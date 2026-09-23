package commands

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Thread is the canonical, portable representation of a traced conversation.
type Thread struct {
	Messages []ThreadMessage `json:"messages"`
	Source   ThreadSource    `json:"source"`
}

// ThreadSource identifies the origin of a normalized thread, and reports the
// span facts needed to judge the conversation it holds.
type ThreadSource struct {
	Representation string `json:"representation"`
	TraceID        string `json:"trace_id,omitempty"`
	SpanID         string `json:"span_id,omitempty"`
	// ResponseID names the stored Responses payload the turns were read from,
	// set only when the span itself held counts rather than content. Without
	// it the render claims text the span does not carry.
	ResponseID string `json:"response_id,omitempty"`
	Model      string `json:"model,omitempty"`
	DurationMS string `json:"duration_ms,omitempty"`
	Tokens     string `json:"tokens,omitempty"`
	// Status and Error are set only when the span itself failed.
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ThreadMessage is a message in conversation order, including system and developer
// messages. Index is its zero-based position, which is also its --slice position.
type ThreadMessage struct {
	Index      int              `json:"index"`
	Role       string           `json:"role"`
	Name       string           `json:"name,omitempty"`
	Content    []ThreadPart     `json:"content"`
	Reasoning  []ThreadPart     `json:"reasoning,omitempty"`
	ToolCalls  []ThreadToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// ThreadPart is content, a safely-rendered state, or an explicitly unavailable value.
type ThreadPart struct {
	Type            string `json:"type"`
	Text            string `json:"text,omitempty"`
	Value           any    `json:"value,omitempty"`
	State           string `json:"state,omitempty"`
	Count           int    `json:"count,omitempty"`
	UnsupportedType string `json:"unsupported_type,omitempty"`
	// Truncated is how many characters a cap cut from the part.
	Truncated int `json:"truncated_chars,omitempty"`
	// Omitted is how many recorded characters an "omitted" part stands in for.
	Omitted int `json:"omitted_chars,omitempty"`
}

// ThreadToolCall is an assistant request to invoke a tool.
type ThreadToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
	// ArgumentsText is set, and Arguments cleared, when a cap cut arguments
	// recorded as structure: a cut encoding is no longer the recorded value.
	ArgumentsText string `json:"arguments_text,omitempty"`
	// Truncated is how many characters were cut from the arguments.
	Truncated int `json:"truncated_chars,omitempty"`
}

// SliceThread applies a zero-based Python-style slice to thread messages.
func SliceThread(thread Thread, expression string) (Thread, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return Thread{}, fmt.Errorf("invalid slice expression: empty expression")
	}
	colons := 0
	for _, r := range expression {
		if r == ':' {
			colons++
		}
	}
	if colons > 1 {
		return Thread{}, fmt.Errorf("invalid slice expression %q: stride syntax is not supported", expression)
	}
	n := len(thread.Messages)
	start, stop := 0, n
	if colons == 0 {
		index, err := parseSliceInteger(expression)
		if err != nil {
			return Thread{}, fmt.Errorf("invalid slice expression %q: %w", expression, err)
		}
		if index < 0 {
			index += n
		}
		if index < 0 || index >= n {
			start, stop = 0, 0
		} else {
			start, stop = index, index+1
		}
	} else {
		parts := splitSlice(expression)
		var err error
		if parts[0] != "" {
			start, err = parseSliceInteger(parts[0])
			if err != nil {
				return Thread{}, fmt.Errorf("invalid slice expression %q: %w", expression, err)
			}
			if start < 0 {
				start += n
			}
		}
		if parts[1] != "" {
			stop, err = parseSliceInteger(parts[1])
			if err != nil {
				return Thread{}, fmt.Errorf("invalid slice expression %q: %w", expression, err)
			}
			if stop < 0 {
				stop += n
			}
		}
		start = clampSliceBound(start, n)
		stop = clampSliceBound(stop, n)
		if stop < start {
			stop = start
		}
	}
	result := thread
	result.Messages = append([]ThreadMessage{}, thread.Messages[start:stop]...)
	return result, nil
}

// ThreadKinds are the kinds --include selects from: the four roles a reader sees
// in the render, and reasoning, which is a section inside a message rather than
// a message of its own. `system` covers the developer role, which the render
// presents as an instruction the same way. `tool` is a tool-role turn, which
// is also what normalization makes of each tool_result in an Anthropic user turn.
var ThreadKinds = []string{"system", "user", "assistant", "tool", threadKindReasoning}

const threadKindReasoning = "reasoning"

// FilterThread keeps only the selected kinds. Role names decide whose messages
// are shown; reasoning decides whether the recorded thinking inside them is. A
// selection naming no role keeps every role, so --include reasoning reads as "the
// thinking, wherever it was recorded" rather than as nothing at all.
//
// Nothing the selection leaves out is deleted: a message whose role was not
// asked for keeps its place with an omitted stub in place of what it recorded,
// and so does the body of every message under a reasoning-only selection. A
// render that dropped the turn outright would show an assistant reacting to a
// result the reader cannot see, with nothing saying a result was there. A
// stubbed turn keeps its tool calls' ids and names, so a result still pairs
// with its call. Thinking left out of a message that still shows its body goes
// quietly — that is what --include user,assistant is for — unless it was all
// the message held. The stub counts recorded characters, not render labels;
// markers saying content is unavailable or was redacted stay in place beside it,
// since they are not content the stub could stand for.
func FilterThread(thread Thread, kinds []string) (Thread, error) {
	selected, err := parseThreadKinds(kinds)
	if err != nil || len(selected) == 0 {
		return thread, err
	}
	roles := slices.ContainsFunc(ThreadKinds, func(kind string) bool { return kind != threadKindReasoning && selected[kind] })
	kept := make([]ThreadMessage, 0, len(thread.Messages))
	for _, message := range thread.Messages {
		roleKept := !roles || selected[threadRoleKind(message)]
		hasBody := len(message.Content) > 0 || len(message.ToolCalls) > 0
		dropBody := hasBody && (!roleKept || !roles)
		dropReasoning := len(message.Reasoning) > 0 && (!roleKept || !selected[threadKindReasoning])
		omitted := 0
		var markers []ThreadPart
		if dropReasoning {
			omitted = threadRecordedChars(message.Reasoning, nil)
			message.Reasoning = nil
		}
		if dropBody {
			omitted += threadRecordedChars(message.Content, message.ToolCalls)
			markers = slices.DeleteFunc(slices.Clone(message.Content), func(part ThreadPart) bool { return part.Type != "unavailable" && part.Type != "state" })
			message.ToolCalls = omitThreadArguments(message.ToolCalls)
		}
		if dropBody || (dropReasoning && !hasBody) {
			message.Content = markers
			if omitted > 0 || len(markers) == 0 {
				message.Content = append([]ThreadPart{{Type: "omitted", Omitted: omitted}}, markers...)
			}
		}
		kept = append(kept, message)
	}
	result := thread
	result.Messages = kept
	return result, nil
}

// ExcludeThreadKinds turns an --exclude list into the --include list it means:
// every kind it does not name. Excluding every kind would read as no selection
// at all, which renders everything, so that is refused.
func ExcludeThreadKinds(kinds []string) ([]string, error) {
	excluded, err := parseThreadKinds(kinds)
	if err != nil {
		return nil, err
	}
	included := slices.DeleteFunc(slices.Clone(ThreadKinds), func(kind string) bool { return excluded[kind] })
	if len(included) == 0 {
		return nil, fmt.Errorf("--exclude names every part of the conversation, leaving nothing to render")
	}
	return included, nil
}

func parseThreadKinds(kinds []string) (map[string]bool, error) {
	selected := map[string]bool{}
	for _, kind := range kinds {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if kind == "" {
			continue
		}
		if !slices.Contains(ThreadKinds, kind) {
			return nil, fmt.Errorf("unknown message type %q: expected one of %s", kind, strings.Join(ThreadKinds, ", "))
		}
		selected[kind] = true
	}
	return selected, nil
}

// omitThreadArguments keeps each call's id and name and drops its arguments.
func omitThreadArguments(calls []ThreadToolCall) []ThreadToolCall {
	if calls == nil {
		return nil
	}
	omitted := make([]ThreadToolCall, len(calls))
	for index, call := range calls {
		omitted[index] = ThreadToolCall{ID: call.ID, Name: call.Name}
	}
	return omitted
}

// threadRecordedChars is how many characters the parts and call arguments
// recorded: their text and values, not the labels a render puts around them.
func threadRecordedChars(parts []ThreadPart, calls []ThreadToolCall) int {
	total := 0
	for _, part := range parts {
		total += utf8.RuneCountInString(part.Text) + utf8.RuneCountInString(threadValueText(part.Value))
	}
	for _, call := range calls {
		total += utf8.RuneCountInString(threadValueText(call.Arguments))
	}
	return total
}

// threadValueText is a recorded value as text, uncapped and unframed.
func threadValueText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, _ := encodeThreadValue(value)
	return encoded
}

// threadRoleKind is the kind a message is selected by.
func threadRoleKind(message ThreadMessage) string {
	if isInstructionRole(message.Role) {
		return "system"
	}
	return message.Role
}

// MatchThread keeps the messages whose recorded text matches. Everything a
// render puts on the page is searched, tool calls included: a call's name and
// its arguments are recorded text like any other, and "where did it call
// search_docs" is the question this exists to answer. Matching is
// case-insensitive; a pattern that means the case it wrote says so with the
// inline `(?-i)` flag.
func MatchThread(thread Thread, pattern string) (Thread, error) {
	expression, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return Thread{}, fmt.Errorf("invalid match pattern %q: %w", pattern, err)
	}
	kept := make([]ThreadMessage, 0, len(thread.Messages))
	for _, message := range thread.Messages {
		if messageMatches(message, expression) {
			kept = append(kept, message)
		}
	}
	result := thread
	result.Messages = kept
	return result, nil
}

func messageMatches(message ThreadMessage, expression *regexp.Regexp) bool {
	// The result of a call carries the call's id and nothing else that names
	// it, so searching for that id has to find both sides of the pair.
	if expression.MatchString(message.Name) || expression.MatchString(message.ToolCallID) {
		return true
	}
	for _, parts := range [][]ThreadPart{message.Content, message.Reasoning} {
		for _, part := range parts {
			if expression.MatchString(part.Text) || expression.MatchString(part.UnsupportedType) {
				return true
			}
			if part.Value != nil {
				if encoded, _ := encodeThreadValue(part.Value); expression.MatchString(encoded) {
					return true
				}
			}
		}
	}
	for _, call := range message.ToolCalls {
		if expression.MatchString(call.Name) || expression.MatchString(call.ID) {
			return true
		}
		if call.Arguments != nil {
			if encoded, _ := encodeThreadValue(call.Arguments); expression.MatchString(encoded) {
				return true
			}
		}
	}
	return false
}

// CapThread is the one place a thread is cut, before any render or structured
// format sees it: each block of recorded text keeps its first maxChars
// characters (toolChars for a tool turn) and says how many more there were,
// and Truncated counts them. Zero is no cap. A cut value recorded as structure
// is no longer the value, so it moves to Text (a json part) or ArgumentsText (a
// call) and the value is cleared; a string stays where it was.
func CapThread(thread Thread, maxChars, toolChars int) Thread {
	if maxChars <= 0 && toolChars <= 0 {
		return thread
	}
	result := thread
	result.Messages = make([]ThreadMessage, len(thread.Messages))
	for index, message := range thread.Messages {
		limit := maxChars
		if message.Role == "tool" {
			limit = toolChars
		}
		message.Content = capThreadParts(message.Content, limit)
		message.Reasoning = capThreadParts(message.Reasoning, limit)
		if len(message.ToolCalls) > 0 {
			calls := make([]ThreadToolCall, len(message.ToolCalls))
			for callIndex, call := range message.ToolCalls {
				call.Arguments, call.ArgumentsText, call.Truncated = capThreadValue(call.Arguments, maxChars)
				calls[callIndex] = call
			}
			message.ToolCalls = calls
		}
		result.Messages[index] = message
	}
	return result
}

func capThreadParts(parts []ThreadPart, maxChars int) []ThreadPart {
	if parts == nil {
		return nil
	}
	capped := make([]ThreadPart, len(parts))
	for index, part := range parts {
		switch part.Type {
		case "json":
			part.Value, part.Text, part.Truncated = capThreadValue(part.Value, maxChars)
		case "unsupported":
			var typeCut, textCut int
			part.UnsupportedType, typeCut = cutThreadText(part.UnsupportedType, maxChars)
			part.Text, textCut = cutThreadText(part.Text, maxChars)
			part.Truncated = typeCut + textCut
		case "state":
			part.State, part.Truncated = cutThreadText(part.State, maxChars)
		case "text", "summary", "error", "exception":
			part.Text, part.Truncated = cutThreadText(part.Text, maxChars)
		}
		capped[index] = part
	}
	return capped
}

// capThreadValue cuts a recorded value: a string in place, anything else by its
// encoding, which comes back as text with the value cleared once it is cut.
func capThreadValue(value any, maxChars int) (any, string, int) {
	if text, ok := value.(string); ok {
		cut, count := cutThreadText(text, maxChars)
		return cut, "", count
	}
	if value == nil {
		return nil, "", 0
	}
	// ponytail: a value that will not encode stays whole, for the render to label.
	encoded, ok := encodeThreadValue(value)
	if cut, count := cutThreadText(encoded, maxChars); ok && count > 0 {
		return nil, cut, count
	}
	return value, "", 0
}

// cutThreadText caps a block of recorded text, keeping its start and saying how
// much was left out, so a trace holding one tool result larger than the context
// it is read in stays readable. It runs on raw text, before any render frames
// or escapes it, so a cut can leave neither a tag, a fence nor an escape
// half-written, and the cap counts the characters that were recorded.
func cutThreadText(text string, maxChars int) (string, int) {
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text, 0
	}
	runes := []rune(text)
	return string(runes[:maxChars]) + fmt.Sprintf("\n[truncated: %d more characters]", len(runes)-maxChars), len(runes) - maxChars
}
