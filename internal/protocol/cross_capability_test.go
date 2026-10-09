package protocol_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestChatRejectsNativeAndOpaqueResponsesOutput(t *testing.T) {
	for _, item := range []string{
		`{"type":"image_generation_call","id":"i","result":"image"}`,
		`{"type":"custom_tool_call","id":"c","call_id":"call","name":"shell","input":"hi"}`,
		`{"type":"reasoning","id":"r","summary":[{"type":"summary_text","text":"thought"}],"encrypted_content":"secret"}`,
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
		`{"type":"reasoning","encrypted_content":"opaque","summary":[]}`,
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
