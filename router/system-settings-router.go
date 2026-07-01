package router

import (
	"net/http"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

func registerSystemSettingsRoutes(apiRouter *gin.RouterGroup) {
	for _, route := range systemSettingsPermissionRoutes {
		apiRouter.Handle(route.method, route.path,
			middleware.AdminAuth(),
			middleware.RequirePermission(route.permission),
			route.handler,
		)
	}
}

var systemSettingsPermissionRoutes = []permissionRoute{
	{method: http.MethodGet, path: "/option/", permission: authz.SystemSettingManage, handler: controller.GetOptions},
	{method: http.MethodPut, path: "/option/", permission: authz.SystemSettingManage, handler: controller.UpdateOption},
	{method: http.MethodPost, path: "/option/payment_compliance", permission: authz.SystemSettingManage, handler: controller.ConfirmPaymentCompliance},
	{method: http.MethodGet, path: "/option/affiliate_rewards", permission: authz.SystemSettingManage, handler: controller.GetAdminAffiliateRewards},
	{method: http.MethodGet, path: "/option/channel_affinity_cache", permission: authz.SystemSettingManage, handler: controller.GetChannelAffinityCacheStats},
	{method: http.MethodDelete, path: "/option/channel_affinity_cache", permission: authz.SystemSettingManage, handler: controller.ClearChannelAffinityCache},
	{method: http.MethodPost, path: "/option/rest_model_ratio", permission: authz.SystemSettingManage, handler: controller.ResetModelRatio},
	{method: http.MethodPost, path: "/option/migrate_console_setting", permission: authz.SystemSettingManage, handler: controller.MigrateConsoleSetting},
	{method: http.MethodGet, path: "/option/waffo-pancake/catalog", permission: authz.SystemSettingManage, handler: controller.ListWaffoPancakeCatalog},
	{method: http.MethodPost, path: "/option/waffo-pancake/pair", permission: authz.SystemSettingManage, handler: controller.CreateWaffoPancakePair},
	{method: http.MethodPost, path: "/option/waffo-pancake/save", permission: authz.SystemSettingManage, handler: controller.SaveWaffoPancake},
	{method: http.MethodPost, path: "/option/waffo-pancake/subscription-product", permission: authz.SystemSettingManage, handler: controller.CreateWaffoPancakeSubscriptionProduct},
	{method: http.MethodGet, path: "/option/waffo-pancake/subscription-product-options", permission: authz.SystemSettingManage, handler: controller.ListWaffoPancakeSubscriptionProductOptions},
	{method: http.MethodPost, path: "/custom-oauth-provider/discovery", permission: authz.SystemSettingManage, handler: controller.FetchCustomOAuthDiscovery},
	{method: http.MethodGet, path: "/custom-oauth-provider/", permission: authz.SystemSettingManage, handler: controller.GetCustomOAuthProviders},
	{method: http.MethodGet, path: "/custom-oauth-provider/:id", permission: authz.SystemSettingManage, handler: controller.GetCustomOAuthProvider},
	{method: http.MethodPost, path: "/custom-oauth-provider/", permission: authz.SystemSettingManage, handler: controller.CreateCustomOAuthProvider},
	{method: http.MethodPut, path: "/custom-oauth-provider/:id", permission: authz.SystemSettingManage, handler: controller.UpdateCustomOAuthProvider},
	{method: http.MethodDelete, path: "/custom-oauth-provider/:id", permission: authz.SystemSettingManage, handler: controller.DeleteCustomOAuthProvider},
	{method: http.MethodGet, path: "/performance/stats", permission: authz.SystemSettingManage, handler: controller.GetPerformanceStats},
	{method: http.MethodDelete, path: "/performance/disk_cache", permission: authz.SystemSettingManage, handler: controller.ClearDiskCache},
	{method: http.MethodPost, path: "/performance/reset_stats", permission: authz.SystemSettingManage, handler: controller.ResetPerformanceStats},
	{method: http.MethodPost, path: "/performance/gc", permission: authz.SystemSettingManage, handler: controller.ForceGC},
	{method: http.MethodGet, path: "/performance/logs", permission: authz.SystemSettingManage, handler: controller.GetLogFiles},
	{method: http.MethodDelete, path: "/performance/logs", permission: authz.SystemSettingManage, handler: controller.CleanupLogFiles},
	{method: http.MethodGet, path: "/ratio_sync/channels", permission: authz.SystemSettingManage, handler: controller.GetSyncableChannels},
	{method: http.MethodPost, path: "/ratio_sync/fetch", permission: authz.SystemSettingManage, handler: controller.FetchUpstreamRatios},
	{method: http.MethodPost, path: "/system-task/log-cleanup", permission: authz.SystemSettingManage, handler: controller.CreateLogCleanupSystemTask},
	{method: http.MethodGet, path: "/system-task/list", permission: authz.SystemSettingManage, handler: controller.ListSystemTasks},
	{method: http.MethodGet, path: "/system-task/current", permission: authz.SystemSettingManage, handler: controller.GetCurrentSystemTask},
	{method: http.MethodGet, path: "/system-task/:task_id", permission: authz.SystemSettingManage, handler: controller.GetSystemTask},
	{method: http.MethodGet, path: "/system-info/instances", permission: authz.SystemSettingManage, handler: controller.ListSystemInstances},
}
