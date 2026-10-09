package openairesponses

import (
	"encoding/json"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

type Usage struct {
	Fields            model.ProtocolFields `json:"-"`
	InputTokens       int64                `json:"input_tokens"`
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

func (usage *Usage) UnmarshalJSON(body []byte) error {
	type plain Usage
	var decoded plain
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(body, &decoded.Fields); err != nil {
		return err
	}
	*usage = Usage(decoded)
	return nil
}

func (usage Usage) MarshalJSON() ([]byte, error) {
	type plain Usage
	body, err := json.Marshal(plain(usage))
	if err != nil {
		return nil, err
	}
	var fields model.ProtocolFields
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	for key, raw := range usage.Fields {
		switch key {
		case "input_tokens_details", "output_tokens_details":
			var original, current model.ProtocolFields
			if err := json.Unmarshal(raw, &original); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(fields[key], &current); err != nil {
				return nil, err
			}
			for name, value := range original {
				if _, exists := current[name]; !exists {
					current[name] = value
				}
			}
			fields[key], err = json.Marshal(current)
			if err != nil {
				return nil, err
			}
		default:
			if _, exists := fields[key]; !exists {
				fields[key] = raw
			}
		}
	}
	return json.Marshal(fields)
}
