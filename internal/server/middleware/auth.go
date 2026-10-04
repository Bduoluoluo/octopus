package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuanli27/octopus/internal/apperror"
	"github.com/xuanli27/octopus/internal/conf"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/server/auth"
	"github.com/xuanli27/octopus/internal/server/resp"
)

func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token == "" {
			resp.Unauthorized(c)
			c.Abort()
			return
		}
		if !auth.VerifyJWTToken(strings.TrimPrefix(token, "Bearer ")) {
			resp.InvalidToken(c)
			c.Abort()
			return
		}
		c.Next()
	}
}

func APIKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		var apiKey string
		var requestType string

		if key := c.Request.Header.Get("x-api-key"); key != "" {
			apiKey = key
			requestType = "anthropic"
		} else if auth := c.Request.Header.Get("Authorization"); auth != "" {
			apiKey = strings.TrimPrefix(auth, "Bearer ")
			requestType = "openai"
		}

		if apiKey == "" {
			resp.APIKeyMissing(c)
			c.Abort()
			return
		}

		if !strings.HasPrefix(apiKey, "sk-"+conf.APP_NAME+"-") {
			resp.InvalidToken(c)
			c.Abort()
			return
		}
		apiKeyObj, err := op.APIKeyGetByAPIKey(apiKey, c.Request.Context())
		if err != nil {
			resp.InvalidToken(c)
			c.Abort()
			return
		}
		if !apiKeyObj.Enabled {
			resp.ErrorWithAppError(c, http.StatusUnauthorized, apperror.New(apperror.CodeAuthAPIKeyDisabled, "API key is disabled").WithStatus(http.StatusUnauthorized))
			c.Abort()
			return
		}
		if apiKeyObj.ExpireAt > 0 && apiKeyObj.ExpireAt < time.Now().Unix() {
			resp.APIKeyExpired(c)
			c.Abort()
			return
		}
		statsAPIKey := op.StatsAPIKeyGet(apiKeyObj.ID)
		if apiKeyObj.MaxCost > 0 && apiKeyObj.MaxCost < statsAPIKey.StatsMetrics.OutputCost+statsAPIKey.StatsMetrics.InputCost {
			resp.ErrorWithAppError(c, http.StatusUnauthorized, apperror.New(apperror.CodeAuthAPIKeyCostExceeded, "API key has reached the max cost").WithStatus(http.StatusUnauthorized))
			c.Abort()
			return
		}
		if apiKeyObj.MaxRPM > 0 {
			allowed, retryAfter := op.RateLimitCheck(apiKeyObj.ID, apiKeyObj.MaxRPM)
			if !allowed {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
				resp.ErrorWithAppError(c, http.StatusTooManyRequests, apperror.New(apperror.CodeAuthAPIKeyRateLimited, "API key has exceeded the rate limit").WithStatus(http.StatusTooManyRequests))
				c.Abort()
				return
			}
		}
		c.Set("request_type", requestType)
		c.Set("supported_models", apiKeyObj.SupportedModels)
		c.Set("model_list_mode", apiKeyObj.ModelListMode)
		c.Set("api_key_id", apiKeyObj.ID)
		c.Request = c.Request.WithContext(op.WithRouteGroup(c.Request.Context(), apiKeyObj.RouteGroupID))
		c.Next()
	}
}
