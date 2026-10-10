package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestHandlerForwardsProtocolVendorPayloads(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		path     string
		inbound  inbound.InboundType
		outbound outbound.OutboundType
		request  string
		response string
		fields   []string
	}{
		{
			name: "chat", path: "/v1/chat/completions", inbound: inbound.InboundTypeOpenAIChat, outbound: outbound.OutboundTypeOpenAIChat,
			request:  `"messages":[{"role":"user","content":[{"type":"vendor_document","source":{"id":"doc"}}]}],"tools":[{"type":"custom","custom":{"name":"shell","format":{"type":"text"}}}],"tool_choice":{"type":"custom","custom":{"name":"shell"}},"vendor":{"flag":false}`,
			response: `{"id":"r","object":"chat.completion","created":0,"model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"vendor_done","logprobs":null}],"usage":null,"error":{},"vendor":{"flag":false}}`,
			fields:   []string{"messages", "tools", "tool_choice", "vendor"},
		},
		{
			name: "responses", path: "/v1/responses", inbound: inbound.InboundTypeOpenAIResponse, outbound: outbound.OutboundTypeOpenAIResponse,
			request:  `"input":{"vendor_input":{"ref":"item","counter":9007199254740993}},"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"shell","format":{"type":"text"}}]},{"type":"function","name":"lookup","parameters":true}],"tool_choice":{"type":"vendor_mode","options":[0,false]},"reasoning":{"effort":"turbo","summary":"brief","vendor":{"flag":false}}`,
			response: `{"id":"r","model":"upstream","created_at":"1786360449.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"error":{},"vendor":{"flag":false}}`,
			fields:   []string{"input", "tools", "tool_choice", "reasoning"},
		},
		{
			name: "anthropic", path: "/v1/messages", inbound: inbound.InboundTypeAnthropic, outbound: outbound.OutboundTypeAnthropic,
			request:  `"max_tokens":16,"messages":[{"role":"user","content":[{"type":"document","citations":{"enabled":"auto","future":false}},{"type":"text","text":"hello","citations":{"vendor":false}},{"type":"vendor_item","source":{"ref":"item"}}]}],"vendor":{"flag":false}`,
			response: `{"id":"r","type":"message","model":"upstream","role":"assistant","content":[{"type":"text","text":"ok","citations":{"vendor":false}},{"type":"vendor_result","value":0}],"stop_reason":"end_turn","error":{}}`,
			fields:   []string{"messages", "max_tokens", "vendor"},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx := setupRelayTestDB(t)
			captured := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read request: %v", err)
					writer.WriteHeader(http.StatusInternalServerError)
					return
				}
				captured <- body
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, fixture.response)
			}))
			defer server.Close()
			channel := &model.Channel{Name: fixture.name, Type: fixture.outbound, Enabled: true, BaseUrls: []model.BaseUrl{{URL: server.URL + "/v1"}}, Model: "upstream", Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "test-key"}}}
			if err := op.ChannelCreate(channel, ctx); err != nil {
				t.Fatal(err)
			}
			group := &model.Group{Name: "protocol-vendor-" + fixture.name, Mode: model.GroupModeFailover}
			if err := op.GroupCreate(group, ctx); err != nil {
				t.Fatal(err)
			}
			if err := op.GroupItemAdd(&model.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "upstream", Priority: 1, Weight: 1}, ctx); err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"model":"` + group.Name + `",` + fixture.request + `}`)
			recorder := httptest.NewRecorder()
			requestContext, _ := gin.CreateTestContext(recorder)
			requestContext.Set("api_key_id", 1)
			requestContext.Request = httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(string(body)))
			Handler(fixture.inbound, requestContext)
			if recorder.Code != http.StatusOK {
				t.Fatalf("native request rejected: %d %s", recorder.Code, recorder.Body.String())
			}
			select {
			case received := <-captured:
				if fixture.name == "responses" && !strings.Contains(string(received), `9007199254740993`) {
					t.Fatalf("raw request number lost precision: %s", received)
				}
				var actual, expected map[string]json.RawMessage
				if err := json.Unmarshal(received, &actual); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(body, &expected); err != nil {
					t.Fatal(err)
				}
				for _, field := range fixture.fields {
					assertEquivalentResponseJSON(t, string(actual[field]), string(expected[field]))
				}
				if string(actual["model"]) != `"upstream"` {
					t.Fatalf("routing model rewrite lost: %s", received)
				}
			default:
				t.Fatal("request never reached upstream")
			}
			assertEquivalentResponseJSON(t, recorder.Body.String(), strings.ReplaceAll(fixture.response, `"model":"upstream"`, `"model":"`+group.Name+`"`))
		})
	}
}
