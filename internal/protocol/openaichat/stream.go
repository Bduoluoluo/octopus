package openaichat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

type StreamDecoder struct {
	choices map[int]bool
	done    bool
	closed  bool
}

func (decoder *StreamDecoder) Push(ctx context.Context, frame model.StreamFrame) ([]model.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if decoder.closed {
		return nil, fmt.Errorf("Chat stream is closed")
	}
	data := bytes.TrimSpace(frame.Data)
	if streamError := parseStreamError(frame); streamError != nil {
		return nil, streamError
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		if decoder.done {
			return nil, nil
		}
		decoder.done = true
		return []model.StreamEvent{{Kind: model.StreamEventKindDone}}, nil
	}
	if len(data) == 0 {
		return nil, nil
	}
	response, err := DecodeResponse(data)
	if err != nil {
		return nil, fmt.Errorf("decode Chat stream: %w", err)
	}
	if decoder.choices == nil {
		decoder.choices = make(map[int]bool)
	}
	for index := range response.Choices {
		choice := &response.Choices[index]
		if choice.FinishReason != nil && *choice.FinishReason == "" {
			choice.FinishReason = nil
		}
		finished := choice.FinishReason != nil
		decoder.choices[choice.Index] = decoder.choices[choice.Index] || finished
		if choice.Delta != nil {
			for partIndex := range choice.Delta.Content.MultipleContent {
				part := &choice.Delta.Content.MultipleContent[partIndex]
				if len(part.Citations) > 0 {
					raw, err := json.Marshal(part.Citations)
					if err != nil {
						return nil, err
					}
					if part.ProviderExtensions == nil {
						part.ProviderExtensions = &model.ProviderExtensions{}
					}
					if part.ProviderExtensions.OpenAIChat == nil {
						part.ProviderExtensions.OpenAIChat = &model.ProtocolExtension{Fields: model.ProtocolFields{}}
					}
					part.ProviderExtensions.OpenAIChat.Fields["citations"] = raw
					part.Citations = nil
				}
			}
		}
	}
	events := model.StreamEventsFromInternalResponse(response)
	for index := range events {
		if events[index].Message != nil {
			for partIndex := range events[index].Message.Content.MultipleContent {
				part := &events[index].Message.Content.MultipleContent[partIndex]
				if raw := chatFields(part.ProviderExtensions)["citations"]; len(raw) > 0 && len(part.Citations) == 0 {
					if err := json.Unmarshal(raw, &part.Citations); err != nil {
						return nil, err
					}
				}
			}
		}
		if events[index].Kind == model.StreamEventKindMessageStop {
			for _, choice := range response.Choices {
				if choice.Index == events[index].Index && choice.FinishReason != nil {
					events[index].StopReason = model.FinishReason(*choice.FinishReason)
					break
				}
			}
		}
		if events[index].Kind == model.StreamEventKindSignatureDelta || events[index].Delta != nil && events[index].Delta.Signature != "" {
			events[index].ProviderExtensions = &model.ProviderExtensions{OpenAIChat: &model.ProtocolExtension{}}
		}
	}
	return events, nil
}

func (decoder *StreamDecoder) End(ctx context.Context) ([]model.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if decoder.done || decoder.closed {
		return nil, nil
	}
	if len(decoder.choices) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	for _, finished := range decoder.choices {
		if !finished {
			return nil, io.ErrUnexpectedEOF
		}
	}
	decoder.done = true
	return []model.StreamEvent{{Kind: model.StreamEventKindDone}}, nil
}

func (decoder *StreamDecoder) Close() error {
	decoder.choices = nil
	decoder.closed = true
	return nil
}

func parseStreamError(frame model.StreamFrame) error {
	data := bytes.TrimSpace(frame.Data)
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		if frame.Event == "error" {
			return &model.ResponseError{Detail: model.ErrorDetail{Message: "stream error", Type: "stream_error"}}
		}
		return nil
	}
	var envelope struct {
		Event     json.RawMessage `json:"event"`
		Type      json.RawMessage `json:"type"`
		Status    json.RawMessage `json:"status"`
		Error     json.RawMessage `json:"error"`
		RequestID json.RawMessage `json:"request_id"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("invalid Chat frame: %w", err)
	}
	var nested struct {
		Error     json.RawMessage `json:"error"`
		RequestID json.RawMessage `json:"request_id"`
	}
	nestedData := bytes.TrimSpace(envelope.Data)
	if len(nestedData) > 0 && nestedData[0] == '{' {
		if err := json.Unmarshal(nestedData, &nested); err != nil {
			return fmt.Errorf("invalid Chat error envelope: %w", err)
		}
	}
	if emptyError(envelope.Error) {
		envelope.Error = nested.Error
	}
	isError := frame.Event == "error" || rawString(envelope.Event) == "error" || rawString(envelope.Type) == "error" || rawString(envelope.Status) == "failed"
	if emptyError(envelope.Error) && !isError {
		return nil
	}
	var wire struct {
		Code      json.RawMessage `json:"code"`
		Message   string          `json:"message"`
		Type      string          `json:"type"`
		Param     string          `json:"param"`
		RequestID string          `json:"request_id"`
	}
	if !emptyError(envelope.Error) {
		if err := json.Unmarshal(envelope.Error, &wire); err != nil {
			if err := json.Unmarshal(envelope.Error, &wire.Message); err != nil {
				wire.Message = string(envelope.Error)
			}
		}
	} else if isError {
		if err := json.Unmarshal(data, &wire); err != nil {
			return fmt.Errorf("invalid Chat error envelope: %w", err)
		}
	}
	code := string(wire.Code)
	if len(wire.Code) > 0 && wire.Code[0] == '"' {
		if err := json.Unmarshal(wire.Code, &code); err != nil {
			return err
		}
	}
	if code == "null" {
		code = ""
	}
	if wire.Message == "" {
		wire.Message = "stream error"
	}
	if wire.Type == "" {
		wire.Type = "stream_error"
	}
	if requestID := rawString(envelope.RequestID); requestID != "" {
		wire.RequestID = requestID
	} else if requestID := rawString(nested.RequestID); requestID != "" {
		wire.RequestID = requestID
	}
	return &model.ResponseError{Detail: model.ErrorDetail{Code: code, Message: wire.Message, Type: wire.Type, Param: wire.Param, RequestID: wire.RequestID}}
}

func rawString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func emptyError(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	switch value := value.(type) {
	case nil:
		return true
	case map[string]any:
		return len(value) == 0
	case []any:
		return len(value) == 0
	default:
		return false
	}
}

type StreamEncoder struct {
	done       bool
	aggregator model.StreamAggregator
}

func (encoder *StreamEncoder) Push(ctx context.Context, events []model.StreamEvent) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	var pending []model.StreamEvent
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		response := model.InternalResponseFromStreamEvents(pending)
		pending = nil
		if response == nil {
			return nil
		}
		encoder.aggregator.Add(response)
		encoded, err := EncodeResponse(ChatResponseUsage(response))
		if err != nil {
			return err
		}
		output.WriteString("data: ")
		output.Write(encoded)
		output.WriteString("\n\n")
		return nil
	}
	for _, event := range events {
		if event.Delta != nil && event.Delta.Signature != "" && (event.ProviderExtensions == nil || event.ProviderExtensions.OpenAIChat == nil) {
			raw, err := json.Marshal(event)
			if err != nil {
				return nil, err
			}
			pending = append(pending, model.StreamEvent{Kind: model.StreamEventKindMessageDelta, ID: event.ID, Model: event.Model, Index: event.Index, Message: &model.Message{ProviderExtensions: chatExtensions(model.ProtocolFields{"reasoning_metadata": raw})}})
			delta := *event.Delta
			delta.Signature = ""
			event.Delta = &delta
			if event.Kind == model.StreamEventKindSignatureDelta {
				continue
			}
		}
		if event.Kind == model.StreamEventKindNativeItem && event.NativeItem != nil && event.NativeItem.Position < 0 {
			raw, err := json.Marshal([]model.ProtocolItem{*event.NativeItem})
			if err != nil {
				return nil, err
			}
			event.Kind = model.StreamEventKindMetadata
			event.ProviderExtensions = chatExtensions(model.ProtocolFields{"protocol_events": raw})
			event.NativeItem = nil
		}
		if event.NativeItem != nil {
			if err := validateNativeItems([]model.ProtocolItem{*event.NativeItem}); err != nil {
				return nil, err
			}
		}
		if event.Kind == model.StreamEventKindNativeItem {
			if event.NativeItem == nil {
				return nil, fmt.Errorf("native protocol event missing payload")
			}
			fields, err := preserveReasoningItems(nil, &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Items: []model.ProtocolItem{*event.NativeItem}}}, nil)
			if err != nil {
				return nil, err
			}
			if len(fields) == 0 {
				return nil, fmt.Errorf("cannot represent native protocol event in Chat Completions")
			}
			event.Kind = model.StreamEventKindMetadata
			event.ProviderExtensions = chatExtensions(fields)
			event.NativeItem = nil
		}
		if event.Kind == model.StreamEventKindError {
			if event.Error != nil {
				return nil, event.Error
			}
			return nil, &model.ResponseError{Detail: model.ErrorDetail{Message: "stream error", Type: "stream_error"}}
		}
		if event.Kind == model.StreamEventKindDone {
			if err := flush(); err != nil {
				return nil, err
			}
			if !encoder.done {
				output.WriteString("data: [DONE]\n\n")
				encoder.done = true
			}
			continue
		}
		if event.Kind == model.StreamEventKindCitationDelta && event.Citation != nil {
			if event.Citation.URL == nil {
				pending = append(pending, event)
				continue
			}
			event.Kind = model.StreamEventKindMessageDelta
			event.Message = &model.Message{Annotations: []model.Annotation{{Type: "url_citation", StartIndex: event.Citation.StartIndex, EndIndex: event.Citation.EndIndex, URLCitation: &model.URLCitation{URL: *event.Citation.URL}}}}
			if event.Citation.Title != nil {
				event.Message.Annotations[0].URLCitation.Title = *event.Citation.Title
			}
		}
		pending = append(pending, event)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (encoder *StreamEncoder) Response() *model.InternalLLMResponse {
	response := encoder.aggregator.BuildAndReset()
	encoder.done = false
	return response
}
func (encoder *StreamEncoder) Reset() { *encoder = StreamEncoder{} }

func ChatResponseUsage(response *model.InternalLLMResponse) *model.InternalLLMResponse {
	if response == nil || !response.Usage.HasAnthropicCacheSemantic() {
		return response
	}
	converted := *response
	usage := *response.Usage
	details := model.PromptTokensDetails{}
	if usage.PromptTokensDetails != nil {
		details = *usage.PromptTokensDetails
	}
	details.CachedTokens = usage.BillableCacheReadInput()
	usage.PromptTokens = usage.EffectiveInputTokens()
	usage.PromptTokensDetails = &details
	usage.CacheReadInputTokens = 0
	usage.CacheCreationInputTokens = 0
	usage.CacheCreation5mInputTokens = 0
	usage.CacheCreation1hInputTokens = 0
	converted.Usage = &usage
	return &converted
}
