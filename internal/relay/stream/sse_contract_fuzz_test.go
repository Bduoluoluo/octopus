package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tmaxmax/go-sse"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

type contractSegmentReader struct {
	data    []byte
	sizes   []byte
	segment int
}

func (reader *contractSegmentReader) Read(target []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	length := len(target)
	if len(reader.sizes) > 0 {
		length = min(length, 1+int(reader.sizes[reader.segment%len(reader.sizes)]))
		reader.segment++
	}
	count := copy(target[:length], reader.data)
	reader.data = reader.data[count:]
	return count, nil
}

func contractSSEEvents(reader io.Reader, limit int) ([]sse.Event, error) {
	var events []sse.Event
	for event, err := range sse.Read(reader, &sse.ReadConfig{MaxEventSize: limit}) {
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
	return events, nil
}

func contractSSEFrames(reader io.Reader, limit int) ([][]byte, error) {
	source := NewFramedSSESource(io.NopCloser(reader), limit)
	defer source.Close()
	var frames [][]byte
	for {
		frame, err := source.ReadEvent(context.Background())
		if err == io.EOF {
			return frames, nil
		}
		if err != nil {
			return frames, err
		}
		frames = append(frames, frame)
	}
}

func TestSSEMatureParserBoundaryContract(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		body := strings.Join([]string{
			": heartbeat", "",
			"id: zero", "event: response.output_text.delta", "data: {", "data: \"delta\":\"hello\"}", "",
			"event: ping", "",
			"id:", "event: response.completed", "data: {\"status\":\"completed\"}", "", "",
		}, newline)
		want := []sse.Event{
			{LastEventID: "zero", Type: "response.output_text.delta", Data: "{\n\"delta\":\"hello\"}"},
			{LastEventID: "zero", Type: "ping"},
			{Type: "response.completed", Data: `{"status":"completed"}`},
		}
		for _, segments := range [][]byte{nil, {0}, {1, 8, 0, 64, 255}} {
			reader := &contractSegmentReader{data: []byte(body), sizes: segments}
			got, err := contractSSEEvents(reader, 4096)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("newline=%q segments=%v: got=%+v err=%v want=%+v", newline, segments, got, err, want)
			}
			frames, err := contractSSEFrames(&contractSegmentReader{data: []byte(body), sizes: segments}, 4096)
			if err != nil || len(frames) != 4 || !bytes.Equal(bytes.Join(frames, nil), []byte(body)) {
				t.Fatalf("raw frame bytes/boundaries changed: newline=%q segments=%v frames=%q err=%v", newline, segments, frames, err)
			}
		}
	}
}

func TestSSEBadJSONIsTransportPayloadAndCodecFailure(t *testing.T) {
	for _, channelType := range []outbound.OutboundType{outbound.OutboundTypeOpenAIChat, outbound.OutboundTypeOpenAIResponse, outbound.OutboundTypeAnthropic} {
		body := "event: content_block_delta\r\ndata: {\"broken\":\r\n\r\n"
		events, err := contractSSEEvents(strings.NewReader(body), 4096)
		if err != nil || len(events) != 1 || events[0].Data != `{"broken":` {
			t.Fatalf("transport silently discarded invalid JSON: events=%+v err=%v", events, err)
		}
		adapter, ok := outbound.Get(channelType).(model.OutboundStreamFrameTransformer)
		if !ok {
			t.Fatalf("channel %d has no frame decoder", channelType)
		}
		writer := newMockStreamWriter()
		processor := NewStreamProcessor(StreamConfig{
			Source: NewFramedSSESource(io.NopCloser(&contractSegmentReader{data: []byte(body), sizes: []byte{0}}), 4096),
			Writer: writer, Context: context.Background(),
			Transform: func(ctx context.Context, data []byte) ([]byte, error) {
				for event, parseErr := range sse.Read(bytes.NewReader(data), &sse.ReadConfig{MaxEventSize: 4096}) {
					if parseErr != nil {
						return nil, parseErr
					}
					if _, decodeErr := adapter.TransformStreamFrame(ctx, model.StreamFrame{Event: event.Type, Data: []byte(event.Data), ID: event.LastEventID}); decodeErr != nil {
						return nil, decodeErr
					}
				}
				return nil, nil
			},
		})
		err = processor.Run()
		if closeErr := adapter.CloseStream(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err == nil || errors.Is(err, ErrEmptyUpstreamStream) || !strings.Contains(err.Error(), "transform error") || writer.buffer.Len() != 0 || processor.PayloadWritten() {
			t.Fatalf("invalid JSON did not propagate before business output: channel=%d err=%v bytes=%q", channelType, err, writer.buffer.Bytes())
		}
	}
}

func TestSSESizeLimitContract(t *testing.T) {
	for _, sizes := range [][]byte{nil, {0}, {255, 1}} {
		for _, oversized := range []bool{false, true} {
			count := 64
			if oversized {
				count = 8192
			}
			body := []byte("event: message\ndata: " + strings.Repeat("x", count) + "\n\n")
			_, frameErr := contractSSEFrames(&contractSegmentReader{data: body, sizes: sizes}, 1024)
			_, parserErr := contractSSEEvents(&contractSegmentReader{data: body, sizes: sizes}, 1024)
			if oversized && (frameErr == nil || parserErr == nil) || !oversized && (frameErr != nil || parserErr != nil) {
				t.Fatalf("limit contract: oversized=%t segments=%v frame=%v parser=%v", oversized, sizes, frameErr, parserErr)
			}
		}
	}
}

type contractBlockedBody struct {
	started chan struct{}
	closed  chan struct{}
	readEnd chan struct{}
	once    sync.Once
	closes  atomic.Int32
}

func (body *contractBlockedBody) Read([]byte) (int, error) {
	close(body.started)
	<-body.closed
	close(body.readEnd)
	return 0, io.ErrClosedPipe
}

func (body *contractBlockedBody) Close() error {
	body.closes.Add(1)
	body.once.Do(func() { close(body.closed) })
	return nil
}

func TestSSECancellationClosesBlockedRead(t *testing.T) {
	body := &contractBlockedBody{started: make(chan struct{}), closed: make(chan struct{}), readEnd: make(chan struct{})}
	source := NewFramedSSESource(body, 4096)
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := newMockStreamWriter()
	processor := NewStreamProcessor(StreamConfig{Source: source, Writer: writer, Context: ctx})
	result := make(chan error, 1)
	go func() { result <- processor.Run() }()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("source read did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || processor.PayloadWritten() || writer.buffer.Len() != 0 {
			t.Fatalf("cancellation changed business output/error: %v bytes=%q", err, writer.buffer.Bytes())
		}
	case <-time.After(time.Second):
		t.Fatal("processor did not exit on cancellation")
	}
	select {
	case <-body.readEnd:
	case <-time.After(time.Second):
		t.Fatal("source Close did not unblock the pending body read")
	}
	if err := source.Close(); err != nil || body.closes.Load() != 1 {
		t.Fatalf("source close is not idempotent: count=%d err=%v", body.closes.Load(), err)
	}
	if _, err := source.ReadEvent(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context was ignored before read: %v", err)
	}
}

func FuzzSSESegmentedCoalescedRead(fuzzer *testing.F) {
	for _, body := range []string{
		"data: {}\n\n",
		": heartbeat\r\n\r\nevent: ping\r\n\r\n",
		"id: zero\nevent: message\ndata: {\ndata: \"text\":\"hello\"}\n\nid:\ndata: last\n",
		"event: error\rdata: {broken\r\r",
		"data: " + strings.Repeat("x", 5000) + "\n\n",
	} {
		fuzzer.Add([]byte(body), []byte{0, 1, 7, 255}, uint16(4096))
	}
	fuzzer.Fuzz(func(t *testing.T, body, segments []byte, size uint16) {
		if len(body) > 16*1024 || len(segments) > 256 {
			t.Skip()
		}
		limit := 1024 + int(size)%8192
		want, wantErr := contractSSEEvents(bytes.NewReader(body), limit)
		got, gotErr := contractSSEEvents(&contractSegmentReader{data: body, sizes: segments}, limit)
		if (wantErr == nil) != (gotErr == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("mature parser depends on transport read sizes: got=%+v err=%v want=%+v err=%v", got, gotErr, want, wantErr)
		}
		wantFrames, wantErr := contractSSEFrames(bytes.NewReader(body), limit)
		gotFrames, gotErr := contractSSEFrames(&contractSegmentReader{data: body, sizes: segments}, limit)
		if (wantErr == nil) != (gotErr == nil) || !reflect.DeepEqual(gotFrames, wantFrames) {
			t.Fatalf("raw framing depends on read sizes: got=%q err=%v want=%q err=%v", gotFrames, gotErr, wantFrames, wantErr)
		}
		if gotErr == nil && !bytes.Equal(bytes.Join(gotFrames, nil), body) {
			t.Fatalf("raw source lost or duplicated bytes: got=%q want=%q", bytes.Join(gotFrames, nil), body)
		}
		for index, frame := range gotFrames {
			wantEvents, wantParseErr := contractSSEEvents(bytes.NewReader(wantFrames[index]), limit)
			gotEvents, gotParseErr := contractSSEEvents(&contractSegmentReader{data: frame, sizes: segments}, limit)
			if (wantParseErr == nil) != (gotParseErr == nil) || !reflect.DeepEqual(gotEvents, wantEvents) {
				t.Fatalf("frame parser changed event/data/ID under segmentation: got=%+v err=%v want=%+v err=%v", gotEvents, gotParseErr, wantEvents, wantParseErr)
			}
		}
	})
}
