package internal

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// AgentMessage separates runtime metadata from the raw LLM message payload.
type AgentMessage struct {
	Kind      string
	SendToLLM bool
	Persist   bool
	Message   Message
}

func WrapMessage(kind string, message Message, sendToLLM, persist bool) AgentMessage {
	return AgentMessage{Kind: kind, SendToLLM: sendToLLM, Persist: persist, Message: message}
}

func WrapMessages(kind string, messages []Message, sendToLLM, persist bool) []AgentMessage {
	wrapped := make([]AgentMessage, 0, len(messages))
	for _, message := range messages {
		wrapped = append(wrapped, WrapMessage(kind, message, sendToLLM, persist))
	}
	return wrapped
}

func AgentTextMessage(kind, role, content string, sendToLLM, persist bool) AgentMessage {
	return WrapMessage(kind, TextMessage(role, content), sendToLLM, persist)
}

func AgentToolResultMessage(kind, toolCallID, content string, sendToLLM, persist bool) AgentMessage {
	return WrapMessage(kind, ToolResultMessage(toolCallID, content), sendToLLM, persist)
}

func ToLLMMessages(messages []AgentMessage) []Message {
	result := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.SendToLLM {
			result = append(result, message.Message)
		}
	}
	return result
}

func PersistedMessages(messages []AgentMessage) []Message {
	result := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.Persist {
			result = append(result, message.Message)
		}
	}
	return result
}

// AgentEvent is the structured runtime event emitted by the loop.
type AgentEvent struct {
	Type        string         `json:"type"`
	RunID       string         `json:"run_id,omitempty"`
	Turn        int            `json:"turn,omitempty"`
	MessageKind string         `json:"message_kind,omitempty"`
	Message     *Message       `json:"message,omitempty"`
	ToolCall    *ToolCall      `json:"tool_call,omitempty"`
	ToolResult  string         `json:"tool_result,omitempty"`
	Blocked     bool           `json:"blocked,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// HookManager provides stable interception points around the loop.
type HookManager struct {
	TransformContext []func([]AgentMessage) ([]AgentMessage, error)
	BeforeToolCall   []func(ToolCall, []AgentMessage) (block bool, reason string, err error)
	AfterToolCall    []func(ToolCall, string, []AgentMessage) (string, error)
	BeforeFinish     []func(Message, []AgentMessage) (Message, error)
}

func DefaultHooks() *HookManager {
	return &HookManager{
		TransformContext: []func([]AgentMessage) ([]AgentMessage, error){compactContextForLLM},
		BeforeToolCall:   []func(ToolCall, []AgentMessage) (bool, string, error){blockDangerousToolCall},
		AfterToolCall:    []func(ToolCall, string, []AgentMessage) (string, error){addToolErrorHint},
		BeforeFinish:     []func(Message, []AgentMessage) (Message, error){ensureAssistantContent},
	}
}

func (h *HookManager) ApplyContext(messages []AgentMessage) ([]AgentMessage, error) {
	if h == nil {
		return messages, nil
	}
	current := messages
	for _, hook := range h.TransformContext {
		next, err := hook(current)
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func (h *HookManager) CheckToolCall(toolCall ToolCall, context []AgentMessage) (bool, string, error) {
	if h == nil {
		return false, "", nil
	}
	for _, hook := range h.BeforeToolCall {
		block, reason, err := hook(toolCall, context)
		if err != nil {
			return false, "", err
		}
		if block {
			if reason == "" {
				reason = fmt.Sprintf("tool %s blocked", toolCall.Function.Name)
			}
			return true, reason, nil
		}
	}
	return false, "", nil
}

func (h *HookManager) ApplyToolResult(toolCall ToolCall, result string, context []AgentMessage) (string, error) {
	if h == nil {
		return result, nil
	}
	current := result
	for _, hook := range h.AfterToolCall {
		next, err := hook(toolCall, current, context)
		if err != nil {
			return "", err
		}
		current = next
	}
	return current, nil
}

func (h *HookManager) ApplyBeforeFinish(message Message, context []AgentMessage) (Message, error) {
	if h == nil {
		return message, nil
	}
	current := message
	for _, hook := range h.BeforeFinish {
		next, err := hook(current, context)
		if err != nil {
			return Message{}, err
		}
		current = next
	}
	return current, nil
}

const defaultContextCharBudget = 40000
const defaultContextKeepTail = 8

func compactContextForLLM(messages []AgentMessage) ([]AgentMessage, error) {
	total := 0
	for _, message := range messages {
		total += approximateMessageSize(message)
	}
	if total <= defaultContextCharBudget || len(messages) <= defaultContextKeepTail {
		return messages, nil
	}

	keepTailStart := len(messages) - defaultContextKeepTail
	removed := make([]string, 0)
	filtered := make([]AgentMessage, 0, len(messages))
	for i, message := range messages {
		if i < keepTailStart && isContextDroppable(message) && total > defaultContextCharBudget {
			removed = append(removed, summarizeMessageForContext(message))
			total -= approximateMessageSize(message)
			continue
		}
		filtered = append(filtered, message)
	}
	if len(removed) == 0 {
		return filtered, nil
	}

	summary := "Earlier context compressed:\n- " + strings.Join(removed, "\n- ")
	entry := AgentTextMessage("context_compaction", "user", summary, true, false)
	return append([]AgentMessage{entry}, filtered...), nil
}

func blockDangerousToolCall(toolCall ToolCall, context []AgentMessage) (bool, string, error) {
	command, err := extractToolCommand(toolCall)
	if err != nil || command == "" {
		return false, "", err
	}
	segments := ParseChain(command)
	for _, segment := range segments {
		parts := tokenize(segment.Raw)
		if len(parts) == 0 {
			continue
		}
		name := parts[0]
		args := parts[1:]
		switch name {
		case "topic":
			if len(args) > 0 && args[0] == "checkout" {
				return true, "topic checkout is reserved for the user", nil
			}
		case "rm":
			if touchesProtectedPath(args) {
				return true, "refusing to remove a topic root or global topics root", nil
			}
		case "write", "mkdir":
			if touchesProtectedPath(args[:minInt(len(args), 1)]) {
				return true, "refusing to write directly to a protected root path", nil
			}
		case "cp", "mv":
			if touchesProtectedPath(args) {
				return true, "refusing to move or copy using a protected root path", nil
			}
		}
	}
	return false, "", nil
}

func addToolErrorHint(toolCall ToolCall, result string, context []AgentMessage) (string, error) {
	if !strings.HasPrefix(result, "[error]") {
		return result, nil
	}
	if strings.Contains(result, "unknown command") {
		return result + "\n[hint] run help to inspect available commands before retrying", nil
	}
	if strings.Contains(result, "usage:") {
		return result + "\n[hint] follow the usage line exactly, or run help for related commands", nil
	}
	return result, nil
}

func ensureAssistantContent(message Message, context []AgentMessage) (Message, error) {
	if message.Content == nil || strings.TrimSpace(*message.Content) == "" {
		fallback := "已完成当前步骤。如果你愿意，我可以继续下一步。"
		message.Content = &fallback
	}
	return message, nil
}

func extractToolCommand(toolCall ToolCall) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
		return "", fmt.Errorf("parse tool arguments: %w", err)
	}
	if toolCall.Function.Name != "run" && args.Command == "" {
		return toolCall.Function.Name, nil
	}
	if toolCall.Function.Name != "run" && args.Command != "" && !strings.HasPrefix(args.Command, toolCall.Function.Name) {
		return toolCall.Function.Name + " " + args.Command, nil
	}
	return strings.TrimSpace(args.Command), nil
}

func touchesProtectedPath(args []string) bool {
	for _, arg := range args {
		if isProtectedPathArg(arg) {
			return true
		}
	}
	return false
}

func isProtectedPathArg(arg string) bool {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return false
	}
	clean := filepath.Clean(arg)
	switch clean {
	case ".", "..", "/":
		return true
	default:
		return false
	}
}

func isContextDroppable(message AgentMessage) bool {
	switch message.Kind {
	case "history_message", "history_summary", "history_summary_ack", "tool_result", "assistant":
		return true
	default:
		return false
	}
}

func summarizeMessageForContext(message AgentMessage) string {
	if message.Message.Role == "assistant" && len(message.Message.ToolCalls) > 0 {
		parts := make([]string, 0, len(message.Message.ToolCalls))
		for _, toolCall := range message.Message.ToolCalls {
			parts = append(parts, toolCall.Function.Name)
		}
		return "assistant used tools: " + strings.Join(parts, ", ")
	}
	if message.Message.Content != nil {
		text := strings.TrimSpace(*message.Message.Content)
		text = strings.ReplaceAll(text, "\n", " ")
		if len([]rune(text)) > 120 {
			text = truncate(text, 120)
		}
		if text != "" {
			return fmt.Sprintf("%s: %s", message.Message.Role, text)
		}
	}
	return fmt.Sprintf("%s message", message.Message.Role)
}

func approximateMessageSize(message AgentMessage) int {
	size := len(message.Kind) + len(message.Message.Role)
	if message.Message.Content != nil {
		size += len(*message.Message.Content)
	}
	if message.Message.Reasoning != nil {
		size += len(*message.Message.Reasoning)
	}
	for _, toolCall := range message.Message.ToolCalls {
		size += len(toolCall.Function.Name) + len(toolCall.Function.Arguments)
	}
	return size
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
