package openairesponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

type InputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format,omitempty"`
}

type FlexibleJSONString string

func (value *FlexibleJSONString) UnmarshalJSON(body []byte) error {
	var text string
	if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		*value = ""
		return nil
	}
	if err := json.Unmarshal(body, &text); err == nil {
		*value = FlexibleJSONString(text)
		return nil
	}
	if !json.Valid(body) {
		return fmt.Errorf("invalid JSON string")
	}
	*value = FlexibleJSONString(body)
	return nil
}

func (value FlexibleJSONString) MarshalJSON() ([]byte, error) { return json.Marshal(string(value)) }
func (value FlexibleJSONString) String() string               { return string(value) }

func mergeUnknownFields(body []byte, original model.ProtocolFields, source any) ([]byte, error) {
	if len(original) == 0 {
		return body, nil
	}
	var fields model.ProtocolFields
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	known := make(map[string]bool)
	typeOf := reflect.TypeOf(source)
	for index := 0; index < typeOf.NumField(); index++ {
		key := strings.Split(typeOf.Field(index).Tag.Get("json"), ",")[0]
		if key != "-" {
			known[key] = true
		}
	}
	for key, value := range original {
		if !known[key] {
			fields[key] = append(json.RawMessage(nil), value...)
		} else if _, present := fields[key]; !present {
			if _, isRequest := source.(Request); isRequest {
				continue
			}
			switch string(bytes.TrimSpace(value)) {
			case "null", "false", "0", "\"\"", "[]", "{}":
				fields[key] = append(json.RawMessage(nil), value...)
			}
		}
	}
	return json.Marshal(fields)
}

func (request *Request) UnmarshalJSON(body []byte) error {
	type plain Request
	var decoded plain
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(body, &decoded.Fields); err != nil {
		return err
	}
	*request = Request(decoded)
	return nil
}

func (request Request) MarshalJSON() ([]byte, error) {
	if request.EncodeError != nil {
		return nil, request.EncodeError
	}
	type plain Request
	body, err := json.Marshal(plain(request))
	if err != nil {
		return nil, err
	}
	return mergeUnknownFields(body, request.Fields, request)
}

func (tool *Tool) UnmarshalJSON(body []byte) error {
	type plain Tool
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(body, &decoded.Fields); err != nil {
		return err
	}
	*tool = Tool(decoded)
	return nil
}

func (tool Tool) MarshalJSON() ([]byte, error) {
	type plain Tool
	body, err := json.Marshal(plain(tool))
	if err != nil {
		return nil, err
	}
	return mergeUnknownFields(body, tool.Fields, tool)
}

func (reasoning *Reasoning) UnmarshalJSON(body []byte) error {
	type plain Reasoning
	var decoded plain
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(body, &decoded.Fields); err != nil {
		return err
	}
	*reasoning = Reasoning(decoded)
	return nil
}

func (reasoning Reasoning) MarshalJSON() ([]byte, error) {
	type plain Reasoning
	body, err := json.Marshal(plain(reasoning))
	if err != nil {
		return nil, err
	}
	return mergeUnknownFields(body, reasoning.Fields, reasoning)
}

func responseCreatedAtSeconds(raw json.RawMessage) int64 {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0
	}
	seconds, err := parseCreatedAtSeconds(number.String())
	if err != nil {
		return 0
	}
	return seconds
}

func responseHasEmptyError(status *string, raw json.RawMessage) bool {
	if status != nil {
		switch strings.ToLower(strings.TrimSpace(*status)) {
		case "failed", "error", "cancelled", "canceled":
			return false
		}
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var fields model.ProtocolFields
	return json.Unmarshal(trimmed, &fields) == nil && len(fields) == 0
}

func (response Response) MarshalJSON() ([]byte, error) {
	type plain Response
	body, err := json.Marshal(plain(response))
	if err != nil {
		return nil, err
	}
	body, err = mergeUnknownFields(body, response.Fields, response)
	if err != nil {
		return nil, err
	}
	var fields model.ProtocolFields
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	if raw := response.Fields["created_at"]; len(raw) > 0 && responseCreatedAtSeconds(raw) == response.CreatedAt {
		fields["created_at"] = raw
	}
	if response.Error == nil && responseHasEmptyError(response.Status, response.Fields["error"]) {
		delete(fields, "error")
	}
	for key, raw := range response.Fields {
		switch key {
		case "id", "model", "created_at", "output", "usage", "status", "error", "incomplete_details":
			continue
		}
		if _, present := fields[key]; !present {
			fields[key] = raw
		}
	}
	return json.Marshal(fields)
}

func (annotation Annotation) MarshalJSON() ([]byte, error) {
	type plain Annotation
	if annotation.URLCitation != nil {
		if annotation.URL == nil {
			annotation.URL = &annotation.URLCitation.URL
		}
		if annotation.Title == nil {
			annotation.Title = &annotation.URLCitation.Title
		}
		annotation.URLCitation = nil
	}
	body, err := json.Marshal(plain(annotation))
	if err != nil {
		return nil, err
	}
	return mergeUnknownFields(body, annotation.Fields, annotation)
}

func (detail *Error) UnmarshalJSON(body []byte) error {
	type plain Error
	var decoded plain
	var wire struct {
		*plain
		Code json.RawMessage `json:"code"`
	}
	wire.plain = &decoded
	if err := json.Unmarshal(body, &wire); err != nil {
		return err
	}
	if err := json.Unmarshal(body, &decoded.Fields); err != nil {
		return err
	}
	if len(wire.Code) > 0 && !bytes.Equal(wire.Code, []byte("null")) {
		if err := json.Unmarshal(wire.Code, &decoded.Code); err != nil {
			var number json.Number
			if err := json.Unmarshal(wire.Code, &number); err != nil {
				return err
			}
			decoded.Code = number.String()
		}
	}
	*detail = Error(decoded)
	return nil
}

func (detail Error) MarshalJSON() ([]byte, error) {
	type plain Error
	body, err := json.Marshal(plain(detail))
	if err != nil {
		return nil, err
	}
	return mergeUnknownFields(body, detail.Fields, detail)
}
