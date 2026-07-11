package router

import (
	"net/http"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"

	"github.com/gin-gonic/gin"
)

func registerChannelRoutingRoutes(apiRouter *gin.RouterGroup) {
	route := apiRouter.Group("/channel-routing/v2")
	route.Use(middleware.AdminAuth())
	for _, item := range channelRoutingReadRoutes {
		route.Handle(item.method, item.path, middleware.RequirePermission(item.permission), item.handler)
	}
}

var channelRoutingReadRoutes = []permissionRoute{
	{method: http.MethodGet, path: "/overview", permission: authz.ChannelRead, handler: controller.GetChannelRoutingOverview},
	{method: http.MethodGet, path: "/groups", permission: authz.ChannelRead, handler: controller.ListChannelRoutingGroups},
	{method: http.MethodGet, path: "/groups/:id", permission: authz.ChannelRead, handler: controller.GetChannelRoutingGroup},
	{method: http.MethodGet, path: "/channels", permission: authz.ChannelRead, handler: controller.ListChannelRoutingChannels},
	{method: http.MethodGet, path: "/costs", permission: authz.ChannelRead, handler: controller.ListChannelRoutingCosts},
	{method: http.MethodGet, path: "/decisions", permission: authz.ChannelRead, handler: controller.ListChannelRoutingDecisions},
	{method: http.MethodGet, path: "/decisions/:id", permission: authz.ChannelRead, handler: controller.GetChannelRoutingDecision},
}
