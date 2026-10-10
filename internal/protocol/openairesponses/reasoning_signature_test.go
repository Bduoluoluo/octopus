package openairesponses_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	wire "github.com/xuanli27/octopus/internal/protocol/openairesponses"
	"github.com/xuanli27/octopus/internal/protocol/openairesponses/inbound"
	"github.com/xuanli27/octopus/internal/protocol/openairesponses/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func TestResponsesReasoningSignatureUpdates(t *testing.T) {
	decoder := &outbound.ResponseOutbound{}
	for _, fixture := range []struct {
		name        string
		finalField  string
		emptyOutput bool
		want        string
	}{
		{name: "changed at response completion", finalField: `,"encrypted_content":"final"`, want: "final"},
		{name: "same signature is not duplicated", finalField: `,"encrypted_content":"revised"`, want: "revised"},
		{name: "missing final signature retains latest", want: "revised"},
		{name: "empty output retains latest", emptyOutput: true, want: "revised"},
		{name: "explicit empty signature", finalField: `,"encrypted_content":""`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			decoder.ResetStream()
			encoder := &inbound.ResponseInbound{}
			finalOutput := `[{"id":"reason","type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]` + fixture.finalField + `},{"id":"message","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]`
			if fixture.emptyOutput {
				finalOutput = `[]`
			}
			frames := []string{
				`{"type":"response.created","response":{"id":"response","model":"model","status":"in_progress","output":[]}}`,
				`{"type":"response.output_item.added","output_index":0,"item":{"id":"reason","type":"reasoning","encrypted_content":"initial","summary":[]}}`,
				`{"type":"response.output_item.done","output_index":0,"item":{"id":"reason","type":"reasoning","encrypted_content":"early","summary":[{"type":"summary_text","text":"thinking"}]}}`,
				`{"type":"response.output_text.delta","output_index":1,"item_id":"message","content_index":0,"delta":"hello"}`,
				`{"type":"response.output_item.done","output_index":0,"item":{"id":"reason","type":"reasoning","encrypted_content":"revised","summary":[{"type":"summary_text","text":"thinking"}]}}`,
				`{"type":"response.completed","response":{"id":"response","model":"model","status":"completed","output":` + finalOutput + `,"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
			}
			var signatures []string
			for index, frame := range frames {
				events, err := decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte(frame)})
				if err != nil {
					t.Fatalf("frame %d: %v", index, err)
				}
				for _, event := range events {
					if event.Kind == model.StreamEventKindSignatureDelta {
						signatures = append(signatures, event.Delta.Signature)
					}
				}
				if index < len(frames)-1 && len(signatures) > 0 {
					t.Fatalf("signature snapshot emitted before final response: %v", signatures)
				}
				if index == len(frames)-2 {
					if _, err := decoder.EndStream(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatalf("signature update must not imply response completion: %v", err)
					}
				}
				encoded, err := encoder.TransformStreamEvents(context.Background(), events)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(encoded), `"type":"`+decodeEventType(t, frame)+`"`) {
					t.Fatalf("native frame lost: %s", encoded)
				}
			}
			if _, err := decoder.EndStream(context.Background()); err != nil {
				t.Fatalf("completed response rejected at EOF: %v", err)
			}
			if fixture.want == "" && len(signatures) != 0 || fixture.want != "" && (len(signatures) != 1 || signatures[0] != fixture.want) {
				t.Fatalf("expected one final signature %q, got %v", fixture.want, signatures)
			}
			response, err := encoder.GetInternalResponse(context.Background())
			if err != nil || response == nil {
				t.Fatalf("aggregate failed: %v", err)
			}
			if response.Error != nil || response.Status != "completed" || response.Usage == nil || response.Usage.TotalTokens != 5 {
				t.Fatalf("completion or usage lost: %+v", response)
			}
			message := response.Choices[0].Message
			if message.Content.Content == nil || *message.Content.Content != "hello" || message.ReasoningContent == nil || *message.ReasoningContent != "thinking" {
				t.Fatalf("content lost or duplicated: %+v", message)
			}
			signature := ""
			if message.ReasoningSignature != nil {
				signature = *message.ReasoningSignature
			}
			if signature != fixture.want {
				t.Fatalf("aggregate signature: got %q, want %q", signature, fixture.want)
			}
			var items []wire.Item
			if err := json.Unmarshal(response.RawResponsesOutputItems, &items); err != nil {
				t.Fatal(err)
			}
			if len(items) != 2 || items[0].EncryptedContent == nil || *items[0].EncryptedContent != fixture.want {
				t.Fatalf("replay snapshot lost latest signature: %s", response.RawResponsesOutputItems)
			}
		})
	}
}

func decodeEventType(t *testing.T, frame string) string {
	t.Helper()
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(frame), &event); err != nil {
		t.Fatal(err)
	}
	return event.Type
}

func TestResponsesReasoningSignatureUpdatesKeepItemsSeparate(t *testing.T) {
	decoder := &outbound.ResponseOutbound{}
	encoder := &inbound.ResponseInbound{}
	for _, frame := range []string{
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"first","type":"reasoning","encrypted_content":"old-first","summary":[]}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"second","type":"reasoning","encrypted_content":"old-second","summary":[]}}`,
		`{"type":"response.completed","response":{"id":"response","status":"completed","output":[{"id":"first","type":"reasoning","encrypted_content":"final-first","summary":[]},{"id":"second","type":"reasoning","encrypted_content":"final-second","summary":[]}]}}`,
	} {
		events, err := decoder.TransformStreamEvent(context.Background(), []byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := encoder.TransformStreamEvents(context.Background(), events); err != nil {
			t.Fatal(err)
		}
	}
	response, err := encoder.GetInternalResponse(context.Background())
	if err != nil || response == nil || len(response.Choices) != 1 {
		t.Fatalf("aggregate: %+v, %v", response, err)
	}
	blocks := response.Choices[0].Message.ReasoningBlocks
	if len(blocks) != 2 || blocks[0].Signature != "final-first" || blocks[1].Signature != "final-second" {
		t.Fatalf("reasoning items merged or stale: %+v", blocks)
	}
}
