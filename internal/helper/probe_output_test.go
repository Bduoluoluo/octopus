package helper

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestResponsesProbeOutputAssembly(t *testing.T) {
	for _, test := range []struct {
		name   string
		events []string
		want   string
	}{
		{"deltas", []string{
			`{"type":"response.output_text.delta","delta":"Hello"}`,
			`{"type":"response.output_text.delta","delta":" "}`,
			`{"type":"response.output_text.delta","delta":"世界"}`,
			`{"type":"response.done"}`,
		}, "Hello 世界"},
		{"completed snapshot replaces deltas", []string{
			`{"type":"response.output_text.delta","delta":"Hello"}`,
			`{"type":"response.completed","response":{"output":[{"content":[{"text":"Hello world"}]}]}}`,
		}, "Hello world"},
		{"multiple content parts", []string{
			`{"type":"response.done","response":{"output":[{"content":[{"text":"你好"},{"text":"\n世界"}]}]}}`,
		}, "你好\n世界"},
		{"refusal", []string{
			`{"type":"response.refusal.delta","delta":"Cannot comply"}`,
			`{"type":"response.completed"}`,
		}, "Cannot comply"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body strings.Builder
			for _, event := range test.events {
				body.WriteString("data: " + event + "\n\n")
			}
			response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body.String()))}
			defer response.Body.Close()
			output, err := ReadResponsesProbe(response)
			if err != nil || output != test.want {
				t.Fatalf("output=%q err=%v, want %q", output, err, test.want)
			}
		})
	}
}

func TestExtractProbeOutput(t *testing.T) {
	for _, test := range []struct{ name, body, want string }{
		{"anthropic", `{"content":[{"type":"text","text":"pulpo"}]}`, "pulpo"},
		{"chat", `{"choices":[{"message":{"content":"pulpo"}}]}`, "pulpo"},
		{"chat parts", `{"choices":[{"message":{"content":[{"type":"text","text":"你好"}]}}]}`, "你好"},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"pulpo"}]}}]}`, "pulpo"},
		{"embedding", `{"data":[{"embedding":[0.1,0.2]}]}`, `{"data":[{"embedding":[0.1,0.2]}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if output := ExtractProbeOutput([]byte(test.body)); output != test.want {
				t.Fatalf("got %q, want %q", output, test.want)
			}
		})
	}
}

func TestProbeOutputTruncation(t *testing.T) {
	text := strings.Repeat("中", maxProbeOutputBytes)
	var output probeOutput
	for _, chunk := range []string{text[:9], text[9:], "ignored"} {
		output.append(chunk)
	}
	result := output.String()
	if !utf8.ValidString(result) || !strings.HasSuffix(result, "\n[truncated]") || len(result) > maxProbeOutputBytes+len("\n[truncated]") {
		t.Fatalf("invalid truncated output: bytes=%d valid=%v", len(result), utf8.ValidString(result))
	}
	body, err := json.Marshal(map[string]any{"content": []map[string]string{{"text": text}}})
	if err != nil {
		t.Fatal(err)
	}
	if extracted := ExtractProbeOutput(body); extracted != result {
		t.Fatal("non-streaming output uses inconsistent truncation")
	}
}
