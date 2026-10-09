package outbound

import (
	"encoding/json"
	"fmt"

	wire "github.com/xuanli27/octopus/internal/protocol/anthropic"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func validateNativeContent(request *model.InternalLLMRequest) error {
	for _, message := range request.Messages {
		if _, _, err := wire.RestoreContent(message.ProviderExtensions); err != nil {
			return err
		}
		for _, part := range message.Content.MultipleContent {
			switch part.Type {
			case "text", "image_url", "document", "server_tool_use", "server_tool_result":
			default:
				if part.Native == nil || part.Native.Format != model.APIFormatAnthropicMessage {
					return fmt.Errorf("cannot represent %q content in Anthropic Messages", part.Type)
				}
			}
		}
	}
	extension := request.GetAnthropicExtensions()
	for _, field := range []string{"system", "metadata", "thinking", "output_config", "cache_control"} {
		if raw := extension.Fields[field]; len(raw) > 0 && !json.Valid(raw) {
			return fmt.Errorf("invalid Anthropic %s extension", field)
		}
	}
	return nil
}

func restoreRequestFields(result *wire.MessageRequest, extension model.AnthropicExtension) error {
	result.Fields = wire.CopyFields(extension.Fields, "system", "metadata", "thinking", "output_config", "cache_control")
	if raw := extension.Fields["system"]; len(raw) > 0 {
		var system *wire.SystemPrompt
		if err := json.Unmarshal(raw, &system); err != nil {
			return err
		}
		result.System = system
		if system == nil {
			result.Fields["system"] = raw
		}
	}
	if raw := extension.Fields["metadata"]; len(raw) > 0 {
		var metadata *wire.AnthropicMetadata
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return err
		}
		if result.Metadata != nil && metadata != nil {
			metadata.UserID = result.Metadata.UserID
		}
		result.Metadata = metadata
		if metadata == nil {
			result.Fields["metadata"] = raw
		}
	}
	for _, field := range []string{"thinking", "output_config", "cache_control"} {
		if raw := extension.Fields[field]; len(raw) > 0 {
			result.Fields[field] = raw
		}
	}
	return nil
}
