package helper

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func TestAppendPromptSuffixJSON(t *testing.T) {
	tests := []struct {
		name        string
		channelType outbound.OutboundType
		body        string
		want        string
	}{
		{
			name:        "chat last user only",
			channelType: outbound.OutboundTypeOpenAIChat,
			body:        `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"last"}],"nested":{"text":"unchanged"}}`,
			want:        `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"last-suffix"}],"nested":{"text":"unchanged"}}`,
		},
		{
			name:        "chat multimodal appends a text block",
			channelType: outbound.OutboundTypeOpenAIChat,
			body:        `{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`,
			want:        `{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},{"type":"text","text":"-suffix"}]}]}`,
		},
		{
			name:        "responses input",
			channelType: outbound.OutboundTypeOpenAIResponse,
			body:        `{"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			want:        `{"input":[{"role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_text","text":"-suffix"}]}]}`,
		},
		{
			name:        "anthropic",
			channelType: outbound.OutboundTypeAnthropic,
			body:        `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			want:        `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"text","text":"-suffix"}]}]}`,
		},
		{
			name: "responses string and precision", channelType: outbound.OutboundTypeOpenAIResponse,
			body: `{"input":"hi","instructions":"keep","extra":{"integer":9007199254740993}}`,
			want: `{"input":"hi-suffix","instructions":"keep","extra":{"integer":9007199254740993}}`,
		},
		{
			name: "chat channel responses probe", channelType: outbound.OutboundTypeOpenAIChat,
			body: `{"input":[{"role":"user","content":"hi"}]}`,
			want: `{"input":[{"role":"user","content":"hi-suffix"}]}`,
		},
		{
			name: "tool output after user", channelType: outbound.OutboundTypeOpenAIChat,
			body: `{"messages":[{"role":"system","content":"keep"},{"role":"user","content":"question"},{"role":"assistant","tool_calls":[{"id":"call_1"}]},{"role":"tool","tool_call_id":"call_1","content":"result"}]}`,
			want: `{"messages":[{"role":"system","content":"keep"},{"role":"user","content":"question-suffix"},{"role":"assistant","tool_calls":[{"id":"call_1"}]},{"role":"tool","tool_call_id":"call_1","content":"result"}]}`,
		},
		{
			name: "responses mixed input items", channelType: outbound.OutboundTypeOpenAIResponse,
			body: `{"input":[{"type":"message","role":"user","content":"question"},{"type":"reasoning","encrypted_content":"keep"},{"type":"function_call","arguments":"{}"},{"type":"function_call_output","output":"keep"}]}`,
			want: `{"input":[{"type":"message","role":"user","content":"question-suffix"},{"type":"reasoning","encrypted_content":"keep"},{"type":"function_call","arguments":"{}"},{"type":"function_call_output","output":"keep"}]}`,
		},
		{
			name: "anthropic tool result turn", channelType: outbound.OutboundTypeAnthropic,
			body: `{"messages":[{"role":"user","content":"question"},{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"keep"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"result"}]}]}`,
			want: `{"messages":[{"role":"user","content":"question-suffix"},{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"keep"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"result"}]}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, changed, err := AppendPromptSuffixJSON([]byte(test.body), test.channelType, "-suffix")
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("expected request to change")
			}
			if !reflect.DeepEqual(decodePromptSuffixJSON(t, string(got)), decodePromptSuffixJSON(t, test.want)) {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}

func decodePromptSuffixJSON(t *testing.T, data string) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPromptSuffixSkipsRequestsWithoutUserContent(t *testing.T) {
	for _, test := range []struct {
		channelType outbound.OutboundType
		body        string
	}{
		{outbound.OutboundTypeOpenAIResponse, `{"input":null,"previous_response_id":"resp_1"}`},
		{outbound.OutboundTypeOpenAIResponse, `{"input":[],"instructions":"keep"}`},
		{outbound.OutboundTypeOpenAIResponse, `{"input":[{"type":"function_call_output","call_id":"call_1","output":"keep"}]}`},
		{outbound.OutboundTypeOpenAIChat, `{"messages":[{"role":"assistant","content":"keep"},{"role":"tool","content":"keep"}]}`},
		{outbound.OutboundTypeOpenAIChat, `{"messages":[null,{"role":"user","content":null}]}`},
		{outbound.OutboundTypeAnthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","content":"keep"}]}]}`},
		{outbound.OutboundTypeGemini, `{"contents":[null,{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"text":"keep"}}}]}]}`},
		{outbound.OutboundTypeGemini, `{"contents":[{"role":"user","parts":[{"text":"keep"}]}]}`},
		{outbound.OutboundTypeVolcengine, `{"input":"keep"}`},
		{outbound.OutboundTypeOpenAIEmbedding, `{"input":"keep"}`},
		{outbound.OutboundTypeOpenAIEmbedding, `invalid JSON is not parsed`},
		{outbound.OutboundType(99), `{"input":"keep"}`},
	} {
		got, changed, err := AppendPromptSuffixJSON([]byte(test.body), test.channelType, "-suffix")
		if err != nil || changed || string(got) != test.body {
			t.Fatalf("unexpected change to %s: got %s, changed=%v, error=%v", test.body, got, changed, err)
		}
	}
	for _, suffix := range []string{"", "\n\n", " \n\u8bf7\u7528\u4e2d\u6587\u56de\u7b54\u3002\n"} {
		got, changed, err := AppendPromptSuffixJSON([]byte(`{"input":"hi"}`), outbound.OutboundTypeOpenAIResponse, suffix)
		if err != nil || changed != (suffix != "") {
			t.Fatalf("unexpected result for suffix %q: %s, %v", suffix, got, err)
		}
		var payload struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal(got, &payload); err != nil || payload.Input != "hi"+suffix {
			t.Fatalf("suffix whitespace or Unicode lost: %q, %v", payload.Input, err)
		}
	}
}

func TestApplyPromptSuffixRestoresRequestBody(t *testing.T) {
	request, err := http.NewRequest(http.MethodPost, "https://example.com", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyPromptSuffix(request, outbound.OutboundTypeOpenAIResponse, " world"); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "hello world") || request.ContentLength != int64(len(body)) {
		t.Fatalf("unexpected request body: %s", body)
	}
	getBody, err := request.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	defer getBody.Close()
	replayed, err := io.ReadAll(getBody)
	if err != nil {
		t.Fatal(err)
	}
	if string(replayed) != string(body) {
		t.Fatalf("GetBody mismatch: %s != %s", replayed, body)
	}
}
