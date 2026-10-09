package model

import (
	"encoding/json"
	"reflect"
)

type StreamFrame struct {
	Event string `json:"event,omitempty"`
	Data  []byte `json:"data,omitempty"`
	ID    string `json:"id,omitempty"`
}

type ProtocolFields map[string]json.RawMessage

type ProtocolExtension struct {
	Fields ProtocolFields `json:"fields,omitempty"`
	Items  []ProtocolItem `json:"items,omitempty"`
}

type ProtocolItem struct {
	Format   APIFormat       `json:"format"`
	Position int             `json:"position"`
	Raw      json.RawMessage `json:"raw"`
}

type ContentCitation struct {
	Type              string         `json:"type"`
	URL               *string        `json:"url,omitempty"`
	Title             *string        `json:"title,omitempty"`
	CitedText         *string        `json:"cited_text,omitempty"`
	DocumentIndex     *int64         `json:"document_index,omitempty"`
	DocumentTitle     *string        `json:"document_title,omitempty"`
	StartCharIndex    *int64         `json:"start_char_index,omitempty"`
	EndCharIndex      *int64         `json:"end_char_index,omitempty"`
	StartPageNumber   *int64         `json:"start_page_number,omitempty"`
	EndPageNumber     *int64         `json:"end_page_number,omitempty"`
	StartBlockIndex   *int64         `json:"start_block_index,omitempty"`
	EndBlockIndex     *int64         `json:"end_block_index,omitempty"`
	SearchResultIndex *int64         `json:"search_result_index,omitempty"`
	Source            *string        `json:"source,omitempty"`
	EncryptedIndex    *string        `json:"encrypted_index,omitempty"`
	StartIndex        *int64         `json:"start_index,omitempty"`
	EndIndex          *int64         `json:"end_index,omitempty"`
	Fields            ProtocolFields `json:"-"`
}

func (citation *ContentCitation) UnmarshalJSON(data []byte) error {
	type plain ContentCitation
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &decoded.Fields); err != nil {
		return err
	}
	*citation = ContentCitation(decoded)
	return nil
}

func (citation ContentCitation) MarshalJSON() ([]byte, error) {
	type plain ContentCitation
	body, err := json.Marshal(plain(citation))
	if err != nil {
		return nil, err
	}
	var fields ProtocolFields
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	for key, value := range citation.Fields {
		if _, exists := fields[key]; !exists {
			fields[key] = value
		}
	}
	return json.Marshal(fields)
}

func CloneRequest(request *InternalLLMRequest) *InternalLLMRequest {
	if request == nil {
		return nil
	}
	return cloneProtocolValue(reflect.ValueOf(request)).Interface().(*InternalLLMRequest)
}

func cloneProtocolFields(fields ProtocolFields) ProtocolFields {
	if fields == nil {
		return nil
	}
	cloned := make(ProtocolFields, len(fields))
	for key, value := range fields {
		cloned[key] = cloneRawMessage(value)
	}
	return cloned
}

func cloneProtocolItems(items []ProtocolItem) []ProtocolItem {
	if items == nil {
		return nil
	}
	cloned := make([]ProtocolItem, len(items))
	for index, item := range items {
		cloned[index] = item
		cloned[index].Raw = cloneRawMessage(item.Raw)
	}
	return cloned
}

func cloneProtocolExtension(extension *ProtocolExtension) *ProtocolExtension {
	if extension == nil {
		return nil
	}
	return &ProtocolExtension{Fields: cloneProtocolFields(extension.Fields), Items: cloneProtocolItems(extension.Items)}
}

func cloneProtocolValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type().Elem())
		cloned.Elem().Set(cloneProtocolValue(value.Elem()))
		return cloned
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(cloneProtocolValue(value.Elem()))
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			cloned.Index(index).Set(cloneProtocolValue(value.Index(index)))
		}
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			cloned.SetMapIndex(iterator.Key(), cloneProtocolValue(iterator.Value()))
		}
		return cloned
	case reflect.Struct:
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(value)
		for index := 0; index < value.NumField(); index++ {
			if cloned.Field(index).CanSet() && value.Field(index).CanInterface() {
				cloned.Field(index).Set(cloneProtocolValue(value.Field(index)))
			}
		}
		return cloned
	default:
		return value
	}
}
