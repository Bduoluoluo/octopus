package openairesponses_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/tmaxmax/go-sse"
	"github.com/xuanli27/octopus/internal/protocol/openairesponses/inbound"
	"github.com/xuanli27/octopus/internal/protocol/openairesponses/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

const responsesSnapshotMetadataFixture = `{
	"id":"snapshot-response","model":"fixture","status":"completed",
	"output":[
		{"type":"reasoning","id":"reason-first","summary":[{"type":"summary_text","text":"plan one"}],"reasoning_content":[{"type":"reasoning_text","text":" detail one"}],"encrypted_content":"opaque-first"},
		{"type":"message","id":"message","role":"assistant","content":[{"type":"output_text","text":"corrected answer","annotations":[{"type":"url_citation","url":"https://source.test","title":"source","start_index":0,"end_index":0,"future":false}]}]},
		{"type":"custom_tool_call","id":"custom","call_id":"call-custom","name":"edit","namespace":"scripts","input":"final input","future":{"value":9007199254740993}},
		{"type":"reasoning","id":"reason-second","summary":[{"type":"summary_text","text":"plan two"}],"encrypted_content":"opaque-second"},
		{"type":"function_call","id":"function-first","call_id":"call-first","name":"lookup","namespace":"primary","arguments":"{\"value\":3}"},
		{"type":"image_generation_call","id":"image","result":"encoded","output_format":"png"},
		{"type":"future_native","id":"future","opaque":{"counter":9007199254740993,"values":[false,null,{"zero":0}],"empty":[]}},
		{"type":"function_call","id":"function-second","call_id":"call-second","name":"lookup","namespace":"secondary","arguments":"{}"}
	],
	"usage":{"input_tokens":4,"output_tokens":5,"total_tokens":9}
}`

func TestResponsesSnapshotMetadataNonStreamProjection(test *testing.T) {
	decoder := &outbound.ResponseOutbound{}
	response, err := decoder.TransformResponse(context.Background(), &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(responsesSnapshotMetadataFixture)),
	})
	if err != nil {
		test.Fatal(err)
	}
	assertResponsesSnapshotMetadata(test, response)
	encoder := &inbound.ResponseInbound{}
	encoded, err := encoder.TransformResponse(context.Background(), response)
	if err != nil {
		test.Fatal(err)
	}
	assertResponsesSnapshotNativeOutput(test, snapshotMetadataOutput(test, encoded))
}

func TestResponsesSnapshotMetadataTerminalCorrection(test *testing.T) {
	for _, fullTerminal := range []bool{true, false} {
		name := "merged_item_snapshots"
		if fullTerminal {
			name = "full_terminal_snapshot"
		}
		test.Run(name, func(test *testing.T) {
			decoder := &outbound.ResponseOutbound{}
			encoder := &inbound.ResponseInbound{}
			var aggregate model.StreamAggregator
			var finalSnapshot *model.Message
			for _, frame := range responsesSnapshotMetadataFrames(test, fullTerminal) {
				events, err := decoder.TransformStreamEvent(context.Background(), frame)
				if err != nil {
					test.Fatalf("decode %s: %v", frame, err)
				}
				for _, event := range events {
					if event.Kind == model.StreamEventKindMessageStop {
						finalSnapshot = event.Message
					}
				}
				aggregate.Add(model.InternalResponseFromStreamEvents(events))
				encoded, err := encoder.TransformStreamEvents(context.Background(), events)
				if err != nil {
					test.Fatal(err)
				}
				if fullTerminal && strings.HasPrefix(string(frame), `{"type":"response.completed"`) {
					assertResponsesSnapshotTerminalFrame(test, encoded)
				}
			}
			if _, err := decoder.EndStream(context.Background()); err != nil {
				test.Fatal(err)
			}
			if finalSnapshot == nil {
				test.Fatal("correction did not produce a final message snapshot")
			}
			assertResponsesSnapshotMessage(test, finalSnapshot)
			for attempt := 0; attempt < 2; attempt++ {
				response := aggregate.Response()
				assertResponsesSnapshotMetadata(test, response)
				*response.Choices[0].Message.Content.MultipleContent[0].Text = "mutated result"
				response.Choices[0].Message.ToolCalls[0].Function.Arguments = "mutated result"
				response.Choices[0].Message.ReasoningBlocks[0].Signature = "mutated result"
			}
			response, err := encoder.GetInternalResponse(context.Background())
			if err != nil {
				test.Fatal(err)
			}
			assertResponsesSnapshotMetadata(test, response)
		})
	}
}

func TestResponsesSnapshotMetadataTopLevelTextWithCitations(test *testing.T) {
	decoder := &outbound.ResponseOutbound{}
	response, err := decoder.TransformResponse(context.Background(), &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body: io.NopCloser(strings.NewReader(`{"status":"completed","output":[
			{"type":"output_text","text":"prefix"},
			{"type":"message","content":[{"type":"output_text","text":"cited","annotations":[{"type":"url_citation","url":"https://source.test","start_index":0,"end_index":5}]}]},
			{"type":"image_generation_call","result":"encoded"}
		]}`)),
	})
	if err != nil || response == nil || response.Error != nil || len(response.Choices) != 1 {
		test.Fatalf("text snapshot rejected: %+v %v", response, err)
	}
	parts := response.Choices[0].Message.Content.MultipleContent
	if len(parts) != 3 || parts[0].Text == nil || *parts[0].Text != "prefix" || parts[1].Text == nil || *parts[1].Text != "cited" || len(parts[1].Citations) != 1 || parts[2].ImageURL == nil {
		test.Fatalf("citation projection erased top-level text or image: %+v", parts)
	}
}

func assertResponsesSnapshotTerminalFrame(test *testing.T, encoded []byte) {
	test.Helper()
	found := false
	for event, err := range sse.Read(strings.NewReader(string(encoded)), nil) {
		if err != nil {
			test.Fatal(err)
		}
		var frame struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal([]byte(event.Data), &frame); err != nil {
			test.Fatal(err)
		}
		if frame.Type == "response.completed" {
			assertResponsesSnapshotNativeOutput(test, snapshotMetadataOutput(test, frame.Response))
			found = true
		}
	}
	if !found {
		test.Fatalf("terminal SSE snapshot missing: %s", encoded)
	}
}

func responsesSnapshotMetadataFrames(test *testing.T, fullTerminal bool) [][]byte {
	test.Helper()
	frames := [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"snapshot-response","model":"fixture","status":"in_progress","output":[]}}`),
		[]byte(`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"message","role":"assistant","content":[]}}`),
		[]byte(`{"type":"response.output_text.delta","output_index":1,"item_id":"message","content_index":0,"delta":"draft answer"}`),
		[]byte(`{"type":"response.output_item.added","output_index":2,"item":{"type":"custom_tool_call","id":"custom","call_id":"call-custom","name":"edit","namespace":"scripts","input":""}}`),
		[]byte(`{"type":"response.custom_tool_call_input.delta","output_index":2,"item_id":"custom","delta":"draft input"}`),
		[]byte(`{"type":"response.output_item.added","output_index":4,"item":{"type":"function_call","id":"function-first","call_id":"call-first","name":"lookup","namespace":"primary","arguments":""}}`),
		[]byte(`{"type":"response.function_call_arguments.delta","output_index":4,"item_id":"function-first","delta":"{\"value\":1}"}`),
	}
	output := snapshotMetadataOutput(test, []byte(responsesSnapshotMetadataFixture))
	for position, item := range output {
		frame, err := json.Marshal(struct {
			Type        string          `json:"type"`
			OutputIndex int             `json:"output_index"`
			Item        json.RawMessage `json:"item"`
		}{Type: "response.output_item.done", OutputIndex: position, Item: item})
		if err != nil {
			test.Fatal(err)
		}
		frames = append(frames, frame)
	}
	var finalResponse map[string]json.RawMessage
	if err := json.Unmarshal([]byte(responsesSnapshotMetadataFixture), &finalResponse); err != nil {
		test.Fatal(err)
	}
	if !fullTerminal {
		delete(finalResponse, "output")
	}
	terminal, err := json.Marshal(struct {
		Type     string                     `json:"type"`
		Response map[string]json.RawMessage `json:"response"`
	}{Type: "response.completed", Response: finalResponse})
	if err != nil {
		test.Fatal(err)
	}
	return append(frames, terminal)
}

func assertResponsesSnapshotMetadata(test *testing.T, response *model.InternalLLMResponse) {
	test.Helper()
	if response == nil || response.Error != nil || response.Status != "completed" || len(response.Choices) != 1 || response.Usage == nil || response.Usage.TotalTokens != 9 {
		test.Fatalf("snapshot response metadata lost: %+v", response)
	}
	assertResponsesSnapshotMessage(test, response.Choices[0].Message)
	var rawOutput []json.RawMessage
	if err := json.Unmarshal(response.RawResponsesOutputItems, &rawOutput); err != nil {
		test.Fatal(err)
	}
	assertResponsesSnapshotNativeOutput(test, rawOutput)
	if response.ProviderExtensions == nil || response.ProviderExtensions.OpenAIResponses == nil {
		test.Fatal("native output extension missing")
	}
	items := response.ProviderExtensions.OpenAIResponses.Items
	if len(items) != len(rawOutput) {
		test.Fatalf("native item count changed: %d", len(items))
	}
	for position, item := range items {
		if item.Position != position || item.Format != model.APIFormatOpenAIResponse || !reflect.DeepEqual(snapshotMetadataJSON(test, item.Raw), snapshotMetadataJSON(test, rawOutput[position])) {
			test.Fatalf("native item changed at %d: %s", position, item.Raw)
		}
	}
}

func assertResponsesSnapshotMessage(test *testing.T, message *model.Message) {
	test.Helper()
	if message == nil || message.ReasoningContent == nil || *message.ReasoningContent != "plan one detail oneplan two" || message.ReasoningSignature == nil || *message.ReasoningSignature != "opaque-firstopaque-second" {
		test.Fatalf("flattened reasoning snapshot inconsistent: %+v", message)
	}
	blocks := message.ReasoningBlocks
	if len(blocks) != 2 || blocks[0].Text != "plan one detail one" || blocks[0].Signature != "opaque-first" || blocks[1].Text != "plan two" || blocks[1].Signature != "opaque-second" || blocks[0].Provider != "openai" || blocks[1].Provider != "openai" {
		test.Fatalf("reasoning item metadata lost: %+v", blocks)
	}
	parts := message.Content.MultipleContent
	if len(parts) != 2 || parts[0].Text == nil || *parts[0].Text != "corrected answer" || len(parts[0].Citations) != 1 || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/png;base64,encoded" {
		test.Fatalf("cited text or image snapshot lost: %+v", parts)
	}
	citation := parts[0].Citations[0]
	if citation.StartIndex == nil || *citation.StartIndex != 0 || citation.EndIndex == nil || *citation.EndIndex != 0 || citation.URL == nil || *citation.URL != "https://source.test" || string(citation.Fields["future"]) != "false" {
		test.Fatalf("zero citation offsets or opaque fields lost: %+v", citation)
	}
	if len(message.ToolCalls) != 3 {
		test.Fatalf("tools lost: %+v", message.ToolCalls)
	}
	wantIDs := []string{"call-custom", "call-first", "call-second"}
	wantTypes := []string{"custom", "function", "function"}
	wantArguments := []string{"final input", `{"value":3}`, `{}`}
	wantNamespaces := []string{"scripts", "primary", "secondary"}
	wantItemIDs := []string{"custom", "function-first", "function-second"}
	for index, call := range message.ToolCalls {
		if call.Index != index || call.ID != wantIDs[index] || call.Type != wantTypes[index] || call.Function.Arguments != wantArguments[index] || call.ProviderExtensions == nil || call.ProviderExtensions.OpenAIResponses == nil {
			test.Fatalf("tool order/index/final arguments changed at %d: %+v", index, call)
		}
		fields := call.ProviderExtensions.OpenAIResponses.Fields
		if string(fields["namespace"]) != `"`+wantNamespaces[index]+`"` || string(fields["item_id"]) != `"`+wantItemIDs[index]+`"` {
			test.Fatalf("tool namespace/identity lost at %d: %+v", index, fields)
		}
	}
	customFields := message.ToolCalls[0].ProviderExtensions.OpenAIResponses.Fields
	if !reflect.DeepEqual(snapshotMetadataJSON(test, customFields["raw"]), snapshotMetadataJSON(test, snapshotMetadataOutput(test, []byte(responsesSnapshotMetadataFixture))[2])) {
		test.Fatalf("opaque custom tool metadata changed: %s", customFields["raw"])
	}
}

func assertResponsesSnapshotNativeOutput(test *testing.T, output []json.RawMessage) {
	test.Helper()
	want := snapshotMetadataOutput(test, []byte(responsesSnapshotMetadataFixture))
	if len(output) != len(want) {
		test.Fatalf("native output count changed: %d", len(output))
	}
	for position, item := range output {
		if !reflect.DeepEqual(snapshotMetadataJSON(test, item), snapshotMetadataJSON(test, want[position])) {
			test.Fatalf("native output changed at %d: %s", position, item)
		}
	}
}

func snapshotMetadataOutput(test *testing.T, body []byte) []json.RawMessage {
	test.Helper()
	var response struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		test.Fatal(err)
	}
	return response.Output
}

func snapshotMetadataJSON(test *testing.T, body []byte) any {
	test.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		test.Fatal(err)
	}
	return value
}
