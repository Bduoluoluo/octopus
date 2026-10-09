package openairesponses

type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	InputTokenDetails struct {
		// CacheWriteTokens is the number of input tokens written to the prompt cache.
		CacheWriteTokens int64 `json:"cache_write_tokens"`
		// CachedTokens is the number of input tokens retrieved from the prompt cache.
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens       int64 `json:"output_tokens"`
	OutputTokenDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	TotalTokens int64 `json:"total_tokens"`
}
