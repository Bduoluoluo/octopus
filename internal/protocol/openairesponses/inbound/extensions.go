package inbound

import (
	"encoding/json"
	"fmt"
	"strings"

	wire "github.com/xuanli27/octopus/internal/protocol/openairesponses"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func (inbound *ResponseInbound) ResetStream() {
	truncation := inbound.truncation
	namespaces := inbound.namespaces
	*inbound = ResponseInbound{truncation: truncation, namespaces: namespaces}
}

func retainRequestExtensions(request *ResponsesRequest, internal *model.InternalLLMRequest) error {
	if internal.ProviderExtensions == nil {
		internal.ProviderExtensions = &model.ProviderExtensions{}
	}
	extension := &model.ProtocolExtension{Fields: request.Fields}
	internal.ProviderExtensions.OpenAIResponses = extension
	if request.Input.Text == nil && len(request.Input.Items) > 0 {
		raw := request.Fields["input"]
		if len(raw) == 0 {
			var err error
			raw, err = json.Marshal(request.Input.Items)
			if err != nil {
				return err
			}
		}
		internal.SetOpenAIRawInputItems(raw)
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		for position, item := range items {
			extension.Items = append(extension.Items, model.ProtocolItem{Format: model.APIFormatOpenAIResponse, Position: position, Raw: item})
		}
	}
	if request.Text != nil {
		internal.Verbosity = request.Text.Verbosity
		if request.Text.Format != nil && internal.ResponseFormat != nil {
			internal.ResponseFormat.Description = request.Text.Format.Description
			internal.ResponseFormat.Strict = request.Text.Format.Strict
		}
	}
	return nil
}

func (inbound *ResponseInbound) nativeStreamFrames(events []model.StreamEvent) ([]byte, bool, error) {
	var output []byte
	for _, event := range events {
		if event.ProviderExtensions == nil || event.ProviderExtensions.OpenAIResponses == nil {
			continue
		}
		raw := event.ProviderExtensions.OpenAIResponses.Fields["stream_frame"]
		if len(raw) == 0 {
			continue
		}
		var fields model.ProtocolFields
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, true, err
		}
		var eventType string
		if err := json.Unmarshal(fields["type"], &eventType); err != nil {
			return nil, true, err
		}
		if responseRaw := fields["response"]; len(responseRaw) > 0 && string(responseRaw) != "null" {
			var response model.ProtocolFields
			if err := json.Unmarshal(responseRaw, &response); err != nil {
				return nil, true, err
			}
			if event.Model != "" {
				response["model"], _ = json.Marshal(event.Model)
			}
			for _, semantic := range events {
				if semantic.Usage != nil {
					usage := convertUsageToResponses(semantic.Usage)
					if original := response["usage"]; len(original) > 0 && string(original) != "null" {
						if err := json.Unmarshal(original, &usage.Fields); err != nil {
							return nil, true, err
						}
					}
					var err error
					response["usage"], err = json.Marshal(usage)
					if err != nil {
						return nil, true, err
					}
				}
				if semantic.ProviderExtensions != nil && semantic.ProviderExtensions.OpenAI != nil && len(semantic.ProviderExtensions.OpenAI.RawResponseItems) > 0 {
					var items []json.RawMessage
					if err := json.Unmarshal(response["output"], &items); err != nil && len(response["output"]) > 0 {
						return nil, true, err
					}
					if len(items) == 0 {
						response["output"] = semantic.ProviderExtensions.OpenAI.RawResponseItems
					}
				}
			}
			fields["response"], _ = json.Marshal(response)
		}
		if eventType == "response.done" || strings.HasPrefix(eventType, "response.") && (strings.HasSuffix(eventType, ".completed") || strings.HasSuffix(eventType, ".failed") || strings.HasSuffix(eventType, ".incomplete") || strings.HasSuffix(eventType, ".cancelled")) {
			inbound.responseCompleted = true
		}
		body, err := json.Marshal(fields)
		if err != nil {
			return nil, true, err
		}
		output = append(output, []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, body))...)
	}
	if len(output) > 0 {
		for _, event := range events {
			if event.Kind == model.StreamEventKindDone {
				output = append(output, []byte("data: [DONE]\n\n")...)
			}
		}
	}
	return output, len(output) > 0, nil
}

func (inbound *ResponseInbound) terminalEvent() []byte {
	if inbound.responseCompleted || !inbound.hasFinished {
		return nil
	}
	inbound.responseCompleted = true
	eventType, status := responsesTerminalEvent(inbound.finalFinishReason)
	response := &ResponsesResponse{Object: "response", ID: inbound.responseID, Model: inbound.model, CreatedAt: inbound.createdAt, Status: &status, Truncation: inbound.truncation, Output: inbound.finalOutputItems(), Usage: convertUsageToResponses(inbound.usage)}
	return inbound.enqueueEvent(&ResponsesStreamEvent{Type: eventType, Response: response})
}

func validateResponseContent(response *model.InternalLLMResponse) error {
	if len(response.Choices) > 1 {
		return fmt.Errorf("Responses cannot represent multiple choices")
	}
	if err := wire.ValidateContentExtensions(response.ProviderExtensions); err != nil {
		return err
	}
	sameProtocol := len(response.RawResponsesOutputItems) > 0 || wire.HasResponsesFrame(response.ProtocolEvents)
	for _, choice := range response.Choices {
		if choice.Index != 0 {
			return fmt.Errorf("Responses cannot represent choice %d", choice.Index)
		}
		if err := wire.ValidateContentExtensions(choice.ProviderExtensions); err != nil {
			return err
		}
		message := choice.Message
		if message == nil {
			message = choice.Delta
		}
		if message == nil {
			continue
		}
		if err := wire.ValidateMessageContent(message, sameProtocol); err != nil {
			return err
		}
		if sameProtocol {
			continue
		}
		if message.Audio != nil {
			return fmt.Errorf("responses protocol cannot represent Chat output audio")
		}
		if len(message.Images) > 0 {
			return fmt.Errorf("responses protocol cannot represent Chat image output without native items")
		}
		for _, part := range message.Content.MultipleContent {
			if part.Type != "text" && part.Type != "image_url" {
				return fmt.Errorf("responses protocol cannot represent output content %q", part.Type)
			}
		}
	}
	return nil
}

func retainResponseExtensions(response *ResponsesResponse, internal *model.InternalLLMResponse) error {
	if internal.ProviderExtensions != nil && internal.ProviderExtensions.OpenAIResponses != nil {
		response.Fields = internal.ProviderExtensions.OpenAIResponses.Fields
	}
	if response.Usage != nil && len(response.Fields["usage"]) > 0 && string(response.Fields["usage"]) != "null" {
		if err := json.Unmarshal(response.Fields["usage"], &response.Usage.Fields); err != nil {
			return err
		}
	}
	if len(internal.RawResponsesOutputItems) > 0 {
		if err := json.Unmarshal(internal.RawResponsesOutputItems, &response.Output); err != nil {
			return fmt.Errorf("decode raw responses output: %w", err)
		}
	}
	if internal.Status != "" {
		response.Status = &internal.Status
	}
	if len(internal.IncompleteDetails) > 0 {
		if err := json.Unmarshal(internal.IncompleteDetails, &response.IncompleteDetails); err != nil {
			return err
		}
	}
	if internal.Error != nil {
		response.Status = stringPointer("failed")
		response.Error = &ResponsesError{Code: internal.Error.Detail.Code, Type: internal.Error.Detail.Type, Message: internal.Error.Detail.Message, Param: internal.Error.Detail.Param}
	}
	if internal.ServiceTier != "" {
		response.ServiceTier = &internal.ServiceTier
	}
	return nil
}

func stringPointer(value string) *string { return &value }

func (inbound *ResponseInbound) recordCompletedItem(index int, item ResponsesItem) {
	if inbound.completedOutputByIndex == nil {
		inbound.completedOutputByIndex = make(map[int]ResponsesItem)
	}
	inbound.completedOutputByIndex[index] = item
	inbound.completedOutputItems = append(inbound.completedOutputItems, item)
}

func flatFunctionName(namespace, name string) string {
	if namespace == "" || name == "" {
		return name
	}
	return namespace + "__" + name
}

func (inbound *ResponseInbound) captureNamespaces(request *model.InternalLLMRequest) error {
	inbound.namespaces = make(map[string]string)
	declared := make(map[string]bool)
	for _, tool := range request.Tools {
		if tool.Type != "function" {
			continue
		}
		if declared[tool.Function.Name] {
			request.MarkOpenAIResponsesPassthroughRequired("tool:duplicate_function_name")
			inbound.namespaces = nil
			return nil
		}
		declared[tool.Function.Name] = true
		var namespace string
		if tool.Function.ProviderExtensions != nil && tool.Function.ProviderExtensions.OpenAIResponses != nil {
			raw := tool.Function.ProviderExtensions.OpenAIResponses.Fields["namespace"]
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &namespace); err != nil {
					return err
				}
			}
		}
		inbound.namespaces[tool.Function.Name] = namespace
	}
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			if call.ProviderExtensions == nil || call.ProviderExtensions.OpenAIResponses == nil {
				continue
			}
			var namespace string
			raw := call.ProviderExtensions.OpenAIResponses.Fields["namespace"]
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &namespace); err != nil {
					return err
				}
			}
			if previous, exists := inbound.namespaces[call.Function.Name]; exists && previous != namespace {
				request.MarkOpenAIResponsesPassthroughRequired("tool:ambiguous_namespace")
				inbound.namespaces = nil
				return nil
			}
			inbound.namespaces[call.Function.Name] = namespace
		}
	}
	return nil
}

func (inbound *ResponseInbound) restoreNamespace(item *ResponsesItem) {
	if item.Type != "function_call" || item.Namespace != "" {
		return
	}
	namespace := inbound.namespaces[item.Name]
	if namespace == "" {
		return
	}
	item.Namespace = namespace
	item.Name = strings.TrimPrefix(item.Name, namespace+"__")
}

func itemExtensions(item *ResponsesItem) *model.ProviderExtensions {
	fields := model.ProtocolFields{}
	fields["namespace"], _ = json.Marshal(item.Namespace)
	fields["item_id"], _ = json.Marshal(item.ID)
	fields["type"], _ = json.Marshal(item.Type)
	return &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Fields: fields}}
}

func contentCitations(annotations []ResponsesAnnotation) []model.ContentCitation {
	var citations []model.ContentCitation
	for _, annotation := range annotations {
		citation := model.ContentCitation{Type: annotation.Type, URL: annotation.URL, Title: annotation.Title, StartIndex: annotation.StartIndex, EndIndex: annotation.EndIndex, Fields: annotation.Fields}
		if annotation.URLCitation != nil {
			citation.URL = &annotation.URLCitation.URL
			citation.Title = &annotation.URLCitation.Title
		}
		citations = append(citations, citation)
	}
	return citations
}

func (inbound *ResponseInbound) emitCitation(citation *model.ContentCitation) [][]byte {
	if citation == nil {
		inbound.streamError = fmt.Errorf("missing citation")
		return nil
	}
	events := inbound.handleTextContent(stringPointer(""))
	if citation.Type != "url_citation" && citation.Type != "file_citation" && citation.Type != "container_file_citation" {
		inbound.streamError = fmt.Errorf("cannot map citation type %q to responses", citation.Type)
		return nil
	}
	annotation := ResponsesAnnotation{Type: citation.Type, URL: citation.URL, Title: citation.Title, StartIndex: citation.StartIndex, EndIndex: citation.EndIndex, Fields: citation.Fields}
	event := &ResponsesStreamEvent{Type: "response.output_text.annotation.added", OutputIndex: inbound.outputIndex, ContentIndex: &inbound.contentIndex, ItemID: &inbound.currentItemID, Annotation: &annotation}
	return append(events, inbound.enqueueEvent(event))
}
