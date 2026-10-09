package outbound

import (
	"encoding/json"
	"fmt"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func validateResponsesRequest(request *model.InternalLLMRequest) error {
	if request.N != nil && *request.N != 1 {
		return fmt.Errorf("responses protocol does not support n=%d", *request.N)
	}
	for _, tool := range request.Tools {
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
