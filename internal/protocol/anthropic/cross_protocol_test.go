package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	inbound "github.com/xuanli27/octopus/internal/protocol/anthropic/inbound"
	outbound "github.com/xuanli27/octopus/internal/protocol/anthropic/outbound"
	responsesInbound "github.com/xuanli27/octopus/internal/protocol/openairesponses/inbound"
	responsesOutbound "github.com/xuanli27/octopus/internal/protocol/openairesponses/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func TestAnthropicRejectsForeignNativeItemsInEveryCarrier(t *testing.T) {
	items := []string{
		`{"type":"image_generation_call","id":"img","result":"opaque"}`,
		`{"type":"custom_tool_call","id":"custom","call_id":"call","name":"shell","input":"echo hi"}`,
		`{"type":"reasoning","id":"reason","summary":[],"encrypted_content":"opaque"}`,
	}
	for _, raw := range items {
		for _, carrier := range []string{"items", "raw", "openai", "fields", "message"} {
			t.Run(carrier+raw, func(t *testing.T) {
				extension := &model.ProviderExtensions{}
				list := json.RawMessage("[" + raw + "]")
				response := &model.InternalLLMResponse{ID: "test", Model: "fixture"}
				switch carrier {
				case "items":
					extension.OpenAIResponses = &model.ProtocolExtension{Items: []model.ProtocolItem{{Format: model.APIFormatOpenAIResponse, Position: 0, Raw: json.RawMessage(raw)}}}
				case "raw":
					response.RawResponsesOutputItems = list
				case "openai":
					extension.OpenAI = &model.OpenAIExtension{RawResponseItems: list}
				case "fields":
					extension.OpenAIResponses = &model.ProtocolExtension{Fields: model.ProtocolFields{"output": list}}
				case "message":
					extension.OpenAIResponses = &model.ProtocolExtension{Items: []model.ProtocolItem{{Format: model.APIFormatOpenAIResponse, Position: 0, Raw: json.RawMessage(raw)}}}
					response.Choices = []model.Choice{{Message: &model.Message{ProviderExtensions: extension}}}
				}
				if carrier != "message" {
					response.ProviderExtensions = extension
				}
				for _, stream := range []bool{false, true} {
					encoder := &inbound.MessagesInbound{}
					var output []byte
					var err error
					if stream {
						output, err = encoder.TransformStream(context.Background(), response)
					} else {
						output, err = encoder.TransformResponse(context.Background(), response)
					}
					if err == nil || len(output) != 0 {
						t.Fatalf("foreign payload downgraded: %s stream=%v output=%s error=%v", carrier, stream, output, err)
					}
				}
				request := &model.InternalLLMRequest{Model: "fixture", ProviderExtensions: extension, RawInputItems: list}
				if _, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), request, "https://example.test/v1", "fixture"); err == nil {
					t.Fatal("foreign request accepted")
				}
			})
		}
	}
}

func TestAnthropicRejectsDecodedResponsesNativeAndEncrypted(t *testing.T) {
	for _, item := range []string{
		`{"type":"image_generation_call","id":"img","result":"opaque"}`,
		`{"type":"custom_tool_call","id":"custom","call_id":"call","name":"shell","input":"echo hi"}`,
		`{"type":"reasoning","id":"reason","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"opaque"}`,
	} {
		body := `{"id":"response","object":"response","model":"fixture","output":[` + item + `],"status":"completed"}`
		decoded, err := (&responsesOutbound.ResponseOutbound{}).TransformResponse(context.Background(), &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
		if err != nil {
			t.Fatal(err)
		}
		if result, err := (&inbound.MessagesInbound{}).TransformResponse(context.Background(), decoded); err == nil || len(result) != 0 {
			t.Fatalf("Responses item downgraded: %s -> %s, %v", item, result, err)
		}
		request, err := (&responsesInbound.ResponseInbound{}).TransformRequest(context.Background(), []byte(`{"model":"fixture","input":[`+item+`]}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), request, "https://example.test/v1", "fixture"); err == nil {
			t.Fatal("decoded native request downgraded")
		}
	}
}

func TestAnthropicRejectsCustomToolsAudioAndMultipleChoices(t *testing.T) {
	text := "hello"
	for _, message := range []*model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "call", Type: "custom", Function: model.FunctionCall{Name: "shell", Arguments: "echo hi"}}}},
		{Role: "assistant", Audio: &model.OutputAudio{Data: "encoded", Transcript: "hello"}},
		{Role: "assistant", ReasoningContent: &text, ReasoningBlocks: []model.ReasoningBlock{{Kind: model.ReasoningBlockKindThinking, Provider: "openai", Text: "hello", Signature: "opaque"}}},
	} {
		response := &model.InternalLLMResponse{Choices: []model.Choice{{Message: message, Delta: message}}}
		if output, err := (&inbound.MessagesInbound{}).TransformResponse(context.Background(), response); err == nil || len(output) != 0 {
			t.Fatalf("invalid response accepted: %#v", message)
		}
		if output, err := (&inbound.MessagesInbound{}).TransformStream(context.Background(), response); err == nil || len(output) != 0 {
			t.Fatalf("invalid stream accepted: %#v", message)
		}
		if _, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), &model.InternalLLMRequest{Model: "fixture", Messages: []model.Message{*message}}, "https://example.test/v1", "fixture"); err == nil {
			t.Fatalf("invalid request accepted: %#v", message)
		}
	}
	response := &model.InternalLLMResponse{Choices: []model.Choice{{Index: 0, Message: &model.Message{Content: model.MessageContent{Content: &text}}}, {Index: 1}}}
	if output, err := (&inbound.MessagesInbound{}).TransformResponse(context.Background(), response); err == nil || len(output) != 0 {
		t.Fatal("multiple response choices accepted")
	}
	if output, err := (&inbound.MessagesInbound{}).TransformStream(context.Background(), response); err == nil || len(output) != 0 {
		t.Fatal("multiple stream choices accepted")
	}
	count := int64(2)
	if _, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), &model.InternalLLMRequest{Model: "fixture", N: &count}, "https://example.test/v1", "fixture"); err == nil {
		t.Fatal("n=2 accepted")
	}
}

func TestAnthropicRootProvenanceProtectsFlatSignatures(t *testing.T) {
	signature := "opaque-openai"
	message := &model.Message{Role: "assistant", ReasoningSignature: &signature}
	extension := &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{}}
	response := &model.InternalLLMResponse{ProviderExtensions: extension, Choices: []model.Choice{{Message: message, Delta: message}}}
	if _, err := (&inbound.MessagesInbound{}).TransformResponse(context.Background(), response); err == nil {
		t.Fatal("flat foreign response signature accepted")
	}
	if _, err := (&inbound.MessagesInbound{}).TransformStream(context.Background(), response); err == nil {
		t.Fatal("flat foreign stream signature accepted")
	}
	if _, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), &model.InternalLLMRequest{Model: "fixture", ProviderExtensions: extension, Messages: []model.Message{*message}}, "https://example.test/v1", "fixture"); err == nil {
		t.Fatal("flat foreign request signature accepted")
	}
}

func TestAnthropicStreamBoundaryBeforeBatchOutput(t *testing.T) {
	text := "hello"
	index := 1
	cases := []model.StreamEvent{
		{Kind: model.StreamEventKindToolCallStart, ToolCall: &model.ToolCall{Type: "custom", ID: "custom"}},
		{Kind: model.StreamEventKindSignatureDelta, OutputIndex: &index, Delta: &model.StreamDelta{Signature: "openai-cipher"}},
		{Kind: model.StreamEventKindTextDelta, Index: 1, Delta: &model.StreamDelta{Text: "second choice"}},
		{Kind: model.StreamEventKindMessageDelta, Message: &model.Message{Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "text", Text: &text}}}}},
		{Kind: model.StreamEventKindMessageDelta, Message: &model.Message{Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "input_audio", Audio: &model.Audio{Data: "audio"}}}}}},
		{Kind: model.StreamEventKindMetadata, ProviderExtensions: &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Fields: model.ProtocolFields{"stream_frame": json.RawMessage(`{"type":"response.image_generation_call.partial_image","partial_image_b64":"opaque"}`)}}}},
		{Kind: model.StreamEventKindMessageDelta, ChoiceExtensions: &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Items: []model.ProtocolItem{{Format: model.APIFormatOpenAIResponse, Raw: json.RawMessage(`{"type":"image_generation_call"}`)}}}}},
	}
	for _, event := range cases {
		encoder := &inbound.MessagesInbound{}
		output, err := encoder.TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindMessageStart, ID: "first"}, event})
		if err == nil || len(output) != 0 {
			t.Fatalf("batch emitted invalid content: %#v -> %s %v", event, output, err)
		}
		aggregate, _ := encoder.GetInternalResponse(context.Background())
		if aggregate != nil {
			t.Fatalf("failed batch contaminated aggregate: %#v", aggregate)
		}
	}
	encoder := &inbound.MessagesInbound{}
	_, err := encoder.TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindMetadata, ProviderExtensions: &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindSignatureDelta, Delta: &model.StreamDelta{Signature: "opaque"}}}); err == nil {
		t.Fatal("foreign stream signature lost provenance")
	}
	encoder.ResetStream()
	if _, err := encoder.TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindSignatureDelta, Delta: &model.StreamDelta{Signature: "anthropic"}}}); err != nil {
		t.Fatalf("reset did not clear foreign provenance: %v", err)
	}
}

func TestAnthropicUnknownNativeContentRemainsSupported(t *testing.T) {
	raw := json.RawMessage(`{"type":"future_anthropic","payload":{"flag":false,"index":0}}`)
	message := &model.Message{Role: "assistant", Content: model.MessageContent{MultipleContent: []model.MessageContentPart{{Type: "future_anthropic", Native: &model.ProtocolItem{Format: model.APIFormatAnthropicMessage, Position: 0, Raw: raw}}}}}
	output, err := (&inbound.MessagesInbound{}).TransformResponse(context.Background(), &model.InternalLLMResponse{Choices: []model.Choice{{Message: message}}})
	if err != nil || !strings.Contains(string(output), `"type":"future_anthropic"`) {
		t.Fatalf("native content rejected: %s %v", output, err)
	}
	request, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), &model.InternalLLMRequest{Model: "fixture", Messages: []model.Message{*message}}, "https://example.test/v1", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(request.Body)
	if !strings.Contains(string(body), `"type":"future_anthropic"`) {
		t.Fatalf("native request lost: %s", body)
	}
	_, err = (&inbound.MessagesInbound{}).TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindNativeItem, NativeItem: &model.ProtocolItem{Format: model.APIFormatAnthropicMessage, Position: 0, Raw: raw}}})
	if err != nil {
		t.Fatal(err)
	}
}
