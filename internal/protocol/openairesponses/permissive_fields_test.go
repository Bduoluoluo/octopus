package openairesponses_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	wire "github.com/xuanli27/octopus/internal/protocol/openairesponses"
	"github.com/xuanli27/octopus/internal/protocol/openairesponses/inbound"
	"github.com/xuanli27/octopus/internal/protocol/openairesponses/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func permissiveResponsesRequest(t *testing.T, body string) (*model.InternalLLMRequest, map[string]json.RawMessage) {
	t.Helper()
	request, err := (&inbound.ResponseInbound{}).TransformRequest(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	request.Model = "upstream-model"
	before := model.CloneRequest(request)
	upstream, err := (&outbound.ResponseOutbound{}).TransformRequest(context.Background(), request, "https://example.test/v1", "test-key")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	defer upstream.Body.Close()
	encoded, err := io.ReadAll(upstream.Body)
	if err != nil {
		t.Fatal(err)
	}
	actual := decodeObject(t, encoded)
	if !reflect.DeepEqual(request, before) {
		t.Fatal("outbound conversion mutated source request")
	}
	requireJSON(t, actual["model"], []byte(`"upstream-model"`))
	if original, present := decodeObject(t, []byte(body))["input"]; present {
		requireJSON(t, actual["input"], original)
	} else if _, present := actual["input"]; present {
		t.Fatalf("input fabricated for native request: %s", encoded)
	}
	return request, actual
}

func TestResponsesPermissiveToolParametersRoundTrip(t *testing.T) {
	for _, parameters := range []string{
		`true`, `false`, `null`, `[]`, `["string",{"future":9007199254740993}]`,
		`"provider-schema"`, `9007199254740993`, `{"type":"object","properties":{"count":{"const":9007199254740993}}}`,
	} {
		t.Run(parameters, func(t *testing.T) {
			tools := `[{"type":"function","name":"lookup","parameters":` + parameters + `},{"type":"namespace","name":"functions","tools":[{"type":"function","name":"nested","parameters":` + parameters + `}]}]`
			_, actual := permissiveResponsesRequest(t, `{"model":"client","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"tools":`+tools+`}`)
			requireJSON(t, actual["tools"], []byte(tools))
		})
	}
}

func TestResponsesPermissiveToolChoiceRoundTrip(t *testing.T) {
	for _, choice := range []string{
		`"future-mode"`, `""`, `null`, `true`, `0`, `[]`, `["auto",{"future":false}]`,
		`{"mode":"auto","future":false}`,
		`{"type":"function","name":"lookup","future":{"count":9007199254740993}}`,
		`{"type":"future","name":{"id":"lookup"},"tools":["lookup"],"mode":{"weight":0}}`,
		`{"type":"allowed_tools","mode":"required","tools":[{"type":"future","name":"lookup","namespace":"functions","options":false}]}`,
	} {
		t.Run(choice, func(t *testing.T) {
			request, actual := permissiveResponsesRequest(t, `{"model":"client","input":"hi","tools":[{"type":"function","name":"lookup"}],"tool_choice":`+choice+`}`)
			requireJSON(t, actual["tool_choice"], []byte(choice))
			if choice != `null` && !request.HasOpenAIResponsesPassthrough() {
				t.Fatal("native tool choice must retain protocol provenance")
			}
			var response wire.Response
			if err := json.Unmarshal([]byte(`{"tool_choice":`+choice+`}`), &response); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			requireJSON(t, decodeObject(t, encoded)["tool_choice"], []byte(choice))
		})
	}
}

func TestResponsesPermissiveReasoningRoundTrip(t *testing.T) {
	for _, reasoning := range []string{
		`{"effort":"turbo","summary":"brief","generate_summary":"provider-summary"}`,
		`{"effort":" high ","summary":"","context":"all_turns","future":{"budget":9007199254740993,"enabled":false}}`,
		`{"context":"provider-context","future":0}`,
		`{}`,
	} {
		t.Run(reasoning, func(t *testing.T) {
			request, actual := permissiveResponsesRequest(t, `{"model":"client","input":"hi","reasoning":`+reasoning+`}`)
			requireJSON(t, actual["reasoning"], []byte(reasoning))
			request.ReasoningEffort = "selected-effort"
			encoded, err := json.Marshal(outbound.ConvertToResponsesRequest(request))
			if err != nil {
				t.Fatal(err)
			}
			expected := decodeObject(t, []byte(reasoning))
			expected["effort"] = json.RawMessage(`"selected-effort"`)
			want, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			requireJSON(t, decodeObject(t, encoded)["reasoning"], want)
		})
	}
}

func TestResponsesPermissiveTimestampRetainsUsableOutput(t *testing.T) {
	for _, fixture := range []struct {
		raw     string
		seconds int64
	}{
		{`"1786360449.75"`, 1786360449},
		{`1786360449.75`, 1786360449},
		{`-1.75`, -1},
		{`170000000000000000000000000000e-20`, 1700000000},
		{`9223372036854775807.9`, 9223372036854775807},
		{`-9223372036854775808.9`, -9223372036854775808},
		{`0.0001`, 0},
		{`1e-9223372036854775808`, 0},
		{`1e1000000`, 0},
		{`9223372036854775808`, 0},
		{`"provider-time"`, 0},
		{`null`, 0},
		{`false`, 0},
		{`{"seconds":1786360449}`, 0},
	} {
		t.Run(fixture.raw, func(t *testing.T) {
			body := `{"id":"r","model":"upstream","status":"completed","created_at":` + fixture.raw + `,"output":[{"id":"msg","type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`
			response, err := (&outbound.ResponseOutbound{}).TransformResponse(context.Background(), &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))})
			if err != nil {
				t.Fatal(err)
			}
			if response.Created != fixture.seconds || response.Choices[0].Message.Content.Content == nil || *response.Choices[0].Message.Content.Content != "OK" {
				t.Fatalf("usable output lost or timestamp incorrect: %+v", response)
			}
			response.Model = "requested-model"
			encoded, err := (&inbound.ResponseInbound{}).TransformResponse(context.Background(), response)
			if err != nil {
				t.Fatal(err)
			}
			actual := decodeObject(t, encoded)
			requireJSON(t, actual["created_at"], []byte(fixture.raw))
			requireJSON(t, actual["output"], decodeObject(t, []byte(body))["output"])
			requireJSON(t, actual["model"], []byte(`"requested-model"`))
		})
	}
}

func TestResponsesPermissiveFieldsKeepPrerequisites(t *testing.T) {
	for _, body := range []string{`{"model":"m","input":"hi","tool_choice":`, `{"input":"hi"}`, `{"model":"m","input":{`} {
		if _, err := (&inbound.ResponseInbound{}).TransformRequest(context.Background(), []byte(body)); err == nil {
			t.Fatalf("invalid request accepted: %s", body)
		}
	}
	for _, body := range []string{`{"created_at":`, `{"created_at":1.2,"output":{}}`} {
		var response wire.Response
		if err := json.Unmarshal([]byte(body), &response); err == nil {
			t.Fatalf("invalid response accepted: %s", body)
		}
	}
	for _, provenance := range []model.APIFormat{"", model.APIFormatOpenAIChatCompletion, model.APIFormatOpenAIResponse} {
		request := &model.InternalLLMRequest{Model: "m", RawAPIFormat: provenance, Tools: []model.Tool{{Type: "function", Function: model.Function{Name: "lookup", Parameters: json.RawMessage(`true`)}}}}
		if _, err := json.Marshal(outbound.ConvertToResponsesRequest(request)); err == nil {
			t.Fatalf("unpreserved cross-protocol parameters silently dropped for %q", provenance)
		}
	}
}

func TestResponsesPermissiveNativeInputRoundTrip(t *testing.T) {
	for _, input := range []string{
		`{"future_input":{"count":9007199254740993,"enabled":false}}`,
		`9007199254740993`, `true`, `false`, `null`, `[]`,
		`[9007199254740993,"provider-input",false]`,
	} {
		t.Run(input, func(t *testing.T) {
			request, actual := permissiveResponsesRequest(t, `{"model":"client","input":`+input+`,"instructions":"guide"}`)
			if !request.HasOpenAIResponsesPassthrough() {
				t.Fatal("native input must not fabricate a cross-protocol message")
			}
			requireJSON(t, actual["input"], []byte(input))
			requireJSON(t, actual["instructions"], []byte(`"guide"`))
		})
	}
	request, _ := permissiveResponsesRequest(t, `{"model":"client","input":{"future_input":false}}`)
	request.SetOpenAIRawInputItems(json.RawMessage(`[{"role":"user","content":"replayed"}]`))
	encoded, err := json.Marshal(outbound.ConvertToResponsesRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	requireJSON(t, decodeObject(t, encoded)["input"], []byte(`[{"role":"user","content":"replayed"}]`))
}

func TestResponsesPermissivePromptOnlyRoundTrip(t *testing.T) {
	for _, body := range []string{
		`{"model":"client","prompt":{"id":"template","variables":{"count":9007199254740993,"enabled":false}}}`,
		`{"model":"client","prompt":{"id":"template"},"instructions":"guide"}`,
		`{"model":"client","previous_response_id":"existing-response"}`,
		`{"model":"client"}`,
	} {
		t.Run(body, func(t *testing.T) {
			request, actual := permissiveResponsesRequest(t, body)
			if !request.HasOpenAIResponsesPassthrough() {
				t.Fatal("request without projected input must retain Responses provenance")
			}
			original := decodeObject(t, []byte(body))
			for _, field := range []string{"prompt", "instructions", "previous_response_id"} {
				if raw, present := original[field]; present {
					requireJSON(t, actual[field], raw)
				}
			}
		})
	}
}

func TestResponsesEmptyErrorDoesNotFailCompletedOutput(t *testing.T) {
	for _, status := range []string{`"completed"`, `null`, `"in_progress"`} {
		body := `{"status":` + status + `,"error":{},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`
		var response wire.Response
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error != nil {
			t.Fatalf("empty error treated as failure: %+v", response.Error)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		actual := decodeObject(t, encoded)
		if _, present := actual["error"]; present {
			t.Fatalf("empty error reintroduced: %s", encoded)
		}
		requireJSON(t, actual["output"], decodeObject(t, []byte(body))["output"])
	}
	for _, body := range []string{
		`{"status":"failed","error":{}}`,
		`{"status":"completed","error":{"message":"upstream failed"}}`,
		`{"error":{"code":429}}`,
		`{"error":{"type":"upstream_error"}}`,
		`{"error":{"detail":"provider-specific failure"}}`,
	} {
		var response wire.Response
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error == nil {
			t.Fatalf("upstream failure dropped: %s", body)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		actual := decodeObject(t, encoded)
		if len(actual["error"]) == 0 {
			t.Fatalf("upstream failure omitted from encoding: %s", encoded)
		}
		originalError := decodeObject(t, decodeObject(t, []byte(body))["error"])
		encodedError := decodeObject(t, actual["error"])
		for key, original := range originalError {
			if key != "code" {
				requireJSON(t, encodedError[key], original)
			}
		}
	}
}
