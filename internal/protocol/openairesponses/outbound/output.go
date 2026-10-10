package outbound

import (
	"encoding/json"
	"strings"

	"github.com/samber/lo"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func enrichResponseItems(result *model.InternalLLMResponse, response *ResponsesResponse) {
	if len(result.Choices) == 0 || result.Choices[0].Message == nil {
		return
	}
	message := result.Choices[0].Message
	message.ToolCalls = nil
	message.ReasoningBlocks = nil
	var annotated []model.MessageContentPart
	var reasoningText, signatures strings.Builder
	for position, item := range response.Output {
		encoded, err := json.Marshal(item)
		if err != nil {
			result.Error = &model.ResponseError{Detail: model.ErrorDetail{Message: err.Error()}}
			return
		}
		result.ProviderExtensions.OpenAIResponses.Items = append(result.ProviderExtensions.OpenAIResponses.Items, model.ProtocolItem{Format: model.APIFormatOpenAIResponse, Position: position, Raw: encoded})
		switch item.Type {
		case "reasoning":
			reasoning := ""
			for _, summary := range item.Summary {
				reasoning += summary.Text
			}
			signature := lo.FromPtr(item.EncryptedContent)
			if item.ReasoningContent != nil {
				reasoning += lo.FromPtr(item.ReasoningContent.Text)
				for _, content := range item.ReasoningContent.Items {
					reasoning += content.Text
				}
			}
			if reasoning != "" || signature != "" {
				message.AppendReasoningBlock(model.ReasoningBlock{Kind: model.ReasoningBlockKindThinking, Index: position, Text: reasoning, Signature: signature, Provider: "openai"})
				reasoningText.WriteString(reasoning)
				signatures.WriteString(signature)
			}
		case "message":
			if item.Content == nil {
				continue
			}
			for _, content := range item.Content.Items {
				if content.Type != "output_text" {
					continue
				}
				annotated = append(annotated, responseOutputTextPart(content))
			}
		case "output_text":
			annotated = append(annotated, responseOutputTextPart(item))
		case "function_call", "custom_tool_call":
			kind, arguments := "function", item.Arguments
			fields := model.ProtocolFields{}
			fields["namespace"], _ = json.Marshal(item.Namespace)
			fields["item_id"], _ = json.Marshal(item.ID)
			if item.Type == "custom_tool_call" {
				kind, arguments = "custom", lo.FromPtr(item.Input)
				fields["raw"] = encoded
			}
			message.ToolCalls = append(message.ToolCalls, model.ToolCall{Index: len(message.ToolCalls), ID: item.CallID, Type: kind, Function: model.FunctionCall{Name: item.Name, Arguments: arguments}, ProviderExtensions: &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Fields: fields}}})
		}
	}
	message.ReasoningContent = nil
	message.ReasoningSignature = nil
	if reasoningText.Len() > 0 {
		message.ReasoningContent = lo.ToPtr(reasoningText.String())
	}
	if signatures.Len() > 0 {
		message.ReasoningSignature = lo.ToPtr(signatures.String())
	}
	hasCitation := false
	for _, part := range annotated {
		if len(part.Citations) > 0 {
			hasCitation = true
		}
	}
	if hasCitation {
		for _, part := range message.Content.MultipleContent {
			if part.Type != "text" {
				annotated = append(annotated, part)
			}
		}
		message.Content = model.MessageContent{MultipleContent: annotated}
	}
	if len(message.ToolCalls) > 0 {
		result.Choices[0].FinishReason = lo.ToPtr("tool_calls")
	}
	if response.Status != nil && *response.Status == "incomplete" {
		reason := "length"
		if response.IncompleteDetails != nil && response.IncompleteDetails.Reason == "content_filter" {
			reason = "content_filter"
		}
		result.Choices[0].FinishReason = &reason
	}
	if response.Error != nil {
		result.Error = &model.ResponseError{Detail: model.ErrorDetail{Code: response.Error.Code, Type: response.Error.Type, Param: response.Error.Param, Message: response.Error.Message}}
	} else if response.Status != nil && (*response.Status == "failed" || *response.Status == "cancelled" || *response.Status == "canceled") {
		result.Error = &model.ResponseError{Detail: model.ErrorDetail{Type: "upstream_error", Message: "upstream response " + *response.Status}}
	}
}

func responseOutputTextPart(content ResponsesItem) model.MessageContentPart {
	part := model.MessageContentPart{Type: "text", Text: content.Text}
	for _, annotation := range content.Annotations {
		citation := model.ContentCitation{Type: annotation.Type, URL: annotation.URL, Title: annotation.Title, StartIndex: annotation.StartIndex, EndIndex: annotation.EndIndex, Fields: annotation.Fields}
		if annotation.URLCitation != nil {
			citation.URL = &annotation.URLCitation.URL
			citation.Title = &annotation.URLCitation.Title
		}
		part.Citations = append(part.Citations, citation)
	}
	return part
}
