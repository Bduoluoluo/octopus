package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"

	dbmodel "github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/relay/stream"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

type cacheRatioOverride struct {
	ratio               float64
	anthropic           bool
	inputTokens         int64
	cacheReadTokens     int64
	cacheCreationTokens int64
	hasInput            bool
}

func newCacheRatioOverride(channel *dbmodel.Channel) *cacheRatioOverride {
	if channel == nil || !channel.CacheRatioEnabled || channel.ValidateCacheRatio() != nil {
		return nil
	}
	percent := channel.CacheRatioMin
	if channel.CacheRatioMax > percent {
		percent += rand.Float64() * (channel.CacheRatioMax - percent)
	}
	return &cacheRatioOverride{ratio: percent / 100, anthropic: channel.Type == outbound.OutboundTypeAnthropic}
}

func (override *cacheRatioOverride) cachedTokens(total int64) int64 {
	if total <= 0 || override.ratio <= 0 {
		return 0
	}
	if override.ratio >= 1 {
		return total
	}
	return max(0, min(total, int64(math.Floor(float64(total)*override.ratio))))
}

func cacheUsageObject(data []byte) map[string]json.RawMessage {
	var value map[string]json.RawMessage
	if json.Unmarshal(data, &value) != nil {
		return nil
	}
	return value
}

func cacheUsageToken(value map[string]json.RawMessage, key string) (int64, bool) {
	var count int64
	raw, exists := value[key]
	if !exists || json.Unmarshal(raw, &count) != nil || bytes.Equal(raw, []byte("null")) || count < 0 {
		return 0, false
	}
	return count, true
}

func setCacheUsageToken(value map[string]json.RawMessage, key string, count int64) {
	value[key] = json.RawMessage(strconv.FormatInt(count, 10))
}

func (override *cacheRatioOverride) rewriteUsage(usage map[string]json.RawMessage, gemini bool) bool {
	if gemini {
		total, exists := cacheUsageToken(usage, "promptTokenCount")
		if !exists {
			return false
		}
		setCacheUsageToken(usage, "cachedContentTokenCount", override.cachedTokens(total))
		return true
	}
	if override.anthropic {
		if count, exists := cacheUsageToken(usage, "input_tokens"); exists {
			override.inputTokens, override.hasInput = count, true
		}
		if count, exists := cacheUsageToken(usage, "cache_read_input_tokens"); exists {
			override.cacheReadTokens = count
		}
		if count, exists := cacheUsageToken(usage, "cache_creation_input_tokens"); exists {
			override.cacheCreationTokens = count
		}
		if !override.hasInput {
			return false
		}
		available := override.inputTokens + override.cacheReadTokens
		total := available + override.cacheCreationTokens
		if available < 0 || total < available {
			return false
		}
		cached := min(available, override.cachedTokens(total))
		setCacheUsageToken(usage, "input_tokens", available-cached)
		setCacheUsageToken(usage, "cache_read_input_tokens", cached)
		return true
	}
	totalKey, detailsKey := "prompt_tokens", "prompt_tokens_details"
	if _, exists := usage["input_tokens"]; exists {
		totalKey, detailsKey = "input_tokens", "input_tokens_details"
	}
	total, exists := cacheUsageToken(usage, totalKey)
	if !exists {
		return false
	}
	details := cacheUsageObject(usage[detailsKey])
	if details == nil {
		details = make(map[string]json.RawMessage)
	}
	setCacheUsageToken(details, "cached_tokens", override.cachedTokens(total))
	usage[detailsKey], _ = json.Marshal(details)
	return true
}

func (override *cacheRatioOverride) rewriteJSON(data []byte) []byte {
	if override == nil {
		return data
	}
	payload := cacheUsageObject(data)
	if payload == nil {
		return data
	}
	if errorPayload := payload["error"]; len(errorPayload) > 0 && !bytes.Equal(errorPayload, []byte("null")) {
		return data
	}
	container := payload
	containerKey := ""
	for _, key := range []string{"response", "message"} {
		if nested := cacheUsageObject(payload[key]); nested != nil {
			container, containerKey = nested, key
			break
		}
	}
	usageKey := "usage"
	if _, exists := container["usageMetadata"]; exists {
		usageKey = "usageMetadata"
	}
	usage := cacheUsageObject(container[usageKey])
	if usage == nil || !override.rewriteUsage(usage, usageKey == "usageMetadata") {
		return data
	}
	container[usageKey], _ = json.Marshal(usage)
	if containerKey != "" {
		payload[containerKey], _ = json.Marshal(container)
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return data
	}
	return rewritten
}

func (override *cacheRatioOverride) rewriteSSE(frame []byte) []byte {
	if override == nil {
		return frame
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(frame), "\r\n", "\n"), "\r", "\n"), "\n")
	var dataLines []string
	for _, line := range lines {
		if line == "data" {
			dataLines = append(dataLines, "")
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(line[5:], " "))
		}
	}
	if len(dataLines) == 0 {
		return frame
	}
	data := []byte(strings.Join(dataLines, "\n"))
	rewritten := override.rewriteJSON(data)
	if bytes.Equal(data, rewritten) {
		return frame
	}
	var result []string
	inserted := false
	for _, line := range lines {
		if line == "data" || strings.HasPrefix(line, "data:") {
			if !inserted {
				result = append(result, "data: "+string(rewritten))
				inserted = true
			}
		} else {
			result = append(result, line)
		}
	}
	return []byte(strings.Join(result, "\n"))
}

type cacheRatioStreamSource struct {
	stream.StreamSource
	override *cacheRatioOverride
	framed   bool
}

func withCacheRatio(source stream.StreamSource, channel *dbmodel.Channel, framed bool) stream.StreamSource {
	override := newCacheRatioOverride(channel)
	if override == nil {
		return source
	}
	return &cacheRatioStreamSource{StreamSource: source, override: override, framed: framed}
}

func (source *cacheRatioStreamSource) ReadEvent(ctx context.Context) ([]byte, error) {
	data, err := source.StreamSource.ReadEvent(ctx)
	if err != nil {
		return data, err
	}
	if source.framed {
		return source.override.rewriteSSE(data), nil
	}
	return source.override.rewriteJSON(data), nil
}
