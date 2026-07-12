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
	for _, item := range channelRoutingWriteRoutes {
		route.Handle(item.method, item.path, middleware.RequirePermission(item.permission), item.handler)
	}
}

var channelRoutingReadRoutes = []permissionRoute{
	{method: http.MethodGet, path: "/overview", permission: authz.ChannelRead, handler: controller.GetChannelRoutingOverview},
	{method: http.MethodGet, path: "/nodes", permission: authz.ChannelRead, handler: controller.ListChannelRoutingNodes},
	{method: http.MethodGet, path: "/groups", permission: authz.ChannelRead, handler: controller.ListChannelRoutingGroups},
	{method: http.MethodGet, path: "/groups/:id", permission: authz.ChannelRead, handler: controller.GetChannelRoutingGroup},
	{method: http.MethodGet, path: "/channels", permission: authz.ChannelRead, handler: controller.ListChannelRoutingChannels},
	{method: http.MethodGet, path: "/costs", permission: authz.ChannelRead, handler: controller.ListChannelRoutingCosts},
	{method: http.MethodGet, path: "/probes", permission: authz.ChannelRead, handler: controller.ListChannelRoutingProbes},
	{method: http.MethodGet, path: "/decisions", permission: authz.ChannelRead, handler: controller.ListChannelRoutingDecisions},
	{method: http.MethodGet, path: "/decisions/:id", permission: authz.ChannelRead, handler: controller.GetChannelRoutingDecision},
	{method: http.MethodPost, path: "/decisions/:id/replay", permission: authz.ChannelRead, handler: controller.ReplayChannelRoutingDecision},
	{method: http.MethodPost, path: "/groups/:id/simulations", permission: authz.ChannelRead, handler: controller.SimulateChannelRoutingGroup},
	{method: http.MethodGet, path: "/policy-drafts", permission: authz.ChannelRead, handler: controller.ListChannelRoutingPolicyDrafts},
	{method: http.MethodGet, path: "/policy-drafts/:id", permission: authz.ChannelRead, handler: controller.GetChannelRoutingPolicyDraft},
}

var channelRoutingWriteRoutes = []permissionRoute{
	{method: http.MethodPost, path: "/policy-drafts", permission: authz.ChannelWrite, handler: controller.CreateChannelRoutingPolicyDraft},
	{method: http.MethodPut, path: "/policy-drafts/:id", permission: authz.ChannelWrite, handler: controller.UpdateChannelRoutingPolicyDraft},
	{method: http.MethodPost, path: "/policy-drafts/:id/validate", permission: authz.ChannelWrite, handler: controller.ValidateChannelRoutingPolicyDraft},
}
