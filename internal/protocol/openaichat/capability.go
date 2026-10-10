package openaichat

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func validateNativeItems(items []model.ProtocolItem) error {
	for _, item := range items {
		if item.Format == model.APIFormatOpenAIChatCompletion {
			continue
		}
		if err := validateNativeItem(item.Format, item.Raw); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeItem(format model.APIFormat, raw json.RawMessage) error {
	var item struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	switch format {
	case model.APIFormatOpenAIResponse:
		switch item.Type {
		case "", "message":
			content := bytes.TrimSpace(item.Content)
			if len(content) > 0 && content[0] == '[' {
				var parts []json.RawMessage
				if err := json.Unmarshal(content, &parts); err != nil {
					return err
				}
				for _, part := range parts {
					var kind struct {
						Type string `json:"type"`
					}
					if err := json.Unmarshal(part, &kind); err != nil {
						return err
					}
					switch kind.Type {
					case "output_text", "input_text", "refusal", "input_image", "input_audio", "input_file":
					default:
						return fmt.Errorf("cannot represent Responses %q content in Chat Completions", kind.Type)
					}
				}
			}
		case "function_call", "function_call_output", "reasoning":
		default:
			return fmt.Errorf("cannot represent Responses %q item in Chat Completions", item.Type)
		}
	case model.APIFormatAnthropicMessage:
		switch item.Type {
		case "text", "image", "tool_use", "tool_result", "thinking", "redacted_thinking":
		default:
			return fmt.Errorf("cannot represent Anthropic %q block in Chat Completions", item.Type)
		}
	default:
		return fmt.Errorf("cannot represent native %s content in Chat Completions", format)
	}
	return nil
}

func preserveReasoningItems(fields model.ProtocolFields, extension *model.ProviderExtensions, raw json.RawMessage) (model.ProtocolFields, error) {
	var items []model.ProtocolItem
	if extension != nil {
		if extension.OpenAIResponses != nil && len(raw) == 0 {
			items = append(items, extension.OpenAIResponses.Items...)
		}
		if extension.Anthropic != nil {
			items = append(items, extension.Anthropic.Items...)
		}
	}
	if len(raw) > 0 {
		var output []json.RawMessage
		if err := json.Unmarshal(raw, &output); err != nil {
			return nil, err
		}
		for position, item := range output {
			items = append(items, model.ProtocolItem{Format: model.APIFormatOpenAIResponse, Position: position, Raw: item})
		}
	}
	var reasoning []model.ProtocolItem
	for _, item := range items {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item.Raw, &kind); err != nil {
			return nil, err
		}
		if kind.Type == "reasoning" || kind.Type == "thinking" || kind.Type == "redacted_thinking" {
			reasoning = append(reasoning, item)
		}
	}
	if len(reasoning) == 0 {
		return fields, nil
	}
	encoded, err := json.Marshal(reasoning)
	if err != nil {
		return nil, err
	}
	result := make(model.ProtocolFields, len(fields)+1)
	for key, value := range fields {
		result[key] = value
	}
	result["reasoning_items"] = encoded
	return result, nil
}

func preserveForeignSignature(message *Message) {
	if message == nil || message.ReasoningSignature == nil {
		return
	}
	fields := make(model.ProtocolFields, len(message.Fields)+1)
	for key, value := range message.Fields {
		fields[key] = value
	}
	fields["reasoning_metadata"], _ = json.Marshal(struct {
		Signature string `json:"signature"`
	}{Signature: *message.ReasoningSignature})
	message.Fields = fields
	message.ReasoningSignature = nil
}

func validateExtensions(extension *model.ProviderExtensions) error {
	if extension == nil {
		return nil
	}
	if extension.OpenAIResponses != nil {
		if err := validateNativeItems(extension.OpenAIResponses.Items); err != nil {
			return err
		}
	}
	if extension.Anthropic != nil {
		if err := validateNativeItems(extension.Anthropic.Items); err != nil {
			return err
		}
	}
	return nil
}

func validateRawResponses(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	for _, item := range items {
		if err := validateNativeItem(model.APIFormatOpenAIResponse, item); err != nil {
			return err
		}
	}
	return nil
}
