package transformer_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/tmaxmax/go-sse"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

type protocolPerfFixture struct {
	name   string
	kind   inbound.InboundType
	prefix string
	delta  string
	suffix string
}

const protocolPerfText = "benchmark-first-token-0123456789!"

func protocolPerfFixtures(chunks int) []protocolPerfFixture {
	text := strings.Repeat(protocolPerfText, chunks)
	return []protocolPerfFixture{
		{
			name: "chat", kind: inbound.InboundTypeOpenAIChat,
			prefix: "data: {\"id\":\"perf\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n",
			delta:  fmt.Sprintf("data: {\"id\":\"perf\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", protocolPerfText),
			suffix: "data: {\"id\":\"perf\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				fmt.Sprintf("data: {\"id\":\"perf\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\ndata: [DONE]\n\n", chunks, chunks+10),
		},
		{
			name: "responses", kind: inbound.InboundTypeOpenAIResponse,
			prefix: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"perf\",\"object\":\"response\",\"model\":\"fixture\",\"created_at\":1,\"status\":\"in_progress\",\"output\":[]}}\n\n" +
				"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_perf\",\"role\":\"assistant\",\"status\":\"in_progress\",\"content\":[]}}\n\n" +
				"event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_perf\",\"part\":{\"type\":\"output_text\",\"text\":\"\",\"annotations\":[]}}\n\n",
			delta:  fmt.Sprintf("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_perf\",\"delta\":%q}\n\n", protocolPerfText),
			suffix: fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"perf\",\"object\":\"response\",\"model\":\"fixture\",\"created_at\":1,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_perf\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":%q,\"annotations\":[]}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":%d,\"total_tokens\":%d}}}\n\ndata: [DONE]\n\n", text, chunks, chunks+10),
		},
		{
			name: "anthropic", kind: inbound.InboundTypeAnthropic,
			prefix: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"perf\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"fixture\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			delta: fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", protocolPerfText),
			suffix: "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":%d}}\n\n", chunks) +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		},
	}
}

type protocolPerfSession struct {
	decoder model.OutboundStreamEventTransformer
	encoder model.InboundStreamEventTransformer
	inbound model.Inbound
	owner   model.Outbound
}

type protocolPerfEnder interface {
	EndStream(context.Context) ([]model.StreamEvent, error)
}

func newProtocolPerfSession(kind inbound.InboundType) *protocolPerfSession {
	input := inbound.Get(kind)
	output := outbound.Get(outbound.OutboundType(kind))
	return &protocolPerfSession{
		decoder: output.(model.OutboundStreamEventTransformer),
		encoder: input.(model.InboundStreamEventTransformer),
		inbound: input,
		owner:   output,
	}
}

func (session *protocolPerfSession) push(ctx context.Context, data string) ([]byte, error) {
	events, err := session.decoder.TransformStreamEvent(ctx, []byte(data))
	if err != nil {
		return nil, err
	}
	return session.encoder.TransformStreamEvents(ctx, events)
}

func (session *protocolPerfSession) end(ctx context.Context) error {
	if ender, ok := session.owner.(protocolPerfEnder); ok {
		events, err := ender.EndStream(ctx)
		if err != nil {
			return err
		}
		if _, err := session.encoder.TransformStreamEvents(ctx, events); err != nil {
			return err
		}
	}
	return nil
}

func (session *protocolPerfSession) close() error {
	if closer, ok := session.owner.(interface{ CloseStream() error }); ok {
		return closer.CloseStream()
	}
	return nil
}

func protocolPerfRead(session *protocolPerfSession, body []byte, firstOnly bool, sample func()) error {
	ctx := context.Background()
	count := 0
	for event, err := range sse.Read(bytes.NewReader(body), &sse.ReadConfig{MaxEventSize: 32 * 1024 * 1024}) {
		if err != nil {
			return err
		}
		output, err := session.push(ctx, event.Data)
		if err != nil {
			return err
		}
		count++
		if sample != nil && count%64 == 0 {
			sample()
		}
		if firstOnly && bytes.Contains(output, []byte(protocolPerfText)) {
			return nil
		}
	}
	if firstOnly {
		return io.ErrUnexpectedEOF
	}
	return session.end(ctx)
}

func protocolPerfVerify(session *protocolPerfSession, chunks int) error {
	response, err := session.inbound.GetInternalResponse(context.Background())
	if err != nil {
		return err
	}
	if response == nil || len(response.Choices) != 1 || response.Choices[0].Message == nil {
		return fmt.Errorf("stream benchmark produced no complete message")
	}
	content := response.Choices[0].Message.Content
	var text string
	if content.Content != nil {
		text = *content.Content
	}
	for _, part := range content.MultipleContent {
		if part.Text != nil {
			text += *part.Text
		}
	}
	if text != strings.Repeat(protocolPerfText, chunks) {
		return fmt.Errorf("stream benchmark lost or duplicated text: got %d bytes, want %d", len(text), chunks*len(protocolPerfText))
	}
	if response.Usage == nil || response.Usage.CompletionTokens != int64(chunks) {
		return fmt.Errorf("stream benchmark lost usage: %+v", response.Usage)
	}
	return nil
}

func BenchmarkProtocolFirstFrame(b *testing.B) {
	for _, fixture := range protocolPerfFixtures(1) {
		b.Run(fixture.name, func(b *testing.B) {
			body := []byte(fixture.prefix + fixture.delta)
			b.ReportAllocs()
			for b.Loop() {
				session := newProtocolPerfSession(fixture.kind)
				if err := protocolPerfRead(session, body, true, nil); err != nil {
					b.Fatal(err)
				}
				if err := session.close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkProtocolStreamHeap(b *testing.B) {
	for _, chunks := range []int{256, 4096} {
		for _, fixture := range protocolPerfFixtures(chunks) {
			b.Run(fmt.Sprintf("%s/%d", fixture.name, chunks), func(b *testing.B) {
				body := []byte(fixture.prefix + strings.Repeat(fixture.delta, chunks) + fixture.suffix)
				warm := newProtocolPerfSession(fixture.kind)
				if err := protocolPerfRead(warm, body, false, nil); err != nil {
					b.Fatal(err)
				}
				if err := protocolPerfVerify(warm, chunks); err != nil {
					b.Fatal(err)
				}
				if err := warm.close(); err != nil {
					b.Fatal(err)
				}
				warm = nil
				var retainedTotal, peakTotal uint64
				b.ReportAllocs()
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					b.StopTimer()
					runtime.GC()
					var initial runtime.MemStats
					runtime.ReadMemStats(&initial)
					peak := initial.HeapAlloc
					sample := func() {
						var current runtime.MemStats
						runtime.ReadMemStats(&current)
						peak = max(peak, current.HeapAlloc)
					}
					b.StartTimer()
					session := newProtocolPerfSession(fixture.kind)
					if err := protocolPerfRead(session, body, false, sample); err != nil {
						b.Fatal(err)
					}
					sample()
					b.StopTimer()
					runtime.GC()
					var live runtime.MemStats
					runtime.ReadMemStats(&live)
					if live.HeapAlloc > initial.HeapAlloc {
						retainedTotal += live.HeapAlloc - initial.HeapAlloc
					}
					runtime.KeepAlive(session)
					if err := protocolPerfVerify(session, chunks); err != nil {
						b.Fatal(err)
					}
					sample()
					peakTotal += peak - initial.HeapAlloc
					if err := session.close(); err != nil {
						b.Fatal(err)
					}
					runtime.KeepAlive(body)
					b.StartTimer()
				}
				b.ReportMetric(float64(retainedTotal)/float64(b.N), "retained-B/op")
				b.ReportMetric(float64(peakTotal)/float64(b.N), "peak-sampled-B/op")
			})
		}
	}
}
