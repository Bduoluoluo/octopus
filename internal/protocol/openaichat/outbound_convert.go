package openaichat

import (
	"context"
	"encoding/json"

	"github.com/samber/lo"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func RequestFromLLM(ctx context.Context, r *model.InternalLLMRequest, reasoningField ReasoningField) *Request {
	if r == nil {
		return nil
	}

	req := &Request{
		Fields:              chatFields(r.ProviderExtensions),
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
	req.Messages = lo.Map(r.Messages, func(m model.Message, _ int) Message {
		return MessageFromLLMWithConfig(m, reasoningField)
	})

	// Convert Stop
	if r.Stop != nil {
		req.Stop = &Stop{
			Stop:         r.Stop.Stop,
			MultipleStop: r.Stop.MultipleStop,
		}
	}

	// Convert StreamOptions
	if r.StreamOptions != nil {
		req.StreamOptions = &StreamOptions{
			IncludeUsage: r.StreamOptions.IncludeUsage,
		}
	}

	req.Tools = lo.Map(r.Tools, func(t model.Tool, _ int) Tool {
		return ToolFromLLM(t)
	})

	// Convert ToolChoice
	req.ToolChoice = ToolChoiceFromLLM(r.ToolChoice)
	if raw := req.Fields["tool_choice"]; len(raw) > 0 && req.ToolChoice != nil && req.ToolChoice.NamedToolChoice != nil {
		var original struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &original) == nil && original.Type == req.ToolChoice.NamedToolChoice.Type && original.Type != "function" && original.Type != "allowed_tools" {
			req.ToolChoice = nil
		}
	}

	// Convert ResponseFormat
	if r.ResponseFormat != nil {
		req.ResponseFormat = &ResponseFormat{
			Type:       r.ResponseFormat.Type,
			JSONSchema: r.ResponseFormat.JSONSchema,
		}
	}

	return req
}

// MessageFromLLM creates OpenAI Message from unified model.Message.
// Defaults to ReasoningFieldAll to preserve both reasoning fields.
func MessageFromLLM(m model.Message) Message {
	return MessageFromLLMWithConfig(m, ReasoningFieldAll)
}

// MessageFromLLMWithConfig creates OpenAI Message from unified model.Message with reasoning field configuration.
func MessageFromLLMWithConfig(m model.Message, reasoningField ReasoningField) Message {
	var reasoningContent, reasoning *string

	// Apply reasoning field configuration
	switch reasoningField {
	case ReasoningFieldContent:
		// Only use reasoning_content field
		// Prefer ReasoningContent, fallback to Reasoning if ReasoningContent is nil
		reasoningContent = m.ReasoningContent
		if reasoningContent == nil && m.Reasoning != nil {
			reasoningContent = m.Reasoning
		}
		reasoning = nil
	case ReasoningFieldReasoning:
		// Only use reasoning field
		// Prefer Reasoning, fallback to ReasoningContent if Reasoning is nil
		reasoning = m.Reasoning
		if reasoning == nil && m.ReasoningContent != nil {
			reasoning = m.ReasoningContent
		}
		reasoningContent = nil
	case ReasoningFieldNone:
		// Strip all reasoning fields
		reasoningContent = nil
		reasoning = nil
	default: // ReasoningFieldAll
		// Preserve both reasoning fields with sync logic
		reasoningContent = m.ReasoningContent
		reasoning = m.Reasoning

		// Sync: if one field has value and the other is nil/empty, copy the value
		if reasoningContent == nil && reasoning != nil && *reasoning != "" {
			reasoningContent = reasoning
		}
		if reasoning == nil && reasoningContent != nil && *reasoningContent != "" {
			reasoning = reasoningContent
		}
	}

	// Build the Message with determined fields
	msg := Message{
		Fields:                 chatFields(m.ProviderExtensions),
		ReasoningSignature:     m.ReasoningSignature,
		RedactedThinkingBlocks: m.RedactedThinkingBlocks,
		ReasoningBlocks:        m.ReasoningBlocks,
		Role:                   m.Role,
		Name:                   m.Name,
		Refusal:                m.Refusal,
		ToolCallID:             m.ToolCallID,
		ReasoningContent:       reasoningContent,
		Reasoning:              reasoning,
	}
	if m.ReasoningSignature != nil && (m.ProviderExtensions == nil || m.ProviderExtensions.OpenAIChat == nil) {
		preserveForeignSignature(&msg)
	}

	if m.Audio != nil {
		msg.Audio = &OutputAudio{
			ID:         m.Audio.ID,
			Data:       m.Audio.Data,
			ExpiresAt:  m.Audio.ExpiresAt,
			Transcript: m.Audio.Transcript,
		}
	}

	// Convert Content
	msg.Content = MessageContentFromLLM(m.Content)
	msg.Images = lo.Map(m.Images, func(part model.MessageContentPart, _ int) MessageContentPart { return MessageContentPartFromLLM(part) })

	// Convert ToolCalls
	if m.ToolCalls != nil {
		msg.ToolCalls = lo.Map(m.ToolCalls, func(tc model.ToolCall, _ int) ToolCall {
			return ToolCallFromLLM(tc)
		})
	}

	// Convert Annotations
	if len(m.Annotations) > 0 {
		msg.Annotations = lo.Map(m.Annotations, func(a model.Annotation, _ int) Annotation {
			return AnnotationFromLLM(a)
		})
	}

	return msg
}

// AnnotationFromLLM creates OpenAI Annotation from unified model.Annotation.
func AnnotationFromLLM(a model.Annotation) Annotation {
	annotation := Annotation{
		Fields:     a.Fields,
		Type:       a.Type,
		StartIndex: a.StartIndex,
		EndIndex:   a.EndIndex,
	}

	if a.URLCitation != nil {
		annotation.URLCitation = &URLCitation{
			Fields: a.URLCitation.Fields,
			URL:    a.URLCitation.URL,
			Title:  a.URLCitation.Title,
		}
	}

	return annotation
}

// MessageContentFromLLM creates OpenAI MessageContent from unified model.MessageContent.
func MessageContentFromLLM(c model.MessageContent) MessageContent {
	content := MessageContent{
		Content: c.Content,
	}

	if c.MultipleContent != nil {
		content.MultipleContent = lo.Map(c.MultipleContent, func(p model.MessageContentPart, _ int) MessageContentPart {
			return MessageContentPartFromLLM(p)
		})
	}

	return content
}

// MessageContentPartFromLLM creates OpenAI MessageContentPart from unified model.MessageContentPart.
func MessageContentPartFromLLM(p model.MessageContentPart) MessageContentPart {
	part := MessageContentPart{
		Fields:    chatFields(p.ProviderExtensions),
		Citations: p.Citations,
		Type:      normalizeContentPartType(p.Type),
		Text:      p.Text,
	}

	if p.ImageURL != nil {
		part.ImageURL = &ImageURL{
			URL:    p.ImageURL.URL,
			Detail: p.ImageURL.Detail,
		}
	}

	if p.VideoURL != nil {
		part.VideoURL = &VideoURL{
			URL: p.VideoURL.URL,
		}
	}

	if p.Audio != nil {
		part.InputAudio = &InputAudio{
			Format: p.Audio.Format,
			Data:   p.Audio.Data,
		}
	}

	if p.File != nil {
		part.Type = "file"
		part.File = &File{
			FileData: p.File.FileData,
			FileID:   p.File.FileID,
			Filename: p.File.Filename,
		}
	}

	return part
}

// normalizeContentPartType maps Responses-only text part types onto the plain
// "text" type used by Chat Completions. The Responses API distinguishes input
// from output text, but Chat Completions has a single text part type, and strict
// OpenAI-compatible upstreams reject the ones they do not know. Types that Chat
// Completions does understand (image_url, video_url, input_audio, ...) pass through.
func normalizeContentPartType(partType string) string {
	switch partType {
	case "input_text", "output_text":
		return "text"
	default:
		return partType
	}
}

// ToolChoiceFromLLM creates OpenAI ToolChoice from unified model.ToolChoice.
func ToolChoiceFromLLM(tc *model.ToolChoice) *ToolChoice {
	if tc == nil {
		return nil
	}

	choice := &ToolChoice{ToolChoice: tc.ToolChoice}

	if tc.NamedToolChoice != nil {
		name := ""
		if tc.NamedToolChoice.Function != nil {
			name = tc.NamedToolChoice.Function.Name
		}
		if name == "" && tc.NamedToolChoice.Name != nil {
			name = *tc.NamedToolChoice.Name
		}
		choice.NamedToolChoice = &NamedToolChoice{
			Type:     tc.NamedToolChoice.Type,
			Function: ToolFunction{Name: name},
		}

		// An allowed_tools choice nests its mode and tool subset; the plain
		// named shape would emit an empty function name and silently lift the
		// caller's restriction.
		if tc.NamedToolChoice.Type == "allowed_tools" {
			choice.ToolChoice = nil
			choice.AllowedTools = &AllowedTools{
				Mode: tc.ToolChoice,
				Tools: lo.Map(tc.Tools, func(o model.ToolOption, _ int) NamedToolChoice {
					return NamedToolChoice{Type: o.Type, Function: ToolFunction{Name: o.Name}}
				}),
			}
		}
	}

	return choice
}

// ToolFromLLM creates OpenAI Tool from unified model.Tool.
func ToolFromLLM(t model.Tool) Tool {
	return Tool{
		Fields: chatFields(t.ProviderExtensions),
		Type:   t.Type,
		Function: Function{
			Fields:      chatFields(t.Function.ProviderExtensions),
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
			Strict:      t.Function.Strict,
		},
	}
}

// ToolCallFromLLM creates OpenAI ToolCall from unified model.ToolCall.
func ToolCallFromLLM(tc model.ToolCall) ToolCall {
	toolCall := ToolCall{
		Fields: chatFields(tc.ProviderExtensions),
		ID:     tc.ID,
		Type:   tc.Type,
		Function: FunctionCall{
			Fields:    chatFields(tc.Function.ProviderExtensions),
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		},
		Index: tc.Index,
	}

	if raw := tc.ThoughtSignature; raw != "" {
		toolCall.ExtraContent = &ToolCallExtraContent{
			Google: &ToolCallGoogleExtraContent{
				ThoughtSignature: raw,
			},
		}
	}

	return toolCall
}

// ToLLMResponse converts OpenAI Response to unified model.Response.
func (r *Response) ToLLMResponse() *model.InternalLLMResponse {
	if r == nil {
		return nil
	}

	resp := &model.InternalLLMResponse{
		ProviderExtensions: chatExtensions(r.Fields),
		ID:                 r.ID,
		Object:             r.Object,
		Created:            r.Created,
		Model:              r.Model,
		SystemFingerprint:  r.SystemFingerprint,
		ServiceTier:        r.ServiceTier,
	}

	// Convert choices
	resp.Choices = lo.Map(r.Choices, func(c Choice, _ int) model.Choice {
		return c.ToLLMChoice()
	})

	// Convert usage
	if r.Usage != nil {
		resp.Usage = r.Usage.ToLLMUsage()
	}

	// Convert error
	if r.Error != nil {
		resp.Error = &model.ResponseError{
			StatusCode: r.Error.StatusCode,
			Detail:     r.Error.Detail,
		}
	}

	// Store citations in TransformerMetadata if present
	if len(r.Citations) > 0 {
		resp.ChatCitations = append([]string(nil), r.Citations...)
	}

	return resp
}

// ToLLMChoice converts OpenAI Choice to unified model.Choice.
func (c Choice) ToLLMChoice() model.Choice {
	choice := model.Choice{
		ProviderExtensions: chatExtensions(c.Fields),
		Index:              c.Index,
		FinishReason:       c.FinishReason,
	}

	if c.Message != nil {
		msg := c.Message.ToLLMMessage()
		choice.Message = &msg
	}

	if c.Delta != nil {
		delta := c.Delta.ToLLMMessage()
		choice.Delta = &delta
	}

	choice.Logprobs = toLLMLogprobs(c.Logprobs)

	return choice
}

// toLLMLogprobs converts OpenAI Logprobs to unified model.LogprobsContent.
func toLLMLogprobs(lp *Logprobs) *model.LogprobsContent {
	if lp == nil {
		return nil
	}

	return &model.LogprobsContent{
		Content: lo.Map(lp.Content, func(t TokenLogprob, _ int) model.TokenLogprob {
			return model.TokenLogprob{
				Token:   t.Token,
				Logprob: t.Logprob,
				Bytes:   t.Bytes,
				TopLogprobs: lo.Map(t.TopLogprobs, func(tl TopLogprob, _ int) model.TopLogprob {
					return model.TopLogprob{
						Token:   tl.Token,
						Logprob: tl.Logprob,
						Bytes:   tl.Bytes,
					}
				}),
			}
		}),
	}
}
