package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func ValidateExtensions(extensions *model.ProviderExtensions) error {
	if extensions == nil {
		return nil
	}
	if extensions.Gemini != nil && extensions.Gemini.ThoughtSignature != "" {
		return fmt.Errorf("Anthropic cannot reuse a Gemini thought signature")
	}
	if extensions.OpenAI != nil {
		if err := ValidateResponsesItems(extensions.OpenAI.RawResponseItems); err != nil {
			return err
		}
	}
	if extensions.OpenAIResponses == nil {
		if extensions.OpenAIChat != nil {
			for _, key := range []string{"reasoning_signature", "encrypted_content", "audio"} {
				if meaningfulRaw(extensions.OpenAIChat.Fields[key]) {
					return fmt.Errorf("Anthropic cannot represent Chat %s", key)
				}
			}
		}
		return nil
	}
	for _, item := range extensions.OpenAIResponses.Items {
		if err := validateResponsesItem(item.Raw); err != nil {
			return err
		}
	}
	fields := extensions.OpenAIResponses.Fields
	for _, key := range []string{"input", "output"} {
		if err := ValidateResponsesItems(fields[key]); err != nil {
			return err
		}
	}
	if raw := fields["encrypted_content"]; meaningfulRaw(raw) {
		return fmt.Errorf("Anthropic cannot reuse OpenAI encrypted reasoning")
	}
	if raw := fields["namespace"]; meaningfulRaw(raw) {
		return fmt.Errorf("Anthropic cannot represent a Responses tool namespace")
	}
	if raw := fields["type"]; len(raw) > 0 {
		var kind string
		if err := json.Unmarshal(raw, &kind); err != nil {
			return err
		}
		if kind != "" && kind != "function" && kind != "message" && kind != "function_call" && kind != "function_call_output" && kind != "reasoning" {
			return fmt.Errorf("Anthropic cannot represent Responses item %q", kind)
		}
	}
	if raw := fields["stream_frame"]; len(raw) > 0 {
		if err := validateResponsesFrame(raw); err != nil {
			return err
		}
	}
	return nil
}

func ValidateResponsesItems(raw json.RawMessage) error {
	if !meaningfulRaw(raw) {
		return nil
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`"`)) {
		var text string
		return json.Unmarshal(raw, &text)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return fmt.Errorf("invalid Responses item list: %w", err)
	}
	for _, item := range items {
		if err := validateResponsesItem(item); err != nil {
			return err
		}
	}
	return nil
}

func validateResponsesItem(raw json.RawMessage) error {
	var item struct {
		Type             string            `json:"type"`
		Role             string            `json:"role"`
		Namespace        string            `json:"namespace"`
		EncryptedContent json.RawMessage   `json:"encrypted_content"`
		Content          json.RawMessage   `json:"content"`
		Summary          []json.RawMessage `json:"summary"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return fmt.Errorf("invalid Responses item: %w", err)
	}
	if meaningfulRaw(item.EncryptedContent) {
		return fmt.Errorf("Anthropic cannot reuse OpenAI encrypted reasoning")
	}
	if item.Namespace != "" {
		return fmt.Errorf("Anthropic cannot represent a Responses tool namespace")
	}
	switch item.Type {
	case "message":
		return validateResponsesContent(item.Content)
	case "":
		if item.Role != "" {
			return validateResponsesContent(item.Content)
		}
	case "function_call", "function_call_output":
		return nil
	case "reasoning":
		for _, summary := range item.Summary {
			var part struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(summary, &part); err != nil {
				return err
			}
			if part.Type != "summary_text" {
				return fmt.Errorf("Anthropic cannot represent reasoning summary %q", part.Type)
			}
		}
		return nil
	}
	return fmt.Errorf("Anthropic cannot represent Responses item %q", item.Type)
}

func validateResponsesContent(raw json.RawMessage) error {
	if !meaningfulRaw(raw) {
		return nil
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`"`)) {
		var text string
		return json.Unmarshal(raw, &text)
	}
	var parts []struct {
		Type        string          `json:"type"`
		Annotations json.RawMessage `json:"annotations"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return err
	}
	for _, part := range parts {
		switch part.Type {
		case "input_text", "output_text", "text":
			if meaningfulRaw(part.Annotations) && !bytes.Equal(bytes.TrimSpace(part.Annotations), []byte("[]")) {
				var citations []model.ContentCitation
				if err := json.Unmarshal(part.Annotations, &citations); err != nil {
					return err
				}
				for _, citation := range citations {
					if _, err := ConvertCitation(citation); err != nil {
						return err
					}
				}
			}
		case "input_image", "refusal":
		default:
			return fmt.Errorf("Anthropic cannot represent Responses content %q", part.Type)
		}
	}
	return nil
}

func validateResponsesFrame(raw json.RawMessage) error {
	var frame struct {
		Type     string          `json:"type"`
		Item     json.RawMessage `json:"item"`
		Part     json.RawMessage `json:"part"`
		Response *struct {
			Output json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return err
	}
	if meaningfulRaw(frame.Item) {
		if err := validateResponsesItem(frame.Item); err != nil {
			return err
		}
	}
	if frame.Response != nil {
		if err := ValidateResponsesItems(frame.Response.Output); err != nil {
			return err
		}
	}
	if meaningfulRaw(frame.Part) {
		if err := validateResponsesContent(append(append([]byte{'['}, frame.Part...), ']')); err != nil {
			return err
		}
	}
	switch frame.Type {
	case "response.refusal.delta", "response.refusal.done", "response.output_text.annotation.added":
		return nil
	case "response.created", "response.in_progress", "response.queued", "response.metadata", "response.completed", "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error", "response.output_item.added", "response.output_item.done", "response.content_part.added", "response.content_part.done", "response.output_text.delta", "response.output_text.done", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_text.delta", "response.reasoning_text.done":
		return nil
	}
	return fmt.Errorf("Anthropic cannot represent Responses event %q", frame.Type)
}

func meaningfulRaw(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte(`""`))
}

func ValidateToolCall(call model.ToolCall, native bool) error {
	if err := ValidateExtensions(call.ProviderExtensions); err != nil {
		return err
	}
	if err := ValidateExtensions(call.Function.ProviderExtensions); err != nil {
		return err
	}
	if call.ThoughtSignature != "" || call.GetGeminiExtensions().ThoughtSignature != "" {
		return fmt.Errorf("Anthropic cannot reuse a foreign tool thought signature")
	}
	if call.Type != "" && call.Type != "function" && !(native && call.Type == "server_tool_use") {
		return fmt.Errorf("Anthropic cannot represent tool call type %q", call.Type)
	}
	return nil
}

func ValidateMessage(message *model.Message, response bool) error {
	if message == nil {
		return nil
	}
	if err := ValidateExtensions(message.ProviderExtensions); err != nil {
		return err
	}
	_, native, err := RestoreContent(message.ProviderExtensions)
	if err != nil {
		return err
	}
	if message.Audio != nil || len(message.Images) > 0 {
		return fmt.Errorf("Anthropic cannot represent Chat audio or generated images")
	}
	for _, annotation := range message.Annotations {
		if _, err := AnnotationCitation(annotation); err != nil {
			return err
		}
	}
	foreign := message.ProviderExtensions != nil && (message.ProviderExtensions.OpenAIResponses != nil || message.ProviderExtensions.OpenAIChat != nil)
	for _, block := range message.ReasoningBlocks {
		if (block.Signature != "" || block.Data != "") && block.Provider != "" && block.Provider != "anthropic" {
			return fmt.Errorf("Anthropic cannot reuse %s opaque reasoning", block.Provider)
		}
		if foreign && (block.Signature != "" || block.Data != "") && !native {
			return fmt.Errorf("Anthropic cannot reuse foreign opaque reasoning")
		}
	}
	if foreign && !native && ((message.ReasoningSignature != nil && *message.ReasoningSignature != "") || len(message.RedactedThinkingBlocks) > 0) {
		return fmt.Errorf("Anthropic cannot reuse foreign opaque reasoning")
	}
	for _, call := range message.ToolCalls {
		if err := ValidateToolCall(call, native); err != nil {
			return err
		}
	}
	for _, part := range message.Content.MultipleContent {
		if !native && part.Native == nil {
			for _, citation := range part.Citations {
				if _, err := ConvertCitation(citation); err != nil {
					return err
				}
			}
		}
		if err := ValidateExtensions(part.ProviderExtensions); err != nil {
			return err
		}
		if part.Native != nil && part.Native.Format != model.APIFormatAnthropicMessage {
			return fmt.Errorf("Anthropic cannot reuse %s native content", part.Native.Format)
		}
		if part.Native != nil {
			var block MessageContentBlock
			if err := json.Unmarshal(part.Native.Raw, &block); err != nil {
				return err
			}
			continue
		}
		if native {
			continue
		}
		switch part.Type {
		case "text", "image_url":
		case "document", "server_tool_use", "server_tool_result":
			if response {
				return fmt.Errorf("Anthropic response content %q requires native blocks", part.Type)
			}
		default:
			return fmt.Errorf("Anthropic cannot represent content %q", part.Type)
		}
	}
	return nil
}

func ValidateResponse(response *model.InternalLLMResponse) error {
	if response == nil {
		return nil
	}
	if len(response.Choices) > 1 {
		return fmt.Errorf("Anthropic Messages cannot represent multiple choices")
	}
	if err := ValidateExtensions(response.ProviderExtensions); err != nil {
		return err
	}
	if err := ValidateResponsesItems(response.RawResponsesOutputItems); err != nil {
		return err
	}
	for _, choice := range response.Choices {
		if err := ValidateExtensions(choice.ProviderExtensions); err != nil {
			return err
		}
		if choice.Index != 0 {
			return fmt.Errorf("Anthropic Messages cannot represent choice %d", choice.Index)
		}
		if choice.Logprobs != nil {
			return fmt.Errorf("Anthropic Messages cannot represent logprobs")
		}
		if err := ValidateMessage(choice.Message, true); err != nil {
			return err
		}
		if err := ValidateMessage(choice.Delta, true); err != nil {
			return err
		}
		if ForeignExtensions(response.ProviderExtensions) || len(response.RawResponsesOutputItems) > 0 {
			if err := ValidateForeignReasoning(choice.Message); err != nil {
				return err
			}
			if err := ValidateForeignReasoning(choice.Delta); err != nil {
				return err
			}
		}
	}
	return nil
}

func ForeignExtensions(extensions *model.ProviderExtensions) bool {
	return extensions != nil && (extensions.OpenAIResponses != nil || extensions.OpenAIChat != nil || extensions.OpenAI != nil)
}

func ValidateForeignReasoning(message *model.Message) error {
	if message == nil {
		return nil
	}
	_, native, err := RestoreContent(message.ProviderExtensions)
	if err != nil {
		return err
	}
	if native {
		return nil
	}
	if (message.ReasoningSignature != nil && *message.ReasoningSignature != "") || len(message.RedactedThinkingBlocks) > 0 {
		return fmt.Errorf("Anthropic cannot reuse foreign opaque reasoning")
	}
	for _, block := range message.ReasoningBlocks {
		if block.Signature != "" || block.Data != "" {
			return fmt.Errorf("Anthropic cannot reuse foreign opaque reasoning")
		}
	}
	return nil
}

func ForeignStream(event model.StreamEvent) bool {
	if event.OutputIndex != nil || event.ItemID != "" {
		return true
	}
	return event.ProviderExtensions != nil && (event.ProviderExtensions.OpenAIResponses != nil || event.ProviderExtensions.OpenAIChat != nil)
}

func ValidateStreamEvent(event model.StreamEvent, foreign bool) error {
	if event.Index != 0 {
		return fmt.Errorf("Anthropic Messages cannot represent choice %d", event.Index)
	}
	if err := ValidateExtensions(event.ProviderExtensions); err != nil {
		return err
	}
	if err := ValidateExtensions(event.ChoiceExtensions); err != nil {
		return err
	}
	if err := ValidateMessage(event.Message, true); err != nil {
		return err
	}
	if event.ToolCall != nil {
		if err := ValidateToolCall(*event.ToolCall, !foreign && !ForeignStream(event)); err != nil {
			return err
		}
	}
	if event.NativeItem != nil && event.NativeItem.Format != model.APIFormatAnthropicMessage {
		return fmt.Errorf("Anthropic cannot encode %s native item", event.NativeItem.Format)
	}
	if foreign || ForeignStream(event) {
		if event.Delta != nil && event.Delta.Signature != "" {
			return fmt.Errorf("Anthropic cannot reuse a foreign reasoning signature")
		}
		if event.ContentBlock != nil && event.ContentBlock.Type == "redacted_thinking" {
			return fmt.Errorf("Anthropic cannot reuse foreign redacted thinking")
		}
	}
	if event.Citation != nil && (foreign || ForeignStream(event)) {
		if _, err := ConvertCitation(*event.Citation); err != nil {
			return err
		}
	}
	return nil
}
