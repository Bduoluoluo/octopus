package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestHandlerFailoverAfterTimeoutsAndUpstreamError(t *testing.T) {
	for _, convert := range []bool{false, true} {
		t.Run(fmt.Sprint(convert), func(t *testing.T) {
			ctx := setupRelayTestDB(t)
			group := &dbmodel.Group{Name: "timeout-error-fallback", Mode: dbmodel.GroupModeFailover, FirstTokenTimeOut: 1}
			if err := op.GroupCreate(group, ctx); err != nil {
				t.Fatal(err)
			}
			var hits [4]atomic.Int32
			for index := range hits {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					hits[index].Add(1)
					writer.Header().Set("Content-Type", "text/event-stream")
					if index < 2 {
						writer.(http.Flusher).Flush()
						select {
						case <-request.Context().Done():
						case <-time.After(6 * time.Second):
						}
						return
					}
					_, _ = io.WriteString(writer, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[]}}\n\n")
					if index == 2 {
						_, _ = io.WriteString(writer, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" \\n\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":999}}\n\n")
						writer.(http.Flusher).Flush()
						_, _ = io.WriteString(writer, "event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":\"upstream_error\",\"message\":\"Upstream request failed\"}}\n\n")
						return
					}
					_, _ = io.WriteString(writer, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fallback-ok\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
				}))
				t.Cleanup(server.Close)
				channel := &dbmodel.Channel{Name: fmt.Sprintf("timeout-fallback-%d", index), Type: outbound.OutboundTypeAnthropic, Enabled: true, BaseUrls: []dbmodel.BaseUrl{{URL: server.URL}}, Keys: []dbmodel.ChannelKey{{Enabled: true, ChannelKey: "test-key"}}, Model: "test-model"}
				if err := op.ChannelCreate(channel, ctx); err != nil {
					t.Fatal(err)
				}
				if err := op.GroupItemAdd(&dbmodel.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "test-model", Priority: index + 1, Weight: 1}, ctx); err != nil {
					t.Fatal(err)
				}
			}
			recorder := httptest.NewRecorder()
			client, _ := gin.CreateTestContext(recorder)
			requestCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			client.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"timeout-error-fallback","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)).WithContext(requestCtx)
			inType := inbound.InboundTypeAnthropic
			if convert {
				inType = inbound.InboundTypeOpenAIChat
			}
			Handler(inType, client)
			for index := range hits {
				if hits[index].Load() != 1 {
					t.Fatalf("channel %d hits=%d, body=%s", index, hits[index].Load(), recorder.Body.String())
				}
			}
			if requestCtx.Err() != nil || !strings.Contains(recorder.Body.String(), "fallback-ok") || strings.Contains(recorder.Body.String(), "999") || strings.Contains(recorder.Body.String(), "upstream_error") {
				t.Fatalf("failed to recover after timeouts: %s", recorder.Body.String())
			}
			if stats := op.StatsAPIKeyGet(0); stats.RequestSuccess != 1 || stats.OutputToken != 2 {
				t.Fatalf("unexpected billing after failover: %+v", stats)
			}
		})
	}
}

func TestUpstreamFailoverReason(t *testing.T) {
	for _, test := range []struct {
		written   bool
		canceled  bool
		remaining int
		want      string
	}{
		{true, false, 3, "blocked_response_started"},
		{false, true, 3, "blocked_client_canceled"},
		{false, false, 0, "candidates_exhausted"},
		{false, false, 3, "next_candidate_available"},
	} {
		if got := upstreamFailoverReason(test.written, test.canceled, test.remaining); got != test.want {
			t.Errorf("got %q, want %q", got, test.want)
		}
	}
}
