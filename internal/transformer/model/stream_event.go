package model

import (
	"encoding/json"
	"sort"
)

type StreamEventKind string

const (
	StreamEventKindMessageStart      StreamEventKind = "message_start"
	StreamEventKindContentBlockStart StreamEventKind = "content_block_start"
	StreamEventKindContentBlockStop  StreamEventKind = "content_block_stop"
	StreamEventKindTextDelta         StreamEventKind = "text_delta"
	StreamEventKindThinkingDelta     StreamEventKind = "thinking_delta"
	StreamEventKindSignatureDelta    StreamEventKind = "signature_delta"
	StreamEventKindToolCallStart     StreamEventKind = "tool_call_start"
	StreamEventKindToolCallDelta     StreamEventKind = "tool_call_delta"
	StreamEventKindToolCallStop      StreamEventKind = "tool_call_stop"
	StreamEventKindUsageDelta        StreamEventKind = "usage_delta"
	StreamEventKindMessageStop       StreamEventKind = "message_stop"
	StreamEventKindDone              StreamEventKind = "done"
	StreamEventKindError             StreamEventKind = "error"
	StreamEventKindCitationDelta     StreamEventKind = "citation_delta"
	StreamEventKindNativeItem        StreamEventKind = "native_item"
	StreamEventKindMetadata          StreamEventKind = "metadata"
	StreamEventKindMessageDelta      StreamEventKind = "message_delta"
)

type StreamEvent struct {
	ChoiceExtensions  *ProviderExtensions `json:"choice_extensions,omitempty"`
	Created           int64               `json:"created,omitempty"`
	SystemFingerprint string              `json:"system_fingerprint,omitempty"`
	ServiceTier       string              `json:"service_tier,omitempty"`
	Message           *Message            `json:"message,omitempty"`
	Logprobs          *LogprobsContent    `json:"logprobs,omitempty"`
	OutputIndex       *int                `json:"output_index,omitempty"`
	ContentIndex      *int                `json:"content_index,omitempty"`
	SequenceNumber    *int                `json:"sequence_number,omitempty"`
	ItemID            string              `json:"item_id,omitempty"`
	CallID            string              `json:"call_id,omitempty"`
	Citation          *ContentCitation    `json:"citation,omitempty"`
	NativeItem        *ProtocolItem       `json:"native_item,omitempty"`
	Status            string              `json:"status,omitempty"`
	IncompleteDetails json.RawMessage     `json:"incomplete_details,omitempty"`
	Kind              StreamEventKind     `json:"kind"`

	ID    string `json:"id,omitempty"`
	Model string `json:"model,omitempty"`
	Index int    `json:"index,omitempty"`
	Role  string `json:"role,omitempty"`

	ContentBlock *StreamContentBlock `json:"content_block,omitempty"`
	Delta        *StreamDelta        `json:"delta,omitempty"`
	ToolCall     *ToolCall           `json:"tool_call,omitempty"`
	Usage        *Usage              `json:"usage,omitempty"`
	StopReason   FinishReason        `json:"stop_reason,omitempty"`
	StopSequence *string             `json:"stop_sequence,omitempty"`
	Error        *ResponseError      `json:"error,omitempty"`

	ProviderExtensions *ProviderExtensions `json:"provider_extensions,omitempty"`
}

type StreamContentBlock struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
	Data string `json:"data,omitempty"`

	Input              json.RawMessage     `json:"input,omitempty"`
	ProviderExtensions *ProviderExtensions `json:"provider_extensions,omitempty"`
}

type StreamDelta struct {
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Refusal   string `json:"refusal,omitempty"`

	ProviderExtensions *ProviderExtensions `json:"provider_extensions,omitempty"`
}

func StreamEventsFromInternalResponse(response *InternalLLMResponse) []StreamEvent {
	if response == nil {
		return nil
	}
	if len(response.ProtocolEvents) > 0 {
		return append([]StreamEvent(nil), response.ProtocolEvents...)
	}
	if response.Object == "[DONE]" {
		return []StreamEvent{{Kind: StreamEventKindDone}}
	}
	if response.Error != nil {
		return []StreamEvent{{Kind: StreamEventKindError, ID: response.ID, Model: response.Model, Error: response.Error}}
	}
	events := make([]StreamEvent, 0, len(response.Choices)+1)
	if response.Created != 0 || response.SystemFingerprint != "" || response.ServiceTier != "" || response.ProviderExtensions != nil {
		events = append(events, StreamEvent{Kind: StreamEventKindMetadata, ID: response.ID, Model: response.Model, Created: response.Created, SystemFingerprint: response.SystemFingerprint, ServiceTier: response.ServiceTier, ProviderExtensions: response.ProviderExtensions})
	}
	for _, choice := range response.Choices {
		if choice.ProviderExtensions != nil {
			events = append(events, StreamEvent{Kind: StreamEventKindMessageDelta, ID: response.ID, Model: response.Model, Index: choice.Index, ChoiceExtensions: choice.ProviderExtensions})
		}
		if choice.Delta != nil {
			delta := choice.Delta
			if delta.Audio != nil || len(delta.Images) > 0 || len(delta.Annotations) > 0 || delta.ProviderExtensions != nil || choice.Logprobs != nil {
				events = append(events, StreamEvent{Kind: StreamEventKindMessageDelta, ID: response.ID, Model: response.Model, Index: choice.Index, Message: &Message{Audio: delta.Audio, Images: delta.Images, Annotations: delta.Annotations, ProviderExtensions: delta.ProviderExtensions}, Logprobs: choice.Logprobs})
			}
			if delta.Role != "" {
				events = append(events, StreamEvent{Kind: StreamEventKindMessageStart, ID: response.ID, Model: response.Model, Index: choice.Index, Role: delta.Role})
			}
			for _, block := range delta.ReasoningBlocks {
				switch block.Kind {
				case ReasoningBlockKindThinking:
					if block.Text != "" {
						events = append(events, StreamEvent{Kind: StreamEventKindThinkingDelta, ID: response.ID, Model: response.Model, Index: choice.Index, Delta: &StreamDelta{Thinking: block.Text, Signature: block.Signature}})
					} else if block.Signature != "" {
						events = append(events, StreamEvent{Kind: StreamEventKindSignatureDelta, ID: response.ID, Model: response.Model, Index: choice.Index, Delta: &StreamDelta{Signature: block.Signature}})
					}
				case ReasoningBlockKindSignature:
					if block.Signature != "" {
						events = append(events, StreamEvent{Kind: StreamEventKindSignatureDelta, ID: response.ID, Model: response.Model, Index: choice.Index, Delta: &StreamDelta{Signature: block.Signature}})
					}
				case ReasoningBlockKindRedacted:
					if block.Data != "" {
						events = append(events, StreamEvent{Kind: StreamEventKindContentBlockStart, ID: response.ID, Model: response.Model, Index: choice.Index, ContentBlock: &StreamContentBlock{Type: string(ReasoningBlockKindRedacted), Data: block.Data}})
						events = append(events, StreamEvent{Kind: StreamEventKindContentBlockStop, ID: response.ID, Model: response.Model, Index: choice.Index, ContentBlock: &StreamContentBlock{Type: string(ReasoningBlockKindRedacted)}})
					}
				}
			}
			if len(delta.ReasoningBlocks) == 0 {
				if reasoning := delta.GetReasoningContent(); reasoning != "" {
					events = append(events, StreamEvent{Kind: StreamEventKindThinkingDelta, ID: response.ID, Model: response.Model, Index: choice.Index, Delta: &StreamDelta{Thinking: reasoning}})
				}
				if delta.ReasoningSignature != nil && *delta.ReasoningSignature != "" {
					events = append(events, StreamEvent{Kind: StreamEventKindSignatureDelta, ID: response.ID, Model: response.Model, Index: choice.Index, Delta: &StreamDelta{Signature: *delta.ReasoningSignature}})
				}
				for _, data := range delta.RedactedThinkingBlocks {
					if data != "" {
						events = append(events, StreamEvent{Kind: StreamEventKindContentBlockStart, ID: response.ID, Model: response.Model, Index: choice.Index, ContentBlock: &StreamContentBlock{Type: string(ReasoningBlockKindRedacted), Data: data}})
						events = append(events, StreamEvent{Kind: StreamEventKindContentBlockStop, ID: response.ID, Model: response.Model, Index: choice.Index, ContentBlock: &StreamContentBlock{Type: string(ReasoningBlockKindRedacted)}})
					}
				}
			}
			if delta.Content.Content != nil && *delta.Content.Content != "" {
				events = append(events, StreamEvent{Kind: StreamEventKindTextDelta, ID: response.ID, Model: response.Model, Index: choice.Index, Delta: &StreamDelta{Text: *delta.Content.Content}})
			}
			if delta.Refusal != "" {
				events = append(events, StreamEvent{Kind: StreamEventKindTextDelta, ID: response.ID, Model: response.Model, Index: choice.Index, Delta: &StreamDelta{Refusal: delta.Refusal}})
			}
			for _, part := range delta.Content.MultipleContent {
				if len(part.Citations) == 0 {
					events = append(events, StreamEvent{Kind: StreamEventKindMessageDelta, ID: response.ID, Model: response.Model, Index: choice.Index, Message: &Message{Content: MessageContent{MultipleContent: []MessageContentPart{part}}}})
				}
				for index := range part.Citations {
					citation := part.Citations[index]
					events = append(events, StreamEvent{Kind: StreamEventKindCitationDelta, ID: response.ID, Model: response.Model, Index: choice.Index, Citation: &citation})
				}
			}
			for _, toolCall := range delta.ToolCalls {
				toolCall := toolCall
				event := StreamEvent{Kind: StreamEventKindToolCallDelta, ID: response.ID, Model: response.Model, Index: choice.Index, ToolCall: &toolCall}
				if toolCall.Function.Arguments != "" {
					event.Delta = &StreamDelta{Arguments: toolCall.Function.Arguments}
				}
				events = append(events, event)
			}
		}
		if choice.FinishReason != nil {
			event := StreamEvent{Kind: StreamEventKindMessageStop, ID: response.ID, Model: response.Model, Index: choice.Index, StopReason: ParseFinishReason(*choice.FinishReason), StopSequence: choice.StopSequence}
			if len(response.RawResponsesOutputItems) > 0 {
				event.ProviderExtensions = &ProviderExtensions{OpenAI: &OpenAIExtension{RawResponseItems: response.RawResponsesOutputItems}}
			}
			events = append(events, event)
		}
	}
	if response.Usage != nil {
		event := StreamEvent{Kind: StreamEventKindUsageDelta, ID: response.ID, Model: response.Model, Usage: response.Usage}
		if len(response.RawResponsesOutputItems) > 0 {
			event.ProviderExtensions = &ProviderExtensions{OpenAI: &OpenAIExtension{RawResponseItems: response.RawResponsesOutputItems}}
		}
		events = append(events, event)
	}
	return events
}

func InternalResponseFromStreamEvents(events []StreamEvent) *InternalLLMResponse {
	if len(events) == 0 {
		return nil
	}
	response := &InternalLLMResponse{Object: "chat.completion.chunk", ProtocolEvents: append([]StreamEvent(nil), events...)}
	choices := make(map[int]*Choice)
	for _, event := range events {
		if event.Kind == StreamEventKindDone {
			if len(events) == 1 {
				response.Object = "[DONE]"
			}
			continue
		}
		if event.Created != 0 {
			response.Created = event.Created
		}
		if event.SystemFingerprint != "" {
			response.SystemFingerprint = event.SystemFingerprint
		}
		if event.ServiceTier != "" {
			response.ServiceTier = event.ServiceTier
		}
		if event.Status != "" {
			response.Status = event.Status
		}
		if event.IncompleteDetails != nil {
			response.IncompleteDetails = cloneRawMessage(event.IncompleteDetails)
		}
		if event.ProviderExtensions != nil {
			response.ProviderExtensions = mergeStreamExtensions(response.ProviderExtensions, event.ProviderExtensions)
		}
		if event.ID != "" {
			response.ID = event.ID
		}
		if event.Model != "" {
			response.Model = event.Model
		}
		if event.Usage != nil {
			response.Usage = event.Usage
		}
		if event.ProviderExtensions != nil && event.ProviderExtensions.OpenAI != nil && len(event.ProviderExtensions.OpenAI.RawResponseItems) > 0 {
			response.RawResponsesOutputItems = event.ProviderExtensions.OpenAI.RawResponseItems
		}
		if event.Kind == StreamEventKindUsageDelta || event.Kind == StreamEventKindMetadata || event.Kind == StreamEventKindNativeItem {
			continue
		}
		if event.Kind == StreamEventKindError {
			response.Error = event.Error
			continue
		}
		choice := choices[event.Index]
		if choice == nil {
			choice = &Choice{Index: event.Index, Delta: &Message{}}
			choices[event.Index] = choice
		}
		switch event.Kind {
		case StreamEventKindMessageDelta:
			if event.ChoiceExtensions != nil {
				choice.ProviderExtensions = mergeStreamExtensions(choice.ProviderExtensions, event.ChoiceExtensions)
			}
			merged := Choice{Message: choice.Delta, Logprobs: choice.Logprobs}
			mergeChoiceDelta(&merged, Choice{Delta: event.Message, Logprobs: event.Logprobs})
			choice.Delta, choice.Logprobs = merged.Message, merged.Logprobs
		case StreamEventKindMessageStart:
			choice.Delta.Role = event.Role
		case StreamEventKindContentBlockStart:
			if event.ContentBlock != nil && event.ContentBlock.Type == string(ReasoningBlockKindRedacted) && event.ContentBlock.Data != "" {
				choice.Delta.RedactedThinkingBlocks = append(choice.Delta.RedactedThinkingBlocks, event.ContentBlock.Data)
				choice.Delta.AppendReasoningBlock(ReasoningBlock{Kind: ReasoningBlockKindRedacted, Index: -1, Data: event.ContentBlock.Data})
			}
		case StreamEventKindTextDelta:
			if event.Delta != nil {
				if event.Delta.Text != "" {
					text := event.Delta.Text
					if choice.Delta.Content.Content != nil {
						text = *choice.Delta.Content.Content + text
					}
					choice.Delta.Content.Content = &text
				}
				if event.Delta.Refusal != "" {
					choice.Delta.Refusal += event.Delta.Refusal
				}
			}
		case StreamEventKindCitationDelta:
			if event.Citation != nil {
				content := choice.Delta.Content
				if len(content.MultipleContent) == 0 && content.Content != nil {
					text := *content.Content
					content.Content = nil
					content.MultipleContent = append(content.MultipleContent, MessageContentPart{Type: "text", Text: &text})
				}
				if len(content.MultipleContent) == 0 {
					content.MultipleContent = append(content.MultipleContent, MessageContentPart{Type: "text"})
				}
				last := len(content.MultipleContent) - 1
				content.MultipleContent[last].Citations = append(content.MultipleContent[last].Citations, *event.Citation)
				choice.Delta.Content = content
			}
		case StreamEventKindThinkingDelta:
			if event.Delta != nil && (event.Delta.Thinking != "" || event.Delta.Signature != "") {
				if event.Delta.Thinking != "" {
					thinking := event.Delta.Thinking
					if choice.Delta.ReasoningContent != nil {
						thinking = *choice.Delta.ReasoningContent + thinking
					}
					choice.Delta.ReasoningContent = &thinking
				}
				choice.Delta.AppendReasoningBlock(ReasoningBlock{Kind: ReasoningBlockKindThinking, Index: -1, Text: event.Delta.Thinking, Signature: event.Delta.Signature})
			}
		case StreamEventKindSignatureDelta:
			if event.Delta != nil && event.Delta.Signature != "" {
				signature := event.Delta.Signature
				if choice.Delta.ReasoningSignature != nil {
					signature = *choice.Delta.ReasoningSignature + signature
				}
				choice.Delta.ReasoningSignature = &signature
				choice.Delta.AppendReasoningBlock(ReasoningBlock{Kind: ReasoningBlockKindSignature, Index: -1, Signature: event.Delta.Signature})
			}
		case StreamEventKindToolCallStart, StreamEventKindToolCallDelta:
			if event.ToolCall != nil {
				toolCall := *event.ToolCall
				if event.Delta != nil && event.Delta.Arguments != "" {
					toolCall.Function.Arguments = event.Delta.Arguments
				}
				choice.Delta.ToolCalls = MergeToolCallDelta(choice.Delta.ToolCalls, toolCall)
			}
		case StreamEventKindMessageStop:
			if event.StopReason != "" {
				reason := event.StopReason.String()
				choice.FinishReason = &reason
			}
			choice.StopSequence = event.StopSequence
		}
	}
	indices := make([]int, 0, len(choices))
	for idx := range choices {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		response.Choices = append(response.Choices, *choices[idx])
	}
	hasNative := false
	for _, event := range events {
		if event.NativeItem != nil {
			hasNative = true
			break
		}
	}
	if len(response.Choices) == 0 && response.Usage == nil && response.Error == nil && response.Object != "[DONE]" && response.ProviderExtensions == nil && response.Created == 0 && response.SystemFingerprint == "" && response.ServiceTier == "" && response.Status == "" && !hasNative {
		return nil
	}
	return response
}
