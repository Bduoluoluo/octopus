package anthropic

import (
	"encoding/json"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

type Usage struct {
	Fields model.ProtocolFields `json:"-"`
	// The number of input tokens which were used to bill.
	InputTokens int64 `json:"input_tokens"`

	// The number of output tokens which were used.
	OutputTokens int64 `json:"output_tokens"`

	// The number of input tokens used to create the cache entry.
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`

	// The number of input tokens read from the cache.
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`

	// CacheCreation is the breakdown of cached tokens by TTL
	CacheCreation *CacheCreation `json:"cache_creation,omitempty"`

	// Available options: standard, priority, batch
	ServiceTier string `json:"service_tier,omitempty"`

	// For moonshot anthropic endpoint, it uses cached tokens instead of cache read input tokens.
	CachedTokens int64 `json:"cached_tokens,omitempty"`
}

func (usage *Usage) UnmarshalJSON(data []byte) error {
	type plain Usage
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &decoded.Fields); err != nil {
		return err
	}
	*usage = Usage(decoded)
	return nil
}

func MergeUsage(previous, next *Usage) *Usage {
	if previous == nil {
		return next
	}
	if next == nil {
		return previous
	}
	merged := *previous
	if _, exists := next.Fields["input_tokens"]; exists {
		merged.InputTokens = next.InputTokens
	}
	if _, exists := next.Fields["output_tokens"]; exists {
		merged.OutputTokens = next.OutputTokens
	}
	if _, exists := next.Fields["cache_read_input_tokens"]; exists {
		merged.CacheReadInputTokens = next.CacheReadInputTokens
	}
	if _, exists := next.Fields["cache_creation_input_tokens"]; exists {
		merged.CacheCreationInputTokens = next.CacheCreationInputTokens
	}
	if _, exists := next.Fields["cache_creation"]; exists {
		merged.CacheCreation = next.CacheCreation
	}
	if _, exists := next.Fields["service_tier"]; exists {
		merged.ServiceTier = next.ServiceTier
	}
	return &merged
}

type CacheCreation struct {
	Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
}
