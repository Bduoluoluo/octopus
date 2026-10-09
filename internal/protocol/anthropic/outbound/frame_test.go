package outbound

import (
	"context"
	"testing"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func TestFrameParallelToolsAndTerminal(t *testing.T) {
	codec := &MessageOutbound{}
	frames := []string{
		`{"type":"message_start","message":{"id":"parallel","model":"fixture","usage":{"input_tokens":5,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"a","name":"first","input":{}}}`,
		`{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"b","name":"second","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{\"b\":2}"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"1}"}}`,
		`{"type":"content_block_stop","index":4}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
		`{"type":"message_stop"}`,
	}
	arguments := map[string]string{}
	for _, frame := range frames {
		events, err := codec.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte(frame)})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind == model.StreamEventKindToolCallDelta {
				arguments[event.ToolCall.ID] += event.Delta.Arguments
				if event.ContentIndex == nil || event.Index != 0 {
					t.Fatalf("bad event indices: %#v", event)
				}
			}
		}
	}
	if arguments["a"] != `{"a":1}` || arguments["b"] != `{"b":2}` {
		t.Fatalf("parallel tools mixed: %#v", arguments)
	}
	if _, err := codec.EndStream(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := codec.EndStream(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := codec.CloseStream(); err != nil {
		t.Fatal(err)
	}
	if _, err := codec.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte(`{"type":"ping"}`)}); err == nil {
		t.Fatal("closed stream accepted frame")
	}
}

func TestFrameEndRejectsMissingTerminalAndRetainsErrors(t *testing.T) {
	for _, frame := range []string{`[DONE]`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, `{`} {
		codec := &MessageOutbound{}
		_, _ = codec.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte(frame)})
		if _, err := codec.EndStream(context.Background()); err == nil {
			t.Fatalf("incomplete stream accepted: %s", frame)
		}
	}
	codec := &MessageOutbound{}
	if _, err := codec.TransformStreamFrame(context.Background(), model.StreamFrame{Event: "message_stop", Data: []byte(`{"type":"message_delta"}`)}); err == nil {
		t.Fatal("SSE event/data mismatch accepted")
	}
}

func TestFrameUsageSnapshotsPreserveExplicitZero(t *testing.T) {
	codec := &MessageOutbound{}
	frames := []string{
		`{"type":"message_start","message":{"id":"usage","model":"fixture","usage":{"input_tokens":9,"output_tokens":1,"cache_read_input_tokens":7,"cache_creation_input_tokens":5}}}`,
		`{"type":"message_delta","usage":{"input_tokens":0,"output_tokens":2,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`,
		`{"type":"message_delta","usage":{"output_tokens":3}}`,
	}
	var usage *model.Usage
	for _, frame := range frames {
		events, err := codec.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte(frame)})
		if err != nil {
			t.Fatal(err)
		}
		usage = lastUsageDelta(events)
	}
	if usage == nil || usage.PromptTokens != 0 || usage.CacheReadInputTokens != 0 || usage.CacheCreationInputTokens != 0 || usage.CompletionTokens != 3 || usage.TotalTokens != 3 {
		t.Fatalf("wrong snapshot %#v", usage)
	}
}
