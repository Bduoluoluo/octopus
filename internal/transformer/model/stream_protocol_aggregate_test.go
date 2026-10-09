package model_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/tmaxmax/go-sse"
	anthropicInbound "github.com/xuanli27/octopus/internal/protocol/anthropic/inbound"
	anthropicOutbound "github.com/xuanli27/octopus/internal/protocol/anthropic/outbound"
	responsesInbound "github.com/xuanli27/octopus/internal/protocol/openairesponses/inbound"
	responsesOutbound "github.com/xuanli27/octopus/internal/protocol/openairesponses/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

type fragmentReader struct{ reader io.Reader }

func (reader fragmentReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 3 {
		buffer = buffer[:3]
	}
	return reader.reader.Read(buffer)
}

func aggregateFrames(t *testing.T, decoder model.OutboundStreamFrameTransformer, encoder model.Inbound, frames []string, batch bool) *model.InternalLLMResponse {
	t.Helper()
	var stream bytes.Buffer
	for _, frame := range frames {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(frame), &envelope); err != nil {
			t.Fatal(err)
		}
		stream.WriteString("event: " + envelope.Type + "\r\ndata: " + frame + "\r\n\r\n")
	}
	var pending []model.StreamEvent
	consumer := encoder.(model.InboundStreamEventTransformer)
	for frame, err := range sse.Read(fragmentReader{reader: &stream}, &sse.ReadConfig{MaxEventSize: 1 << 20}) {
		if err != nil {
			t.Fatal(err)
		}
		events, err := decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Event: frame.Type, Data: []byte(frame.Data)})
		if err != nil {
			t.Fatal(err)
		}
		if batch {
			pending = append(pending, events...)
		} else if _, err := consumer.TransformStreamEvents(context.Background(), events); err != nil {
			t.Fatal(err)
		}
	}
	ended, err := decoder.EndStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pending = append(pending, ended...)
	if _, err := consumer.TransformStreamEvents(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	response, err := encoder.GetInternalResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Error != nil {
		t.Fatalf("failed aggregate: %#v", response)
	}
	if err := decoder.CloseStream(); err != nil {
		t.Fatal(err)
	}
	return response
}

func stringField(t *testing.T, raw json.RawMessage, field string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := json.Unmarshal(fields[field], &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAnthropicSSEAggregateOrderedNativeReasoningAndCitations(t *testing.T) {
	frames := []string{
		`{"type":"message_start","message":{"id":"msg","model":"fixture","role":"assistant","content":[],"usage":{"input_tokens":4,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"","citations":null,"future":false}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"char_location","document_index":0,"start_char_index":0,"end_char_index":5,"future":0}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"plan "}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"one"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig-"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"one"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tool-a","name":"alpha","input":{}}}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"tool-b","name":"beta","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"b\":0}"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"1}"}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":4,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"thinking_delta","thinking":"second plan"}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"signature_delta","signature":"sig-two"}}`,
		`{"type":"content_block_stop","index":4}`,
		`{"type":"content_block_start","index":5,"content_block":{"type":"redacted_thinking","data":"opaque"}}`,
		`{"type":"content_block_stop","index":5}`,
		`{"type":"content_block_start","index":6,"content_block":{"type":"web_search_tool_result","tool_use_id":"search","content":[{"type":"web_search_result","url":"https://example.test","encrypted_content":"opaque-search","future":9007199254740993}]}}`,
		`{"type":"content_block_stop","index":6}`,
		`{"type":"content_block_start","index":7,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":7,"delta":{"type":"text_delta","text":"last"}}`,
		`{"type":"content_block_delta","index":7,"delta":{"type":"citations_delta","citation":{"type":"page_location","document_index":1,"start_page_number":0,"end_page_number":2}}}`,
		`{"type":"content_block_stop","index":7}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`,
		`{"type":"message_stop"}`,
	}
	for _, batch := range []bool{false, true} {
		response := aggregateFrames(t, &anthropicOutbound.MessageOutbound{}, &anthropicInbound.MessagesInbound{}, frames, batch)
		if len(response.Choices) != 1 {
			t.Fatalf("choices=%#v", response.Choices)
		}
		message := response.Choices[0].Message
		if len(message.ReasoningBlocks) != 3 || message.ReasoningBlocks[0].Text != "plan one" || message.ReasoningBlocks[0].Signature != "sig-one" || message.ReasoningBlocks[1].Text != "second plan" || message.ReasoningBlocks[1].Signature != "sig-two" || message.ReasoningBlocks[2].Data != "opaque" {
			t.Fatalf("reasoning mixed: %#v", message.ReasoningBlocks)
		}
		if len(message.ToolCalls) != 2 || message.ToolCalls[0].Function.Arguments != `{"a":1}` || message.ToolCalls[1].Function.Arguments != `{"b":0}` {
			t.Fatalf("tools mixed: %#v", message.ToolCalls)
		}
		items := message.ProviderExtensions.Anthropic.Items
		wantKinds := []string{"text", "thinking", "tool_use", "tool_use", "thinking", "redacted_thinking", "web_search_tool_result", "text"}
		if len(items) != len(wantKinds) {
			t.Fatalf("native count=%d", len(items))
		}
		for index, item := range items {
			if item.Position != index || stringField(t, item.Raw, "type") != wantKinds[index] {
				t.Fatalf("native order lost: %#v", items)
			}
		}
		if stringField(t, items[1].Raw, "signature") != "sig-one" || stringField(t, items[1].Raw, "thinking") != "plan one" {
			t.Fatalf("native thinking not accumulated: %s", items[1].Raw)
		}
		parts := message.Content.MultipleContent
		if len(parts) != 3 || *parts[0].Text != "first" || parts[1].Native == nil || *parts[2].Text != "last" || len(parts[0].Citations) != 1 || len(parts[2].Citations) != 1 || *parts[0].Citations[0].DocumentIndex != 0 || *parts[2].Citations[0].StartPageNumber != 0 {
			t.Fatalf("content/citations lost: %#v", parts)
		}
		if response.Usage == nil || response.Usage.PromptTokens != 4 || response.Usage.CompletionTokens != 10 {
			t.Fatalf("usage=%#v", response.Usage)
		}
		encoded, err := (&anthropicInbound.MessagesInbound{}).TransformResponse(context.Background(), response)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `9007199254740993`) || strings.Count(string(encoded), `"signature":"sig-one"`) != 1 {
			t.Fatalf("native output changed: %s", encoded)
		}
	}
}

func TestResponsesSSEAggregateSeparateOutputIndicesAndNativeSnapshots(t *testing.T) {
	frames := []string{
		`{"type":"response.created","response":{"id":"resp","model":"fixture","created_at":123,"status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"r0","summary":[]}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"r0","summary_index":0,"delta":"plan "}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"r0","summary_index":0,"delta":"one"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"r0","summary":[{"type":"summary_text","text":"plan one"}],"encrypted_content":"opaque-one"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"m1","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":1,"item_id":"m1","content_index":0,"delta":"first"}`,
		`{"type":"response.output_text.annotation.added","output_index":1,"item_id":"m1","content_index":0,"annotation":{"type":"url_citation","url":"https://first.test","start_index":0,"end_index":5}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"reasoning","id":"r2","summary":[]}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":2,"item_id":"r2","summary_index":0,"delta":"plan two"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"reasoning","id":"r2","summary":[{"type":"summary_text","text":"plan two"}],"encrypted_content":"opaque-two"}}`,
		`{"type":"response.output_item.added","output_index":3,"item":{"type":"message","id":"m3","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":3,"item_id":"m3","content_index":0,"delta":"second"}`,
		`{"type":"response.output_text.annotation.added","output_index":3,"item_id":"m3","content_index":0,"annotation":{"type":"url_citation","url":"https://second.test","start_index":0,"end_index":6}}`,
		`{"type":"response.output_item.added","output_index":4,"item":{"type":"image_generation_call","id":"img","status":"in_progress"}}`,
		`{"type":"response.output_item.done","output_index":4,"item":{"type":"image_generation_call","id":"img","status":"completed","result":"encoded","future":false}}`,
		`{"type":"response.output_item.done","output_index":4,"item":{"type":"image_generation_call","id":"img","status":"completed","result":"encoded","future":false}}`,
		`{"type":"response.completed","response":{"id":"resp","model":"fixture","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}`,
	}
	for _, batch := range []bool{false, true} {
		response := aggregateFrames(t, &responsesOutbound.ResponseOutbound{}, &responsesInbound.ResponseInbound{}, frames, batch)
		message := response.Choices[0].Message
		if len(message.ReasoningBlocks) != 2 || message.ReasoningBlocks[0].Text != "plan one" || message.ReasoningBlocks[0].Signature != "opaque-one" || message.ReasoningBlocks[1].Text != "plan two" || message.ReasoningBlocks[1].Signature != "opaque-two" {
			t.Fatalf("reasoning=%#v", message.ReasoningBlocks)
		}
		parts := message.Content.MultipleContent
		if len(parts) != 2 || *parts[0].Text != "first" || *parts[1].Text != "second" || len(parts[0].Citations) != 1 || len(parts[1].Citations) != 1 || *parts[0].Citations[0].URL != "https://first.test" || *parts[1].Citations[0].URL != "https://second.test" {
			t.Fatalf("output/content indices collided: %#v", parts)
		}
		var raw []json.RawMessage
		if err := json.Unmarshal(response.RawResponsesOutputItems, &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 5 || stringField(t, raw[4], "result") != "encoded" || strings.Count(string(response.RawResponsesOutputItems), `"id":"img"`) != 1 {
			t.Fatalf("duplicated raw output: %s", response.RawResponsesOutputItems)
		}
		if len(response.ProviderExtensions.OpenAIResponses.Items) != 5 || response.Status != "completed" || response.Usage.TotalTokens != 8 {
			t.Fatalf("metadata/snapshot lost: %#v", response)
		}
	}
}

func TestNativeOnlyAggregationAndSnapshotReplacement(t *testing.T) {
	var aggregate model.StreamAggregator
	for _, raw := range []string{`{"type":"image_generation_call","id":"native","status":"in_progress"}`, `{"type":"image_generation_call","id":"native","status":"completed","result":"done"}`} {
		aggregate.Add(model.InternalResponseFromStreamEvents([]model.StreamEvent{{Kind: model.StreamEventKindNativeItem, NativeItem: &model.ProtocolItem{Format: model.APIFormatOpenAIResponse, Position: 3, Raw: json.RawMessage(raw)}}}))
	}
	first, second := aggregate.Response(), aggregate.Response()
	if first == nil || first.Error != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("unstable aggregate: %#v %#v", first, second)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(first.RawResponsesOutputItems, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || stringField(t, raw[0], "result") != "done" {
		t.Fatalf("native snapshot lost: %s", first.RawResponsesOutputItems)
	}
	aggregate.BuildAndReset()
	if aggregate.Response() != nil {
		t.Fatal("reset retained native items")
	}
}
