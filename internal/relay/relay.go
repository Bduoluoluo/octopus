package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/outlierwindow"
	"github.com/xuanli27/octopus/internal/relay/balancer"
	"github.com/xuanli27/octopus/internal/server/resp"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
	"github.com/xuanli27/octopus/internal/transformer/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
	"github.com/xuanli27/octopus/internal/utils/log"
)

func Handler(inboundType inbound.InboundType, c *gin.Context) {
	// 解析请求
	rawBody, internalRequest, inAdapter, err := parseRequest(inboundType, c)
	if err != nil {
		return
	}
	if !apiKeyAllowsModel(c.GetString("supported_models"), c.GetString("model_list_mode"), internalRequest.Model) {
		resp.ErrorWithCode(c, http.StatusBadRequest, CodeRelayModelNotSupported, "model not supported")
		return
	}

	requestModel := internalRequest.Model
	apiKeyID := c.GetInt("api_key_id")

	// 获取通道分组
	group, err := op.GroupGetEnabledMap(requestModel, c.Request.Context())
	if err != nil {
		resp.ErrorWithCode(c, http.StatusNotFound, CodeRelayModelNotFound, "model not found")
		return
	}

	// === HTTP Replay 机制 ===
	// 当 HTTP 请求携带 previous_response_id 时，尝试从本地加载上一次成功的 replay 状态，
	// 优先路由到同一渠道/key，并将请求转为自包含形式（合并历史，移除 previous_response_id）。
	var responsesReplayState *wsConversationState
	if inboundType == inbound.InboundTypeOpenAIResponse && internalRequest.RawAPIFormat == model.APIFormatOpenAIResponse {
		if prevID := internalRequest.OpenAIPreviousResponseID(); prevID != "" {
			responsesReplayState = resolveResponsesReplayState(apiKeyID, group.ID, requestModel, internalRequest)
			if responsesReplayState != nil {
				log.Debugf("loaded HTTP replay state (apikey=%d, group=%d, model=%s, previous_response_id=%s, channel=%d, key=%d)",
					apiKeyID, group.ID, requestModel, prevID, responsesReplayState.ChannelID, responsesReplayState.ChannelKeyID)
				// 转换请求为自包含形式（移除 previous_response_id，合并历史）
				// BuildReplayRequest 返回 nil 表示合并失败，应保留原始请求
				if replayed := responsesReplayState.BuildReplayRequest(internalRequest); replayed != nil {
					internalRequest = replayed
					log.Debugf("HTTP replay request transformed (apikey=%d, removed previous_response_id, merged history)", apiKeyID)
				} else {
					log.Warnf("HTTP replay history merge failed (apikey=%d, group=%d, model=%s, previous_response_id=%s), keeping original request",
						apiKeyID, group.ID, requestModel, prevID)
					responsesReplayState = nil // 放弃 replay，使用原始请求
				}
			} else {
				log.Debugf("no HTTP replay state found (apikey=%d, group=%d, model=%s, previous_response_id=%s)",
					apiKeyID, group.ID, requestModel, prevID)
			}
		}
	}

	// 创建迭代器（策略排序 + 粘性优先）
	// 如果有 replay state，注入为 sticky 偏好
	var preferredSticky *balancer.SessionEntry
	if responsesReplayState != nil {
		preferredSticky = responsesReplayStateToSticky(responsesReplayState)
		if preferredSticky != nil {
			log.Debugf("HTTP replay sticky routing preference (channel=%d, key=%d)", preferredSticky.ChannelID, preferredSticky.ChannelKeyID)
		}
	}
	iter := balancer.NewIteratorWithPreference(group, apiKeyID, requestModel, preferredSticky)
	if iter.Len() == 0 {
		resp.ErrorWithCode(c, http.StatusServiceUnavailable, CodeRelayNoAvailableChannel, "no available channel")
		return
	}

	// === 早期心跳 ===
	// 在所有 forward / 重试 / 退避之前启动早期心跳协程，覆盖前置阶段（连接慢、failover、退避叠加）
	// 期间向客户端发 SSE 注释字节，避免被 Cloudflare 在 120s 零字节阈值上判 524。
	// 仅对流式请求生效；非流式无法发送 SSE 注释（破坏 application/json 协议），
	// 不施加任何本地超时——上游慢响应应让其自然完成或由上游/CF 自身处理。
	isStream := internalRequest.Stream != nil && *internalRequest.Stream
	hb := startEarlyHeartbeat(c, isStream)
	defer hb.Stop()

	// 初始化 Metrics
	metrics := NewRelayMetrics(apiKeyID, requestModel, rawBody, internalRequest)
	metrics.SetClientIP(c.ClientIP())
	// 如果触发了 HTTP replay，记录 ws_mode=replay 和 ws_recovery=replay
	if responsesReplayState != nil {
		metrics.SetWSMode(dbmodel.RelayLogWSModeReplay)
		metrics.SetWSRecovery(dbmodel.RelayLogWSRecoveryReplay)
	}
	responsesPassthroughRequired := internalRequest.HasOpenAIResponsesPassthrough()
	responsesPassthroughCapableFound := false

	// 请求级上下文
	req := &relayRequest{
		c:               c,
		inAdapter:       inAdapter,
		internalRequest: internalRequest,
		metrics:         metrics,
		apiKeyID:        apiKeyID,
		requestModel:    requestModel,
		groupID:         group.ID,
		groupSessionTTL: group.SessionKeepTime,
		iter:            iter,
		rawBody:         rawBody,
		heartbeat:       hb,
	}

	var lastErr error
	var lastResult attemptResult
	retryChannels := make(map[int]bool)

	// 同通道重试次数：启用时使用配置值，否则 1 次（不重试）
	maxSameChannelRetries := 1
	if group.RetryEnabled {
		maxSameChannelRetries = group.MaxRetries
		if maxSameChannelRetries <= 0 {
			maxSameChannelRetries = 3
		}
	}

	for round := 0; round < maxSameChannelRetries; round++ {
		if round > 0 {
			iter.Reset()
		}
		for iter.Next() {
			select {
			case <-c.Request.Context().Done():
				// Client may cancel after a previous attempt already delivered a full
				// answer (or while we were about to failover). Prefer success when
				// metrics already show a completed stream (#111/#116).
				if metricsSuggestCompletedStream(metrics) {
					log.Debugf("request context canceled but stream already complete, treating as success")
					metrics.SaveWithChannelStats(c.Request.Context(), true, nil, iter.Attempts(), false)
				} else {
					log.Infof("client canceled request before completion, stopping retry")
					metrics.SaveWithChannelStats(c.Request.Context(), false, fmt.Errorf("client canceled request"), iter.Attempts(), false)
				}
				return
			default:
			}

			item := iter.Item()
			if round > 0 && !retryChannels[item.ChannelID] {
				iter.Skip(item.ChannelID, 0, fmt.Sprintf("channel_%d", item.ChannelID), "not eligible for retry")
				continue
			}

			// 获取通道
			channel, err := op.ChannelGet(item.ChannelID, c.Request.Context())
			if err != nil {
				log.Warnf("failed to get channel %d: %v", item.ChannelID, err)
				iter.Skip(item.ChannelID, 0, fmt.Sprintf("channel_%d", item.ChannelID), fmt.Sprintf("channel not found: %v", err))
				lastErr = err
				continue
			}
			if !channel.Enabled {
				iter.Skip(channel.ID, 0, channel.Name, "channel disabled")
				continue
			}
			if responsesPassthroughRequired {
				if channel.Type == outbound.OutboundTypeOpenAIResponse {
					responsesPassthroughCapableFound = true
				} else {
					iter.Skip(channel.ID, 0, channel.Name, "openai responses passthrough required")
					continue
				}
			}

			// 出站适配器
			outAdapter := outbound.Get(channel.Type)
			if outAdapter == nil {
				iter.Skip(channel.ID, 0, channel.Name, fmt.Sprintf("unsupported channel type: %d", channel.Type))
				continue
			}

			// 类型兼容性检查
			if internalRequest.IsEmbeddingRequest() && !outbound.IsEmbeddingChannelType(channel.Type) {
				iter.Skip(channel.ID, 0, channel.Name, "channel type not compatible with embedding request")
				continue
			}
			if internalRequest.IsChatRequest() && !outbound.IsChatChannelType(channel.Type) {
				iter.Skip(channel.ID, 0, channel.Name, "channel type not compatible with chat request")
				continue
			}

			// 设置实际模型
			internalRequest.Model = item.ModelName

			log.Debugf("request model %s, mode: %d, forwarding to channel: %s model: %s (attempt %d/%d, sticky=%t)",
				requestModel, group.Mode, channel.Name, item.ModelName,
				iter.Index()+1, iter.Len(), iter.IsSticky())

			selectOpts := dbmodel.ChannelKeySelectOptions{
				ExcludeKeyIDs:  make(map[int]struct{}),
				PreferredKeyID: iter.StickyKeyID(),
			}
			var usedKey dbmodel.ChannelKey
			for {
				usedKey = channel.GetChannelKey(selectOpts)
				if usedKey.ChannelKey == "" {
					break
				}
				if !iter.SkipCircuitBreak(channel.ID, usedKey.ID, channel.Name) {
					break
				}
				selectOpts.ExcludeKeyIDs[usedKey.ID] = struct{}{}
				usedKey = dbmodel.ChannelKey{}
			}
			if usedKey.ChannelKey == "" {
				if len(selectOpts.ExcludeKeyIDs) == 0 {
					iter.Skip(channel.ID, 0, channel.Name, "no available key")
				}
				continue
			}

			var result attemptResult
			// 每轮每个渠道只尝试一次；下一轮按原候选顺序继续。
			ra := &relayAttempt{
				relayRequest:         req,
				outAdapter:           outAdapter,
				channel:              channel,
				usedKey:              usedKey,
				firstTokenTimeOutSec: group.FirstTokenTimeOut,
			}
			result = ra.attempt()
			if result.Success || result.Written || result.Canceled || result.ResetConversation || !isRetryableStatus(result.StatusCode) {
				retryChannels[channel.ID] = false
			} else {
				retryChannels[channel.ID] = true
			}

			// 同通道重试耗尽后记录熔断器失败
			if !result.Success && !result.Written && !result.Canceled && !result.ResetConversation {
				failureKind := circuitFailureKind(group.RetryEnabled, result.StatusCode)
				balancer.RecordFailure(channel.ID, usedKey.ID, internalRequest.Model, failureKind)
				outlierwindow.Report(channel.ID, false, result.StatusCode, time.Now())
				if failureKind == balancer.FailureHard {
					maybeLearnManagedRoute(c.Request.Context(), channel.ID, internalRequest.Model, inboundType, result.Err)
				}
			}

			if result.Success {
				outlierwindow.Report(channel.ID, true, result.StatusCode, time.Now())

				// === HTTP Replay 状态保存 ===
				// 成功后，如果是 OpenAI Responses HTTP 请求，保存 replay 状态供后续续接
				// 注意：exact replay 请求成功后也需要保存新状态，否则只能续接一轮
				// 优先使用 metrics.InternalResponse（streaming 安全），避免二次 GetInternalResponse 消耗聚合器
				if inboundType == inbound.InboundTypeOpenAIResponse &&
					req.internalRequest.RawAPIFormat == model.APIFormatOpenAIResponse {
					internalResponse := metrics.InternalResponse
					if internalResponse == nil {
						var err error
						internalResponse, err = inAdapter.GetInternalResponse(c.Request.Context())
						if err != nil {
							log.Debugf("failed to get internal response for replay state save: %v", err)
						}
					}
					if internalResponse != nil {
						// 如果是 exact replay 请求，基于已有状态继续累积
						var newState *wsConversationState
						if req.internalRequest.IsOpenAIExactReplayRequest() && responsesReplayState != nil {
							newState = cloneWSConversationState(responsesReplayState)
							if newState != nil {
								newState.ChannelID = channel.ID
								newState.ChannelKeyID = usedKey.ID
							}
						}
						if newState == nil {
							newState = &wsConversationState{
								RequestModel: requestModel,
								ChannelID:    channel.ID,
								ChannelKeyID: usedKey.ID,
							}
						}
						newState.ApplySuccessfulTurn(req.internalRequest, internalResponse)
						if newState.LastResponseID != "" {
							ttl := wsConversationStateTTL(group.SessionKeepTime)
							storeResponsesReplayState(apiKeyID, group.ID, requestModel, newState, ttl)
							log.Debugf("saved HTTP replay state (apikey=%d, group=%d, model=%s, response_id=%s, channel=%d, key=%d, ttl=%v, is_replay=%t)",
								apiKeyID, group.ID, requestModel, newState.LastResponseID, channel.ID, usedKey.ID, ttl, req.internalRequest.IsOpenAIExactReplayRequest())
						}
					}
				}

				metrics.SaveWithChannelStats(c.Request.Context(), true, nil, iter.Attempts(), false)
				return
			}
			if result.Canceled {
				// Double-check completion from metrics (soft finish_reason / usage).
				if metricsSuggestCompletedStream(metrics) {
					log.Debugf("client cancel after completed stream metrics, treating as success")
					metrics.SaveWithChannelStats(c.Request.Context(), true, nil, iter.Attempts(), false)
				} else {
					metrics.SaveWithChannelStats(c.Request.Context(), false, result.Err, iter.Attempts(), false)
				}
				return
			}
			if result.ResetConversation {
				metrics.SaveWithChannelStats(c.Request.Context(), false, result.Err, iter.Attempts(), false)
				if publicErr, ok := classifyWSPublicError(result.Err, result.StatusCode); ok {
					hb.FlushOrError(c, publicErr.Status, publicErr.Message)
				} else {
					hb.FlushOrError(c, result.StatusCode, result.Err.Error())
				}
				return
			}
			if result.Written {
				// Stream started but failed mid-way. If usage/content already look
				// complete (common when upstream drops after last token), count success.
				if metricsSuggestCompletedStream(metrics) {
					log.Debugf("stream written then error but metrics complete, treating as success")
					metrics.SaveWithChannelStats(c.Request.Context(), true, nil, iter.Attempts(), false)
				} else {
					metrics.SaveWithChannelStats(c.Request.Context(), false, result.Err, iter.Attempts(), false)
				}
				return
			}
			log.Debugf("continuing to next channel after failed attempt (channel=%s status=%d remaining_candidates=%d)",
				channel.Name, result.StatusCode, iter.Len()-iter.Index()-1)
			lastErr = result.Err
			lastResult = result
		}
	}

	// 所有候选通道均失败
	if responsesPassthroughRequired && !responsesPassthroughCapableFound {
		err := fmt.Errorf("openai responses native tools require an openai responses channel")
		metrics.SaveWithChannelStats(c.Request.Context(), false, err, iter.Attempts(), false)
		hb.FlushOrError(c, http.StatusBadRequest, "当前请求包含 OpenAI Responses 原生工具，仅支持 OpenAI Responses 通道直通")
		return
	}
	metrics.SaveWithChannelStats(c.Request.Context(), false, lastErr, iter.Attempts(), false)

	// 透传 429/503 状态码和 Retry-After 头，让客户端 SDK 的重试机制接管
	if isPassthroughStatus(lastResult.StatusCode) {
		if lastResult.RetryAfter > 0 {
			c.Header("Retry-After", fmt.Sprintf("%d", int(lastResult.RetryAfter.Seconds())))
		}
		hb.FlushOrError(c, lastResult.StatusCode, "channel failed")
		return
	}
	if lastResult.StatusCode > 0 {
		hb.FlushOrError(c, lastResult.StatusCode, "channel failed")
		return
	}
	hb.FlushOrError(c, http.StatusBadGateway, "channel failed")
}

func circuitFailureKind(retryEnabled bool, statusCode int) balancer.FailureKind {
	if retryEnabled && isPassthroughStatus(statusCode) {
		return balancer.FailureSoftRateLimit
	}
	return balancer.FailureHard
}

// attempt 统一管理一次通道尝试的完整生命周期
func (ra *relayAttempt) attempt() attemptResult {
	ra.metrics.UpstreamFailed = false
	span := ra.iter.StartAttempt(ra.channel.ID, ra.usedKey.ID, ra.channel.Name)

	// 转发请求
	statusCode, fwdErr := ra.forward()
	var upstreamErr *model.ResponseError
	if errors.As(fwdErr, &upstreamErr) {
		statusCode = newUpstreamResponseError(upstreamErr.Detail, upstreamErr.StatusCode).StatusCode
		ra.metrics.UpstreamFailed = true
	}

	// 更新 channel key 状态
	ra.usedKey.StatusCode = statusCode
	ra.usedKey.LastUseTimeStamp = time.Now().Unix()

	if fwdErr == nil {
		// ====== 成功 ======
		// Passthrough handlers collect response at stream end via PassthroughConfig.CollectMetrics
		ra.collectResponse()
		ra.usedKey.TotalCost += ra.metrics.Stats.InputCost + ra.metrics.Stats.OutputCost
		op.ChannelKeyUpdate(ra.usedKey)

		span.End(dbmodel.AttemptSuccess, statusCode, "")

		// Channel 维度统计
		op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
			WaitTime:       span.Duration().Milliseconds(),
			RequestSuccess: 1,
		})

		// 熔断器：记录成功
		balancer.RecordSuccess(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
		// 会话保持：更新粘性记录
		balancer.SetSticky(ra.apiKeyID, ra.requestModel, ra.channel.ID, ra.usedKey.ID)

		return attemptResult{Success: true}
	}

	// ====== 失败 ======
	// Prefer first-token timeout over generic cancel: attachFirstTokenBudget cancels
	// with errFirstTokenTimeout, but some stacks still surface as context.Canceled.
	if isFirstTokenTimeout(ra.requestContext(), fwdErr) {
		op.ChannelKeyUpdate(ra.usedKey)
		span.End(dbmodel.AttemptFailed, statusCode, "timeout=first_token: "+fwdErr.Error())
		op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
			WaitTime:      span.Duration().Milliseconds(),
			RequestFailed: 1,
		})
		return attemptResult{
			Success:           false,
			Written:           ra.streamPayloadWritten.Load(),
			FirstTokenTimeout: true,
			Err:               fmt.Errorf("channel %s failed: %v", ra.channel.Name, fwdErr),
			StatusCode:        statusCode,
			RetryAfter:        ra.retryAfter,
		}
	}

	if upstreamErr == nil && isClientCancellation(ra.requestContext(), fwdErr) {
		written := ra.streamPayloadWritten.Load()
		if written {
			ra.collectResponse()
		}
		// Issue #116: 客户端在完整流（含 finish_reason）送达后立即断连时，上游 EOF
		// 尚未读到、读侧会收到 context canceled。内容已完整交付，应按成功收口，
		// 避免 HTTP 200 + 完整 token 却被记为 success=false。
		if written && streamResponseCompleted(ra.metrics.InternalResponse) {
			if statusCode == 0 {
				statusCode = http.StatusOK
			}
			ra.usedKey.StatusCode = statusCode
			ra.usedKey.TotalCost += ra.metrics.Stats.InputCost + ra.metrics.Stats.OutputCost
			op.ChannelKeyUpdate(ra.usedKey)

			span.End(dbmodel.AttemptSuccess, statusCode, "")

			op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
				WaitTime:       span.Duration().Milliseconds(),
				RequestSuccess: 1,
			})

			balancer.RecordSuccess(ra.channel.ID, ra.usedKey.ID, ra.internalRequest.Model)
			balancer.SetSticky(ra.apiKeyID, ra.requestModel, ra.channel.ID, ra.usedKey.ID)

			log.Debugf("client canceled after complete stream (finish_reason present), treating as success")
			return attemptResult{Success: true, StatusCode: statusCode}
		}

		// Client/proxy aborted the request. This is not an upstream channel fault:
		// mark attempt as skipped so logs/stats don't look like provider outage.
		op.ChannelKeyUpdate(ra.usedKey)
		msg := "client canceled request"
		if written {
			msg = "client canceled after partial stream"
		}
		span.End(dbmodel.AttemptSkipped, statusCode, msg)
		log.Infof("%s (channel=%s, model=%s): %v", msg, ra.channel.Name, ra.internalRequest.Model, fwdErr)
		return attemptResult{
			Success:    false,
			Written:    written,
			Canceled:   true,
			Err:        fmt.Errorf("%s: %w", msg, fwdErr),
			StatusCode: statusCode,
		}
	}

	// Distinguish timeout classes in attempt message for logs / UI.
	failMsg := fwdErr.Error()
	firstTokenTimeout := isFirstTokenTimeout(ra.requestContext(), fwdErr)
	requestTimeout := isRequestTimeout(ra.requestContext(), fwdErr)
	if firstTokenTimeout {
		failMsg = "timeout=first_token: " + failMsg
	} else if requestTimeout {
		failMsg = "timeout=request: " + failMsg
	}
	if upstreamErr != nil {
		remaining := ra.iter.Len() - ra.iter.Index() - 1
		reason := upstreamFailoverReason(ra.streamPayloadWritten.Load(), ra.requestContext().Err() != nil, remaining)
		failMsg += fmt.Sprintf(" [failover=%s remaining_candidates=%d]", reason, remaining)
		log.Warnf("upstream failure (channel_id=%d status=%d failover=%s remaining_candidates=%d)", ra.channel.ID, statusCode, reason, remaining)
	}

	op.ChannelKeyUpdate(ra.usedKey)
	span.End(dbmodel.AttemptFailed, statusCode, failMsg)

	// Channel 维度统计
	op.StatsChannelUpdate(ra.channel.ID, dbmodel.StatsMetrics{
		WaitTime:      span.Duration().Milliseconds(),
		RequestFailed: 1,
	})

	// 注意：熔断器记录已移至 Handler() 的同通道重试循环外，
	// 避免重试期间过早触发熔断

	written := ra.streamPayloadWritten.Load()
	if written {
		ra.collectResponse()
		if upstreamErr != nil {
			payload, _ := json.Marshal(map[string]any{"type": "error", "error": upstreamErr.Detail})
			_, _ = ra.getStreamWriter().Write(append(append([]byte("event: error\ndata: "), payload...), '\n', '\n'))
			ra.getStreamWriter().Flush()
		}
	}
	return attemptResult{
		Success:           false,
		Written:           written,
		ResetConversation: statusCode == http.StatusConflict && needsConversationRestart(relayErrorMessage(fwdErr)),
		FirstTokenTimeout: firstTokenTimeout,
		Err:               fmt.Errorf("channel %s failed: %s", ra.channel.Name, failMsg),
		StatusCode:        statusCode,
		RetryAfter:        ra.retryAfter,
	}
}

func upstreamFailoverReason(written, canceled bool, remaining int) string {
	if written {
		return "blocked_response_started"
	}
	if canceled {
		return "blocked_client_canceled"
	}
	if remaining <= 0 {
		return "candidates_exhausted"
	}
	return "next_candidate_available"
}

// parseRequest 解析并验证入站请求
// 返回值中的 rawBody 为客户端原始请求字节，供同格式直通路径重用。
func parseRequest(inboundType inbound.InboundType, c *gin.Context) ([]byte, *model.InternalLLMRequest, model.Inbound, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return nil, nil, nil, err
	}

	inAdapter := inbound.Get(inboundType)
	internalRequest, err := inAdapter.TransformRequest(c.Request.Context(), body)
	if err != nil {
		// Client payload problems (invalid JSON / schema) are 4xx, not server faults.
		// Returning 500 made Codex report "high demand" and retry forever (#115).
		status := http.StatusBadRequest
		msg := err.Error()
		if strings.Contains(strings.ToLower(msg), "internal") {
			status = http.StatusInternalServerError
		}
		resp.Error(c, status, msg)
		return nil, nil, nil, err
	}

	// Pass through the original query parameters
	internalRequest.Query = c.Request.URL.Query()

	if err := internalRequest.Validate(); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return nil, nil, nil, err
	}

	return body, internalRequest, inAdapter, nil
}

// forward 转发请求到上游服务
