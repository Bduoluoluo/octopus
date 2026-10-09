package openaichat

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func (value OpenAIError) MarshalJSON() ([]byte, error) { return json.Marshal(value.Detail) }

func (value *OpenAIError) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &value.Detail)
}

func (value *Choice) UnmarshalJSON(data []byte) error {
	type plain Choice
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = Choice(decoded)
	value.Fields = fields
	return nil
}

func (value Choice) MarshalJSON() ([]byte, error) {
	type plain Choice
	return encodeFields(plain(value), value.Fields)
}

func decodeFields(data []byte, target any) (model.ProtocolFields, error) {
	if err := json.Unmarshal(data, target); err != nil {
		return nil, err
	}
	var fields model.ProtocolFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	shape := reflect.TypeOf(target).Elem()
	for index := 0; index < shape.NumField(); index++ {
		name := strings.Split(shape.Field(index).Tag.Get("json"), ",")[0]
		if !bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			delete(fields, name)
		}
	}
	return fields, nil
}

func encodeFields(value any, extra model.ProtocolFields) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(extra) == 0 {
		return data, err
	}
	var fields model.ProtocolFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, raw := range extra {
		if _, exists := fields[key]; !exists {
			fields[key] = raw
		}
	}
	return json.Marshal(fields)
}

func chatExtensions(fields model.ProtocolFields) *model.ProviderExtensions {
	if len(fields) == 0 {
		return nil
	}
	return &model.ProviderExtensions{OpenAIChat: &model.ProtocolExtension{Fields: fields}}
}

func chatFields(extension *model.ProviderExtensions) model.ProtocolFields {
	if extension == nil || extension.OpenAIChat == nil {
		return nil
	}
	return extension.OpenAIChat.Fields
}

func (value *Request) UnmarshalJSON(data []byte) error {
	type plain Request
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = Request(decoded)
	value.Fields = fields
	return nil
}

func (value Request) MarshalJSON() ([]byte, error) {
	type plain Request
	return encodeFields(plain(value), value.Fields)
}

func (value *Message) UnmarshalJSON(data []byte) error {
	type plain Message
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = Message(decoded)
	value.Fields = fields
	return nil
}

func (value Message) MarshalJSON() ([]byte, error) {
	type plain Message
	return encodeFields(plain(value), value.Fields)
}

func (value *Annotation) UnmarshalJSON(data []byte) error {
	type plain Annotation
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = Annotation(decoded)
	value.Fields = fields
	return nil
}

func (value Annotation) MarshalJSON() ([]byte, error) {
	type plain Annotation
	return encodeFields(plain(value), value.Fields)
}

func (value *URLCitation) UnmarshalJSON(data []byte) error {
	type plain URLCitation
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = URLCitation(decoded)
	value.Fields = fields
	return nil
}

func (value URLCitation) MarshalJSON() ([]byte, error) {
	type plain URLCitation
	return encodeFields(plain(value), value.Fields)
}

func (value *MessageContentPart) UnmarshalJSON(data []byte) error {
	type plain MessageContentPart
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = MessageContentPart(decoded)
	value.Fields = fields
	return nil
}

func (value MessageContentPart) MarshalJSON() ([]byte, error) {
	type plain MessageContentPart
	return encodeFields(plain(value), value.Fields)
}

func (value *Response) UnmarshalJSON(data []byte) error {
	type plain Response
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = Response(decoded)
	value.Fields = fields
	return nil
}

func (value Response) MarshalJSON() ([]byte, error) {
	type plain Response
	return encodeFields(plain(value), value.Fields)
}

func (value *Tool) UnmarshalJSON(data []byte) error {
	type plain Tool
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = Tool(decoded)
	value.Fields = fields
	return nil
}

func (value Tool) MarshalJSON() ([]byte, error) {
	type plain Tool
	return encodeFields(plain(value), value.Fields)
}

func (value *Function) UnmarshalJSON(data []byte) error {
	type plain Function
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = Function(decoded)
	value.Fields = fields
	return nil
}

func (value Function) MarshalJSON() ([]byte, error) {
	type plain Function
	return encodeFields(plain(value), value.Fields)
}

func (value *FunctionCall) UnmarshalJSON(data []byte) error {
	type plain FunctionCall
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = FunctionCall(decoded)
	value.Fields = fields
	return nil
}

func (value FunctionCall) MarshalJSON() ([]byte, error) {
	type plain FunctionCall
	return encodeFields(plain(value), value.Fields)
}

func (value *ToolCall) UnmarshalJSON(data []byte) error {
	type plain ToolCall
	var decoded plain
	fields, err := decodeFields(data, &decoded)
	if err != nil {
		return err
	}
	*value = ToolCall(decoded)
	value.Fields = fields
	return nil
}

func (value ToolCall) MarshalJSON() ([]byte, error) {
	type plain ToolCall
	return encodeFields(plain(value), value.Fields)
}
