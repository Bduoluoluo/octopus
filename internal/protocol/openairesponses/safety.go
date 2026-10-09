package openairesponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func ValidateMessageContent(message *model.Message, nativeResponses bool) error {
	if message == nil {
		return nil
	}
	if err := ValidateContentExtensions(message.ProviderExtensions); err != nil {
		return err
	}
	if message.ReasoningSignature != nil && *message.ReasoningSignature != "" && !nativeResponses {
		return fmt.Errorf("cannot reuse foreign or unproven reasoning signature as Responses encrypted_content")
	}
	if len(message.RedactedThinkingBlocks) > 0 {
		return fmt.Errorf("cannot convert redacted thinking to Responses")
	}
	for _, block := range message.ReasoningBlocks {
		if block.Kind == model.ReasoningBlockKindRedacted || block.Data != "" {
			return fmt.Errorf("cannot convert opaque reasoning block to Responses")
		}
		if block.Signature != "" && (!nativeResponses || (block.Provider != "" && block.Provider != "openai" && block.Provider != "openai_responses" && block.Provider != string(model.APIFormatOpenAIResponse))) {
			return fmt.Errorf("cannot reuse %q reasoning signature as Responses encrypted_content", block.Provider)
		}
		if block.Kind != model.ReasoningBlockKindThinking && block.Kind != model.ReasoningBlockKindSignature {
			return fmt.Errorf("cannot convert reasoning block %q to Responses", block.Kind)
		}
	}
	for _, part := range message.Content.MultipleContent {
		if err := ValidateContentExtensions(part.ProviderExtensions); err != nil {
			return err
		}
		if part.Native != nil {
			if err := ValidateNativeContent(*part.Native); err != nil {
				return err
			}
		}
		if part.ServerToolUse != nil || part.ServerToolResult != nil || part.Document != nil || part.VideoURL != nil {
			return fmt.Errorf("cannot convert native content attached to %q to Responses", part.Type)
		}
		if len(part.Citations) > 0 && !nativeResponses {
			return fmt.Errorf("cannot convert foreign content citations to Responses without a lossless citation mapping")
		}
	}
	for _, call := range message.ToolCalls {
		if err := ValidateContentExtensions(call.ProviderExtensions); err != nil {
			return err
		}
		if err := ValidateContentExtensions(call.Function.ProviderExtensions); err != nil {
			return err
		}
		if call.ThoughtSignature != "" {
			return fmt.Errorf("cannot convert foreign tool signature to Responses")
		}
		if call.Type != "" && call.Type != "function" && !nativeResponses {
			return fmt.Errorf("cannot convert native tool call %q to Responses", call.Type)
		}
	}
	if message.ToolCallIsError != nil && *message.ToolCallIsError {
		return fmt.Errorf("Responses cannot represent tool_result.is_error")
	}
	return nil
}

func ValidateContentExtensions(extensions *model.ProviderExtensions) error {
	if extensions == nil {
		return nil
	}
	if extensions.OpenAIResponses != nil {
		for _, item := range extensions.OpenAIResponses.Items {
			if item.Format != model.APIFormatOpenAIResponse {
				return fmt.Errorf("foreign native item in Responses namespace")
			}
			if !json.Valid(item.Raw) {
				return fmt.Errorf("invalid native Responses item")
			}
		}
	}
	if extensions.Common != nil && len(extensions.Common.Raw) > 0 {
		return fmt.Errorf("cannot convert unclassified native payload to Responses")
	}
	if extensions.Gemini != nil && (extensions.Gemini.ThoughtSignature != "" || extensions.Gemini.CachedContentRef != nil || len(extensions.Gemini.SpeechConfig) > 0) {
		return fmt.Errorf("cannot convert Gemini native payload to Responses")
	}
	if extensions.Volcengine != nil && len(extensions.Volcengine.Raw) > 0 {
		return fmt.Errorf("cannot convert retired protocol payload to Responses")
	}
	if extensions.Anthropic != nil {
		native := extensions.Anthropic
		if len(native.ServerTool) > 0 || len(native.MCPServers) > 0 || len(native.Container) > 0 {
			return fmt.Errorf("cannot convert Anthropic native tool/container payload to Responses")
		}
		for _, item := range native.Items {
			if err := ValidateNativeContent(item); err != nil {
				return err
			}
		}
		if raw := native.Fields["content"]; len(raw) > 0 {
			if err := validateAnthropicContent(raw); err != nil {
				return err
			}
		}
		for _, key := range []string{"stream_events", "signature", "encrypted_content", "redacted_thinking", "server_tool_use", "context_management", "container", "mcp_servers"} {
			if raw := native.Fields[key]; meaningfulJSON(raw) {
				return fmt.Errorf("cannot convert Anthropic native field %q to Responses", key)
			}
		}
		for key, raw := range native.Fields {
			switch key {
			case "content", "role", "cache_control", "system", "thinking", "metadata", "model", "max_tokens", "temperature", "top_p", "top_k", "stream", "stop_sequences", "tools", "tool_choice", "service_tier":
				continue
			}
			if meaningfulJSON(raw) {
				return fmt.Errorf("cannot convert Anthropic extension field %q to Responses", key)
			}
		}
	}
	if extensions.OpenAIChat != nil {
		for _, item := range extensions.OpenAIChat.Items {
			if err := ValidateNativeContent(item); err != nil {
				return err
			}
		}
		for key, raw := range extensions.OpenAIChat.Fields {
			if meaningfulJSON(raw) {
				return fmt.Errorf("cannot convert Chat native field %q to Responses", key)
			}
		}
	}
	return nil
}

func meaningfulJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte(`""`)) && !bytes.Equal(trimmed, []byte("[]"))
}

func ValidateNativeContent(item model.ProtocolItem) error {
	if item.Format == model.APIFormatOpenAIResponse {
		if !json.Valid(item.Raw) {
			return fmt.Errorf("invalid native Responses item")
		}
		return nil
	}
	if item.Format != model.APIFormatAnthropicMessage || item.Position < 0 {
		return fmt.Errorf("cannot convert native %s item to Responses", item.Format)
	}
	return validateAnthropicBlock(item.Raw)
}

func validateAnthropicContent(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return fmt.Errorf("invalid native Anthropic content: %w", err)
	}
	for _, block := range blocks {
		if err := validateAnthropicBlock(block); err != nil {
			return err
		}
	}
	return nil
}

func validateAnthropicBlock(raw json.RawMessage) error {
	var fields model.ProtocolFields
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("invalid native Anthropic block: %w", err)
	}
	var kind string
	if err := json.Unmarshal(fields["type"], &kind); err != nil {
		return fmt.Errorf("native Anthropic block missing type: %w", err)
	}
	allowed := map[string]bool{"type": true, "cache_control": true}
	switch kind {
	case "text":
		allowed["text"] = true
		if meaningfulJSON(fields["citations"]) {
			return fmt.Errorf("cannot convert native Anthropic citations to Responses")
		}
		allowed["citations"] = true
	case "thinking":
		allowed["thinking"] = true
		if meaningfulJSON(fields["signature"]) {
			return fmt.Errorf("cannot reuse Anthropic signature as Responses encrypted_content")
		}
		allowed["signature"] = true
	case "tool_use":
		allowed["id"], allowed["name"], allowed["input"] = true, true, true
	case "tool_result":
		allowed["tool_use_id"], allowed["content"], allowed["is_error"] = true, true, true
		if raw := fields["is_error"]; meaningfulJSON(raw) && string(raw) != "false" {
			return fmt.Errorf("Responses cannot represent native tool_result.is_error")
		}
		if err := validateAnthropicContent(fields["content"]); err != nil {
			return err
		}
	case "image":
		allowed["source"] = true
	default:
		return fmt.Errorf("cannot convert native Anthropic block %q to Responses", kind)
	}
	for key := range fields {
		if !allowed[key] {
			return fmt.Errorf("cannot convert native Anthropic %s field %q to Responses", kind, key)
		}
	}
	return nil
}

func ReasoningItems(message *model.Message) []Item {
	var items []Item
	if len(message.ReasoningBlocks) > 0 {
		for _, block := range message.ReasoningBlocks {
			item := Item{Type: "reasoning", Summary: []ReasoningSummary{}}
			if block.Text != "" {
				item.Summary = append(item.Summary, ReasoningSummary{Type: "summary_text", Text: block.Text})
			}
			if block.Signature != "" {
				signature := block.Signature
				item.EncryptedContent = &signature
			}
			if len(item.Summary) > 0 || item.EncryptedContent != nil {
				items = append(items, item)
			}
		}
		return items
	}
	text := message.GetReasoningContent()
	if text != "" || (message.ReasoningSignature != nil && *message.ReasoningSignature != "") {
		item := Item{Type: "reasoning", EncryptedContent: message.ReasoningSignature, Summary: []ReasoningSummary{}}
		if text != "" {
			item.Summary = append(item.Summary, ReasoningSummary{Type: "summary_text", Text: text})
		}
		items = append(items, item)
	}
	return items
}

func HasResponsesFrame(events []model.StreamEvent) bool {
	for _, event := range events {
		if event.ProviderExtensions != nil && event.ProviderExtensions.OpenAIResponses != nil && len(event.ProviderExtensions.OpenAIResponses.Fields["stream_frame"]) > 0 {
			return true
		}
	}
	return false
}

func ValidateStreamContent(events []model.StreamEvent) error {
	sameProtocol := HasResponsesFrame(events)
	for _, event := range events {
		if event.Index != 0 {
			return fmt.Errorf("Responses cannot represent choice %d", event.Index)
		}
		if err := ValidateContentExtensions(event.ProviderExtensions); err != nil {
			return err
		}
		if err := ValidateContentExtensions(event.ChoiceExtensions); err != nil {
			return err
		}
		if err := ValidateMessageContent(event.Message, sameProtocol); err != nil {
			return err
		}
		if event.Message != nil && !sameProtocol {
			if event.Message.Audio != nil || len(event.Message.Images) > 0 {
				return fmt.Errorf("cannot convert native audio/image message delta to Responses")
			}
			for _, part := range event.Message.Content.MultipleContent {
				if part.Type != "text" {
					return fmt.Errorf("cannot convert streamed %q content to Responses", part.Type)
				}
				if part.Text == nil {
					return fmt.Errorf("streamed text part has no text")
				}
			}
		}
		if event.NativeItem != nil {
			if err := ValidateNativeContent(*event.NativeItem); err != nil {
				return err
			}
		}
		if event.ContentBlock != nil {
			if err := ValidateContentExtensions(event.ContentBlock.ProviderExtensions); err != nil {
				return err
			}
			if event.ContentBlock.Data != "" || strings.Contains(event.ContentBlock.Type, "redacted") {
				return fmt.Errorf("cannot convert opaque content block to Responses")
			}
			switch event.ContentBlock.Type {
			case "", "text", "output_text", "thinking", "reasoning", "refusal":
			default:
				if !sameProtocol {
					return fmt.Errorf("cannot convert native content block %q to Responses", event.ContentBlock.Type)
				}
			}
		}
		if event.Delta != nil {
			if err := ValidateContentExtensions(event.Delta.ProviderExtensions); err != nil {
				return err
			}
			if event.Delta.Signature != "" && !sameProtocol {
				return fmt.Errorf("cannot reuse foreign or unproven stream signature as Responses encrypted_content")
			}
		}
		if event.ToolCall != nil {
			if err := ValidateMessageContent(&model.Message{ToolCalls: []model.ToolCall{*event.ToolCall}}, sameProtocol); err != nil {
				return err
			}
		}
	}
	return nil
}
