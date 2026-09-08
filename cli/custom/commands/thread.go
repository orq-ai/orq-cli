package commands

import (
	"fmt"
	"slices"
	"strings"
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
	Model          string `json:"model,omitempty"`
	DurationMS     string `json:"duration_ms,omitempty"`
	Tokens         string `json:"tokens,omitempty"`
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
}

// ThreadToolCall is an assistant request to invoke a tool.
type ThreadToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
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

// ThreadKinds are the kinds --only selects from: the four roles a reader sees
// in the render, and reasoning, which is a section inside a message rather than
// a message of its own. `system` covers the developer role, which the render
// presents as an instruction the same way.
var ThreadKinds = []string{"system", "user", "assistant", "tool", threadKindReasoning}

const threadKindReasoning = "reasoning"

// FilterThread keeps only the selected kinds. Role names decide whose messages
// survive; reasoning decides whether the recorded thinking inside them does. A
// selection naming no role keeps every role, so --only reasoning reads as "the
// thinking, wherever it was recorded" rather than as nothing at all.
//
// A message the selection empties is dropped. One that was already empty
// survives a role selection, because the render says "[content unavailable]"
// for a turn that happened with nothing recorded and that is a fact about the
// trace rather than something the filter was asked to hide — but not a
// reasoning-only selection, which asked about sections and not about turns.
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
		if roles && !selected[threadRoleKind(message.Role)] {
			continue
		}
		had := len(message.Content) > 0 || len(message.Reasoning) > 0 || len(message.ToolCalls) > 0
		if !selected[threadKindReasoning] {
			message.Reasoning = nil
		}
		if !roles {
			message.Content, message.ToolCalls = nil, nil
		}
		if (had || !roles) && len(message.Content) == 0 && len(message.Reasoning) == 0 && len(message.ToolCalls) == 0 {
			continue
		}
		kept = append(kept, message)
	}
	result := thread
	result.Messages = kept
	return result, nil
}

func threadRoleKind(role string) string {
	if isInstructionRole(role) {
		return "system"
	}
	return role
}
