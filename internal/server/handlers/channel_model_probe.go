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

// testModelRequest POST /api/v1/channel/test-model 璇锋眰浣撱€?
// ChannelID 涓?Channel 浜岄€変竴:
//   - ChannelID:娴嬪凡淇濆瓨鍒?DB 鐨勬笭閬?key_index 鏄娓犻亾 channel.keys 鏁扮粍涓嬫爣
//   - Channel:娴嬫柊寤哄脊绐楅噷鏈繚瀛樼殑涓存椂娓犻亾(鐢ㄦ埛宸茶緭鍏?Key 浣嗚繕娌℃彁浜?,key_index 鍚屾牱鏄?keys 鏁扮粍涓嬫爣
type testModelRequest struct {
	ChannelID *int           `json:"channel_id,omitempty"`
	Channel   *model.Channel `json:"channel,omitempty"`
	Model     string         `json:"model" binding:"required"`
	KeyIndex  int            `json:"key_index,omitempty"` // 0 鏄悎娉曞€?琛ㄧず绗竴涓?Key
	TimeoutMS int64          `json:"timeout_ms,omitempty"`
}

// testChannelModel 瀵瑰崟涓笭閬撲笂鐨勫崟涓ā鍨嬪彂璧风湡瀹炴祴璇曡皟鐢ㄣ€?
//
// NOTE: 鏈枃浠跺師鍚?channel_test.go,浣?Go 宸ュ叿閾句細鎶?*_test.go 涓€寰嬭涓?
// 鍗曞厓娴嬭瘯鏂囦欢骞舵帓闄ゅ湪鏅€?build 涔嬪,浼氬鑷磋矾鐢变笉鍦ㄤ簩杩涘埗涓敞鍐?404)銆?
// 鍛藉悕涓?channel_model_probe.go 閬垮紑璇ュ悗缂€銆?
//
// NOTE(security): 褰撳墠绔蛋 ChannelID 璺緞鏃朵粎浼?ID 涓庢ā鍨嬪悕,Channel 鏁版嵁浠庣紦瀛樿鍙?
// 涓嶈姹傚墠绔妸 ChannelKey 鍐嶅彂鍥炴潵(铏界劧 listChannel 鎺ュ彛璁捐濡傛鈥斺€旇 /workspace/octopus瀹夊叏娉勯湶闂.md)銆?
func testChannelModel(c *gin.Context) {
	var req testModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}

	// 娓犻亾鏉ユ簮浜岄€変竴
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

	// key_index 杈圭晫妫€鏌?
	if req.KeyIndex < 0 || req.KeyIndex >= len(channel.Keys) {
		resp.Error(c, http.StatusBadRequest, "key_index out of range")
		return
	}
	// 妯″瀷蹇呴』鍦ㄦ笭閬撳凡閫夋ā鍨嬮泦鍚堥噷(model + custom_model 鍚堝苟鍘婚噸)
	modelSet := make(map[string]struct{})
	for _, m := range xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel) {
		modelSet[m] = struct{}{}
	}
	if _, ok := modelSet[req.Model]; !ok {
		resp.Error(c, http.StatusBadRequest, "model is not in channel's selected models")
		return
	}

	// 瓒呮椂
	timeout := 30 * time.Second
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	// 涓婇檺淇濇姢:鍗曟鏈€闀?5 鍒嗛挓,闃叉鎭舵剰鏋勯€犵殑 timeout_ms 鎸傛璧勬簮
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	result, err := helper.TestChannelModel(ctx, channel, req.Model, req.KeyIndex, timeout)
	if err != nil {
		// helper 杩斿洖鐨勯敊璇兘鏄弬鏁?涓婁笅鏂囩被闂,缁熶竴鍥?400
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	resp.Success(c, result)
}
