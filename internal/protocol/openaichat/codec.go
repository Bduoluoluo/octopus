package openaichat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func DecodeRequest(body []byte) (*model.InternalLLMRequest, error) {
	var request Request
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	result := request.ToLLMRequest()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	if raw := fields["response_format"]; len(raw) > 0 && string(raw) != "null" {
		result.ResponseFormat = &model.ResponseFormat{}
		if err := json.Unmarshal(raw, result.ResponseFormat); err != nil {
			return nil, err
		}
	}
	result.RawAPIFormat = model.APIFormatOpenAIChatCompletion
	result.Prediction = request.Prediction
	result.WebSearchOptions = request.WebSearchOptions
	result.N = request.N
	if request.Audio != nil {
		result.Audio = request.Audio
	}
	return result, nil
}

func EncodeRequest(ctx context.Context, request *model.InternalLLMRequest) ([]byte, error) {
	if request == nil {
		return nil, fmt.Errorf("request is nil")
	}
	if err := validateExtensions(request.ProviderExtensions); err != nil {
		return nil, err
	}
	if err := validateRawResponses(request.OpenAIRawInputItems()); err != nil {
		return nil, err
	}
	for _, tool := range request.Tools {
		if tool.Type != "function" && request.RawAPIFormat != model.APIFormatOpenAIChatCompletion && (tool.ProviderExtensions == nil || tool.ProviderExtensions.OpenAIChat == nil) {
			return nil, fmt.Errorf("cannot represent tool %q in Chat Completions", tool.Type)
		}
	}
	for _, message := range request.Messages {
		if err := validateExtensions(message.ProviderExtensions); err != nil {
			return nil, err
		}
		for _, part := range message.Content.MultipleContent {
			if part.Native != nil || part.Document != nil || part.ServerToolUse != nil || part.ServerToolResult != nil {
				return nil, fmt.Errorf("cannot represent %q content in Chat Completions", part.Type)
			}
			switch part.Type {
			case "text", "input_text", "output_text", "image_url", "input_audio", "file", "video_url":
			default:
				if request.RawAPIFormat != model.APIFormatOpenAIChatCompletion {
					return nil, fmt.Errorf("cannot represent %q content in Chat Completions", part.Type)
				}
			}
		}
	}
	wire := RequestFromLLM(ctx, request, ReasoningFieldAll)
	var err error
	wire.Fields, err = preserveReasoningItems(wire.Fields, request.ProviderExtensions, request.OpenAIRawInputItems())
	if err != nil {
		return nil, err
	}
	for index, message := range request.Messages {
		if request.RawAPIFormat != "" && request.RawAPIFormat != model.APIFormatOpenAIChatCompletion {
			preserveForeignSignature(&wire.Messages[index])
		}
		wire.Messages[index].Fields, err = preserveReasoningItems(wire.Messages[index].Fields, message.ProviderExtensions, nil)
		if err != nil {
			return nil, err
		}
	}
	wire.Prediction = request.Prediction
	wire.WebSearchOptions = request.WebSearchOptions
	wire.N = request.N
	if request.ResponseFormat != nil {
		raw, err := json.Marshal(request.ResponseFormat)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &wire.ResponseFormat); err != nil {
			return nil, err
		}
	}
	if request.Thinking != nil {
		wire.Thinking = &Thinking{Type: request.Thinking.Type}
	}
	if request.Audio != nil {
		wire.Audio = &RequestAudio{Format: request.Audio.Format, Voice: request.Audio.Voice}
	}
	return json.Marshal(wire)
}

func DecodeResponse(body []byte) (*model.InternalLLMResponse, error) {
	if err := parseStreamError(model.StreamFrame{Data: body}); err != nil {
		return nil, err
	}
	var response Response
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	return response.ToLLMResponse(), nil
}

func EncodeResponse(response *model.InternalLLMResponse) ([]byte, error) {
	if response == nil {
		return json.Marshal(nil)
	}
	if err := validateExtensions(response.ProviderExtensions); err != nil {
		return nil, err
	}
	if err := validateRawResponses(response.RawResponsesOutputItems); err != nil {
		return nil, err
	}
	for _, choice := range response.Choices {
		for _, message := range []*model.Message{choice.Message, choice.Delta} {
			if message == nil {
				continue
			}
			if err := validateExtensions(message.ProviderExtensions); err != nil {
				return nil, err
			}
			for _, part := range message.Content.MultipleContent {
				if part.Native != nil || part.Document != nil || part.ServerToolUse != nil || part.ServerToolResult != nil {
					return nil, fmt.Errorf("cannot represent %q response content in Chat Completions", part.Type)
				}
			}
		}
	}
	wire := ResponseFromLLM(response)
	var err error
	wire.Fields, err = preserveReasoningItems(wire.Fields, response.ProviderExtensions, response.RawResponsesOutputItems)
	if err != nil {
		return nil, err
	}
	for index, choice := range response.Choices {
		if len(response.RawResponsesOutputItems) > 0 || response.ProviderExtensions != nil && response.ProviderExtensions.OpenAIChat == nil && (response.ProviderExtensions.OpenAIResponses != nil || response.ProviderExtensions.Anthropic != nil) {
			preserveForeignSignature(wire.Choices[index].Message)
			preserveForeignSignature(wire.Choices[index].Delta)
		}
		if choice.Message != nil {
			wire.Choices[index].Message.Fields, err = preserveReasoningItems(wire.Choices[index].Message.Fields, choice.Message.ProviderExtensions, nil)
			if err != nil {
				return nil, err
			}
		}
		if choice.Delta != nil {
			wire.Choices[index].Delta.Fields, err = preserveReasoningItems(wire.Choices[index].Delta.Fields, choice.Delta.ProviderExtensions, nil)
			if err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(wire)
}
