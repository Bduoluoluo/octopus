package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func CaptureContent(content MessageContent, fields model.ProtocolFields) (*model.ProviderExtensions, error) {
	extension := &model.AnthropicExtension{Fields: CopyFields(fields, "content")}
	if len(content.Raw) > 0 && bytes.HasPrefix(bytes.TrimSpace(content.Raw), []byte("[")) {
		var blocks []json.RawMessage
		if err := json.Unmarshal(content.Raw, &blocks); err != nil {
			return nil, err
		}
		for position, raw := range blocks {
			extension.Items = append(extension.Items, model.ProtocolItem{Format: model.APIFormatAnthropicMessage, Position: position, Raw: raw})
		}
		if len(blocks) == 0 {
			extension.Fields["content"] = json.RawMessage("[]")
		}
	} else if len(content.Raw) > 0 {
		extension.Fields["content"] = append(json.RawMessage(nil), content.Raw...)
	} else if content.Content != nil {
		raw, err := json.Marshal(*content.Content)
		if err != nil {
			return nil, err
		}
		extension.Fields["content"] = raw
	} else {
		for position, block := range content.MultipleContent {
			raw, err := json.Marshal(block)
			if err != nil {
				return nil, err
			}
			extension.Items = append(extension.Items, model.ProtocolItem{Format: model.APIFormatAnthropicMessage, Position: position, Raw: raw})
		}
		if content.MultipleContent != nil && len(content.MultipleContent) == 0 {
			extension.Fields["content"] = json.RawMessage("[]")
		}
	}
	return &model.ProviderExtensions{Anthropic: extension}, nil
}

func RestoreContent(extension *model.ProviderExtensions) (MessageContent, bool, error) {
	if extension == nil || extension.Anthropic == nil {
		return MessageContent{}, false, nil
	}
	native := extension.Anthropic
	if raw, exists := native.Fields["content"]; exists {
		var content MessageContent
		if err := json.Unmarshal(raw, &content); err != nil {
			return content, false, err
		}
		return content, true, nil
	}
	if native.Items == nil {
		return MessageContent{}, false, nil
	}
	blocks := make([]MessageContentBlock, 0, len(native.Items))
	for _, item := range native.Items {
		if item.Format != model.APIFormatAnthropicMessage {
			return MessageContent{}, false, fmt.Errorf("cannot restore %s as Anthropic content", item.Format)
		}
		var block MessageContentBlock
		if err := json.Unmarshal(item.Raw, &block); err != nil {
			return MessageContent{}, false, err
		}
		blocks = append(blocks, block)
	}
	return MessageContent{MultipleContent: blocks}, true, nil
}

func ContentPart(block MessageContentBlock, position int) (model.MessageContentPart, error) {
	raw, err := json.Marshal(block)
	if err != nil {
		return model.MessageContentPart{}, err
	}
	part := model.MessageContentPart{Type: block.Type, Native: &model.ProtocolItem{Format: model.APIFormatAnthropicMessage, Position: position, Raw: raw}}
	if block.CacheControl != nil {
		part.CacheControl = &model.CacheControl{Type: block.CacheControl.Type, TTL: block.CacheControl.TTL}
	}
	switch block.Type {
	case "text":
		part.Text, part.Citations = block.Text, block.Citations
	case "image":
		part.Type = "image_url"
		if block.Source != nil {
			url := block.Source.URL
			if block.Source.Type == "base64" {
				url = "data:" + block.Source.MediaType + ";base64," + block.Source.Data
			}
			part.ImageURL = &model.ImageURL{URL: url}
		}
	case "document":
		if block.Source != nil {
			part.Document = &model.DocumentSource{Type: block.Source.Type, MediaType: block.Source.MediaType, Data: block.Source.Data, URL: block.Source.URL, Content: block.Source.Content, Title: block.Title, Context: block.Context}
			if part.Document.Type == "text" {
				part.Document.Text, part.Document.Data = part.Document.Data, ""
			}
			if block.CitationConfig != nil && block.CitationConfig.Enabled != nil {
				part.Document.Citations = &model.DocumentCitations{Enabled: *block.CitationConfig.Enabled}
			}
		}
	}
	return part, nil
}
