package model

import (
	"encoding/json"
	"testing"
)

func TestNativeStreamAggregationPreservesOpaqueFields(t *testing.T) {
	var aggregate StreamAggregator
	aggregate.Add(InternalResponseFromStreamEvents([]StreamEvent{
		{Kind: StreamEventKindContentBlockStart, ContentIndex: streamIndex(0), ContentBlock: &StreamContentBlock{Type: "text"}, NativeItem: &ProtocolItem{Format: APIFormatAnthropicMessage, Position: 0, Raw: json.RawMessage(`{"type":"text","text":"hello","citations":{"provider_config":false}}`)}},
		{Kind: StreamEventKindContentBlockStop, ContentIndex: streamIndex(0)},
		{Kind: StreamEventKindToolCallStart, ContentIndex: streamIndex(1), ToolCall: &ToolCall{Index: 0, ID: "call", Type: "function", Function: FunctionCall{Name: "lookup"}}, NativeItem: &ProtocolItem{Format: APIFormatAnthropicMessage, Position: 1, Raw: json.RawMessage(`{"type":"tool_use","id":"call","name":"lookup","input":{}}`)}},
		{Kind: StreamEventKindToolCallDelta, ContentIndex: streamIndex(1), ToolCall: &ToolCall{Index: 0, ID: "call"}, Delta: &StreamDelta{Arguments: "{unfinished"}},
		{Kind: StreamEventKindToolCallStop, ContentIndex: streamIndex(1)},
		{Kind: StreamEventKindMessageStop, StopReason: FinishReasonLength},
	}))
	response := aggregate.Response()
	if response == nil || response.Error != nil || len(response.Choices) != 1 {
		t.Fatalf("native provider result rejected: %+v", response)
	}
	message := response.Choices[0].Message
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Arguments != "{unfinished" {
		t.Fatalf("tool payload lost: %+v", message)
	}
	items := message.ProviderExtensions.Anthropic.Items
	if len(items) != 2 {
		t.Fatalf("native items lost: %+v", items)
	}
	var text, tool ProtocolFields
	if err := json.Unmarshal(items[0].Raw, &text); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(items[1].Raw, &tool); err != nil {
		t.Fatal(err)
	}
	if string(text["citations"]) != `{"provider_config":false}` || string(tool["input"]) != `"{unfinished"` {
		t.Fatalf("opaque payload changed: %s %s", items[0].Raw, items[1].Raw)
	}
}
