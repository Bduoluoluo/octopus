package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func streamIndex(value int) *int { return &value }

func TestStreamEventBatchRetainsAudioMetadataAndRefusal(t *testing.T) {
	events := []StreamEvent{
		{Kind: StreamEventKindMetadata, Created: 42, SystemFingerprint: "fp", ProviderExtensions: &ProviderExtensions{OpenAIChat: &ProtocolExtension{Fields: ProtocolFields{"first": json.RawMessage(`0`)}}}},
		{Kind: StreamEventKindMessageDelta, Message: &Message{Audio: &OutputAudio{ID: "audio", Data: "a", Transcript: "first "}}},
		{Kind: StreamEventKindTextDelta, Delta: &StreamDelta{Refusal: "no "}},
		{Kind: StreamEventKindMessageDelta, Message: &Message{Audio: &OutputAudio{Data: "b", Transcript: "second"}}},
		{Kind: StreamEventKindTextDelta, Delta: &StreamDelta{Refusal: "thanks"}},
		{Kind: StreamEventKindMetadata, ServiceTier: "standard", ProviderExtensions: &ProviderExtensions{OpenAIChat: &ProtocolExtension{Fields: ProtocolFields{"second": json.RawMessage(`false`)}}}},
		{Kind: StreamEventKindDone},
	}
	chunk := InternalResponseFromStreamEvents(events)
	if chunk == nil || chunk.Object == "[DONE]" || len(chunk.Choices) != 1 {
		t.Fatalf("mixed done erased response: %#v", chunk)
	}
	delta := chunk.Choices[0].Delta
	if delta.Audio == nil || delta.Audio.ID != "audio" || delta.Audio.Data != "ab" || delta.Audio.Transcript != "first second" || delta.Refusal != "no thanks" {
		t.Fatalf("batched delta lost: %#v", delta)
	}
	var aggregate StreamAggregator
	aggregate.Add(chunk)
	response := aggregate.Response()
	message := response.Choices[0].Message
	if message.Refusal != "no thanks" || message.Content.Content != nil || len(message.Content.MultipleContent) != 0 {
		t.Fatalf("refusal acquired invented text: %#v", message)
	}
	if response.Created != 42 || response.SystemFingerprint != "fp" || response.ServiceTier != "standard" || len(response.ProviderExtensions.OpenAIChat.Fields) != 2 {
		t.Fatalf("metadata overwritten: %#v", response)
	}
	if delta.Audio.Data != "ab" {
		t.Fatal("aggregation mutated source audio")
	}
}

func TestStreamAggregateLateItemIdentityAndSeparateChoices(t *testing.T) {
	events := []StreamEvent{
		{Kind: StreamEventKindTextDelta, Index: 2, ItemID: "item", ContentIndex: streamIndex(0), Delta: &StreamDelta{Text: "hel"}},
		{Kind: StreamEventKindTextDelta, Index: 2, ItemID: "item", OutputIndex: streamIndex(4), ContentIndex: streamIndex(0), Delta: &StreamDelta{Text: "lo"}},
		{Kind: StreamEventKindCitationDelta, Index: 2, ItemID: "item", OutputIndex: streamIndex(4), ContentIndex: streamIndex(0), Citation: &ContentCitation{Type: "char_location", DocumentIndex: new(int64)}},
		{Kind: StreamEventKindTextDelta, Index: 0, OutputIndex: streamIndex(4), ContentIndex: streamIndex(0), Delta: &StreamDelta{Text: "other"}},
	}
	var aggregate StreamAggregator
	for _, event := range events {
		aggregate.Add(InternalResponseFromStreamEvents([]StreamEvent{event}))
	}
	response := aggregate.Response()
	if len(response.Choices) != 2 || response.Choices[0].Index != 0 || response.Choices[1].Index != 2 {
		t.Fatalf("choices=%#v", response.Choices)
	}
	parts := response.Choices[1].Message.Content.MultipleContent
	if len(parts) != 1 || *parts[0].Text != "hello" || len(parts[0].Citations) != 1 || *response.Choices[0].Message.Content.Content != "other" {
		t.Fatalf("scopes mixed: %#v", response.Choices)
	}
	if !reflect.DeepEqual(response, aggregate.Response()) {
		t.Fatal("Response mutated the accumulated source")
	}
}

func TestStreamAggregateExplicitEmptySnapshotClearsNativeItems(t *testing.T) {
	var aggregate StreamAggregator
	aggregate.Add(InternalResponseFromStreamEvents([]StreamEvent{{Kind: StreamEventKindNativeItem, NativeItem: &ProtocolItem{Format: APIFormatOpenAIResponse, Position: 0, Raw: json.RawMessage(`{"type":"image_generation_call","id":"image"}`)}}}))
	aggregate.Add(InternalResponseFromStreamEvents([]StreamEvent{{Kind: StreamEventKindMetadata, ProviderExtensions: &ProviderExtensions{OpenAI: &OpenAIExtension{RawResponseItems: json.RawMessage(`[]`)}}}}))
	response := aggregate.Response()
	if string(response.RawResponsesOutputItems) != "[]" || len(response.ProviderExtensions.OpenAIResponses.Items) != 0 {
		t.Fatalf("empty snapshot lost: %#v", response)
	}
}

func TestInvalidNativeAggregationIsExplicitFailure(t *testing.T) {
	for _, raw := range []string{`{`, `null`, `{}`, `{"type":"text","text":123}`} {
		var aggregate StreamAggregator
		aggregate.Add(InternalResponseFromStreamEvents([]StreamEvent{{Kind: StreamEventKindNativeItem, NativeItem: &ProtocolItem{Format: APIFormatAnthropicMessage, Position: 0, Raw: json.RawMessage(raw)}}}))
		response := aggregate.Response()
		if response == nil || response.Error == nil || response.Error.Detail.Type != "protocol_aggregation_error" {
			t.Fatalf("invalid native swallowed: %s -> %#v", raw, response)
		}
	}
}

func TestNativeUnknownFieldsRemainOpaque(t *testing.T) {
	var aggregate StreamAggregator
	raw := json.RawMessage(`{"type":"future_block","data":{"counter":9007199254740993},"text":["opaque"],"signature":false}`)
	aggregate.Add(InternalResponseFromStreamEvents([]StreamEvent{{Kind: StreamEventKindNativeItem, NativeItem: &ProtocolItem{Format: APIFormatAnthropicMessage, Position: 0, Raw: raw}}}))
	response := aggregate.Response()
	if response == nil || response.Error != nil {
		t.Fatalf("opaque block rejected: %#v", response)
	}
	items := response.Choices[0].Message.ProviderExtensions.Anthropic.Items
	var want, got ProtocolFields
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(items[0].Raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("opaque fields changed: %s", items[0].Raw)
	}
}

func TestParallelThinkingSignaturesStayWithContentIndex(t *testing.T) {
	var aggregate StreamAggregator
	events := []StreamEvent{
		{Kind: StreamEventKindThinkingDelta, ContentIndex: streamIndex(0), Delta: &StreamDelta{Thinking: "first "}},
		{Kind: StreamEventKindThinkingDelta, ContentIndex: streamIndex(1), Delta: &StreamDelta{Thinking: "second"}},
		{Kind: StreamEventKindSignatureDelta, ContentIndex: streamIndex(1), Delta: &StreamDelta{Signature: "two"}},
		{Kind: StreamEventKindSignatureDelta, ContentIndex: streamIndex(0), Delta: &StreamDelta{Signature: "one-"}},
		{Kind: StreamEventKindThinkingDelta, ContentIndex: streamIndex(0), Delta: &StreamDelta{Thinking: "part"}},
		{Kind: StreamEventKindSignatureDelta, ContentIndex: streamIndex(0), Delta: &StreamDelta{Signature: "signature"}},
	}
	aggregate.Add(InternalResponseFromStreamEvents(events))
	response := aggregate.Response()
	blocks := response.Choices[0].Message.ReasoningBlocks
	if len(blocks) != 2 || blocks[0].Text != "first part" || blocks[0].Signature != "one-signature" || blocks[1].Text != "second" || blocks[1].Signature != "two" {
		t.Fatalf("thinking blocks mixed: %#v", blocks)
	}
}
