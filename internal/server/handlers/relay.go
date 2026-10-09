package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xuanli27/octopus/internal/relay"
	"github.com/xuanli27/octopus/internal/server/middleware"
	"github.com/xuanli27/octopus/internal/server/router"
	"github.com/xuanli27/octopus/internal/transformer/inbound"
)

func init() {
	router.NewGroupRouter("/v1").
		Use(middleware.APIKeyAuth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/chat/completions", http.MethodPost).
				Handle(chat),
		).
		AddRoute(
			router.NewRoute("/responses", http.MethodPost).
				Handle(response),
		).
		AddRoute(
			router.NewRoute("/responses/compact", http.MethodPost).
				Handle(responseCompact),
		).
		AddRoute(
			router.NewRoute("/messages", http.MethodPost).
				Handle(message),
		)

	// WebSocket route for /v1/responses (no RequireJSON middleware)
	router.NewGroupRouter("/v1").
		Use(middleware.APIKeyAuth()).
		AddRoute(
			router.NewRoute("/responses", http.MethodGet).
				Handle(wsResponse),
		)
}

func chat(c *gin.Context) {
	relay.Handler(inbound.InboundTypeOpenAIChat, c)
}
func response(c *gin.Context) {
	relay.Handler(inbound.InboundTypeOpenAIResponse, c)
}
func responseCompact(c *gin.Context) {
	relay.HandleResponsesCompact(c)
}
func message(c *gin.Context) {
	relay.Handler(inbound.InboundTypeAnthropic, c)
}
func wsResponse(c *gin.Context) {
	relay.HandleWSResponse(c)
}
