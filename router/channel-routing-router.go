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
	{method: http.MethodGet, path: "/overview", permission: authz.ChannelRoutingRead, handler: controller.GetChannelRoutingOverview},
	{method: http.MethodGet, path: "/nodes", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingNodes},
	{method: http.MethodGet, path: "/groups", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingGroups},
	{method: http.MethodGet, path: "/groups/:id", permission: authz.ChannelRoutingRead, handler: controller.GetChannelRoutingGroup},
	{method: http.MethodGet, path: "/groups/:id/replay-profiles", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingGroupReplayProfiles},
	{method: http.MethodGet, path: "/groups/:id/error-budget", permission: authz.ChannelRoutingRead, handler: controller.GetChannelRoutingGroupErrorBudget},
	{method: http.MethodGet, path: "/channels", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingChannels},
	{method: http.MethodGet, path: "/endpoints", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingEndpoints},
	{method: http.MethodGet, path: "/costs", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingCosts},
	{method: http.MethodGet, path: "/costs/:pool_id/:member_id", permission: authz.ChannelRoutingRead, handler: controller.GetChannelRoutingCost},
	{method: http.MethodGet, path: "/probes", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingProbes},
	{method: http.MethodGet, path: "/decisions", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingDecisions},
	{method: http.MethodGet, path: "/decisions/:id", permission: authz.ChannelRoutingRead, handler: controller.GetChannelRoutingDecision},
	{method: http.MethodGet, path: "/decisions/:id/candidates", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingDecisionCandidates},
	{method: http.MethodGet, path: "/events", permission: authz.ChannelRoutingRead, handler: controller.GetChannelRoutingEvents},
	{method: http.MethodGet, path: "/policy-drafts", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingPolicyDrafts},
	{method: http.MethodGet, path: "/policy-drafts/:id", permission: authz.ChannelRoutingRead, handler: controller.GetChannelRoutingPolicyDraft},
	{method: http.MethodGet, path: "/policy-drafts/:id/approvals", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingPolicyApprovals},
	{method: http.MethodGet, path: "/policies/current", permission: authz.ChannelRoutingRead, handler: controller.GetChannelRoutingCurrentPolicy},
	{method: http.MethodGet, path: "/policies/:version", permission: authz.ChannelRoutingRead, handler: controller.GetChannelRoutingPolicyRevision},
	{method: http.MethodGet, path: "/policies/:version/rollback-approvals", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingPolicyRollbackApprovals},
	{method: http.MethodGet, path: "/operations", permission: authz.ChannelRoutingRead, handler: controller.ListChannelRoutingOperations},
	{method: http.MethodGet, path: "/operations/:id", permission: authz.ChannelRoutingRead, handler: controller.GetChannelRoutingOperation},
}

var channelRoutingWriteRoutes = []permissionRoute{
	{method: http.MethodPost, path: "/decisions/:id/replay", permission: authz.ChannelRoutingOperate, handler: controller.ReplayChannelRoutingDecision},
	{method: http.MethodPost, path: "/groups/:id/simulations", permission: authz.ChannelRoutingOperate, handler: controller.SimulateChannelRoutingGroup},
	{method: http.MethodPost, path: "/costs/sync", permission: authz.ChannelRoutingOperate, handler: controller.SyncChannelRoutingCosts},
	{method: http.MethodPost, path: "/probes/run", permission: authz.ChannelRoutingOperate, handler: controller.RunChannelRoutingActiveProbe},
	{method: http.MethodPost, path: "/breakers/reset", permission: authz.ChannelRoutingOperate, handler: controller.ResetChannelRoutingBreaker},
	{method: http.MethodPost, path: "/audit-exports", permission: authz.ChannelRoutingAuditExport, handler: controller.CreateChannelRoutingAuditExport},
	{method: http.MethodGet, path: "/audit-exports/:id/download", permission: authz.ChannelRoutingAuditExport, handler: controller.DownloadChannelRoutingAuditExport},
	{method: http.MethodPost, path: "/policy-drafts", permission: authz.ChannelRoutingWrite, handler: controller.CreateChannelRoutingPolicyDraft},
	{method: http.MethodPut, path: "/policy-drafts/:id", permission: authz.ChannelRoutingWrite, handler: controller.UpdateChannelRoutingPolicyDraft},
	{method: http.MethodPost, path: "/policy-drafts/:id/validate", permission: authz.ChannelRoutingWrite, handler: controller.ValidateChannelRoutingPolicyDraft},
	{method: http.MethodPost, path: "/policy-drafts/:id/simulate", permission: authz.ChannelRoutingWrite, handler: controller.SimulateChannelRoutingPolicyDraft},
	{method: http.MethodPost, path: "/policy-drafts/:id/approvals", permission: authz.ChannelRoutingDeploy, handler: controller.ApproveChannelRoutingPolicyDraft},
	{method: http.MethodPost, path: "/policy-drafts/:id/publish", permission: authz.ChannelRoutingDeploy, handler: controller.PublishChannelRoutingPolicyDraft},
	{method: http.MethodPost, path: "/policies/:version/rollback-approvals", permission: authz.ChannelRoutingDeploy, handler: controller.ApproveChannelRoutingPolicyRollback},
	{method: http.MethodPost, path: "/policies/:version/rollback", permission: authz.ChannelRoutingDeploy, handler: controller.RollbackChannelRoutingPolicy},
}
