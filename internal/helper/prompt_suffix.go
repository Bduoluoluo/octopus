package helper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func ApplyPromptSuffix(request *http.Request, channelType outbound.OutboundType, suffix string) error {
	if request == nil || request.Body == nil || suffix == "" || !outbound.IsChatChannelType(channelType) {
		return nil
	}

	body, err := io.ReadAll(request.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}
	modified, _, err := AppendPromptSuffixJSON(body, channelType, suffix)
	if err != nil {
		modified = body
	}
	request.Body = io.NopCloser(bytes.NewReader(modified))
	request.ContentLength = int64(len(modified))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(modified)), nil
	}
	return err
}

func AppendPromptSuffixJSON(body []byte, channelType outbound.OutboundType, suffix string) ([]byte, bool, error) {
	if len(body) == 0 || suffix == "" || !outbound.IsChatChannelType(channelType) {
		return body, false, nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, fmt.Errorf("failed to decode request for prompt suffix: %w", err)
	}

	var (
		modified bool
		err      error
	)
	switch channelType {
	case outbound.OutboundTypeAnthropic:
		modified, err = appendMessagesPromptSuffix(payload, suffix, "text")
	case outbound.OutboundTypeOpenAIResponse:
		modified, err = appendResponsesPromptSuffix(payload, suffix)
	case outbound.OutboundTypeOpenAIChat:
		if _, exists := payload["messages"]; exists {
			modified, err = appendMessagesPromptSuffix(payload, suffix, "text")
		} else {
			modified, err = appendResponsesPromptSuffix(payload, suffix)
		}
	}
	if err != nil {
		return nil, false, err
	}
	if !modified {
		return body, false, nil
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("failed to encode request with prompt suffix: %w", err)
	}
	return result, true, nil
}

func appendMessagesPromptSuffix(payload map[string]json.RawMessage, suffix, textType string) (bool, error) {
	rawMessages, ok := payload["messages"]
	if !ok {
		return false, nil
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return false, fmt.Errorf("failed to decode messages for prompt suffix: %w", err)
	}
	for index := len(messages) - 1; index >= 0; index-- {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(messages[index], &message); err != nil {
			return false, fmt.Errorf("failed to decode message for prompt suffix: %w", err)
		}
		var role string
		_ = json.Unmarshal(message["role"], &role)
		if strings.EqualFold(strings.TrimSpace(role), "user") {
			updated, changed, err := appendMessageContent(message, suffix, textType)
			if err != nil {
				return false, err
			}
			if changed {
				messages[index] = updated
				payload["messages"], err = json.Marshal(messages)
				return true, err
			}
		}
	}
	return false, nil
}

func appendResponsesPromptSuffix(payload map[string]json.RawMessage, suffix string) (bool, error) {
	rawInput, ok := payload["input"]
	if !ok || bytes.Equal(bytes.TrimSpace(rawInput), []byte("null")) {
		return false, nil
	}

	var inputText string
	if err := json.Unmarshal(rawInput, &inputText); err == nil {
		inputText += suffix
		payload["input"], _ = json.Marshal(inputText)
		return true, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(rawInput, &items); err != nil {
		return false, fmt.Errorf("failed to decode Responses input for prompt suffix: %w", err)
	}
	for index := len(items) - 1; index >= 0; index-- {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(items[index], &item); err != nil {
			return false, fmt.Errorf("failed to decode Responses input item for prompt suffix: %w", err)
		}
		var role, itemType string
		_ = json.Unmarshal(item["role"], &role)
		_ = json.Unmarshal(item["type"], &itemType)
		if role == "user" && (itemType == "" || itemType == "message") {
			updated, changed, err := appendMessageContent(item, suffix, "input_text")
			if err != nil {
				return false, err
			}
			if changed {
				items[index] = updated
				payload["input"], err = json.Marshal(items)
				return true, err
			}
		}
	}
	return false, nil
}

func appendMessageContent(message map[string]json.RawMessage, suffix, textType string) ([]byte, bool, error) {
	rawContent, ok := message["content"]
	if !ok || bytes.Equal(bytes.TrimSpace(rawContent), []byte("null")) {
		return nil, false, nil
	}

	var text string
	if err := json.Unmarshal(rawContent, &text); err == nil {
		message["content"], _ = json.Marshal(text + suffix)
		result, marshalErr := json.Marshal(message)
		return result, true, marshalErr
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(rawContent, &parts); err != nil {
		return nil, false, fmt.Errorf("failed to decode message content for prompt suffix: %w", err)
	}
	if !hasPromptSuffixUserContent(parts) {
		return nil, false, nil
	}
	textPart, err := json.Marshal(map[string]string{"type": textType, "text": suffix})
	if err != nil {
		return nil, false, err
	}
	parts = append(parts, textPart)
	message["content"], _ = json.Marshal(parts)
	result, err := json.Marshal(message)
	return result, true, err
}

func hasPromptSuffixUserContent(parts []json.RawMessage) bool {
	for _, rawPart := range parts {
		var part struct {
			Type             string          `json:"type"`
			Thought          bool            `json:"thought"`
			FunctionCall     json.RawMessage `json:"functionCall"`
			FunctionResponse json.RawMessage `json:"functionResponse"`
		}
		if json.Unmarshal(rawPart, &part) != nil || bytes.Equal(bytes.TrimSpace(rawPart), []byte("null")) {
			continue
		}
		switch part.Type {
		case "tool_result", "tool_use", "server_tool_use", "thinking", "redacted_thinking":
			continue
		}
		if strings.HasSuffix(part.Type, "_tool_result") || part.Thought || len(part.FunctionCall) > 0 || len(part.FunctionResponse) > 0 {
			continue
		}
		return true
	}
	return len(parts) == 0
}
