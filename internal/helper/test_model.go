package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func normalizeBaseURL(baseURL, suffix string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if suffix == "" {
		return baseURL
	}
	suffix = strings.Trim(suffix, "/")
	if strings.HasSuffix(baseURL, "/"+suffix) {
		return baseURL
	}
	return baseURL + "/" + suffix
}

const testPrompt = "translate `octopus` to Spanish"

const testMaxTokens = 1024

// TestModelResult 保存上游状态码、测试耗时和错误信息；网络错误时状态码为 0。
type TestModelResult struct {
	Model      string `json:"model"`
	StatusCode int    `json:"status_code"`
	DelayMS    int64  `json:"delay_ms"`
	Error      string `json:"error,omitempty"`
}

// TestChannelModel 直接测试指定模型，不经过转发、重试和计费流程。
// channel 必须包含真实密钥，支持已保存和表单中尚未保存的渠道。
func TestChannelModel(
	ctx context.Context,
	channel *model.Channel,
	modelName string,
	keyIndex int,
	timeout time.Duration,
) (*TestModelResult, error) {
	result := &TestModelResult{Model: modelName}

	if channel == nil {
		return nil, errors.New("channel is nil")
	}
	if modelName == "" {
		return nil, errors.New("model is empty")
	}
	if channel.Type == outbound.OutboundTypeOpenAIEmbedding {
		return nil, fmt.Errorf("channel type %d does not support model testing", channel.Type)
	}
	baseURL := channel.GetBaseUrl()
	if baseURL == "" {
		return nil, errors.New("no valid base url")
	}
	if keyIndex < 0 || keyIndex >= len(channel.Keys) {
		return nil, fmt.Errorf("key_index %d out of range (have %d keys)", keyIndex, len(channel.Keys))
	}
	key := strings.TrimSpace(channel.Keys[keyIndex].ChannelKey)
	if key == "" {
		return nil, errors.New("selected key is empty")
	}
	if timeout <= 0 {
		timeout = DefaultModelTestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := buildTestRequest(ctx, channel.Type, baseURL, key, modelName)
	if err != nil {
		return nil, err
	}
	// 自定义请求头优先于默认值，与渠道配置保持一致。
	for _, header := range channel.CustomHeader {
		if name := strings.TrimSpace(header.HeaderKey); name != "" {
			req.Header.Set(name, header.HeaderValue)
		}
	}

	httpClient, err := ChannelHTTPClientWithContext(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("build http client: %w", err)
	}

	start := time.Now()
	resp, err := httpClient.Do(req)
	result.DelayMS = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = truncateErr(err.Error(), 200)
		return result, nil
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	if UsesResponsesProbe(channel.Type) {
		if err := ValidateResponsesProbe(resp); err != nil {
			result.Error = truncateErr(err.Error(), 200)
		}
		result.DelayMS = time.Since(start).Milliseconds()
		return result, nil
	}

	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if readErr != nil {
		result.Error = truncateErr(fmt.Sprintf("read body: %v", readErr), 200)
		return result, nil
	}

	if resp.StatusCode >= 400 {
		result.Error = summarizeUpstreamError(resp.StatusCode, bodyBytes)
	}
	return result, nil
}

func buildTestRequest(
	ctx context.Context,
	channelType outbound.OutboundType,
	baseURL, key, modelName string,
) (*http.Request, error) {
	switch channelType {
	case outbound.OutboundTypeOpenAIChat, outbound.OutboundTypeOpenAIResponse:
		return BuildResponsesProbeRequest(ctx, baseURL, key, modelName)

	case outbound.OutboundTypeAnthropic:
		url := normalizeBaseURL(baseURL, "v1") + "/messages"
		body := map[string]any{
			"model": modelName,
			"messages": []map[string]string{
				{"role": "user", "content": testPrompt},
			},
			"max_tokens": testMaxTokens,
		}
		return newJSONRequest(ctx, http.MethodPost, url, body, func(r *http.Request) {
			r.Header.Set("X-Api-Key", key)
			r.Header.Set("Anthropic-Version", "2023-06-01")
		})

	case outbound.OutboundTypeGemini:
		var url string
		if strings.HasSuffix(strings.TrimRight(baseURL, "/"), "/v1") {
			url = normalizeBaseURL(baseURL, "") + "/models/" + modelName + ":generateContent"
		} else {
			url = normalizeBaseURL(baseURL, "v1beta") + "/models/" + modelName + ":generateContent"
		}
		body := map[string]any{
			"contents": []map[string]any{
				{"parts": []map[string]string{{"text": testPrompt}}},
			},
			"generationConfig": map[string]any{
				"maxOutputTokens": testMaxTokens,
			},
		}
		return newJSONRequest(ctx, http.MethodPost, url, body, func(r *http.Request) {
			r.Header.Set("X-Goog-Api-Key", key)
		})

	case outbound.OutboundTypeVolcengine:
		url := normalizeBaseURL(baseURL, "v3") + "/chat/completions"
		body := map[string]any{
			"model": modelName,
			"messages": []map[string]string{
				{"role": "user", "content": testPrompt},
			},
			"max_tokens": testMaxTokens,
		}
		return newJSONRequest(ctx, http.MethodPost, url, body, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+key)
		})

	default:
		return nil, fmt.Errorf("unsupported channel type: %d", channelType)
	}
}

func newJSONRequest(
	ctx context.Context,
	method, url string,
	body any,
	extraHeaders func(*http.Request),
) (*http.Request, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if extraHeaders != nil {
		extraHeaders(req)
	}
	return req, nil
}

func summarizeUpstreamError(statusCode int, body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 {
		return fmt.Sprintf("upstream returned %d with empty body", statusCode)
	}
	var probe struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if jsonErr := json.Unmarshal(body, &probe); jsonErr == nil {
		msg := probe.Error.Message
		if msg == "" {
			msg = probe.Message
		}
		if msg != "" {
			return truncateErr(fmt.Sprintf("%d: %s", statusCode, msg), 200)
		}
	}
	return truncateErr(fmt.Sprintf("%d: %s", statusCode, trimmed), 200)
}

func truncateErr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
