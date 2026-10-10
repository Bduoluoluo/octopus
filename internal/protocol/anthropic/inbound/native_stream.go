package inbound

import (
	"encoding/json"
	"strings"

	wire "github.com/xuanli27/octopus/internal/protocol/anthropic"
	"github.com/xuanli27/octopus/internal/transformer/model"
)

func (i *MessagesInbound) restoreStreamFrame(event model.StreamEvent) ([]byte, bool, error) {
	if event.ProviderExtensions == nil || event.ProviderExtensions.Anthropic == nil || event.Kind == model.StreamEventKindError {
		return nil, false, nil
	}
	fields := event.ProviderExtensions.Anthropic.Fields
	if string(fields["stream_frame_projection"]) != "true" {
		return nil, false, nil
	}
	raw := fields["stream_frame"]
	if len(raw) == 0 {
		return nil, true, nil
	}
	var frame wire.StreamEvent
	if err := json.Unmarshal(raw, &frame); err != nil {
		return nil, true, err
	}
	var eventName, eventID string
	if err := json.Unmarshal(fields["stream_frame_event"], &eventName); err != nil {
		return nil, true, err
	}
	if err := json.Unmarshal(fields["stream_frame_id"], &eventID); err != nil {
		return nil, true, err
	}
	if frame.Type == "" {
		frame.Type = eventName
	}
	switch frame.Type {
	case "message_start":
		i.hasStarted = true
		if frame.Message != nil {
			i.messageID, i.modelName = frame.Message.ID, frame.Message.Model
			if event.Model != "" && event.Model != frame.Message.Model {
				var envelope, message model.ProtocolFields
				if err := json.Unmarshal(raw, &envelope); err != nil {
					return nil, true, err
				}
				if err := json.Unmarshal(envelope["message"], &message); err != nil {
					return nil, true, err
				}
				message["model"], _ = json.Marshal(event.Model)
				envelope["message"], _ = json.Marshal(message)
				var err error
				raw, err = json.Marshal(envelope)
				if err != nil {
					return nil, true, err
				}
				i.modelName = event.Model
			}
		}
	case "message_delta":
		if frame.Delta != nil && frame.Delta.StopReason != nil {
			i.stopReason, i.stopSequence, i.hasFinished = frame.Delta.StopReason, frame.Delta.StopSequence, true
		}
	case "message_stop":
		i.messageStopped = true
	}
	var output strings.Builder
	if eventName != "" {
		output.WriteString("event:" + eventName + "\n")
	}
	if eventID != "" {
		output.WriteString("id:" + eventID + "\n")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		output.WriteString("data:" + line + "\n")
	}
	output.WriteByte('\n')
	return []byte(output.String()), true, nil
}
