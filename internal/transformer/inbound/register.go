package inbound

import (
	"github.com/xuanli27/octopus/internal/transformer/inbound/anthropic"
	"github.com/xuanli27/octopus/internal/transformer/inbound/openai"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

type InboundType int

const (
	InboundTypeOpenAIChat      InboundType = 0
	InboundTypeOpenAIResponse  InboundType = 1
	InboundTypeAnthropic       InboundType = 2
	InboundTypeOpenAIEmbedding InboundType = 3
)

var inboundFactories = map[InboundType]func() model.Inbound{
	InboundTypeOpenAIChat:     func() model.Inbound { return &openai.ChatInbound{} },
	InboundTypeOpenAIResponse: func() model.Inbound { return &openai.ResponseInbound{} },
	InboundTypeAnthropic:      func() model.Inbound { return &anthropic.MessagesInbound{} },
}

func Get(inboundType InboundType) model.Inbound {
	if factory, ok := inboundFactories[inboundType]; ok {
		return factory()
	}
	return nil
}
