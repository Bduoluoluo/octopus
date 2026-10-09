package outbound

import (
	"github.com/xuanli27/octopus/internal/transformer/model"
	outAnthropic "github.com/xuanli27/octopus/internal/transformer/outbound/anthropic"
	"github.com/xuanli27/octopus/internal/transformer/outbound/openai"
)

type OutboundType int

const (
	OutboundTypeOpenAIChat      OutboundType = 0
	OutboundTypeOpenAIResponse  OutboundType = 1
	OutboundTypeAnthropic       OutboundType = 2
	OutboundTypeGemini          OutboundType = 3
	OutboundTypeVolcengine      OutboundType = 4
	OutboundTypeOpenAIEmbedding OutboundType = 5
)

// ChatChannelTypes 定义支持 chat 请求的 channel 类型集合
var ChatChannelTypes = map[OutboundType]bool{
	OutboundTypeOpenAIChat:     true,
	OutboundTypeOpenAIResponse: true,
	OutboundTypeAnthropic:      true,
}

// IsEmbeddingChannelType 判断 channel 类型是否支持 embedding 请求
func IsEmbeddingChannelType(channelType OutboundType) bool {
	return false
}

// IsChatChannelType 判断 channel 类型是否支持 chat 请求
func IsChatChannelType(channelType OutboundType) bool {
	return ChatChannelTypes[channelType]
}

var outboundFactories = map[OutboundType]func() model.Outbound{
	OutboundTypeOpenAIChat:     func() model.Outbound { return &openai.ChatOutbound{} },
	OutboundTypeOpenAIResponse: func() model.Outbound { return &openai.ResponseOutbound{} },
	OutboundTypeAnthropic:      func() model.Outbound { return &outAnthropic.MessageOutbound{} },
}

func Get(outboundType OutboundType) model.Outbound {
	if factory, ok := outboundFactories[outboundType]; ok {
		return factory()
	}
	return nil
}

func IsSupported(channelType OutboundType) bool {
	_, ok := outboundFactories[channelType]
	return ok
}

func IsRetired(channelType OutboundType) bool {
	return channelType == OutboundTypeGemini || channelType == OutboundTypeVolcengine || channelType == OutboundTypeOpenAIEmbedding
}
