package stream

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestProtocolLifecycleFinalizeDoesNotTransformEndTwice(t *testing.T) {
	writer := newMockStreamWriter()
	source := newMockStreamSource([][]byte{[]byte("wire")})
	transformCalls, endCalls, firstTokenCalls, finishCalls := 0, 0, 0, 0
	processor := NewStreamProcessor(StreamConfig{
		Source:  source,
		Writer:  writer,
		Context: context.Background(),
		Transform: func(context.Context, []byte) ([]byte, error) {
			transformCalls++
			return []byte("converted"), nil
		},
		OnFirstToken: func() { firstTokenCalls++ },
		OnEnd: func(context.Context) ([]byte, error) {
			endCalls++
			return []byte("terminal"), nil
		},
		OnFinish: func(context.Context, []byte) error {
			finishCalls++
			return nil
		},
	})
	if err := processor.Run(); err != nil {
		t.Fatal(err)
	}
	if transformCalls != 1 || endCalls != 1 || firstTokenCalls != 1 || finishCalls != 1 || writer.buffer.String() != "convertedterminal" || !source.closed {
		t.Fatalf("lifecycle callbacks/order changed: transform=%d end=%d first=%d finish=%d output=%q closed=%t", transformCalls, endCalls, firstTokenCalls, finishCalls, writer.buffer.String(), source.closed)
	}
}

func TestProtocolLifecycleEOFValidationPrecedesSuccess(t *testing.T) {
	for _, empty := range []bool{false, true} {
		events := [][]byte{[]byte("wire")}
		if empty {
			events = nil
		}
		writer := newMockStreamWriter()
		endCalls, finishCalls := 0, 0
		processor := NewStreamProcessor(StreamConfig{
			Source:  newMockStreamSource(events),
			Writer:  writer,
			Context: context.Background(),
			OnEnd: func(context.Context) ([]byte, error) {
				endCalls++
				return []byte("must-not-write"), io.ErrUnexpectedEOF
			},
			OnFinish: func(context.Context, []byte) error {
				finishCalls++
				return nil
			},
		})
		err := processor.Run()
		if !errors.Is(err, io.ErrUnexpectedEOF) || endCalls != 1 || finishCalls != 0 {
			t.Fatalf("EOF validated as success (empty=%t): %v end=%d finish=%d", empty, err, endCalls, finishCalls)
		}
		if empty && (!errors.Is(err, ErrEmptyUpstreamStream) || writer.buffer.Len() != 0 || processor.PayloadWritten()) {
			t.Fatalf("empty upstream became business output: %v output=%q", err, writer.buffer.String())
		}
		if !empty && writer.buffer.String() != "wire" {
			t.Fatalf("invalid end output was written: %q", writer.buffer.String())
		}
	}
}

func TestProtocolLifecycleEndCannotStartEmptyStream(t *testing.T) {
	writer := newMockStreamWriter()
	processor := NewStreamProcessor(StreamConfig{
		Source:  newMockStreamSource(nil),
		Writer:  writer,
		Context: context.Background(),
		OnEnd: func(context.Context) ([]byte, error) {
			return []byte("terminal-only"), nil
		},
	})
	if err := processor.Run(); !errors.Is(err, ErrEmptyUpstreamStream) || writer.buffer.Len() != 0 || processor.PayloadWritten() {
		t.Fatalf("EOF output changed business-start semantics: %v output=%q", err, writer.buffer.String())
	}
}

func TestProtocolLifecycleCancellationDoesNotSynthesizeEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endCalls, finishCalls := 0, 0
	processor := NewStreamProcessor(StreamConfig{
		Source:          &cancelTestSource{first: []byte("wire")},
		Writer:          newMockStreamWriter(),
		Context:         ctx,
		BufferRawStream: true,
		OnFirstToken:    cancel,
		OnEnd: func(context.Context) ([]byte, error) {
			endCalls++
			return nil, io.ErrUnexpectedEOF
		},
		OnFinish: func(_ context.Context, raw []byte) error {
			finishCalls++
			if string(raw) != "wire" {
				t.Fatalf("partial metric payload lost: %q", raw)
			}
			return nil
		},
	})
	if err := processor.Run(); !errors.Is(err, context.Canceled) || endCalls != 0 || finishCalls != 1 {
		t.Fatalf("cancel lifecycle changed: %v end=%d finish=%d", err, endCalls, finishCalls)
	}
}
