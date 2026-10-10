package inbound

import "testing"

func TestPreserveReasoningEffort(t *testing.T) {
	for _, effort := range []string{"minimal", "low", "medium", "high", "", "turbo", "ultra", "MEDIUM", " low"} {
		request, err := convertToInternalRequest(&ResponsesRequest{Model: "m", Reasoning: &ResponsesReasoning{Effort: effort}})
		if err != nil || request.ReasoningEffort != effort {
			t.Fatalf("reasoning effort %q changed: %+v %v", effort, request, err)
		}
	}
}

func TestPreserveReasoningSummary(t *testing.T) {
	for _, summary := range []string{"auto", "concise", "detailed", "", "brief", "verbose", "Auto"} {
		request, err := convertToInternalRequest(&ResponsesRequest{Model: "m", Reasoning: &ResponsesReasoning{Summary: &summary}})
		if err != nil {
			t.Fatal(err)
		}
		options := request.GetOpenAIResponsesOptions()
		if options.ReasoningSummary == nil || *options.ReasoningSummary != summary {
			t.Fatalf("reasoning summary %q changed: %+v", summary, options)
		}
	}
}

func TestResponsesTerminalEvent(t *testing.T) {
	cases := []struct {
		finish     string
		wantEvent  string
		wantStatus string
	}{
		{"stop", "response.completed", "completed"},
		{"tool_calls", "response.completed", "completed"},
		{"length", "response.incomplete", "incomplete"},
		{"pause_turn", "response.incomplete", "incomplete"},
		{"error", "response.failed", "failed"},
		{"malformed_function_call", "response.failed", "failed"},
		{"safety", "response.incomplete", "incomplete"},
		{"recitation", "response.incomplete", "incomplete"},
		{"content_filter", "response.incomplete", "incomplete"},
		{"refusal", "response.completed", "completed"},
		{"prohibited_content", "response.incomplete", "incomplete"},
		{"spii", "response.incomplete", "incomplete"},
		{"image_safety", "response.incomplete", "incomplete"},
		{"", "response.completed", "completed"},
	}
	for _, tc := range cases {
		gotEvent, gotStatus := responsesTerminalEvent(tc.finish)
		if gotEvent != tc.wantEvent || gotStatus != tc.wantStatus {
			t.Errorf("responsesTerminalEvent(%q) = (%q, %q), want (%q, %q)",
				tc.finish, gotEvent, gotStatus, tc.wantEvent, tc.wantStatus)
		}
	}
}
