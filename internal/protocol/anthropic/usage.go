package anthropic

type Usage struct {
	// The number of input tokens which were used to bill.
	InputTokens int64 `json:"input_tokens"`

	// The number of output tokens which were used.
	OutputTokens int64 `json:"output_tokens"`

	// The number of input tokens used to create the cache entry.
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`

	// The number of input tokens read from the cache.
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`

	// CacheCreation is the breakdown of cached tokens by TTL
	CacheCreation CacheCreation `json:"cache_creation"`

	// Available options: standard, priority, batch
	ServiceTier string `json:"service_tier,omitempty"`

	// For moonshot anthropic endpoint, it uses cached tokens instead of cache read input tokens.
	CachedTokens int64 `json:"cached_tokens,omitempty"`
}

type CacheCreation struct {
	Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
}
