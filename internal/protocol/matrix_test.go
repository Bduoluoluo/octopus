package protocol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tmaxmax/go-sse"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

type publicFixture struct {
	name     string
	request  string
	response string
	stream   []string
}

func publicFixtures() []publicFixture {
	return []publicFixture{
		{
			name:     "chat",
			request:  `{"model":"fixture","messages":[{"role":"system","content":"Be concise."},{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_history","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"history\"}"}}]},{"role":"tool","tool_call_id":"call_history","content":"history result"},{"role":"user","content":"continue"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}}]}`,
			response: `{"id":"chatcmpl_fixture","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"ok","tool_calls":[{"id":"call_answer","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"answer\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
			stream: []string{
				`{"id":"chatcmpl_fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
				`{"id":"chatcmpl_fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
				`{"id":"chatcmpl_fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_answer","type":"function","function":{"name":"lookup","arguments":"{\"query\":"}}]}}]}`,
				`{"id":"chatcmpl_fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"answer\"}"}}]}}]}`,
				`{"id":"chatcmpl_fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"id":"chatcmpl_fixture","object":"chat.completion.chunk","model":"fixture","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`,
				`[DONE]`,
			},
		},
		{
			name:     "responses",
			request:  `{"model":"fixture","instructions":"Be concise.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"function_call","call_id":"call_history","name":"lookup","arguments":"{\"query\":\"history\"}"},{"type":"function_call_output","call_id":"call_history","output":"history result"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}`,
			response: `{"id":"resp_fixture","object":"response","model":"fixture","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]},{"type":"function_call","id":"fc_1","call_id":"call_answer","name":"lookup","arguments":"{\"query\":\"answer\"}","status":"completed"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`,
			stream: []string{
				`{"type":"response.created","response":{"id":"resp_fixture","model":"fixture","status":"in_progress","output":[]}}`,
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
				`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"ok"}`,
				`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_answer","name":"lookup","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","call_id":"call_answer","name":"lookup","delta":"{\"query\":"}`,
				`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","call_id":"call_answer","name":"lookup","delta":"\"answer\"}"}`,
				`{"type":"response.completed","response":{"id":"resp_fixture","object":"response","model":"fixture","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]},{"type":"function_call","id":"fc_1","call_id":"call_answer","name":"lookup","arguments":"{\"query\":\"answer\"}","status":"completed"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
			},
		},
		{
			name:     "anthropic",
			request:  `{"model":"fixture","max_tokens":32,"system":"Be concise.","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"tool_use","id":"call_history","name":"lookup","input":{"query":"history"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_history","content":"history result"},{"type":"text","text":"continue"}]}],"tools":[{"name":"lookup","description":"look up","input_schema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}`,
			response: `{"id":"msg_fixture","type":"message","role":"assistant","model":"fixture","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"call_answer","name":"lookup","input":{"query":"answer"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`,
			stream: []string{
				`{"type":"message_start","message":{"id":"msg_fixture","type":"message","model":"fixture","role":"assistant","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_answer","name":"lookup","input":{}}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"answer\"}"}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
				`{"type":"message_stop"}`,
			},
		},
	}
}

func TestPublicProtocolConversionMatrix(t *testing.T) {
	fixtures := publicFixtures()
	for inputIndex, inputFixture := range fixtures {
		for outputIndex, outputFixture := range fixtures {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s_to_%s/stream=%t", inputFixture.name, outputFixture.name, streaming), func(t *testing.T) {
					ctx := context.Background()
					inAdapter := inbound.Get(inbound.InboundType(inputIndex))
					request, err := inAdapter.TransformRequest(ctx, []byte(inputFixture.request))
					if err != nil {
						t.Fatal(err)
					}
					request.Stream = &streaming
					server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, upstream *http.Request) {
						payload, err := io.ReadAll(upstream.Body)
						if err != nil {
							t.Error(err)
							writer.WriteHeader(500)
							return
						}
						parsed, err := inbound.Get(inbound.InboundType(outputIndex)).TransformRequest(ctx, payload)
						if err != nil {
							t.Error(err)
							writer.WriteHeader(400)
							return
						}
						assertPublicRequest(t, parsed)
						if !streaming {
							writer.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(writer, outputFixture.response)
							return
						}
						writer.Header().Set("Content-Type", "text/event-stream")
						for _, data := range outputFixture.stream {
							var envelope struct {
								Type string `json:"type"`
							}
							if data != "[DONE]" {
								if err := json.Unmarshal([]byte(data), &envelope); err != nil {
									t.Error(err)
									return
								}
							}
							if envelope.Type != "" {
								_, _ = fmt.Fprintf(writer, "event: %s\n", envelope.Type)
							}
							_, _ = fmt.Fprintf(writer, "data: %s\n\n", data)
						}
					}))
					defer server.Close()
					outAdapter := outbound.Get(outbound.OutboundType(outputIndex))
					upstreamRequest, err := outAdapter.TransformRequest(ctx, request, server.URL+"/v1", "fixture-key")
					if err != nil {
						t.Fatal(err)
					}
					response, err := server.Client().Do(upstreamRequest)
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					if response.StatusCode != 200 {
						t.Fatalf("upstream status %d", response.StatusCode)
					}
					var downstream bytes.Buffer
					if !streaming {
						internal, err := outAdapter.TransformResponse(ctx, response)
						if err != nil {
							t.Fatal(err)
						}
						encoded, err := inAdapter.TransformResponse(ctx, internal)
						if err != nil {
							t.Fatal(err)
						}
						downstream.Write(encoded)
						decoded, err := outbound.Get(outbound.OutboundType(inputIndex)).TransformResponse(ctx, &http.Response{StatusCode: 200, Body: io.NopCloser(&downstream), Header: make(http.Header)})
						if err != nil {
							t.Fatal(err)
						}
						assertPublicResponse(t, decoded)
					} else {
						decoder := outAdapter.(model.OutboundStreamFrameTransformer)
						encoder := inAdapter.(model.InboundStreamEventTransformer)
						for frame, err := range sse.Read(response.Body, &sse.ReadConfig{MaxEventSize: 65536}) {
							if err != nil {
								t.Fatal(err)
							}
							events, err := decoder.TransformStreamFrame(ctx, model.StreamFrame{Event: frame.Type, Data: []byte(frame.Data), ID: frame.LastEventID})
							if err != nil {
								t.Fatal(err)
							}
							data, err := encoder.TransformStreamEvents(ctx, events)
							if err != nil {
								t.Fatal(err)
							}
							downstream.Write(data)
						}
						finalEvents, err := decoder.EndStream(ctx)
						if err != nil {
							t.Fatal(err)
						}
						finalData, err := encoder.TransformStreamEvents(ctx, finalEvents)
						if err != nil {
							t.Fatal(err)
						}
						downstream.Write(finalData)
						if err := decoder.CloseStream(); err != nil {
							t.Fatal(err)
						}
						if !strings.Contains(downstream.String(), "ok") {
							t.Fatalf("missing text: %s", downstream.String())
						}
						aggregate, err := inAdapter.GetInternalResponse(ctx)
						if err != nil {
							t.Fatal(err)
						}
						assertPublicResponse(t, aggregate)
						wireDecoder := outbound.Get(outbound.OutboundType(inputIndex)).(model.OutboundStreamFrameTransformer)
						var wireAggregate model.StreamAggregator
						for frame, err := range sse.Read(bytes.NewReader(downstream.Bytes()), nil) {
							if err != nil {
								t.Fatal(err)
							}
							events, err := wireDecoder.TransformStreamFrame(ctx, model.StreamFrame{Event: frame.Type, Data: []byte(frame.Data), ID: frame.LastEventID})
							if err != nil {
								t.Fatalf("downstream stream invalid: %v\n%s", err, downstream.String())
							}
							wireAggregate.Add(model.InternalResponseFromStreamEvents(events))
						}
						if _, err := wireDecoder.EndStream(ctx); err != nil {
							t.Fatal(err)
						}
						assertPublicResponse(t, wireAggregate.Response())
						if err := wireDecoder.CloseStream(); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func assertPublicRequest(t *testing.T, request *model.InternalLLMRequest) {
	t.Helper()
	if request.Model != "fixture" {
		t.Errorf("model=%s", request.Model)
	}
	var instructions, text, result string
	var calls []model.ToolCall
	for _, message := range request.Messages {
		switch message.Role {
		case "system", "developer":
			instructions += contentText(message.Content)
		case "user":
			text += contentText(message.Content)
		case "tool":
			result += contentText(message.Content)
			if message.ToolCallID == nil || *message.ToolCallID != "call_history" {
				t.Errorf("lost tool result binding: %#v", message.ToolCallID)
			}
		}
		calls = append(calls, message.ToolCalls...)
	}
	if instructions != "Be concise." || !strings.Contains(text, "hello") || !strings.Contains(text, "continue") || result != "history result" {
		t.Errorf("request semantic loss: instructions=%q text=%q result=%q", instructions, text, result)
	}
	if len(calls) != 1 || calls[0].ID != "call_history" || calls[0].Function.Name != "lookup" || calls[0].Function.Arguments != `{"query":"history"}` {
		t.Errorf("tool history=%#v", calls)
	}
	if len(request.Tools) != 1 || request.Tools[0].Function.Name != "lookup" {
		t.Errorf("tools=%#v", request.Tools)
		return
	}
	var schema map[string]any
	if err := json.Unmarshal(request.Tools[0].Function.Parameters, &schema); err != nil {
		t.Error(err)
		return
	}
	if schema["type"] != "object" || schema["properties"] == nil || schema["required"] == nil {
		t.Errorf("tool schema=%#v", schema)
	}
}

func assertPublicResponse(t *testing.T, response *model.InternalLLMResponse) {
	t.Helper()
	if response == nil {
		t.Fatal("nil aggregate")
	}
	if len(response.Choices) != 1 || response.Choices[0].Message == nil {
		t.Fatalf("expected one assistant choice: %#v", response.Choices)
	}
	message := response.Choices[0].Message
	if contentText(message.Content) != "ok" {
		t.Errorf("text=%q", contentText(message.Content))
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls=%#v", message.ToolCalls)
	}
	call := message.ToolCalls[0]
	if call.ID != "call_answer" || call.Function.Name != "lookup" || call.Function.Arguments != `{"query":"answer"}` {
		t.Errorf("tool call=%#v", call)
	}
	if response.Usage == nil || response.Usage.EffectiveInputTokens() != 2 || response.Usage.CompletionTokens != 3 || response.Usage.TotalTokens != 5 {
		t.Errorf("usage=%#v", response.Usage)
	}
	if response.Choices[0].FinishReason == nil || *response.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish reason=%v", response.Choices[0].FinishReason)
	}
}

func contentText(content model.MessageContent) string {
	if content.Content != nil {
		return *content.Content
	}
	var text strings.Builder
	for _, part := range content.MultipleContent {
		if part.Text != nil {
			text.WriteString(*part.Text)
		}
	}
	return text.String()
}
