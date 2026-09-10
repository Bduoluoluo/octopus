package helper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestBuildTestRequestProtocols(t *testing.T) {
	tests := []struct {
		name        string
		channelType outbound.OutboundType
		path        string
		header      string
		auth        string
		bodyField   string
	}{
		{"chat", outbound.OutboundTypeOpenAIChat, "/v1/responses", "Authorization", "Bearer test-key", "input"},
		{"responses", outbound.OutboundTypeOpenAIResponse, "/v1/responses", "Authorization", "Bearer test-key", "input"},
		{"anthropic", outbound.OutboundTypeAnthropic, "/v1/messages", "X-Api-Key", "test-key", "messages"},
		{"gemini", outbound.OutboundTypeGemini, "/v1beta/models/test-model:generateContent", "X-Goog-Api-Key", "test-key", "contents"},
		{"volcengine", outbound.OutboundTypeVolcengine, "/v3/chat/completions", "Authorization", "Bearer test-key", "messages"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := buildTestRequest(context.Background(), test.channelType, "https://example.com", "test-key", "test-model")
			if err != nil {
				t.Fatal(err)
			}
			defer request.Body.Close()
			if request.Method != http.MethodPost || request.URL.Path != test.path {
				t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
			}
			if request.Header.Get(test.header) != test.auth || request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("unexpected headers: %v", request.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if _, ok := body[test.bodyField]; !ok {
				t.Fatalf("missing %s in %v", test.bodyField, body)
			}
		})
	}
	for _, channelType := range []outbound.OutboundType{outbound.OutboundTypeOpenAIEmbedding, 99} {
		if _, err := buildTestRequest(context.Background(), channelType, "https://example.com", "key", "model"); err == nil {
			t.Fatalf("expected unsupported type error for %d", channelType)
		}
	}
}

func TestTestChannelModelHeadersAndError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer override" || request.Header.Get("X-Test") != "custom" {
			t.Errorf("custom headers not applied: %v", request.Header)
		}
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"invalid test key"}}`))
	}))
	defer server.Close()
	channel := &model.Channel{
		Type:     outbound.OutboundTypeOpenAIChat,
		BaseUrls: []model.BaseUrl{{URL: server.URL + "/v1"}},
		Keys:     []model.ChannelKey{{ChannelKey: "test-key"}},
		CustomHeader: []model.CustomHeader{
			{HeaderKey: "Authorization", HeaderValue: "Bearer override"},
			{HeaderKey: "X-Test", HeaderValue: "custom"},
			{HeaderKey: " "},
		},
	}
	result, err := TestChannelModel(context.Background(), channel, "test-model", 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusUnauthorized || !strings.Contains(result.Error, "invalid test key") {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := TestChannelModel(context.Background(), channel, "test-model", 1, time.Second); err == nil {
		t.Fatal("expected invalid key index error")
	}
}

func TestTestChannelModelTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	channel := &model.Channel{
		Type:     outbound.OutboundTypeOpenAIChat,
		BaseUrls: []model.BaseUrl{{URL: server.URL}},
		Keys:     []model.ChannelKey{{ChannelKey: "test-key"}},
	}
	result, err := TestChannelModel(context.Background(), channel, "test-model", 0, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != 0 || !strings.Contains(result.Error, "deadline exceeded") {
		t.Fatalf("expected timeout, got %+v", result)
	}
}
