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
	if decoder.done {
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			return nil, nil
		}
		return nil, fmt.Errorf("Chat event after terminal marker")
	}
	if bytes.Equal(data, []byte("[DONE]")) {
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
		if decoder.choices[choice.Index] && choice.Delta != nil && (choice.Delta.Content.Content != nil || len(choice.Delta.ToolCalls) > 0) {
			return nil, fmt.Errorf("Chat choice %d received data after finish", choice.Index)
		}
		decoder.choices[choice.Index] = decoder.choices[choice.Index] || finished
	}
	events := model.StreamEventsFromInternalResponse(response)
	for index := range events {
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
		Event     string          `json:"event"`
		Error     json.RawMessage `json:"error"`
		RequestID string          `json:"request_id"`
		Data      *struct {
			Error     json.RawMessage `json:"error"`
			RequestID string          `json:"request_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("invalid Chat frame: %w", err)
	}
	if len(envelope.Error) == 0 && envelope.Data != nil {
		envelope.Error = envelope.Data.Error
	}
	isError := frame.Event == "error" || envelope.Event == "error"
	if (len(envelope.Error) == 0 || bytes.Equal(envelope.Error, []byte("null"))) && !isError {
		return nil
	}
	var wire struct {
		Code      json.RawMessage `json:"code"`
		Message   string          `json:"message"`
		Type      string          `json:"type"`
		Param     string          `json:"param"`
		RequestID string          `json:"request_id"`
	}
	if len(envelope.Error) > 0 {
		if err := json.Unmarshal(envelope.Error, &wire); err != nil {
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
	if envelope.RequestID != "" {
		wire.RequestID = envelope.RequestID
	}
	return &model.ResponseError{Detail: model.ErrorDetail{Code: code, Message: wire.Message, Type: wire.Type, Param: wire.Param, RequestID: wire.RequestID}}
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
			return nil, fmt.Errorf("cannot represent another protocol's opaque signature in Chat Completions")
		}
		if event.NativeItem != nil {
			if err := validateNativeItems([]model.ProtocolItem{*event.NativeItem}); err != nil {
				return nil, err
			}
		}
		if event.Kind == model.StreamEventKindNativeItem {
			return nil, fmt.Errorf("cannot represent native protocol event in Chat Completions")
		}
		if event.Kind == model.StreamEventKindError {
			if event.Error != nil {
				return nil, event.Error
			}
			continue
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
		if encoder.done {
			return nil, fmt.Errorf("Chat output after terminal marker")
		}
		if event.Kind == model.StreamEventKindCitationDelta && event.Citation != nil {
			if event.Citation.URL == nil {
				return nil, fmt.Errorf("cannot represent document citation in Chat Completions")
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
