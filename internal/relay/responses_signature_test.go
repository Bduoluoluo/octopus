package relay

import (
	"context"
	"strings"
	"testing"

	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestResponsesStreamSignatureUpdatePreservesResult(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			name := "transformed"
			if passthrough {
				name = "passthrough"
			}
			status := "completed"
			if failed {
				status = "failed"
			}
			t.Run(name+"/"+status, func(t *testing.T) {
				attempt, recorder := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
				errorField := ""
				if failed {
					errorField = `,"error":{"code":"upstream_error","message":"actual upstream failure"}`
				}
				frames := []string{
					`{"type":"response.created","response":{"id":"response","model":"gpt-4o","status":"in_progress","output":[]}}`,
					`{"type":"response.output_item.done","output_index":0,"item":{"id":"reason","type":"reasoning","encrypted_content":"early","summary":[]}}`,
					`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"hello"}`,
					`{"type":"response.` + status + `","response":{"id":"response","model":"gpt-4o","status":"` + status + `","output":[{"id":"reason","type":"reasoning","encrypted_content":"final","summary":[]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}` + errorField + `}}`,
				}
				body := "data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"
				err := runLifecycleSSE(attempt, context.Background(), sseTestResponse(body), passthrough)
				if failed {
					if err == nil || !strings.Contains(err.Error(), "actual upstream failure") {
						t.Fatalf("real upstream failure masked: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("completed result rejected: %v", err)
				}
				if !strings.Contains(recorder.Body.String(), `"delta":"hello"`) || !strings.Contains(recorder.Body.String(), `"type":"response.completed"`) || !strings.Contains(recorder.Body.String(), `"encrypted_content":"final"`) {
					t.Fatalf("incomplete downstream result: %s", recorder.Body.String())
				}
				attempt.collectResponse()
				response := attempt.metrics.InternalResponse
				if response == nil || response.Usage == nil || response.Usage.TotalTokens != 5 || response.Status != "completed" {
					t.Fatalf("completion or usage missing: %+v", response)
				}
				if response.Choices[0].Message.ReasoningSignature == nil || *response.Choices[0].Message.ReasoningSignature != "final" {
					t.Fatalf("incorrect final signature: %+v", response.Choices[0].Message)
				}
			})
		}
	}
}
