package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func (config *CitationConfig) UnmarshalJSON(data []byte) error {
	type plain CitationConfig
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &decoded.Fields); err != nil {
		return err
	}
	*config = CitationConfig(decoded)
	return nil
}

func (config CitationConfig) MarshalJSON() ([]byte, error) {
	fields := make(model.ProtocolFields, len(config.Fields)+1)
	for key, value := range config.Fields {
		fields[key] = value
	}
	if config.Enabled != nil {
		encoded, err := json.Marshal(*config.Enabled)
		if err != nil {
			return nil, err
		}
		fields["enabled"] = encoded
	}
	return json.Marshal(fields)
}

func (block *MessageContentBlock) UnmarshalJSON(data []byte) error {
	type plain MessageContentBlock
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &decoded.Fields); err != nil {
		return err
	}
	if decoded.Fields == nil {
		return fmt.Errorf("content block must be an object")
	}
	if raw, exists := decoded.Fields["citations"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		switch decoded.Type {
		case "document":
			if err := json.Unmarshal(raw, &decoded.CitationConfig); err != nil {
				return fmt.Errorf("document citations must be a configuration object: %w", err)
			}
		case "text":
			if err := json.Unmarshal(raw, &decoded.Citations); err != nil {
				return fmt.Errorf("text citations must be an array: %w", err)
			}
		}
	}
	*block = MessageContentBlock(decoded)
	return nil
}

func (block MessageContentBlock) MarshalJSON() ([]byte, error) {
	encoded, err := block.marshalKnownFields()
	if err != nil {
		return nil, err
	}
	var fields model.ProtocolFields
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	for key, value := range block.Fields {
		if _, exists := fields[key]; !exists {
			fields[key] = value
		}
	}
	switch block.Type {
	case "document":
		if block.CitationConfig != nil {
			fields["citations"], err = json.Marshal(block.CitationConfig)
		}
	case "text":
		if block.Citations != nil {
			fields["citations"], err = json.Marshal(block.Citations)
		}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}
