package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/tmaxmax/go-sse"
	anthropicInbound "github.com/xuanli27/octopus/internal/protocol/anthropic/inbound"
	anthropicOutbound "github.com/xuanli27/octopus/internal/protocol/anthropic/outbound"
	responsesInbound "github.com/xuanli27/octopus/internal/protocol/openairesponses/inbound"
	responsesOutbound "github.com/xuanli27/octopus/internal/protocol/openairesponses/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func TestResponsesRejectForeignOpaqueRequestHistory(t *testing.T) {
	fixtures := []string{
		`{"type":"thinking","thinking":"visible","signature":"anthropic-signature"}`,
		`{"type":"redacted_thinking","data":"opaque-redacted"}`,
		`{"type":"server_tool_use","id":"server","name":"web_search","input":{"query":"q"}}`,
		`{"type":"web_search_tool_result","tool_use_id":"server","content":[{"type":"web_search_result","url":"https://example.test","encrypted_content":"encrypted"}]}`,
		`{"type":"future_block","opaque":"secret"}`,
		`{"type":"tool_result","tool_use_id":"call","content":[{"type":"future_block","opaque":"secret"}]}`,
	}
	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			body := `{"model":"claude","max_tokens":64,"messages":[{"role":"assistant","content":[` + fixture + `]}]}`
			request, err := (&anthropicInbound.MessagesInbound{}).TransformRequest(context.Background(), []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := (&responsesOutbound.ResponseOutbound{}).TransformRequest(context.Background(), request, "https://upstream.test/v1", "key")
			if err == nil || encoded != nil {
				t.Fatalf("foreign native request encoded: %v %v", encoded, err)
			}
			if data, err := json.Marshal(responsesOutbound.ConvertToResponsesRequest(request)); err == nil || len(data) > 0 {
				t.Fatalf("WS helper encoded foreign native request: %s %v", data, err)
			}
			if data, err := responsesOutbound.MarshalResponsesInputItems(request.Messages); err == nil || len(data) > 0 {
				t.Fatalf("replay helper encoded foreign native request: %s %v", data, err)
			}
		})
	}
}

func TestResponsesRejectForeignOpaqueNonStreamOutput(t *testing.T) {
	fixtures := []string{
		`{"type":"thinking","thinking":"visible","signature":"signed"}`,
		`{"type":"redacted_thinking","data":"redacted"}`,
		`{"type":"server_tool_use","id":"server","name":"web_search","input":{"q":"question"}}`,
		`{"type":"web_search_tool_result","tool_use_id":"server","content":[{"type":"web_search_result","encrypted_content":"opaque"}]}`,
		`{"type":"future_block","opaque":{"value":1}}`,
		`{"type":"text","text":"answer","future_opaque":"opaque"}`,
	}
	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			body := `{"id":"msg","type":"message","model":"claude","role":"assistant","content":[` + fixture + `],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
			response, err := (&anthropicOutbound.MessageOutbound{}).TransformResponse(context.Background(), &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := (&responsesInbound.ResponseInbound{}).TransformResponse(context.Background(), response)
			if err == nil || len(encoded) != 0 {
				t.Fatalf("foreign native response silently encoded: %s %v", encoded, err)
			}
		})
	}
}

func TestResponsesRejectForeignOpaqueStreamOutput(t *testing.T) {
	for _, block := range []string{
		`{"type":"thinking","thinking":"visible","signature":"signed"}`,
		`{"type":"redacted_thinking","data":"redacted"}`,
		`{"type":"server_tool_use","id":"server","name":"web_search","input":{"q":"question"}}`,
		`{"type":"future_block","opaque":"secret"}`,
	} {
		t.Run(block, func(t *testing.T) {
			for _, legacy := range []bool{false, true} {
				decoder := &anthropicOutbound.MessageOutbound{}
				encoder := &responsesInbound.ResponseInbound{}
				rejected := false
				for _, raw := range []string{
					`{"type":"message_start","message":{"id":"msg","type":"message","model":"claude","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":` + block + `}`,
				} {
					events, err := decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte(raw)})
					if err != nil {
						t.Fatal(err)
					}
					var encoded []byte
					if legacy {
						encoded, err = encoder.TransformStream(context.Background(), model.InternalResponseFromStreamEvents(events))
					} else {
						encoded, err = encoder.TransformStreamEvents(context.Background(), events)
					}
					if err != nil {
						if len(encoded) > 0 {
							t.Fatal("partial bytes emitted on invalid frame")
						}
						rejected = true
						break
					}
				}
				if !rejected {
					t.Fatalf("foreign opaque frame accepted legacy=%v", legacy)
				}
			}
		})
	}
}

func TestResponsesGenericThinkingTextStillEncodes(t *testing.T) {
	body := `{"model":"claude","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"first"},{"type":"thinking","thinking":"second"},{"type":"text","text":"answer"}]},{"role":"user","content":"continue"}]}`
	request, err := (&anthropicInbound.MessagesInbound{}).TransformRequest(context.Background(), []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := (&responsesOutbound.ResponseOutbound{}).TransformRequest(context.Background(), request, "https://upstream.test/v1", "key")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("first")) || !bytes.Contains(raw, []byte("second")) || bytes.Contains(raw, []byte("encrypted_content")) {
		t.Fatalf("thinking lost or misencoded: %s", raw)
	}
	responseBody := `{"id":"msg","type":"message","model":"claude","role":"assistant","content":[{"type":"thinking","thinking":"first"},{"type":"thinking","thinking":"second"},{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`
	internal, err := (&anthropicOutbound.MessageOutbound{}).TransformResponse(context.Background(), &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(responseBody))})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := (&responsesInbound.ResponseInbound{}).TransformResponse(context.Background(), internal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("first")) || !bytes.Contains(encoded, []byte("second")) || bytes.Contains(encoded, []byte("encrypted_content")) {
		t.Fatalf("output thinking lost: %s", encoded)
	}
}

func TestResponsesMessageDeltaArraysAndChoiceExtensions(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		encoder := &responsesInbound.ResponseInbound{}
		chunk := &model.InternalLLMResponse{ID: "resp", Model: "m", Choices: []model.Choice{{Index: 0, Delta: &model.Message{Role: "assistant", Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "text", Text: lo.ToPtr("first")}, {Type: "text", Text: lo.ToPtr("second")}}}}}}}
		var encoded []byte
		var err error
		if legacy {
			encoded, err = encoder.TransformStream(context.Background(), chunk)
		} else {
			encoded, err = encoder.TransformStreamEvents(context.Background(), model.StreamEventsFromInternalResponse(chunk))
		}
		if err != nil {
			t.Fatal(err)
		}
		decoder := &responsesOutbound.ResponseOutbound{}
		text := ""
		for frame, err := range sse.Read(bytes.NewReader(encoded), nil) {
			if err != nil {
				t.Fatal(err)
			}
			events, err := decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Event: frame.Type, Data: []byte(frame.Data)})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Kind == model.StreamEventKindTextDelta && event.Delta != nil {
					text += event.Delta.Text
				}
			}
		}
		if text != "firstsecond" {
			t.Fatalf("lost streamed array legacy=%v: %s", legacy, encoded)
		}
	}
	for _, event := range []model.StreamEvent{
		{Kind: model.StreamEventKindMessageDelta, Message: &model.Message{Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "input_audio", Audio: &model.Audio{Data: "audio", Format: "wav"}}}}}},
		{Kind: model.StreamEventKindMessageDelta, Message: &model.Message{Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "image_url", ImageURL: &model.ImageURL{URL: "https://image.test/a.png"}}}}}},
		{Kind: model.StreamEventKindMessageDelta, ChoiceExtensions: &model.ProviderExtensions{OpenAIChat: &model.ProtocolExtension{Fields: model.ProtocolFields{"native_private": json.RawMessage(`{"opaque":true}`)}}}},
	} {
		encoded, err := (&responsesInbound.ResponseInbound{}).TransformStreamEvents(context.Background(), []model.StreamEvent{event})
		if err == nil || len(encoded) != 0 {
			t.Fatalf("unsupported delta dropped: %s %v", encoded, err)
		}
	}
}

func TestResponsesMultiChoiceCannotBypassNativeOutput(t *testing.T) {
	response := &model.InternalLLMResponse{RawResponsesOutputItems: json.RawMessage(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first"}]}]`), Choices: []model.Choice{{Index: 0, Message: &model.Message{Role: "assistant"}}, {Index: 1, Message: &model.Message{Role: "assistant"}}}}
	if raw, err := (&responsesInbound.ResponseInbound{}).TransformResponse(context.Background(), response); err == nil || len(raw) != 0 {
		t.Fatalf("multiple response choices accepted: %s %v", raw, err)
	}
	response.Choices[0].Delta = response.Choices[0].Message
	response.Choices[1].Delta = response.Choices[1].Message
	if raw, err := (&responsesInbound.ResponseInbound{}).TransformStream(context.Background(), response); err == nil || len(raw) != 0 {
		t.Fatalf("multiple stream choices accepted: %s %v", raw, err)
	}
	events := []model.StreamEvent{
		{Kind: model.StreamEventKindTextDelta, Index: 1, Delta: &model.StreamDelta{Text: "second"}},
		{Kind: model.StreamEventKindMetadata, ProviderExtensions: &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Fields: model.ProtocolFields{"stream_frame": json.RawMessage(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"first"}`)}}}},
	}
	if raw, err := (&responsesInbound.ResponseInbound{}).TransformStreamEvents(context.Background(), events); err == nil || len(raw) != 0 {
		t.Fatalf("native frame bypassed choice validation: %s %v", raw, err)
	}
}

func TestResponsesSameProtocolOpaqueHistoryStillEncodes(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"type":"reasoning","id":"r","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"same-provider-opaque"},{"type":"message","role":"user","content":"continue"}]}`)
	request, err := (&responsesInbound.ResponseInbound{}).TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(responsesOutbound.ConvertToResponsesRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	requireJSON(t, decodeObject(t, encoded)["input"], decodeObject(t, body)["input"])
	replayed, err := responsesOutbound.MarshalResponsesInputItems(request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(replayed, []byte("same-provider-opaque")) {
		t.Fatalf("same-provider opaque lost: %s", replayed)
	}
}
