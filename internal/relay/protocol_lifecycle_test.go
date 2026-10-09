package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/xuanli27/octopus/internal/relay/stream"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

type lifecycleOutbound struct {
	model.Outbound
	frames        []model.StreamFrame
	terminal      bool
	endCalls      int
	closeCalls    int
	legacyCalls   int
	validationErr error
	closeErr      error
}

func (adapter *lifecycleOutbound) TransformStreamFrame(_ context.Context, frame model.StreamFrame) ([]model.StreamEvent, error) {
	frame.Data = bytes.Clone(frame.Data)
	adapter.frames = append(adapter.frames, frame)
	if adapter.validationErr != nil && bytes.Contains(frame.Data, []byte(`"invalid":true`)) {
		return nil, adapter.validationErr
	}
	var payload struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	}
	if len(frame.Data) > 0 {
		if err := json.Unmarshal(frame.Data, &payload); err != nil {
			return nil, err
		}
	}
	if frame.Event == "response.completed" || payload.Type == "response.completed" {
		adapter.terminal = true
		return nil, nil
	}
	if payload.Delta == "" {
		return []model.StreamEvent{{Kind: model.StreamEventKindMessageStart, Role: "assistant"}}, nil
	}
	return []model.StreamEvent{
		{Kind: model.StreamEventKindTextDelta, Delta: &model.StreamDelta{Text: payload.Delta}},
		{Kind: model.StreamEventKindUsageDelta, Usage: &model.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8}},
	}, nil
}

func (adapter *lifecycleOutbound) TransformStreamEvent(context.Context, []byte) ([]model.StreamEvent, error) {
	adapter.legacyCalls++
	return nil, errors.New("frame-aware adapter must not use legacy decoding")
}

func (adapter *lifecycleOutbound) EndStream(ctx context.Context) ([]model.StreamEvent, error) {
	adapter.endCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !adapter.terminal {
		return nil, io.ErrUnexpectedEOF
	}
	return []model.StreamEvent{{Kind: model.StreamEventKindDone}}, nil
}

func (adapter *lifecycleOutbound) CloseStream() error {
	adapter.closeCalls++
	return adapter.closeErr
}

type lifecycleInbound struct {
	model.Inbound
	aggregator model.StreamAggregator
	events     []model.StreamEvent
	resetCalls int
	encodeErr  error
}

func (adapter *lifecycleInbound) TransformStreamEvents(_ context.Context, events []model.StreamEvent) ([]byte, error) {
	adapter.events = append(adapter.events, events...)
	adapter.aggregator.Add(model.InternalResponseFromStreamEvents(events))
	if adapter.encodeErr != nil {
		return nil, adapter.encodeErr
	}
	var output bytes.Buffer
	for _, event := range events {
		if event.Delta != nil {
			output.WriteString("data: encoded-" + event.Delta.Text + "\n\n")
		}
		if event.Kind == model.StreamEventKindDone {
			output.WriteString("data: [DONE]\n\n")
		}
	}
	return output.Bytes(), nil
}

func (adapter *lifecycleInbound) GetInternalResponse(context.Context) (*model.InternalLLMResponse, error) {
	return adapter.aggregator.Response(), nil
}

func (adapter *lifecycleInbound) ResetStream() {
	adapter.resetCalls++
	adapter.events = nil
	adapter.aggregator.Reset()
}

func newLifecycleAttempt(t *testing.T) (*relayAttempt, *lifecycleOutbound, *lifecycleInbound) {
	t.Helper()
	attempt, _ := newEmptyStreamTestAttempt(t, inbound.InboundTypeOpenAIResponse, model.APIFormatOpenAIResponse, outbound.OutboundTypeOpenAIResponse)
	decoder := &lifecycleOutbound{}
	encoder := &lifecycleInbound{}
	attempt.outAdapter = decoder
	attempt.inAdapter = encoder
	return attempt, decoder, encoder
}

func runLifecycleSSE(attempt *relayAttempt, ctx context.Context, response *http.Response, passthrough bool) error {
	if passthrough {
		return attempt.handleStreamResponsePassthroughV2(ctx, response, model.PassthroughConfig{
			CollectMetrics: true,
			TerminalEvents: responsesPassthroughTerminalEvents,
		})
	}
	return attempt.handleStreamResponseV2(ctx, response)
}

func TestProtocolLifecycleSSEFrameAndSingleConsumption(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "transformed", true: "raw_sidecar"}[passthrough], func(t *testing.T) {
			attempt, decoder, encoder := newLifecycleAttempt(t)
			writer := &notifyStreamWriter{header: http.Header{}}
			attempt.streamWriter = writer
			body := ": heartbeat\r\n\r\nevent: response.created\r\nid: first\r\ndata: {}\r\n\r\n" +
				"event: response.output_text.delta\r\nid: second\r\ndata: {\r\ndata: \"delta\":\"hello\"}\r\n\r\n" +
				"event: response.completed\r\nid: third\r\ndata: {}\r\n\r\n"
			if err := runLifecycleSSE(attempt, context.Background(), sseTestResponse(body), passthrough); err != nil {
				t.Fatal(err)
			}
			if len(decoder.frames) != 3 || decoder.frames[0].Event != "response.created" || decoder.frames[0].ID != "first" || decoder.frames[1].Event != "response.output_text.delta" || decoder.frames[1].ID != "second" || decoder.frames[1].Data == nil || string(decoder.frames[1].Data) != "{\n\"delta\":\"hello\"}" || decoder.frames[2].Event != "response.completed" {
				t.Fatalf("SSE frame boundary/fields lost: %+v", decoder.frames)
			}
			if decoder.endCalls != 1 || decoder.closeCalls != 1 || decoder.legacyCalls != 0 || encoder.resetCalls != 0 {
				t.Fatalf("unexpected lifecycle: decoder=%+v resets=%d", decoder, encoder.resetCalls)
			}
			if len(encoder.events) != 4 {
				t.Fatalf("events consumed more than once: %+v", encoder.events)
			}
			response, err := encoder.GetInternalResponse(context.Background())
			if err != nil || response == nil || len(response.Choices) != 1 || response.Choices[0].Message.Content.Content == nil || *response.Choices[0].Message.Content.Content != "hello" || response.Usage.TotalTokens != 8 {
				t.Fatalf("sidecar text or usage duplicated: %+v, %v", response, err)
			}
			if passthrough {
				if writer.buf.String() != body {
					t.Fatalf("raw sidecar replaced wire output: %q", writer.buf.String())
				}
			} else if writer.buf.String() != "data: encoded-hello\n\ndata: [DONE]\n\n" {
				t.Fatalf("end events not emitted exactly once: %q", writer.buf.String())
			}
		})
	}
}

func TestProtocolLifecycleMissingTerminalAndEmptyEOF(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, body := range []string{"", "event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n"} {
			attempt, decoder, encoder := newLifecycleAttempt(t)
			err := runLifecycleSSE(attempt, context.Background(), sseTestResponse(body), passthrough)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("missing terminal accepted (raw=%t body=%q): %v", passthrough, body, err)
			}
			if decoder.endCalls != 1 || decoder.closeCalls != 1 {
				t.Fatalf("missing End/Close: %+v", decoder)
			}
			if body == "" {
				if !errors.Is(err, stream.ErrEmptyUpstreamStream) || encoder.resetCalls != 1 || attempt.streamPayloadWritten.Load() {
					t.Fatalf("empty stream semantics changed: %v resets=%d", err, encoder.resetCalls)
				}
			} else if encoder.resetCalls != 0 || !attempt.streamPayloadWritten.Load() {
				t.Fatal("partial business output must retain its aggregation and block retries")
			}
		}
	}
}

func TestProtocolLifecycleSidecarValidationAndAttemptIsolation(t *testing.T) {
	for _, failEncoder := range []bool{false, true} {
		attempt, decoder, encoder := newLifecycleAttempt(t)
		writer := &notifyStreamWriter{header: http.Header{}}
		attempt.streamWriter = writer
		validationErr := errors.New("invalid citation structure")
		if failEncoder {
			encoder.encodeErr = validationErr
		} else {
			decoder.validationErr = validationErr
		}
		body := "event: response.created\ndata: {}\n\nevent: response.output_text.delta\ndata: {\"delta\":\"stale\",\"invalid\":true}\n\n"
		err := runLifecycleSSE(attempt, context.Background(), sseTestResponse(body), true)
		if !errors.Is(err, validationErr) || writer.buf.Len() != 0 || encoder.resetCalls != 1 || len(encoder.events) != 0 || decoder.closeCalls != 1 {
			t.Fatalf("sidecar error swallowed or leaked (encoder=%t): err=%v bytes=%q resets=%d", failEncoder, err, writer.buf.String(), encoder.resetCalls)
		}
		if decoder.endCalls != 0 {
			t.Fatal("failed frame must close, not masquerade as successful EOF")
		}
		encoder.encodeErr = nil
		freshDecoder := &lifecycleOutbound{}
		attempt.outAdapter = freshDecoder
		body = "event: response.output_text.delta\ndata: {\"delta\":\"fresh\"}\n\nevent: response.completed\ndata: {}\n\n"
		if err := runLifecycleSSE(attempt, context.Background(), sseTestResponse(body), true); err != nil {
			t.Fatal(err)
		}
		response, _ := encoder.GetInternalResponse(context.Background())
		if response == nil || len(response.Choices) != 1 || *response.Choices[0].Message.Content.Content != "fresh" || strings.Contains(writer.buf.String(), "stale") || len(freshDecoder.frames) != 2 {
			t.Fatalf("failed attempt contaminated next sidecar: %+v output=%s", response, writer.buf.String())
		}
	}
}

func TestProtocolLifecycleSidecarCancelDoesNotReplay(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		attempt, decoder, encoder := newLifecycleAttempt(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		writer := &notifyStreamWriter{header: http.Header{}}
		writer.onWrite = func(data []byte) {
			if !terminal || bytes.Contains(data, []byte("response.completed")) {
				cancel()
			}
		}
		attempt.streamWriter = writer
		body := "event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n"
		if terminal {
			body += "event: response.completed\ndata: {}\n\n"
		}
		response := sseTestResponse("")
		response.Body = &stallUntilCancelBody{ctx: ctx, data: []byte(body)}
		err := runLifecycleSSE(attempt, ctx, response, true)
		if terminal && err != nil || !terminal && !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation semantics changed (terminal=%t): %v", terminal, err)
		}
		attempt.collectResponse()
		if decoder.closeCalls != 1 || encoder.resetCalls != 0 || attempt.metrics.InternalResponse == nil || attempt.metrics.Stats.InputToken != 3 || attempt.metrics.Stats.OutputToken != 5 {
			t.Fatalf("partial aggregation lost: decoder=%+v resets=%d metrics=%+v", decoder, encoder.resetCalls, attempt.metrics.Stats)
		}
		if got := *attempt.metrics.InternalResponse.Choices[0].Message.Content.Content; got != "hello" {
			t.Fatalf("cancellation replayed consumed text: %q", got)
		}
		wantFrames, wantEnd := 1, 0
		if terminal {
			wantFrames, wantEnd = 2, 1
		}
		if len(decoder.frames) != wantFrames || decoder.endCalls != wantEnd {
			t.Fatalf("frames/end consumed twice: %+v", decoder)
		}
	}
}

func TestProtocolLifecycleWSJSONUsesFrame(t *testing.T) {
	attempt, decoder, encoder := newLifecycleAttempt(t)
	data := `{"type":"response.output_text.delta","delta":"hello"}`
	if _, err := attempt.transformStreamData(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if len(decoder.frames) != 1 || string(decoder.frames[0].Data) != data || decoder.frames[0].Event != "" || decoder.frames[0].ID != "" || decoder.legacyCalls != 0 || len(encoder.events) != 2 {
		t.Fatalf("WS JSON did not use the common frame decoder: %+v", decoder)
	}
}

func TestProtocolLifecycleMergedSSEAndEventOnly(t *testing.T) {
	attempt, decoder, _ := newLifecycleAttempt(t)
	data := "event: ping\n\nevent: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\nevent: response.completed\ndata: {}\n\n"
	if _, err := attempt.transformStreamSSE(context.Background(), []byte(data)); err != nil {
		t.Fatal(err)
	}
	if len(decoder.frames) != 3 || decoder.frames[0].Event != "ping" || len(decoder.frames[0].Data) != 0 || !decoder.terminal {
		t.Fatalf("merged or event-only frame lost: %+v", decoder.frames)
	}
}

func TestProtocolLifecycleCloseErrorPropagates(t *testing.T) {
	attempt, decoder, _ := newLifecycleAttempt(t)
	decoder.closeErr = errors.New("codec close error")
	err := runLifecycleSSE(attempt, context.Background(), sseTestResponse(""), false)
	if !errors.Is(err, decoder.closeErr) || !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, stream.ErrEmptyUpstreamStream) {
		t.Fatalf("primary or close error lost: %v", err)
	}
}

func TestProtocolLifecycleWSUpstreamEOF(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, terminal := range []bool{false, true} {
			attempt, decoder, encoder := newLifecycleAttempt(t)
			attempt.streamWriter = &notifyStreamWriter{header: http.Header{}}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				connection, err := websocket.Accept(writer, request, nil)
				if err != nil {
					return
				}
				defer connection.Close(websocket.StatusNormalClosure, "")
				if err := connection.Write(request.Context(), websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"hello"}`)); err != nil {
					return
				}
				if terminal {
					_ = connection.Write(request.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"status":"completed"}}`))
				}
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				cancel()
				server.Close()
				t.Fatal(err)
			}
			if passthrough {
				_, err = attempt.handleWSPassthroughStream(ctx, &pooledConn{conn: connection})
			} else {
				err = attempt.handleWSStreamResponseV2(ctx, &wsUpstreamReader{conn: connection, statusCode: http.StatusOK})
			}
			_ = connection.CloseNow()
			cancel()
			server.Close()
			if terminal && err != nil || !terminal && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("WS EOF validation wrong (raw=%t terminal=%t): %v", passthrough, terminal, err)
			}
			if decoder.endCalls != 1 || decoder.closeCalls != 1 || decoder.legacyCalls != 0 || encoder.resetCalls != 0 || !attempt.streamPayloadWritten.Load() {
				t.Fatalf("WS lifecycle lost (raw=%t terminal=%t): decoder=%+v resets=%d", passthrough, terminal, decoder, encoder.resetCalls)
			}
		}
	}
}
