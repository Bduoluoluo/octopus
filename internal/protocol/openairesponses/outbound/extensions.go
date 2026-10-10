package outbound

import (
	"encoding/json"
	"fmt"

	wire "github.com/xuanli27/octopus/internal/protocol/openairesponses"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func validateResponsesRequest(request *model.InternalLLMRequest) error {
	if request.RawAPIFormat == model.APIFormatOpenAIResponse && request.ProviderExtensions != nil && request.ProviderExtensions.OpenAIResponses != nil {
		return nil
	}
	if request.N != nil && *request.N != 1 {
		return fmt.Errorf("responses protocol does not support n=%d", *request.N)
	}
	if err := wire.ValidateContentExtensions(request.ProviderExtensions); err != nil {
		return err
	}
	for _, tool := range request.Tools {
		if err := wire.ValidateContentExtensions(tool.ProviderExtensions); err != nil {
			return err
		}
		if err := wire.ValidateContentExtensions(tool.Function.ProviderExtensions); err != nil {
			return err
		}
		if tool.Type != "function" && len(tool.AnthropicServerSpec) > 0 {
			return fmt.Errorf("cannot convert Anthropic server tool to Responses")
		}
		switch tool.Type {
		case "function":
			if len(tool.Function.Parameters) > 0 {
				var parameters map[string]json.RawMessage
				if err := json.Unmarshal(tool.Function.Parameters, &parameters); err != nil {
					return fmt.Errorf("invalid function parameters for %q: %w", tool.Function.Name, err)
				}
			}
		case "image_generation":
		default:
			if request.RawAPIFormat != model.APIFormatOpenAIResponse {
				return fmt.Errorf("cannot convert tool type %q to responses", tool.Type)
			}
		}
	}
	for _, message := range request.Messages {
		if err := wire.ValidateMessageContent(&message, request.RawAPIFormat == model.APIFormatOpenAIResponse); err != nil {
			return err
		}
		for _, part := range message.Content.MultipleContent {
			switch part.Type {
			case "text", "image_url", "file", "input_audio":
			default:
				if request.RawAPIFormat != model.APIFormatOpenAIResponse {
					return fmt.Errorf("cannot convert content type %q to responses", part.Type)
				}
			}
		}
	}
	return nil
}

func attachResponseRequestFields(result *ResponsesRequest, request *model.InternalLLMRequest) error {
	if request.RawAPIFormat != model.APIFormatOpenAIResponse || request.ProviderExtensions == nil || request.ProviderExtensions.OpenAIResponses == nil {
		return nil
	}
	extension := request.ProviderExtensions.OpenAIResponses
	result.Fields = extension.Fields
	if len(request.OpenAIRawInputItems()) == 0 {
		if raw, present := extension.Fields["input"]; present {
			var original ResponsesInput
			if err := json.Unmarshal(raw, &original); err != nil {
				return err
			}
			if len(original.Raw) > 0 || original.Text == nil && len(original.Items) == 0 {
				result.Input = ResponsesInput{Raw: append(json.RawMessage(nil), raw...)}
			}
		} else {
			result.Input = ResponsesInput{}
		}
	}
	if raw := extension.Fields["tools"]; len(raw) > 0 {
		var tools []ResponsesTool
		if err := json.Unmarshal(raw, &tools); err != nil {
			return err
		}
		result.Tools = tools
	}
	if raw := extension.Fields["tool_choice"]; len(raw) > 0 {
		var choice ResponsesToolChoice
		if err := json.Unmarshal(raw, &choice); err != nil {
			return err
		}
		result.ToolChoice = &choice
	}
	if raw := extension.Fields["reasoning"]; len(raw) > 0 && string(raw) != "null" {
		var reasoning wire.Reasoning
		if err := json.Unmarshal(raw, &reasoning); err != nil {
			return err
		}
		if result.Reasoning == nil {
			result.Reasoning = &wire.Reasoning{}
		}
		result.Reasoning.Context = reasoning.Context
		result.Reasoning.Fields = reasoning.Fields
	}
	if len(request.OpenAIRawInputItems()) > 0 {
		if raw := extension.Fields["instructions"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &result.Instructions); err != nil {
				return err
			}
		} else {
			result.Instructions = ""
		}
	}
	return nil
}
