package outbound

import (
	"encoding/json"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func stringPtr(value string) *string { return &value }

func anthropicCacheRequest(latestUser string) *model.InternalLLMRequest {
	system := "You are helpful."
	previousUser := "Summarize this repository."
	assistant := "I can help with that."
	return &model.InternalLLMRequest{
		Model:        "gpt-5.4",
		RawAPIFormat: model.APIFormatAnthropicMessage,
		Messages: []model.Message{
			{Role: "system", Content: model.MessageContent{Content: &system}, CacheControl: &model.CacheControl{Type: model.CacheControlTypeEphemeral, TTL: model.CacheTTL5m}},
			{Role: "user", Content: model.MessageContent{Content: &previousUser}, CacheControl: &model.CacheControl{Type: model.CacheControlTypeEphemeral, TTL: model.CacheTTL5m}},
			{Role: "assistant", Content: model.MessageContent{Content: &assistant}},
			{Role: "user", Content: model.MessageContent{Content: &latestUser}},
		},
		Tools: []model.Tool{{Type: "function", Function: model.Function{Name: "lookup", Description: "Lookup repository information", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)}, CacheControl: &model.CacheControl{Type: model.CacheControlTypeEphemeral, TTL: model.CacheTTL5m}}},
	}
}
