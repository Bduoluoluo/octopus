package model

import "sort"

type StreamAggregator struct {
	chunks []*InternalLLMResponse
}

func (a *StreamAggregator) Add(chunk *InternalLLMResponse) {
	if chunk == nil || chunk.Object == "[DONE]" {
		return
	}
	a.chunks = append(a.chunks, chunk)
}

func (a *StreamAggregator) Reset() {
	a.chunks = nil
}

func (a *StreamAggregator) Response() *InternalLLMResponse {
	if a == nil || len(a.chunks) == 0 {
		return nil
	}

	firstChunk := a.chunks[0]
	result := &InternalLLMResponse{
		ID:                firstChunk.ID,
		Object:            "chat.completion",
		Created:           firstChunk.Created,
		Model:             firstChunk.Model,
		SystemFingerprint: firstChunk.SystemFingerprint,
		ServiceTier:       firstChunk.ServiceTier,
	}
	choicesMap := make(map[int]*Choice)

	for _, chunk := range a.chunks {
		if chunk == nil {
			continue
		}
		if chunk.ID != "" {
			result.ID = chunk.ID
		}
		if chunk.Model != "" {
			result.Model = chunk.Model
		}
		if chunk.Created != 0 {
			result.Created = chunk.Created
		}
		if chunk.SystemFingerprint != "" {
			result.SystemFingerprint = chunk.SystemFingerprint
		}
		if chunk.ServiceTier != "" {
			result.ServiceTier = chunk.ServiceTier
		}
		if chunk.Status != "" {
			result.Status = chunk.Status
		}
		if chunk.IncompleteDetails != nil {
			result.IncompleteDetails = cloneRawMessage(chunk.IncompleteDetails)
		}
		if chunk.ProviderExtensions != nil {
			result.ProviderExtensions = mergeStreamExtensions(result.ProviderExtensions, chunk.ProviderExtensions)
		}
		if chunk.Usage != nil {
			result.Usage = chunk.Usage
		}
		if len(chunk.RawResponsesOutputItems) > 0 {
			result.RawResponsesOutputItems = cloneRawMessage(chunk.RawResponsesOutputItems)
		}
		if chunk.Error != nil {
			result.Error = chunk.Error
		}
		for _, choice := range chunk.Choices {
			existingChoice := choicesMap[choice.Index]
			if existingChoice == nil {
				existingChoice = &Choice{Index: choice.Index, Message: &Message{}}
				choicesMap[choice.Index] = existingChoice
			}
			mergeChoiceDelta(existingChoice, choice)
		}
	}

	result.Choices = make([]Choice, 0, len(choicesMap))
	indices := make([]int, 0, len(choicesMap))
	for idx := range choicesMap {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		result.Choices = append(result.Choices, *choicesMap[idx])
	}
	if err := applyCitationBlocks(result, a.chunks); err != nil && result.Error == nil {
		result.Error = &ResponseError{StatusCode: 502, Detail: ErrorDetail{Type: "protocol_aggregation_error", Message: err.Error()}}
	}
	return result
}

func (a *StreamAggregator) BuildAndReset() *InternalLLMResponse {
	response := a.Response()
	a.Reset()
	return response
}

func mergeChoiceDelta(existingChoice *Choice, choice Choice) {
	if choice.ProviderExtensions != nil {
		existingChoice.ProviderExtensions = mergeStreamExtensions(existingChoice.ProviderExtensions, choice.ProviderExtensions)
	}
	if choice.Delta != nil {
		delta := choice.Delta
		existingChoice.Message.Annotations = append(existingChoice.Message.Annotations, delta.Annotations...)
		if delta.ProviderExtensions != nil {
			existingChoice.Message.ProviderExtensions = mergeStreamExtensions(existingChoice.Message.ProviderExtensions, delta.ProviderExtensions)
		}
		if delta.Role != "" {
			existingChoice.Message.Role = delta.Role
		}
		if delta.Content.Content != nil {
			if existingChoice.Message.Content.Content == nil {
				existingChoice.Message.Content.Content = new(string)
			}
			*existingChoice.Message.Content.Content += *delta.Content.Content
		}
		if len(delta.Content.MultipleContent) > 0 {
			existingChoice.Message.Content.MultipleContent = append(existingChoice.Message.Content.MultipleContent, delta.Content.MultipleContent...)
		}
		if len(delta.Images) > 0 {
			existingChoice.Message.Content.MultipleContent = append(existingChoice.Message.Content.MultipleContent, delta.Images...)
		}
		if delta.Audio != nil {
			if existingChoice.Message.Audio == nil {
				existingChoice.Message.Audio = &struct {
					Data       string `json:"data,omitempty"`
					ExpiresAt  int64  `json:"expires_at,omitempty"`
					ID         string `json:"id,omitempty"`
					Transcript string `json:"transcript,omitempty"`
				}{}
			}
			if delta.Audio.ID != "" {
				existingChoice.Message.Audio.ID = delta.Audio.ID
			}
			if delta.Audio.ExpiresAt > 0 {
				existingChoice.Message.Audio.ExpiresAt = delta.Audio.ExpiresAt
			}
			existingChoice.Message.Audio.Data += delta.Audio.Data
			existingChoice.Message.Audio.Transcript += delta.Audio.Transcript
		}
		if reasoning := delta.GetReasoningContent(); reasoning != "" {
			if existingChoice.Message.ReasoningContent == nil {
				existingChoice.Message.ReasoningContent = new(string)
			}
			*existingChoice.Message.ReasoningContent += reasoning
		}
		for _, toolCall := range delta.ToolCalls {
			existingChoice.Message.ToolCalls = MergeToolCallDelta(existingChoice.Message.ToolCalls, toolCall)
		}
		if delta.Refusal != "" {
			existingChoice.Message.Refusal += delta.Refusal
		}
		if delta.ReasoningSignature != nil {
			if existingChoice.Message.ReasoningSignature == nil {
				existingChoice.Message.ReasoningSignature = new(string)
			}
			*existingChoice.Message.ReasoningSignature += *delta.ReasoningSignature
		}
		existingChoice.Message.ReasoningBlocks = append(existingChoice.Message.ReasoningBlocks, delta.ReasoningBlocks...)
		existingChoice.Message.RedactedThinkingBlocks = append(existingChoice.Message.RedactedThinkingBlocks, delta.RedactedThinkingBlocks...)
	}
	if choice.StopSequence != nil {
		existingChoice.StopSequence = choice.StopSequence
	}
	if choice.FinishReason != nil {
		existingChoice.FinishReason = choice.FinishReason
	}
	if choice.Logprobs != nil {
		if existingChoice.Logprobs == nil {
			existingChoice.Logprobs = &LogprobsContent{}
		}
		existingChoice.Logprobs.Content = append(existingChoice.Logprobs.Content, choice.Logprobs.Content...)
	}
}

func MergeToolCallDelta(toolCalls []ToolCall, delta ToolCall) []ToolCall {
	for i, tc := range toolCalls {
		if tc.Index == delta.Index {
			if delta.ProviderExtensions != nil {
				toolCalls[i].ProviderExtensions = CloneProviderExtensions(delta.ProviderExtensions)
			}
			if delta.ThoughtSignature != "" {
				toolCalls[i].ThoughtSignature = delta.ThoughtSignature
			}
			if delta.ID != "" {
				toolCalls[i].ID = delta.ID
			}
			if delta.Type != "" {
				toolCalls[i].Type = delta.Type
			}
			if delta.Function.Name != "" {
				if toolCalls[i].Function.Name == "" {
					toolCalls[i].Function.Name = delta.Function.Name
				} else if toolCalls[i].Function.Name != delta.Function.Name {
					toolCalls[i].Function.Name += delta.Function.Name
				}
			}
			if delta.Function.Arguments != "" {
				toolCalls[i].Function.Arguments += delta.Function.Arguments
			}
			return toolCalls
		}
	}
	return append(toolCalls, delta)
}
