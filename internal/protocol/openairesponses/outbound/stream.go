package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	wire "github.com/xuanli27/octopus/internal/protocol/openairesponses"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

type responseFrameState struct {
	started     bool
	terminal    bool
	done        bool
	closed      bool
	items       map[int]*responseItemState
	itemIndexes map[string]int
	usage       *model.Usage
}

type responseItemState struct {
	item      wire.Item
	started   bool
	stopped   bool
	arguments string
	text      map[string]string
	citations map[string]bool
}

func (outbound *ResponseOutbound) ResetStream() {
	*outbound = ResponseOutbound{}
}

func (outbound *ResponseOutbound) CloseStream() error {
	if outbound.frameState != nil {
		outbound.frameState.closed = true
	}
	return nil
}

func (outbound *ResponseOutbound) EndStream(ctx context.Context) ([]model.StreamEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := outbound.frameState
	if state == nil || !state.terminal {
		return nil, fmt.Errorf("responses stream ended without terminal event: %w", io.ErrUnexpectedEOF)
	}
	if state.done {
		return nil, nil
	}
	state.done = true
	return []model.StreamEvent{{Kind: model.StreamEventKindDone, ID: outbound.streamID, Model: outbound.streamModel}}, nil
}

func (outbound *ResponseOutbound) TransformStreamFrame(ctx context.Context, frame model.StreamFrame) ([]model.StreamEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if outbound.frameState == nil {
		outbound.frameState = &responseFrameState{items: make(map[int]*responseItemState), itemIndexes: make(map[string]int)}
		outbound.outputItems = make(map[int]ResponsesItem)
	}
	state := outbound.frameState
	if state.closed {
		return nil, fmt.Errorf("responses stream is closed")
	}
	data := bytes.TrimSpace(frame.Data)
	if len(data) == 0 {
		return nil, nil
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		if !state.terminal {
			return nil, nil
		}
		return outbound.EndStream(ctx)
	}
	if state.terminal {
		return nil, nil
	}
	var event wire.StreamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("decode responses frame: %w", err)
	}
	if event.Type == "" {
		event.Type = frame.Event
	}
	if event.Type == "" {
		return nil, fmt.Errorf("responses frame has no event type")
	}
	if event.Response != nil {
		if event.Response.ID != "" {
			outbound.streamID = event.Response.ID
		}
		if event.Response.Model != "" {
			outbound.streamModel = event.Response.Model
		}
		if event.Response.Usage != nil {
			state.usage = convertResponsesUsage(event.Response.Usage)
		}
	}
	base := model.StreamEvent{ID: outbound.streamID, Model: outbound.streamModel, OutputIndex: &event.OutputIndex, ContentIndex: event.ContentIndex, SequenceNumber: &event.SequenceNumber, ItemID: stringValue(event.ItemID), CallID: event.CallID}
	if event.Response != nil {
		base.Created = event.Response.CreatedAt
		base.ServiceTier = stringValue(event.Response.ServiceTier)
	}
	var events []model.StreamEvent
	nativeOnly := false
	emit := func(kind model.StreamEventKind) *model.StreamEvent {
		value := base
		value.Kind = kind
		events = append(events, value)
		return &events[len(events)-1]
	}
	if !state.started && (event.Type == "response.created" || event.Type == "response.in_progress") {
		state.started = true
		emit(model.StreamEventKindMessageStart).Role = "assistant"
	}
	index := event.OutputIndex
	if event.ItemID != nil {
		if known, ok := state.itemIndexes[*event.ItemID]; ok {
			index = known
		}
	}
	base.OutputIndex = &index
	item := state.items[index]
	if item == nil {
		item = &responseItemState{text: make(map[string]string)}
		state.items[index] = item
	}
	if event.Item != nil && event.Item.ID != "" {
		state.itemIndexes[event.Item.ID] = index
	}
	if event.ItemID != nil && item.item.ID == "" {
		item.item.ID = *event.ItemID
	}
	switch event.Type {
	case "response.created", "response.in_progress", "response.queued", "response.metadata", "response.reasoning.done":
	case "response.output_item.added", "response.output_item.done":
		if event.Item == nil {
			return nil, fmt.Errorf("%s missing item", event.Type)
		}
		if err := outbound.consumeItem(index, *event.Item, event.Type == "response.output_item.done", base, &events); err != nil {
			return nil, err
		}
	case "response.content_part.added", "response.content_part.done":
		if event.Part == nil {
			return nil, fmt.Errorf("%s missing part", event.Type)
		}
		kind := model.StreamEventKindContentBlockStart
		if event.Type == "response.content_part.done" {
			kind = model.StreamEventKindContentBlockStop
		}
		blockType := event.Part.Type
		if blockType == "output_text" || blockType == "refusal" {
			blockType = "text"
		}
		if blockType == "reasoning" {
			blockType = "thinking"
		}
		if kind == model.StreamEventKindContentBlockStart {
			emit(kind).ContentBlock = &model.StreamContentBlock{Type: blockType}
		}
		text := stringValue(event.Part.Text)
		if event.Part.Type == "refusal" && event.Part.Refusal != nil {
			text = *event.Part.Refusal
		}
		if err := outbound.consumeText(index, event.ContentIndex, event.Part.Type, text, true, base, &events); err != nil {
			return nil, err
		}
		for _, annotation := range event.Part.Annotations {
			citation := model.ContentCitation{Type: annotation.Type, URL: annotation.URL, Title: annotation.Title, StartIndex: annotation.StartIndex, EndIndex: annotation.EndIndex, Fields: annotation.Fields}
			if annotation.URLCitation != nil {
				citation.URL = &annotation.URLCitation.URL
				citation.Title = &annotation.URLCitation.Title
			}
			if err := outbound.consumeCitation(index, event.ContentIndex, citation, base, &events); err != nil {
				return nil, err
			}
		}
		if kind == model.StreamEventKindContentBlockStop {
			emit(kind).ContentBlock = &model.StreamContentBlock{Type: blockType}
		}
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		if event.Part == nil {
			return nil, fmt.Errorf("%s missing part", event.Type)
		}
		if err := outbound.consumeText(index, event.SummaryIndex, "reasoning", stringValue(event.Part.Text), true, base, &events); err != nil {
			return nil, err
		}
	case "response.output_text.delta", "response.output_text.done", "response.refusal.delta", "response.refusal.done", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_text.delta", "response.reasoning_text.done":
		kind := "output_text"
		if strings.Contains(event.Type, "refusal") {
			kind = "refusal"
		}
		if strings.Contains(event.Type, "reasoning") {
			kind = "reasoning"
		}
		delta := event.Delta
		done := strings.HasSuffix(event.Type, ".done")
		if done {
			delta = event.Text
		}
		if done && kind == "refusal" {
			delta = event.Refusal
			if delta == "" {
				delta = event.Text
			}
		}
		partIndex := event.ContentIndex
		if kind == "reasoning" {
			partIndex = event.SummaryIndex
		}
		if err := outbound.consumeText(index, partIndex, kind, delta, done, base, &events); err != nil {
			return nil, err
		}
	case "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		if item.item.Type == "" {
			item.item.Type = "function_call"
			if strings.Contains(event.Type, "custom_tool") {
				item.item.Type = "custom_tool_call"
			}
		}
		if event.Name != "" {
			item.item.Name = event.Name
		}
		if event.Namespace != "" {
			item.item.Namespace = event.Namespace
		}
		if event.CallID != "" {
			item.item.CallID = event.CallID
		}
		value := event.Delta
		done := strings.HasSuffix(event.Type, ".done")
		if done {
			value = event.Arguments
			if item.item.Type == "custom_tool_call" {
				value = event.Input
			}
		}
		if err := outbound.consumeArguments(index, value, done, base, &events); err != nil {
			return nil, err
		}
	case "response.output_text.annotation.added":
		var annotation struct {
			Annotation model.ContentCitation `json:"annotation"`
		}
		if err := json.Unmarshal(data, &annotation); err != nil {
			return nil, err
		}
		if err := outbound.consumeCitation(index, event.ContentIndex, annotation.Annotation, base, &events); err != nil {
			return nil, err
		}
	case "response.completed", "response.incomplete", "response.failed", "response.cancelled", "response.canceled", "error":
		if event.Type == "response.completed" && event.Response == nil {
			return nil, fmt.Errorf("%s missing response", event.Type)
		}
		if event.Response != nil {
			for outputIndex, output := range event.Response.Output {
				if err := outbound.consumeItem(outputIndex, output, true, base, &events); err != nil {
					return nil, err
				}
			}
		}
		if event.Response == nil || len(event.Response.Output) == 0 {
			indices := make([]int, 0, len(outbound.outputItems))
			for position := range outbound.outputItems {
				indices = append(indices, position)
			}
			sort.Ints(indices)
			for _, position := range indices {
				if err := outbound.consumeItem(position, outbound.outputItems[position], true, base, &events); err != nil {
					return nil, err
				}
			}
		}
		var reasoningIndices []int
		for position, output := range outbound.outputItems {
			if output.Type == "reasoning" && output.EncryptedContent != nil && *output.EncryptedContent != "" {
				reasoningIndices = append(reasoningIndices, position)
			}
		}
		sort.Ints(reasoningIndices)
		for _, position := range reasoningIndices {
			output := outbound.outputItems[position]
			signature := emit(model.StreamEventKindSignatureDelta)
			signature.OutputIndex = &position
			signature.ContentIndex = nil
			signature.ItemID = output.ID
			signature.Delta = &model.StreamDelta{Signature: *output.EncryptedContent}
		}
		state.terminal = true
		status := strings.TrimPrefix(event.Type, "response.")
		if event.Response != nil && event.Response.Status != nil {
			status = *event.Response.Status
		}
		detail := event.Error
		if event.Response != nil && event.Response.Error != nil {
			detail = event.Response.Error
		}
		if detail == nil && (event.Type == "error" || status == "failed" || status == "cancelled" || status == "canceled") {
			detail = &wire.Error{Code: event.Code, Message: event.Message}
			if detail.Message == "" {
				detail.Message = "upstream response " + status
			}
		}
		if detail != nil {
			emit(model.StreamEventKindError).Error = &model.ResponseError{StatusCode: event.Status, Detail: model.ErrorDetail{Type: detail.Type, Code: detail.Code, Message: detail.Message, Param: detail.Param}}
		}
		stop := emit(model.StreamEventKindMessageStop)
		stop.Status = status
		stop.StopReason = model.ParseFinishReason("stop")
		if status == "incomplete" {
			stop.StopReason = model.ParseFinishReason("length")
			if event.Response != nil && event.Response.IncompleteDetails != nil {
				stop.IncompleteDetails, _ = json.Marshal(event.Response.IncompleteDetails)
				if event.Response.IncompleteDetails.Reason == "content_filter" {
					stop.StopReason = model.ParseFinishReason("content_filter")
				}
			}
		} else if len(outbound.toolCallIndexes) > 0 {
			stop.StopReason = model.ParseFinishReason("tool_calls")
		}
		raw, err := outbound.rawOutputItems()
		if err != nil {
			return nil, err
		}
		stop.ProviderExtensions = &model.ProviderExtensions{OpenAI: &model.OpenAIExtension{RawResponseItems: raw}}
		if state.usage != nil {
			usage := emit(model.StreamEventKindUsageDelta)
			usage.Usage = state.usage
			usage.ProviderExtensions = &model.ProviderExtensions{OpenAI: &model.OpenAIExtension{RawResponseItems: raw}}
		}
	default:
		nativeOnly = true
	}
	if event.Response != nil && event.Response.Usage != nil && !state.terminal {
		emit(model.StreamEventKindUsageDelta).Usage = state.usage
	}
	var rawFrame model.ProtocolFields
	if err := json.Unmarshal(data, &rawFrame); err != nil {
		return nil, err
	}
	rawFrame["type"], _ = json.Marshal(event.Type)
	raw, err := json.Marshal(rawFrame)
	if err != nil {
		return nil, err
	}
	if nativeOnly {
		native := emit(model.StreamEventKindNativeItem)
		native.NativeItem = &model.ProtocolItem{Format: model.APIFormatOpenAIResponse, Position: -1, Raw: raw}
		native.ProviderExtensions = &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Fields: model.ProtocolFields{"stream_frame": raw}}}
	} else if event.Item != nil && event.Item.Type != "message" && event.Item.Type != "function_call" && event.Item.Type != "reasoning" {
		if len(events) == 0 {
			emit(model.StreamEventKindNativeItem)
		}
		native := &events[len(events)-1]
		if native.ProviderExtensions == nil {
			native.ProviderExtensions = &model.ProviderExtensions{}
		}
		native.ProviderExtensions.OpenAIResponses = &model.ProtocolExtension{Fields: model.ProtocolFields{"stream_frame": raw}}
		encoded, err := json.Marshal(event.Item)
		if err != nil {
			return nil, err
		}
		native.NativeItem = &model.ProtocolItem{Format: model.APIFormatOpenAIResponse, Position: index, Raw: encoded}
	} else {
		if len(events) == 0 {
			emit(model.StreamEventKindMetadata)
		}
		metadata := &events[len(events)-1]
		if metadata.ProviderExtensions == nil {
			metadata.ProviderExtensions = &model.ProviderExtensions{}
		}
		metadata.ProviderExtensions.OpenAIResponses = &model.ProtocolExtension{Fields: model.ProtocolFields{"stream_frame": raw}}
	}
	return events, nil
}

func (outbound *ResponseOutbound) consumeArguments(index int, value string, done bool, base model.StreamEvent, events *[]model.StreamEvent) error {
	state := outbound.frameState.items[index]
	if state.stopped && !done {
		return fmt.Errorf("arguments received after item %d closed", index)
	}
	tool := model.ToolCall{Index: outbound.toolCallIndexFor(index), ID: state.item.CallID, Type: "function", Function: model.FunctionCall{Name: state.item.Name}}
	if state.item.Type == "custom_tool_call" {
		tool.Type = "custom"
	}
	tool.ProviderExtensions = &model.ProviderExtensions{OpenAIResponses: &model.ProtocolExtension{Fields: model.ProtocolFields{}}}
	tool.ProviderExtensions.OpenAIResponses.Fields["namespace"], _ = json.Marshal(state.item.Namespace)
	tool.ProviderExtensions.OpenAIResponses.Fields["item_id"], _ = json.Marshal(state.item.ID)
	if !state.started {
		state.started = true
		event := base
		event.Kind = model.StreamEventKindToolCallStart
		event.ToolCall = &tool
		*events = append(*events, event)
	}
	delta := value
	if done {
		var err error
		delta, err = missingSuffix(state.arguments, value, state.item.Type == "function_call")
		if err != nil {
			return fmt.Errorf("tool %q: %w", state.item.CallID, err)
		}
	} else {
		state.arguments += value
	}
	if done && value != "" {
		state.arguments = value
	}
	if delta != "" {
		fragment := tool
		fragment.Function.Arguments = ""
		event := base
		event.Kind = model.StreamEventKindToolCallDelta
		event.ToolCall = &fragment
		event.Delta = &model.StreamDelta{Arguments: delta}
		*events = append(*events, event)
	}
	if state.item.Type == "custom_tool_call" {
		value := state.arguments
		state.item.Input = &value
	} else {
		state.item.Arguments = state.arguments
	}
	outbound.outputItems[index] = state.item
	return nil
}

func missingSuffix(previous, final string, compareJSON bool) (string, error) {
	if final == "" {
		return "", nil
	}
	if strings.HasPrefix(final, previous) {
		return strings.TrimPrefix(final, previous), nil
	}
	if compareJSON {
		var left, right any
		first := json.NewDecoder(strings.NewReader(previous))
		first.UseNumber()
		second := json.NewDecoder(strings.NewReader(final))
		second.UseNumber()
		if first.Decode(&left) == nil && second.Decode(&right) == nil && reflect.DeepEqual(left, right) {
			return "", nil
		}
	}
	return "", fmt.Errorf("final content disagrees with streamed content")
}

func (outbound *ResponseOutbound) consumeText(index int, contentIndex *int, kind, value string, done bool, base model.StreamEvent, events *[]model.StreamEvent) error {
	state := outbound.frameState.items[index]
	position := 0
	if contentIndex != nil {
		position = *contentIndex
	}
	key := fmt.Sprintf("%s:%d", kind, position)
	delta := value
	if done {
		var err error
		delta, err = missingSuffix(state.text[key], value, false)
		if err != nil {
			return err
		}
		if value != "" {
			state.text[key] = value
		}
	} else {
		state.text[key] += value
	}
	if delta != "" {
		event := base
		event.ContentIndex = &position
		event.OutputIndex = &index
		event.Kind = model.StreamEventKindTextDelta
		event.Delta = &model.StreamDelta{Text: delta}
		if kind == "refusal" {
			event.Delta = &model.StreamDelta{Refusal: delta}
		}
		if kind == "reasoning" {
			event.Kind = model.StreamEventKindThinkingDelta
			event.Delta = &model.StreamDelta{Thinking: delta}
		}
		*events = append(*events, event)
	}
	if kind == "reasoning" {
		state.item.Type = "reasoning"
		for len(state.item.Summary) <= position {
			state.item.Summary = append(state.item.Summary, wire.ReasoningSummary{Type: "summary_text"})
		}
		state.item.Summary[position].Text = state.text[key]
	} else {
		if state.item.Type == "" {
			state.item.Type = "message"
			state.item.Role = "assistant"
		}
		if state.item.Content == nil {
			state.item.Content = &wire.Input{}
		}
		for len(state.item.Content.Items) <= position {
			state.item.Content.Items = append(state.item.Content.Items, wire.Item{})
		}
		part := &state.item.Content.Items[position]
		part.Type = kind
		text := state.text[key]
		if kind == "refusal" {
			part.Refusal = &text
		} else {
			part.Text = &text
		}
	}
	outbound.outputItems[index] = state.item
	return nil
}

func (outbound *ResponseOutbound) consumeItem(index int, value wire.Item, done bool, base model.StreamEvent, events *[]model.StreamEvent) error {
	state := outbound.frameState.items[index]
	if state == nil {
		state = &responseItemState{text: make(map[string]string)}
		outbound.frameState.items[index] = state
	}
	if value.ID != "" {
		outbound.frameState.itemIndexes[value.ID] = index
	}
	previous := state.item
	if value.ID == "" {
		value.ID = previous.ID
	}
	if value.CallID == "" {
		value.CallID = previous.CallID
	}
	if value.Name == "" {
		value.Name = previous.Name
	}
	if value.Namespace == "" {
		value.Namespace = previous.Namespace
	}
	if value.Content == nil {
		value.Content = cloneResponsesInput(previous.Content)
	}
	if len(value.Summary) == 0 {
		value.Summary = append([]wire.ReasoningSummary(nil), previous.Summary...)
	}
	if value.EncryptedContent == nil {
		value.EncryptedContent = previous.EncryptedContent
	}
	base.OutputIndex = &index
	base.ItemID = value.ID
	base.CallID = value.CallID
	state.item = value
	switch value.Type {
	case "function_call", "custom_tool_call":
		arguments := value.Arguments
		if value.Type == "custom_tool_call" {
			arguments = stringValue(value.Input)
		}
		if err := outbound.consumeArguments(index, arguments, true, base, events); err != nil {
			return err
		}
		if done && !state.stopped {
			event := base
			event.Kind = model.StreamEventKindToolCallStop
			*events = append(*events, event)
		}
	case "message":
		if value.Content != nil {
			for contentIndex, part := range value.Content.Items {
				text := stringValue(part.Text)
				if part.Type == "refusal" {
					text = stringValue(part.Refusal)
				}
				if err := outbound.consumeText(index, &contentIndex, part.Type, text, true, base, events); err != nil {
					return err
				}
				for _, annotation := range part.Annotations {
					citation := model.ContentCitation{Type: annotation.Type, URL: annotation.URL, Title: annotation.Title, StartIndex: annotation.StartIndex, EndIndex: annotation.EndIndex, Fields: annotation.Fields}
					if annotation.URLCitation != nil {
						citation.URL = &annotation.URLCitation.URL
						citation.Title = &annotation.URLCitation.Title
					}
					if err := outbound.consumeCitation(index, &contentIndex, citation, base, events); err != nil {
						return err
					}
				}
			}
		}
	case "reasoning":
		for summaryIndex, summary := range value.Summary {
			if err := outbound.consumeText(index, &summaryIndex, "reasoning", summary.Text, true, base, events); err != nil {
				return err
			}
		}
	}
	state.item = value
	if value.Type == "function_call" {
		state.item.Arguments = state.arguments
	}
	if done {
		state.stopped = true
	}
	outbound.outputItems[index] = state.item
	return nil
}

func (outbound *ResponseOutbound) rawOutputItems() (json.RawMessage, error) {
	indices := make([]int, 0, len(outbound.outputItems))
	for index := range outbound.outputItems {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	items := make([]wire.Item, 0, len(indices))
	for _, index := range indices {
		items = append(items, outbound.outputItems[index])
	}
	return json.Marshal(items)
}

func (outbound *ResponseOutbound) consumeCitation(index int, contentIndex *int, citation model.ContentCitation, base model.StreamEvent, events *[]model.StreamEvent) error {
	state := outbound.frameState.items[index]
	if state.citations == nil {
		state.citations = make(map[string]bool)
	}
	position := 0
	if contentIndex != nil {
		position = *contentIndex
	}
	raw, err := json.Marshal(citation)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%d:%s", position, raw)
	if state.citations[key] {
		return nil
	}
	state.citations[key] = true
	event := base
	event.Kind = model.StreamEventKindCitationDelta
	event.OutputIndex = &index
	event.ContentIndex = &position
	event.Citation = &citation
	*events = append(*events, event)
	if state.item.Content == nil {
		state.item.Content = &wire.Input{}
	}
	for len(state.item.Content.Items) <= position {
		state.item.Content.Items = append(state.item.Content.Items, wire.Item{Type: "output_text"})
	}
	part := &state.item.Content.Items[position]
	part.Annotations = append(part.Annotations, wire.Annotation{Type: citation.Type, URL: citation.URL, Title: citation.Title, StartIndex: citation.StartIndex, EndIndex: citation.EndIndex, Fields: citation.Fields})
	outbound.outputItems[index] = state.item
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var _ model.OutboundStreamFrameTransformer = (*ResponseOutbound)(nil)
