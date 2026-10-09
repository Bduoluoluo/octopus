package anthropic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"

	inbound "github.com/xuanli27/octopus/internal/protocol/anthropic/inbound"
	outbound "github.com/xuanli27/octopus/internal/protocol/anthropic/outbound"
)

func decodeJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNativeRequestRoundTrip(t *testing.T) {
	body := []byte(`{
	"model":"claude-original","max_tokens":128,"temperature":0,"top_p":0,"stream":false,
	"system":[{"type":"text","text":"first","future":0},{"type":"text","text":"second","cache_control":{"type":"ephemeral","ttl":"1h"}}],
	"metadata":{"user_id":"user-1","future":false},"thinking":{"type":"disabled"},"cache_control":{"type":"ephemeral"},"future":null,
	"tools":[{"name":"lookup","input_schema":{"type":"object"},"strict":false,"defer_loading":false}],
	"messages":[
	{"role":"user","content":[{"type":"document","source":{"type":"content","content":[{"type":"text","text":"source"}],"future":0},"citations":{"enabled":false,"future":0},"title":"doc"},{"type":"text","text":"ask"}]},
	{"role":"assistant","content":[{"type":"text","text":"before","citations":[]},{"type":"thinking","thinking":"plan","signature":"opaque"},{"type":"tool_use","id":"call1","name":"lookup","input":{"index":0}},{"type":"text","text":"after","citations":[{"type":"char_location","document_index":0,"start_char_index":0,"end_char_index":1,"future":false}]}]},
	{"role":"user","content":[{"type":"text","text":"before-result"},{"type":"tool_result","tool_use_id":"call1","is_error":false,"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}},{"type":"document","source":{"type":"url","url":"https://example.test/a.pdf"},"citations":{"enabled":true}},{"type":"text","text":"result","citations":null}]},{"type":"text","text":"after-result"}]}
	]}`)
	request, err := (&inbound.MessagesInbound{}).TransformRequest(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	request.Model = "claude-upstream"
	upstream, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), request, "https://example.test/v1", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatal(err)
	}
	want, got := decodeJSON(t, body), decodeJSON(t, encoded)
	want["model"] = "claude-upstream"
	for _, key := range []string{"model", "messages", "system", "metadata", "thinking", "tools", "temperature", "top_p", "stream", "cache_control", "future"} {
		if !reflect.DeepEqual(want[key], got[key]) {
			t.Errorf("%s changed:\nwant %#v\ngot %#v", key, want[key], got[key])
		}
	}
	var nestedCount int
	for _, message := range request.Messages {
		if message.Role == "tool" {
			nestedCount = len(message.Content.MultipleContent)
		}
	}
	if nestedCount != 3 {
		t.Fatalf("tool result nested parts = %d", nestedCount)
	}
}

func TestNativeResponseRoundTrip(t *testing.T) {
	body := []byte(`{"id":"msg-native","type":"message","role":"assistant","model":"upstream","future":false,"content":[{"type":"text","text":"before","citations":null},{"type":"server_tool_use","id":"server1","name":"web_search","input":{"query":"q"},"caller":{"type":"direct"}},{"type":"web_search_tool_result","tool_use_id":"server1","content":[{"type":"web_search_result","url":"https://example.test","title":"result","encrypted_content":"opaque","future":0}]},{"type":"thinking","thinking":"plan","signature":"signature"},{"type":"redacted_thinking","data":"opaque-data"},{"type":"text","text":"after","citations":[{"type":"page_location","document_index":0,"document_title":null,"start_page_number":0,"end_page_number":2,"future":false}]},{"type":"future_native","payload":{"count":9007199254740993}}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":3}}`)
	response, err := (&outbound.MessageOutbound{}).TransformResponse(context.Background(), &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))})
	if err != nil {
		t.Fatal(err)
	}
	response.Model = "client-model"
	encoded, err := (&inbound.MessagesInbound{}).TransformResponse(context.Background(), response)
	if err != nil {
		t.Fatal(err)
	}
	want, got := decodeJSON(t, body), decodeJSON(t, encoded)
	if !reflect.DeepEqual(want["content"], got["content"]) {
		t.Fatalf("content changed:\nwant %s\ngot %s", body, encoded)
	}
	if got["model"] != "client-model" || got["future"] != false {
		t.Fatalf("model or extension lost: %s", encoded)
	}
}

func TestNativeRequestEmptyAndNullFields(t *testing.T) {
	for _, suffix := range []string{`"system":[],"metadata":null,"cache_control":null`, `"system":null,"thinking":null`, `"system":""`} {
		body := []byte(`{"model":"fixture","max_tokens":1,"messages":[{"role":"user","content":[]}],` + suffix + `}`)
		request, err := (&inbound.MessagesInbound{}).TransformRequest(context.Background(), body)
		if err != nil {
			t.Fatal(err)
		}
		upstream, err := (&outbound.MessageOutbound{}).TransformRequest(context.Background(), request, "https://example.test/v1", "fixture")
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := io.ReadAll(upstream.Body)
		want, got := decodeJSON(t, body), decodeJSON(t, encoded)
		for key, expected := range want {
			if !reflect.DeepEqual(expected, got[key]) {
				t.Errorf("%s: %s became %s", key, body, encoded)
			}
		}
	}
}
