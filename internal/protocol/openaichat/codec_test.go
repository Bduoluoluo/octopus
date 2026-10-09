package openaichat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func TestChatFieldPreservationAndAuthoritativeEdits(t *testing.T) {
	body := []byte(`{"model":"before","n":2,"temperature":0,"store":false,"future_flag":false,"reasoning_budget":0,"stream_options":null,"messages":[{"role":"assistant","content":[{"type":"text","text":"","future_part":0}],"future_message":{"k":1},"tool_calls":[{"id":"c","index":0,"type":"function","future_call":false,"function":{"name":"lookup","arguments":"{}","future_function":0}}]},{"role":"user","content":[{"type":"file","file":{"file_data":"raw-base64","filename":"a.pdf"}}]}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":false,"schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}},"tools":[{"type":"function","future_tool":true,"function":{"name":"lookup","parameters":{"type":"object"},"future_definition":"value"}}],"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[{"type":"function","function":{"name":"lookup"}}]}}}`)
	request, err := DecodeRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if request.ResponseFormat.Name != "answer" || request.ResponseFormat.RawSchema == nil || len(request.ToolChoice.Tools) != 1 {
		t.Fatalf("lost schema/tool restriction: %#v", request)
	}
	request.Model = "after"
	request.Messages[0].Content.MultipleContent[0].Text = pointer("modified")
	encoded, err := EncodeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal(body, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	before["model"] = "after"
	before["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] = "modified"
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("field loss\nwant: %s\ngot: %s", body, encoded)
	}
}

func TestChatFrameTerminalUsageAndParallelTools(t *testing.T) {
	var decoder StreamDecoder
	var encoder StreamEncoder
	frames := []string{
		`{"id":"r","model":"m","created":12,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"alpha","arguments":"{"}},{"index":1,"id":"b","type":"function","function":{"name":"beta","arguments":"{\"b\":"}}]}}]}`,
		`{"id":"r","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}},{"index":0,"function":{"arguments":"\"a\":1}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"r","model":"m","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`,
		"[DONE]",
	}
	var output strings.Builder
	for index, frame := range frames {
		events, err := decoder.Push(context.Background(), model.StreamFrame{Data: []byte(frame)})
		if err != nil {
			t.Fatalf("frame %d: %v", index, err)
		}
		encoded, err := encoder.Push(context.Background(), events)
		if err != nil {
			t.Fatal(err)
		}
		if index == 1 && strings.Contains(string(encoded), "[DONE]") {
			t.Fatal("choice finish ended transport before usage")
		}
		output.Write(encoded)
	}
	if strings.Count(output.String(), "[DONE]") != 1 {
		t.Fatal(output.String())
	}
	response := encoder.Response()
	if response.Usage == nil || response.Usage.TotalTokens != 12 || response.Created != 12 {
		t.Fatalf("metadata lost: %#v", response)
	}
	calls := response.Choices[0].Message.ToolCalls
	if len(calls) != 2 || calls[0].Function.Arguments != `{"a":1}` || calls[1].Function.Arguments != `{"b":2}` {
		t.Fatalf("parallel arguments mixed: %#v", calls)
	}
	if events, err := decoder.End(context.Background()); err != nil || len(events) != 0 {
		t.Fatalf("repeated terminal: %v %v", events, err)
	}
	if err := decoder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Push(context.Background(), model.StreamFrame{Data: []byte(frames[0])}); err == nil {
		t.Fatal("accepted closed stream")
	}
}

func TestChatEOFAndErrorClassification(t *testing.T) {
	for _, fixture := range []struct {
		name, frame string
		valid       bool
	}{
		{"incomplete", `{"choices":[{"index":0,"delta":{"content":"partial"}}]}`, false},
		{"finished", `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`, true},
		{"emptyFinish", `{"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":""}]}`, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var decoder StreamDecoder
			if _, err := decoder.Push(context.Background(), model.StreamFrame{Data: []byte(fixture.frame)}); err != nil {
				t.Fatal(err)
			}
			events, err := decoder.End(context.Background())
			if fixture.valid {
				if err != nil || len(events) != 1 || events[0].Kind != model.StreamEventKindDone {
					t.Fatalf("%v %v", events, err)
				}
			} else if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("truncation accepted: %v", err)
			}
		})
	}
	for _, frame := range []model.StreamFrame{
		{Event: "error"},
		{Event: "error", Data: []byte(`{"error":{"message":"limited","code":429,"type":"rate_limit_error"}}`)},
		{Data: []byte(`{"event":"error","data":{"error":{"message":"failed","code":"upstream_error"}}}`)},
	} {
		var decoder StreamDecoder
		if _, err := decoder.Push(context.Background(), frame); err == nil {
			t.Fatalf("error frame accepted: %#v", frame)
		}
	}
}

func TestChatMixedEventsAndDoneDoNotLoseText(t *testing.T) {
	var encoder StreamEncoder
	encoded, err := encoder.Push(context.Background(), []model.StreamEvent{
		{Kind: model.StreamEventKindTextDelta, Index: 0, Delta: &model.StreamDelta{Text: "first"}},
		{Kind: model.StreamEventKindTextDelta, Index: 0, Delta: &model.StreamDelta{Text: " second"}},
		{Kind: model.StreamEventKindUsageDelta, Usage: &model.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}},
		{Kind: model.StreamEventKindDone},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "first second") || !strings.Contains(string(encoded), "[DONE]") || !strings.Contains(string(encoded), "\"total_tokens\":3") {
		t.Fatal(string(encoded))
	}
}

func TestChatRejectsUnrepresentableCrossProtocolRequest(t *testing.T) {
	for _, part := range []model.MessageContentPart{
		{Type: "document", Document: &model.DocumentSource{Type: "text", Text: "source"}},
		{Type: "compaction", Native: &model.ProtocolItem{Format: model.APIFormatOpenAIResponse, Raw: json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`)}},
	} {
		request := &model.InternalLLMRequest{RawAPIFormat: model.APIFormatAnthropicMessage, Messages: []model.Message{{Role: "user", Content: model.MessageContent{MultipleContent: []model.MessageContentPart{part}}}}}
		if _, err := EncodeRequest(context.Background(), request); err == nil {
			t.Fatalf("silently accepted %s", part.Type)
		}
	}
}

func pointer[T any](value T) *T { return &value }

func TestChatStreamSupplementalFieldsAndArrayContent(t *testing.T) {
	var decoder StreamDecoder
	var encoder StreamEncoder
	fixture := `{"id":"r","model":"m","choices":[{"index":0,"future_choice":0,"delta":{"role":"assistant","content":[{"type":"text","text":"hello","future_part":false}],"future_delta":"value","audio":{"id":"a","data":"YQ==","transcript":"hello"}}}],"future_response":false}`
	events, err := decoder.Push(context.Background(), model.StreamFrame{Data: []byte(fixture)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encoder.Push(context.Background(), events)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"future_choice":0`, `"future_part":false`, `"future_delta":"value"`, `"future_response":false`, `"data":"YQ=="`, `"text":"hello"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("lost %s: %s", field, encoded)
		}
	}
}
