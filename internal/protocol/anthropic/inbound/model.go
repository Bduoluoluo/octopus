package inbound

import wire "github.com/xuanli27/octopus/internal/protocol/anthropic"

type MessageRequest = wire.MessageRequest
type AnthropicMetadata = wire.AnthropicMetadata
type SystemPrompt = wire.SystemPrompt
type SystemPromptPart = wire.SystemPromptPart
type Thinking = wire.Thinking
type OutputConfig = wire.OutputConfig
type ToolChoice = wire.ToolChoice
type Tool = wire.Tool
type CacheControl = wire.CacheControl
type InputSchema = wire.InputSchema
type MessageParam = wire.MessageParam
type MessageContent = wire.MessageContent
type MessageContentBlock = wire.MessageContentBlock
type ProviderExtensions = wire.ProviderExtensions
type GeminiExtension = wire.GeminiExtension
type DocumentCitationsControl = wire.DocumentCitationsControl
type ImageSource = wire.ImageSource
type StreamEvent = wire.StreamEvent
type StreamDelta = wire.StreamDelta
type StreamMessage = wire.StreamMessage
type Message = wire.Message
type ErrorDetail = wire.ErrorDetail
type AnthropicError = wire.AnthropicError
type Usage = wire.Usage
type CacheCreationUsage = wire.CacheCreationUsage

const (
	ThinkingTypeEnabled       = wire.ThinkingTypeEnabled
	ThinkingTypeDisabled      = wire.ThinkingTypeDisabled
	ThinkingTypeAdaptive      = wire.ThinkingTypeAdaptive
	EffortMax                 = wire.EffortMax
	EffortXHigh               = wire.EffortXHigh
	EffortHigh                = wire.EffortHigh
	EffortMedium              = wire.EffortMedium
	EffortLow                 = wire.EffortLow
	ThinkingDisplaySummarized = wire.ThinkingDisplaySummarized
	ThinkingDisplayOmitted    = wire.ThinkingDisplayOmitted
)
