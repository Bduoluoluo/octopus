package model

import (
	"encoding/json"
	"testing"
)

func TestCloneRequestProtocolIsolation(t *testing.T) {
	text := "original"
	request := &InternalLLMRequest{
		Messages:           []Message{{Role: "user", Content: MessageContent{Content: &text}}},
		Tools:              []Tool{{Type: "function", Function: Function{Name: "fixture", Parameters: json.RawMessage(`{"type":"object"}`)}}},
		ProviderExtensions: &ProviderExtensions{OpenAIResponses: &ProtocolExtension{Items: []ProtocolItem{{Position: 0, Raw: json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}}}},
	}
	cloned := CloneRequest(request)
	*cloned.Messages[0].Content.Content = "changed"
	cloned.Tools[0].Function.Parameters[0] = '['
	cloned.ProviderExtensions.OpenAIResponses.Items[0].Raw[0] = '['
	if text != "original" || !json.Valid(request.Tools[0].Function.Parameters) || !json.Valid(request.ProviderExtensions.OpenAIResponses.Items[0].Raw) {
		t.Fatal("attempt clone mutated original request")
	}
	encoded, err := json.Marshal(request.ProviderExtensions)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ProviderExtensions
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OpenAIResponses.Items[0].Position != 0 || string(decoded.OpenAIResponses.Items[0].Raw) != string(request.ProviderExtensions.OpenAIResponses.Items[0].Raw) {
		t.Fatal("serialization lost opaque item or zero position")
	}
}
