package commands

import (
	"fmt"
	"strings"
)

// Thread is the canonical, portable representation of a traced conversation.
type Thread struct {
	Instructions []ThreadInstruction `json:"instructions"`
	Messages     []ThreadMessage     `json:"messages"`
	Source       ThreadSource        `json:"source"`
}

// ThreadSource identifies the origin of a normalized thread.
type ThreadSource struct {
	Representation string `json:"representation"`
	TraceID        string `json:"trace_id,omitempty"`
	SpanID         string `json:"span_id,omitempty"`
}

// ThreadInstruction is a system or developer instruction that precedes messages.
type ThreadInstruction struct {
	Role    string       `json:"role"`
	Content []ThreadPart `json:"content"`
}

// ThreadMessage is a message in source order. Index is the source's zero-based index.
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
