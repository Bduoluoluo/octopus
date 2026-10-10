package anthropic

import (
	"fmt"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func ConvertCitation(citation model.ContentCitation) (model.ContentCitation, error) {
	if citation.Type == "url_citation" {
		if citation.URL == nil || *citation.URL == "" {
			return citation, fmt.Errorf("URL citation is missing its URL")
		}
		citation.Type = "web_search_result_location"
		return citation, nil
	}
	switch citation.Type {
	case "char_location", "page_location", "content_block_location", "search_result_location", "web_search_result_location":
		return citation, nil
	default:
		return citation, fmt.Errorf("Anthropic cannot represent citation type %q", citation.Type)
	}
}

func AnnotationCitation(annotation model.Annotation) (model.ContentCitation, error) {
	citation := model.ContentCitation{Type: annotation.Type, StartIndex: annotation.StartIndex, EndIndex: annotation.EndIndex, Fields: CopyFields(annotation.Fields)}
	if annotation.URLCitation != nil {
		citation.URL = &annotation.URLCitation.URL
		citation.Title = &annotation.URLCitation.Title
		for key, raw := range annotation.URLCitation.Fields {
			if _, exists := citation.Fields[key]; !exists {
				citation.Fields[key] = raw
			}
		}
	}
	return ConvertCitation(citation)
}

func NormalizeMessageMetadata(message model.Message) model.Message {
	parts := append([]model.MessageContentPart(nil), message.Content.MultipleContent...)
	if message.Content.Content != nil && (message.Refusal != "" || len(message.Annotations) > 0) {
		parts = append([]model.MessageContentPart{{Type: "text", Text: message.Content.Content}}, parts...)
		message.Content.Content = nil
	}
	if message.Refusal != "" {
		refusal := message.Refusal
		parts = append(parts, model.MessageContentPart{Type: "text", Text: &refusal})
	}
	if len(message.Annotations) > 0 {
		textIndex := -1
		for index := range parts {
			if parts[index].Type == "text" {
				textIndex = index
				break
			}
		}
		if textIndex < 0 {
			empty := ""
			textIndex = len(parts)
			parts = append(parts, model.MessageContentPart{Type: "text", Text: &empty})
		}
		parts[textIndex].Citations = append([]model.ContentCitation(nil), parts[textIndex].Citations...)
		for _, annotation := range message.Annotations {
			citation, err := AnnotationCitation(annotation)
			if err == nil {
				parts[textIndex].Citations = append(parts[textIndex].Citations, citation)
			}
		}
	}
	for index := range parts {
		parts[index].Citations = append([]model.ContentCitation(nil), parts[index].Citations...)
		for citationIndex, citation := range parts[index].Citations {
			if converted, err := ConvertCitation(citation); err == nil {
				parts[index].Citations[citationIndex] = converted
			}
		}
	}
	message.Content.MultipleContent = parts
	return message
}
