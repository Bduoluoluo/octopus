package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xuanli27/octopus/internal/conf"
	"github.com/xuanli27/octopus/internal/db"
	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/op"
)

func TestAPIKeyAuthResolvesModelsInBoundRouteGroup(t *testing.T) {
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "route-group.db"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	custom, err := op.RouteGroupCreate("custom", ctx)
	if err != nil {
		t.Fatal(err)
	}
	defaultRoute := &model.Group{Name: "same-model", Mode: model.GroupModeRoundRobin}
	customRoute := &model.Group{Name: "same-model", RouteGroupID: custom.ID, Mode: model.GroupModeFailover}
	for _, route := range []*model.Group{defaultRoute, customRoute} {
		if err := op.GroupCreate(route, ctx); err != nil {
			t.Fatal(err)
		}
	}
	key := &model.APIKey{Name: "test", APIKey: "sk-" + conf.APP_NAME + "-route-group-test", Enabled: true}
	if err := op.APIKeyCreate(key, ctx); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []struct {
		groupID int
		routeID int
	}{{model.DefaultRouteGroupID, defaultRoute.ID}, {custom.ID, customRoute.ID}, {model.DefaultRouteGroupID, defaultRoute.ID}} {
		key.RouteGroupID = binding.groupID
		if err := op.APIKeyUpdate(key, ctx); err != nil {
			t.Fatal(err)
		}
		for _, header := range []string{"Authorization", "x-api-key"} {
			router := gin.New()
			router.GET("/test", APIKeyAuth(), func(context *gin.Context) {
				route, err := op.GroupGetEnabledMap("same-model", context.Request.Context())
				if err != nil || route.ID != binding.routeID {
					t.Errorf("wrong route for key binding %d: %+v %v", binding.groupID, route, err)
				}
				context.Status(http.StatusNoContent)
			})
			request := httptest.NewRequest(http.MethodGet, "/test", nil)
			value := key.APIKey
			if header == "Authorization" {
				value = "Bearer " + value
			}
			request.Header.Set(header, value)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("auth failed: status=%d body=%s", response.Code, response.Body.String())
			}
		}
	}
}
