package outbound

import "testing"

func TestThreeRuntimeOutboundProtocols(t *testing.T) {
	if OutboundTypeOpenAIChat != 0 || OutboundTypeOpenAIResponse != 1 || OutboundTypeAnthropic != 2 ||
		OutboundTypeGemini != 3 || OutboundTypeVolcengine != 4 || OutboundTypeOpenAIEmbedding != 5 {
		t.Fatal("persisted outbound IDs changed")
	}
	if len(outboundFactories) != 3 {
		t.Fatalf("unexpected factories: %d", len(outboundFactories))
	}
	for _, protocol := range []OutboundType{0, 1, 2} {
		if !IsSupported(protocol) || !IsChatChannelType(protocol) || Get(protocol) == nil {
			t.Fatalf("missing supported outbound %d", protocol)
		}
	}
	for _, protocol := range []OutboundType{-1, 3, 4, 5, 99} {
		if Get(protocol) != nil || IsSupported(protocol) || IsChatChannelType(protocol) || IsEmbeddingChannelType(protocol) {
			t.Fatalf("unsupported outbound %d has runtime capability", protocol)
		}
	}
}
