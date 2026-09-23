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
	// Truncated is how many characters a cap cut from Text or Value, set only
	// on a structured render that was asked for one.
	Truncated int `json:"truncated_chars,omitempty"`
}

// ThreadToolCall is an assistant request to invoke a tool.
type ThreadToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
	// Truncated is how many characters a cap cut from Arguments.
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
// presents as an instruction the same way.
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
// result the reader cannot see, with nothing saying a result was there.
// Thinking left out of a message that still shows its body goes quietly —
// that is what --include user,assistant is for — unless it was all the message
// held.
func FilterThread(thread Thread, kinds []string) (Thread, error) {
	selected := map[string]bool{}
	roles := false
	for _, kind := range kinds {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if kind == "" {
			continue
		}
		if !slices.Contains(ThreadKinds, kind) {
			return Thread{}, fmt.Errorf("unknown message type %q: expected one of %s", kind, strings.Join(ThreadKinds, ", "))
		}
		selected[kind] = true
		roles = roles || kind != threadKindReasoning
	}
	if len(selected) == 0 {
		return thread, nil
	}
	kept := make([]ThreadMessage, 0, len(thread.Messages))
	for _, message := range thread.Messages {
		roleKept := !roles || selected[threadRoleKind(message.Role)]
		dropBody := !roleKept || !roles
		dropReasoning := !roleKept || !selected[threadKindReasoning]
		hadBody := len(message.Content) > 0 || len(message.ToolCalls) > 0
		bodyChars := threadPartsChars(message.Content) + threadToolCallsChars(message.ToolCalls)
		reasoningChars := threadPartsChars(message.Reasoning)
		hadReasoning := len(message.Reasoning) > 0
		if dropReasoning {
			message.Reasoning = nil
		}
		switch {
		case dropBody && hadBody:
			if dropReasoning {
				bodyChars += reasoningChars
			}
			message.Content, message.ToolCalls = []ThreadPart{omittedThreadPart(bodyChars)}, nil
		case dropReasoning && hadReasoning && !hadBody:
			message.Content = []ThreadPart{omittedThreadPart(reasoningChars)}
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
	excluded := map[string]bool{}
	for _, kind := range kinds {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if kind == "" {
			continue
		}
		if !slices.Contains(ThreadKinds, kind) {
			return nil, fmt.Errorf("unknown message type %q: expected one of %s", kind, strings.Join(ThreadKinds, ", "))
		}
		excluded[kind] = true
	}
	included := slices.DeleteFunc(slices.Clone(ThreadKinds), func(kind string) bool { return excluded[kind] })
	if len(included) == 0 {
		return nil, fmt.Errorf("--exclude names every part of the conversation, leaving nothing to render")
	}
	return included, nil
}

func omittedThreadPart(chars int) ThreadPart {
	return ThreadPart{Type: "omitted", Count: chars}
}

// threadPartsChars counts the characters the parts recorded, as a render would
// show them uncapped: text as written, a value as its encoded JSON.
func threadPartsChars(parts []ThreadPart) int {
	total := 0
	for _, part := range parts {
		switch part.Type {
		case "json":
			total += threadValueChars(part.Value)
		case "omitted":
			total += part.Count
		default:
			total += utf8.RuneCountInString(part.Text) + utf8.RuneCountInString(part.UnsupportedType)
		}
	}
	return total
}

func threadToolCallsChars(calls []ThreadToolCall) int {
	total := 0
	for _, call := range calls {
		total += threadValueChars(call.Arguments)
	}
	return total
}

func threadValueChars(value any) int {
	if value == nil {
		return 0
	}
	if text, ok := value.(string); ok {
		return utf8.RuneCountInString(text)
	}
	encoded, _ := encodeThreadValue(value)
	return utf8.RuneCountInString(encoded)
}

func threadRoleKind(role string) string {
	if isInstructionRole(role) {
		return "system"
	}
	return role
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

// CapThread cuts the thread's recorded text the way the xml and markdown renders
// do, for the structured formats that emit the thread itself. A value that
// needs cutting becomes its encoded text with the cut marked, since half a JSON
// document is not a value; Truncated says how much went.
func CapThread(thread Thread, maxChars, toolChars int) Thread {
	result := thread
	result.Messages = make([]ThreadMessage, len(thread.Messages))
	for index, message := range thread.Messages {
		limit := threadMessageCap(message, maxChars, toolChars)
		message.Content = capThreadParts(message.Content, limit)
		message.Reasoning = capThreadParts(message.Reasoning, limit)
		if len(message.ToolCalls) > 0 {
			calls := make([]ThreadToolCall, len(message.ToolCalls))
			for callIndex, call := range message.ToolCalls {
				call.Arguments, call.Truncated = capThreadValue(call.Arguments, maxChars)
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
		if part.Type == "json" {
			part.Value, part.Truncated = capThreadValue(part.Value, maxChars)
		} else {
			part.Text, part.Truncated = capThreadText(part.Text, maxChars)
		}
		capped[index] = part
	}
	return capped
}

func capThreadValue(value any, maxChars int) (any, int) {
	if value == nil || maxChars <= 0 || threadValueChars(value) <= maxChars {
		return value, 0
	}
	text, ok := value.(string)
	if !ok {
		text, _ = encodeThreadValue(value)
	}
	return capThreadText(text, maxChars)
}

func capThreadText(text string, maxChars int) (string, int) {
	cut := utf8.RuneCountInString(text) - maxChars
	if maxChars <= 0 || cut <= 0 {
		return text, 0
	}
	return truncateThreadText(text, maxChars), cut
}
