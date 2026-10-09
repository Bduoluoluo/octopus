package openai

import (
	"context"

	"github.com/xuanli27/octopus/internal/protocol/openaichat"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

type ChatInbound struct {
	encoder        openaichat.StreamEncoder
	storedResponse *model.InternalLLMResponse
}

func (inbound *ChatInbound) TransformRequest(ctx context.Context, body []byte) (*model.InternalLLMRequest, error) {
	return openaichat.DecodeRequest(body)
}

func (inbound *ChatInbound) TransformResponse(ctx context.Context, response *model.InternalLLMResponse) ([]byte, error) {
	inbound.storedResponse = response
	return openaichat.EncodeResponse(chatResponseUsage(response))
}

func (inbound *ChatInbound) TransformStream(ctx context.Context, response *model.InternalLLMResponse) ([]byte, error) {
	return inbound.TransformStreamEvents(ctx, model.StreamEventsFromInternalResponse(response))
}

func chatResponseUsage(response *model.InternalLLMResponse) *model.InternalLLMResponse {
	return openaichat.ChatResponseUsage(response)
}

func (inbound *ChatInbound) TransformStreamEvents(ctx context.Context, events []model.StreamEvent) ([]byte, error) {
	return inbound.encoder.Push(ctx, events)
}

func (inbound *ChatInbound) GetInternalResponse(ctx context.Context) (*model.InternalLLMResponse, error) {
	if inbound.storedResponse != nil {
		return inbound.storedResponse, nil
	}
	return inbound.encoder.Response(), nil
}

func (inbound *ChatInbound) ResetStream() {
	inbound.storedResponse = nil
	inbound.encoder.Reset()
}
