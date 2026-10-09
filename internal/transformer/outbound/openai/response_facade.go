package openai

import (
	"encoding/json"

	codec "github.com/xuanli27/octopus/internal/protocol/openairesponses/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

type ResponseOutbound = codec.ResponseOutbound
type ResponsesRequest = codec.ResponsesRequest
type ResponsesInput = codec.ResponsesInput
type ResponsesItem = codec.ResponsesItem
type ResponsesInputAudio = codec.ResponsesInputAudio
type ResponsesReasoningSummary = codec.ResponsesReasoningSummary
type ResponsesAnnotation = codec.ResponsesAnnotation
type ResponsesTool = codec.ResponsesTool
type ResponsesToolChoice = codec.ResponsesToolChoice
type ResponsesTextOptions = codec.ResponsesTextOptions
type ResponsesTextFormat = codec.ResponsesTextFormat
type ResponsesReasoning = codec.ResponsesReasoning
type ResponsesResponse = codec.ResponsesResponse
type ResponsesUsage = codec.ResponsesUsage
type ResponsesError = codec.ResponsesError
type ResponsesStreamEvent = codec.ResponsesStreamEvent

func ConvertToResponsesRequest(request *model.InternalLLMRequest) *ResponsesRequest {
	return codec.ConvertToResponsesRequest(request)
}

func MarshalResponsesInputItems(messages []model.Message) (json.RawMessage, error) {
	return codec.MarshalResponsesInputItems(messages)
}
