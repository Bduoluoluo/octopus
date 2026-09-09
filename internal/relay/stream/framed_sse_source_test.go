package stream

import (
	"context"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestFramedSSESourcePreservesFramesAcrossReads(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		first := "event: message_start" + newline + "data: {\"type\":\"message_start\"}" + newline + newline
		second := "event: error" + newline + "data: {\"error\":\"failed\"}" + newline + newline
		source := NewFramedSSESource(io.NopCloser(iotest.OneByteReader(strings.NewReader(first+second))), 1024)
		for _, expected := range []string{first, second} {
			data, err := source.ReadEvent(context.Background())
			if err != nil || string(data) != expected {
				t.Fatalf("newline=%q: got %q, %v", newline, data, err)
			}
		}
		if _, err := source.ReadEvent(context.Background()); err != io.EOF {
			t.Fatalf("expected EOF, got %v", err)
		}
		_ = source.Close()
	}
}

func TestFramedSSESourceTrailingFrameAndLimit(t *testing.T) {
	source := NewFramedSSESource(io.NopCloser(strings.NewReader("data: last\n")), 1024)
	data, err := source.ReadEvent(context.Background())
	if err != nil || string(data) != "data: last\n" {
		t.Fatalf("trailing frame lost: %q %v", data, err)
	}
	_ = source.Close()
	source = NewFramedSSESource(io.NopCloser(strings.NewReader(strings.Repeat("x", 8192))), 4096)
	if _, err := source.ReadEvent(context.Background()); err == nil || err == io.EOF {
		t.Fatalf("expected limit error, got %v", err)
	}
	_ = source.Close()
}
