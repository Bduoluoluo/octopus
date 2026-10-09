package inbound

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func TestStreamMetadataAggregatesWithoutStartingOutput(t *testing.T) {
	codec := &MessagesInbound{}
	event := model.StreamEvent{
		Kind: model.StreamEventKindMetadata, ID: "meta-id", Model: "meta-model",
		Created: 123, SystemFingerprint: "fingerprint", ServiceTier: "standard",
		ProviderExtensions: &model.ProviderExtensions{OpenAIChat: &model.ProtocolExtension{Fields: model.ProtocolFields{"future": json.RawMessage(`false`)}}},
	}
	output, err := codec.TransformStreamEvents(context.Background(), []model.StreamEvent{event})
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 || codec.hasStarted {
		t.Fatalf("metadata started output: %s", output)
	}
	output, err = codec.TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Text: "answer"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"id":"meta-id"`) || !strings.Contains(string(output), `"model":"meta-model"`) {
		t.Fatalf("metadata identifiers lost: %s", output)
	}
	response, err := codec.GetInternalResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Created != 123 || response.SystemFingerprint != "fingerprint" || response.ServiceTier != "standard" || response.ProviderExtensions == nil || string(response.ProviderExtensions.OpenAIChat.Fields["future"]) != "false" {
		t.Fatalf("metadata lost: %#v", response)
	}
}

func TestSupplementalMessageMetadataDoesNotCreateContent(t *testing.T) {
	codec := &MessagesInbound{}
	output, err := codec.TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindMessageDelta, Message: &model.Message{ProviderExtensions: &model.ProviderExtensions{OpenAIChat: &model.ProtocolExtension{Fields: model.ProtocolFields{"future": json.RawMessage(`0`)}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 || codec.hasStarted {
		t.Fatalf("supplemental metadata created content: %s", output)
	}
	response, err := codec.GetInternalResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || len(response.Choices) != 1 || response.Choices[0].Message.ProviderExtensions == nil {
		t.Fatalf("message metadata not aggregated: %#v", response)
	}
}

func TestSupplementalEventsRejectUnrepresentableContent(t *testing.T) {
	events := []model.StreamEvent{
		{Kind: model.StreamEventKindMessageDelta, Message: &model.Message{Audio: &model.OutputAudio{Data: "audio"}}},
		{Kind: model.StreamEventKindMessageDelta, Message: &model.Message{Images: []model.MessageContentPart{{Type: "image_url"}}}},
		{Kind: model.StreamEventKindMessageDelta, Message: &model.Message{Annotations: []model.Annotation{{Type: "url_citation", URLCitation: &model.URLCitation{URL: "https://example.test"}}}}},
		{Kind: model.StreamEventKindMessageDelta, Logprobs: &model.LogprobsContent{}},
		{Kind: model.StreamEventKindMetadata, Delta: &model.StreamDelta{Text: "hidden content"}},
	}
	for _, event := range events {
		codec := &MessagesInbound{}
		output, err := codec.TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindMessageStart}, event})
		if err == nil || len(output) != 0 || codec.hasStarted {
			t.Fatalf("unsupported event consumed: event=%#v error=%v output=%s", event, err, output)
		}
	}
}

func TestResetStreamPreservesInputEstimateOnly(t *testing.T) {
	codec := &MessagesInbound{}
	_, err := codec.TransformRequest(context.Background(), []byte(`{"model":"fixture","max_tokens":5,"messages":[{"role":"user","content":"hello world"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	inputToken := codec.inputToken
	_, err = codec.TransformStreamEvents(context.Background(), []model.StreamEvent{
		{Kind: model.StreamEventKindMessageStart, ID: "old", Model: "old-model"},
		{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Text: "old-text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	codec.ResetStream()
	if codec.inputToken != inputToken || codec.hasStarted || codec.blocks != nil || codec.pendingUsage != nil {
		t.Fatalf("unexpected reset state: %#v", codec)
	}
	output, err := codec.TransformStream(context.Background(), &model.InternalLLMResponse{ID: "new", Model: "new-model", Choices: []model.Choice{{Delta: &model.Message{Role: "assistant", Content: model.MessageContent{Content: stringPtr("new-text")}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), "old") || !strings.Contains(string(output), `"index":0`) {
		t.Fatalf("attempt state leaked: %s", output)
	}
	response, err := codec.GetInternalResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.ID != "new" || response.Choices[0].Message.Content.Content == nil || *response.Choices[0].Message.Content.Content != "new-text" {
		t.Fatalf("aggregate state leaked: %#v", response)
	}
}
