package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/xuanli27/octopus/internal/op"
	"github.com/xuanli27/octopus/internal/server/middleware"
	"github.com/xuanli27/octopus/internal/server/resp"
	"github.com/xuanli27/octopus/internal/server/router"
)

func init() {
	router.NewGroupRouter("/api/v1/route-group").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(router.NewRoute("/list", http.MethodGet).Handle(listRouteGroups)).
		AddRoute(router.NewRoute("/create", http.MethodPost).Handle(createRouteGroup)).
		AddRoute(router.NewRoute("/delete/:id", http.MethodDelete).Handle(deleteRouteGroup))
}

func listRouteGroups(c *gin.Context) {
	groups, err := op.RouteGroupList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, groups)
}

func createRouteGroup(c *gin.Context) {
	var request struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.InvalidJSON(c)
		return
	}
	group, err := op.RouteGroupCreate(request.Name, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	resp.Success(c, group)
}

func deleteRouteGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		resp.InvalidParam(c)
		return
	}
	if err := op.RouteGroupDelete(id, c.Request.Context()); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	resp.Success(c, nil)
}
