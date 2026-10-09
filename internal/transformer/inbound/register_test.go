package inbound

import "testing"

func TestThreeRuntimeInboundProtocols(t *testing.T) {
	if InboundTypeOpenAIChat != 0 || InboundTypeOpenAIResponse != 1 || InboundTypeAnthropic != 2 || InboundTypeOpenAIEmbedding != 3 {
		t.Fatal("persisted inbound IDs changed")
	}
	if len(inboundFactories) != 3 {
		t.Fatalf("unexpected factories: %d", len(inboundFactories))
	}
	for _, protocol := range []InboundType{0, 1, 2} {
		first, second := Get(protocol), Get(protocol)
		if first == nil || second == nil || first == second {
			t.Fatalf("protocol %d must create isolated codecs", protocol)
		}
	}
	for _, protocol := range []InboundType{-1, 3, 4, 99} {
		if Get(protocol) != nil {
			t.Fatalf("unsupported inbound %d has a runtime codec", protocol)
		}
	}
}
