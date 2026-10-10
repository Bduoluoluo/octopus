package inbound

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samber/lo"
	"github.com/xuanli27/octopus/internal/protocol/anthropic"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/utils/log"
	"github.com/xuanli27/octopus/internal/utils/tokenizer"
	"github.com/xuanli27/octopus/internal/utils/xurl"
)

type MessagesInbound struct {
	// Stream state tracking
	hasStarted                bool
	hasTextContentStarted     bool
	hasThinkingContentStarted bool
	hasToolContentStarted     bool
	hasFinished               bool
	messageStopped            bool
	messageID                 string
	modelName                 string
	contentIndex              int64
	stopReason                *string
	stopSequence              *string
	toolCallIndices           map[int]bool // Track which tool call indices we've seen
	inputToken                int64

	streamAggregator model.StreamAggregator
	// storedResponse stores the non-stream response
	storedResponse *model.InternalLLMResponse
	blocks         map[string]*outputBlock
	nextBlockIndex int64
	lastBlockKey   string
	pendingUsage   *model.Usage
	foreignStream  bool
}

func (i *MessagesInbound) TransformRequest(ctx context.Context, body []byte) (*model.InternalLLMRequest, error) {
	i.ResetStream()
	i.inputToken = 0
	var anthropicReq MessageRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, err
	}
	if anthropicReq.MaxTokens < 1 {
		anthropicReq.MaxTokens = 1
	}
	chatReq := &model.InternalLLMRequest{
		Model:               anthropicReq.Model,
		MaxTokens:           &anthropicReq.MaxTokens,
		Temperature:         anthropicReq.Temperature,
		TopP:                anthropicReq.TopP,
		TopK:                anthropicReq.TopK,
		Stream:              anthropicReq.Stream,
		Metadata:            map[string]string{},
		RawAPIFormat:        model.APIFormatAnthropicMessage,
		TransformerMetadata: map[string]string{},
	}
	if tier := strings.TrimSpace(anthropicReq.ServiceTier); tier != "" {
		chatReq.ServiceTier = &tier
	}
	if anthropicReq.Metadata != nil {
		if userID := strings.TrimSpace(anthropicReq.Metadata.UserID); userID != "" {
			chatReq.SetTransformerMetadataValue(model.TransformerMetadataAnthropicUserID, userID)
		}
	}
	// mcp_servers / container (A-H6): preserve the raw payload for
	// round-trip on the Anthropic→Anthropic same-protocol path. Triggers
	// the mcp-client-2025-11-20 beta header downstream (A-H7).
	if len(anthropicReq.MCPServers) > 0 || len(anthropicReq.Container) > 0 {
		chatReq.SetAnthropicExtensions(model.AnthropicExtension{
			MCPServers: anthropicReq.MCPServers,
			Container:  anthropicReq.Container,
		})
	}
	extension := chatReq.GetAnthropicExtensions()
	extension.Fields = anthropic.CopyFields(anthropicReq.Fields, "model", "messages", "max_tokens", "temperature", "top_p", "top_k", "stream", "stop_sequences", "tools", "tool_choice", "service_tier", "mcp_servers", "container")
	chatReq.SetAnthropicExtensions(extension)

	// Convert messages
	messages := make([]model.Message, 0, len(anthropicReq.Messages))

	// Add system message if present
	if anthropicReq.System != nil {
		if anthropicReq.System.Prompt != nil {
			systemContent := anthropicReq.System.Prompt
			messages = append(messages, model.Message{
				Role: "system",
				Content: model.MessageContent{
					Content: systemContent,
				},
			})
			i.inputToken += int64(tokenizer.CountTokens(*systemContent, chatReq.Model))
		} else if len(anthropicReq.System.MultiplePrompts) > 0 {
			// Mark that system was originally in array format
			chatReq.SetTransformerMetadataValue(model.TransformerMetadataAnthropicSystemArrayFormat, "true")

			for _, prompt := range anthropicReq.System.MultiplePrompts {
				msg := model.Message{
					Role: "system",
					Content: model.MessageContent{
						Content: &prompt.Text,
					},
					CacheControl: convertToLLMCacheControl(prompt.CacheControl),
				}
				i.inputToken += int64(tokenizer.CountTokens(prompt.Text, chatReq.Model))
				messages = append(messages, msg)
			}
		}
	}

	// Convert Anthropic messages to ChatCompletionMessage
	for msgIndex, msg := range anthropicReq.Messages {
		firstMessage := len(messages)
		chatMsg := model.Message{
			Role:         msg.Role,
			MessageIndex: lo.ToPtr(msgIndex),
		}

		var (
			hasContent    bool
			hasToolResult bool
		)

		// Convert content

		if msg.Content.Content != nil {
			chatMsg.Content = model.MessageContent{
				Content: msg.Content.Content,
			}
			hasContent = true
			i.inputToken += int64(tokenizer.CountTokens(*msg.Content.Content, chatReq.Model))
		} else if len(msg.Content.MultipleContent) > 0 {
			contentParts := make([]model.MessageContentPart, 0, len(msg.Content.MultipleContent))

			var (
				reasoningContent      string
				hasReasoningInContent bool
			)

			var reasoningSignature string

			for _, block := range msg.Content.MultipleContent {
				switch block.Type {
				case "thinking":

					// Keep thinking content in MultipleContent to preserve order
					thinkingText := ""
					if block.Thinking != nil && *block.Thinking != "" {
						thinkingText = *block.Thinking
						reasoningContent = thinkingText
						hasReasoningInContent = true
					}

					sig := ""
					if block.Signature != nil && *block.Signature != "" {
						sig = *block.Signature
						reasoningSignature = sig
					}

					// Preserve per-block provenance so multi-thinking-block assistant turns can
					// be replayed to Anthropic without flattening to a single signature.
					chatMsg.AppendReasoningBlock(model.ReasoningBlock{
						Kind:      model.ReasoningBlockKindThinking,
						Index:     -1,
						Text:      thinkingText,
						Signature: sig,
						Provider:  "anthropic",
					})
				case "redacted_thinking":
					if block.Data != "" {
						chatMsg.RedactedThinkingBlocks = append(chatMsg.RedactedThinkingBlocks, block.Data)
						chatMsg.AppendReasoningBlock(model.ReasoningBlock{
							Kind:     model.ReasoningBlockKindRedacted,
							Index:    -1,
							Data:     block.Data,
							Provider: "anthropic",
						})
						hasContent = true
					}
				case "text":
					contentParts = append(contentParts, model.MessageContentPart{
						Citations:    block.Citations,
						Type:         "text",
						Text:         block.Text,
						CacheControl: convertToLLMCacheControl(block.CacheControl),
					})
					i.inputToken += int64(tokenizer.CountTokens(lo.FromPtr(block.Text), chatReq.Model))
					hasContent = true
				case "image":
					if block.Source != nil {
						part := model.MessageContentPart{
							Type:         "image_url",
							CacheControl: convertToLLMCacheControl(block.CacheControl),
						}
						if block.Source.Type == "base64" {
							// Convert Anthropic image format to OpenAI format
							imageURL := fmt.Sprintf("data:%s;base64,%s", block.Source.MediaType, block.Source.Data)
							part.ImageURL = &model.ImageURL{
								URL: imageURL,
							}
						} else {
							part.ImageURL = &model.ImageURL{
								URL: block.Source.URL,
							}
						}

						contentParts = append(contentParts, part)
						hasContent = true
					}
				case "tool_result":
					hasToolResult = true
					toolMsg := model.Message{
						Role:            "tool",
						MessageIndex:    lo.ToPtr(msgIndex),
						ToolCallID:      block.ToolUseID,
						CacheControl:    convertToLLMCacheControl(block.CacheControl),
						ToolCallIsError: block.IsError,
					}

					if block.Content != nil {
						if block.Content.Content != nil {
							toolMsg.Content = model.MessageContent{
								Content: block.Content.Content,
							}
						} else if len(block.Content.MultipleContent) > 0 {
							// Handle multiple content blocks in tool_result
							// Keep as MultipleContent to preserve the original format
							toolContentParts := make([]model.MessageContentPart, 0, len(block.Content.MultipleContent))
							for position, contentBlock := range block.Content.MultipleContent {
								part, err := anthropic.ContentPart(contentBlock, position)
								if err != nil {
									return nil, err
								}
								toolContentParts = append(toolContentParts, part)
								i.inputToken += int64(tokenizer.CountTokens(lo.FromPtr(contentBlock.Text), chatReq.Model))
							}

							toolMsg.Content = model.MessageContent{
								MultipleContent: toolContentParts,
							}
						}
					}

					messages = append(messages, toolMsg)
				case "tool_use":
					toolCall := model.ToolCall{
						ID:   block.ID,
						Type: "function",
						Function: model.FunctionCall{
							Name:      lo.FromPtr(block.Name),
							Arguments: string(block.Input),
						},
						CacheControl: convertToLLMCacheControl(block.CacheControl),
					}
					chatMsg.ToolCalls = append(chatMsg.ToolCalls, toolCall)
					hasContent = true
				case "document":
					part := convertDocumentBlockToLLM(block)
					if part != nil {
						contentParts = append(contentParts, *part)
						hasContent = true
					}
				case "server_tool_use":
					contentParts = append(contentParts, model.MessageContentPart{
						Type: "server_tool_use",
						ServerToolUse: &model.ServerToolUseBlock{
							ID:    block.ID,
							Name:  lo.FromPtr(block.Name),
							Input: block.Input,
						},
						CacheControl: convertToLLMCacheControl(block.CacheControl),
					})
					hasContent = true
				case "web_search_tool_result", "code_execution_tool_result":
					result := &model.ServerToolResultBlock{
						ToolUseID: lo.FromPtr(block.ToolUseID),
						IsError:   block.IsError,
						BlockType: block.Type,
					}
					if block.Content != nil {
						if block.Content.Content != nil {
							b, _ := json.Marshal(*block.Content.Content)
							result.Content = b
						} else if len(block.Content.MultipleContent) > 0 {
							b, _ := json.Marshal(block.Content.MultipleContent)
							result.Content = b
						}
					}
					contentParts = append(contentParts, model.MessageContentPart{
						Type:             "server_tool_result",
						ServerToolResult: result,
						CacheControl:     convertToLLMCacheControl(block.CacheControl),
					})
					hasContent = true
				default:
					part, err := anthropic.ContentPart(block, len(contentParts))
					if err != nil {
						return nil, err
					}
					contentParts = append(contentParts, part)
					hasContent = true
				}
			}

			// Check if it's a simple text-only message (single text block)
			if len(contentParts) == 1 && contentParts[0].Type == "text" && len(contentParts[0].Citations) == 0 {
				// Convert single text block to simple content format for compatibility
				chatMsg.Content = model.MessageContent{
					Content: contentParts[0].Text,
				}
				// Preserve cache control at message level when simplifying
				if contentParts[0].CacheControl != nil {
					chatMsg.CacheControl = contentParts[0].CacheControl
				}

				hasContent = true
			} else if len(contentParts) > 0 {
				chatMsg.Content = model.MessageContent{
					MultipleContent: contentParts,
				}
				hasContent = true
			}

			if hasReasoningInContent || reasoningSignature != "" || len(chatMsg.ReasoningBlocks) > 0 {
				hasContent = true
			}

			// Assign reasoning content and signature if present
			if reasoningContent != "" && hasReasoningInContent {
				chatMsg.ReasoningContent = &reasoningContent
			}

			if reasoningSignature != "" {
				chatMsg.ReasoningSignature = &reasoningSignature
			}
		}

		if !hasContent && len(messages) == firstMessage {
			hasContent = true
		}

		// If this message had tool_result blocks, set MessageIndex so we can match it later
		if hasToolResult {
			chatMsg.MessageIndex = lo.ToPtr(msgIndex)
		}

		native, err := anthropic.CaptureContent(msg.Content, msg.Fields)
		if err != nil {
			return nil, fmt.Errorf("capture Anthropic message %d: %w", msgIndex, err)
		}
		if hasContent || (!hasToolResult && len(msg.Content.Raw) > 0) {
			messages = append(messages, chatMsg)
		}
		for index := firstMessage; index < len(messages); index++ {
			if messages[index].MessageIndex != nil && *messages[index].MessageIndex == msgIndex {
				messages[index].ProviderExtensions = native
			}
		}
	}

	chatReq.Messages = messages

	// Convert tools
	if len(anthropicReq.Tools) > 0 {
		tools := make([]model.Tool, 0, len(anthropicReq.Tools))
		for _, tool := range anthropicReq.Tools {
			if tool.IsServerTool() {
				// Server-side tool (web_search_*, code_execution_*, computer_*).
				// Preserve the raw spec body so the outbound path can replay
				// the wire payload verbatim; Type drives beta header selection.
				llmTool := model.Tool{
					Type: tool.Type,
					Function: model.Function{
						Name: tool.Name,
					},
					CacheControl:        convertToLLMCacheControl(tool.CacheControl),
					AnthropicServerSpec: tool.RawBody,
				}
				tools = append(tools, llmTool)
				i.inputToken += int64(tokenizer.CountTokens(tool.Name, chatReq.Model))
				continue
			}
			llmTool := model.Tool{
				Type: "function",
				Function: model.Function{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
					Strict:      tool.Strict,
				},
				CacheControl:        convertToLLMCacheControl(tool.CacheControl),
				AnthropicServerSpec: tool.RawBody,
			}
			tools = append(tools, llmTool)
			i.inputToken += int64(tokenizer.CountTokens(tool.Name, chatReq.Model))
			i.inputToken += int64(tokenizer.CountTokens(tool.Description, chatReq.Model))
			i.inputToken += int64(tokenizer.CountTokens(string(tool.InputSchema), chatReq.Model))
		}
		i.inputToken += int64(len(tools) * 3)

		chatReq.Tools = tools
	}

	// Convert tool_choice
	if anthropicReq.ToolChoice != nil {
		chatReq.ToolChoice = convertToolChoiceFromAnthropic(anthropicReq.ToolChoice)
	}

	// Convert stop sequences
	if len(anthropicReq.StopSequences) > 0 {
		if len(anthropicReq.StopSequences) == 1 {
			chatReq.Stop = &model.Stop{
				Stop: &anthropicReq.StopSequences[0],
			}
		} else {
			chatReq.Stop = &model.Stop{
				MultipleStop: anthropicReq.StopSequences,
			}
		}
	}

	// Convert thinking configuration to reasoning effort and preserve budget
	if anthropicReq.Thinking != nil {
		if anthropicReq.Thinking.Display != "" {
			chatReq.ThinkingDisplay = anthropicReq.Thinking.Display
		}
		switch anthropicReq.Thinking.Type {
		case ThinkingTypeEnabled:
			if anthropicReq.Thinking.BudgetTokens != nil {
				chatReq.ReasoningEffort = thinkingBudgetToReasoningEffort(*anthropicReq.Thinking.BudgetTokens)
				chatReq.ReasoningBudget = anthropicReq.Thinking.BudgetTokens
			} else {
				log.Warnf("thinking type is 'enabled' but budget_tokens is nil, thinking will be ignored")
			}
		case ThinkingTypeAdaptive:
			effort := EffortHigh
			if anthropicReq.OutputConfig != nil && anthropicReq.OutputConfig.Effort != "" {
				effort = anthropicReq.OutputConfig.Effort
			}
			chatReq.ReasoningEffort = effort
			chatReq.AdaptiveThinking = true
		case ThinkingTypeDisabled:
			// Explicitly disabled, nothing to do
		default:
			log.Warnf("unknown thinking type: %s", anthropicReq.Thinking.Type)
		}
	}
	return chatReq, nil
}

// convertToolChoiceFromAnthropic converts the wire-level Anthropic
// ToolChoice into the provider-agnostic internal representation. The string
// form is used for {auto,none,any} which are the simple modes; the named
// form preserves `tool + name` (Anthropic) and `disable_parallel_tool_use`
// so outbound emitters can reproduce them verbatim when the upstream is
// also Anthropic.
func convertToolChoiceFromAnthropic(src *ToolChoice) *model.ToolChoice {
	if src == nil {
		return nil
	}
	switch src.Type {
	case "auto", "none", "any":
		if src.DisableParallelToolUse == nil {
			mode := src.Type
			return &model.ToolChoice{ToolChoice: &mode}
		}
		return &model.ToolChoice{
			NamedToolChoice: &model.NamedToolChoice{
				Type:                   src.Type,
				DisableParallelToolUse: src.DisableParallelToolUse,
			},
		}
	case "tool":
		named := &model.NamedToolChoice{
			Type:                   "tool",
			DisableParallelToolUse: src.DisableParallelToolUse,
		}
		if src.Name != nil {
			name := *src.Name
			named.Name = &name
			named.Function = &model.ToolFunction{Name: name}
		}
		return &model.ToolChoice{NamedToolChoice: named}
	default:
		return nil
	}
}

// convertDocumentBlockToLLM maps an Anthropic document content block into
// an internal MessageContentPart of type "document". The wire `source`
// carries either a base64/url/text payload or a pre-chunked content array;
// Title / Context / Citations metadata is preserved verbatim.
func convertDocumentBlockToLLM(block MessageContentBlock) *model.MessageContentPart {
	if block.Source == nil {
		return nil
	}
	doc := &model.DocumentSource{
		Type:      block.Source.Type,
		MediaType: block.Source.MediaType,
		Data:      block.Source.Data,
		URL:       block.Source.URL,
		Content:   block.Source.Content,
		Title:     block.Title,
		Context:   block.Context,
	}
	// The wire shape carries text in source.data when type == "text"; split
	// it out into the dedicated Text field so converters can distinguish
	// raw text from a base64 blob.
	if doc.Type == "text" {
		doc.Text = doc.Data
		doc.Data = ""
	}
	if block.CitationConfig != nil {
		doc.Citations = &model.DocumentCitations{Enabled: lo.FromPtr(block.CitationConfig.Enabled)}
	}
	return &model.MessageContentPart{
		Type:         "document",
		Document:     doc,
		CacheControl: convertToLLMCacheControl(block.CacheControl),
	}
}

func (i *MessagesInbound) TransformResponse(ctx context.Context, response *model.InternalLLMResponse) ([]byte, error) {
	if response == nil {
		return nil, fmt.Errorf("response is nil")
	}
	if len(response.Choices) > 1 {
		return nil, fmt.Errorf("Anthropic Messages cannot represent multiple choices")
	}
	if err := anthropic.ValidateResponse(response); err != nil {
		return nil, err
	}
	// Store the response for later retrieval
	i.storedResponse = response

	resp := &Message{
		ID:    response.ID,
		Type:  "message",
		Role:  "assistant",
		Model: response.Model,
	}

	// Convert choices to content blocks
	if len(response.Choices) > 0 {
		choice := response.Choices[0]

		var message *model.Message

		if choice.Message != nil {
			message = choice.Message
		} else if choice.Delta != nil {
			message = choice.Delta
		}

		if message != nil {
			normalized := anthropic.NormalizeMessageMetadata(*message)
			message = &normalized
			var contentBlocks []MessageContentBlock

			// Prefer per-block reasoning provenance when available so multiple thinking /
			// redacted_thinking blocks from the upstream can be replayed in order. Fall back to
			// the legacy flat fields when ReasoningBlocks is empty (non-Anthropic upstream).
			if len(message.ReasoningBlocks) > 0 {
				for _, rb := range message.ReasoningBlocks {
					switch rb.Kind {
					case model.ReasoningBlockKindThinking:
						block := MessageContentBlock{Type: "thinking"}
						if rb.Text != "" {
							t := rb.Text
							block.Thinking = &t
						}
						if rb.Signature != "" {
							s := rb.Signature
							block.Signature = &s
						}
						contentBlocks = append(contentBlocks, block)
					case model.ReasoningBlockKindRedacted:
						if rb.Data != "" {
							contentBlocks = append(contentBlocks, MessageContentBlock{
								Type: "redacted_thinking",
								Data: rb.Data,
							})
						}
					}
				}
			} else {
				// Handle reasoning content (thinking) first if present
				if message.ReasoningContent != nil && *message.ReasoningContent != "" {
					thinkingBlock := MessageContentBlock{
						Type:     "thinking",
						Thinking: message.ReasoningContent,
					}
					if message.ReasoningSignature != nil && *message.ReasoningSignature != "" {
						thinkingBlock.Signature = message.ReasoningSignature
					}
					// No fallback magic string — if signature is absent (non-Anthropic upstream),
					// Signature remains nil and is omitted via omitempty.

					contentBlocks = append(contentBlocks, thinkingBlock)
				}

				// Handle redacted thinking blocks
				for _, data := range message.RedactedThinkingBlocks {
					contentBlocks = append(contentBlocks, MessageContentBlock{
						Type: "redacted_thinking",
						Data: data,
					})
				}
			}

			// Handle regular content
			if message.Content.Content != nil && *message.Content.Content != "" {
				contentBlocks = append(contentBlocks, MessageContentBlock{
					Type: "text",
					Text: message.Content.Content,
				})
			} else if len(message.Content.MultipleContent) > 0 {
				for _, part := range message.Content.MultipleContent {
					if part.Native != nil && part.Native.Format == model.APIFormatAnthropicMessage {
						var block MessageContentBlock
						if err := json.Unmarshal(part.Native.Raw, &block); err != nil {
							return nil, err
						}
						contentBlocks = append(contentBlocks, block)
						continue
					}
					switch part.Type {
					case "text":
						if part.Text != nil {
							contentBlocks = append(contentBlocks, MessageContentBlock{
								Citations: part.Citations,
								Type:      "text",
								Text:      part.Text,
							})
						}
					case "image_url":
						if part.ImageURL != nil && part.ImageURL.URL != "" {
							// Convert OpenAI image format to Anthropic format
							url := part.ImageURL.URL
							if parsed := xurl.ParseDataURL(url); parsed != nil {
								contentBlocks = append(contentBlocks, MessageContentBlock{
									Type: "image",
									Source: &ImageSource{
										Type:      "base64",
										MediaType: parsed.MediaType,
										Data:      parsed.Data,
									},
								})
							} else {
								contentBlocks = append(contentBlocks, MessageContentBlock{
									Type: "image",
									Source: &ImageSource{
										Type: "url",
										URL:  part.ImageURL.URL,
									},
								})
							}
						}
					}
				}
			}

			// Handle tool calls
			if len(message.ToolCalls) > 0 {
				for _, toolCall := range message.ToolCalls {
					var input json.RawMessage
					if toolCall.Function.Arguments != "" {
						// Attempt to use the provided arguments; repair if invalid, fallback to {}
						if json.Valid([]byte(toolCall.Function.Arguments)) {
							input = json.RawMessage(toolCall.Function.Arguments)
						} else {
							input = json.RawMessage("{}")
						}
					} else {
						input = json.RawMessage("{}")
					}

					block := MessageContentBlock{
						Type:  "tool_use",
						ID:    toolCall.ID,
						Name:  &toolCall.Function.Name,
						Input: input,
					}
					contentBlocks = append(contentBlocks, block)
				}
			}

			resp.Content = contentBlocks
		}

		// Convert finish reason
		if choice.FinishReason != nil {
			reason := model.ParseFinishReason(*choice.FinishReason)
			if wire := reason.ToAnthropic(); wire != "" {
				resp.StopReason = &wire
			} else {
				resp.StopReason = choice.FinishReason
			}
		}

		if choice.StopSequence != nil {
			resp.StopSequence = choice.StopSequence
		}
		if message != nil && message.Refusal != "" && (resp.StopReason == nil || *resp.StopReason == "end_turn") {
			resp.StopReason = lo.ToPtr("refusal")
		}
	}

	// Convert usage
	if response.Usage != nil {
		resp.Usage = i.convertUsage(response.Usage)
	}
	if response.ProviderExtensions != nil && response.ProviderExtensions.Anthropic != nil {
		resp.Fields = anthropic.CopyFields(response.ProviderExtensions.Anthropic.Fields)
	}
	if len(response.Choices) > 0 && response.Choices[0].Message != nil {
		content, exists, err := anthropic.RestoreContent(response.Choices[0].Message.ProviderExtensions)
		if err != nil {
			return nil, err
		}
		if exists {
			resp.Content = content.MultipleContent
		}
	}

	return json.Marshal(resp)
}

func (i *MessagesInbound) TransformStream(ctx context.Context, stream *model.InternalLLMResponse) ([]byte, error) {
	if err := anthropic.ValidateResponse(stream); err != nil {
		return nil, err
	}
	return i.TransformStreamEvents(ctx, model.StreamEventsFromInternalResponse(stream))
}

func joinSSEEvents(events [][]byte) []byte {
	result := make([]byte, 0)
	for idx, event := range events {
		if idx > 0 {
			result = append(result, '\n')
		}
		result = append(result, event...)
	}
	return result
}

func (i *MessagesInbound) convertUsage(usage *model.Usage) *Usage {
	anthropicUsage := &Usage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
	}
	if usage.HasAnthropicCacheSemantic() {
		anthropicUsage.CacheCreationInputTokens = usage.CacheCreationInputTokens
		anthropicUsage.CacheReadInputTokens = usage.CacheReadInputTokens
		if usage.CacheCreation5mInputTokens > 0 || usage.CacheCreation1hInputTokens > 0 {
			anthropicUsage.CacheCreation = &CacheCreationUsage{
				Ephemeral5mInputTokens: usage.CacheCreation5mInputTokens,
				Ephemeral1hInputTokens: usage.CacheCreation1hInputTokens,
			}
		}
	} else if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
		anthropicUsage.CacheReadInputTokens = usage.PromptTokensDetails.CachedTokens
		anthropicUsage.InputTokens -= anthropicUsage.CacheReadInputTokens
		if anthropicUsage.InputTokens < 0 {
			anthropicUsage.InputTokens = 0
		}
	}
	return anthropicUsage
}

// GetInternalResponse returns the complete internal response for logging, statistics, etc.
// For streaming: aggregates all stored stream chunks into a complete response
// For non-streaming: returns the stored response
func (i *MessagesInbound) GetInternalResponse(ctx context.Context) (*model.InternalLLMResponse, error) {
	if i.storedResponse != nil {
		return i.storedResponse, nil
	}
	return i.streamAggregator.BuildAndReset(), nil
}

// mergeToolCall merges a tool call delta into the existing tool calls slice
func mergeToolCall(toolCalls []model.ToolCall, delta model.ToolCall) []model.ToolCall {
	// Find existing tool call by index
	for i, tc := range toolCalls {
		if tc.Index == delta.Index {
			// Merge the delta into existing tool call
			if delta.ID != "" {
				toolCalls[i].ID = delta.ID
			}
			if delta.Type != "" {
				toolCalls[i].Type = delta.Type
			}
			if delta.Function.Name != "" {
				toolCalls[i].Function.Name += delta.Function.Name
			}
			if delta.Function.Arguments != "" {
				toolCalls[i].Function.Arguments += delta.Function.Arguments
			}
			return toolCalls
		}
	}

	// New tool call, add it
	return append(toolCalls, delta)
}

// formatSSEEvent 格式化为完整的 SSE 事件格式
func formatSSEEvent(eventType string, data []byte) []byte {
	return []byte(fmt.Sprintf("event:%s\ndata:%s\n\n", eventType, string(data)))
}
