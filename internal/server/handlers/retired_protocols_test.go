package handlers

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xuanli27/octopus/internal/server/router"
)

func TestRuntimeProtocolRoutes(t *testing.T) {
	engine := gin.New()
	if err := router.RegisterAll(engine); err != nil {
		t.Fatal(err)
	}
	routes := make(map[string]bool)
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"POST /v1/chat/completions", "POST /v1/responses", "POST /v1/messages",
		"POST /v1/responses/compact", "GET /v1/responses", "GET /v1/models",
	} {
		if !routes[route] {
			t.Fatalf("missing retained route %s", route)
		}
	}
	for _, route := range []string{
		"POST /v1/embeddings", "POST /v1/images/generations", "POST /v1/images/edits", "POST /v1/images/variations",
	} {
		if routes[route] {
			t.Fatalf("retired route remains registered: %s", route)
		}
	}
}
