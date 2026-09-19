package commands

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Conversation is the canonical, portable representation of a traced conversation.
type Conversation struct {
	Messages []ConversationMessage `json:"messages"`
	Source   ConversationSource    `json:"source"`
}

// ConversationSource identifies the origin of a normalized conversation, and reports the
// span facts needed to judge the conversation it holds.
type ConversationSource struct {
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

// ConversationMessage is a message in conversation order, including system and developer
// messages. Index is its zero-based position, which is also its --slice position.
type ConversationMessage struct {
	Index      int                    `json:"index"`
	Role       string                 `json:"role"`
	Name       string                 `json:"name,omitempty"`
	Content    []ConversationPart     `json:"content"`
	Reasoning  []ConversationPart     `json:"reasoning,omitempty"`
	ToolCalls  []ConversationToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
}

// ConversationPart is content, a safely-rendered state, or an explicitly unavailable value.
type ConversationPart struct {
	Type            string `json:"type"`
	Text            string `json:"text,omitempty"`
	Value           any    `json:"value,omitempty"`
	State           string `json:"state,omitempty"`
	Count           int    `json:"count,omitempty"`
	UnsupportedType string `json:"unsupported_type,omitempty"`
}

// ConversationToolCall is an assistant request to invoke a tool.
type ConversationToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

// SliceConversation applies a zero-based Python-style slice to conversation messages.
func SliceConversation(conversation Conversation, expression string) (Conversation, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return Conversation{}, fmt.Errorf("invalid slice expression: empty expression")
	}
	colons := 0
	for _, r := range expression {
		if r == ':' {
			colons++
		}
	}
	if colons > 1 {
		return Conversation{}, fmt.Errorf("invalid slice expression %q: stride syntax is not supported", expression)
	}
	n := len(conversation.Messages)
	start, stop := 0, n
	if colons == 0 {
		index, err := parseSliceInteger(expression)
		if err != nil {
			return Conversation{}, fmt.Errorf("invalid slice expression %q: %w", expression, err)
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
				return Conversation{}, fmt.Errorf("invalid slice expression %q: %w", expression, err)
			}
			if start < 0 {
				start += n
			}
		}
		if parts[1] != "" {
			stop, err = parseSliceInteger(parts[1])
			if err != nil {
				return Conversation{}, fmt.Errorf("invalid slice expression %q: %w", expression, err)
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
	result := conversation
	result.Messages = append([]ConversationMessage{}, conversation.Messages[start:stop]...)
	return result, nil
}

// ConversationKinds are the kinds --include selects from: the four roles a reader sees
// in the render, and reasoning, which is a section inside a message rather than
// a message of its own. `system` covers the developer role, which the render
// presents as an instruction the same way.
var ConversationKinds = []string{"system", "user", "assistant", "tool", conversationKindReasoning}

const conversationKindReasoning = "reasoning"

// FilterConversation keeps only the selected kinds. Role names decide whose messages
// survive; reasoning decides whether the recorded thinking inside them does. A
// selection naming no role keeps every role, so --include reasoning reads as "the
// thinking, wherever it was recorded" rather than as nothing at all.
//
// A message the selection empties is dropped. One that was already empty
// survives a role selection, because the render says "[content unavailable]"
// for a turn that happened with nothing recorded and that is a fact about the
// trace rather than something the filter was asked to hide — but not a
// reasoning-only selection, which asked about sections and not about turns.
func FilterConversation(conversation Conversation, kinds []string) (Conversation, error) {
	selected := map[string]bool{}
	roles := false
	for _, kind := range kinds {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if kind == "" {
			continue
		}
		if !slices.Contains(ConversationKinds, kind) {
			return Conversation{}, fmt.Errorf("unknown message type %q: expected one of %s", kind, strings.Join(ConversationKinds, ", "))
		}
		selected[kind] = true
		roles = roles || kind != conversationKindReasoning
	}
	if len(selected) == 0 {
		return conversation, nil
	}
	kept := make([]ConversationMessage, 0, len(conversation.Messages))
	for _, message := range conversation.Messages {
		if roles && !selected[conversationRoleKind(message.Role)] {
			continue
		}
		had := len(message.Content) > 0 || len(message.Reasoning) > 0 || len(message.ToolCalls) > 0
		if !selected[conversationKindReasoning] {
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
	result := conversation
	result.Messages = kept
	return result, nil
}

func conversationRoleKind(role string) string {
	if isInstructionRole(role) {
		return "system"
	}
	return role
}

// MatchConversation keeps the messages whose recorded text matches. Everything a
// render puts on the page is searched, tool calls included: a call's name and
// its arguments are recorded text like any other, and "where did it call
// search_docs" is the question this exists to answer. Matching is
// case-insensitive; a pattern that means the case it wrote says so with the
// inline `(?-i)` flag.
func MatchConversation(conversation Conversation, pattern string) (Conversation, error) {
	expression, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return Conversation{}, fmt.Errorf("invalid match pattern %q: %w", pattern, err)
	}
	kept := make([]ConversationMessage, 0, len(conversation.Messages))
	for _, message := range conversation.Messages {
		if messageMatches(message, expression) {
			kept = append(kept, message)
		}
	}
	result := conversation
	result.Messages = kept
	return result, nil
}

func messageMatches(message ConversationMessage, expression *regexp.Regexp) bool {
	// The result of a call carries the call's id and nothing else that names
	// it, so searching for that id has to find both sides of the pair.
	if expression.MatchString(message.Name) || expression.MatchString(message.ToolCallID) {
		return true
	}
	for _, parts := range [][]ConversationPart{message.Content, message.Reasoning} {
		for _, part := range parts {
			if expression.MatchString(part.Text) || expression.MatchString(part.UnsupportedType) {
				return true
			}
			if part.Value != nil {
				if encoded, _ := encodeConversationValue(part.Value); expression.MatchString(encoded) {
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
			if encoded, _ := encodeConversationValue(call.Arguments); expression.MatchString(encoded) {
				return true
			}
		}
	}
	return false
}
