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
		permissionDeniedCode := systemSettingsPermissionDeniedCode(route)
		handlers := []gin.HandlerFunc{
			middleware.AdminAuth(),
			middleware.RequirePermissionWithCode(route.permission, permissionDeniedCode),
		}
		for _, permission := range route.additionalPermissions {
			handlers = append(handlers, middleware.RequirePermissionWithCode(permission, permissionDeniedCode))
		}
		if route.criticalRateLimit {
			handlers = append(handlers, middleware.CriticalRateLimit())
		}
		if route.secureVerification {
			authRequiredCode := "dashboard_session_required"
			if permissionDeniedCode == "payment_settings_permission_denied" {
				authRequiredCode = "payment_settings_auth_required"
			} else if permissionDeniedCode == "payment_operations_permission_denied" {
				authRequiredCode = "payment_operations_auth_required"
			}
			handlers = append(handlers, middleware.DashboardSessionRequired(authRequiredCode))
			handlers = append(handlers, middleware.SecureVerificationRequired())
		}
		handlers = append(handlers, route.handler)
		apiRouter.Handle(route.method, route.path, handlers...)
	}
}

func systemSettingsPermissionDeniedCode(route permissionRoute) string {
	if route.permission == authz.PaymentOperationsManage {
		return "payment_operations_permission_denied"
	}
	if route.permission == authz.PaymentGatewayManage {
		return "payment_settings_permission_denied"
	}
	for _, permission := range route.additionalPermissions {
		if permission == authz.PaymentGatewayManage {
			return "payment_settings_permission_denied"
		}
	}
	return "permission_denied"
}

var systemSettingsPermissionRoutes = []permissionRoute{
	{method: http.MethodGet, path: "/option/", permission: authz.SystemSettingManage, handler: controller.GetOptions},
	{method: http.MethodPut, path: "/option/", permission: authz.SystemSettingManage, handler: controller.UpdateOption},
	{method: http.MethodPut, path: "/option/payment", permission: authz.SystemSettingManage, additionalPermissions: []authz.Permission{authz.PaymentGatewayManage}, secureVerification: true, handler: controller.UpdatePaymentSettings},
	{method: http.MethodGet, path: "/option/payment/overview", permission: authz.PaymentOperationsManage, handler: controller.GetPaymentOperationsOverview},
	{method: http.MethodGet, path: "/option/payment/credential-revocation-preview", permission: authz.SystemSettingManage, additionalPermissions: []authz.Permission{authz.PaymentGatewayManage}, handler: controller.GetPaymentCredentialRevocationPreview},
	{method: http.MethodGet, path: "/option/payment/limits", permission: authz.PaymentGatewayManage, handler: controller.ListPaymentLimitPolicies},
	{method: http.MethodPut, path: "/option/payment/limits", permission: authz.PaymentGatewayManage, secureVerification: true, handler: controller.UpdatePaymentLimitPolicy},
	{method: http.MethodGet, path: "/option/payment/audit", permission: authz.PaymentOperationsManage, handler: controller.ListPaymentAudit},
	{method: http.MethodGet, path: "/option/payment/audit/:trade_no", permission: authz.PaymentOperationsManage, handler: controller.GetPaymentAudit},
	{method: http.MethodPost, path: "/option/payment/audit/:trade_no/fulfill", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.ResolveManualPaymentOrder},
	{method: http.MethodPost, path: "/option/payment/audit/:trade_no/reject", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.RejectManualPaymentOrder},
	{method: http.MethodPost, path: "/option/payment/audit/:trade_no/void", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.VoidManualPaymentOrder},
	{method: http.MethodPost, path: "/option/payment/audit/:trade_no/external-refund", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.ConfirmExternalPaymentRefund},
	{method: http.MethodPost, path: "/option/payment/audit/:trade_no/credential-incident/acknowledge", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.AcknowledgePaymentCredentialIncident},
	{method: http.MethodPost, path: "/option/payment/audit/:trade_no/credential-incident/resolve", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.ResolvePaymentCredentialIncident},
	{method: http.MethodPost, path: "/option/payment/audit/unmatched/:id/dismiss", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.DismissUnmatchedPaymentEvent},
	{method: http.MethodPost, path: "/option/payment/audit/unmatched/:id/link", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.LinkUnmatchedPaymentEvent},
	{method: http.MethodPost, path: "/option/payment/audit/unmatched/:id/retry-legacy", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.RetryLegacyUnmatchedPaymentEvent},
	{method: http.MethodPost, path: "/option/payment/audit/unmatched/:id/resolve-legacy-topup", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.ResolveLegacyTopUpPaymentEvent},
	{method: http.MethodPost, path: "/option/payment/audit/unmatched/:id/resolve-legacy-subscription", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.ResolveLegacySubscriptionPaymentEvent},
	{method: http.MethodGet, path: "/option/payment/customer-bindings", permission: authz.PaymentOperationsManage, handler: controller.ListStripeCustomerBindings},
	{method: http.MethodPost, path: "/option/payment/customer-bindings/:id/retire", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.RetireStripeCustomerBinding},
	{method: http.MethodPost, path: "/option/payment/debts/:id/resolve", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.ResolvePaymentDebt},
	{method: http.MethodGet, path: "/option/billing/reservations", permission: authz.PaymentOperationsManage, handler: controller.ListBillingReservationsForAdmin},
	{method: http.MethodGet, path: "/option/billing/reservations/:request_id", permission: authz.PaymentOperationsManage, handler: controller.GetBillingReservationForAdmin},
	{method: http.MethodPost, path: "/option/billing/reservations/:request_id/resolve", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.ResolveBillingReservationForAdmin},
	{method: http.MethodPost, path: "/user/topup/complete", permission: authz.PaymentOperationsManage, secureVerification: true, handler: controller.AdminCompleteTopUp},
	{method: http.MethodGet, path: "/subscription/admin/stripe/inventory", permission: authz.PaymentOperationsManage, handler: controller.AdminListStripeLegacySubscriptionInventory},
	{method: http.MethodPost, path: "/subscription/admin/stripe/inventory/sync", permission: authz.PaymentOperationsManage, secureVerification: true, criticalRateLimit: true, handler: controller.AdminSyncStripeLegacySubscriptionInventory},
	{method: http.MethodPost, path: "/subscription/admin/stripe/inventory/:id/cancel-at-period-end", permission: authz.PaymentOperationsManage, secureVerification: true, criticalRateLimit: true, handler: controller.AdminCancelStripeLegacySubscriptionAtPeriodEnd},
	{method: http.MethodPost, path: "/option/payment_compliance", permission: authz.SystemSettingManage, additionalPermissions: []authz.Permission{authz.PaymentGatewayManage}, secureVerification: true, handler: controller.ConfirmPaymentCompliance},
	{method: http.MethodGet, path: "/option/affiliate_rewards", permission: authz.SystemSettingManage, handler: controller.GetAdminAffiliateRewards},
	{method: http.MethodGet, path: "/option/invite_link_batches", permission: authz.SystemSettingManage, handler: controller.ListInviteLinkBatches},
	{method: http.MethodPost, path: "/option/invite_link_batches", permission: authz.SystemSettingManage, handler: controller.CreateInviteLinkBatch},
	{method: http.MethodPut, path: "/option/invite_link_batches/:id", permission: authz.SystemSettingManage, handler: controller.UpdateInviteLinkBatch},
	{method: http.MethodPost, path: "/option/invite_link_batches/:id/active", permission: authz.SystemSettingManage, handler: controller.ActivateInviteLinkBatch},
	{method: http.MethodGet, path: "/option/invite_link_batches/random", permission: authz.SystemSettingManage, handler: controller.GenerateInviteLinkBatchRandomLink},
	{method: http.MethodGet, path: "/option/channel_affinity_cache", permission: authz.SystemSettingManage, handler: controller.GetChannelAffinityCacheStats},
	{method: http.MethodDelete, path: "/option/channel_affinity_cache", permission: authz.SystemSettingManage, handler: controller.ClearChannelAffinityCache},
	{method: http.MethodPost, path: "/option/rest_model_ratio", permission: authz.SystemSettingManage, handler: controller.ResetModelRatio},
	{method: http.MethodPost, path: "/option/migrate_console_setting", permission: authz.SystemSettingManage, handler: controller.MigrateConsoleSetting},
	{method: http.MethodPost, path: "/option/waffo-pancake/catalog", permission: authz.SystemSettingManage, additionalPermissions: []authz.Permission{authz.PaymentGatewayManage}, secureVerification: true, handler: controller.ListWaffoPancakeCatalog},
	{method: http.MethodPost, path: "/option/waffo-pancake/pair", permission: authz.SystemSettingManage, additionalPermissions: []authz.Permission{authz.PaymentGatewayManage}, secureVerification: true, handler: controller.CreateWaffoPancakePair},
	{method: http.MethodPost, path: "/option/waffo-pancake/save", permission: authz.SystemSettingManage, additionalPermissions: []authz.Permission{authz.PaymentGatewayManage}, secureVerification: true, handler: controller.SaveWaffoPancake},
	{method: http.MethodPost, path: "/option/waffo-pancake/subscription-product", permission: authz.SystemSettingManage, additionalPermissions: []authz.Permission{authz.PaymentGatewayManage}, secureVerification: true, handler: controller.CreateWaffoPancakeSubscriptionProduct},
	{method: http.MethodGet, path: "/option/waffo-pancake/subscription-product-options", permission: authz.SystemSettingManage, additionalPermissions: []authz.Permission{authz.PaymentGatewayManage}, handler: controller.ListWaffoPancakeSubscriptionProductOptions},
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
	{method: http.MethodDelete, path: "/system-info/stale-instances", permission: authz.SystemSettingManage, handler: controller.DeleteStaleSystemInstances},
	{method: http.MethodDelete, path: "/system-info/instances/:node_name", permission: authz.SystemSettingManage, handler: controller.DeleteStaleSystemInstance},
}
