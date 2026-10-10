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

func TestHandlerForwardsResponsesNamespaceCustomTools(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx := setupRelayTestDB(t)
			captured := make(chan []byte, 1)
			upstreamError := "custom tool rejected by upstream"
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read upstream request: %v", err)
					writer.WriteHeader(http.StatusInternalServerError)
					return
				}
				captured <- body
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = io.WriteString(writer, `{"id":"resp_tools","object":"response","created_at":1,"model":"upstream","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`)
				} else {
					_, _ = io.WriteString(writer, `{"error":{"message":"`+upstreamError+`","type":"invalid_request_error"}}`)
				}
			}))
			defer server.Close()

			channel := &model.Channel{
				Name:     "responses-namespace-custom",
				Type:     outbound.OutboundTypeOpenAIResponse,
				Enabled:  true,
				BaseUrls: []model.BaseUrl{{URL: server.URL + "/v1"}},
				Model:    "upstream",
				Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "test-key"}},
			}
			if err := op.ChannelCreate(channel, ctx); err != nil {
				t.Fatal(err)
			}
			group := &model.Group{Name: "responses-namespace-custom-group", Mode: model.GroupModeFailover}
			if err := op.GroupCreate(group, ctx); err != nil {
				t.Fatal(err)
			}
			if err := op.GroupItemAdd(&model.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "upstream", Priority: 1, Weight: 1}, ctx); err != nil {
				t.Fatal(err)
			}

			tools := `[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: WORD"},"future":{"enabled":false,"count":0}}]}]`
			body := `{"model":"` + group.Name + `","input":"hello","tools":` + tools + `}`
			recorder := httptest.NewRecorder()
			requestContext, _ := gin.CreateTestContext(recorder)
			requestContext.Set("api_key_id", 7)
			requestContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			requestContext.Request.Header.Set("Content-Type", "application/json")
			logs := op.RelayLogSubscribe()
			defer op.RelayLogUnsubscribe(logs)
			Handler(inbound.InboundTypeOpenAIResponse, requestContext)

			select {
			case received := <-captured:
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(received, &payload); err != nil {
					t.Fatal(err)
				}
				assertEquivalentResponseJSON(t, string(payload["tools"]), tools)
				if string(payload["model"]) != `"upstream"` {
					t.Fatalf("upstream model was not selected: %s", received)
				}
			default:
				t.Fatalf("request never reached upstream: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if recorder.Code != status {
				t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
			}
			select {
			case entry := <-logs:
				if len(entry.Attempts) != 1 || entry.Attempts[0].ChannelID != channel.ID {
					t.Fatalf("upstream attempt was not logged: %+v", entry)
				}
				if status == http.StatusBadRequest && (entry.Attempts[0].Status != model.AttemptFailed || !strings.Contains(entry.Attempts[0].Msg, upstreamError)) {
					t.Fatalf("upstream rejection was not logged: %+v", entry)
				}
				if status == http.StatusOK && entry.Attempts[0].Status != model.AttemptSuccess {
					t.Fatalf("upstream success was not logged: %+v", entry)
				}
			default:
				t.Fatal("forwarded request did not produce a relay log")
			}
		})
	}
}
