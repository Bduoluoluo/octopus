package anthropic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tmaxmax/go-sse"

	wire "github.com/xuanli27/octopus/internal/protocol/anthropic"
	inbound "github.com/xuanli27/octopus/internal/protocol/anthropic/inbound"
	outbound "github.com/xuanli27/octopus/internal/protocol/anthropic/outbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func TestCitationsDeltaRoundTripThroughIR(t *testing.T) {
	upstream := strings.Join([]string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"claim"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"char_location","document_index":0,"start_char_index":0,"end_char_index":5,"cited_text":"claim"}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	}, "\n")
	codec := &outbound.MessageOutbound{}
	var events []model.StreamEvent
	for _, line := range strings.Split(upstream, "\n") {
		decoded, err := codec.TransformStreamEvent(context.Background(), []byte(line))
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, decoded...)
	}
	if len(events) == 0 {
		t.Fatal("expected stream events")
	}
	var citationCount int
	for _, event := range events {
		if event.Kind == model.StreamEventKindCitationDelta {
			citationCount++
		}
	}
	if citationCount != 1 {
		t.Fatalf("citation events=%d, events=%#v", citationCount, events)
	}
	inbound := &inbound.MessagesInbound{}
	encoded, err := inbound.TransformStreamEvents(context.Background(), events)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "citations_delta") {
		t.Fatalf("missing citations_delta: %s", encoded)
	}
	var event wire.StreamEvent
	if err := json.Unmarshal([]byte(`{"type":"content_block_delta","delta":{"type":"citations_delta","citation":{"type":"char_location","document_index":0}}}`), &event); err != nil {
		t.Fatal(err)
	}
	if event.Delta == nil || event.Delta.Citation == nil || event.Delta.Citation.DocumentIndex == nil || *event.Delta.Citation.DocumentIndex != 0 {
		t.Fatalf("lost zero citation position: %#v", event)
	}
}

func TestCitationsDeltaMultipleBlocksPerFrameAndAggregate(t *testing.T) {
	decoder := &outbound.MessageOutbound{}
	encoder := &inbound.MessagesInbound{}
	frames := []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"fixture","content":[]}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"char_location","document_index":0,"start_char_index":0,"end_char_index":5,"future":true}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"second"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"citations_delta","citation":{"type":"page_location","document_index":1,"start_page_number":0,"end_page_number":2}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	}
	var output bytes.Buffer
	for _, frame := range frames {
		events, err := decoder.TransformStreamEvent(context.Background(), []byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		data, err := encoder.TransformStreamEvents(context.Background(), events)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(data)
	}
	var positions []int64
	for frame, err := range sse.Read(bytes.NewReader(output.Bytes()), &sse.ReadConfig{MaxEventSize: 65536}) {
		if err != nil {
			t.Fatal(err)
		}
		var event wire.StreamEvent
		if err := json.Unmarshal([]byte(frame.Data), &event); err != nil {
			t.Fatal(err)
		}
		if event.Delta != nil && event.Delta.Citation != nil {
			positions = append(positions, *event.Index)
		}
	}
	if len(positions) != 2 || positions[0] != 0 || positions[1] != 1 {
		t.Fatalf("citation block positions=%v", positions)
	}
	aggregate, err := encoder.GetInternalResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if aggregate == nil || len(aggregate.Choices) != 1 || aggregate.Choices[0].Message == nil {
		t.Fatalf("aggregate=%#v", aggregate)
	}
	parts := aggregate.Choices[0].Message.Content.MultipleContent
	if len(parts) != 2 || parts[0].Text == nil || *parts[0].Text != "first" || parts[1].Text == nil || *parts[1].Text != "second" {
		t.Fatalf("aggregate content=%#v", parts)
	}
	if len(parts[0].Citations) != 1 || len(parts[1].Citations) != 1 || *parts[0].Citations[0].DocumentIndex != 0 || *parts[1].Citations[0].StartPageNumber != 0 {
		t.Fatalf("aggregate citations=%#v", parts)
	}
}
