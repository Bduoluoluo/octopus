package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuanli27/octopus/internal/helper"
	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/server/middleware"
	"github.com/xuanli27/octopus/internal/server/resp"
	"github.com/xuanli27/octopus/internal/server/router"
	"github.com/xuanli27/octopus/internal/utils/xstrings"
)

func init() {
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/test-model", http.MethodPost).
				Handle(testChannelModel),
		)
}

// testModelRequest 的 ChannelID 和 Channel 必须二选一，KeyIndex 从 0 开始。
type testModelRequest struct {
	ChannelID *int           `json:"channel_id,omitempty"`
	Channel   *model.Channel `json:"channel,omitempty"`
	Model     string         `json:"model" binding:"required"`
	KeyIndex  int            `json:"key_index,omitempty"`
	TimeoutMS int64          `json:"timeout_ms,omitempty"`
}

// 已保存渠道从服务端读取密钥，未保存渠道使用表单传入的数据。
func testChannelModel(c *gin.Context) {
	var req testModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}

	hasID := req.ChannelID != nil && *req.ChannelID > 0
	hasChannel := req.Channel != nil
	if (hasID && hasChannel) || (!hasID && !hasChannel) {
		resp.Error(c, http.StatusBadRequest, "exactly one of channel_id or channel is required")
		return
	}

	var channel *model.Channel
	if hasID {
		ch, err := op.ChannelGet(*req.ChannelID, c.Request.Context())
		if err != nil {
			resp.Error(c, http.StatusNotFound, "channel not found")
			return
		}
		channel = ch
	} else {
		channel = req.Channel
	}

	if req.KeyIndex < 0 || req.KeyIndex >= len(channel.Keys) {
		resp.Error(c, http.StatusBadRequest, "key_index out of range")
		return
	}
	modelSet := make(map[string]struct{})
	for _, m := range xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel) {
		modelSet[m] = struct{}{}
	}
	if _, ok := modelSet[req.Model]; !ok {
		resp.Error(c, http.StatusBadRequest, "model is not in channel's selected models")
		return
	}

	timeout := helper.DefaultModelTestTimeout
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	// 限制单次测试最长时间，避免请求长期占用资源。
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	result, err := helper.TestChannelModel(ctx, channel, req.Model, req.KeyIndex, timeout)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	resp.Success(c, result)
}
