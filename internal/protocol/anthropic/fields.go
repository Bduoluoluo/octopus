package anthropic

import (
	"encoding/json"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func DecodeFields(data []byte, target any, fields *model.ProtocolFields) error {
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	return json.Unmarshal(data, fields)
}

func EncodeFields(value any, original model.ProtocolFields) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields model.ProtocolFields
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	for key, raw := range original {
		if _, exists := fields[key]; !exists {
			fields[key] = raw
		}
	}
	return json.Marshal(fields)
}

func CopyFields(fields model.ProtocolFields, excluded ...string) model.ProtocolFields {
	result := make(model.ProtocolFields, len(fields))
	for key, raw := range fields {
		result[key] = append(json.RawMessage(nil), raw...)
	}
	for _, key := range excluded {
		delete(result, key)
	}
	return result
}

func (request *MessageRequest) UnmarshalJSON(data []byte) error {
	type plain MessageRequest
	var value plain
	if err := DecodeFields(data, &value, &value.Fields); err != nil {
		return err
	}
	*request = MessageRequest(value)
	return nil
}

func (request MessageRequest) MarshalJSON() ([]byte, error) {
	type plain MessageRequest
	return EncodeFields(plain(request), request.Fields)
}

func (message *Message) UnmarshalJSON(data []byte) error {
	type plain Message
	var value plain
	if err := DecodeFields(data, &value, &value.Fields); err != nil {
		return err
	}
	*message = Message(value)
	return nil
}

func (message Message) MarshalJSON() ([]byte, error) {
	type plain Message
	return EncodeFields(plain(message), message.Fields)
}

func (message *MessageParam) UnmarshalJSON(data []byte) error {
	type plain MessageParam
	var value plain
	if err := DecodeFields(data, &value, &value.Fields); err != nil {
		return err
	}
	*message = MessageParam(value)
	return nil
}

func (message MessageParam) MarshalJSON() ([]byte, error) {
	type plain MessageParam
	return EncodeFields(plain(message), message.Fields)
}

func (part *SystemPromptPart) UnmarshalJSON(data []byte) error {
	type plain SystemPromptPart
	var value plain
	if err := DecodeFields(data, &value, &value.Fields); err != nil {
		return err
	}
	*part = SystemPromptPart(value)
	return nil
}

func (part SystemPromptPart) MarshalJSON() ([]byte, error) {
	type plain SystemPromptPart
	return EncodeFields(plain(part), part.Fields)
}

func (source *ImageSource) UnmarshalJSON(data []byte) error {
	type plain ImageSource
	var value plain
	if err := DecodeFields(data, &value, &value.Fields); err != nil {
		return err
	}
	*source = ImageSource(value)
	return nil
}

func (source ImageSource) MarshalJSON() ([]byte, error) {
	type plain ImageSource
	return EncodeFields(plain(source), source.Fields)
}

func (metadata *AnthropicMetadata) UnmarshalJSON(data []byte) error {
	type plain AnthropicMetadata
	var value plain
	if err := DecodeFields(data, &value, &value.Fields); err != nil {
		return err
	}
	*metadata = AnthropicMetadata(value)
	return nil
}

func (metadata AnthropicMetadata) MarshalJSON() ([]byte, error) {
	type plain AnthropicMetadata
	return EncodeFields(plain(metadata), metadata.Fields)
}

func (event StreamEvent) MarshalJSON() ([]byte, error) {
	type plain StreamEvent
	if event.Type != "message_delta" || event.Usage == nil || event.Usage.Fields != nil {
		return json.Marshal(plain(event))
	}
	usage := struct {
		InputTokens              int64          `json:"input_tokens,omitempty"`
		OutputTokens             int64          `json:"output_tokens"`
		CacheCreationInputTokens int64          `json:"cache_creation_input_tokens,omitempty"`
		CacheReadInputTokens     int64          `json:"cache_read_input_tokens,omitempty"`
		CacheCreation            *CacheCreation `json:"cache_creation,omitempty"`
	}{event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.CacheCreationInputTokens, event.Usage.CacheReadInputTokens, event.Usage.CacheCreation}
	return json.Marshal(struct {
		plain
		Usage any `json:"usage"`
	}{plain(event), usage})
}
