package relay

import (
	"bytes"
	"encoding/json"
	"strings"
)

type responseModelTrace struct {
	UpstreamRequestModel  string
	UpstreamResponseModel string
	ModelMismatch         bool
}

func (trace *responseModelTrace) observe(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if trace.UpstreamRequestModel != "" && name != trace.UpstreamRequestModel {
		trace.ModelMismatch = true
		trace.UpstreamResponseModel = name
	} else if !trace.ModelMismatch {
		trace.UpstreamResponseModel = name
	}
}

func mapResponseModelJSON(data []byte, downstreamModel string, observe func(string)) []byte {
	var payload map[string]json.RawMessage
	if json.Unmarshal(data, &payload) != nil || payload == nil {
		return data
	}
	changed := false
	rewrite := func(object map[string]json.RawMessage) bool {
		updated := false
		for _, field := range []string{"model", "modelVersion"} {
			var name string
			if json.Unmarshal(object[field], &name) != nil {
				continue
			}
			if observe != nil {
				observe(name)
			}
			if downstreamModel != "" && name != downstreamModel {
				object[field], _ = json.Marshal(downstreamModel)
				updated = true
			}
		}
		return updated
	}
	changed = rewrite(payload)
	var kind string
	_ = json.Unmarshal(payload["type"], &kind)
	for _, key := range []string{"response", "message"} {
		if (key == "response" && !strings.HasPrefix(kind, "response.")) || (key == "message" && kind != "message_start") {
			continue
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(payload[key], &nested) == nil && nested != nil && rewrite(nested) {
			payload[key], _ = json.Marshal(nested)
			changed = true
		}
	}
	if !changed {
		return data
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return data
	}
	return rewritten
}

func mapResponseModelSSE(data []byte, downstreamModel string, observe func(string)) []byte {
	normalized := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	frames := strings.Split(normalized, "\n\n")
	changed := false
	for index, frame := range frames {
		lines := strings.Split(frame, "\n")
		var values []string
		for _, line := range lines {
			if line == "data" {
				values = append(values, "")
			} else if strings.HasPrefix(line, "data:") {
				values = append(values, strings.TrimPrefix(line[5:], " "))
			}
		}
		original := []byte(strings.Join(values, "\n"))
		rewritten := mapResponseModelJSON(original, downstreamModel, observe)
		if bytes.Equal(original, rewritten) {
			continue
		}
		changed = true
		output := make([]string, 0, len(lines))
		inserted := false
		for _, line := range lines {
			if line == "data" || strings.HasPrefix(line, "data:") {
				if !inserted {
					output = append(output, "data: "+string(rewritten))
					inserted = true
				}
			} else {
				output = append(output, line)
			}
		}
		frames[index] = strings.Join(output, "\n")
	}
	if !changed {
		return data
	}
	return []byte(strings.Join(frames, "\n\n"))
}

func (ra *relayAttempt) observeResponseModel(data []byte) {
	mapResponseModelJSON(data, "", ra.observeResponseModelName)
}

func (ra *relayAttempt) observeResponseModelName(name string) {
	if ra.metrics == nil {
		return
	}
	if ra.metrics.UpstreamRequestModel == "" && ra.internalRequest != nil {
		ra.metrics.UpstreamRequestModel = strings.TrimSpace(ra.internalRequest.Model)
	}
	ra.metrics.observe(name)
}
