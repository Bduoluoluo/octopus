package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestPromptSuffixIsolatedAcrossRetryRounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name        string
		channelType outbound.OutboundType
		inboundType inbound.InboundType
		request     string
		response    string
	}{
		{"chat", outbound.OutboundTypeOpenAIChat, inbound.InboundTypeOpenAIChat,
			`{"model":"suffix-rounds","messages":[{"role":"user","content":"hello"}]}`,
			`{"id":"reply","model":"upstream","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`},
		{"responses passthrough", outbound.OutboundTypeOpenAIResponse, inbound.InboundTypeOpenAIResponse,
			`{"model":"suffix-rounds","input":"hello"}`,
			`{"id":"reply","model":"upstream","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`},
		{"anthropic passthrough", outbound.OutboundTypeAnthropic, inbound.InboundTypeAnthropic,
			`{"model":"suffix-rounds","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`,
			`{"id":"reply","type":"message","model":"upstream","role":"assistant","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := setupRelayTestDB(t)
			captured := make(chan []byte, 3)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Error(err)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				captured <- body
				writer.Header().Set("Content-Type", "application/json")
				if calls.Add(1) < 3 {
					writer.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(writer, `{"error":{"message":"try another channel"}}`)
					return
				}
				_, _ = io.WriteString(writer, test.response)
			}))
			defer server.Close()
			group := &model.Group{Name: "suffix-rounds", Mode: model.GroupModeFailover, RetryEnabled: true, MaxRetries: 2}
			if err := op.GroupCreate(group, ctx); err != nil {
				t.Fatal(err)
			}
			for index, suffix := range []string{"-A", "-B"} {
				channel := &model.Channel{Name: fmt.Sprintf("suffix-%d", index), Type: test.channelType,
					Enabled: true, BaseUrls: []model.BaseUrl{{URL: server.URL}}, PromptSuffix: &suffix,
					Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "test"}}}
				if err := op.ChannelCreate(channel, ctx); err != nil {
					t.Fatal(err)
				}
				if err := op.GroupItemAdd(&model.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "upstream", Priority: index + 1, Weight: 1}, ctx); err != nil {
					t.Fatal(err)
				}
			}
			subscriber := op.RelayLogSubscribe()
			defer op.RelayLogUnsubscribe(subscriber)
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			requestContext, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(test.request)).WithContext(requestContext)
			Handler(test.inboundType, ginContext)
			if recorder.Code != http.StatusOK || calls.Load() != 3 {
				t.Fatalf("expected A, B, A retry sequence; calls=%d response=%d %s", calls.Load(), recorder.Code, recorder.Body.String())
			}
			for _, expected := range []string{"hello-A", "hello-B", "hello-A"} {
				body := <-captured
				var payload struct {
					Input    string `json:"input"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatal(err)
				}
				actual := payload.Input
				if len(payload.Messages) == 1 {
					actual = payload.Messages[0].Content
				}
				if actual != expected {
					t.Fatalf("suffix accumulated or crossed channels: got %q want %q", actual, expected)
				}
			}
			select {
			case entry := <-subscriber:
				if entry.RequestContent != test.request || !entry.Success {
					t.Fatalf("original request log changed: %+v", entry)
				}
			case <-time.After(time.Second):
				t.Fatal("missing request log")
			}
		})
	}
}
