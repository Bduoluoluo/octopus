package openairesponses_test

import (
	"context"
	"io"
	"testing"

	"github.com/xuanli27/octopus/internal/protocol/openairesponses/inbound"
	"github.com/xuanli27/octopus/internal/protocol/openairesponses/outbound"
)

func TestResponsesToolDefinitionsDeferValidationToUpstream(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		tools string
		input string
	}{
		{
			name:  "namespace custom tool",
			tools: `[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: WORD"}}]}]`,
		},
		{
			name:  "mixed namespace tools",
			tools: `[{"type":"namespace","name":"functions","description":"tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}]`,
		},
		{
			name:  "unknown namespace tool",
			tools: `[{"type":"namespace","name":"functions","tools":[{"type":"future_tool","name":"future","options":{"enabled":false,"count":0}}]}]`,
		},
		{
			name:  "namespace without name",
			tools: `[{"type":"namespace","tools":[{"type":"function","name":"lookup"}]}]`,
		},
		{
			name:  "nested namespace",
			tools: `[{"type":"namespace","name":"outer","tools":[{"type":"namespace","name":"inner","tools":[{"type":"function","name":"lookup"}]}]}]`,
		},
		{
			name:  "duplicate function names",
			tools: `[{"type":"function","name":"lookup"},{"type":"function","name":"lookup"}]`,
		},
		{
			name:  "flattened namespace collision",
			tools: `[{"type":"function","name":"ns__lookup"},{"type":"namespace","name":"ns","tools":[{"type":"function","name":"lookup"}]}]`,
		},
		{
			name:  "history namespace collision",
			tools: `[]`,
			input: `[{"type":"function_call","id":"fc_first","call_id":"first","namespace":"ns","name":"inner__lookup","arguments":"{}"},{"type":"function_call","id":"fc_second","call_id":"second","namespace":"ns__inner","name":"lookup","arguments":"{}"}]`,
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			input := fixture.input
			if input == "" {
				input = `[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]`
			}
			body := []byte(`{"model":"client","input":` + input + `,"tools":` + fixture.tools + `,"tool_choice":"auto"}`)
			request, err := (&inbound.ResponseInbound{}).TransformRequest(context.Background(), body)
			if err != nil {
				t.Fatalf("inbound rejected tools before upstream: %v", err)
			}
			if err := request.Validate(); err != nil {
				t.Fatalf("relay validation rejected request: %v", err)
			}
			if !request.HasOpenAIResponsesPassthrough() {
				t.Fatal("native tools must retain their Responses protocol")
			}
			request.Model = "upstream"
			upstreamRequest, err := (&outbound.ResponseOutbound{}).TransformRequest(context.Background(), request, "http://example.test/v1", "test-key")
			if err != nil {
				t.Fatalf("outbound rejected same-protocol request: %v", err)
			}
			defer upstreamRequest.Body.Close()
			encoded, err := io.ReadAll(upstreamRequest.Body)
			if err != nil {
				t.Fatal(err)
			}
			actual := decodeObject(t, encoded)
			expected := decodeObject(t, body)
			for _, field := range []string{"input", "tools", "tool_choice"} {
				requireJSON(t, actual[field], expected[field])
			}
			requireJSON(t, actual["model"], []byte(`"upstream"`))
		})
	}
}
