package helper

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestModelDiscoveryRejectsRetiredProtocols(t *testing.T) {
	for _, channelType := range []outbound.OutboundType{3, 4, 5, 99} {
		if _, err := FetchModels(t.Context(), model.Channel{Type: channelType}); !errors.Is(err, model.ErrUnsupportedChannelType) {
			t.Fatalf("unsupported discovery %d: %v", channelType, err)
		}
	}
}

func TestModelDiscoveryKeepsVendorNames(t *testing.T) {
	models := []string{"gemini-compatible", "doubao-compatible", "deepseek-chat", "text-embedding-3-large"}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		items := make([]map[string]string, 0, len(models))
		for _, name := range models {
			items = append(items, map[string]string{"id": name})
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": items})
	}))
	defer server.Close()
	got, err := FetchModels(t.Context(), model.Channel{Type: outbound.OutboundTypeOpenAIChat, BaseUrls: []model.BaseUrl{{URL: server.URL}}})
	if err != nil || !reflect.DeepEqual(got, models) {
		t.Fatalf("vendor model list changed: %v %v", got, err)
	}
}
