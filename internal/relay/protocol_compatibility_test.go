package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestProtocolCompatibilityCompletedStreams(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		inbound  inbound.InboundType
		outbound outbound.OutboundType
		format   model.APIFormat
		body     string
		terminal string
	}{
		{
			name: "chat", inbound: inbound.InboundTypeOpenAIChat, outbound: outbound.OutboundTypeOpenAIChat, format: model.APIFormatOpenAIChatCompletion,
			body:     "data: {\"id\":\"r\",\"model\":\"gpt-4o\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5},\"error\":{}}\n\ndata: [DONE]\n\n",
			terminal: "[DONE]",
		},
		{
			name: "responses", inbound: inbound.InboundTypeOpenAIResponse, outbound: outbound.OutboundTypeOpenAIResponse, format: model.APIFormatOpenAIResponse,
			body:     "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"gpt-4o\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5},\"error\":{}}}\n\n",
			terminal: "response.completed",
		},
		{
			name: "anthropic", inbound: inbound.InboundTypeAnthropic, outbound: outbound.OutboundTypeAnthropic, format: model.APIFormatAnthropicMessage,
			body:     "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"r\",\"model\":\"gpt-4o\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":3}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2},\"error\":{}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			terminal: "message_stop",
		},
	} {
		for _, passthrough := range []bool{false, true} {
			if fixture.name == "chat" && passthrough {
				continue
			}
			for _, contentType := range []string{"text/event-stream", "application/json", "text/plain"} {
				t.Run(fixture.name+"/"+contentType+"/"+map[bool]string{false: "converted", true: "passthrough"}[passthrough], func(t *testing.T) {
					attempt, recorder := newEmptyStreamTestAttempt(t, fixture.inbound, fixture.format, fixture.outbound)
					response := sseTestResponse(fixture.body)
					response.Header.Set("Content-Type", contentType)
					if err := runLifecycleSSE(attempt, context.Background(), response, passthrough); err != nil {
						t.Fatalf("valid terminal-only stream rejected: %v", err)
					}
					if !strings.Contains(recorder.Body.String(), fixture.terminal) {
						t.Fatalf("completion lost: %s", recorder.Body.String())
					}
					attempt.collectResponse()
					if attempt.metrics.InternalResponse == nil || attempt.metrics.InternalResponse.Error != nil || attempt.metrics.Stats.InputToken != 3 || attempt.metrics.Stats.OutputToken != 2 {
						t.Fatalf("usage or valid result lost: %+v", attempt.metrics)
					}
				})
			}
		}
	}
}

func TestProtocolCompatibilityMIMESniffRespectsCancellation(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		attempt, _ := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
		reader, writer := io.Pipe()
		defer writer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: reader}
		finished := make(chan error, 1)
		go func() {
			finished <- runLifecycleSSE(attempt, ctx, response, passthrough)
		}()
		select {
		case err := <-finished:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("sniff ignored cancellation: %v", err)
			}
		case <-time.After(2 * time.Second):
			_ = reader.Close()
			cancel()
			t.Fatal("MIME sniff blocked stream cancellation")
		}
		cancel()
	}
}

func TestProtocolCompatibilityIncorrectMIMEKeepsErrors(t *testing.T) {
	for _, body := range []string{
		`<html>Bad Gateway</html>`,
		`{"error":{"message":"rate limit exceeded"}}`,
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"rate limit exceeded\"}}\n\n",
		"data: {invalid json}\n\n",
	} {
		for _, passthrough := range []bool{false, true} {
			attempt, recorder := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
			response := sseTestResponse(body)
			response.Header.Set("Content-Type", "text/plain")
			if err := runLifecycleSSE(attempt, context.Background(), response, passthrough); err == nil {
				t.Fatalf("failure accepted as success: %s", body)
			}
			if recorder.Body.Len() != 0 {
				t.Fatalf("failure forwarded as normal content: %s", recorder.Body.String())
			}
		}
	}
}

func TestWSPassthroughEmptyErrorDoesNotRejectCompletion(t *testing.T) {
	stats := &wsPassthroughStats{}
	observeWSPassthroughEvent(stats, []byte(`{"type":"response.completed","error":{},"response":{"id":"r","status":"completed","error":{},"output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`))
	if stats.Error != nil || stats.Usage == nil || stats.Usage.TotalTokens != 5 || stats.ResponseID != "r" {
		t.Fatalf("empty error broke successful WS result: %+v", stats)
	}
	observeWSPassthroughEvent(stats, []byte(`{"type":"response.failed","status":429,"error":{"message":"rate limit exceeded","code":"rate_limit_exceeded"}}`))
	if stats.Error == nil || stats.Error.Status != http.StatusTooManyRequests {
		t.Fatalf("real WS error lost: %+v", stats)
	}
}
