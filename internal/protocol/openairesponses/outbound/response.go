package outbound

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/samber/lo"

	wire "github.com/xuanli27/octopus/internal/protocol/openairesponses"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

// ResponseOutbound implements the Outbound interface for OpenAI Responses API.
type ResponseOutbound struct {
	frameState *responseFrameState
	// Stream state tracking
	streamID    string
	streamModel string
	outputItems map[int]ResponsesItem
	// toolCallIndexes maps a Responses output_index to a dense 0-based
	// tool_calls index: output_index counts all output items (reasoning,
	// message, function_call), so function calls may start above 0, while
	// the Chat Completions protocol expects dense 0-based tool_calls indices.
	toolCallIndexes map[int]int
}

func (o *ResponseOutbound) toolCallIndexFor(outputIndex int) int {
	if o.toolCallIndexes == nil {
		o.toolCallIndexes = make(map[int]int)
	}
	if idx, ok := o.toolCallIndexes[outputIndex]; ok {
		return idx
	}
	idx := len(o.toolCallIndexes)
	o.toolCallIndexes[outputIndex] = idx
	return idx
}

func (o *ResponseOutbound) TransformRequest(ctx context.Context, request *model.InternalLLMRequest, baseUrl, key string) (*http.Request, error) {
	if request == nil {
		return nil, fmt.Errorf("request is nil")
	}

	request = model.CloneRequest(request)
	request.NormalizeMessages()
	if err := validateResponsesRequest(request); err != nil {
		return nil, err
	}
	o.ResetStream()

	// Convert to Responses API request format
	responsesReq := ConvertToResponsesRequest(request)

	body, err := json.Marshal(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal responses api request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	applyOpenAIOrgProjectHeaders(req, request)

	// Parse and set URL
	parsedUrl, err := url.Parse(strings.TrimSuffix(baseUrl, "/"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse base url: %w", err)
	}
	parsedUrl.Path = parsedUrl.Path + "/responses"
	req.URL = parsedUrl
	req.Method = http.MethodPost

	return req, nil
}

// TransformRequestRaw keeps the original OpenAI Responses payload intact and only rewrites
// the top-level model to the selected upstream model.
func (o *ResponseOutbound) TransformRequestRaw(ctx context.Context, rawBody []byte, modelName, baseUrl, key string, query url.Values) (*http.Request, error) {
	if len(rawBody) == 0 {
		return nil, fmt.Errorf("raw body is empty")
	}
	if strings.TrimSpace(modelName) != "" {
		rewrittenBody, err := rewriteRawResponsesRequestModel(rawBody, modelName)
		if err != nil {
			return nil, err
		}
		rawBody = rewrittenBody
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "", bytes.NewReader(rawBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.ContentLength = int64(len(rawBody))
	bodyBytes := rawBody
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	// OpenAI-Organization / OpenAI-Project are forwarded by the relay's
	// copyHeaders on the raw-passthrough path, so no explicit application
	// is needed here (O-M7).

	parsedURL, err := url.Parse(strings.TrimSuffix(baseUrl, "/"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse base url: %w", err)
	}
	parsedURL.Path = parsedURL.Path + "/responses"
	if query != nil {
		parsedURL.RawQuery = query.Encode()
	}
	req.URL = parsedURL
	req.Method = http.MethodPost

	return req, nil
}

func rewriteRawResponsesRequestModel(rawBody []byte, modelName string) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return nil, fmt.Errorf("failed to decode raw responses request: %w", err)
	}
	payload["model"] = strings.TrimSpace(modelName)
	rewrittenBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode raw responses request: %w", err)
	}
	return rewrittenBody, nil
}

func (o *ResponseOutbound) TransformResponse(ctx context.Context, response *http.Response) (*model.InternalLLMResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("response is nil")
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("response body is empty")
	}

	// Check for error response
	if response.StatusCode >= 400 {
		var errResp struct {
			Error model.ErrorDetail `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
			return nil, &model.ResponseError{
				StatusCode: response.StatusCode,
				Detail:     errResp.Error,
			}
		}
		return nil, fmt.Errorf("HTTP error %d: %s", response.StatusCode, string(body))
	}

	var resp ResponsesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal responses api response: %w", err)
	}

	// Convert to internal response
	return convertToLLMResponseFromResponses(&resp), nil
}

func (o *ResponseOutbound) TransformStreamEvent(ctx context.Context, eventData []byte) ([]model.StreamEvent, error) {
	return o.TransformStreamFrame(ctx, model.StreamFrame{Data: eventData})
}

func (o *ResponseOutbound) TransformStream(ctx context.Context, eventData []byte) (*model.InternalLLMResponse, error) {
	events, err := o.TransformStreamEvent(ctx, eventData)
	if err != nil {
		return nil, err
	}
	return model.InternalResponseFromStreamEvents(events), nil
}

type ResponsesRequest = wire.Request
type ResponsesInput = wire.Input
type ResponsesItem = wire.Item
type ResponsesInputAudio = wire.InputAudio
type ResponsesReasoningSummary = wire.ReasoningSummary
type ResponsesAnnotation = wire.Annotation
type ResponsesTool = wire.Tool
type ResponsesToolChoice = wire.ToolChoice
type ResponsesTextOptions = wire.TextOptions
type ResponsesTextFormat = wire.TextFormat
type ResponsesReasoning = wire.Reasoning
type ResponsesResponse = wire.Response
type ResponsesUsage = wire.Usage
type ResponsesError = wire.Error
type ResponsesStreamEvent = wire.StreamEvent

const anthropicPromptCacheRetention24h = "24h"

func anthropicCacheMetadataForResponses(req *model.InternalLLMRequest) (*string, *string) {
	if req == nil {
		return nil, nil
	}
	if req.ResponsesPromptCacheKey != nil || req.PromptCacheRetention != nil {
		return req.ResponsesPromptCacheKey, req.PromptCacheRetention
	}

	return derivedAnthropicCacheMetadata(req)
}

func derivedAnthropicCacheMetadata(req *model.InternalLLMRequest) (*string, *string) {
	projection := deriveAnthropicCacheProjection(req)
	if !projection.ok {
		return nil, nil
	}

	sum := sha256.Sum256(projection.payload)
	key := "anthropic-cache-" + hex.EncodeToString(sum[:])
	return &key, projection.retention
}

func DerivedAnthropicCacheMetadata(req *model.InternalLLMRequest) (*string, *string) {
	return derivedAnthropicCacheMetadata(req)
}

func buildAnthropicCacheKeyPayload(req *model.InternalLLMRequest) []byte {
	projection := buildAnthropicCacheProjection(req)
	if !projection.ok {
		return nil
	}
	return projection.payload
}

type anthropicCacheProjection struct {
	payload     []byte
	retention   *string
	anchor      string
	ok          bool
	cacheSignal bool
}

type anthropicCacheTool struct {
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	TTL         string          `json:"ttl,omitempty"`
}

type anthropicCachePart struct {
	Type    string          `json:"type,omitempty"`
	Text    string          `json:"text,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	TTL     string          `json:"ttl,omitempty"`
}

type anthropicCacheMessage struct {
	Role  string               `json:"role,omitempty"`
	Parts []anthropicCachePart `json:"parts,omitempty"`
}

type anthropicCachePayload struct {
	Instructions string                  `json:"instructions,omitempty"`
	Tools        []anthropicCacheTool    `json:"tools,omitempty"`
	Messages     []anthropicCacheMessage `json:"messages,omitempty"`
}

func deriveAnthropicCacheProjection(req *model.InternalLLMRequest) anthropicCacheProjection {
	return buildAnthropicCacheProjection(req)
}

func buildAnthropicCacheProjection(req *model.InternalLLMRequest) anthropicCacheProjection {
	if req == nil {
		return anthropicCacheProjection{}
	}

	projection := anthropicCacheProjection{cacheSignal: requestHasCacheControl(req)}
	if !projection.cacheSignal && req.RawAPIFormat != model.APIFormatAnthropicMessage {
		return projection
	}

	payload := anthropicCachePayload{
		Instructions: strings.TrimSpace(convertInstructionsFromMessages(req.Messages)),
	}
	selectedTTL := ""

	for _, tool := range req.Tools {
		if tool.Type != "function" {
			continue
		}
		payload.Tools = append(payload.Tools, anthropicCacheTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  cloneRawJSON(tool.Function.Parameters),
			Strict:      tool.Function.Strict,
			TTL:         cacheControlTTL(tool.CacheControl),
		})
		if selectedTTL == "" {
			selectedTTL = cacheControlTTL(tool.CacheControl)
		}
	}

	stableMessageLimit, messageAnchor, messageTTL := stableAnthropicMessageLimit(req.Messages)
	if payload.Instructions != "" {
		projection.anchor = "system"
		if selectedTTL == "" {
			selectedTTL = stableSystemCacheTTL(req.Messages)
		}
	}
	if len(payload.Tools) > 0 {
		projection.anchor = "tools"
	}
	if stableMessageLimit >= 0 {
		if projection.anchor == "" || projection.anchor == "system" {
			projection.anchor = messageAnchor
		}
		if selectedTTL == "" {
			selectedTTL = messageTTL
		}
		for i := 0; i <= stableMessageLimit && i < len(req.Messages); i++ {
			msg := req.Messages[i]
			if msg.Role == "system" || msg.Role == "developer" {
				continue
			}
			if cacheMsg, ok := cacheMessageFromMessage(msg); ok {
				payload.Messages = append(payload.Messages, cacheMsg)
			}
		}
	}

	if payload.Instructions == "" && len(payload.Tools) == 0 && len(payload.Messages) == 0 {
		projection.anchor = "none"
		return projection
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return projection
	}
	projection.payload = data
	projection.ok = true
	if selectedTTL == model.CacheTTL1h {
		value := anthropicPromptCacheRetention24h
		projection.retention = &value
	}
	if projection.anchor == "" {
		projection.anchor = "unknown"
	}
	return projection
}

func stableAnthropicMessageLimit(messages []model.Message) (int, string, string) {
	userIndexes := make([]int, 0, len(messages))
	for i, msg := range messages {
		if msg.Role == "user" {
			userIndexes = append(userIndexes, i)
		}
	}
	if len(userIndexes) >= 2 {
		idx := userIndexes[len(userIndexes)-2]
		return idx, "previous_user", messageCacheTTL(messages[idx])
	}
	if len(userIndexes) == 1 {
		idx := userIndexes[0]
		return idx, "final_user_fallback", messageCacheTTL(messages[idx])
	}
	return -1, "", ""
}

func cacheMessageFromMessage(msg model.Message) (anthropicCacheMessage, bool) {
	cacheMessage := anthropicCacheMessage{Role: msg.Role}
	if msg.Content.Content != nil {
		cacheMessage.Parts = append(cacheMessage.Parts, anthropicCachePart{Type: "text", Text: strings.TrimSpace(*msg.Content.Content), TTL: cacheControlTTL(msg.CacheControl)})
	}
	for _, part := range msg.Content.MultipleContent {
		cachePart := anthropicCachePart{Type: part.Type, TTL: cacheControlTTL(part.CacheControl)}
		switch part.Type {
		case "text":
			if part.Text != nil {
				cachePart.Text = strings.TrimSpace(*part.Text)
			}
		case "server_tool_use":
			if part.ServerToolUse != nil {
				cachePart.Name = part.ServerToolUse.Name
				cachePart.Input = cloneRawJSON(part.ServerToolUse.Input)
			}
		case "server_tool_result":
			if part.ServerToolResult != nil {
				cachePart.IsError = part.ServerToolResult.IsError != nil && *part.ServerToolResult.IsError
				cachePart.Content = cloneRawJSON(part.ServerToolResult.Content)
			}
		default:
			continue
		}
		cacheMessage.Parts = append(cacheMessage.Parts, cachePart)
	}
	if msg.ToolCallID != nil && msg.Content.Content != nil {
		cacheMessage.Parts = append(cacheMessage.Parts, anthropicCachePart{Type: "tool_result", Text: strings.TrimSpace(*msg.Content.Content), TTL: cacheControlTTL(msg.CacheControl)})
	}
	for _, toolCall := range msg.ToolCalls {
		cacheMessage.Parts = append(cacheMessage.Parts, anthropicCachePart{Type: "tool_use", Name: toolCall.Function.Name, Text: strings.TrimSpace(toolCall.Function.Arguments), TTL: cacheControlTTL(toolCall.CacheControl)})
	}
	return cacheMessage, len(cacheMessage.Parts) > 0
}

func requestHasCacheControl(req *model.InternalLLMRequest) bool {
	if req == nil {
		return false
	}
	for _, msg := range req.Messages {
		if msg.CacheControl != nil {
			return true
		}
		for _, part := range msg.Content.MultipleContent {
			if part.CacheControl != nil {
				return true
			}
		}
		for _, toolCall := range msg.ToolCalls {
			if toolCall.CacheControl != nil {
				return true
			}
		}
	}
	for _, tool := range req.Tools {
		if tool.CacheControl != nil {
			return true
		}
	}
	return false
}

func cacheControlTTL(cacheControl *model.CacheControl) string {
	if cacheControl == nil {
		return ""
	}
	return cacheControl.TTL
}

func stableSystemCacheTTL(messages []model.Message) string {
	for _, msg := range messages {
		if msg.Role != "system" && msg.Role != "developer" {
			continue
		}
		if ttl := cacheControlTTL(msg.CacheControl); ttl != "" {
			return ttl
		}
		for _, part := range msg.Content.MultipleContent {
			if ttl := cacheControlTTL(part.CacheControl); ttl != "" {
				return ttl
			}
		}
	}
	return ""
}

func messageCacheTTL(msg model.Message) string {
	if ttl := cacheControlTTL(msg.CacheControl); ttl != "" {
		return ttl
	}
	for _, part := range msg.Content.MultipleContent {
		if ttl := cacheControlTTL(part.CacheControl); ttl != "" {
			return ttl
		}
	}
	for _, toolCall := range msg.ToolCalls {
		if ttl := cacheControlTTL(toolCall.CacheControl); ttl != "" {
			return ttl
		}
	}
	return ""
}

func anthropicCacheTTLPresent(req *model.InternalLLMRequest, ttl string) bool {
	if req == nil || ttl == "" {
		return false
	}
	for _, msg := range req.Messages {
		if msg.CacheControl != nil && msg.CacheControl.TTL == ttl {
			return true
		}
		for _, part := range msg.Content.MultipleContent {
			if part.CacheControl != nil && part.CacheControl.TTL == ttl {
				return true
			}
		}
		for _, toolCall := range msg.ToolCalls {
			if toolCall.CacheControl != nil && toolCall.CacheControl.TTL == ttl {
				return true
			}
		}
	}
	for _, tool := range req.Tools {
		if tool.CacheControl != nil && tool.CacheControl.TTL == ttl {
			return true
		}
	}
	return false
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func ConvertToResponsesRequest(req *model.InternalLLMRequest) *ResponsesRequest {
	if req == nil {
		return nil
	}
	req = model.CloneRequest(req)
	if err := validateResponsesRequest(req); err != nil {
		return &ResponsesRequest{EncodeError: err}
	}
	// `user` is deprecated on OpenAI text APIs and is rejected by some
	// OpenAI-compatible upstreams, so keep the modern identifiers
	// (`prompt_cache_key` / `safety_identifier`) and omit the legacy field.
	promptCacheKey, promptCacheRetention := anthropicCacheMetadataForResponses(req)
	responsesOptions := req.GetOpenAIResponsesOptions()
	result := &ResponsesRequest{
		Model:                req.Model,
		Temperature:          req.Temperature,
		TopP:                 req.TopP,
		Stream:               req.Stream,
		Store:                req.Store,
		ServiceTier:          req.ServiceTier,
		Truncation:           req.Truncation,
		Metadata:             req.Metadata,
		MaxOutputTokens:      req.MaxCompletionTokens,
		ParallelToolCalls:    req.ParallelToolCalls,
		PromptCacheKey:       promptCacheKey,
		PromptCacheRetention: promptCacheRetention,
	}

	// Convert instructions from system messages
	result.Instructions = convertInstructionsFromMessages(req.Messages)

	// Convert input from messages or preserve original array items when available.
	result.Input = buildResponsesInput(req)

	// Convert tools
	if len(req.Tools) > 0 {
		result.Tools = convertToolsToResponses(req.Tools)
	}

	// Convert tool choice
	if req.ToolChoice != nil {
		result.ToolChoice = convertToolChoiceToResponses(req.ToolChoice)
	}

	// Convert text options
	if req.ResponseFormat != nil {
		format := &ResponsesTextFormat{
			Type:        req.ResponseFormat.Type,
			Name:        req.ResponseFormat.Name,
			Description: req.ResponseFormat.Description,
			Strict:      req.ResponseFormat.Strict,
		}
		// Prefer the parsed Schema so nested fields survive round-trips;
		// fall back to RawSchema (passthrough) then the legacy JSONSchema
		// blob for callers that never populated the new fields.
		if req.ResponseFormat.Schema != nil {
			if b, err := req.ResponseFormat.Schema.ToOpenAIResponseFormat(); err == nil {
				format.Schema = b
			}
		}
		if len(format.Schema) == 0 && len(req.ResponseFormat.RawSchema) > 0 {
			format.Schema = req.ResponseFormat.RawSchema
		}
		if len(format.Schema) == 0 && len(req.ResponseFormat.JSONSchema) > 0 {
			format.Schema = req.ResponseFormat.JSONSchema
		}
		result.Text = &ResponsesTextOptions{Format: format}
	}

	// Verbosity (O-M8) is a sibling of format on Responses text. Attach it
	// even when ResponseFormat is nil — the gpt-5 verbosity knob can be
	// used with plain-text output too.
	if req.Verbosity != nil && strings.TrimSpace(*req.Verbosity) != "" {
		if result.Text == nil {
			result.Text = &ResponsesTextOptions{}
		}
		result.Text.Verbosity = req.Verbosity
	}

	// Convert reasoning
	if req.ReasoningEffort != "" || req.ReasoningBudget != nil || responsesOptions.ReasoningSummary != nil || responsesOptions.ReasoningGenerateSummary != nil {
		result.Reasoning = &ResponsesReasoning{
			Effort:          req.ReasoningEffort,
			MaxTokens:       req.ReasoningBudget,
			Summary:         responsesOptions.ReasoningSummary,
			GenerateSummary: responsesOptions.ReasoningGenerateSummary,
		}
	}

	// Pass-through fields
	result.PreviousResponseID = responsesOptions.PreviousResponseID
	result.Background = responsesOptions.Background
	result.Prompt = responsesOptions.Prompt
	if result.PromptCacheKey == nil {
		result.PromptCacheKey = responsesOptions.PromptCacheKey
	}
	if result.PromptCacheRetention == nil {
		result.PromptCacheRetention = responsesOptions.PromptCacheRetention
	}
	result.SafetyIdentifier = responsesOptions.SafetyIdentifier
	result.MaxToolCalls = responsesOptions.MaxToolCalls
	result.Conversation = responsesOptions.Conversation
	result.ContextManagement = responsesOptions.ContextManagement
	result.StreamOptions = responsesOptions.StreamOptions
	result.Include = req.Include
	result.TopLogprobs = req.TopLogprobs

	result.EncodeError = attachResponseRequestFields(result, req)
	return result
}

func buildResponsesInput(req *model.InternalLLMRequest) ResponsesInput {
	if req == nil {
		return ResponsesInput{}
	}
	// RawInputItems is the authoritative runtime source for Responses requests,
	// especially after websocket replay mutates it in-place for exact replay.
	if rawInputItems := req.OpenAIRawInputItems(); len(rawInputItems) > 0 {
		return ResponsesInput{Raw: sanitizeResponsesRawItems(rawInputItems)}
	}
	openaiExt := req.GetOpenAIExtensions()
	if len(openaiExt.RawResponseItems) > 0 {
		return ResponsesInput{Raw: sanitizeResponsesRawItems(append(json.RawMessage(nil), openaiExt.RawResponseItems...))}
	}
	return sanitizeResponsesInput(convertInputFromMessages(req.Messages, req.TransformOptions))
}

func MarshalResponsesInputItems(msgs []model.Message) (json.RawMessage, error) {
	forceArray := true
	input := convertInputFromMessages(msgs, model.TransformOptions{ArrayInputs: &forceArray})
	if len(input.Items) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(input.Items)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func convertInstructionsFromMessages(msgs []model.Message) string {
	var instructions []string
	for _, msg := range msgs {
		if msg.Role != "system" && msg.Role != "developer" {
			continue
		}
		if msg.Content.Content != nil {
			instructions = append(instructions, *msg.Content.Content)
		}
		if len(msg.Content.MultipleContent) > 0 {
			var sb strings.Builder
			for _, p := range msg.Content.MultipleContent {
				if p.Type == "text" && p.Text != nil {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(*p.Text)
				}
			}
			if sb.Len() > 0 {
				instructions = append(instructions, sb.String())
			}
		}
	}
	return strings.Join(instructions, "\n")
}

func convertInputFromMessages(msgs []model.Message, transformOptions model.TransformOptions) ResponsesInput {
	if len(msgs) == 0 {
		return ResponsesInput{}
	}

	wasArrayFormat := transformOptions.ArrayInputs != nil && *transformOptions.ArrayInputs

	// Check for simple single user message
	nonSystemMsgs := make([]model.Message, 0)
	for _, msg := range msgs {
		if msg.Role != "system" && msg.Role != "developer" {
			nonSystemMsgs = append(nonSystemMsgs, msg)
		}
	}

	if !wasArrayFormat && len(nonSystemMsgs) == 1 && nonSystemMsgs[0].Content.Content != nil && nonSystemMsgs[0].Role == "user" {
		return ResponsesInput{Text: nonSystemMsgs[0].Content.Content}
	}

	// Build call_id -> item_id mapping for function_call_output reference
	callIDToItemID := make(map[string]string)
	var items []ResponsesItem
	for _, msg := range msgs {
		switch msg.Role {
		case "system", "developer":
			continue
		case "user":
			items = append(items, convertUserMessageToResponses(msg))
		case "assistant":
			assistantItems := convertAssistantMessageToResponses(msg)
			for _, item := range assistantItems {
				if item.Type == "function_call" && item.ID != "" && item.CallID != "" {
					callIDToItemID[item.CallID] = item.ID
				}
			}
			items = append(items, assistantItems...)
		case "tool":
			items = append(items, convertToolMessageToResponses(msg, callIDToItemID))
		}
	}

	return ResponsesInput{Items: sanitizeResponsesItems(items)}
}

func convertUserMessageToResponses(msg model.Message) ResponsesItem {
	var contentItems []ResponsesItem

	if msg.Content.Content != nil {
		contentItems = append(contentItems, ResponsesItem{
			Type: "input_text",
			Text: msg.Content.Content,
		})
	} else {
		for _, p := range msg.Content.MultipleContent {
			switch p.Type {
			case "text":
				if p.Text != nil {
					contentItems = append(contentItems, ResponsesItem{
						Type: "input_text",
						Text: p.Text,
					})
				}
			case "image_url":
				if p.ImageURL != nil {
					contentItems = append(contentItems, ResponsesItem{
						Type:     "input_image",
						ImageURL: &p.ImageURL.URL,
						Detail:   p.ImageURL.Detail,
					})
				}
			case "file":
				// O-H6: reproduce whichever file representation the
				// caller used originally.
				if p.File == nil {
					continue
				}
				item := ResponsesItem{Type: "input_file"}
				if p.File.FileID != "" {
					item.FileID = lo.ToPtr(p.File.FileID)
				}
				if p.File.FileURL != "" {
					item.FileURL = lo.ToPtr(p.File.FileURL)
				}
				if p.File.Filename != "" {
					item.Filename = lo.ToPtr(p.File.Filename)
				}
				if p.File.FileData != "" {
					item.FileData = lo.ToPtr(p.File.FileData)
				}
				if item.FileID == nil && item.FileURL == nil && item.FileData == nil {
					continue
				}
				contentItems = append(contentItems, item)
			case "input_audio":
				// O-H6: audio on Responses rides in a nested object
				// rather than a flat field.
				if p.Audio == nil {
					continue
				}
				contentItems = append(contentItems, ResponsesItem{
					Type: "input_audio",
					InputAudio: &ResponsesInputAudio{
						Data:   p.Audio.Data,
						Format: p.Audio.Format,
					},
				})
			}
		}
	}

	return ResponsesItem{
		Role:    msg.Role,
		Content: &ResponsesInput{Items: contentItems},
	}
}

func convertAssistantMessageToResponses(msg model.Message) []ResponsesItem {
	var items []ResponsesItem

	// Handle reasoning content
	if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		reasoningItem := ResponsesItem{
			Type: "reasoning",
			Summary: []ResponsesReasoningSummary{{
				Type: "summary_text",
				Text: *msg.ReasoningContent,
			}},
		}
		if msg.ReasoningSignature != nil && *msg.ReasoningSignature != "" {
			reasoningItem.EncryptedContent = msg.ReasoningSignature
		}
		items = append(items, reasoningItem)
	}

	// Handle tool calls
	for _, tc := range msg.ToolCalls {
		items = append(items, ResponsesItem{
			ID:        generateResponsesItemID(),
			Type:      "function_call",
			CallID:    tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	// Handle content
	var contentItems []ResponsesItem
	if msg.Content.Content != nil {
		contentItems = append(contentItems, ResponsesItem{
			Type: "output_text",
			Text: msg.Content.Content,
		})
	} else {
		for _, p := range msg.Content.MultipleContent {
			if p.Type == "text" && p.Text != nil {
				contentItems = append(contentItems, ResponsesItem{
					Type: "output_text",
					Text: p.Text,
				})
			}
		}
	}

	if len(contentItems) > 0 {
		items = append(items, ResponsesItem{
			Type:    "message",
			Role:    msg.Role,
			Status:  lo.ToPtr("completed"),
			Content: &ResponsesInput{Items: contentItems},
		})
	}

	return sanitizeResponsesItems(items)
}

func convertToolMessageToResponses(msg model.Message, callIDToItemID map[string]string) ResponsesItem {
	var output ResponsesInput

	if msg.Content.Content != nil {
		output.Text = msg.Content.Content
	} else if len(msg.Content.MultipleContent) > 0 {
		for _, p := range msg.Content.MultipleContent {
			if p.Type == "text" && p.Text != nil {
				output.Items = append(output.Items, ResponsesItem{
					Type: "input_text",
					Text: p.Text,
				})
			}
		}
	}

	if output.Text == nil && len(output.Items) == 0 {
		output.Text = lo.ToPtr("")
	}

	item := ResponsesItem{
		Type:   "function_call_output",
		CallID: lo.FromPtr(msg.ToolCallID),
		Output: &output,
	}

	// Set item_reference to the corresponding function_call's ID
	if msg.ToolCallID != nil {
		if itemID, ok := callIDToItemID[*msg.ToolCallID]; ok {
			item.ItemReference = lo.ToPtr(itemID)
		}
	}

	return item
}

func convertToolsToResponses(tools []model.Tool) []ResponsesTool {
	result := make([]ResponsesTool, 0, len(tools))
	for _, tool := range tools {
		switch tool.Type {
		case "function":
			rt := ResponsesTool{
				Type:        "function",
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Strict:      tool.Function.Strict,
			}
			if len(tool.Function.Parameters) > 0 {
				var params map[string]any
				decoder := json.NewDecoder(bytes.NewReader(tool.Function.Parameters))
				decoder.UseNumber()
				if err := decoder.Decode(&params); err == nil {
					rt.Parameters = params
				}
			}
			result = append(result, rt)
		case "image_generation":
			rt := ResponsesTool{
				Type: "image_generation",
			}
			if tool.ImageGeneration != nil {
				rt.Background = tool.ImageGeneration.Background
				rt.OutputFormat = tool.ImageGeneration.OutputFormat
				rt.Quality = tool.ImageGeneration.Quality
				rt.Size = tool.ImageGeneration.Size
				rt.OutputCompression = tool.ImageGeneration.OutputCompression
			}
			result = append(result, rt)
		}
	}
	return result
}

func convertToolChoiceToResponses(tc *model.ToolChoice) *ResponsesToolChoice {
	if tc == nil {
		return nil
	}

	result := &ResponsesToolChoice{}
	if tc.ToolChoice != nil {
		result.Mode = tc.ToolChoice
	} else if tc.NamedToolChoice != nil {
		result.Type = &tc.NamedToolChoice.Type
		if name := tc.NamedToolChoice.ResolvedFunctionName(); name != "" {
			n := name
			result.Name = &n
		}
	}
	return result
}

func convertToLLMResponseFromResponses(resp *ResponsesResponse) *model.InternalLLMResponse {
	if resp == nil {
		return &model.InternalLLMResponse{
			Object: "chat.completion",
		}
	}

	result := &model.InternalLLMResponse{
		ID:                 resp.ID,
		Object:             "chat.completion",
		Model:              resp.Model,
		Created:            resp.CreatedAt,
		Status:             lo.FromPtr(resp.Status),
		ProviderExtensions: &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Fields: resp.Fields}},
	}
	if resp.IncompleteDetails != nil {
		result.IncompleteDetails, _ = json.Marshal(resp.IncompleteDetails)
	}
	if len(resp.Output) > 0 {
		if rawOutput, err := json.Marshal(resp.Output); err == nil {
			result.RawResponsesOutputItems = rawOutput
		}
	}

	var (
		contentParts     []model.MessageContentPart
		textContent      strings.Builder
		refusalContent   strings.Builder
		reasoningContent strings.Builder
		toolCalls        []model.ToolCall
	)

	for _, outputItem := range resp.Output {
		switch outputItem.Type {
		case "message":
			if outputItem.Content != nil {
				for _, item := range outputItem.Content.Items {
					switch item.Type {
					case "output_text":
						if item.Text != nil {
							textContent.WriteString(*item.Text)
						}
					case "refusal":
						if item.Refusal != nil {
							refusalContent.WriteString(*item.Refusal)
						} else if item.Text != nil {
							refusalContent.WriteString(*item.Text)
						}
					}
				}
			}
		case "output_text":
			if outputItem.Text != nil {
				textContent.WriteString(*outputItem.Text)
			}
		case "function_call":
			toolCalls = append(toolCalls, model.ToolCall{
				ID:   outputItem.CallID,
				Type: "function",
				Function: model.FunctionCall{
					Name:      outputItem.Name,
					Arguments: outputItem.Arguments,
				},
			})
		case "reasoning":
			for _, summary := range outputItem.Summary {
				reasoningContent.WriteString(summary.Text)
			}
		case "image_generation_call":
			if outputItem.Result != nil && *outputItem.Result != "" {
				outputFormat := "png"
				if outputItem.OutputFormat != nil {
					outputFormat = *outputItem.OutputFormat
				}
				contentParts = append(contentParts, model.MessageContentPart{
					Type: "image_url",
					ImageURL: &model.ImageURL{
						URL: "data:image/" + outputFormat + ";base64," + *outputItem.Result,
					},
				})
			}
		}
	}

	choice := model.Choice{
		Index: 0,
		Message: &model.Message{
			Role:      "assistant",
			ToolCalls: toolCalls,
		},
	}

	// Set reasoning content if present
	if reasoningContent.Len() > 0 {
		choice.Message.ReasoningContent = lo.ToPtr(reasoningContent.String())
	}
	if refusalContent.Len() > 0 {
		choice.Message.Refusal = refusalContent.String()
	}

	// Set message content
	if textContent.Len() > 0 {
		if len(contentParts) > 0 {
			textPart := model.MessageContentPart{
				Type: "text",
				Text: lo.ToPtr(textContent.String()),
			}
			contentParts = append([]model.MessageContentPart{textPart}, contentParts...)
			choice.Message.Content = model.MessageContent{
				MultipleContent: contentParts,
			}
		} else {
			choice.Message.Content = model.MessageContent{
				Content: lo.ToPtr(textContent.String()),
			}
		}
	} else if len(contentParts) > 0 {
		choice.Message.Content = model.MessageContent{
			MultipleContent: contentParts,
		}
	}

	// Set finish reason based on status
	if len(toolCalls) > 0 {
		choice.FinishReason = lo.ToPtr("tool_calls")
	} else {
		finishReason, respErr := normalizeResponsesFinishReason(resp.Status, resp.Error)
		choice.FinishReason = finishReason
		if respErr != nil {
			result.Error = respErr
		}
	}

	result.Choices = []model.Choice{choice}
	result.Usage = convertResponsesUsage(resp.Usage)
	enrichResponseItems(result, resp)

	return result
}

func cloneResponsesInput(input *ResponsesInput) *ResponsesInput {
	if input == nil {
		return nil
	}
	cloned := *input
	cloned.Items = append([]ResponsesItem(nil), input.Items...)
	if len(input.Raw) > 0 {
		cloned.Raw = append(json.RawMessage(nil), input.Raw...)
	}
	return &cloned
}

func sanitizeResponsesInput(input ResponsesInput) ResponsesInput {
	if len(input.Raw) > 0 {
		input.Raw = sanitizeResponsesRawItems(input.Raw)
	}
	if len(input.Items) > 0 {
		input.Items = sanitizeResponsesItems(input.Items)
	}
	return input
}

func sanitizeResponsesItems(items []ResponsesItem) []ResponsesItem {
	if len(items) == 0 {
		return items
	}

	sanitized := make([]ResponsesItem, len(items))
	for i, item := range items {
		sanitized[i] = item
		ensureResponsesReasoningSummary(&sanitized[i])
		ensureResponsesRefusalShape(&sanitized[i])
	}
	return sanitized
}

func ensureResponsesRefusalShape(item *ResponsesItem) {
	if item == nil || item.Content == nil {
		return
	}
	for i := range item.Content.Items {
		contentItem := &item.Content.Items[i]
		if contentItem.Type != "refusal" || contentItem.Refusal != nil || contentItem.Text == nil {
			continue
		}
		contentItem.Refusal = contentItem.Text
		contentItem.Text = nil
	}
}

func ensureResponsesReasoningSummary(item *ResponsesItem) {
	if item == nil || item.Type != "reasoning" {
		return
	}
	if len(item.Summary) == 0 {
		item.Summary = []ResponsesReasoningSummary{{
			Type: "summary_text",
			Text: "",
		}}
		return
	}
	for i := range item.Summary {
		if item.Summary[i].Type == "" {
			item.Summary[i].Type = "summary_text"
		}
		if item.Summary[i].Text == "" {
			item.Summary[i].Text = ""
		}
	}
}

func sanitizeResponsesRawItems(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return raw
	}

	changed := false

	// Build call_id -> item_id mapping from function_call items.
	// Generate an id for any function_call that has call_id but no id,
	// so the function_call_output backfill can always resolve item_reference.
	callIDToItemID := make(map[string]string)
	for _, item := range items {
		if decodeRawString(item["type"]) == "function_call" {
			callID := decodeRawString(item["call_id"])
			if callID == "" {
				continue
			}
			itemID := decodeRawString(item["id"])
			if itemID == "" {
				itemID = generateResponsesItemID()
				if b, err := json.Marshal(itemID); err == nil {
					item["id"] = b
					changed = true
				}
			}
			if itemID != "" {
				callIDToItemID[callID] = itemID
			}
		}
	}

	for _, item := range items {
		itemType := decodeRawString(item["type"])

		// Sanitize function_call_output: add missing item_reference
		if itemType == "function_call_output" {
			refRaw, hasRef := item["item_reference"]
			refMissing := !hasRef || len(bytes.TrimSpace(refRaw)) == 0 ||
				bytes.Equal(bytes.TrimSpace(refRaw), []byte("null")) ||
				bytes.Equal(bytes.TrimSpace(refRaw), []byte(`""`))
			if refMissing {
				callID := decodeRawString(item["call_id"])
				if callID != "" {
					if itemID, ok := callIDToItemID[callID]; ok {
						if b, err := json.Marshal(itemID); err == nil {
							item["item_reference"] = b
							changed = true
						}
					}
				}
			}
		}

		if itemType != "reasoning" {
			continue
		}

		summaryRaw, ok := item["summary"]
		if !ok || len(bytes.TrimSpace(summaryRaw)) == 0 || bytes.Equal(bytes.TrimSpace(summaryRaw), []byte("null")) {
			item["summary"] = defaultRawResponsesReasoningSummary()
			changed = true
			continue
		}

		sanitizedSummary, summaryChanged, ok := sanitizeResponsesRawSummary(summaryRaw)
		if ok && summaryChanged {
			item["summary"] = sanitizedSummary
			changed = true
		}
	}

	if !changed {
		return raw
	}

	data, err := json.Marshal(items)
	if err != nil {
		return raw
	}
	return data
}

func sanitizeResponsesRawSummary(raw json.RawMessage) (json.RawMessage, bool, bool) {
	var summaryItems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &summaryItems); err != nil {
		return nil, false, false
	}
	if len(summaryItems) == 0 {
		return raw, false, true
	}

	changed := false
	for _, summary := range summaryItems {
		typeRaw, hasType := summary["type"]
		if !hasType || len(bytes.TrimSpace(typeRaw)) == 0 || bytes.Equal(bytes.TrimSpace(typeRaw), []byte("null")) {
			summary["type"] = []byte(`"summary_text"`)
			changed = true
		}
		textRaw, hasText := summary["text"]
		if !hasText || len(bytes.TrimSpace(textRaw)) == 0 || bytes.Equal(bytes.TrimSpace(textRaw), []byte("null")) {
			summary["text"] = []byte(`""`)
			changed = true
		}
	}

	if !changed {
		return raw, false, true
	}

	data, err := json.Marshal(summaryItems)
	if err != nil {
		return nil, false, false
	}
	return data, true, true
}

func defaultRawResponsesReasoningSummary() json.RawMessage {
	return json.RawMessage(`[{"type":"summary_text","text":""}]`)
}

func decodeRawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// normalizeResponsesFinishReason maps an OpenAI Responses API `status` value
// to a legal OpenAI Chat Completions finish_reason. The Chat schema enum is
// stop | length | tool_calls | content_filter | function_call; emitting
// anything else (the historical "error" value we used) is rejected outright
// by strict downstream SDKs such as OpenAI Python / Pydantic AI.
//
// When the upstream carries a ResponsesError on failure / incomplete turns
// we synthesise a ResponseError so the inbound layer can surface the
// original cause to the client via the final chunk payload. Tool-call
// driven completions are handled by the caller before this helper runs;
// content_filter / nuanced mappings (incomplete_details.reason) will land
// with O-M1.
func normalizeResponsesFinishReason(status *string, errDetail *ResponsesError) (*string, *model.ResponseError) {
	var respErr *model.ResponseError
	if errDetail != nil && (errDetail.Message != "" || errDetail.Code != "") {
		respErr = &model.ResponseError{
			Detail: model.ErrorDetail{
				Code:    errDetail.Code,
				Message: errDetail.Message,
			},
		}
	}

	if status == nil {
		return nil, respErr
	}
	switch *status {
	case "completed":
		return lo.ToPtr("stop"), nil
	case "incomplete":
		return lo.ToPtr("length"), respErr
	case "failed":
		return lo.ToPtr("stop"), respErr
	default:
		return nil, respErr
	}
}

func convertResponsesUsage(usage *ResponsesUsage) *model.Usage {
	if usage == nil {
		return nil
	}

	result := &model.Usage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.TotalTokens,
	}

	if usage.InputTokenDetails.CachedTokens > 0 || usage.InputTokenDetails.CacheWriteTokens > 0 {
		result.PromptTokensDetails = &model.PromptTokensDetails{
			CachedTokens:      usage.InputTokenDetails.CachedTokens,
			WriteCachedTokens: usage.InputTokenDetails.CacheWriteTokens,
		}
	}

	if usage.OutputTokenDetails.ReasoningTokens > 0 {
		result.CompletionTokensDetails = &model.CompletionTokensDetails{
			ReasoningTokens: usage.OutputTokenDetails.ReasoningTokens,
		}
	}

	return result
}

// CanPassthrough implements model.PassthroughCapable.
// Returns true when the inbound format is OpenAI Responses API.
func (o *ResponseOutbound) CanPassthrough(inboundFormat model.APIFormat) bool {
	return inboundFormat == model.APIFormatOpenAIResponse
}

// PassthroughConfig implements model.PassthroughCapable.
// Returns OpenAI Responses-specific passthrough settings.
func (o *ResponseOutbound) PassthroughConfig() model.PassthroughConfig {
	return model.PassthroughConfig{
		TerminalEvents: map[string]struct{}{
			"response.completed":  {},
			"response.failed":     {},
			"response.incomplete": {},
			"error":               {},
		},
		CollectMetrics: false, // OpenAI Responses uses different metrics semantics
	}
}

// generateResponsesItemID generates a unique ID for Responses API items (function_call, etc.).
// Format matches OpenAI's pattern: item_<random_base62_string>
func generateResponsesItemID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// fallback: use timestamp + counter
		return fmt.Sprintf("item_%016x%08x", time.Now().UnixNano(), itemIDCounter.Add(1))
	}
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i := range b {
		b[i] = charset[b[i]%byte(len(charset))]
	}
	return "item_" + string(b)
}

var itemIDCounter atomic.Uint64
