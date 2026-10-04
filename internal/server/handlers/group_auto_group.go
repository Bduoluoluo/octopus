package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/server/middleware"
	"github.com/xuanli27/octopus/internal/server/resp"
	"github.com/xuanli27/octopus/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/group/auto-group").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(router.NewRoute("/config", http.MethodGet).Handle(getGroupAutoGroupConfig)).
		AddRoute(router.NewRoute("/config", http.MethodPut).Handle(updateGroupAutoGroupConfig)).
		AddRoute(router.NewRoute("/run", http.MethodPost).Handle(runGroupAutoGroup))
}

func getGroupAutoGroupConfig(c *gin.Context) {
	routeGroupID, err := strconv.Atoi(c.DefaultQuery("route_group_id", "1"))
	if err != nil || routeGroupID <= 0 {
		resp.InvalidParam(c)
		return
	}
	config, err := op.GroupAutoGroupConfigGet(op.WithRouteGroup(c.Request.Context(), routeGroupID))
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, config)
}

func updateGroupAutoGroupConfig(c *gin.Context) {
	var req model.GroupAutoGroupConfigUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	ctx := op.WithRouteGroup(c.Request.Context(), req.RouteGroupID)
	config, err := op.GroupAutoGroupConfigUpdate(&req, ctx)
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, err)
		return
	}
	resp.Success(c, config)
}

func runGroupAutoGroup(c *gin.Context) {
	var req model.GroupAutoGroupRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	ctx := op.WithRouteGroup(c.Request.Context(), req.RouteGroupID)
	if err := op.RunGroupAutoGroup(req.ChannelIDs, ctx); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, err)
		return
	}
	resp.Success(c, nil)
}
