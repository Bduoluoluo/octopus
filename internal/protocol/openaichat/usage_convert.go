package openaichat

import "github.com/xuanli27/octopus/internal/transformer/model"

func (u *Usage) ToLLMUsage() *model.Usage {
	if u == nil {
		return nil
	}

	usage := &model.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}

	if u.PromptTokensDetails != nil {
		usage.PromptTokensDetails = &model.PromptTokensDetails{
			AudioTokens:       u.PromptTokensDetails.AudioTokens,
			CachedTokens:      u.PromptTokensDetails.CachedTokens,
			WriteCachedTokens: u.PromptTokensDetails.WriteCachedTokens,
		}
	}

	// Some OpenAI-compatible providers (e.g. SGLang) report reasoning tokens as a
	// top-level `reasoning_tokens` field instead of the nested
	// completion_tokens_details. Prefer the nested value (the OpenAI standard used by
	// OpenAI/DeepSeek/Gemini), falling back to the top-level value only when the nested
	// value is absent (zero).
	reasoningTokens := int64(0)
	if u.CompletionTokensDetails != nil {
		reasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	if reasoningTokens == 0 {
		reasoningTokens = u.ReasoningTokens
	}

	if u.CompletionTokensDetails != nil || reasoningTokens != 0 {
		details := CompletionTokensDetails{}
		if u.CompletionTokensDetails != nil {
			details = *u.CompletionTokensDetails
		}
		usage.CompletionTokensDetails = &model.CompletionTokensDetails{
			AudioTokens:              details.AudioTokens,
			ReasoningTokens:          reasoningTokens,
			AcceptedPredictionTokens: details.AcceptedPredictionTokens,
			RejectedPredictionTokens: details.RejectedPredictionTokens,
		}
	}

	if (usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens == 0) && u.CachedTokens > 0 {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &model.PromptTokensDetails{}
		}

		usage.PromptTokensDetails.CachedTokens = u.CachedTokens
	}

	return usage
}

// UsageFromLLM creates OpenAI Usage from unified model.Usage.
func UsageFromLLM(u *model.Usage) *Usage {
	if u == nil {
		return nil
	}

	usage := &Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}

	if u.PromptTokensDetails != nil {
		usage.PromptTokensDetails = &PromptTokensDetails{
			AudioTokens:       u.PromptTokensDetails.AudioTokens,
			CachedTokens:      u.PromptTokensDetails.CachedTokens,
			WriteCachedTokens: u.PromptTokensDetails.WriteCachedTokens,
		}
	}

	if u.CompletionTokensDetails != nil {
		usage.CompletionTokensDetails = &CompletionTokensDetails{
			AudioTokens:              u.CompletionTokensDetails.AudioTokens,
			ReasoningTokens:          u.CompletionTokensDetails.ReasoningTokens,
			AcceptedPredictionTokens: u.CompletionTokensDetails.AcceptedPredictionTokens,
			RejectedPredictionTokens: u.CompletionTokensDetails.RejectedPredictionTokens,
		}
	}

	return usage
}
