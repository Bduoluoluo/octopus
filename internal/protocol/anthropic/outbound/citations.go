package outbound

import "github.com/xuanli27/octopus/internal/transformer/model"

func hasToolResults(messages []model.Message) bool {
	for _, message := range messages {
		if message.Role == "tool" {
			return true
		}
	}
	return false
}

func hasCitations(parts []model.MessageContentPart) bool {
	for _, part := range parts {
		if part.Citations != nil {
			return true
		}
	}
	return false
}
