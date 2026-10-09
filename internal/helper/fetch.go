package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dlclark/regexp2"
	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

const modelFetchUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"

func FetchModels(ctx context.Context, request model.Channel) ([]string, error) {
	if !outbound.IsSupported(request.Type) {
		return nil, fmt.Errorf("%w: %d", model.ErrUnsupportedChannelType, request.Type)
	}
	client, err := ChannelHTTPClientWithContext(ctx, &request)
	if err != nil {
		return nil, err
	}
	fetchModel := make([]string, 0)
	switch request.Type {
	case outbound.OutboundTypeAnthropic:
		// Issue #91: native Anthropic /models may 404 on OpenAI-compatible relays.
		fetchModel, err = fetchAnthropicModelsOrOpenAI(client, ctx, request)
	default:
		fetchModel, err = fetchOpenAIModels(client, ctx, request)
	}
	if err != nil {
		return nil, err
	}
	if request.MatchRegex != nil && *request.MatchRegex != "" {
		matchModel := make([]string, 0)
		re, err := regexp2.Compile(*request.MatchRegex, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		for _, model := range fetchModel {
			matched, err := re.MatchString(model)
			if err != nil {
				return nil, err
			}
			if matched {
				matchModel = append(matchModel, model)
			}
		}
		return matchModel, nil
	}
	return fetchModel, nil
}

// refer: https://platform.openai.com/docs/api-reference/models/list
// Issue #91: probe common model-list paths when BaseURL is a site root
// (e.g. https://example.com → try /v1/models, /api/v1/models, …).
func fetchOpenAIModels(client *http.Client, ctx context.Context, request model.Channel) ([]string, error) {
	base := request.GetBaseUrl()
	urls := openAIModelListURLs(base)
	if len(urls) == 0 {
		return nil, fmt.Errorf("empty channel base url")
	}

	var lastErr error
	for _, modelsURL := range urls {
		models, err := fetchOpenAIModelsAt(client, ctx, request, modelsURL)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", modelsURL, err)
			continue
		}
		if len(models) == 0 {
			lastErr = fmt.Errorf("%s: empty model list", modelsURL)
			continue
		}
		return models, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no model list endpoint succeeded for %s", base)
}

func fetchOpenAIModelsAt(client *http.Client, ctx context.Context, request model.Channel, modelsURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	applyDefaultModelRequestHeaders(req, request)
	req.Header.Set("Authorization", "Bearer "+request.GetChannelKey().ChannelKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result model.OpenAIModelList
	if err := decodeModelJSONResponse(resp, &result); err != nil {
		return nil, err
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		id := strings.TrimSpace(m.ID)
		if id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}

// openAIModelListURLs returns candidate GET URLs for OpenAI-compatible model lists.
func openAIModelListURLs(baseURL string) []string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	lower := strings.ToLower(baseURL)
	out := make([]string, 0, 5)
	add := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u == "" {
			return
		}
		for _, existing := range out {
			if strings.EqualFold(existing, u) {
				return
			}
		}
		out = append(out, u)
	}

	// Already points at a versioned API prefix — only append /models.
	if hasOpenAIVersionSuffix(lower) {
		add(baseURL + "/models")
		return out
	}

	// Site root (or arbitrary path): probe common layouts used by NewAPI / OneAPI / Sub2API.
	add(baseURL + "/models")
	add(baseURL + "/v1/models")
	add(baseURL + "/api/v1/models")
	add(baseURL + "/v1beta/models")
	return out
}

func hasOpenAIVersionSuffix(lowerBase string) bool {
	suffixes := []string{
		"/v1", "/v1beta", "/api/v1", "/openai/v1", "/openai/v1beta",
	}
	for _, s := range suffixes {
		if strings.HasSuffix(lowerBase, s) {
			return true
		}
	}
	return false
}

// refer: https://platform.claude.com/docs
func fetchAnthropicModels(client *http.Client, ctx context.Context, request model.Channel) ([]string, error) {
	var allModels []string
	var afterID string
	for {
		req, _ := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			request.GetBaseUrl()+"/models",
			nil,
		)
		applyDefaultModelRequestHeaders(req, request)
		req.Header.Set("X-Api-Key", request.GetChannelKey().ChannelKey)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		q := req.URL.Query()
		if afterID != "" {
			q.Set("after_id", afterID)
		}
		req.URL.RawQuery = q.Encode()

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		var result model.AnthropicModelList
		if err := decodeModelJSONResponse(resp, &result); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		for _, m := range result.Data {
			allModels = append(allModels, m.ID)
		}

		if !result.HasMore {
			break
		}

		afterID = result.LastID
	}
	if len(allModels) == 0 {
		// Many Anthropic-compatible relays only expose OpenAI-style /v1/models.
		return fetchOpenAIModels(client, ctx, request)
	}
	return allModels, nil
}

// fetchAnthropicModelsOrOpenAI tries the native Anthropic /models API first; on
// hard failure falls back to OpenAI-compatible endpoints (issue #91).
func fetchAnthropicModelsOrOpenAI(client *http.Client, ctx context.Context, request model.Channel) ([]string, error) {
	models, err := fetchAnthropicModels(client, ctx, request)
	if err == nil && len(models) > 0 {
		return models, nil
	}
	// fetchAnthropicModels already falls back to OpenAI when the native list is empty.
	// When the native request itself errors (404/HTML), try OpenAI paths explicitly.
	if err != nil {
		if openaiModels, openaiErr := fetchOpenAIModels(client, ctx, request); openaiErr == nil && len(openaiModels) > 0 {
			return openaiModels, nil
		} else if openaiErr != nil {
			return nil, fmt.Errorf("anthropic models: %v; openai fallback: %w", err, openaiErr)
		}
		return nil, err
	}
	return models, nil
}

func applyDefaultModelRequestHeaders(req *http.Request, request model.Channel) {
	if req == nil {
		return
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", modelFetchUserAgent)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/plain, */*")
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	}
	for _, header := range request.CustomHeader {
		if header.HeaderKey != "" {
			req.Header.Set(header.HeaderKey, header.HeaderValue)
		}
	}
}

func decodeModelJSONResponse(resp *http.Response, result any) error {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return formatModelHTTPError(resp.StatusCode, resp.Header.Get("Content-Type"), bodyBytes)
	}
	if len(bodyBytes) == 0 {
		return nil
	}
	if err := json.Unmarshal(bodyBytes, result); err != nil {
		if summary := extractModelHTMLResponseSummary(resp.Header.Get("Content-Type"), bodyBytes); summary != "" {
			return fmt.Errorf("decode response failed: %s", summary)
		}
		return fmt.Errorf("decode response failed: %w", err)
	}
	return nil
}

func formatModelHTTPError(statusCode int, contentType string, bodyBytes []byte) error {
	if payload, ok := parseModelErrorPayload(bodyBytes); ok {
		if message := extractModelErrorMessage(payload); message != "" {
			return fmt.Errorf("http %d: %s", statusCode, message)
		}
	}
	if summary := extractModelHTMLResponseSummary(contentType, bodyBytes); summary != "" {
		return fmt.Errorf("http %d: %s", statusCode, summary)
	}
	return fmt.Errorf("http %d: %s", statusCode, strings.TrimSpace(string(bodyBytes)))
}

func parseModelErrorPayload(bodyBytes []byte) (map[string]any, bool) {
	if len(bodyBytes) == 0 {
		return map[string]any{}, true
	}
	var payload map[string]any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func extractModelErrorMessage(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if message, ok := payload["message"].(string); ok && strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	if message, ok := payload["msg"].(string); ok && strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	if errorPayload, ok := payload["error"].(map[string]any); ok {
		if message, ok := errorPayload["message"].(string); ok && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
	}
	return ""
}

func extractModelHTMLResponseSummary(contentType string, bodyBytes []byte) string {
	body := strings.TrimSpace(string(bodyBytes))
	if body == "" {
		return ""
	}
	lowered := strings.ToLower(body)
	loweredContentType := strings.ToLower(contentType)
	if !strings.Contains(loweredContentType, "text/html") && !strings.Contains(lowered, "<html") && !strings.Contains(lowered, "<!doctype") {
		if strings.Contains(lowered, "just a moment") {
			return "Just a moment..."
		}
		return ""
	}
	if start := strings.Index(lowered, "<title>"); start >= 0 {
		start += len("<title>")
		if end := strings.Index(lowered[start:], "</title>"); end >= 0 {
			title := strings.TrimSpace(body[start : start+end])
			if pipe := strings.Index(title, "|"); pipe >= 0 {
				title = strings.TrimSpace(title[:pipe])
			}
			if title != "" {
				return title
			}
		}
	}
	if strings.Contains(lowered, "just a moment") {
		return "Just a moment..."
	}
	if strings.Contains(lowered, "cloudflare tunnel error") {
		return "Cloudflare Tunnel error"
	}
	if strings.Contains(lowered, "cloudflare") {
		return "Cloudflare challenge"
	}
	return ""
}
