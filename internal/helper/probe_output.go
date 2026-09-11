package helper

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const maxProbeOutputBytes = 8 * 1024

type probeOutput struct {
	text      strings.Builder
	truncated bool
}

func (output *probeOutput) append(text string) {
	if output.truncated {
		return
	}
	remaining := maxProbeOutputBytes - output.text.Len()
	if len(text) > remaining {
		for remaining > 0 && !utf8.RuneStart(text[remaining]) {
			remaining--
		}
		text = text[:remaining]
		output.truncated = true
	}
	output.text.WriteString(text)
}

func (output *probeOutput) String() string {
	if output.truncated {
		return output.text.String() + "\n[truncated]"
	}
	return output.text.String()
}

func ExtractProbeOutput(body []byte) string {
	var payload struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	var output probeOutput
	if json.Unmarshal(body, &payload) == nil {
		for _, content := range payload.Content {
			output.append(content.Text)
		}
		for _, choice := range payload.Choices {
			var text string
			if json.Unmarshal(choice.Message.Content, &text) == nil {
				output.append(text)
			} else {
				var parts []struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(choice.Message.Content, &parts) == nil {
					for _, part := range parts {
						output.append(part.Text)
					}
				}
			}
		}
		for _, candidate := range payload.Candidates {
			for _, part := range candidate.Content.Parts {
				output.append(part.Text)
			}
		}
	}
	if output.text.Len() == 0 {
		output.append(strings.TrimSpace(string(body)))
	}
	return output.String()
}
