package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type streamBlockKey struct {
	choice  int
	output  int
	item    string
	content int
}

type aggregatedStreamBlock struct {
	key          streamBlockKey
	order        int
	kind         string
	text         string
	refusal      string
	thinking     string
	signature    string
	data         string
	arguments    string
	citations    []ContentCitation
	native       *ProtocolItem
	fields       ProtocolFields
	closed       bool
	textSet      bool
	thinkingSet  bool
	signatureSet bool
	argumentsSet bool
}

func applyCitationBlocks(response *InternalLLMResponse, chunks []*InternalLLMResponse) error {
	blocks := make(map[streamBlockKey]*aggregatedStreamBlock)
	last := make(map[int]*aggregatedStreamBlock)
	lastThinking := make(map[streamBlockKey]*aggregatedStreamBlock)
	lastText := make(map[streamBlockKey]*aggregatedStreamBlock)
	itemPositions := make(map[string]int)
	for _, chunk := range chunks {
		if chunk == nil {
			continue
		}
		for _, event := range chunk.ProtocolEvents {
			if event.OutputIndex != nil && event.ItemID != "" {
				itemPositions[fmt.Sprintf("%d:%s", event.Index, event.ItemID)] = *event.OutputIndex
			}
		}
	}
	nextUnindexed := make(map[int]int)
	snapshots := []ProtocolItem(nil)
	nativeEvents := make(map[int][]ProtocolItem)
	sawReasoning := make(map[int]bool)
	sawText := make(map[int]bool)
	newBlock := func(key streamBlockKey, kind string) *aggregatedStreamBlock {
		block := &aggregatedStreamBlock{key: key, kind: kind, order: len(blocks)}
		blocks[key] = block
		return block
	}
	for _, chunk := range chunks {
		if chunk == nil {
			continue
		}
		events := chunk.ProtocolEvents
		if len(events) == 0 {
			events = StreamEventsFromInternalResponse(chunk)
		}
		for _, event := range events {
			if event.NativeItem != nil {
				var nativeFields ProtocolFields
				if err := json.Unmarshal(event.NativeItem.Raw, &nativeFields); err != nil {
					return fmt.Errorf("invalid native stream item: %w", err)
				}
				if nativeFields == nil {
					return fmt.Errorf("native stream item must be an object")
				}
				if event.NativeItem.Format == APIFormatAnthropicMessage && event.NativeItem.Position >= 0 {
					var kind string
					if err := json.Unmarshal(nativeFields["type"], &kind); err != nil {
						return fmt.Errorf("invalid native block type: %w", err)
					}
					if kind == "" {
						return fmt.Errorf("native content block missing type")
					}
				}
			}
			if event.Kind == StreamEventKindContentBlockStop || event.Kind == StreamEventKindToolCallStop {
				for _, block := range blocks {
					if block.key.choice != event.Index {
						continue
					}
					if event.OutputIndex != nil && block.key.output != *event.OutputIndex {
						continue
					}
					if event.ContentIndex != nil && block.key.content != *event.ContentIndex {
						continue
					}
					if event.ContentIndex == nil && event.OutputIndex == nil && last[event.Index] != block {
						continue
					}
					block.closed = true
				}
				continue
			}
			if event.OutputIndex != nil && event.ItemID != "" {
				itemPositions[fmt.Sprintf("%d:%s", event.Index, event.ItemID)] = *event.OutputIndex
			}
			if event.NativeItem != nil && event.NativeItem.Format == APIFormatOpenAIResponse {
				snapshots = mergeStreamItems(snapshots, []ProtocolItem{*event.NativeItem})
			}
			if event.NativeItem != nil && event.NativeItem.Position < 0 {
				nativeEvents[event.Index] = append(nativeEvents[event.Index], cloneProtocolItems([]ProtocolItem{*event.NativeItem})[0])
				continue
			}
			kind := streamBlockKind(event)
			if kind == "" {
				continue
			}
			key := streamBlockKey{choice: event.Index, output: -1, content: -1}
			if event.OutputIndex != nil {
				key.output = *event.OutputIndex
			} else if event.ItemID != "" {
				if position, exists := itemPositions[fmt.Sprintf("%d:%s", event.Index, event.ItemID)]; exists {
					key.output = position
				} else {
					key.item = event.ItemID
				}
			}
			scope := key
			if event.ContentIndex != nil {
				key.content = *event.ContentIndex
			} else if event.NativeItem != nil && event.NativeItem.Format == APIFormatAnthropicMessage {
				key.content = event.NativeItem.Position
			}
			if event.NativeItem != nil && event.NativeItem.Format == APIFormatOpenAIResponse {
				continue
			}
			var block *aggregatedStreamBlock
			if key.content < 0 && event.Kind != StreamEventKindContentBlockStart {
				if kind == "thinking" {
					block = lastThinking[scope]
				}
				if kind == "text" {
					block = lastText[scope]
				}
				if block != nil && block.closed && event.Kind != StreamEventKindSignatureDelta {
					block = nil
				}
			}
			if block == nil && key.content >= 0 {
				block = blocks[key]
			}
			if block == nil && key.content < 0 {
				previous := last[event.Index]
				if previous != nil && !previous.closed && previous.key.output == key.output && previous.key.item == key.item && previous.kind == kind && event.Kind != StreamEventKindContentBlockStart {
					block = previous
				} else {
					key.content = -1 - nextUnindexed[event.Index]
					nextUnindexed[event.Index]++
				}
			}
			if block == nil {
				block = newBlock(key, kind)
			}
			last[event.Index] = block
			if kind == "thinking" || kind == "redacted_thinking" {
				sawReasoning[event.Index] = true
				if kind == "thinking" {
					lastThinking[scope] = block
				}
			}
			if kind == "text" {
				sawText[event.Index] = true
				lastText[scope] = block
			}
			if event.NativeItem != nil {
				block.native = &ProtocolItem{Format: event.NativeItem.Format, Position: event.NativeItem.Position, Raw: cloneRawMessage(event.NativeItem.Raw)}
				if err := json.Unmarshal(event.NativeItem.Raw, &block.fields); err != nil {
					return fmt.Errorf("invalid native stream block: %w", err)
				}
				if block.fields == nil {
					return fmt.Errorf("native stream block must be an object")
				}
				if err := readInitialBlock(block); err != nil {
					return err
				}
			}
			switch event.Kind {
			case StreamEventKindContentBlockStart:
				if event.ContentBlock != nil {
					if block.native == nil {
						block.text = event.ContentBlock.Text
						block.data = event.ContentBlock.Data
					}
				}
			case StreamEventKindTextDelta:
				if event.Delta != nil {
					block.text += event.Delta.Text
					block.refusal += event.Delta.Refusal
					block.textSet = true
				}
			case StreamEventKindThinkingDelta:
				if event.Delta != nil {
					block.thinking += event.Delta.Thinking
					block.signature += event.Delta.Signature
					block.thinkingSet = true
					block.signatureSet = block.signatureSet || event.Delta.Signature != ""
				}
			case StreamEventKindSignatureDelta:
				if event.Delta != nil {
					block.signature += event.Delta.Signature
					block.signatureSet = true
				}
			case StreamEventKindCitationDelta:
				if event.Citation != nil {
					block.citations = append(block.citations, *event.Citation)
				}
			case StreamEventKindToolCallStart, StreamEventKindToolCallDelta:
				if event.ToolCall != nil {
					arguments := event.ToolCall.Function.Arguments
					if event.Delta != nil && event.Delta.Arguments != "" {
						arguments = event.Delta.Arguments
					}
					if arguments != "" {
						block.arguments += arguments
						block.argumentsSet = true
					}
				}
			}
		}
	}
	ordered := make([]*aggregatedStreamBlock, 0, len(blocks))
	scopeOrder := make(map[streamBlockKey]int)
	for _, block := range blocks {
		ordered = append(ordered, block)
		scope := block.key
		scope.content = -1
		if current, exists := scopeOrder[scope]; !exists || block.order < current {
			scopeOrder[scope] = block.order
		}
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		first, second := ordered[left], ordered[right]
		if first.key.choice != second.key.choice {
			return first.key.choice < second.key.choice
		}
		if (first.key.output >= 0) != (second.key.output >= 0) {
			return first.key.output >= 0
		}
		if first.key.output >= 0 && second.key.output >= 0 && first.key.output != second.key.output {
			return first.key.output < second.key.output
		}
		if first.key.item != second.key.item {
			firstScope, secondScope := first.key, second.key
			firstScope.content, secondScope.content = -1, -1
			return scopeOrder[firstScope] < scopeOrder[secondScope]
		}
		if first.key.output == second.key.output && first.key.item == second.key.item && first.key.content >= 0 && second.key.content >= 0 && first.key.content != second.key.content {
			return first.key.content < second.key.content
		}
		if first.key.output == second.key.output && first.key.item == second.key.item && (first.key.content >= 0) != (second.key.content >= 0) {
			return first.key.content >= 0
		}
		return first.order < second.order
	})
	choices := make(map[int]*Choice)
	for index := range response.Choices {
		choices[response.Choices[index].Index] = &response.Choices[index]
	}
	ensureChoice := func(index int) *Choice {
		if choice := choices[index]; choice != nil {
			return choice
		}
		response.Choices = append(response.Choices, Choice{Index: index, Message: &Message{Role: "assistant"}})
		for position := range response.Choices {
			choices[response.Choices[position].Index] = &response.Choices[position]
		}
		return choices[index]
	}
	parts := make(map[int][]MessageContentPart)
	reasoning := make(map[int][]ReasoningBlock)
	nativeBlocks := make(map[int][]ProtocolItem)
	for _, block := range ordered {
		index := block.key.choice
		if block.native != nil {
			if err := writeFinalBlock(block); err != nil {
				return err
			}
			nativeBlocks[index] = append(nativeBlocks[index], *block.native)
		}
		switch block.kind {
		case "thinking":
			reasoning[index] = append(reasoning[index], ReasoningBlock{Kind: ReasoningBlockKindThinking, Index: block.key.content, Text: block.thinking, Signature: block.signature, Provider: streamBlockProvider(block)})
		case "redacted_thinking":
			reasoning[index] = append(reasoning[index], ReasoningBlock{Kind: ReasoningBlockKindRedacted, Index: block.key.content, Data: block.data, Provider: streamBlockProvider(block)})
		case "tool_use":
		case "text":
			if block.refusal != "" && block.text == "" && len(block.citations) == 0 && block.native == nil {
				continue
			}
			text := block.text
			part := MessageContentPart{Type: "text", Text: &text, Citations: block.citations, Native: block.native}
			parts[index] = append(parts[index], part)
		default:
			if block.native != nil {
				parts[index] = append(parts[index], MessageContentPart{Type: block.kind, Native: block.native})
			}
		}
	}
	for index, content := range parts {
		message := ensureChoice(index).Message
		if message == nil {
			continue
		}
		extra := make([]MessageContentPart, 0)
		for _, part := range message.Content.MultipleContent {
			if part.Type != "text" && part.Native == nil {
				extra = append(extra, part)
			}
		}
		if sawText[index] && len(content) == 1 && content[0].Type == "text" && len(content[0].Citations) == 0 && len(extra) == 0 {
			message.Content = MessageContent{Content: content[0].Text}
		} else {
			message.Content = MessageContent{MultipleContent: append(content, extra...)}
		}
	}
	for index := range sawReasoning {
		message := ensureChoice(index).Message
		if message == nil {
			continue
		}
		message.ReasoningBlocks = reasoning[index]
		message.RedactedThinkingBlocks = nil
		var text, signature strings.Builder
		for position := range message.ReasoningBlocks {
			block := &message.ReasoningBlocks[position]
			if block.Index < 0 {
				block.Index = position
			}
			text.WriteString(block.Text)
			signature.WriteString(block.Signature)
			if block.Kind == ReasoningBlockKindRedacted {
				message.RedactedThinkingBlocks = append(message.RedactedThinkingBlocks, block.Data)
			}
		}
		if text.Len() > 0 {
			value := text.String()
			message.ReasoningContent = &value
		}
		if signature.Len() > 0 {
			value := signature.String()
			message.ReasoningSignature = &value
		}
	}
	for index, items := range nativeBlocks {
		message := ensureChoice(index).Message
		if message.ProviderExtensions == nil {
			message.ProviderExtensions = &ProviderExtensions{}
		}
		if message.ProviderExtensions.Anthropic == nil {
			message.ProviderExtensions.Anthropic = &AnthropicExtension{}
		}
		message.ProviderExtensions.Anthropic.Items = items
	}
	for index, items := range nativeEvents {
		message := ensureChoice(index).Message
		if message.ProviderExtensions == nil {
			message.ProviderExtensions = &ProviderExtensions{}
		}
		if message.ProviderExtensions.Anthropic == nil {
			message.ProviderExtensions.Anthropic = &AnthropicExtension{}
		}
		message.ProviderExtensions.Anthropic.Fields = mergeStreamFields(message.ProviderExtensions.Anthropic.Fields, ProtocolFields{})
		raw := make([]json.RawMessage, 0, len(items))
		for _, item := range items {
			raw = append(raw, item.Raw)
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		message.ProviderExtensions.Anthropic.Fields["stream_events"] = encoded
	}
	if len(response.RawResponsesOutputItems) > 0 {
		var rawItems []json.RawMessage
		if err := json.Unmarshal(response.RawResponsesOutputItems, &rawItems); err != nil {
			return fmt.Errorf("invalid Responses output snapshot: %w", err)
		}
		snapshots = nil
		for position, raw := range rawItems {
			snapshots = mergeStreamItems(snapshots, []ProtocolItem{{Format: APIFormatOpenAIResponse, Position: position, Raw: raw}})
		}
	}
	if len(snapshots) > 0 || len(response.RawResponsesOutputItems) > 0 {
		if response.ProviderExtensions == nil {
			response.ProviderExtensions = &ProviderExtensions{}
		}
		if response.ProviderExtensions.OpenAIResponses == nil {
			response.ProviderExtensions.OpenAIResponses = &ProtocolExtension{}
		}
		response.ProviderExtensions.OpenAIResponses.Items = snapshots
		rawItems := make([]json.RawMessage, 0, len(snapshots))
		for _, item := range snapshots {
			rawItems = append(rawItems, item.Raw)
		}
		encoded, err := json.Marshal(rawItems)
		if err != nil {
			return err
		}
		response.RawResponsesOutputItems = encoded
		if response.ProviderExtensions.OpenAI == nil {
			response.ProviderExtensions.OpenAI = &OpenAIExtension{}
		}
		response.ProviderExtensions.OpenAI.RawResponseItems = cloneRawMessage(encoded)
	}
	sort.SliceStable(response.Choices, func(left, right int) bool { return response.Choices[left].Index < response.Choices[right].Index })
	return nil
}

func streamBlockKind(event StreamEvent) string {
	if event.NativeItem != nil && event.NativeItem.Format == APIFormatAnthropicMessage && event.NativeItem.Position >= 0 {
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(event.NativeItem.Raw, &header) == nil && header.Type != "" {
			return header.Type
		}
	}
	switch event.Kind {
	case StreamEventKindContentBlockStart:
		if event.ContentBlock != nil {
			return event.ContentBlock.Type
		}
	case StreamEventKindTextDelta, StreamEventKindCitationDelta:
		return "text"
	case StreamEventKindThinkingDelta, StreamEventKindSignatureDelta:
		return "thinking"
	case StreamEventKindToolCallStart, StreamEventKindToolCallDelta:
		if event.ToolCall != nil && event.ToolCall.Type == "server_tool_use" {
			return "server_tool_use"
		}
		return "tool_use"
	}
	return ""
}

func streamBlockProvider(block *aggregatedStreamBlock) string {
	if block.native != nil && block.native.Format == APIFormatAnthropicMessage {
		return "anthropic"
	}
	if block.key.output >= 0 || block.key.item != "" {
		return "openai"
	}
	return ""
}

func readInitialBlock(block *aggregatedStreamBlock) error {
	fields := map[string]*string{}
	switch block.kind {
	case "text":
		fields["text"] = &block.text
	case "thinking":
		fields["thinking"], fields["signature"] = &block.thinking, &block.signature
	case "redacted_thinking":
		fields["data"] = &block.data
	}
	for key, target := range fields {
		if raw := block.fields[key]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if err := json.Unmarshal(raw, target); err != nil {
				return fmt.Errorf("invalid native block %s: %w", key, err)
			}
		}
	}
	if block.kind == "text" {
		if raw := block.fields["citations"]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if err := json.Unmarshal(raw, &block.citations); err != nil {
				return fmt.Errorf("invalid native block citations: %w", err)
			}
		}
	}
	return nil
}

func writeFinalBlock(block *aggregatedStreamBlock) error {
	if block.textSet {
		block.fields["text"], _ = json.Marshal(block.text)
	}
	if block.thinkingSet {
		block.fields["thinking"], _ = json.Marshal(block.thinking)
	}
	if block.signatureSet {
		block.fields["signature"], _ = json.Marshal(block.signature)
	}
	if len(block.citations) > 0 {
		encoded, err := json.Marshal(block.citations)
		if err != nil {
			return err
		}
		block.fields["citations"] = encoded
	}
	if block.argumentsSet && json.Valid([]byte(block.arguments)) {
		block.fields["input"] = json.RawMessage(block.arguments)
	} else if block.argumentsSet && block.closed {
		return fmt.Errorf("invalid completed native tool arguments at content index %d", block.key.content)
	}
	encoded, err := json.Marshal(block.fields)
	if err != nil {
		return err
	}
	block.native.Raw = encoded
	return nil
}

func mergeStreamFields(previous, next ProtocolFields) ProtocolFields {
	fields := cloneProtocolFields(previous)
	if fields == nil {
		fields = make(ProtocolFields)
	}
	for key, value := range next {
		fields[key] = cloneRawMessage(value)
	}
	return fields
}

func mergeStreamItems(previous, next []ProtocolItem) []ProtocolItem {
	items := cloneProtocolItems(previous)
	for _, item := range next {
		position := -1
		identity := streamItemID(item.Raw)
		for index, current := range items {
			if current.Format != item.Format {
				continue
			}
			if (identity != "" && identity == streamItemID(current.Raw)) || (item.Position >= 0 && item.Position == current.Position) {
				position = index
				break
			}
		}
		copy := cloneProtocolItems([]ProtocolItem{item})[0]
		if position >= 0 {
			items[position] = copy
		} else {
			items = append(items, copy)
		}
	}
	sort.SliceStable(items, func(left, right int) bool { return items[left].Position < items[right].Position })
	return items
}

func streamItemID(raw json.RawMessage) string {
	var header struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return ""
	}
	return header.ID
}

func mergeStreamExtensions(previous, next *ProviderExtensions) *ProviderExtensions {
	if previous == nil {
		return CloneProviderExtensions(next)
	}
	if next == nil {
		return CloneProviderExtensions(previous)
	}
	result := CloneProviderExtensions(previous)
	incoming := CloneProviderExtensions(next)
	mergeProtocol := func(destination **ProtocolExtension, source *ProtocolExtension) {
		if source == nil {
			return
		}
		if *destination == nil {
			*destination = source
			return
		}
		(*destination).Fields = mergeStreamFields((*destination).Fields, source.Fields)
		(*destination).Items = mergeStreamItems((*destination).Items, source.Items)
	}
	mergeProtocol(&result.OpenAIChat, incoming.OpenAIChat)
	mergeProtocol(&result.OpenAIResponses, incoming.OpenAIResponses)
	if incoming.Anthropic != nil {
		if result.Anthropic == nil {
			result.Anthropic = incoming.Anthropic
		} else {
			fields := mergeStreamFields(result.Anthropic.Fields, incoming.Anthropic.Fields)
			items := mergeStreamItems(result.Anthropic.Items, incoming.Anthropic.Items)
			mergeAnthropicExtension(result.Anthropic, incoming.Anthropic)
			result.Anthropic.Fields, result.Anthropic.Items = fields, items
		}
	}
	if incoming.OpenAI != nil {
		if result.OpenAI == nil {
			result.OpenAI = incoming.OpenAI
		} else {
			mergeOpenAIExtension(result.OpenAI, incoming.OpenAI)
		}
	}
	if incoming.Common != nil {
		result.Common = incoming.Common
	}
	if incoming.Gemini != nil {
		if result.Gemini == nil {
			result.Gemini = incoming.Gemini
		} else {
			mergeGeminiExtension(result.Gemini, incoming.Gemini)
		}
	}
	if incoming.Volcengine != nil {
		result.Volcengine = incoming.Volcengine
	}
	return result
}
