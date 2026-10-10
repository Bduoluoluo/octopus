package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/samber/lo"
	wire "github.com/xuanli27/octopus/internal/protocol/anthropic"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

type streamBlock struct {
	kind      string
	tool      *model.ToolCall
	arguments string
	stopped   bool
}

func (o *MessageOutbound) TransformStreamFrame(ctx context.Context, frame model.StreamFrame) ([]model.StreamEvent, error) {
	events, err := o.transformFrame(ctx, frame)
	if err != nil {
		o.streamError = err
	}
	if err == nil && len(events) > 0 && json.Valid(frame.Data) {
		for index := range events {
			fields := model.ProtocolFields{"stream_frame_projection": json.RawMessage("true")}
			if index == 0 {
				fields["stream_frame"] = append(json.RawMessage(nil), frame.Data...)
				fields["stream_frame_event"], _ = json.Marshal(frame.Event)
				fields["stream_frame_id"], _ = json.Marshal(frame.ID)
			}
			events[index].ProviderExtensions = &model.ProviderExtensions{Anthropic: &model.AnthropicExtension{Fields: fields}}
		}
	}
	return events, err
}

func (o *MessageOutbound) transformFrame(ctx context.Context, frame model.StreamFrame) ([]model.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.closed {
		return nil, fmt.Errorf("Anthropic stream is closed")
	}
	if o.streamError != nil {
		return nil, o.streamError
	}
	if len(bytes.TrimSpace(frame.Data)) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(frame.Data), []byte("[DONE]")) {
		o.terminal = true
		return []model.StreamEvent{{Kind: model.StreamEventKindDone}}, nil
	}
	var event wire.StreamEvent
	if err := json.Unmarshal(frame.Data, &event); err != nil {
		return nil, fmt.Errorf("invalid Anthropic stream frame: %w", err)
	}
	if event.Type == "" {
		event.Type = frame.Event
	}
	if frame.Event == "error" || (event.Error != nil && (event.Error.Type != "" || event.Error.Message != "")) {
		event.Type = "error"
	}
	if event.Type == "ping" {
		return nil, nil
	}
	if !o.initialized {
		o.blocks = make(map[int]*streamBlock)
		o.toolCalls = make(map[int]*model.ToolCall)
		o.toolIndex = -1
		o.initialized = true
	}
	var events []model.StreamEvent
	appendEvent := func(value model.StreamEvent) {
		value.ID, value.Model = o.streamID, o.streamModel
		if event.Index != nil {
			value.ContentIndex = lo.ToPtr(int(*event.Index))
		}
		events = append(events, value)
	}
	appendUsage := func(usage *wire.Usage) {
		if usage != nil {
			o.wireUsage = wire.MergeUsage(o.wireUsage, usage)
			o.streamUsage = convertAnthropicUsage(o.wireUsage)
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindUsageDelta, Usage: o.streamUsage})
		}
	}
	appendNative := func() {
		appendEvent(model.StreamEvent{Kind: model.StreamEventKindNativeItem, NativeItem: &model.ProtocolItem{Format: model.APIFormatAnthropicMessage, Position: -1, Raw: append(json.RawMessage(nil), frame.Data...)}})
	}
	switch event.Type {
	case "message_start":
		if event.Message == nil {
			appendNative()
			break
		}
		o.streamID, o.streamModel = event.Message.ID, event.Message.Model
		appendEvent(model.StreamEvent{Kind: model.StreamEventKindMessageStart, Role: "assistant"})
		appendUsage(event.Message.Usage)
	case "content_block_start":
		if event.Index == nil || *event.Index < 0 || event.ContentBlock == nil {
			appendNative()
			break
		}
		index := int(*event.Index)
		if previous := o.blocks[index]; previous != nil {
			appendNative()
			break
		}
		block := event.ContentBlock
		state := &streamBlock{kind: block.Type}
		o.blocks[index] = state
		initial := *block
		initial.Fields = wire.CopyFields(block.Fields)
		switch block.Type {
		case "text":
			initial.Text = lo.ToPtr("")
			if len(initial.Citations) > 0 {
				initial.Citations = []wire.TextCitation{}
			}
		case "thinking":
			initial.Thinking, initial.Signature = lo.ToPtr(""), lo.ToPtr("")
		}
		raw, err := json.Marshal(initial)
		if err != nil {
			return nil, err
		}
		native := &model.ProtocolItem{Format: model.APIFormatAnthropicMessage, Position: index, Raw: raw}
		if block.Type == "tool_use" || block.Type == "server_tool_use" {
			o.toolIndex++
			tool := &model.ToolCall{Index: o.toolIndex, ID: block.ID, Type: "function", Function: model.FunctionCall{Name: lo.FromPtr(block.Name)}}
			if block.Type == "server_tool_use" {
				tool.Type = "server_tool_use"
			}
			if len(block.Input) > 0 && !bytes.Equal(bytes.TrimSpace(block.Input), []byte("{}")) {
				tool.Function.Arguments = string(block.Input)
				state.arguments = tool.Function.Arguments
			}
			state.tool = tool
			o.toolCalls[index] = tool
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindToolCallStart, ToolCall: tool, NativeItem: native})
		} else {
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindContentBlockStart, ContentBlock: &model.StreamContentBlock{Type: block.Type, Data: block.Data}, NativeItem: native})
			if block.Text != nil && *block.Text != "" {
				appendEvent(model.StreamEvent{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Text: *block.Text}})
			}
			if block.Thinking != nil && *block.Thinking != "" {
				appendEvent(model.StreamEvent{Kind: model.StreamEventKindThinkingDelta, Delta: &model.StreamDelta{Thinking: *block.Thinking}})
			}
			if block.Signature != nil && *block.Signature != "" {
				appendEvent(model.StreamEvent{Kind: model.StreamEventKindSignatureDelta, Delta: &model.StreamDelta{Signature: *block.Signature}})
			}
			for _, citation := range block.Citations {
				appendEvent(model.StreamEvent{Kind: model.StreamEventKindCitationDelta, Citation: lo.ToPtr(citation)})
			}
		}
	case "content_block_delta":
		if event.Index == nil || *event.Index < 0 || event.Delta == nil || event.Delta.Type == nil {
			appendNative()
			break
		}
		index := int(*event.Index)
		state := o.blocks[index]
		switch *event.Delta.Type {
		case "text_delta":
			if event.Delta.Text == nil {
				appendNative()
				break
			}
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Text: *event.Delta.Text}})
		case "thinking_delta":
			if event.Delta.Thinking == nil {
				appendNative()
				break
			}
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindThinkingDelta, Delta: &model.StreamDelta{Thinking: *event.Delta.Thinking}})
		case "signature_delta":
			if event.Delta.Signature == nil {
				appendNative()
				break
			}
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindSignatureDelta, Delta: &model.StreamDelta{Signature: *event.Delta.Signature}})
		case "citations_delta":
			if event.Delta.Citation == nil {
				appendNative()
				break
			}
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindCitationDelta, Citation: event.Delta.Citation})
		case "input_json_delta":
			if state == nil || state.tool == nil || event.Delta.PartialJSON == nil {
				appendNative()
				break
			}
			arguments := *event.Delta.PartialJSON
			state.arguments += arguments
			tool := *state.tool
			tool.Function = model.FunctionCall{Arguments: arguments}
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindToolCallDelta, ToolCall: &tool, Delta: &model.StreamDelta{Arguments: arguments}})
		default:
			appendNative()
		}
	case "content_block_stop":
		if event.Index == nil {
			appendNative()
			break
		}
		state := o.blocks[int(*event.Index)]
		if state == nil || state.stopped {
			appendNative()
			break
		}
		state.stopped = true
		if state.tool != nil {
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindToolCallStop, ToolCall: &model.ToolCall{Index: state.tool.Index, ID: state.tool.ID}})
		} else {
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindContentBlockStop, ContentBlock: &model.StreamContentBlock{Type: state.kind}})
		}
	case "message_delta":
		appendUsage(event.Usage)
		if event.Delta != nil && event.Delta.StopReason != nil {
			if reason := convertStopReason(event.Delta.StopReason); reason != nil {
				o.finished = true
				appendEvent(model.StreamEvent{Kind: model.StreamEventKindMessageStop, StopReason: model.ParseFinishReason(*reason), StopSequence: event.Delta.StopSequence})
			}
		}
	case "message_stop":
		o.terminal = true
		if o.streamUsage != nil {
			appendEvent(model.StreamEvent{Kind: model.StreamEventKindUsageDelta, Usage: o.streamUsage})
		}
		appendEvent(model.StreamEvent{Kind: model.StreamEventKindDone})
	case "error":
		if event.Error == nil {
			return nil, fmt.Errorf("Anthropic error event missing error")
		}
		err := &model.ResponseError{StatusCode: mapAnthropicErrorTypeToStatus(event.Error.Type), Detail: model.ErrorDetail{Type: event.Error.Type, Message: event.Error.Message}}
		o.streamError = err
		appendEvent(model.StreamEvent{Kind: model.StreamEventKindError, Error: err})
	default:
		appendNative()
	}
	if len(events) == 0 {
		appendNative()
	}
	return events, nil
}

func (o *MessageOutbound) EndStream(ctx context.Context) ([]model.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.streamError != nil {
		return nil, o.streamError
	}
	if !o.terminal {
		if o.finished {
			o.terminal = true
			return []model.StreamEvent{{Kind: model.StreamEventKindDone}}, nil
		}
		return nil, fmt.Errorf("Anthropic stream ended before message_stop")
	}
	return nil, nil
}

func (o *MessageOutbound) CloseStream() error {
	o.closed = true
	o.blocks = nil
	o.toolCalls = nil
	o.wireUsage = nil
	return nil
}
