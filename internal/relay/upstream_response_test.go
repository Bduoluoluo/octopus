package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestUpstreamPayloadError(t *testing.T) {
	for _, test := range []struct {
		body   string
		event  string
		status int
	}{
		{`{"error":{"code":"rate_limit_exceeded","message":"rate limit exceeded"}}`, "", 429},
		{`{"error":{"code":503,"message":"unavailable"}}`, "", 503},
		{`{"error":"platform failure"}`, "", 502},
		{`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"platform failure"}}}`, "", 502},
		{`{"status":"failed","output":[],"usage":{"input_tokens":100}}`, "", 502},
		{`{"message":"failed"}`, "error", 502},
		{`{"success":false,"message":"failed"}`, "", 502},
		{`{"code":500,"message":"platform failed"}`, "", 500},
		{`{"status":503,"message":"platform failed"}`, "", 503},
		{`{"error":null,"status":"completed","output":[]}`, "", 0},
		{`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"error":null}`, "", 0},
		{`{"choices":[{"message":{"content":"upstream error: 429"}}]}`, "", 0},
		{`{"type":"response.output_text.delta","delta":"error"}`, "", 0},
	} {
		err := upstreamPayloadError([]byte(test.body), test.event)
		if test.status == 0 {
			if err != nil {
				t.Errorf("unexpected error for %s: %v", test.body, err)
			}
			continue
		}
		var responseErr *model.ResponseError
		if !errors.As(err, &responseErr) || responseErr.StatusCode != test.status {
			t.Errorf("%s: expected %d, got %v", test.body, test.status, err)
		}
	}
}

func TestUpstreamNonStreamErrorDoesNotWrite(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			attempt, recorder := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
			response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"rate limit exceeded"},"usage":{"input_tokens":100}}`))}
			var err error
			if passthrough {
				err = attempt.handleResponsePassthrough(context.Background(), response, model.PassthroughConfig{CollectMetrics: true})
			} else {
				err = attempt.handleResponse(context.Background(), response)
			}
			var upstreamErr *model.ResponseError
			if !errors.As(err, &upstreamErr) {
				t.Fatalf("expected upstream failure, got %v", err)
			}
			if recorder.Body.Len() != 0 || attempt.responseCollected.Load() {
				t.Fatal("error response was forwarded or billed")
			}
		})
	}
}

func TestUpstreamStreamErrorBeforeContentDoesNotWrite(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			attempt, recorder := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
			body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"bad\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
				"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"rate limit exceeded\"}}}\n\n"
			var err error
			if passthrough {
				err = attempt.handleStreamResponsePassthroughV2(context.Background(), sseTestResponse(body), model.PassthroughConfig{CollectMetrics: true})
			} else {
				err = attempt.handleStreamResponseV2(context.Background(), sseTestResponse(body))
			}
			var upstreamErr *model.ResponseError
			if !errors.As(err, &upstreamErr) {
				t.Fatalf("expected failure, got %v", err)
			}
			if recorder.Body.Len() != 0 || attempt.streamPayloadWritten.Load() || attempt.responseCollected.Load() {
				t.Fatalf("failed stream committed data: %s", recorder.Body.String())
			}
		})
	}
}

func TestUpstreamNamedSSEErrorDoesNotWrite(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		attempt, recorder := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
		body := "event: error\ndata: {\"code\":\"rate_limit_exceeded\",\"message\":\"rate limit exceeded\"}\n\n"
		var err error
		if passthrough {
			err = attempt.handleStreamResponsePassthroughV2(context.Background(), sseTestResponse(body), model.PassthroughConfig{})
		} else {
			err = attempt.handleStreamResponseV2(context.Background(), sseTestResponse(body))
		}
		var responseErr *model.ResponseError
		if !errors.As(err, &responseErr) || responseErr.StatusCode != 429 || recorder.Body.Len() != 0 {
			t.Fatalf("named SSE error lost: err=%v body=%q", err, recorder.Body.String())
		}
	}
}

func TestUpstreamStreamPreambleAndLegitimateContent(t *testing.T) {
	for _, test := range []struct {
		body    string
		content bool
	}{
		{`{"choices":[{"delta":{"role":"assistant","content":""}}]}`, false},
		{`{"type":"response.output_text.delta","delta":""}`, false},
		{`{"type":"content_block_delta","delta":{"type":"text_delta","text":""}}`, false},
		{`{"type":"content_block_start","content_block":{"type":"text","text":""}}`, false},
		{`{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool_1","name":"search","input":{}}}`, true},
		{`{"choices":[{"delta":{"tool_calls":[{"id":"tool_1","type":"function","function":{"name":"search","arguments":"{}"}}]}}]}`, true},
		{`{"choices":[{"delta":{"refusal":"cannot comply"}}]}`, true},
		{`{"type":"response.output_text.delta","delta":"rate limit exceeded"}`, true},
		{`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`, true},
	} {
		if got := streamEventHasContent([]byte(test.body), ""); got != test.content {
			t.Errorf("content=%t expected %t: %s", got, test.content, test.body)
		}
	}
}

func TestUpstreamErrorFailoverAndAccounting(t *testing.T) {
	for _, mode := range []struct{ streaming, partial bool }{{false, false}, {true, false}, {true, true}} {
		streaming, partial := mode.streaming, mode.partial
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/partial=%t/fallback=%t", streaming, partial, fallback), func(t *testing.T) {
				ctx := setupRelayTestDB(t)
				if err := op.LLMCreate(dbmodel.LLMInfo{Name: "test-model", LLMPrice: dbmodel.LLMPrice{Input: 1000000, Output: 2000000}}, ctx); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = op.LLMDelete("test-model", context.Background()) })
				var firstHits, secondHits atomic.Int32
				first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					firstHits.Add(1)
					if streaming {
						writer.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(writer, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"bad\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":999}}}\n\n")
						writer.(http.Flusher).Flush()
						if partial {
							_, _ = io.WriteString(writer, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial-output\"}}\n\n")
							_, _ = io.WriteString(writer, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":999}}\n\n")
							writer.(http.Flusher).Flush()
						}
						_, _ = io.WriteString(writer, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"rate limit exceeded\"}}\n\n")
					} else {
						writer.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(writer, `{"error":{"message":"rate limit exceeded"},"usage":{"input_tokens":999}}`)
					}
				}))
				defer first.Close()
				second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					secondHits.Add(1)
					if streaming {
						writer.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(writer, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"good\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"usage\":{\"input_tokens\":3}}}\n\n"+
							"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fallback-ok\"}}\n\n"+
							"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"+"data: {\"type\":\"message_stop\"}\n\n")
					} else {
						writer.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(writer, `{"id":"good","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"fallback-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
					}
				}))
				defer second.Close()
				group := &dbmodel.Group{Name: "guard-failover", Mode: dbmodel.GroupModeFailover}
				if err := op.GroupCreate(group, ctx); err != nil {
					t.Fatal(err)
				}
				var channels []*dbmodel.Channel
				for index, server := range []*httptest.Server{first, second} {
					if index == 1 && !fallback {
						break
					}
					channel := &dbmodel.Channel{Name: fmt.Sprint("guard-channel-", index), Type: outbound.OutboundTypeAnthropic, Enabled: true, BaseUrls: []dbmodel.BaseUrl{{URL: server.URL + "/v1"}}, Keys: []dbmodel.ChannelKey{{Enabled: true, ChannelKey: "test-key"}}, Model: "test-model"}
					if err := op.ChannelCreate(channel, ctx); err != nil {
						t.Fatal(err)
					}
					channels = append(channels, channel)
					if err := op.GroupItemAdd(&dbmodel.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "test-model", Priority: index + 1, Weight: 1}, ctx); err != nil {
						t.Fatal(err)
					}
				}
				recorder := httptest.NewRecorder()
				client, _ := gin.CreateTestContext(recorder)
				client.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":"guard-failover","max_tokens":64,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, streaming)))
				client.Request.Header.Set("Content-Type", "application/json")
				Handler(inbound.InboundTypeAnthropic, client)
				if firstHits.Load() != 1 {
					t.Fatalf("first hits=%d", firstHits.Load())
				}
				stats := op.StatsAPIKeyGet(0)
				if partial {
					if secondHits.Load() != 0 || !strings.Contains(recorder.Body.String(), "event: error") || stats.RequestSuccess != 0 || stats.RequestFailed != 1 || stats.InputToken != 0 || stats.OutputToken != 0 || stats.InputCost != 0 || stats.OutputCost != 0 {
						t.Fatalf("partial failure hidden or billed: %s stats=%+v", recorder.Body.String(), stats)
					}
				} else if fallback {
					if secondHits.Load() != 1 || !strings.Contains(recorder.Body.String(), "fallback-ok") || strings.Contains(recorder.Body.String(), "999") {
						t.Fatalf("failover failed: %s", recorder.Body.String())
					}
					if stats.InputToken != 3 || stats.OutputToken != 2 || stats.InputCost != 3 || stats.OutputCost != 4 || stats.RequestSuccess != 1 {
						t.Fatalf("unexpected stats: %+v", stats)
					}
				} else if recorder.Code != 429 || stats.RequestFailed != 1 || stats.InputToken != 0 || stats.InputCost != 0 || stats.RequestSuccess != 0 {
					t.Fatalf("failure counted as success: status=%d stats=%+v", recorder.Code, stats)
				}
				for index, original := range channels {
					channel, err := op.ChannelGet(original.ID, ctx)
					if err != nil {
						t.Fatal(err)
					}
					expected := float64(0)
					if index == 1 && fallback && !partial {
						expected = 7
					}
					if channel.Keys[0].TotalCost != expected {
						t.Fatalf("channel %d cost=%v want=%v", index, channel.Keys[0].TotalCost, expected)
					}
				}
			})
		}
	}
}
