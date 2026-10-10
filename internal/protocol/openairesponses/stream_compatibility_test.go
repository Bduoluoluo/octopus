package openairesponses_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/xuanli27/octopus/internal/protocol/openairesponses/inbound"
	"github.com/xuanli27/octopus/internal/protocol/openairesponses/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func TestResponsesStreamFinalSnapshotsCanCorrectDeltas(t *testing.T) {
	decoder := &outbound.ResponseOutbound{}
	encoder := &inbound.ResponseInbound{}
	frames := []string{
		`{"type":"response.created","response":{"id":"r","model":"m","status":"in_progress","output":[]}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"draft answer"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc","call_id":"call","name":"lookup","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"value\":1}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc","call_id":"call","name":"lookup","arguments":"{\"value\":2}"}}`,
		`{"type":"response.completed","response":{"id":"r","model":"m","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"corrected answer"}]},{"type":"function_call","id":"fc","call_id":"call","name":"lookup","arguments":"{\"value\":3}"}],"usage":{"input_tokens":4,"output_tokens":5,"total_tokens":9}}}`,
	}
	var aggregate model.StreamAggregator
	var streamedText, streamedArguments string
	for _, frame := range frames {
		events, err := decoder.TransformStreamEvent(context.Background(), []byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Delta != nil {
				streamedText += event.Delta.Text
				streamedArguments += event.Delta.Arguments
			}
		}
		aggregate.Add(model.InternalResponseFromStreamEvents(events))
		encoded, err := encoder.TransformStreamEvents(context.Background(), events)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(frame, `"type":"response.completed"`) && !strings.Contains(string(encoded), "corrected answer") {
			t.Fatalf("final downstream snapshot lost: %s", encoded)
		}
	}
	if _, err := decoder.EndStream(context.Background()); err != nil {
		t.Fatal(err)
	}
	if streamedText != "draft answer" || streamedArguments != `{"value":1}` {
		t.Fatalf("corrections were appended to prior deltas: %q %q", streamedText, streamedArguments)
	}
	for attempt := 0; attempt < 2; attempt++ {
		response := aggregate.Response()
		if response == nil || response.Error != nil || response.Status != "completed" || response.Usage == nil || response.Usage.TotalTokens != 9 {
			t.Fatalf("final result rejected: %+v", response)
		}
		message := response.Choices[0].Message
		if message.Content.Content == nil || *message.Content.Content != "corrected answer" || len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Arguments != `{"value":3}` {
			t.Fatalf("final aggregate must use upstream snapshot: %+v", message)
		}
		*message.Content.Content = "mutated copy"
	}
}

func TestResponsesStreamToleratesLifecycleVariants(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.done"} {
		t.Run(terminal, func(t *testing.T) {
			decoder := &outbound.ResponseOutbound{}
			encoder := &inbound.ResponseInbound{}
			for _, frame := range []string{
				`{"type":"response.output_item.added","output_index":0}`,
				`{"type":"response.content_part.added","output_index":0,"content_index":0}`,
				`{"type":"response.reasoning_summary_part.done","output_index":0}`,
				`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc","call_id":"call","name":"lookup","arguments":"{\"value\":"}}`,
				`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"1}"}`,
				`{"type":"response.output_item.done","output_index":0}`,
				`{"type":"` + terminal + `"}`,
			} {
				events, err := decoder.TransformStreamEvent(context.Background(), []byte(frame))
				if err != nil {
					t.Fatalf("variant blocked: %s: %v", frame, err)
				}
				if _, err := encoder.TransformStreamEvents(context.Background(), events); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := decoder.EndStream(context.Background()); err != nil {
				t.Fatal(err)
			}
			response, err := encoder.GetInternalResponse(context.Background())
			if err != nil || response == nil || response.Error != nil || response.Status != "completed" {
				t.Fatalf("completed result lost: %+v %v", response, err)
			}
			if len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"value":1}` {
				t.Fatalf("late arguments lost: %+v", response.Choices)
			}
		})
	}
}

func TestResponsesStreamTolerancePreservesRealFailures(t *testing.T) {
	decoder := &outbound.ResponseOutbound{}
	if _, err := decoder.TransformStreamEvent(context.Background(), []byte(`{"type":"response.output_text.delta","delta":"partial"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.EndStream(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("unconfirmed completion accepted: %v", err)
	}
	if _, err := decoder.TransformStreamEvent(context.Background(), []byte(`{"type":`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	events, err := decoder.TransformStreamEvent(context.Background(), []byte(`{"type":"response.failed","response":{"status":"failed","error":{"message":"actual failure","code":"upstream_error"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"corrected partial"}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	response := model.InternalResponseFromStreamEvents(events)
	if response == nil || response.Error == nil || response.Error.Detail.Message != "actual failure" {
		raw, _ := json.Marshal(response)
		t.Fatalf("upstream error hidden by correction: %s", raw)
	}
}
