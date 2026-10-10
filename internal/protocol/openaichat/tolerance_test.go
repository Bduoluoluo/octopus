package openaichat

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func TestChatVendorToolsReachEncodingWithoutFiltering(t *testing.T) {
	fixture := `{"model":"before","messages":[{"role":"user","content":[{"type":"vendor_document","source":{"id":"doc"}}]}],"tools":[{"type":"custom","custom":{"name":"shell","format":{"type":"text"}}},{"type":"vendor_search","vendor":{"mode":"full"}}],"tool_choice":{"type":"custom","custom":{"name":"shell"}}}`
	request, err := DecodeRequest([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	request.Model = "after"
	encoded, err := EncodeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var original, result map[string]any
	if err := json.Unmarshal([]byte(fixture), &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	original["model"] = "after"
	if !reflect.DeepEqual(original, result) {
		t.Fatalf("vendor payload changed: %s", encoded)
	}
	request.ToolChoice = &model.ToolChoice{ToolChoice: pointer("none")}
	encoded, err = EncodeRequest(context.Background(), request)
	if err != nil || !strings.Contains(string(encoded), `"tool_choice":"none"`) {
		t.Fatalf("authoritative tool choice lost: %s %v", encoded, err)
	}
}

func TestChatVendorToolCallsAndAnnotationsPreserved(t *testing.T) {
	fixture := `{"id":"r","model":"m","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"answer","citations":[{"type":"char_location","document_index":0,"start_char_index":0,"end_char_index":6,"vendor":false}]}],"tool_calls":[{"id":"c","index":0,"type":"custom","custom":{"name":"shell","input":"echo ok"}}]},"finish_reason":"vendor_done"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	response, err := DecodeResponse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"custom":{"name":"shell","input":"echo ok"}`, `"document_index":0`, `"start_char_index":0`, `"text":"answer"`, `"vendor":false`, `"finish_reason":"vendor_done"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("lost %s: %s", field, encoded)
		}
	}
	if strings.Contains(string(encoded), `"function"`) {
		t.Fatalf("invented function for custom call: %s", encoded)
	}
}

func TestChatEmptyErrorPlaceholdersAreNotFailures(t *testing.T) {
	for _, placeholder := range []string{`{}`, `[]`, `null`, `{ }`, `[ ]`} {
		t.Run(placeholder, func(t *testing.T) {
			fixture := `{"error":` + placeholder + `,"id":"r","model":"m","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`
			response, err := DecodeResponse([]byte(fixture))
			if err != nil || response.Error != nil {
				t.Fatalf("empty placeholder rejected: %v", err)
			}
			encoded, err := EncodeResponse(response)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			if !emptyError(result["error"]) || len(result["error"]) == 0 {
				t.Fatalf("placeholder not preserved: %s", encoded)
			}
			var decoder StreamDecoder
			var encoder StreamEncoder
			events, err := decoder.Push(context.Background(), model.StreamFrame{Data: []byte(fixture)})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err = encoder.Push(context.Background(), events)
			if err != nil || !strings.Contains(string(encoded), `"content":"ok"`) {
				t.Fatalf("successful output lost: %s %v", encoded, err)
			}
		})
	}
}

func TestChatRealErrorsStillFailAfterOutputAndTerminal(t *testing.T) {
	for _, frame := range []model.StreamFrame{
		{Data: []byte(`{"error":{"message":"limited","code":429,"type":"rate_limit_error"}}`)},
		{Data: []byte(`{"error":"Bad Gateway"}`)},
		{Data: []byte(`{"error":[],"status":"failed","message":"Bad Gateway"}`)},
		{Data: []byte(`{"error":{},"type":"error","message":"Bad Gateway"}`)},
		{Data: []byte(`{"error":{},"data":{"error":{"message":"Bad Gateway","type":"upstream_error"}}}`)},
		{Event: "error", Data: []byte(`{"error":{}}`)},
	} {
		var decoder StreamDecoder
		for _, data := range []string{`{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`} {
			if _, err := decoder.Push(context.Background(), model.StreamFrame{Data: []byte(data)}); err != nil {
				t.Fatal(err)
			}
		}
		_, err := decoder.Push(context.Background(), frame)
		var upstreamError *model.ResponseError
		if !errors.As(err, &upstreamError) {
			t.Fatalf("real error not classified: %s %v", frame.Data, err)
		}
		if frame.Event == "" {
			if _, err := DecodeResponse(frame.Data); !errors.As(err, &upstreamError) {
				t.Fatalf("non-stream error not classified: %s %v", frame.Data, err)
			}
		}
	}
	for _, invalid := range []string{`{"error":`, `{"choices":[`} {
		if _, err := DecodeResponse([]byte(invalid)); err == nil {
			t.Fatalf("malformed JSON accepted: %s", invalid)
		}
	}
}

func TestChatVendorStatusAndDataAreNotErrorEnvelopes(t *testing.T) {
	fixture := `{"id":"r","model":"m","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"data":"opaque_trace","status":200,"type":{"vendor":true},"request_id":42,"error":{}}`
	var decoder StreamDecoder
	var encoder StreamEncoder
	events, err := decoder.Push(context.Background(), model.StreamFrame{Data: []byte(fixture)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encoder.Push(context.Background(), events)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"content":"ok"`, `"data":"opaque_trace"`, `"status":200`, `"type":{"vendor":true}`, `"request_id":42`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("lost %s: %s", field, encoded)
		}
	}
}

func TestChatLateFramesPreserveTextUsageAndFinishReason(t *testing.T) {
	var decoder StreamDecoder
	var encoder StreamEncoder
	var output strings.Builder
	for _, frame := range []string{
		`{"id":"r","choices":[{"index":0,"delta":{"content":"first"},"finish_reason":"vendor_done"}]}`,
		`{"id":"r","choices":[{"index":0,"delta":{"content":" second"}}]}`,
		`[DONE]`,
		`{"id":"r","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5},"vendor_trailer":false}`,
		`[DONE]`,
	} {
		events, err := decoder.Push(context.Background(), model.StreamFrame{Data: []byte(frame)})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := encoder.Push(context.Background(), events)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(encoded)
	}
	if strings.Count(output.String(), "[DONE]") != 1 || !strings.Contains(output.String(), `"finish_reason":"vendor_done"`) || !strings.Contains(output.String(), `"vendor_trailer":false`) {
		t.Fatalf("stream metadata lost: %s", output.String())
	}
	response := encoder.Response()
	if response.Error != nil || response.Usage == nil || response.Usage.TotalTokens != 5 || *response.Choices[0].Message.Content.Content != "first second" {
		t.Fatalf("late result lost: %#v", response)
	}
}

func TestChatCrossProtocolReasoningAndCitationsRetainUsableText(t *testing.T) {
	native := model.ProtocolItem{Format: model.APIFormatOpenAIResponse, Position: 0, Raw: json.RawMessage(`{"type":"reasoning","id":"r","summary":[{"type":"summary_text","text":"thought"}],"encrypted_content":"opaque"}`)}
	message := model.Message{
		Role: "assistant", ReasoningContent: pointer("thought"), ReasoningSignature: pointer("signed"), RedactedThinkingBlocks: []string{"redacted"},
		Content:            model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "text", Text: pointer("answer"), Citations: []model.ContentCitation{{Type: "page_location", DocumentIndex: pointer(int64(0)), StartPageNumber: pointer(int64(0)), EndPageNumber: pointer(int64(2))}}}}},
		ProviderExtensions: &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Items: []model.ProtocolItem{native}}},
	}
	request := &model.InternalLLMRequest{Model: "m", RawAPIFormat: model.APIFormatAnthropicMessage, Messages: []model.Message{message}}
	encodedRequest, err := EncodeRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response := &model.InternalLLMResponse{Model: "m", Choices: []model.Choice{{Message: &message}}}
	encodedResponse, err := EncodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range [][]byte{encodedRequest, encodedResponse} {
		for _, field := range []string{`"text":"answer"`, `"reasoning_metadata":{"signature":"signed"}`, `"redacted_thinking_blocks":["redacted"]`, `"encrypted_content":"opaque"`, `"document_index":0`, `"start_page_number":0`} {
			if !strings.Contains(string(encoded), field) {
				t.Fatalf("lost %s: %s", field, encoded)
			}
		}
		if strings.Contains(string(encoded), `"reasoning_signature"`) {
			t.Fatalf("foreign signature reused as native Chat signature: %s", encoded)
		}
	}
	if message.ProviderExtensions.OpenAIChat != nil {
		t.Fatal("encoding mutated foreign extension")
	}
}

func TestChatCrossProtocolMetadataStream(t *testing.T) {
	var encoder StreamEncoder
	encoded, err := encoder.Push(context.Background(), []model.StreamEvent{
		{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Text: "answer"}},
		{Kind: model.StreamEventKindSignatureDelta, Delta: &model.StreamDelta{Signature: "signature"}},
		{Kind: model.StreamEventKindCitationDelta, Citation: &model.ContentCitation{Type: "char_location", DocumentIndex: pointer(int64(0)), StartCharIndex: pointer(int64(0)), EndCharIndex: pointer(int64(6))}},
		{Kind: model.StreamEventKindNativeItem, NativeItem: &model.ProtocolItem{Format: model.APIFormatOpenAIResponse, Position: -1, Raw: json.RawMessage(`{"type":"response.vendor_metadata","vendor":false}`)}},
		{Kind: model.StreamEventKindDone},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"text":"answer"`, `"signature":"signature"`, `"reasoning_metadata":`, `"document_index":0`, `"start_char_index":0`, `"type":"response.vendor_metadata"`, `[DONE]`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("lost %s: %s", field, encoded)
		}
	}
	if strings.Contains(string(encoded), `"reasoning_signature"`) {
		t.Fatalf("foreign signature reused as native Chat signature: %s", encoded)
	}
}

func TestChatStreamTextWithCitationsSurvives(t *testing.T) {
	var decoder StreamDecoder
	var encoder StreamEncoder
	fixture := `{"choices":[{"index":0,"delta":{"content":[{"type":"text","text":"answer","citations":[{"type":"char_location","document_index":0,"start_char_index":0}]}]},"finish_reason":"stop"}]}`
	events, err := decoder.Push(context.Background(), model.StreamFrame{Data: []byte(fixture)})
	if err != nil {
		t.Fatal(err)
	}
	decoded := model.InternalResponseFromStreamEvents(events)
	if decoded == nil || len(decoded.Choices) == 0 || len(decoded.Choices[0].Delta.Content.MultipleContent) == 0 || len(decoded.Choices[0].Delta.Content.MultipleContent[0].Citations) != 1 {
		t.Fatalf("citations absent from IR: %#v", decoded)
	}
	encoded, err := encoder.Push(context.Background(), events)
	if err != nil || !strings.Contains(string(encoded), `"text":"answer"`) || !strings.Contains(string(encoded), `"start_char_index":0`) {
		t.Fatalf("text/citation dropped: %s %v", encoded, err)
	}
}

func TestChatNakedIRSignatureIsMetadataNotNativeChatState(t *testing.T) {
	message := &model.Message{Role: "assistant", Content: model.MessageContent{Content: pointer("answer")}, ReasoningSignature: pointer("foreign_signed")}
	encoded, err := EncodeResponse(&model.InternalLLMResponse{Choices: []model.Choice{{Message: message}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"reasoning_signature"`) || !strings.Contains(string(encoded), `"reasoning_metadata":{"signature":"foreign_signed"}`) || !strings.Contains(string(encoded), `"content":"answer"`) {
		t.Fatalf("unknown signature provenance mishandled: %s", encoded)
	}
	if message.ReasoningSignature == nil || *message.ReasoningSignature != "foreign_signed" {
		t.Fatal("signature retention mutated IR")
	}
}

func TestChatNativeSignatureRoundTrips(t *testing.T) {
	fixture := `{"choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_signature":"native_signed"}}]}`
	response, err := DecodeResponse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeResponse(response)
	if err != nil || !strings.Contains(string(encoded), `"reasoning_signature":"native_signed"`) {
		t.Fatalf("native signature lost: %s %v", encoded, err)
	}
	var decoder StreamDecoder
	var encoder StreamEncoder
	events, err := decoder.Push(context.Background(), model.StreamFrame{Data: []byte(`{"choices":[{"index":0,"delta":{"content":"answer","reasoning_signature":"native_signed"},"finish_reason":"stop"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = encoder.Push(context.Background(), events)
	if err != nil || !strings.Contains(string(encoded), `"reasoning_signature":"native_signed"`) {
		t.Fatalf("native stream signature lost: %s %v", encoded, err)
	}
}
