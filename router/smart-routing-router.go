package router

import (
	"net/http"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"

	"github.com/gin-gonic/gin"
)

func registerSmartRoutingRoutes(apiRouter *gin.RouterGroup) {
	route := apiRouter.Group("/smart-routing")
	route.Use(middleware.AdminAuth())

	for _, item := range smartRoutingPermissionRoutes {
		route.Handle(item.method, item.path, middleware.RequirePermission(item.permission), item.handler)
	}
}

var smartRoutingPermissionRoutes = []permissionRoute{
	{method: http.MethodGet, path: "/settings", permission: authz.ChannelRead, handler: controller.GetSmartRoutingSettings},
	{method: http.MethodPut, path: "/settings", permission: authz.ChannelWrite, handler: controller.UpdateSmartRoutingSettings},
	{method: http.MethodGet, path: "/bindings", permission: authz.ChannelRead, handler: controller.ListSmartRoutingBindings},
	{method: http.MethodPost, path: "/bindings", permission: authz.ChannelSensitiveWrite, handler: controller.CreateSmartRoutingBinding},
	{method: http.MethodGet, path: "/bindings/:channelId", permission: authz.ChannelRead, handler: controller.GetSmartRoutingBinding},
	{method: http.MethodPut, path: "/bindings/:channelId", permission: authz.ChannelSensitiveWrite, handler: controller.UpdateSmartRoutingBinding},
	{method: http.MethodDelete, path: "/bindings/:channelId", permission: authz.ChannelSensitiveWrite, handler: controller.DeleteSmartRoutingBinding},
	{method: http.MethodPost, path: "/bindings/:channelId/test", permission: authz.ChannelOperate, handler: controller.TestSmartRoutingBinding},
	{method: http.MethodPost, path: "/bindings/:channelId/groups", permission: authz.ChannelOperate, handler: controller.LoadSmartRoutingBindingGroups},
	{method: http.MethodGet, path: "/metrics", permission: authz.ChannelRead, handler: controller.ListSmartRoutingMetrics},
	{method: http.MethodGet, path: "/snapshots", permission: authz.ChannelRead, handler: controller.ListSmartRoutingSnapshots},
	{method: http.MethodGet, path: "/breakers", permission: authz.ChannelRead, handler: controller.ListSmartRoutingBreakers},
	{method: http.MethodPost, path: "/breakers/:id/reset", permission: authz.ChannelOperate, handler: controller.ResetSmartRoutingBreaker},
	{method: http.MethodPost, path: "/sync", permission: authz.ChannelOperate, handler: controller.EnqueueSmartRoutingSync},
	{method: http.MethodGet, path: "/agent/recommendations", permission: authz.ChannelRead, handler: controller.ListSmartRoutingAgentRecommendations},
	{method: http.MethodPost, path: "/agent/recommendations/:id/approve", permission: authz.ChannelWrite, handler: controller.ApproveSmartRoutingAgentRecommendation},
	{method: http.MethodPost, path: "/agent/recommendations/:id/reject", permission: authz.ChannelWrite, handler: controller.RejectSmartRoutingAgentRecommendation},
}
