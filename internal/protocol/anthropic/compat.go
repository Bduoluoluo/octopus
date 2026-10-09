package anthropic

import (
	"encoding/json"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

type ProviderExtensions = model.ProviderExtensions
type GeminiExtension = model.GeminiExtension
type DocumentCitationsControl = CitationConfig
type CacheCreationUsage = CacheCreation

const (
	ThinkingTypeEnabled       = "enabled"
	ThinkingTypeDisabled      = "disabled"
	ThinkingTypeAdaptive      = "adaptive"
	EffortMax                 = "max"
	EffortXHigh               = "xhigh"
	EffortHigh                = "high"
	EffortMedium              = "medium"
	EffortLow                 = "low"
	ThinkingDisplaySummarized = "summarized"
	ThinkingDisplayOmitted    = "omitted"
)

func (tool Tool) IsServerTool() bool {
	return tool.Type != "" && tool.Type != "function" && tool.Type != "custom"
}

func (tool *Tool) UnmarshalJSON(data []byte) error {
	type plain Tool
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*tool = Tool(decoded)
	tool.RawBody = append(json.RawMessage(nil), data...)
	return nil
}

func (tool Tool) MarshalJSON() ([]byte, error) {
	type plain Tool
	encoded, err := json.Marshal(plain(tool))
	if err != nil {
		return nil, err
	}
	var fields model.ProtocolFields
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	if len(tool.RawBody) > 0 {
		var original model.ProtocolFields
		if err := json.Unmarshal(tool.RawBody, &original); err != nil {
			return nil, err
		}
		for key, value := range original {
			if _, exists := fields[key]; !exists {
				fields[key] = value
			}
		}
	}
	return json.Marshal(fields)
}
