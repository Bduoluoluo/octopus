package transformer_test

import (
	"context"
	"io"
	"testing"

	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func BenchmarkProtocolRequest(b *testing.B) {
	fixtures := []struct {
		name string
		kind inbound.InboundType
		body string
	}{
		{"chat", inbound.InboundTypeOpenAIChat, `{"model":"fixture","messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`},
		{"responses", inbound.InboundTypeOpenAIResponse, `{"model":"fixture","instructions":"Be concise.","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"tools":[{"type":"function","name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`},
		{"anthropic", inbound.InboundTypeAnthropic, `{"model":"fixture","max_tokens":128,"system":"Be concise.","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`},
	}
	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			body := []byte(fixture.body)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				request, err := inbound.Get(fixture.kind).TransformRequest(context.Background(), body)
				if err != nil {
					b.Fatal(err)
				}
				result, err := outbound.Get(outbound.OutboundType(fixture.kind)).TransformRequest(context.Background(), request, "https://example.invalid/v1", "fixture")
				if err != nil {
					b.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, result.Body); err != nil {
					b.Fatal(err)
				}
				result.Body.Close()
			}
		})
	}
}
