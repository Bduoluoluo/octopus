package inbound

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/samber/lo"
	wire "github.com/xuanli27/octopus/internal/protocol/anthropic"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

type outputBlock struct {
	index  int64
	kind   string
	closed bool
}

func (i *MessagesInbound) ResetStream() {
	inputToken := i.inputToken
	*i = MessagesInbound{inputToken: inputToken}
}

func (i *MessagesInbound) TransformStreamEvents(ctx context.Context, events []model.StreamEvent) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	foreign := i.foreignStream
	for _, event := range events {
		foreign = foreign || wire.ForeignStream(event)
	}
	for _, event := range events {
		if err := wire.ValidateStreamEvent(event, foreign); err != nil {
			return nil, err
		}
		if err := validateSupplementalEvent(event); err != nil {
			return nil, err
		}
	}
	i.foreignStream = foreign
	var expanded []model.StreamEvent
	for _, event := range events {
		if event.Message != nil && event.Message.Refusal != "" && (event.Kind == model.StreamEventKindMessageDelta || event.Kind == model.StreamEventKindMetadata) {
			message := *event.Message
			message.Refusal = ""
			projection := event
			projection.Message = &message
			expanded = append(expanded, projection)
		} else {
			expanded = append(expanded, event)
		}
		if (event.Kind == model.StreamEventKindMessageDelta || event.Kind == model.StreamEventKindMetadata) && event.Message != nil {
			for _, annotation := range event.Message.Annotations {
				citation, err := wire.AnnotationCitation(annotation)
				if err != nil {
					return nil, err
				}
				expanded = append(expanded, model.StreamEvent{Kind: model.StreamEventKindCitationDelta, Citation: &citation})
			}
			if event.Message.Refusal != "" {
				expanded = append(expanded, model.StreamEvent{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Refusal: event.Message.Refusal}})
			}
		}
	}
	events = expanded
	if i.blocks == nil {
		i.blocks = make(map[string]*outputBlock)
	}
	var output []byte
	emit := func(event StreamEvent) error {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		output = append(output, formatSSEEvent(event.Type, data)...)
		return nil
	}
	for _, event := range events {
		if event.Usage != nil {
			i.pendingUsage = event.Usage
			break
		}
	}
	start := func(event model.StreamEvent) error {
		if event.ID != "" {
			i.messageID = event.ID
		}
		if event.Model != "" {
			i.modelName = event.Model
		}
		if i.hasStarted {
			return nil
		}
		i.hasStarted = true
		usage := &Usage{InputTokens: i.inputToken, OutputTokens: 1}
		if i.pendingUsage != nil {
			usage = i.convertUsage(i.pendingUsage)
		}
		return emit(StreamEvent{Type: "message_start", Message: &StreamMessage{ID: i.messageID, Type: "message", Role: "assistant", Content: []MessageContentBlock{}, Model: i.modelName, Usage: usage}})
	}
	closeBlock := func(block *outputBlock) error {
		if block == nil || block.closed {
			return nil
		}
		block.closed = true
		return emit(StreamEvent{Type: "content_block_stop", Index: &block.index})
	}
	closeAll := func() error {
		blocks := make([]*outputBlock, 0, len(i.blocks))
		for _, block := range i.blocks {
			if !block.closed {
				blocks = append(blocks, block)
			}
		}
		sort.Slice(blocks, func(left, right int) bool { return blocks[left].index < blocks[right].index })
		for _, block := range blocks {
			if err := closeBlock(block); err != nil {
				return err
			}
		}
		return nil
	}
	keyFor := func(event model.StreamEvent, kind string) string {
		if event.ContentIndex != nil {
			return fmt.Sprintf("content:%d", *event.ContentIndex)
		}
		if event.ToolCall != nil {
			return fmt.Sprintf("tool:%d", event.ToolCall.Index)
		}
		if event.Kind == model.StreamEventKindToolCallStop {
			return fmt.Sprintf("tool:%d", event.Index)
		}
		return kind
	}
	open := func(event model.StreamEvent, kind string) (*outputBlock, error) {
		key := keyFor(event, kind)
		if block := i.blocks[key]; block != nil && !block.closed {
			return block, nil
		}
		if event.ContentIndex == nil && key != i.lastBlockKey {
			if previous := i.blocks[i.lastBlockKey]; previous != nil && previous.kind != "tool_use" && previous.kind != "server_tool_use" {
				if err := closeBlock(previous); err != nil {
					return nil, err
				}
			}
		}
		block := &outputBlock{index: i.nextBlockIndex, kind: kind}
		i.nextBlockIndex++
		i.blocks[key] = block
		i.lastBlockKey = key
		content := &MessageContentBlock{Type: kind}
		if event.NativeItem != nil {
			if event.NativeItem.Format != model.APIFormatAnthropicMessage {
				return nil, fmt.Errorf("cannot encode %s native content as Anthropic", event.NativeItem.Format)
			}
			if err := json.Unmarshal(event.NativeItem.Raw, content); err != nil {
				return nil, err
			}
		}
		switch kind {
		case "text":
			content.Text = lo.ToPtr("")
			content.Citations = nil
			delete(content.Fields, "citations")
		case "thinking":
			content.Thinking, content.Signature = lo.ToPtr(""), lo.ToPtr("")
		case "redacted_thinking":
			if event.ContentBlock != nil {
				content.Data = event.ContentBlock.Data
			}
		case "tool_use", "server_tool_use":
			if event.ToolCall != nil {
				content.ID, content.Name = event.ToolCall.ID, &event.ToolCall.Function.Name
				content.Input = json.RawMessage("{}")
			}
		}
		if err := emit(StreamEvent{Type: "content_block_start", Index: &block.index, ContentBlock: content}); err != nil {
			return nil, err
		}
		return block, nil
	}
	finalize := func() error {
		if !i.hasFinished || i.messageStopped {
			return nil
		}
		if err := closeAll(); err != nil {
			return err
		}
		delta := StreamEvent{Type: "message_delta", Delta: &StreamDelta{StopReason: i.stopReason, StopSequence: i.stopSequence}}
		if i.pendingUsage != nil {
			delta.Usage = i.convertUsage(i.pendingUsage)
		}
		if err := emit(delta); err != nil {
			return err
		}
		i.messageStopped = true
		return emit(StreamEvent{Type: "message_stop"})
	}
	for _, event := range events {
		if stream := model.InternalResponseFromStreamEvents([]model.StreamEvent{event}); stream != nil && stream.Object != "[DONE]" {
			i.streamAggregator.Add(stream)
		}
		if native, handled, err := i.restoreStreamFrame(event); err != nil {
			return nil, err
		} else if handled {
			output = append(output, native...)
			continue
		}
		if i.messageStopped && event.Kind != model.StreamEventKindError {
			continue
		}
		if event.Kind == model.StreamEventKindMetadata || event.Kind == model.StreamEventKindMessageDelta {
			if event.ID != "" {
				i.messageID = event.ID
			}
			if event.Model != "" {
				i.modelName = event.Model
			}
			continue
		}
		if event.Kind != model.StreamEventKindError && event.Kind != model.StreamEventKindDone && event.Kind != model.StreamEventKindUsageDelta {
			if err := start(event); err != nil {
				return nil, err
			}
		}
		switch event.Kind {
		case model.StreamEventKindMessageStart:
		case model.StreamEventKindContentBlockStart, model.StreamEventKindNativeItem:
			if event.NativeItem != nil && event.NativeItem.Format == model.APIFormatAnthropicMessage && event.NativeItem.Position < 0 {
				var rawEvent StreamEvent
				if err := json.Unmarshal(event.NativeItem.Raw, &rawEvent); err != nil {
					return nil, err
				}
				if rawEvent.Type == "" {
					return nil, fmt.Errorf("native Anthropic event missing type")
				}
				output = append(output, formatSSEEvent(rawEvent.Type, event.NativeItem.Raw)...)
				continue
			}
			kind := ""
			if event.ContentBlock != nil {
				kind = event.ContentBlock.Type
			}
			if kind == "" && event.NativeItem != nil {
				var content MessageContentBlock
				if err := json.Unmarshal(event.NativeItem.Raw, &content); err != nil {
					return nil, err
				}
				kind = content.Type
			}
			if kind == "" {
				return nil, fmt.Errorf("Anthropic content block is missing type")
			}
			block, err := open(event, kind)
			if err != nil {
				return nil, err
			}
			if kind == "redacted_thinking" && event.ContentIndex == nil {
				if err := closeBlock(block); err != nil {
					return nil, err
				}
			}
		case model.StreamEventKindTextDelta, model.StreamEventKindThinkingDelta, model.StreamEventKindSignatureDelta, model.StreamEventKindCitationDelta:
			kind := "text"
			delta := &StreamDelta{}
			switch event.Kind {
			case model.StreamEventKindTextDelta:
				if event.Delta == nil {
					continue
				}
				text := event.Delta.Text
				if text == "" {
					text = event.Delta.Refusal
				}
				if text == "" {
					continue
				}
				delta.Type, delta.Text = lo.ToPtr("text_delta"), &text
			case model.StreamEventKindThinkingDelta:
				if event.Delta == nil {
					continue
				}
				kind = "thinking"
				delta.Type, delta.Thinking = lo.ToPtr("thinking_delta"), &event.Delta.Thinking
			case model.StreamEventKindSignatureDelta:
				if event.Delta == nil {
					continue
				}
				kind = "thinking"
				delta.Type, delta.Signature = lo.ToPtr("signature_delta"), &event.Delta.Signature
			case model.StreamEventKindCitationDelta:
				if event.Citation == nil {
					return nil, fmt.Errorf("Anthropic citation event missing citation")
				}
				citation := *event.Citation
				if citation.Type == "url_citation" {
					converted, err := wire.ConvertCitation(citation)
					if err != nil {
						return nil, err
					}
					citation = converted
				}
				delta.Type, delta.Citation = lo.ToPtr("citations_delta"), &citation
			}
			block, err := open(event, kind)
			if err != nil {
				return nil, err
			}
			if err := emit(StreamEvent{Type: "content_block_delta", Index: &block.index, Delta: delta}); err != nil {
				return nil, err
			}
			if event.Kind == model.StreamEventKindThinkingDelta && event.Delta.Signature != "" {
				if err := emit(StreamEvent{Type: "content_block_delta", Index: &block.index, Delta: &StreamDelta{Type: lo.ToPtr("signature_delta"), Signature: &event.Delta.Signature}}); err != nil {
					return nil, err
				}
			}
		case model.StreamEventKindToolCallStart, model.StreamEventKindToolCallDelta:
			if event.ToolCall == nil {
				return nil, fmt.Errorf("Anthropic tool event missing tool call")
			}
			kind := "tool_use"
			if event.ToolCall.Type == "server_tool_use" {
				kind = "server_tool_use"
			}
			block, err := open(event, kind)
			if err != nil {
				return nil, err
			}
			arguments := event.ToolCall.Function.Arguments
			if event.Delta != nil && event.Delta.Arguments != "" {
				arguments = event.Delta.Arguments
			}
			if arguments != "" {
				if err := emit(StreamEvent{Type: "content_block_delta", Index: &block.index, Delta: &StreamDelta{Type: lo.ToPtr("input_json_delta"), PartialJSON: &arguments}}); err != nil {
					return nil, err
				}
			}
		case model.StreamEventKindContentBlockStop, model.StreamEventKindToolCallStop:
			key := i.lastBlockKey
			if event.ContentIndex != nil || event.Kind == model.StreamEventKindToolCallStop {
				key = keyFor(event, "")
			}
			if err := closeBlock(i.blocks[key]); err != nil {
				return nil, err
			}
		case model.StreamEventKindMessageStop:
			if err := closeAll(); err != nil {
				return nil, err
			}
			reason := event.StopReason.ToAnthropic()
			if reason == "" {
				reason = "end_turn"
			}
			i.stopReason, i.stopSequence, i.hasFinished = &reason, event.StopSequence, true
		case model.StreamEventKindUsageDelta:
			if event.Usage != nil {
				i.pendingUsage = event.Usage
			}
			if i.hasFinished {
				if err := finalize(); err != nil {
					return nil, err
				}
			}
		case model.StreamEventKindDone:
			if !i.hasFinished {
				i.stopReason, i.hasFinished = lo.ToPtr("end_turn"), true
			}
			if err := finalize(); err != nil {
				return nil, err
			}
		case model.StreamEventKindError:
			if event.Error == nil {
				return nil, fmt.Errorf("Anthropic error event missing error")
			}
			errType := event.Error.Detail.Type
			if errType == "" {
				errType = "api_error"
			}
			if err := emit(StreamEvent{Type: "error", Error: &ErrorDetail{Type: errType, Message: event.Error.Detail.Message}}); err != nil {
				return nil, err
			}
			i.messageStopped = true
		default:
			return nil, fmt.Errorf("unsupported Anthropic output event %q", event.Kind)
		}
	}
	return output, nil
}

func validateSupplementalEvent(event model.StreamEvent) error {
	if event.Kind != model.StreamEventKindMetadata && event.Kind != model.StreamEventKindMessageDelta {
		return nil
	}
	if event.Logprobs != nil {
		return fmt.Errorf("Anthropic Messages cannot represent streamed logprobs")
	}
	if event.Delta != nil || event.ContentBlock != nil || event.ToolCall != nil || event.Citation != nil || event.NativeItem != nil || event.Error != nil {
		return fmt.Errorf("Anthropic supplemental event %q contains unsupported semantic payload", event.Kind)
	}
	if event.Message == nil {
		return nil
	}
	message := event.Message
	if message.Audio != nil || len(message.Images) > 0 {
		return fmt.Errorf("Anthropic Messages cannot represent streamed audio or generated images")
	}
	if message.Content.Content != nil || len(message.Content.MultipleContent) > 0 || len(message.ToolCalls) > 0 || message.GetReasoningContent() != "" || message.ReasoningSignature != nil || len(message.ReasoningBlocks) > 0 || len(message.RedactedThinkingBlocks) > 0 {
		return fmt.Errorf("Anthropic supplemental event %q contains message content requiring dedicated content events", event.Kind)
	}
	return nil
}
