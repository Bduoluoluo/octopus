package protocol_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestChatRejectsUnrepresentableNativeResponsesOutput(t *testing.T) {
	for _, item := range []string{
		`{"type":"image_generation_call","id":"i","result":"image"}`,
		`{"type":"custom_tool_call","id":"c","call_id":"call","name":"shell","input":"hi"}`,
	} {
		response, err := outbound.Get(outbound.OutboundTypeOpenAIResponse).TransformResponse(context.Background(), &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"r","model":"m","status":"completed","output":[` + item + `]}`))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformResponse(context.Background(), response); err == nil {
			t.Fatalf("native output silently lost: %s", item)
		}
	}
}

func TestChatRejectsNativeResponsesHistory(t *testing.T) {
	for _, item := range []string{
		`{"type":"item_reference","id":"r"}`,
		`{"type":"custom_tool_call","call_id":"c","name":"shell","input":"hi"}`,
	} {
		request, err := inbound.Get(inbound.InboundTypeOpenAIResponse).TransformRequest(context.Background(), []byte(`{"model":"m","input":[{"role":"user","content":"hi"},`+item+`]}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := outbound.Get(outbound.OutboundTypeOpenAIChat).TransformRequest(context.Background(), request, "https://example.invalid/v1", "fixture"); err == nil {
			t.Fatalf("native history silently lost: %s", item)
		}
	}
}

func TestChatPreservesResponsesReasoningOutputAsMetadata(t *testing.T) {
	fixture := `{"id":"r","model":"m","status":"completed","output":[{"type":"reasoning","id":"think","summary":[{"type":"summary_text","text":"thought"}],"encrypted_content":"secret"},{"type":"message","id":"msg","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`
	response, err := outbound.Get(outbound.OutboundTypeOpenAIResponse).TransformResponse(context.Background(), &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fixture))})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformResponse(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	assertChatPreservesOpaqueMetadata(t, encoded, "secret", "answer")
	if !strings.Contains(string(encoded), `"reasoning_content":"thought"`) {
		t.Fatalf("readable reasoning lost: %s", encoded)
	}
}

func TestChatPreservesResponsesReasoningHistoryAsMetadata(t *testing.T) {
	fixture := `{"model":"m","input":[{"role":"user","content":"hi"},{"type":"reasoning","id":"think","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"thought"}]}]}`
	request, err := inbound.Get(inbound.InboundTypeOpenAIResponse).TransformRequest(context.Background(), []byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := outbound.Get(outbound.OutboundTypeOpenAIChat).TransformRequest(context.Background(), request, "https://example.invalid/v1", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer encoded.Body.Close()
	body, err := io.ReadAll(encoded.Body)
	if err != nil {
		t.Fatal(err)
	}
	assertChatPreservesOpaqueMetadata(t, body, "opaque", "hi")
	if !strings.Contains(string(body), `"reasoning_content":"thought"`) {
		t.Fatalf("readable reasoning history lost: %s", body)
	}
}

func assertChatPreservesOpaqueMetadata(t *testing.T, body []byte, encrypted, text string) {
	t.Helper()
	if !json.Valid(body) {
		t.Fatalf("invalid Chat encoding: %s", body)
	}
	for _, field := range []string{`"encrypted_content":"` + encrypted + `"`, `"reasoning_items":`, `"format":"openai/responses"`, `"content":"` + text + `"`} {
		if !strings.Contains(string(body), field) {
			t.Fatalf("lost %s: %s", field, body)
		}
	}
	if strings.Contains(string(body), `"reasoning_signature"`) || strings.Contains(string(body), `"thought_signature"`) {
		t.Fatalf("Responses encryption reused as Chat signature: %s", body)
	}
}
