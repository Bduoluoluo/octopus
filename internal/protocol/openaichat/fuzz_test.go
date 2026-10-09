package openaichat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func FuzzChatRequestRoundTrip(f *testing.F) {
	f.Add([]byte(`{"model":"m","messages":[{"role":"user","content":""}]}`))
	f.Add([]byte(`{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		request, err := DecodeRequest(data)
		if err != nil {
			return
		}
		encoded, err := EncodeRequest(context.Background(), request)
		if err != nil {
			return
		}
		if !json.Valid(encoded) {
			t.Fatalf("invalid encoding: %s", encoded)
		}
		if _, err := DecodeRequest(encoded); err != nil {
			t.Fatalf("invalid round trip: %s: %v", encoded, err)
		}
	})
}

func FuzzChatToolArgumentFragments(f *testing.F) {
	f.Add("alpha", "{\"x\":1}", uint8(4))
	f.Fuzz(func(t *testing.T, name, arguments string, split uint8) {
		runes := []rune(arguments)
		arguments = string(runes)
		cut := int(split)
		if cut > len(runes) {
			cut = len(runes)
		}
		var decoder StreamDecoder
		var aggregate model.StreamAggregator
		for index, fragment := range []string{string(runes[:cut]), string(runes[cut:])} {
			tool := ToolCall{Index: 0, Function: FunctionCall{Arguments: fragment}}
			if index == 0 {
				tool.ID = "call"
				tool.Type = "function"
				tool.Function.Name = name
			}
			body, err := json.Marshal(Response{ID: "r", Model: "m", Object: "chat.completion.chunk", Choices: []Choice{{Index: 0, Delta: &Message{ToolCalls: []ToolCall{tool}}}}})
			if err != nil {
				t.Fatal(err)
			}
			events, err := decoder.Push(context.Background(), model.StreamFrame{Data: body})
			if err != nil {
				t.Fatal(err)
			}
			aggregate.Add(model.InternalResponseFromStreamEvents(events))
		}
		response := aggregate.Response()
		if response == nil || len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 {
			t.Fatalf("lost tool: %#v", response)
		}
		expected, err := json.Marshal(arguments)
		if err != nil {
			t.Fatal(err)
		}
		var normalized string
		if err := json.Unmarshal(expected, &normalized); err != nil {
			t.Fatal(err)
		}
		if response.Choices[0].Message.ToolCalls[0].Function.Arguments != normalized {
			t.Fatal("tool arguments changed")
		}
	})
}
