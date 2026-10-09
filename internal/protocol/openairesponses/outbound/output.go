package outbound

import (
	"encoding/json"

	"github.com/samber/lo"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func enrichResponseItems(result *model.InternalLLMResponse, response *ResponsesResponse) {
	if len(result.Choices) == 0 || result.Choices[0].Message == nil {
		return
	}
	message := result.Choices[0].Message
	var annotated []model.MessageContentPart
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
				message.AppendReasoningBlock(model.ReasoningBlock{Kind: model.ReasoningBlockKindThinking, Index: position, Text: reasoning, Signature: signature})
				if signature != "" {
					message.ReasoningSignature = &signature
				}
			}
		case "message":
			if item.Content == nil {
				continue
			}
			for _, content := range item.Content.Items {
				if content.Type != "output_text" {
					continue
				}
				part := model.MessageContentPart{Type: "text", Text: content.Text}
				for _, annotation := range content.Annotations {
					citation := model.ContentCitation{Type: annotation.Type, URL: annotation.URL, Title: annotation.Title, StartIndex: annotation.StartIndex, EndIndex: annotation.EndIndex, Fields: annotation.Fields}
					if annotation.URLCitation != nil {
						citation.URL = &annotation.URLCitation.URL
						citation.Title = &annotation.URLCitation.Title
					}
					part.Citations = append(part.Citations, citation)
				}
				annotated = append(annotated, part)
			}
		case "function_call":
			for index := range message.ToolCalls {
				if message.ToolCalls[index].ID != item.CallID {
					continue
				}
				fields := model.ProtocolFields{}
				fields["namespace"], _ = json.Marshal(item.Namespace)
				fields["item_id"], _ = json.Marshal(item.ID)
				message.ToolCalls[index].ProviderExtensions = &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Fields: fields}}
			}
		case "custom_tool_call":
			fields := model.ProtocolFields{"raw": encoded}
			message.ToolCalls = append(message.ToolCalls, model.ToolCall{ID: item.CallID, Type: "custom", Function: model.FunctionCall{Name: item.Name, Arguments: lo.FromPtr(item.Input)}, ProviderExtensions: &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Fields: fields}}})
		}
	}
	hasCitation := false
	for _, part := range annotated {
		if len(part.Citations) > 0 {
			hasCitation = true
		}
	}
	if hasCitation {
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
