package grouphealth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestBuildProbeRequestForResponses(t *testing.T) {
	channel := &model.Channel{
		Type:     outbound.OutboundTypeOpenAIResponse,
		BaseUrls: []model.BaseUrl{{URL: "https://example.com/v1"}},
	}
	usedKey := &model.ChannelKey{ID: 1, ChannelKey: "sk-test"}

	req, err := buildProbeRequest(context.Background(), channel, usedKey, "gpt-5.4")
	if err != nil {
		t.Fatalf("buildProbeRequest returned error: %v", err)
	}
	if req.URL.Path != "/v1/responses" {
		t.Fatalf("expected /v1/responses, got %s", req.URL.Path)
	}
}

func TestRunCandidateResponsesStream(t *testing.T) {
	for _, mode := range []string{"success", "error", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if request.URL.Path != "/v1/responses" || body["stream"] != true || body["model"] != "selected-model" || body["instructions"] == nil || body["input"] == nil {
					t.Error("group health did not send the Responses probe")
				}
				if request.Header.Get("X-Probe-Test") != "custom" {
					t.Error("missing custom header")
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, "data: {\"type\":\"response.created\"}\n\n")
				writer.(http.Flusher).Flush()
				switch mode {
				case "success":
					_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"content\":[{\"text\":\"hi\"}]}]}}\n\n")
				case "error":
					_, _ = io.WriteString(writer, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Bad Gateway\"}}}\n\n")
				case "timeout":
					<-request.Context().Done()
				}
			}))
			defer server.Close()
			channel := model.Channel{Type: outbound.OutboundTypeOpenAIChat, BaseUrls: []model.BaseUrl{{URL: server.URL}}, CustomHeader: []model.CustomHeader{{HeaderKey: "X-Probe-Test", HeaderValue: "custom"}}}
			prober := NewProber()
			if prober.CandidateTimeout != 10*time.Second {
				t.Fatal("expected 10 second default")
			}
			if mode == "timeout" {
				prober.CandidateTimeout = 100 * time.Millisecond
			}
			result := prober.RunCandidate(context.Background(), channel, model.ChannelKey{ChannelKey: "test-key"}, "selected-model")
			if result.HTTPStatus != 200 || result.Success != (mode == "success") {
				t.Fatalf("unexpected result: %+v", result)
			}
			if mode == "success" && result.Output != "hi" {
				t.Fatalf("missing response output: %+v", result)
			}
			if !result.Success && result.Output != "" {
				t.Fatalf("failed probe returned success output: %+v", result)
			}
			if mode == "error" && !strings.Contains(result.ErrorMessage, "Bad Gateway") {
				t.Fatalf("missing error: %+v", result)
			}
			if mode == "timeout" && !strings.Contains(result.ErrorMessage, "deadline exceeded") {
				t.Fatalf("missing timeout: %+v", result)
			}
		})
	}
}

func TestBuildProbeRequestForEmbeddings(t *testing.T) {
	channel := &model.Channel{
		Type:     outbound.OutboundTypeOpenAIEmbedding,
		BaseUrls: []model.BaseUrl{{URL: "https://example.com/v1"}},
	}
	usedKey := &model.ChannelKey{ID: 1, ChannelKey: "sk-test"}

	req, err := buildProbeRequest(context.Background(), channel, usedKey, "text-embedding-3-large")
	if err != nil {
		t.Fatalf("buildProbeRequest returned error: %v", err)
	}
	if req.URL.Path != "/v1/embeddings" {
		t.Fatalf("expected /v1/embeddings, got %s", req.URL.Path)
	}
}
