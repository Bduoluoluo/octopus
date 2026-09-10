package helper

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

func TestResponsesProbeRequest(t *testing.T) {
	for _, channelType := range []outbound.OutboundType{outbound.OutboundTypeOpenAIChat, outbound.OutboundTypeOpenAIResponse} {
		request, err := buildTestRequest(context.Background(), channelType, "https://example.com/v1/", "test-key", "selected-model")
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Model        string `json:"model"`
			Stream       bool   `json:"stream"`
			Instructions string `json:"instructions"`
			Input        []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		request.Body.Close()
		if request.URL.Path != "/v1/responses" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected request: %v", request)
		}
		if body.Model != "selected-model" || !body.Stream || len(body.Instructions) < 6000 || !strings.HasPrefix(body.Instructions, "You are Codex, based on GPT-5.") {
			t.Fatal("missing Responses model, stream or instructions")
		}
		if len(body.Input) != 1 || body.Input[0].Role != "user" || len(body.Input[0].Content) != 1 || body.Input[0].Content[0].Type != "input_text" || body.Input[0].Content[0].Text != "hi" {
			t.Fatalf("unexpected input: %+v", body.Input)
		}
	}
}

func TestValidateResponsesProbe(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		status      int
		wantError   string
	}{
		{"completed", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\ndata: {\"type\":\"response.completed\"}\n\n", "text/event-stream", 200, ""},
		{"completed output", "event: response.completed\ndata: {\"response\":{\"status\":\"completed\",\"output\":[{\"content\":[{\"text\":\"Hello\"}]}]}}\n\n", "text/event-stream", 200, ""},
		{"done output", "data: {\"type\":\"response.done\",\"response\":{\"status\":\"completed\",\"output\":[{\"content\":[{\"text\":\"Hello\"}]}]}}\n\n", "text/event-stream", 200, ""},
		{"done event", "event: response.done\ndata: {\"response\":{\"output\":[{\"content\":[{\"text\":\"Hello\"}]}]}}\n\n", "text/event-stream", 200, ""},
		{"done failed", "data: {\"type\":\"response.done\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"Bad Gateway\"}}}\n\n", "text/event-stream", 200, "Bad Gateway"},
		{"done empty", "data: {\"type\":\"response.done\",\"response\":{\"output\":[]}}\n\n", "text/event-stream", 200, "without output"},
		{"nested failure", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"rate limited\"}}}\n\n", "text/event-stream", 200, "rate limited"},
		{"error event", "event: error\ndata: {\"message\":\"Bad Gateway\"}\n\n", "text/event-stream", 200, "Bad Gateway"},
		{"failure after output", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\ndata: {\"type\":\"response.failed\"}\n\n", "text/event-stream", 200, "response.failed"},
		{"empty completed", "data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n", "text/event-stream", 200, "without output"},
		{"truncated", "data: {\"type\":\"response.created\"}\n\n", "text/event-stream", 200, "before response.completed"},
		{"done only", "data: [DONE]\n\n", "text/event-stream", 200, "before response.completed"},
		{"incomplete", "data: {\"type\":\"response.incomplete\"}\n\n", "text/event-stream", 200, "incomplete"},
		{"invalid event", "data: invalid\n\n", "text/event-stream", 200, "invalid Responses event"},
		{"json error", `{"error":{"message":"upstream_error"}}`, "application/json", 200, "upstream_error"},
		{"json success", `{"status":"completed","output":[{"content":[{"text":"Hello"}]}]}`, "application/json", 200, ""},
		{"json empty", `{"status":"completed","output":[]}`, "application/json", 200, "with output"},
		{"http error", `{"error":{"message":"rate limited"}}`, "application/json", 429, "rate limited"},
		{"html", "<html>Bad Gateway</html>", "text/html", 200, "invalid Responses body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {test.contentType}}, Body: io.NopCloser(strings.NewReader(test.body))}
			defer response.Body.Close()
			err := ValidateResponsesProbe(response)
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("expected %q, got %v", test.wantError, err)
			}
		})
	}
}

func TestChannelResponsesProbeStream(t *testing.T) {
	for _, mode := range []string{"success", "error", "timeout", "timeout_after_output", "done_keepalive"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
				case "timeout_after_output", "done_keepalive":
					_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
					if mode == "done_keepalive" {
						_, _ = io.WriteString(writer, "data: {\"type\":\"response.done\",\"response\":{\"status\":\"completed\"}}\n\n")
					}
					writer.(http.Flusher).Flush()
					<-request.Context().Done()
				}
			}))
			defer server.Close()
			channel := &model.Channel{Type: outbound.OutboundTypeOpenAIResponse, BaseUrls: []model.BaseUrl{{URL: server.URL}}, Keys: []model.ChannelKey{{ChannelKey: "test-key"}}}
			timeout := time.Second
			if mode == "timeout" || mode == "timeout_after_output" {
				timeout = 100 * time.Millisecond
			}
			result, err := TestChannelModel(context.Background(), channel, "selected-model", 0, timeout)
			if err != nil {
				t.Fatal(err)
			}
			if result.StatusCode != 200 {
				t.Fatalf("unexpected status: %+v", result)
			}
			if (mode == "success" || mode == "done_keepalive") && result.Error != "" {
				t.Fatal(result.Error)
			}
			if mode == "error" && !strings.Contains(result.Error, "Bad Gateway") {
				t.Fatalf("missing stream error: %+v", result)
			}
			if mode == "timeout" && !strings.Contains(result.Error, "before receiving output") {
				t.Fatalf("missing timeout: %+v", result)
			}
			if mode == "timeout_after_output" && !strings.Contains(result.Error, "after receiving output") {
				t.Fatalf("missing timeout stage: %+v", result)
			}
		})
	}
}
