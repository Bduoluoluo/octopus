package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tmaxmax/go-sse"
	"github.com/xuanli27/octopus/internal/relay/stream"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func upstreamPayloadError(data []byte, eventType string) error {
	var payload map[string]json.RawMessage
	if json.Unmarshal(data, &payload) != nil {
		if eventType == "error" {
			return newUpstreamResponseError(model.ErrorDetail{Message: "upstream SSE error"}, 0)
		}
		return nil
	}
	var kind, status string
	_ = json.Unmarshal(payload["type"], &kind)
	_ = json.Unmarshal(payload["status"], &status)
	failed := kind == "error" || kind == "response.failed" || eventType == "error" || eventType == "response.failed" || status == "failed"
	if nested := payload["response"]; len(nested) > 0 && (strings.HasPrefix(kind, "response.") || strings.HasPrefix(eventType, "response.")) {
		if err := upstreamPayloadError(nested, ""); err != nil {
			return err
		}
	}
	raw := bytes.TrimSpace(payload["error"])
	if len(raw) > 0 && string(raw) != "null" && string(raw) != "false" && string(raw) != "0" && string(raw) != `""` {
		failed = true
	} else {
		raw = data
	}
	if bytes.Equal(bytes.TrimSpace(payload["success"]), []byte("false")) {
		failed = true
	}
	if !failed && len(payload["message"]) > 0 && len(payload["choices"]) == 0 && len(payload["output"]) == 0 && len(payload["content"]) == 0 && len(payload["candidates"]) == 0 {
		for _, field := range []string{"code", "status", "status_code"} {
			var code int
			if json.Unmarshal(payload[field], &code) == nil && code >= 400 && code <= 599 {
				failed = true
			}
		}
	}
	if !failed {
		return nil
	}
	var detail model.ErrorDetail
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil && fields != nil {
		_ = json.Unmarshal(fields["message"], &detail.Message)
		_ = json.Unmarshal(fields["type"], &detail.Type)
		if json.Unmarshal(fields["code"], &detail.Code) != nil {
			detail.Code = string(fields["code"])
		}
	} else {
		_ = json.Unmarshal(raw, &detail.Message)
	}
	if detail.Message == "" {
		detail.Message = "upstream returned an error response"
	}
	code, _ := strconv.Atoi(detail.Code)
	if code == 0 {
		for _, field := range []string{"status", "status_code"} {
			var statusCode int
			if json.Unmarshal(payload[field], &statusCode) == nil && statusCode >= 400 && statusCode <= 599 {
				code = statusCode
			}
		}
	}
	return newUpstreamResponseError(detail, code)
}

func newUpstreamResponseError(detail model.ErrorDetail, code int) *model.ResponseError {
	message := strings.ToLower(detail.Message + " " + detail.Type + " " + detail.Code)
	if code < 400 || code > 599 {
		code = http.StatusBadGateway
		switch {
		case isUpstreamRateLimitError(message), strings.Contains(message, "resource_exhausted"):
			code = http.StatusTooManyRequests
		case strings.Contains(message, "overloaded"), strings.Contains(message, "unavailable"):
			code = http.StatusServiceUnavailable
		}
	}
	return &model.ResponseError{StatusCode: code, Detail: detail}
}

func streamEventHasContent(data []byte, eventType string) bool {
	if string(bytes.TrimSpace(data)) == "[DONE]" {
		return true
	}
	var payload struct {
		Type    string `json:"type"`
		Choices []struct {
			Delta        model.Message `json:"delta"`
			FinishReason *string       `json:"finish_reason"`
		} `json:"choices"`
		Delta        json.RawMessage           `json:"delta"`
		ContentBlock *model.StreamContentBlock `json:"content_block"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return true
	}
	if payload.Type == "" {
		payload.Type = eventType
	}
	switch payload.Type {
	case "ping", "message_start", "content_block_stop", "response.created", "response.in_progress", "response.queued", "response.content_part.added":
		return false
	case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.refusal.delta":
		var delta string
		return json.Unmarshal(payload.Delta, &delta) == nil && delta != ""
	case "content_block_delta":
		var delta struct {
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
			Signature   string `json:"signature"`
		}
		if json.Unmarshal(payload.Delta, &delta) != nil {
			return true
		}
		return delta.Text != "" || delta.Thinking != "" || delta.PartialJSON != "" || delta.Signature != ""
	case "content_block_start":
		return payload.ContentBlock != nil && (payload.ContentBlock.Type != "text" && payload.ContentBlock.Type != "thinking" || payload.ContentBlock.Text != "")
	case "response.output_item.added":
		var item struct {
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		_ = json.Unmarshal(data, &item)
		return item.Item.Type != "message" && item.Item.Type != "reasoning"
	}
	for _, choice := range payload.Choices {
		if choice.FinishReason != nil || choiceHasDeliveredContent(&model.Choice{Delta: &choice.Delta}) || choice.Delta.Refusal != "" {
			return true
		}
	}
	if len(payload.Choices) > 0 {
		return false
	}
	if payload.Type != "" {
		return true
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	return len(fields["candidates"]) > 0
}

func guardedStreamTransform(transform stream.StreamTransform, framed bool) stream.StreamTransform {
	var pending [][]byte
	pendingSize := 0
	started := false
	return func(ctx context.Context, data []byte) ([]byte, error) {
		ready := false
		if framed {
			for event, err := range sse.Read(bytes.NewReader(data), &sse.ReadConfig{MaxEventSize: maxSSEEventSize}) {
				if err != nil {
					return nil, err
				}
				if err := upstreamPayloadError([]byte(event.Data), event.Type); err != nil {
					return nil, err
				}
				ready = ready || streamEventHasContent([]byte(event.Data), event.Type)
			}
		} else {
			if err := upstreamPayloadError(data, ""); err != nil {
				return nil, err
			}
			ready = streamEventHasContent(data, "")
		}
		if !started && !ready {
			pendingSize += len(data)
			if pendingSize > 1024*1024 {
				return nil, fmt.Errorf("upstream stream preamble exceeds 1 MiB")
			}
			pending = append(pending, bytes.Clone(data))
			return nil, nil
		}
		started = true
		pending = append(pending, data)
		var output bytes.Buffer
		for _, part := range pending {
			if transform != nil {
				converted, err := transform(ctx, part)
				if err != nil {
					return nil, err
				}
				output.Write(converted)
			} else {
				output.Write(part)
			}
		}
		pending = nil
		return output.Bytes(), nil
	}
}
