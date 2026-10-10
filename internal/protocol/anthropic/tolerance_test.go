package anthropic_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/samber/lo"
	inbound "github.com/xuanli27/octopus/internal/protocol/anthropic/inbound"
	outbound "github.com/xuanli27/octopus/internal/protocol/anthropic/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func TestNativeStreamToleranceRetainsFramesAndError(t *testing.T) {
	decoder, encoder := &outbound.MessageOutbound{}, &inbound.MessagesInbound{}
	frames := []string{
		`{"type":"message_start","message":{"id":"msg","model":"fixture","usage":{"input_tokens":4,"output_tokens":0},"future":false}}`,
		`{"type":"message_start","message":{"id":"msg","model":"fixture","usage":{"input_tokens":4,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"hello","citations":{"future":true}},"future":0}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"future_delta","future":{"value":9007199254740993}}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":" again"},"error":{}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3},"future":null}`,
		`{"type":"message_stop"}`,
		`{"type":"message_delta","usage":{"output_tokens":4}}`,
		"{\n\"type\":\"future_event\",\n\"payload\":false\n}",
	}
	for _, body := range frames {
		events, err := decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Event: "message", ID: "event-id", Data: []byte(body)})
		if err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
		output, err := encoder.TransformStreamEvents(context.Background(), events)
		if err != nil {
			t.Fatalf("encode %s: %v", body, err)
		}
		var data []string
		for _, line := range strings.Split(string(output), "\n") {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(line, "data:"))
			}
		}
		if strings.Join(data, "\n") != body || !bytes.Contains(output, []byte("id:event-id\n")) {
			t.Fatalf("frame changed: %s -> %s", body, output)
		}
	}
	if _, err := decoder.EndStream(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, err := decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)})
	if err != nil || len(events) != 1 || events[0].Error == nil || events[0].Error.StatusCode != 529 {
		t.Fatalf("lost error: %#v %v", events, err)
	}
	output, err := encoder.TransformStreamEvents(context.Background(), events)
	if err != nil || !bytes.Contains(output, []byte(`"message":"busy"`)) {
		t.Fatalf("late error hidden: %s %v", output, err)
	}
	if _, err := decoder.EndStream(context.Background()); err == nil {
		t.Fatal("upstream error lost at EOF")
	}
}

func TestTolerantNativeRequestAndResponseExtensions(t *testing.T) {
	content := `[{"type":"document","citations":{"enabled":"auto","future":false}},{"type":"future_tool_result","content":{"error_code":"provider_specific","value":0}},{"type":"text","text":"hello","citations":{"enabled":true}}]`
	request, err := (&inbound.MessagesInbound{}).TransformRequest(context.Background(), []byte(`{"model":"fixture","max_tokens":10,"messages":[{"role":"user","content":`+content+`}]}`))
	if err != nil {
		t.Fatal(err)
	}
	httpRequest, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), request, "https://example.test", "key")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(httpRequest.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"enabled":"auto"`, `"future":false`, `"error_code":"provider_specific"`, `"value":0`} {
		if !bytes.Contains(body, []byte(field)) {
			t.Fatalf("request field lost: %s", body)
		}
	}
	responseBody := `{"type":"message","model":"fixture","id":"msg","role":"assistant","error":{},"content":` + content + `,"stop_reason":"end_turn"}`
	response, err := (&outbound.MessageOutbound{}).TransformResponse(context.Background(), &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responseBody))})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&inbound.MessagesInbound{}).TransformResponse(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"enabled":"auto"`, `"future":false`, `"error_code":"provider_specific"`, `"value":0`, `"error":{}`} {
		if !bytes.Contains(result, []byte(field)) {
			t.Fatalf("response field lost: %s", result)
		}
	}
}

func TestCrossProtocolRefusalAndURLCitationConversion(t *testing.T) {
	message := model.Message{Role: "assistant", Content: model.MessageContent{Content: lo.ToPtr("answer")}, Refusal: "I cannot do that.", Annotations: []model.Annotation{{Type: "url_citation", StartIndex: lo.ToPtr(int64(0)), EndIndex: lo.ToPtr(int64(6)), URLCitation: &model.URLCitation{URL: "https://example.test", Title: "source"}}}}
	response := &model.InternalLLMResponse{Choices: []model.Choice{{Message: &message, Delta: &message, FinishReason: lo.ToPtr("stop")}}}
	body, err := (&inbound.MessagesInbound{}).TransformResponse(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`"text":"answer"`, `"text":"I cannot do that."`, `"type":"web_search_result_location"`, `"start_index":0`, `"url":"https://example.test"`, `"stop_reason":"refusal"`} {
		if !bytes.Contains(body, []byte(value)) {
			t.Fatalf("converted field missing %s: %s", value, body)
		}
	}
	request := &model.InternalLLMRequest{Model: "fixture", Messages: []model.Message{message}}
	upstream, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), request, "https://example.test", "key")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := io.ReadAll(upstream.Body)
	if !bytes.Contains(encoded, []byte(`I cannot do that.`)) || !bytes.Contains(encoded, []byte(`web_search_result_location`)) {
		t.Fatalf("request metadata lost: %s", encoded)
	}
	stream, err := (&inbound.MessagesInbound{}).TransformStream(context.Background(), response)
	if err != nil || !bytes.Contains(stream, []byte(`"type":"citations_delta"`)) || !bytes.Contains(stream, []byte(`I cannot do that.`)) {
		t.Fatalf("stream metadata lost: %s %v", stream, err)
	}
	if message.Content.Content == nil || len(message.Content.MultipleContent) != 0 || message.Annotations[0].Type != "url_citation" {
		t.Fatal("conversion mutated caller message")
	}
}

func TestTolerantDecoderRetainsActualErrorsAndMalformedJSON(t *testing.T) {
	for _, body := range []string{`{"type":"error","error":{}}`, `{"error":{"type":"overloaded_error","message":"busy"}}`, `{"type":"message","content":[`, `{"type":"message","error":{"message":"failed"}}`} {
		if _, err := (&outbound.MessageOutbound{}).TransformResponse(context.Background(), &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}); err == nil {
			t.Fatalf("bad response accepted: %s", body)
		}
	}
	for _, body := range []string{`{"type":"content_block_delta","delta":`, `{"type":"error","error":{}}`, `{"error":{"message":"busy"}}`} {
		decoder := &outbound.MessageOutbound{}
		events, err := decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte(body)})
		if err == nil && (len(events) == 0 || events[0].Error == nil) {
			t.Fatalf("bad stream accepted: %s", body)
		}
		if _, err := decoder.EndStream(context.Background()); err == nil {
			t.Fatalf("bad stream accepted at EOF: %s", body)
		}
	}
	if _, err := (&outbound.MessageOutbound{}).TransformResponse(context.Background(), &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"error":{}}`))}); err == nil {
		t.Fatal("HTTP error accepted")
	}
	if _, err := (&inbound.MessagesInbound{}).TransformRequest(context.Background(), []byte(`{"messages":[`)); err == nil {
		t.Fatal("malformed request accepted")
	}
}

func TestNativeStreamModelAliasAndDone(t *testing.T) {
	decoder, encoder := &outbound.MessageOutbound{}, &inbound.MessagesInbound{}
	start := `{"type":"message_start","message":{"id":"msg","model":"upstream","content":[],"future":{"count":9007199254740993}}}`
	events, err := decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Event: "message_start", Data: []byte(start)})
	if err != nil {
		t.Fatal(err)
	}
	for index := range events {
		events[index].Model = "requested"
	}
	output, err := encoder.TransformStreamEvents(context.Background(), events)
	if err != nil || !bytes.Contains(output, []byte(`"model":"requested"`)) || !bytes.Contains(output, []byte(`9007199254740993`)) {
		t.Fatalf("model alias or extension lost: %s %v", output, err)
	}
	events, err = decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte(`[DONE]`)})
	if err != nil {
		t.Fatal(err)
	}
	output, err = encoder.TransformStreamEvents(context.Background(), events)
	if err != nil || !bytes.Contains(output, []byte(`"type":"message_stop"`)) {
		t.Fatalf("terminal marker lost: %s %v", output, err)
	}
	if _, err := decoder.EndStream(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeToolArgumentsRemainUpstreamPayload(t *testing.T) {
	decoder := &outbound.MessageOutbound{}
	for _, body := range []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"lookup","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{unfinished"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`,
		`{"type":"message_stop"}`,
	} {
		if _, err := decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte(body)}); err != nil {
			t.Fatalf("valid outer event rejected: %s %v", body, err)
		}
	}
	if _, err := decoder.EndStream(context.Background()); err != nil {
		t.Fatal(err)
	}
}
