package inbound

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConvertToInternalRequestPreservesRawInputItems(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: ResponsesInput{Items: []ResponsesItem{
			{Type: "input_text", Text: stringPtr("hello")},
		}},
	}

	internalReq, err := convertToInternalRequest(req)
	if err != nil {
		t.Fatalf("convertToInternalRequest failed: %v", err)
	}
	if len(internalReq.RawInputItems) == 0 {
		t.Fatalf("expected raw input items to be preserved")
	}

	var items []map[string]any
	if err := json.Unmarshal(internalReq.RawInputItems, &items); err != nil {
		t.Fatalf("unmarshal raw input items failed: %v", err)
	}
	if len(items) != 1 || items[0]["type"] != "input_text" {
		t.Fatalf("expected original raw input items to be kept, got %#v", items)
	}
	if internalReq.TransformOptions.ArrayInputs == nil || !*internalReq.TransformOptions.ArrayInputs {
		t.Fatalf("expected array input flag to stay true")
	}
}

func TestConvertToInternalRequestMarksPassthroughForUnsupportedToolType(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: ResponsesInput{Text: stringPtr("hello")},
		Tools: []ResponsesTool{{
			Type: "apply_patch",
		}},
	}

	internalReq, err := convertToInternalRequest(req)
	if err != nil {
		t.Fatalf("convertToInternalRequest failed: %v", err)
	}
	if !internalReq.HasOpenAIResponsesPassthrough() {
		t.Fatalf("expected unsupported responses tool to require passthrough")
	}
	if ext := internalReq.GetOpenAIExtensions(); !ext.ResponsesPassthroughRequired || ext.ResponsesPassthroughReason != "tool:apply_patch" {
		t.Fatalf("expected OpenAI extension passthrough view, got %#v", ext)
	}
}

func TestConvertToInternalRequestMarksPassthroughForUnsupportedInputItem(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: ResponsesInput{Items: []ResponsesItem{{
			Type:   "apply_patch_call_output",
			CallID: "apc_123",
		}}},
	}

	internalReq, err := convertToInternalRequest(req)
	if err != nil {
		t.Fatalf("convertToInternalRequest failed: %v", err)
	}
	if !internalReq.HasOpenAIResponsesPassthrough() {
		t.Fatalf("expected unsupported responses input item to require passthrough")
	}
	if ext := internalReq.GetOpenAIExtensions(); !ext.ResponsesPassthroughRequired || ext.ResponsesPassthroughReason != "input:apply_patch_call_output" {
		t.Fatalf("expected OpenAI extension passthrough view, got %#v", ext)
	}
}

func TestConvertToInternalRequestDoesNotMarkPassthroughForSupportedFileAndAudioInputs(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: ResponsesInput{Items: []ResponsesItem{
			{
				Type: "message",
				Role: "user",
				Content: &ResponsesInput{Items: []ResponsesItem{
					{Type: "input_file", FileID: stringPtr("file_123")},
					{Type: "input_audio", InputAudio: &ResponsesInputAudio{Format: "wav", Data: "AAA="}},
				}},
			},
		}},
	}

	internalReq, err := convertToInternalRequest(req)
	if err != nil {
		t.Fatalf("convertToInternalRequest failed: %v", err)
	}
	if internalReq.HasOpenAIResponsesPassthrough() {
		t.Fatalf("expected supported file/audio inputs to stay normalized without passthrough")
	}
	if len(internalReq.Messages) != 1 || len(internalReq.Messages[0].Content.MultipleContent) != 2 {
		t.Fatalf("expected supported file/audio inputs to normalize into message content, got %#v", internalReq.Messages)
	}
	if internalReq.Messages[0].Content.MultipleContent[0].Type != "file" {
		t.Fatalf("expected file content part, got %#v", internalReq.Messages[0].Content.MultipleContent[0])
	}
	if internalReq.Messages[0].Content.MultipleContent[1].Type != "input_audio" {
		t.Fatalf("expected input_audio content part, got %#v", internalReq.Messages[0].Content.MultipleContent[1])
	}
}

func TestConvertToInternalRequestNormalizesTopLevelInputFile(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: ResponsesInput{Items: []ResponsesItem{{
			Type:     "input_file",
			FileID:   stringPtr("file_456"),
			Filename: stringPtr("notes.txt"),
		}}},
	}

	internalReq, err := convertToInternalRequest(req)
	if err != nil {
		t.Fatalf("convertToInternalRequest failed: %v", err)
	}
	if internalReq.HasOpenAIResponsesPassthrough() {
		t.Fatalf("expected top-level input_file to stay normalized without passthrough")
	}
	if len(internalReq.Messages) != 1 {
		t.Fatalf("expected one normalized message, got %#v", internalReq.Messages)
	}
	if internalReq.Messages[0].Role != "user" {
		t.Fatalf("expected top-level input_file to default to user role, got %#v", internalReq.Messages[0].Role)
	}
	if len(internalReq.Messages[0].Content.MultipleContent) != 1 || internalReq.Messages[0].Content.MultipleContent[0].Type != "file" {
		t.Fatalf("expected top-level input_file to become file content, got %#v", internalReq.Messages[0].Content)
	}
	if internalReq.Messages[0].Content.MultipleContent[0].File == nil || internalReq.Messages[0].Content.MultipleContent[0].File.FileID != "file_456" {
		t.Fatalf("expected normalized file reference to preserve file_id, got %#v", internalReq.Messages[0].Content.MultipleContent[0].File)
	}
}

func stringPtr(value string) *string {
	return &value
}

// Issue #115: Codex sends reasoning.content=null and tool_search_call.arguments as object.
func TestTransformRequestCodexToolSearchAndNullReasoningContent(t *testing.T) {
	body := []byte(`{
  "model": "gpt-5.5",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [{ "type": "input_text", "text": "hello" }]
    },
    {
      "type": "reasoning",
      "summary": [{ "type": "summary_text", "text": "thinking" }],
      "content": null,
      "encrypted_content": "sig_123"
    },
    {
      "type": "tool_search_call",
      "call_id": "call_search",
      "status": "completed",
      "execution": "client",
      "arguments": {
        "query": "spawn subagent code review",
        "limit": 5
      }
    }
  ],
  "tools": [
    {
      "type": "function",
      "name": "exec_command",
      "parameters": { "type": "object" }
    },
    {
      "type": "custom",
      "name": "apply_patch",
      "format": { "type": "grammar", "syntax": "lark", "definition": "start: begin_patch" }
    }
  ],
  "include": ["reasoning.encrypted_content"],
  "stream": true
}`)

	inbound := &ResponseInbound{}
	req, err := inbound.TransformRequest(t.Context(), body)
	if err != nil {
		t.Fatalf("TransformRequest failed: %v", err)
	}
	if req.Model != "gpt-5.5" {
		t.Fatalf("model: got %q", req.Model)
	}
	// Unsupported tool types / input items must mark passthrough rather than hard-fail.
	if !req.HasOpenAIResponsesPassthrough() {
		t.Fatalf("expected passthrough for custom tools / tool_search_call")
	}
	// Raw input items must preserve object arguments for passthrough fidelity.
	raw := req.OpenAIRawInputItems()
	if len(raw) == 0 {
		t.Fatalf("expected raw input items preserved")
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("unmarshal raw items: %v", err)
	}
	foundSearch := false
	for _, it := range items {
		if it["type"] == "tool_search_call" {
			foundSearch = true
			args, ok := it["arguments"].(map[string]any)
			if !ok {
				t.Fatalf("expected arguments object preserved, got %#v", it["arguments"])
			}
			if args["query"] != "spawn subagent code review" {
				t.Fatalf("arguments.query lost, got %#v", args)
			}
		}
	}
	if !foundSearch {
		t.Fatalf("tool_search_call missing from raw items: %#v", items)
	}
}

func TestFlexibleJSONStringAcceptsObjectAndString(t *testing.T) {
	var s FlexibleJSONString
	if err := json.Unmarshal([]byte(`{"a":1}`), &s); err != nil {
		t.Fatalf("object: %v", err)
	}
	if !strings.Contains(s.String(), "\"a\"") {
		t.Fatalf("expected compact JSON object, got %q", s)
	}
	if err := json.Unmarshal([]byte(`"hello"`), &s); err != nil {
		t.Fatalf("string: %v", err)
	}
	if s.String() != "hello" {
		t.Fatalf("got %q", s)
	}
	if err := json.Unmarshal([]byte(`null`), &s); err != nil || s.String() != "" {
		t.Fatalf("null: err=%v val=%q", err, s)
	}
}

func TestResponsesInputAcceptsNull(t *testing.T) {
	var input ResponsesInput
	if err := json.Unmarshal([]byte(`null`), &input); err != nil {
		t.Fatalf("null content: %v", err)
	}
	if input.Text != nil || len(input.Items) != 0 {
		t.Fatalf("expected empty input for null, got %#v", input)
	}
}
