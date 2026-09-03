package commands

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// NormalizeThread converts supported Chat Completions and Responses span payloads
// into a single loss-conscious representation.
func NormalizeThread(span map[string]any, source ThreadSource) (Thread, error) {
	responsesInstructions, _ := threadLookup(span, "openresponses.instructions")
	responsesInput, _ := threadLookup(span, "openresponses.input")
	responsesOutput, _ := threadLookup(span, "openresponses.output")
	responsesInstructionsOK := usableThreadParts(responsesInstructions)
	responsesInputOK := usableResponsesValue(responsesInput)
	responsesOutputOK := usableResponsesValue(responsesOutput)

	chatInput := firstUsableChatValue(span, "gen_ai.input", "input", chatMessages)
	chatOutput := firstUsableChatValue(span, "gen_ai.output", "output", chatOutputMessages)
	chatInputOK, chatOutputOK := len(chatMessages(chatInput)) > 0, len(chatOutputMessages(chatOutput)) > 0
	if !responsesInstructionsOK && !responsesInputOK && !responsesOutputOK && !chatInputOK && !chatOutputOK {
		return Thread{}, fmt.Errorf("span does not contain a supported Chat Completions or Responses conversation")
	}

	thread := Thread{Source: source}
	if responsesInstructionsOK {
		for _, instruction := range threadParts(responsesInstructions) {
			thread.Instructions = append(thread.Instructions, ThreadInstruction{Role: "system", Content: []ThreadPart{instruction}})
		}
	}
	inputCount, pending, toolNames := 0, []ThreadPart(nil), map[string]string{}
	if responsesInputOK {
		inputItems := responseItems(responsesInput)
		if count, unavailable := unavailableThreadCount(responsesInput); unavailable && len(inputItems) == 0 {
			thread.Messages = append(thread.Messages, ThreadMessage{Index: 0, Role: "user", Content: []ThreadPart{{Type: "unavailable", Count: count}}})
			inputCount = count
		} else {
			inputCount = len(inputItems)
			for index, item := range inputItems {
				pending = thread.appendResponseItem(item, index, pending, toolNames)
			}
		}
	} else if chatInputOK {
		inputCount = appendChatInput(&thread, chatInput, len(thread.Instructions) == 0, toolNames)
	}
	if responsesOutputOK {
		outputItems := responseItems(responsesOutput)
		boundaryChecked := false
		for offset, item := range outputItems {
			before := len(thread.Messages)
			pending = thread.appendResponseItem(item, inputCount+offset, pending, toolNames)
			if !boundaryChecked && len(thread.Messages) > before {
				boundaryChecked = true
				last := len(thread.Messages) - 1
				if last > 0 && thread.Messages[last-1].Role == "assistant" && thread.Messages[last].Role == "assistant" && reflect.DeepEqual(withoutIndex(thread.Messages[last-1]), withoutIndex(thread.Messages[last])) {
					thread.Messages = thread.Messages[:last]
				}
			}
		}
		if count, unavailable := unavailableThreadCount(responsesOutput); unavailable && len(outputItems) == 0 {
			thread.Messages = append(thread.Messages, ThreadMessage{Index: inputCount, Role: "assistant", Content: []ThreadPart{{Type: "unavailable", Count: count}}, Reasoning: pending})
			pending = nil
		}
	} else if chatOutputOK {
		pending = appendChatOutput(&thread, chatOutput, inputCount, toolNames, pending)
	}
	if len(pending) > 0 {
		outputCount := 0
		if responsesOutputOK {
			outputCount = len(responseItems(responsesOutput))
		} else if chatOutputOK {
			outputCount = len(chatOutputMessages(chatOutput))
		}
		thread.Messages = append(thread.Messages, ThreadMessage{Index: inputCount + outputCount, Role: "assistant", Content: []ThreadPart{}, Reasoning: pending})
	}
	if responsesInstructionsOK || responsesInputOK || responsesOutputOK {
		thread.Source.Representation = "responses"
	} else {
		thread.Source.Representation = "chat_completions"
	}
	return thread, nil
}

func appendChatInput(thread *Thread, input any, appendInstructions bool, toolNames map[string]string) int {
	inputMessages := chatMessages(input)
	for index, raw := range inputMessages {
		if message, instruction, ok := normalizeChatMessage(raw, index); ok {
			if instruction != nil && appendInstructions {
				thread.Instructions = append(thread.Instructions, *instruction)
			} else if instruction == nil {
				resolveChatToolName(&message, toolNames)
				rememberChatToolNames(message, toolNames)
				thread.Messages = append(thread.Messages, message)
			}
		}
	}
	return len(inputMessages)
}

func appendChatOutput(thread *Thread, output any, inputCount int, toolNames map[string]string, pending []ThreadPart) []ThreadPart {
	outputMessages := chatOutputMessages(output)
	for offset, raw := range outputMessages {
		message, instruction, ok := normalizeChatMessage(raw, inputCount+offset)
		if !ok {
			continue
		}
		if instruction != nil {
			continue
		}
		resolveChatToolName(&message, toolNames)
		rememberChatToolNames(message, toolNames)
		if offset == 0 && len(thread.Messages) > 0 && thread.Messages[len(thread.Messages)-1].Role == "assistant" && message.Role == "assistant" && reflect.DeepEqual(withoutIndex(thread.Messages[len(thread.Messages)-1]), withoutIndex(message)) {
			continue
		}
		if message.Role == "assistant" && len(pending) > 0 {
			message.Reasoning = append(append([]ThreadPart(nil), pending...), message.Reasoning...)
			pending = nil
		}
		thread.Messages = append(thread.Messages, message)
	}
	return pending
}

func rememberChatToolNames(message ThreadMessage, toolNames map[string]string) {
	if message.Role != "assistant" {
		return
	}
	for _, call := range message.ToolCalls {
		if call.ID != "" && call.Name != "" {
			toolNames[call.ID] = call.Name
		}
	}
}

func resolveChatToolName(message *ThreadMessage, toolNames map[string]string) {
	if message.Role == "tool" && message.Name == "" {
		message.Name = toolNames[message.ToolCallID]
	}
}

func withoutIndex(message ThreadMessage) ThreadMessage { message.Index = 0; return message }

func chatMessages(value any) []any {
	value = decodeThreadValue(value)
	if messages, ok := value.([]any); ok {
		return messages
	}
	if object, ok := threadMap(value); ok {
		if messages, ok := decodeThreadValue(object["messages"]).([]any); ok {
			return messages
		}
	}
	return nil
}

func chatOutputMessages(value any) []any {
	value = decodeThreadValue(value)
	if object, ok := threadMap(value); ok {
		if choices, ok := decodeThreadValue(object["choices"]).([]any); ok {
			messages := make([]any, 0, len(choices))
			for _, choice := range choices {
				if choiceMap, ok := threadMap(choice); ok && choiceMap["message"] != nil {
					messages = append(messages, choiceMap["message"])
				}
			}
			return messages
		}
		if object["message"] != nil {
			return []any{object["message"]}
		}
		if object["role"] != nil {
			return []any{object}
		}
	}
	return nil
}

func normalizeChatMessage(raw any, index int) (ThreadMessage, *ThreadInstruction, bool) {
	object, ok := threadMap(decodeThreadValue(raw))
	if !ok {
		return ThreadMessage{}, nil, false
	}
	role := threadString(object["role"])
	if role == "" {
		return ThreadMessage{}, nil, false
	}
	content := threadParts(object["content"])
	if role == "system" || role == "developer" {
		return ThreadMessage{}, &ThreadInstruction{Role: role, Content: content}, true
	}
	if role != "user" && role != "assistant" && role != "tool" {
		return ThreadMessage{}, nil, false
	}
	message := ThreadMessage{Index: index, Role: role, Name: threadString(object["name"]), Content: content, ToolCallID: threadString(object["tool_call_id"])}
	if role == "assistant" {
		message.Reasoning = recordedReasoning(object)
		message.ToolCalls = chatToolCalls(object["tool_calls"])
	}
	return message, nil, true
}

func firstUsableChatValue(span map[string]any, primary, fallback string, decode func(any) []any) any {
	if value, ok := threadLookup(span, primary); ok && len(decode(value)) > 0 {
		return value
	}
	if value, ok := threadLookup(span, fallback); ok && len(decode(value)) > 0 {
		return value
	}
	return nil
}

func usableResponsesValue(value any) bool {
	return len(responseItems(value)) > 0 || hasUnavailableThreadCount(value)
}
func hasUnavailableThreadCount(value any) bool { _, ok := unavailableThreadCount(value); return ok }
func usableThreadParts(value any) bool {
	for _, part := range threadParts(value) {
		if part.Text != "" || part.Type == "json" || part.Type == "unavailable" || part.Type == "unsupported" {
			return true
		}
	}
	return false
}

func responseItems(value any) []any {
	value = decodeThreadValue(value)
	if items, ok := value.([]any); ok {
		return items
	}
	if object, ok := threadMap(value); ok {
		if items, ok := decodeThreadValue(object["items"]).([]any); ok {
			return items
		}
	}
	return nil
}

func (thread *Thread) appendResponseItem(raw any, index int, pending []ThreadPart, toolNames map[string]string) []ThreadPart {
	item, ok := threadMap(decodeThreadValue(raw))
	if !ok {
		return pending
	}
	switch threadString(item["type"]) {
	case "reasoning":
		return append(pending, responseReasoning(item)...)
	case "message":
		role := threadString(item["role"])
		if role == "" {
			role = "assistant"
		}
		if role == "system" || role == "developer" {
			thread.Instructions = append(thread.Instructions, ThreadInstruction{Role: role, Content: threadParts(item["content"])})
			return pending
		}
		message := ThreadMessage{Index: index, Role: role, Name: threadString(item["name"]), Content: threadParts(item["content"]), ToolCallID: threadString(item["call_id"])}
		if role == "assistant" {
			message.Reasoning = pending
			pending = nil
		}
		thread.Messages = append(thread.Messages, message)
	case "function_call":
		call := responseToolCall(item)
		if toolNames != nil {
			toolNames[call.ID] = call.Name
		}
		thread.Messages = append(thread.Messages, ThreadMessage{Index: index, Role: "assistant", Content: []ThreadPart{}, Reasoning: pending, ToolCalls: []ThreadToolCall{call}})
		return nil
	case "function_call_output":
		callID := threadString(item["call_id"])
		name := threadString(item["name"])
		if name == "" && toolNames != nil {
			name = toolNames[callID]
		}
		content := item["output"]
		if content == nil {
			content = item["content"]
		}
		thread.Messages = append(thread.Messages, ThreadMessage{Index: index, Role: "tool", Name: name, ToolCallID: callID, Content: threadParts(content)})
	case "error", "exception":
		role := threadString(item["role"])
		if role != "user" && role != "assistant" && role != "tool" {
			role = "assistant"
		}
		content := item["content"]
		if content == nil {
			content = item[threadString(item["type"])]
		}
		thread.Messages = append(thread.Messages, ThreadMessage{Index: index, Role: role, Content: threadErrorParts(content, threadString(item["type"]))})
	}
	return pending
}

func responseToolCall(item map[string]any) ThreadToolCall {
	arguments := item["arguments"]
	if raw, ok := arguments.(string); ok {
		arguments = decodeJSONOrString(raw)
	}
	return ThreadToolCall{ID: firstThreadString(item["call_id"], item["id"]), Name: threadString(item["name"]), Arguments: arguments}
}

func chatToolCalls(value any) []ThreadToolCall {
	items, _ := decodeThreadValue(value).([]any)
	if len(items) == 0 {
		return nil
	}
	calls := make([]ThreadToolCall, 0, len(items))
	for _, raw := range items {
		item, ok := threadMap(raw)
		if !ok {
			continue
		}
		function, _ := threadMap(item["function"])
		name, arguments := threadString(item["name"]), item["arguments"]
		if function != nil {
			if name == "" {
				name = threadString(function["name"])
			}
			if arguments == nil {
				arguments = function["arguments"]
			}
		}
		if rawArguments, ok := arguments.(string); ok {
			arguments = decodeJSONOrString(rawArguments)
		}
		calls = append(calls, ThreadToolCall{ID: firstThreadString(item["id"], item["call_id"]), Name: name, Arguments: arguments})
	}
	return calls
}

func responseReasoning(item map[string]any) []ThreadPart {
	if states := stateThreadParts(item); len(states) > 0 {
		return states
	}
	var parts []ThreadPart
	for _, key := range []string{"content", "summary", "summaries", "reasoning", "thinking"} {
		if value := item[key]; value != nil {
			parsed := reasoningThreadParts(value)
			if key == "summary" || key == "summaries" {
				for index := range parsed {
					if parsed[index].Type == "text" {
						parsed[index].Type = "summary"
					}
				}
			}
			parts = append(parts, parsed...)
		}
	}
	return append(parts, stateThreadParts(item)...)
}

func recordedReasoning(message map[string]any) []ThreadPart {
	if states := stateThreadParts(message); len(states) > 0 {
		return states
	}
	var parts []ThreadPart
	for _, key := range []string{"reasoning_content", "reasoning", "thinking", "summary", "summaries", "reasoning_summary"} {
		if value := message[key]; value != nil {
			parsed := reasoningThreadParts(value)
			if key == "summary" || key == "summaries" || key == "reasoning_summary" {
				parsed = markThreadPartsSummary(parsed)
			}
			parts = append(parts, parsed...)
		}
	}
	return append(parts, stateThreadParts(message)...)
}

func reasoningThreadParts(value any) []ThreadPart {
	if object, ok := threadMap(value); ok {
		if states := stateThreadParts(object); len(states) > 0 {
			return states
		}
	}
	switch typed := decodeThreadValue(value).(type) {
	case string:
		return []ThreadPart{{Type: "text", Text: typed}}
	case []any:
		var parts []ThreadPart
		for _, item := range typed {
			parts = append(parts, reasoningThreadParts(item)...)
		}
		return parts
	case map[string]any:
		var parts []ThreadPart
		for _, key := range []string{"content", "text"} {
			if nested := typed[key]; nested != nil {
				parts = append(parts, reasoningThreadParts(nested)...)
			}
		}
		for _, key := range []string{"summary", "summaries"} {
			if nested := typed[key]; nested != nil {
				parts = append(parts, markThreadPartsSummary(reasoningThreadParts(nested))...)
			}
		}
		return append(parts, stateThreadParts(typed)...)
	default:
		return nil
	}
}

func markThreadPartsSummary(parts []ThreadPart) []ThreadPart {
	for index := range parts {
		if parts[index].Type == "text" {
			parts[index].Type = "summary"
		}
	}
	return parts
}

func threadErrorParts(value any, kind string) []ThreadPart {
	switch typed := decodeThreadValue(value).(type) {
	case string:
		return []ThreadPart{{Type: kind, Text: typed}}
	case []any:
		var parts []ThreadPart
		for _, item := range typed {
			parts = append(parts, threadErrorParts(item, kind)...)
		}
		return parts
	case map[string]any:
		for _, key := range []string{"message", "text", "content"} {
			if nested := typed[key]; nested != nil {
				return threadErrorParts(nested, kind)
			}
		}
	}
	return nil
}

func stateThreadParts(object map[string]any) []ThreadPart {
	states := []struct{ key, state string }{{"encrypted", "encrypted"}, {"redacted", "redacted"}, {"masked", "masked"}, {"truncated", "truncated"}, {"signature", "redacted"}}
	var parts []ThreadPart
	seen := map[string]bool{}
	for _, candidate := range states {
		for key, value := range object {
			lowerKey := strings.ToLower(key)
			byKey := strings.Contains(lowerKey, candidate.key) && threadStateValuePresent(value)
			byDiscriminator := (lowerKey == "type" || lowerKey == "state" || lowerKey == "status") && strings.Contains(strings.ToLower(threadString(value)), candidate.key)
			if byKey || byDiscriminator {
				if !seen[candidate.state] {
					parts = append(parts, ThreadPart{Type: "state", State: candidate.state})
					seen[candidate.state] = true
				}
				break
			}
		}
	}
	return parts
}

func threadStateValuePresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return strings.TrimSpace(typed) != ""
	default:
		return true
	}
}

func threadParts(value any) []ThreadPart {
	if object, ok := threadMap(value); ok {
		if states := stateThreadParts(object); len(states) > 0 {
			return states
		}
	}
	if count, unavailable := unavailableThreadCount(value); unavailable {
		return []ThreadPart{{Type: "unavailable", Count: count}}
	}
	value = decodeThreadValue(value)
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return []ThreadPart{{Type: "text", Text: typed}}
	case []any:
		var parts []ThreadPart
		for _, item := range typed {
			parts = append(parts, threadParts(item)...)
		}
		return parts
	case map[string]any:
		kind := threadString(typed["type"])
		if kind == "" {
			if text, ok := typed["text"]; ok {
				return threadParts(text)
			}
			return []ThreadPart{{Type: "json", Value: typed}}
		}
		switch kind {
		case "text", "input_text", "output_text", "summary_text", "refusal":
			return threadParts(typed["text"])
		default:
			return []ThreadPart{{Type: "unsupported", UnsupportedType: kind}}
		}
	default:
		return []ThreadPart{{Type: "json", Value: typed}}
	}
}

func threadLookup(span map[string]any, key string) (any, bool) {
	if value, ok := span[key]; ok {
		return value, true
	}
	if attrs, ok := threadMap(span["attributes"]); ok {
		if value, ok := attrs[key]; ok {
			return value, true
		}
	}
	parts := strings.Split(key, ".")
	for _, root := range []map[string]any{span, mapThreadValue(span["attributes"])} {
		var current any = root
		found := true
		for _, part := range parts {
			object, ok := threadMap(current)
			if !ok {
				found = false
				break
			}
			current, ok = object[part]
			if !ok {
				found = false
				break
			}
		}
		if found {
			return current, true
		}
	}
	return nil, false
}

func mapThreadValue(value any) map[string]any { object, _ := threadMap(value); return object }
func threadMap(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}
func threadString(value any) string { text, _ := decodeThreadValue(value).(string); return text }
func firstThreadString(values ...any) string {
	for _, value := range values {
		if text := threadString(value); text != "" {
			return text
		}
	}
	return ""
}

func decodeThreadValue(value any) any {
	switch typed := value.(type) {
	case string:
		return decodeJSONOrString(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = decodeThreadValue(item)
		}
		return result
	case map[string]any:
		if len(stateThreadParts(typed)) > 0 {
			return typed
		}
		if wrapped, ok := typed["_value"]; ok {
			return decodeThreadValue(wrapped)
		}
		if wrapped, ok := typed["string"]; ok {
			return decodeThreadValue(wrapped)
		}
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = decodeThreadValue(item)
		}
		return result
	default:
		return value
	}
}

func decodeJSONOrString(text string) any {
	trimmed := strings.TrimSpace(text)
	if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) || (strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
		var value any
		if json.Unmarshal([]byte(trimmed), &value) == nil {
			return decodeThreadValue(value)
		}
	}
	return text
}

func unavailableThreadCount(value any) (int, bool) {
	if text, ok := value.(string); ok {
		decoded := decodeJSONOrString(text)
		if decodedText, unchanged := decoded.(string); unchanged && decodedText == text {
			return 0, false
		}
		return unavailableThreadCount(decoded)
	}
	object, ok := threadMap(value)
	if !ok {
		return 0, false
	}
	if wrapped, ok := object["_value"]; ok {
		return unavailableThreadCount(wrapped)
	}
	if wrapped, ok := object["string"]; ok {
		return unavailableThreadCount(wrapped)
	}
	items, ok := threadMap(object["items"])
	if !ok {
		return 0, false
	}
	count, ok := items["count"]
	if !ok {
		return 0, false
	}
	switch value := count.(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	case string:
		number, err := strconv.Atoi(value)
		return number, err == nil
	}
	return 0, false
}
func splitSlice(value string) [2]string {
	parts := strings.SplitN(value, ":", 2)
	return [2]string{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])}
}
func parseSliceInteger(value string) (int, error) { return strconv.Atoi(strings.TrimSpace(value)) }
func clampSliceBound(value, length int) int {
	if value < 0 {
		return 0
	}
	if value > length {
		return length
	}
	return value
}
