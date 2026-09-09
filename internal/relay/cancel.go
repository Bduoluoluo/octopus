package relay

import (
	"context"
	"errors"
	"strings"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

var (
	errLocalRelayBudgetExceeded = errors.New("local relay budget exceeded")
	errFirstTokenTimeout        = errors.New("first token timeout")
	errRequestTimeout           = errors.New("request timeout")
)

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}

func isLocalRelayBudgetExceeded(ctx context.Context, err error) bool {
	if errors.Is(err, errLocalRelayBudgetExceeded) {
		return true
	}
	if ctx == nil {
		return false
	}
	return errors.Is(context.Cause(ctx), errLocalRelayBudgetExceeded)
}

func isFirstTokenTimeout(ctx context.Context, err error) bool {
	if errors.Is(err, errFirstTokenTimeout) {
		return true
	}
	if ctx == nil {
		return false
	}
	return errors.Is(context.Cause(ctx), errFirstTokenTimeout)
}

func isRequestTimeout(ctx context.Context, err error) bool {
	if errors.Is(err, errRequestTimeout) {
		return true
	}
	if ctx == nil {
		return false
	}
	return errors.Is(context.Cause(ctx), errRequestTimeout)
}

func isClientCancellation(ctx context.Context, err error) bool {
	if isLocalRelayBudgetExceeded(ctx, err) || isLocalRelayBudgetExceeded(ctx, contextError(ctx)) ||
		isFirstTokenTimeout(ctx, err) || isFirstTokenTimeout(ctx, contextError(ctx)) ||
		isRequestTimeout(ctx, err) || isRequestTimeout(ctx, contextError(ctx)) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if ctx == nil {
		return false
	}
	return errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// streamResponseCompleted reports whether an aggregated stream response is complete
// enough that a trailing client disconnect should count as success.
//
// Strict: every choice has a non-empty finish_reason.
// Soft (flaky Grok/relay stations): assistant content already delivered AND usage
// is present — many midstream proxies close the SSE after content without emitting
// finish_reason, then the client disconnects and we would otherwise record
// context canceled as failure (#111 / #116).
func streamResponseCompleted(resp *model.InternalLLMResponse) bool {
	if resp == nil || resp.Error != nil {
		return false
	}
	if len(resp.EmbeddingData) > 0 {
		return true
	}
	if len(resp.Choices) == 0 {
		return false
	}

	allFinished := true
	hasContent := false
	for i := range resp.Choices {
		ch := &resp.Choices[i]
		fr := ch.FinishReason
		if fr == nil || strings.TrimSpace(*fr) == "" {
			allFinished = false
		}
		if choiceHasDeliveredContent(ch) {
			hasContent = true
		}
	}
	if allFinished {
		return true
	}
	// Soft complete: content + usage means the model turn effectively finished.
	if hasContent && resp.Usage != nil {
		if resp.Usage.CompletionTokens > 0 || resp.Usage.PromptTokens > 0 || resp.Usage.TotalTokens > 0 {
			return true
		}
	}
	return false
}

func choiceHasDeliveredContent(ch *model.Choice) bool {
	if ch == nil {
		return false
	}
	msg := ch.Message
	if msg == nil && ch.Delta != nil {
		msg = ch.Delta
	}
	if msg == nil {
		return false
	}
	if msg.Content.Content != nil && strings.TrimSpace(*msg.Content.Content) != "" {
		return true
	}
	if len(msg.Content.MultipleContent) > 0 {
		return true
	}
	if len(msg.ToolCalls) > 0 {
		return true
	}
	if msg.ReasoningContent != nil && strings.TrimSpace(*msg.ReasoningContent) != "" {
		return true
	}
	return false
}

// metricsSuggestCompletedStream is used when the request context is canceled in
// the outer handler loop after an attempt may already have collected usage.
func metricsSuggestCompletedStream(m *RelayMetrics) bool {
	if m == nil || m.UpstreamFailed {
		return false
	}
	if streamResponseCompleted(m.InternalResponse) {
		return true
	}
	// Usage-only path: response body already tallied into metrics.
	return m.Stats.OutputToken > 0 && m.Stats.InputToken > 0
}
