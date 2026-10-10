package openaichat

import (
	"github.com/samber/lo"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

// ToLLMToolCall converts OpenAI ToolCall to unified model.ToolCall.
func (tc ToolCall) ToLLMToolCall() model.ToolCall {
	toolCall := model.ToolCall{
		ProviderExtensions: chatExtensions(tc.Fields),
		ID:                 tc.ID,
		Type:               tc.Type,
		Function: model.FunctionCall{
			ProviderExtensions: chatExtensions(tc.Function.Fields),
			Name:               tc.Function.Name,
			Arguments:          tc.Function.Arguments,
		},
		Index: tc.Index,
	}

	extraContent := tc.ExtraContent
	if extraContent == nil && tc.ExtraFields != nil {
		extraContent = tc.ExtraFields.ExtraContent
	}

	if extraContent != nil &&
		extraContent.Google != nil &&
		extraContent.Google.ThoughtSignature != "" {
		toolCall.ThoughtSignature = extraContent.Google.ThoughtSignature
	}

	return toolCall
}

// ToLLMRequest converts OpenAI Request to unified model.InternalLLMRequest.
func (r *Request) ToLLMRequest() *model.InternalLLMRequest {
	if r == nil {
		return nil
	}

	req := &model.InternalLLMRequest{
		ProviderExtensions:  chatExtensions(r.Fields),
		Model:               r.Model,
		FrequencyPenalty:    r.FrequencyPenalty,
		Logprobs:            r.Logprobs,
		MaxCompletionTokens: r.MaxCompletionTokens,
		MaxTokens:           r.MaxTokens,
		PresencePenalty:     r.PresencePenalty,
		Seed:                r.Seed,
		Store:               r.Store,
		Temperature:         r.Temperature,
		TopLogprobs:         r.TopLogprobs,
		TopP:                r.TopP,
		PromptCacheKey:      r.PromptCacheKey,
		SafetyIdentifier:    r.SafetyIdentifier,
		User:                r.User,
		LogitBias:           r.LogitBias,
		Metadata:            r.Metadata,
		Modalities:          r.Modalities,
		ReasoningEffort:     r.ReasoningEffort,
		ReasoningBudget:     r.ReasoningBudget,
		ReasoningSummary:    r.ReasoningSummary,
		ServiceTier:         r.ServiceTier,
		Stream:              r.Stream,
		ParallelToolCalls:   r.ParallelToolCalls,
		Verbosity:           r.Verbosity,
	}

	// Convert messages
	req.Messages = lo.Map(r.Messages, func(m Message, _ int) model.Message {
		return m.ToLLMMessage()
	})

	// Convert Stop
	if r.Stop != nil {
		req.Stop = &model.Stop{
			Stop:         r.Stop.Stop,
			MultipleStop: r.Stop.MultipleStop,
		}
	}

	// Convert StreamOptions
	if r.StreamOptions != nil {
		req.StreamOptions = &model.StreamOptions{
			IncludeUsage: r.StreamOptions.IncludeUsage,
		}
	}

	// Convert Tools
	req.Tools = lo.Map(r.Tools, func(t Tool, _ int) model.Tool {
		return t.ToLLMTool()
	})

	// Convert ToolChoice
	req.ToolChoice = r.ToolChoice.ToLLMToolChoice()

	// Convert ResponseFormat
	if r.ResponseFormat != nil {
		req.ResponseFormat = &model.ResponseFormat{
			Type:       r.ResponseFormat.Type,
			JSONSchema: r.ResponseFormat.JSONSchema,
		}
	}

	if r.Thinking != nil {
		req.Thinking = &model.ThinkingConfig{Type: r.Thinking.Type}
	}

	return req
}

// ToLLMToolChoice converts OpenAI ToolChoice to unified model.ToolChoice.
func (t *ToolChoice) ToLLMToolChoice() *model.ToolChoice {
	if t == nil {
		return nil
	}

	choice := &model.ToolChoice{
		ToolChoice: t.ToolChoice,
	}

	if t.NamedToolChoice != nil {
		choice.NamedToolChoice = &model.NamedToolChoice{
			Type: t.NamedToolChoice.Type,
			Function: &model.ToolFunction{
				Name: t.NamedToolChoice.Function.Name,
			},
		}
	}

	// An allowed_tools choice carries its mode in model.ToolChoice.ToolChoice and
	// its tool subset in model.ToolChoice.Tools, matching the Responses convention.
	if t.AllowedTools != nil {
		choice.NamedToolChoice = &model.NamedToolChoice{Type: "allowed_tools"}
		choice.ToolChoice = t.AllowedTools.Mode
		choice.Tools = lo.Map(t.AllowedTools.Tools, func(tool NamedToolChoice, _ int) model.ToolOption {
			return model.ToolOption{Type: tool.Type, Name: tool.Function.Name}
		})
	}

	return choice
}

// ToLLMMessage converts OpenAI Message to unified model.Message.
func (m Message) ToLLMMessage() model.Message {
	msg := model.Message{
		ProviderExtensions:     chatExtensions(m.Fields),
		ReasoningSignature:     m.ReasoningSignature,
		RedactedThinkingBlocks: m.RedactedThinkingBlocks,
		ReasoningBlocks:        m.ReasoningBlocks,
		Role:                   m.Role,
		Name:                   m.Name,
		Refusal:                m.Refusal,
		ToolCallID:             m.ToolCallID,
		ReasoningContent:       m.ReasoningContent,
		Reasoning:              m.Reasoning,
	}

	if m.Audio != nil {
		msg.Audio = &model.OutputAudio{
			ID:         m.Audio.ID,
			Data:       m.Audio.Data,
			ExpiresAt:  m.Audio.ExpiresAt,
			Transcript: m.Audio.Transcript,
		}
	}

	// Sync reasoning fields: if one field has value and the other is nil, copy the value
	if msg.ReasoningContent == nil && m.Reasoning != nil && *m.Reasoning != "" {
		msg.ReasoningContent = m.Reasoning
	}

	if msg.Reasoning == nil && msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		msg.Reasoning = msg.ReasoningContent
	}

	// Convert Content
	msg.Content = m.Content.ToLLMContent()
	msg.Images = lo.Map(m.Images, func(part MessageContentPart, _ int) model.MessageContentPart { return part.ToLLMPart() })

	// Convert ToolCalls
	if m.ToolCalls != nil {
		msg.ToolCalls = lo.Map(m.ToolCalls, func(tc ToolCall, _ int) model.ToolCall {
			return tc.ToLLMToolCall()
		})

		firstThoughtSignature := lo.FindOrElse(msg.ToolCalls, model.ToolCall{}, func(tc model.ToolCall) bool {
			return tc.ThoughtSignature != ""
		})

		if raw := firstThoughtSignature.ThoughtSignature; raw != "" {
			msg.ReasoningSignature = lo.ToPtr(raw)
		}
	}

	// Convert Annotations
	if len(m.Annotations) > 0 {
		msg.Annotations = lo.Map(m.Annotations, func(a Annotation, _ int) model.Annotation {
			return a.ToLLMAnnotation()
		})
	}
	if msg.ReasoningSignature != nil && msg.ProviderExtensions == nil {
		msg.ProviderExtensions = &model.ProviderExtensions{OpenAIChat: &model.ProtocolExtension{}}
	}

	return msg
}

// ToLLMAnnotation converts OpenAI Annotation to unified model.Annotation.
func (a Annotation) ToLLMAnnotation() model.Annotation {
	annotation := model.Annotation{
		Fields:     a.Fields,
		Type:       a.Type,
		StartIndex: a.StartIndex,
		EndIndex:   a.EndIndex,
	}

	if a.URLCitation != nil {
		annotation.URLCitation = &model.URLCitation{
			Fields: a.URLCitation.Fields,
			URL:    a.URLCitation.URL,
			Title:  a.URLCitation.Title,
		}
	}

	return annotation
}

// ToLLMContent converts OpenAI MessageContent to unified model.MessageContent.
func (c MessageContent) ToLLMContent() model.MessageContent {
	content := model.MessageContent{
		Content: c.Content,
	}

	if c.MultipleContent != nil {
		content.MultipleContent = lo.Map(c.MultipleContent, func(p MessageContentPart, _ int) model.MessageContentPart {
			return p.ToLLMPart()
		})
	}

	return content
}

// ToLLMPart converts OpenAI MessageContentPart to unified model.MessageContentPart.
func (p MessageContentPart) ToLLMPart() model.MessageContentPart {
	part := model.MessageContentPart{
		ProviderExtensions: chatExtensions(p.Fields),
		Citations:          p.Citations,
		Type:               p.Type,
		Text:               p.Text,
	}

	if p.ImageURL != nil {
		part.ImageURL = &model.ImageURL{
			URL:    p.ImageURL.URL,
			Detail: p.ImageURL.Detail,
		}
	}

	if p.VideoURL != nil {
		part.VideoURL = &model.VideoURL{
			URL: p.VideoURL.URL,
		}
	}

	if p.InputAudio != nil {
		part.Audio = &model.Audio{
			Format: p.InputAudio.Format,
			Data:   p.InputAudio.Data,
		}
	}

	if p.File != nil {
		part.Type = "file"
		part.File = &model.File{
			FileData: p.File.FileData,
			FileID:   p.File.FileID,
			Filename: p.File.Filename,
		}
	}

	return part
}

// ResponseFromLLM creates OpenAI Response from unified model.Response.
func ResponseFromLLM(r *model.InternalLLMResponse) *Response {
	if r == nil {
		return nil
	}

	resp := &Response{
		Fields:            chatFields(r.ProviderExtensions),
		ID:                r.ID,
		Object:            r.Object,
		Created:           r.Created,
		Model:             r.Model,
		SystemFingerprint: r.SystemFingerprint,
		ServiceTier:       r.ServiceTier,
	}

	// Convert choices
	resp.Choices = lo.Map(r.Choices, func(c model.Choice, _ int) Choice {
		return ChoiceFromLLM(c)
	})
	if resp.Choices == nil {
		resp.Choices = []Choice{}
	}

	// Convert usage
	if r.Usage != nil {
		resp.Usage = UsageFromLLM(r.Usage)
	}

	// Convert error
	if r.Error != nil {
		resp.Error = &OpenAIError{
			StatusCode: r.Error.StatusCode,
			Detail:     r.Error.Detail,
		}
	}

	// Extract citations from TransformerMetadata if present
	if r.ChatCitations != nil {
		resp.Citations = append([]string(nil), r.ChatCitations...)
	}

	return resp
}

// ChoiceFromLLM creates OpenAI Choice from unified model.Choice.
func ChoiceFromLLM(c model.Choice) Choice {
	choice := Choice{
		Fields:       chatFields(c.ProviderExtensions),
		Index:        c.Index,
		FinishReason: c.FinishReason,
	}

	if c.Message != nil {
		msg := MessageFromLLM(*c.Message)
		choice.Message = &msg
	}

	if c.Delta != nil {
		delta := MessageFromLLM(*c.Delta)
		choice.Delta = &delta
	}

	if c.Logprobs != nil {
		choice.Logprobs = &Logprobs{
			Content: lo.Map(c.Logprobs.Content, func(t model.TokenLogprob, _ int) TokenLogprob {
				return TokenLogprob{
					Token:   t.Token,
					Logprob: t.Logprob,
					Bytes:   t.Bytes,
					TopLogprobs: lo.Map(t.TopLogprobs, func(tl model.TopLogprob, _ int) TopLogprob {
						return TopLogprob{
							Token:   tl.Token,
							Logprob: tl.Logprob,
							Bytes:   tl.Bytes,
						}
					}),
				}
			}),
		}
	}

	return choice
}
