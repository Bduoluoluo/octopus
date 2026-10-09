package anthropic

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPinnedThinkingSignatureWireShape(t *testing.T) {
	empty := ""
	for _, block := range []MessageContentBlock{
		{Type: "thinking"},
		{Type: "thinking", Thinking: &empty, Signature: &empty},
	} {
		encoded, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		var actual map[string]any
		if err := json.Unmarshal(encoded, &actual); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, map[string]any{"type": "thinking", "thinking": "", "signature": ""}) {
			t.Fatalf("thinking wire=%s", encoded)
		}
	}
	deltaType, thinking := "thinking_delta", "Thinking..."
	encoded, err := json.Marshal(StreamDelta{Type: &deltaType, Thinking: &thinking, Signature: &empty})
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, map[string]any{"type": "thinking_delta", "thinking": "Thinking..."}) {
		t.Fatalf("thinking delta=%s", encoded)
	}
}
