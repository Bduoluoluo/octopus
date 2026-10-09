package openairesponses

import (
	"encoding/json"
	"testing"
)

func TestPinnedStreamEventStatusUnion(t *testing.T) {
	for _, fixture := range []struct {
		body   string
		status int
	}{
		{`{"type":"error","status":429}`, 429},
		{`{"type":"response.created","sequence_number":0,"status":"in_progress","response":{"id":"resp_1","model":"gpt-5","status":"in_progress","output":[]}}`, 0},
		{`{"type":"response.completed","sequence_number":1,"status":"completed","response":{"id":"resp_1","model":"gpt-5","status":"completed","output":[]}}`, 0},
	} {
		var event StreamEvent
		if err := json.Unmarshal([]byte(fixture.body), &event); err != nil {
			t.Fatal(err)
		}
		if event.Status != fixture.status {
			t.Fatalf("status=%d, want %d", event.Status, fixture.status)
		}
	}
}
