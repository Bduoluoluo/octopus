package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tmaxmax/go-sse"
	dbmodel "github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/relay/stream"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func cacheRatioChannel(channelType outbound.OutboundType, percent float64) *dbmodel.Channel {
	return &dbmodel.Channel{Type: channelType, CacheRatioEnabled: true, CacheRatioMin: percent, CacheRatioMax: percent}
}

func cacheRatioValue(t *testing.T, data []byte, path ...string) int64 {
	t.Helper()
	raw := json.RawMessage(data)
	for _, key := range path {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatalf("invalid JSON %s: %v", raw, err)
		}
		raw = object[key]
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("missing/invalid token field %v in %s", path, data)
	}
	return value
}

func TestCacheRatioProtocolPayloads(t *testing.T) {
	for _, test := range []struct {
		name        string
		channelType outbound.OutboundType
		body        string
		path        []string
	}{
		{"chat", outbound.OutboundTypeOpenAIChat, `{"usage":{"prompt_tokens":1000,"completion_tokens":20,"total_tokens":1020,"prompt_tokens_details":{"audio_tokens":8}},"metadata":{"large":9007199254740993}}`, []string{"usage", "prompt_tokens_details", "cached_tokens"}},
		{"responses", outbound.OutboundTypeOpenAIResponse, `{"usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020,"input_tokens_details":null}}`, []string{"usage", "input_tokens_details", "cached_tokens"}},
		{"responses-event", outbound.OutboundTypeOpenAIResponse, `{"type":"response.completed","response":{"usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020}}}`, []string{"response", "usage", "input_tokens_details", "cached_tokens"}},
		{"anthropic", outbound.OutboundTypeAnthropic, `{"usage":{"input_tokens":800,"cache_read_input_tokens":100,"cache_creation_input_tokens":100,"output_tokens":20}}`, []string{"usage", "cache_read_input_tokens"}},
		{"gemini", outbound.OutboundTypeGemini, `{"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":20,"totalTokenCount":1020}}`, []string{"usageMetadata", "cachedContentTokenCount"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, percent := range []float64{0, 60, 100} {
				channel := cacheRatioChannel(test.channelType, percent)
				output := newCacheRatioOverride(channel).rewriteJSON([]byte(test.body))
				want := int64(percent * 10)
				if test.channelType == outbound.OutboundTypeAnthropic {
					want = min(want, 900)
					if cacheRatioValue(t, output, "usage", "input_tokens")+want+100 != 1000 {
						t.Fatalf("Anthropic total changed: %s", output)
					}
				}
				if got := cacheRatioValue(t, output, test.path...); got != want {
					t.Fatalf("percent=%v: got %d, want %d: %s", percent, got, want, output)
				}
				if test.name == "chat" {
					if cacheRatioValue(t, output, "usage", "prompt_tokens_details", "audio_tokens") != 8 ||
						cacheRatioValue(t, output, "metadata", "large") != 9007199254740993 {
						t.Fatal("unrelated details or integer precision lost")
					}
				}
			}
		})
	}
}

func TestCacheRatioDisabledMissingAndErrorUsage(t *testing.T) {
	for _, body := range []string{
		`{"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":200}}}`,
		`{"usage":null}`, `{"output":[{"text":"hello"}]}`, "not json", "[DONE]",
	} {
		channel := &dbmodel.Channel{}
		if got := newCacheRatioOverride(channel).rewriteJSON([]byte(body)); string(got) != body {
			t.Fatalf("disabled changed response: %s", got)
		}
	}
	override := newCacheRatioOverride(cacheRatioChannel(outbound.OutboundTypeOpenAIResponse, 50))
	for _, body := range []string{
		`{"usage":null}`, `{"usage":{"output_tokens":20}}`,
		`{"error":{"message":"failure"},"usage":{"input_tokens":100}}`,
		`{"usage":{"input_tokens":-1}}`, `{"usage":{"input_tokens":null}}`,
	} {
		if got := override.rewriteJSON([]byte(body)); string(got) != body {
			t.Fatalf("missing/error usage changed: %s", got)
		}
	}
}

func TestCacheRatioRandomStableWithinRequest(t *testing.T) {
	channel := &dbmodel.Channel{CacheRatioEnabled: true, CacheRatioMin: 23.5, CacheRatioMax: 76.5}
	for iteration := 0; iteration < 100; iteration++ {
		override := newCacheRatioOverride(channel)
		if override.ratio < .235 || override.ratio > .765 {
			t.Fatalf("out of range: %v", override.ratio)
		}
		body := []byte(`{"usage":{"input_tokens":1000000,"output_tokens":20}}`)
		first := override.rewriteJSON(body)
		second := override.rewriteJSON(body)
		if !bytes.Equal(first, second) {
			t.Fatal("cache percentage changed between events")
		}
	}
}

func TestCacheRatioAnthropicStreamPartialUsage(t *testing.T) {
	override := newCacheRatioOverride(cacheRatioChannel(outbound.OutboundTypeAnthropic, 60))
	start := override.rewriteJSON([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":800,"cache_read_input_tokens":100,"cache_creation_input_tokens":100,"output_tokens":0}}}`))
	delta := override.rewriteJSON([]byte(`{"type":"message_delta","usage":{"output_tokens":20}}`))
	if cacheRatioValue(t, start, "message", "usage", "input_tokens") != 300 ||
		cacheRatioValue(t, start, "message", "usage", "cache_read_input_tokens") != 600 ||
		cacheRatioValue(t, delta, "usage", "input_tokens") != 300 ||
		cacheRatioValue(t, delta, "usage", "cache_read_input_tokens") != 600 {
		t.Fatalf("partial usage lost input accounting: start=%s delta=%s", start, delta)
	}
}

func TestCacheRatioSSEFraming(t *testing.T) {
	frame := ": heartbeat\r\nid: 7\r\nevent: response.completed\r\nretry: 1000\r\ndata: {\"response\": {\r\ndata: \"usage\":{\"input_tokens\":1000}}}\r\n\r\n"
	source := withCacheRatio(stream.NewFramedSSESource(io.NopCloser(strings.NewReader(frame)), 4096), cacheRatioChannel(outbound.OutboundTypeOpenAIResponse, 50), true)
	defer source.Close()
	rewritten, err := source.ReadEvent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rewritten, []byte(": heartbeat\n")) || !bytes.Contains(rewritten, []byte("id: 7\n")) || !bytes.Contains(rewritten, []byte("retry: 1000\n")) {
		t.Fatalf("SSE metadata lost: %s", rewritten)
	}
	count := 0
	for event, err := range sse.Read(bytes.NewReader(rewritten), nil) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		if cacheRatioValue(t, []byte(event.Data), "response", "usage", "input_tokens_details", "cached_tokens") != 500 {
			t.Fatal(event.Data)
		}
	}
	if count != 1 {
		t.Fatalf("unexpected event count: %d", count)
	}
}

func TestCacheRatioNonStreamProtocolsAndMetrics(t *testing.T) {
	for _, upstream := range []struct {
		channelType outbound.OutboundType
		body        string
	}{
		{outbound.OutboundTypeOpenAIChat, `{"id":"chat-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":20,"total_tokens":1020}}`},
		{outbound.OutboundTypeOpenAIResponse, `{"id":"resp-1","object":"response","model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020}}`},
		{outbound.OutboundTypeAnthropic, `{"id":"msg-1","type":"message","model":"claude","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":900,"cache_read_input_tokens":100,"output_tokens":20}}`},
	} {
		for _, downstream := range []struct {
			inType inbound.InboundType
			format model.APIFormat
			path   []string
		}{
			{inbound.InboundTypeOpenAIChat, model.APIFormatOpenAIChatCompletion, []string{"usage", "prompt_tokens_details", "cached_tokens"}},
			{inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, []string{"usage", "input_tokens_details", "cached_tokens"}},
			{inbound.InboundTypeAnthropic, model.APIFormatAnthropicMessage, []string{"usage", "cache_read_input_tokens"}},
		} {
			t.Run(fmt.Sprintf("%d-to-%d", upstream.channelType, downstream.inType), func(t *testing.T) {
				attempt, recorder := newEmptyStreamTestAttempt(t, downstream.inType, downstream.format, upstream.channelType)
				attempt.channel = cacheRatioChannel(upstream.channelType, 60)
				response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstream.body))}
				if err := attempt.handleResponse(context.Background(), response); err != nil {
					t.Fatal(err)
				}
				if got := cacheRatioValue(t, recorder.Body.Bytes(), downstream.path...); got != 600 {
					t.Fatalf("wrong client usage: %s", recorder.Body.String())
				}
				if downstream.inType == inbound.InboundTypeOpenAIChat && cacheRatioValue(t, recorder.Body.Bytes(), "usage", "prompt_tokens") != 1000 {
					t.Fatal("Chat prompt_tokens excludes cache")
				}
				attempt.collectResponse()
				usage := attempt.metrics.InternalResponse.Usage
				if usage.BillableCacheReadInput() != 600 || usage.EffectiveInputTokens() != 1000 || usage.CompletionTokens != 20 {
					t.Fatalf("metrics differ from response: %+v", usage)
				}
			})
		}
	}
}

func TestCacheRatioResponsesStreamAndPassthrough(t *testing.T) {
	completed := `{"type":"response.completed","response":{"id":"resp-1","object":"response","model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020}}}`
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			attempt, recorder := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
			attempt.channel = cacheRatioChannel(outbound.OutboundTypeOpenAIResponse, 60)
			body := "data: " + completed + "\n\n"
			var err error
			if passthrough {
				err = attempt.handleStreamResponsePassthroughV2(context.Background(), sseTestResponse(body), model.PassthroughConfig{CollectMetrics: true})
			} else {
				err = attempt.handleStreamResponseV2(context.Background(), sseTestResponse(body))
				attempt.collectResponse()
			}
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for event, err := range sse.Read(bytes.NewReader(recorder.Body.Bytes()), nil) {
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(event.Data, "response.completed") {
					found = true
					if cacheRatioValue(t, []byte(event.Data), "response", "usage", "input_tokens_details", "cached_tokens") != 600 {
						t.Fatal(event.Data)
					}
				}
			}
			if !found || attempt.metrics.InternalResponse == nil || attempt.metrics.InternalResponse.Usage.BillableCacheReadInput() != 600 {
				t.Fatalf("stream or metrics missing adjusted usage: %s", recorder.Body.String())
			}
		})
	}
}

func TestCacheRatioResponsesNonStreamPassthrough(t *testing.T) {
	attempt, recorder := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
	attempt.channel = cacheRatioChannel(outbound.OutboundTypeOpenAIResponse, 60)
	body := `{"id":"resp-1","object":"response","model":"gpt-4o","status":"completed","output":[{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"extra":{"opaque":true},"usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020}}`
	response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	if err := attempt.handleResponsePassthrough(context.Background(), response, model.PassthroughConfig{CollectMetrics: true}); err != nil {
		t.Fatal(err)
	}
	if cacheRatioValue(t, recorder.Body.Bytes(), "usage", "input_tokens_details", "cached_tokens") != 600 ||
		attempt.metrics.InternalResponse.Usage.BillableCacheReadInput() != 600 ||
		!strings.Contains(recorder.Body.String(), `"opaque":true`) {
		t.Fatalf("passthrough body or metrics incorrect: %s", recorder.Body.String())
	}
}

func TestCacheRatioAnthropicStreamProtocols(t *testing.T) {
	for _, percent := range []float64{0, 60, 100} {
		for _, passthrough := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/%t", percent, passthrough), func(t *testing.T) {
				inType, format := inbound.InboundTypeOpenAIChat, model.APIFormatOpenAIChatCompletion
				if passthrough {
					inType, format = inbound.InboundTypeAnthropic, model.APIFormatAnthropicMessage
				}
				attempt, recorder := newEmptyStreamTestAttempt(t, inType, format, outbound.OutboundTypeAnthropic)
				attempt.channel = cacheRatioChannel(outbound.OutboundTypeAnthropic, percent)
				body := strings.Join([]string{
					`event: message_start`,
					`data: {"type":"message_start","message":{"id":"msg-1","type":"message","model":"claude","role":"assistant","content":[],"usage":{"input_tokens":900,"cache_read_input_tokens":100,"output_tokens":0}}}`, "",
					`event: content_block_start`,
					`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, "",
					`event: content_block_delta`,
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`, "",
					`event: content_block_stop`,
					`data: {"type":"content_block_stop","index":0}`, "",
					`event: message_delta`,
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`, "",
					`event: message_stop`,
					`data: {"type":"message_stop"}`, "", "",
				}, "\n")
				var err error
				if passthrough {
					err = attempt.handleStreamResponsePassthroughV2(context.Background(), sseTestResponse(body), model.PassthroughConfig{CollectMetrics: true})
				} else {
					err = attempt.handleStreamResponseV2(context.Background(), sseTestResponse(body))
					attempt.collectResponse()
				}
				if err != nil {
					t.Fatal(err)
				}
				want := int64(percent * 10)
				if attempt.metrics.InternalResponse == nil {
					t.Fatal("missing metrics")
				}
				usage := attempt.metrics.InternalResponse.Usage
				if usage == nil || usage.EffectiveInputTokens() != 1000 || usage.BillableCacheReadInput() != want || usage.CompletionTokens != 20 {
					t.Fatalf("incorrect stream metrics: %+v", usage)
				}
				var lastUsage map[string]json.RawMessage
				for event, err := range sse.Read(bytes.NewReader(recorder.Body.Bytes()), nil) {
					if err != nil {
						t.Fatal(err)
					}
					payload := cacheUsageObject([]byte(event.Data))
					if current := cacheUsageObject(payload["usage"]); current != nil {
						lastUsage = current
					}
				}
				if lastUsage == nil {
					t.Fatalf("missing downstream usage: %s", recorder.Body.String())
				}
				encoded, _ := json.Marshal(lastUsage)
				if passthrough {
					if cacheRatioValue(t, encoded, "cache_read_input_tokens") != want ||
						cacheRatioValue(t, encoded, "input_tokens") != 1000-want {
						t.Fatalf("wrong Anthropic usage: %s", encoded)
					}
				} else if cacheRatioValue(t, encoded, "prompt_tokens") != 1000 {
					t.Fatalf("wrong Chat usage: %s", encoded)
				}
			})
		}
	}
}

func TestCacheRatioWebSocketUsage(t *testing.T) {
	override := newCacheRatioOverride(cacheRatioChannel(outbound.OutboundTypeOpenAIResponse, 60))
	event := override.rewriteJSON([]byte(`{"type":"response.completed","response":{"id":"resp-1","usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020}}}`))
	stats := &wsPassthroughStats{}
	observeWSPassthroughEvent(stats, event)
	if stats.Usage == nil || stats.Usage.BillableCacheReadInput() != 600 || stats.Usage.EffectiveInputTokens() != 1000 {
		t.Fatalf("incorrect WebSocket metrics: %+v", stats.Usage)
	}
	var compact responsesCompactResponse
	if err := json.Unmarshal(override.rewriteJSON([]byte(`{"usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020}}`)), &compact); err != nil {
		t.Fatal(err)
	}
	if convertCompactUsage(compact.Usage).BillableCacheReadInput() != 600 {
		t.Fatal("compact metrics lost cache reads")
	}
}
