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
		Type             string          `json:"type"`
		Signature        *string         `json:"signature"`
		EncryptedContent *string         `json:"encrypted_content"`
		Content          json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	if item.Signature != nil && *item.Signature != "" || item.EncryptedContent != nil && *item.EncryptedContent != "" {
		return fmt.Errorf("cannot represent opaque %s reasoning in Chat Completions", format)
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
		case "text", "image", "tool_use", "tool_result", "thinking":
		default:
			return fmt.Errorf("cannot represent Anthropic %q block in Chat Completions", item.Type)
		}
	default:
		return fmt.Errorf("cannot represent native %s content in Chat Completions", format)
	}
	return nil
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
