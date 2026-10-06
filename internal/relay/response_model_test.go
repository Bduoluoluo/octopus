package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tmaxmax/go-sse"
	dbmodel "github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/relay/balancer"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func assertEquivalentResponseJSON(t *testing.T, actual, expected string) {
	t.Helper()
	decode := func(data string) any {
		decoder := json.NewDecoder(strings.NewReader(data))
		decoder.UseNumber()
		var payload any
		if err := decoder.Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	if !reflect.DeepEqual(decode(actual), decode(expected)) {
		t.Fatalf("response differs: got %s want %s", actual, expected)
	}
}

func assertEquivalentResponseSSE(t *testing.T, actual, expected string) {
	t.Helper()
	parse := func(data string) []string {
		var events []string
		for event, err := range sse.Read(strings.NewReader(data), nil) {
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, event.Type, event.Data)
		}
		return events
	}
	actualEvents, expectedEvents := parse(actual), parse(expected)
	if len(actualEvents) != len(expectedEvents) {
		t.Fatalf("event count changed: got %d want %d", len(actualEvents)/2, len(expectedEvents)/2)
	}
	for index := 0; index < len(actualEvents); index += 2 {
		if actualEvents[index] != expectedEvents[index] {
			t.Fatalf("event type changed: got %s want %s", actualEvents[index], expectedEvents[index])
		}
		assertEquivalentResponseJSON(t, actualEvents[index+1], expectedEvents[index+1])
	}
}

func TestResponseModelJSONOnlyRewritesProtocolFields(t *testing.T) {
	for _, body := range []string{
		`{"model":"returned","choices":[{"message":{"content":"returned","model":"tool-data"}}],"metadata":{"model":"returned","integer":9007199254740993}}`,
		`{"type":"response.completed","response":{"model":"returned","output":[{"type":"function_call","arguments":"{\"model\":\"returned\"}"}]},"metadata":{"model":"returned","integer":9007199254740993}}`,
		`{"type":"message_start","message":{"model":"returned","content":[{"type":"tool_use","input":{"model":"returned"}}]},"metadata":{"model":"returned","integer":9007199254740993}}`,
		`{"modelVersion":"returned","candidates":[{"content":{"parts":[{"text":"returned"}]}}],"metadata":{"model":"returned","integer":9007199254740993}}`,
	} {
		trace := responseModelTrace{UpstreamRequestModel: "requested"}
		result := mapResponseModelJSON([]byte(body), "public-model", trace.observe)
		if !trace.ModelMismatch || trace.UpstreamResponseModel != "returned" {
			t.Fatalf("mismatch not recorded: %+v", trace)
		}
		var original, changed map[string]json.RawMessage
		_ = json.Unmarshal([]byte(body), &original)
		_ = json.Unmarshal(result, &changed)
		if !bytes.Equal(original["metadata"], changed["metadata"]) {
			t.Fatalf("metadata changed: %s", result)
		}
		found := false
		mapResponseModelJSON(result, "", func(name string) {
			found = true
			if name != "public-model" {
				t.Fatalf("wrong downstream model: %s", result)
			}
		})
		if !found {
			t.Fatal("missing downstream model")
		}
	}
	for _, body := range []string{`{"model":"requested"}`, `{"model":""}`, `{"model":null}`, `{"usage":{}}`, `[DONE]`, `invalid`} {
		trace := responseModelTrace{UpstreamRequestModel: "requested"}
		mapResponseModelJSON([]byte(body), "public-model", trace.observe)
		if trace.ModelMismatch {
			t.Fatalf("alias/missing model incorrectly marked: %s", body)
		}
	}
}

func TestResponseModelSSEPreservesFramesAndData(t *testing.T) {
	input := ": ping\r\nid: 7\r\nevent: response.created\r\nretry: 1000\r\ndata: {\"type\":\"response.created\",\r\ndata: \"response\":{\"model\":\"returned\"}}\r\n\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"returned\"}\r\n\r\ndata: [DONE]\r\n\r\n"
	trace := responseModelTrace{UpstreamRequestModel: "requested"}
	result := mapResponseModelSSE([]byte(input), "public-model", trace.observe)
	for _, expected := range []string{": ping\n", "id: 7\n", "retry: 1000\n", `"delta":"returned"`, `data: [DONE]`} {
		if !bytes.Contains(result, []byte(expected)) {
			t.Fatalf("lost SSE field %s: %s", expected, result)
		}
	}
	count := 0
	for _, err := range sse.Read(bytes.NewReader(result), nil) {
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 3 || !trace.ModelMismatch {
		t.Fatalf("invalid SSE events: %d %+v", count, trace)
	}
}

func TestResponseModelNonStreamConversionAndPassthrough(t *testing.T) {
	for _, upstream := range []struct {
		kind outbound.OutboundType
		body string
	}{
		{outbound.OutboundTypeOpenAIChat, `{"id":"chat-1","model":"returned","choices":[{"index":0,"message":{"role":"assistant","content":"returned"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`},
		{outbound.OutboundTypeOpenAIResponse, `{"id":"resp-1","object":"response","model":"returned","status":"completed","output":[{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"returned"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`},
		{outbound.OutboundTypeAnthropic, `{"id":"msg-1","type":"message","model":"returned","role":"assistant","content":[{"type":"text","text":"returned"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`},
	} {
		for _, downstream := range []struct {
			kind   inbound.InboundType
			format model.APIFormat
		}{
			{inbound.InboundTypeOpenAIChat, model.APIFormatOpenAIChatCompletion},
			{inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse},
			{inbound.InboundTypeAnthropic, model.APIFormatAnthropicMessage},
		} {
			for _, passthrough := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%d/%t", upstream.kind, downstream.kind, passthrough), func(t *testing.T) {
					attempt, recorder := newEmptyStreamTestAttempt(t, downstream.kind, downstream.format, upstream.kind)
					attempt.requestModel = "public-model"
					response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstream.body))}
					var err error
					if passthrough {
						err = attempt.handleResponsePassthrough(t.Context(), response, model.PassthroughConfig{CollectMetrics: true})
					} else {
						err = attempt.handleResponse(t.Context(), response)
						attempt.collectResponse()
					}
					if err != nil {
						t.Fatal(err)
					}
					var payload struct {
						Model string `json:"model"`
					}
					if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || payload.Model != "public-model" {
						t.Fatalf("downstream model not rewritten: %s %v", recorder.Body.String(), err)
					}
					if !attempt.metrics.ModelMismatch || attempt.metrics.UpstreamResponseModel != "returned" || attempt.metrics.ActualModel != "returned" || attempt.metrics.Stats.OutputToken != 2 {
						t.Fatalf("original model or usage lost: %+v", attempt.metrics)
					}
				})
			}
		}
	}
}

func TestResponseModelStreamingAndLog(t *testing.T) {
	ctx := setupRelayTestDB(t)
	for _, passthrough := range []bool{false, true} {
		for _, requestModel := range []string{"gpt-4o", "public-model"} {
			attempt, recorder := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
			attempt.requestModel, attempt.metrics.RequestModel = requestModel, requestModel
			body := "data: " + `{"type":"response.created","response":{"id":"resp-1","model":"returned","status":"in_progress","output":[]}}` + "\n\n" +
				"data: " + `{"type":"response.completed","response":{"id":"resp-1","object":"response","model":"returned","status":"completed","output":[{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}` + "\n\n"
			var err error
			if passthrough {
				err = attempt.handleStreamResponsePassthroughV2(ctx, sseTestResponse(body), model.PassthroughConfig{CollectMetrics: true})
			} else {
				err = attempt.handleStreamResponseV2(ctx, sseTestResponse(body))
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
				mapResponseModelJSON([]byte(event.Data), "", func(name string) {
					found = true
					if name != requestModel {
						t.Fatalf("wrong streamed model: %s", event.Data)
					}
				})
			}
			if !found || !attempt.metrics.ModelMismatch || attempt.metrics.ActualModel != "returned" {
				t.Fatalf("lost upstream model: %+v", attempt.metrics)
			}
			subscriber := op.RelayLogSubscribe()
			attempt.metrics.saveLog(ctx, true, nil, time.Second, nil, 1, "test")
			select {
			case entry := <-subscriber:
				if !entry.ModelMismatch || entry.UpstreamRequestModel != "gpt-4o" || entry.UpstreamResponseModel != "returned" || !entry.Success || entry.Error != "" {
					t.Fatalf("invalid mismatch log: %+v", entry)
				}
			case <-time.After(time.Second):
				t.Fatal("no log notification")
			}
			op.RelayLogUnsubscribe(subscriber)
		}
	}
}

func TestResponseModelTracksFinalRequestOverride(t *testing.T) {
	metrics := NewRelayMetrics(1, "public-model", nil, nil)
	metrics.SetTransportRequestPayload([]byte(`{"model":"override-model","input":"hi"}`), "route-model")
	mapResponseModelJSON([]byte(`{"model":"override-model"}`), "public-model", metrics.observe)
	if metrics.ModelMismatch || metrics.UpstreamRequestModel != "override-model" {
		t.Fatalf("compared against route alias: %+v", metrics)
	}
	metrics.observe("unexpected")
	metrics.observe("override-model")
	if !metrics.ModelMismatch || metrics.UpstreamResponseModel != "unexpected" {
		t.Fatal("later matching frame erased mismatch")
	}
}

func TestResponseModelImagesProxy(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		ginContext, _ := gin.CreateTestContext(recorder)
		ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		metrics := newImagesRelayMetrics(1, "public-image")
		metrics.UpstreamRequestModel = "upstream-image"
		var err error
		if streaming {
			body := "event: image_generation.completed\ndata: " + `{"type":"image_generation.completed","model":"returned-image","b64_json":"YQ==","usage":{"input_tokens":10,"output_tokens":2}}` + "\n\n"
			_, _, err = proxySSE(t.Context(), ginContext, sseTestResponse(body), 0, metrics, nil)
		} else {
			body := `{"model":"returned-image","data":[{"b64_json":"YQ=="}],"usage":{"input_tokens":10,"output_tokens":2}}`
			_, _, err = proxyNonStream(ginContext, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, metrics)
		}
		if err != nil {
			t.Fatal(err)
		}
		if !metrics.ModelMismatch || metrics.UpstreamResponseModel != "returned-image" || !strings.Contains(recorder.Body.String(), `"model":"public-image"`) || !strings.Contains(recorder.Body.String(), `"b64_json":"YQ=="`) {
			t.Fatalf("image proxy lost model/data: %s %+v", recorder.Body.String(), metrics)
		}
	}
}

func TestResponseModelMismatchResetsAfterFailover(t *testing.T) {
	ctx := setupRelayTestDB(t)
	group := &dbmodel.Group{Name: "model-check-failover", Mode: dbmodel.GroupModeFailover}
	if err := op.GroupCreate(group, ctx); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			if index == 0 {
				_, _ = io.WriteString(writer, `{"model":"wrong-model","choices":[]}`)
				return
			}
			_, _ = io.WriteString(writer, `{"id":"ok","model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}))
		t.Cleanup(server.Close)
		channel := &dbmodel.Channel{Name: fmt.Sprintf("model-check-%d", index), Type: outbound.OutboundTypeOpenAIChat, Enabled: true, BaseUrls: []dbmodel.BaseUrl{{URL: server.URL}}, Keys: []dbmodel.ChannelKey{{Enabled: true, ChannelKey: "test"}}}
		if err := op.ChannelCreate(channel, ctx); err != nil {
			t.Fatal(err)
		}
		if err := op.GroupItemAdd(&dbmodel.GroupItem{GroupID: group.ID, ChannelID: channel.ID, ModelName: "upstream-model", Priority: index + 1}, ctx); err != nil {
			t.Fatal(err)
		}
	}
	subscriber := op.RelayLogSubscribe()
	defer op.RelayLogUnsubscribe(subscriber)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-check-failover","messages":[{"role":"user","content":"hi"}]}`))
	Handler(inbound.InboundTypeOpenAIChat, ginContext)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"model":"model-check-failover"`) {
		t.Fatal(recorder.Body.String())
	}
	select {
	case entry := <-subscriber:
		if entry.ModelMismatch || entry.UpstreamResponseModel != "upstream-model" || !entry.Success || entry.TotalAttempts != 2 {
			t.Fatalf("failed attempt contaminated final response model: %+v", entry)
		}
	case <-time.After(time.Second):
		t.Fatal("missing log")
	}
}

func TestResponseModelCompactProxy(t *testing.T) {
	setupRelayTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"compact-1","model":"returned-model","output":[],"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	defer server.Close()
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	channel := &dbmodel.Channel{ID: 1, BaseUrls: []dbmodel.BaseUrl{{URL: server.URL}}}
	iter := balancer.NewIterator(dbmodel.Group{Mode: dbmodel.GroupModeFailover, Items: []dbmodel.GroupItem{{ChannelID: 1, ModelName: "public-model"}}}, 1, "public-model")
	iter.Next()
	metrics := NewRelayMetrics(1, "public-model", nil, nil)
	_, _, err := forwardResponsesCompact(ginContext, metrics, iter, channel, dbmodel.ChannelKey{ID: 1, ChannelKey: "test"}, []byte(`{"model":"public-model","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	length := recorder.Header().Get("Content-Length")
	if !metrics.ModelMismatch || !strings.Contains(recorder.Body.String(), `"model":"public-model"`) || (length != "" && length != strconv.Itoa(recorder.Body.Len())) {
		t.Fatalf("invalid compact response: %s %+v", recorder.Body.String(), metrics)
	}
}

func TestResponseModelImageWriterAcrossChunks(t *testing.T) {
	image := strings.Repeat("YQ==", 100000)
	input := `{"data":[{"model":"nested","b64_json":"` + image + `"}],"mo\u0064el" : "upstream", "metadata":{"model":"upstream"},"usage":{"input_tokens":10}}`
	for _, size := range []int{1, 7, 32768} {
		var result bytes.Buffer
		trace := responseModelTrace{UpstreamRequestModel: "requested"}
		writer := &responseModelWriter{destination: &result, model: "public-model", observe: trace.observe}
		for offset := 0; offset < len(input); offset += size {
			if _, err := writer.Write([]byte(input[offset:min(offset+size, len(input))])); err != nil {
				t.Fatal(err)
			}
			if len(writer.pending) > 4096 {
				t.Fatal("buffered large image data")
			}
		}
		if err := writer.finish(); err != nil {
			t.Fatal(err)
		}
		want := strings.Replace(input, `"upstream", "metadata"`, `"public-model", "metadata"`, 1)
		if result.String() != want || !trace.ModelMismatch {
			t.Fatalf("chunk size %d changed image data or missed model", size)
		}
	}
}
