package helper

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tmaxmax/go-sse"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

const DefaultModelTestTimeout = 10 * time.Second

//go:embed probe_instructions.txt
var probeInstructions string

func UsesResponsesProbe(channelType outbound.OutboundType) bool {
	return channelType == outbound.OutboundTypeOpenAIChat || channelType == outbound.OutboundTypeOpenAIResponse
}

func BuildResponsesProbeRequest(ctx context.Context, baseURL, key, modelName string) (*http.Request, error) {
	body := map[string]any{
		"model":        modelName,
		"stream":       true,
		"instructions": probeInstructions,
		"input": []map[string]any{{
			"role":    "user",
			"content": []map[string]string{{"type": "input_text", "text": "hi"}},
		}},
	}
	return newJSONRequest(ctx, http.MethodPost, normalizeBaseURL(baseURL, "v1")+"/responses", body, func(request *http.Request) {
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Accept", "text/event-stream")
	})
}

type responsesProbePayload struct {
	Type     string                 `json:"type"`
	Status   string                 `json:"status"`
	Message  string                 `json:"message"`
	Error    json.RawMessage        `json:"error"`
	Response *responsesProbePayload `json:"response"`
	Delta    string                 `json:"delta"`
	Output   []struct {
		Content []struct {
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
}

func (payload *responsesProbePayload) failure() error {
	if payload.Response != nil {
		if err := payload.Response.failure(); err != nil {
			return err
		}
	}
	if len(payload.Error) > 0 && string(payload.Error) != "null" {
		return fmt.Errorf("upstream error: %s", truncateErr(string(payload.Error), 200))
	}
	switch payload.Type {
	case "error", "response.failed", "response.incomplete":
		return fmt.Errorf("upstream %s: %s", payload.Type, payload.Message)
	}
	switch payload.Status {
	case "failed", "incomplete", "cancelled":
		return fmt.Errorf("upstream response %s", payload.Status)
	}
	return nil
}

func (payload *responsesProbePayload) hasOutput() bool {
	if payload.Response != nil && payload.Response.hasOutput() {
		return true
	}
	for _, item := range payload.Output {
		for _, content := range item.Content {
			if strings.TrimSpace(content.Text) != "" || strings.TrimSpace(content.Refusal) != "" {
				return true
			}
		}
	}
	return false
}

func ValidateResponsesProbe(response *http.Response) error {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, err := io.ReadAll(io.LimitReader(response.Body, 32*1024))
		if err != nil {
			return fmt.Errorf("read upstream error: %w", err)
		}
		return fmt.Errorf("%s", summarizeUpstreamError(response.StatusCode, body))
	}
	if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		var payload responsesProbePayload
		if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&payload); err != nil {
			return fmt.Errorf("invalid Responses body: %w", err)
		}
		if err := payload.failure(); err != nil {
			return err
		}
		if payload.Status != "completed" || !payload.hasOutput() {
			return fmt.Errorf("upstream did not return a completed response with output")
		}
		return nil
	}
	hasOutput := false
	for event, err := range sse.Read(response.Body, &sse.ReadConfig{MaxEventSize: 1024 * 1024}) {
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				if hasOutput {
					return fmt.Errorf("Responses test timed out after receiving output, while waiting for response.completed or response.done: %w", err)
				}
				return fmt.Errorf("Responses test timed out before receiving output: %w", err)
			}
			return fmt.Errorf("read Responses stream: %w", err)
		}
		if strings.TrimSpace(event.Data) == "[DONE]" {
			break
		}
		if event.Data == "" && event.Type != "error" && event.Type != "response.failed" {
			continue
		}
		var payload responsesProbePayload
		if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
			return fmt.Errorf("invalid Responses event: %w", err)
		}
		if payload.Type == "" {
			payload.Type = event.Type
		}
		if err := payload.failure(); err != nil {
			return err
		}
		if (payload.Type == "response.output_text.delta" || payload.Type == "response.refusal.delta") && strings.TrimSpace(payload.Delta) != "" {
			hasOutput = true
		}
		hasOutput = hasOutput || payload.hasOutput()
		if payload.Type == "response.completed" || payload.Type == "response.done" {
			if !hasOutput {
				return fmt.Errorf("upstream completed without output")
			}
			return nil
		}
	}
	return fmt.Errorf("upstream stream ended before response.completed or response.done")
}
