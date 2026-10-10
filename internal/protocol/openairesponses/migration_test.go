package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	wire "github.com/xuanli27/octopus/internal/protocol/openairesponses"
	inbound "github.com/xuanli27/octopus/internal/protocol/openairesponses/inbound"
	outbound "github.com/xuanli27/octopus/internal/protocol/openairesponses/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func decodeObject(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func requireJSON(t *testing.T, actual, expected []byte) {
	t.Helper()
	var left, right any
	first := json.NewDecoder(bytes.NewReader(actual))
	first.UseNumber()
	second := json.NewDecoder(bytes.NewReader(expected))
	second.UseNumber()
	if err := first.Decode(&left); err != nil {
		t.Fatal(err)
	}
	if err := second.Decode(&right); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("got %s; want %s", actual, expected)
	}
}

func TestPinnedRequestNativeOrderAndReplayOverrides(t *testing.T) {
	body := []byte(`{"model":"client","instructions":"guide","previous_response_id":"old","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi","future":false}]},{"type":"tool_search_call","arguments":{"count":9007199254740993},"id":"search"},{"type":"custom_tool_call","id":"custom","call_id":"call_custom","name":"freeform","input":"echo hi"},{"type":"custom_tool_call_output","call_id":"call_custom","output":"hi"},{"type":"reasoning","id":"r","summary":[{"type":"summary_text","text":"reason"}],"encrypted_content":"opaque"},{"type":"item_reference","id":"existing"}],"tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"fn","parameters":{"type":"object","properties":{"count":{"const":9007199254740993}}}}]},{"type":"custom","name":"freeform","format":{"type":"grammar","syntax":"lark","definition":"start: WORD"}},{"type":"file_search","vector_store_ids":["vs"],"max_num_results":0}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"fn"}]},"text":{"format":{"type":"json_schema","name":"answer","strict":false,"schema":{"type":"object"}},"verbosity":"low"},"future":{"value":9007199254740993,"flag":false}}`)
	decoder := &inbound.ResponseInbound{}
	request, err := decoder.TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	request.Model = "selected"
	encoded, err := json.Marshal(outbound.ConvertToResponsesRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	expected := decodeObject(t, body)
	actual := decodeObject(t, encoded)
	for _, key := range []string{"input", "tools", "tool_choice", "text", "future"} {
		requireJSON(t, actual[key], expected[key])
	}
	requireJSON(t, actual["model"], []byte(`"selected"`))
	options := request.GetOpenAIResponsesOptions()
	options.PreviousResponseID = nil
	request.SetOpenAIResponsesOptions(options)
	request.SetOpenAIRawInputItems(json.RawMessage(`[{"type":"message","role":"user","content":"replayed"}]`))
	encoded, err = json.Marshal(outbound.ConvertToResponsesRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	actual = decodeObject(t, encoded)
	if _, present := actual["previous_response_id"]; present {
		t.Fatal("cleared continuation restored")
	}
	requireJSON(t, actual["input"], []byte(`[{"type":"message","role":"user","content":"replayed"}]`))
}

func TestPinnedResponseOpaqueItemsAndUnknownFields(t *testing.T) {
	body := `{"id":"resp","object":"response","model":"upstream","created_at":1786360449.0,"status":"completed","future":{"zero":0},"output":[{"type":"reasoning","id":"r","encrypted_content":"signature","summary":[{"type":"summary_text","text":"thought"}]},{"type":"custom_tool_call","id":"ct","call_id":"custom","name":"shell","input":"echo hi","future":false},{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[{"type":"url_citation","start_index":0,"end_index":5,"url":"https://example.test","title":"source","future":0}]}]},{"type":"image_generation_call","id":"image","result":"YWJj","future":{"keep":true}}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":1}}}`
	response, err := (&outbound.ResponseOutbound{}).TransformResponse(context.Background(), &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
	if err != nil {
		t.Fatal(err)
	}
	response.Model = "client"
	encoded, err := (&inbound.ResponseInbound{}).TransformResponse(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	actual := decodeObject(t, encoded)
	expected := decodeObject(t, []byte(body))
	for _, field := range []string{"output", "future"} {
		requireJSON(t, actual[field], expected[field])
	}
	requireJSON(t, actual["model"], []byte(`"client"`))
	if len(response.Choices[0].Message.ReasoningBlocks) != 1 || response.Choices[0].Message.ReasoningBlocks[0].Signature != "signature" {
		t.Fatal("signature missing from IR")
	}
}

func TestPinnedParallelArgumentsDoneAndFrameLifecycle(t *testing.T) {
	decoder := &outbound.ResponseOutbound{}
	encoder := &inbound.ResponseInbound{}
	fixtures := []model.StreamFrame{
		{Event: "response.created", Data: []byte(`{"response":{"id":"resp","model":"model","status":"in_progress","output":[]}}`)},
		{Data: []byte(`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_a","call_id":"a","name":"one","arguments":""}}`)},
		{Data: []byte(`{"type":"response.output_item.added","output_index":4,"item":{"type":"function_call","id":"fc_b","call_id":"b","name":"two","arguments":""}}`)},
		{Data: []byte(`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_a","delta":"{\"a\":"}`)},
		{Data: []byte(`{"type":"response.function_call_arguments.done","output_index":4,"item_id":"fc_b","arguments":"{}"}`)},
		{Data: []byte(`{"type":"response.function_call_arguments.done","output_index":2,"item_id":"fc_a","arguments":"{\"a\":1}"}`)},
		{Data: []byte(`{"type":"response.function_call_arguments.done","output_index":2,"item_id":"fc_a","arguments":"{ \"a\": 1 }"}`)},
		{Data: []byte(`{"type":"response.completed","response":{"id":"resp","model":"model","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`)},
	}
	arguments := map[string]string{}
	starts := map[string]int{}
	for _, frame := range fixtures {
		events, err := decoder.TransformStreamFrame(context.Background(), frame)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind == model.StreamEventKindToolCallStart {
				starts[event.ToolCall.ID]++
			}
			if event.Kind == model.StreamEventKindToolCallDelta {
				arguments[event.ToolCall.ID] += event.Delta.Arguments
			}
		}
		raw, err := encoder.TransformStreamEvents(context.Background(), events)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte("event: response.")) {
			t.Fatalf("lost SSE event boundary: %s", raw)
		}
	}
	if arguments["a"] != `{"a":1}` || arguments["b"] != "{}" || starts["a"] != 1 || starts["b"] != 1 {
		t.Fatalf("bad argument lifecycle: %v %v", arguments, starts)
	}
	events, err := decoder.EndStream(context.Background())
	if err != nil || len(events) != 1 || events[0].Kind != model.StreamEventKindDone {
		t.Fatalf("terminal EOF: %v %v", events, err)
	}
	response, err := encoder.GetInternalResponse(context.Background())
	if err != nil || response.Usage == nil || response.Usage.TotalTokens != 5 {
		t.Fatalf("usage missing: %v %v", response, err)
	}
	decoder.ResetStream()
	if _, err := decoder.TransformStreamFrame(context.Background(), model.StreamFrame{Data: []byte("[DONE]")}); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.EndStream(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("bare DONE accepted: %v", err)
	}
	if err := decoder.CloseStream(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.TransformStreamFrame(context.Background(), fixtures[0]); err == nil {
		t.Fatal("closed machine reused")
	}
}

func TestPinnedReasoningSignatureAndTruncatedEOF(t *testing.T) {
	decoder := &outbound.ResponseOutbound{}
	var signatures []string
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"reason","type":"reasoning","encrypted_content":"provisional","summary":[]}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"reason","type":"reasoning","encrypted_content":"final","summary":[]}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"id":"reason","type":"reasoning","encrypted_content":"final","summary":[]}]}}`,
	} {
		events, err := decoder.TransformStreamEvent(context.Background(), []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind == model.StreamEventKindSignatureDelta {
				signatures = append(signatures, event.Delta.Signature)
			}
		}
	}
	if !reflect.DeepEqual(signatures, []string{"final"}) {
		t.Fatalf("signatures %v", signatures)
	}
	incomplete := &outbound.ResponseOutbound{}
	if _, err := incomplete.TransformStreamEvent(context.Background(), []byte(`{"type":"response.output_text.delta","delta":"partial"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := incomplete.EndStream(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("missing EOF error: %v", err)
	}
}

func TestPinnedTimestampAndErrorUnion(t *testing.T) {
	for _, timestamp := range []string{"1786360449", "1786360449.0", "178636044900e-2"} {
		var response wire.Response
		if err := json.Unmarshal([]byte(`{"created_at":`+timestamp+`}`), &response); err != nil || response.CreatedAt != 1786360449 {
			t.Fatalf("%s: %v %d", timestamp, err, response.CreatedAt)
		}
	}
	for _, timestamp := range []string{"1786360449.5", "9223372036854775808", "1e1000000", `"1786360449"`} {
		var response wire.Response
		if err := json.Unmarshal([]byte(`{"created_at":`+timestamp+`}`), &response); err == nil {
			t.Fatalf("accepted %s", timestamp)
		}
	}
	for _, code := range []string{`"upstream_error"`, "500", "null"} {
		var response wire.Response
		if err := json.Unmarshal([]byte(`{"error":{"message":"failure","code":`+code+`}}`), &response); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResponsesRejectUnsupportedChoicesAndAudio(t *testing.T) {
	count := int64(2)
	if _, err := json.Marshal(outbound.ConvertToResponsesRequest(&model.InternalLLMRequest{Model: "model", N: &count})); err == nil {
		t.Fatal("multiple choices dropped")
	}
	encoder := &inbound.ResponseInbound{}
	if _, err := encoder.TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindMessageDelta, Message: &model.Message{Audio: &model.OutputAudio{Data: "audio"}}}}); err == nil {
		t.Fatal("audio silently dropped")
	}
}

func TestPinnedNamespaceHistoryAndCollision(t *testing.T) {
	decoder := &inbound.ResponseInbound{}
	request, err := decoder.TransformRequest(context.Background(), []byte(`{"model":"m","tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"ns__lookup","parameters":{"type":"object"}}]}],"input":[{"type":"function_call","id":"fc","call_id":"call","namespace":"ns","name":"ns__lookup","arguments":"{}"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.Tools[0].Function.Name != "ns__ns__lookup" || request.Messages[0].ToolCalls[0].Function.Name != "ns__ns__lookup" {
		t.Fatalf("namespace not flattened once: %+v", request)
	}
	response := &model.InternalLLMResponse{Choices: []model.Choice{{Message: &model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "call", Type: "function", Function: model.FunctionCall{Name: "ns__ns__lookup", Arguments: "{}"}}}}}}}
	encoded, err := decoder.TransformResponse(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	var result wire.Response
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if result.Output[0].Name != "ns__lookup" || result.Output[0].Namespace != "ns" {
		t.Fatalf("wrong native identity: %s", encoded)
	}
	request, err = decoder.TransformRequest(context.Background(), []byte(`{"model":"m","input":"hi","tools":[{"type":"function","name":"ns__lookup"},{"type":"namespace","name":"ns","tools":[{"type":"function","name":"lookup"}]}]}`))
	if err != nil {
		t.Fatalf("namespace collision must be forwarded to upstream: %v", err)
	}
	if !request.HasOpenAIResponsesPassthrough() {
		t.Fatal("namespace collision must preserve original tool identities")
	}
	response.Choices[0].Message.ToolCalls[0].Function.Name = "ns__lookup"
	encoded, err = decoder.TransformResponse(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if result.Output[0].Name != "ns__lookup" || result.Output[0].Namespace != "" {
		t.Fatalf("ambiguous tool identity must not be guessed: %s", encoded)
	}
}

func TestResponsesUsageSnapshotAndCacheWriteAccounting(t *testing.T) {
	body := `{"id":"r","status":"completed","service_tier":"flex","tools":[{"type":"file_search","future":false}],"output":[],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"future":0,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":2,"future_detail":false}}}`
	response, err := (&outbound.ResponseOutbound{}).TransformResponse(context.Background(), &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))})
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage.HasAnthropicCacheSemantic() || response.Usage.EffectiveInputTokens() != 10 || response.Usage.PromptTokensDetails.WriteCachedTokens != 2 {
		t.Fatalf("changed usage semantics: %+v", response.Usage)
	}
	encoded, err := (&inbound.ResponseInbound{}).TransformResponse(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	object := decodeObject(t, encoded)
	if string(object["service_tier"]) != `"flex"` || len(object["tools"]) == 0 {
		t.Fatalf("lost response metadata: %s", encoded)
	}
	usage := decodeObject(t, object["usage"])
	if string(usage["input_tokens"]) != "10" || string(usage["future"]) != "0" {
		t.Fatalf("lost usage snapshot: %s", encoded)
	}
	details := decodeObject(t, usage["input_tokens_details"])
	if string(details["future_detail"]) != "false" || string(details["cache_write_tokens"]) != "2" {
		t.Fatalf("lost nested usage: %s", encoded)
	}
}

func TestResponsesCitationZeroAndNativeUnknownEvent(t *testing.T) {
	decoder := &outbound.ResponseOutbound{}
	encoder := &inbound.ResponseInbound{}
	frames := []string{
		`{"type":"response.created","response":{"id":"r","model":"m","status":"in_progress","output":[]}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}`,
		`{"type":"response.output_text.annotation.added","output_index":0,"content_index":0,"annotation":{"type":"url_citation","start_index":0,"end_index":5,"url":"https://example.test","title":"source","future":0}}`,
		`{"type":"response.future_progress","future":{"value":9007199254740993}}`,
		`{"type":"response.completed","response":{"id":"r","model":"m","status":"completed","output":[]}}`,
	}
	var output []byte
	citations := 0
	for _, frame := range frames {
		events, err := decoder.TransformStreamEvent(context.Background(), []byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind == model.StreamEventKindCitationDelta {
				citations++
				if event.Citation.StartIndex == nil || *event.Citation.StartIndex != 0 {
					t.Fatal("zero annotation offset lost")
				}
			}
		}
		encoded, err := encoder.TransformStreamEvents(context.Background(), events)
		if err != nil {
			t.Fatal(err)
		}
		output = append(output, encoded...)
	}
	if citations != 1 {
		t.Fatalf("citation repeated %d times", citations)
	}
	if !bytes.Contains(output, []byte(`"value":9007199254740993`)) || !bytes.Contains(output, []byte(`"start_index":0`)) {
		t.Fatalf("native fields lost: %s", output)
	}
}

func TestResponsesTerminalFailureIncompleteAndReset(t *testing.T) {
	for _, fixture := range []struct {
		body   string
		code   string
		reason string
	}{
		{`{"type":"response.failed","response":{"id":"r","status":"failed","error":{"message":"failed","type":"upstream_error","code":"upstream_error"}}}`, "upstream_error", "stop"},
		{`{"type":"error","status":429,"error":{"message":"slow down","code":"rate_limit","type":"upstream_error"}}`, "rate_limit", "stop"},
		{`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`, "", "content_filter"},
	} {
		decoder := &outbound.ResponseOutbound{}
		events, err := decoder.TransformStreamEvent(context.Background(), []byte(fixture.body))
		if err != nil {
			t.Fatal(err)
		}
		response := model.InternalResponseFromStreamEvents(events)
		if response == nil || len(response.Choices) == 0 || response.Choices[0].FinishReason == nil || *response.Choices[0].FinishReason != fixture.reason {
			t.Fatalf("terminal missing: %+v", response)
		}
		if fixture.code != "" && (response.Error == nil || response.Error.Detail.Code != fixture.code) {
			t.Fatalf("lost error code: %+v", response)
		}
		if _, err := decoder.EndStream(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	encoder := &inbound.ResponseInbound{}
	if _, err := encoder.TransformRequest(context.Background(), []byte(`{"model":"m","input":"hi","truncation":"disabled"}`)); err != nil {
		t.Fatal(err)
	}
	encoder.ResetStream()
	raw, err := encoder.TransformStreamEvents(context.Background(), []model.StreamEvent{{Kind: model.StreamEventKindMessageStart, ID: "new", Model: "m", Created: 123, ServiceTier: "flex", Role: "assistant"}, {Kind: model.StreamEventKindMessageStop, StopReason: model.ParseFinishReason("stop")}, {Kind: model.StreamEventKindDone}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"truncation":"disabled"`)) || !bytes.Contains(raw, []byte(`"created_at":123`)) || !bytes.Contains(raw, []byte("response.completed")) {
		t.Fatalf("reset or terminal semantics: %s", raw)
	}
}

func TestResponsesPartDoneFlushesBeforeClose(t *testing.T) {
	decoder := &outbound.ResponseOutbound{}
	if _, err := decoder.TransformStreamEvent(context.Background(), []byte(`{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`)); err != nil {
		t.Fatal(err)
	}
	events, err := decoder.TransformStreamEvent(context.Background(), []byte(`{"type":"response.content_part.done","output_index":0,"content_index":0,"part":{"type":"output_text","text":"final only"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != model.StreamEventKindTextDelta || events[0].Delta.Text != "final only" || events[1].Kind != model.StreamEventKindContentBlockStop {
		t.Fatalf("part closed before its final content: %+v", events)
	}
}
