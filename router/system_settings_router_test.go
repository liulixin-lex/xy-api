package router

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestSystemSettingsRoutesUseSystemSettingPermission(t *testing.T) {
	assertSystemSettingsRoutePermission(t, http.MethodGet, "/option/", controller.GetOptions)
	assertSystemSettingsRoutePermission(t, http.MethodPut, "/option/", controller.UpdateOption)
	assertPaymentGatewayRoutePermission(t, http.MethodPost, "/option/payment_compliance", controller.ConfirmPaymentCompliance)
	assertPaymentGatewayRoutePermission(t, http.MethodPut, "/option/payment", controller.UpdatePaymentSettings)
	assertPaymentGatewayScopedRoutePermission(t, http.MethodPost, "/option/waffo-pancake/catalog", true, controller.ListWaffoPancakeCatalog)
	assertPaymentGatewayScopedRoutePermission(t, http.MethodPost, "/option/waffo-pancake/pair", true, controller.CreateWaffoPancakePair)
	assertPaymentGatewayScopedRoutePermission(t, http.MethodPost, "/option/waffo-pancake/save", true, controller.SaveWaffoPancake)
	assertPaymentGatewayScopedRoutePermission(t, http.MethodPost, "/option/waffo-pancake/subscription-product", true, controller.CreateWaffoPancakeSubscriptionProduct)
	assertPaymentGatewayScopedRoutePermission(t, http.MethodGet, "/option/waffo-pancake/subscription-product-options", false, controller.ListWaffoPancakeSubscriptionProductOptions)
	assertPaymentGatewayScopedRoutePermission(t, http.MethodGet, "/option/payment/credential-revocation-preview", false, controller.GetPaymentCredentialRevocationPreview)
	assertPaymentOperationsRoutePermission(t, http.MethodGet, "/option/payment/overview", false, controller.GetPaymentOperationsOverview)
	assertDirectPaymentGatewayRoutePermission(t, http.MethodGet, "/option/payment/limits", false, controller.ListPaymentLimitPolicies)
	assertDirectPaymentGatewayRoutePermission(t, http.MethodPut, "/option/payment/limits", true, controller.UpdatePaymentLimitPolicy)
	assertPaymentOperationsRoutePermission(t, http.MethodGet, "/option/payment/audit", false, controller.ListPaymentAudit)
	assertPaymentOperationsRoutePermission(t, http.MethodGet, "/option/payment/audit/:trade_no", false, controller.GetPaymentAudit)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/:trade_no/fulfill", true, controller.ResolveManualPaymentOrder)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/:trade_no/reject", true, controller.RejectManualPaymentOrder)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/:trade_no/void", true, controller.VoidManualPaymentOrder)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/:trade_no/external-refund", true, controller.ConfirmExternalPaymentRefund)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/:trade_no/credential-incident/acknowledge", true, controller.AcknowledgePaymentCredentialIncident)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/:trade_no/credential-incident/resolve", true, controller.ResolvePaymentCredentialIncident)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/unmatched/:id/dismiss", true, controller.DismissUnmatchedPaymentEvent)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/unmatched/:id/link", true, controller.LinkUnmatchedPaymentEvent)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/unmatched/:id/retry-legacy", true, controller.RetryLegacyUnmatchedPaymentEvent)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/unmatched/:id/resolve-legacy-topup", true, controller.ResolveLegacyTopUpPaymentEvent)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/audit/unmatched/:id/resolve-legacy-subscription", true, controller.ResolveLegacySubscriptionPaymentEvent)
	assertPaymentOperationsRoutePermission(t, http.MethodGet, "/option/payment/customer-bindings", false, controller.ListStripeCustomerBindings)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/customer-bindings/:id/retire", true, controller.RetireStripeCustomerBinding)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/payment/debts/:id/resolve", true, controller.ResolvePaymentDebt)
	assertPaymentOperationsRoutePermission(t, http.MethodGet, "/option/billing/reservations", false, controller.ListBillingReservationsForAdmin)
	assertPaymentOperationsRoutePermission(t, http.MethodGet, "/option/billing/reservations/:request_id", false, controller.GetBillingReservationForAdmin)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/option/billing/reservations/:request_id/resolve", true, controller.ResolveBillingReservationForAdmin)
	assertPaymentOperationsRoutePermission(t, http.MethodPost, "/user/topup/complete", true, controller.AdminCompleteTopUp)
	assertPaymentOperationsRoutePermission(t, http.MethodGet, "/subscription/admin/stripe/inventory", false, controller.AdminListStripeLegacySubscriptionInventory)
	assertPaymentOperationsRateLimitedRoute(t, http.MethodPost, "/subscription/admin/stripe/inventory/sync", controller.AdminSyncStripeLegacySubscriptionInventory)
	assertSystemSettingsRoutePermission(t, http.MethodGet, "/option/affiliate_rewards", controller.GetAdminAffiliateRewards)
	assertSystemSettingsRoutePermission(t, http.MethodGet, "/option/invite_link_batches", controller.ListInviteLinkBatches)
	assertSystemSettingsRoutePermission(t, http.MethodPost, "/option/invite_link_batches", controller.CreateInviteLinkBatch)
	assertSystemSettingsRoutePermission(t, http.MethodPut, "/option/invite_link_batches/:id", controller.UpdateInviteLinkBatch)
	assertSystemSettingsRoutePermission(t, http.MethodPost, "/option/invite_link_batches/:id/active", controller.ActivateInviteLinkBatch)
	assertSystemSettingsRoutePermission(t, http.MethodGet, "/option/invite_link_batches/random", controller.GenerateInviteLinkBatchRandomLink)
	assertSystemSettingsRoutePermission(t, http.MethodGet, "/custom-oauth-provider/", controller.GetCustomOAuthProviders)
	assertSystemSettingsRoutePermission(t, http.MethodGet, "/performance/stats", controller.GetPerformanceStats)
	assertSystemSettingsRoutePermission(t, http.MethodGet, "/ratio_sync/channels", controller.GetSyncableChannels)
	assertSystemSettingsRoutePermission(t, http.MethodPost, "/system-task/log-cleanup", controller.CreateLogCleanupSystemTask)
	assertSystemSettingsRoutePermission(t, http.MethodGet, "/system-info/instances", controller.ListSystemInstances)
	assertSystemSettingsRoutePermission(t, http.MethodDelete, "/system-info/stale-instances", controller.DeleteStaleSystemInstances)
	assertSystemSettingsRoutePermission(t, http.MethodDelete, "/system-info/instances/:node_name", controller.DeleteStaleSystemInstance)
}

func assertDirectPaymentGatewayRoutePermission(t *testing.T, method string, path string, secureVerification bool, handler any) {
	t.Helper()
	for _, route := range systemSettingsPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, authz.PaymentGatewayManage, route.permission)
			assert.Empty(t, route.additionalPermissions)
			assert.Equal(t, secureVerification, route.secureVerification)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}

func assertPaymentOperationsRoutePermission(t *testing.T, method string, path string, secureVerification bool, handler any) {
	t.Helper()
	for _, route := range systemSettingsPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, authz.PaymentOperationsManage, route.permission)
			assert.Empty(t, route.additionalPermissions)
			assert.Equal(t, secureVerification, route.secureVerification)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}

func assertPaymentGatewayRoutePermission(t *testing.T, method string, path string, handler any) {
	assertPaymentGatewayScopedRoutePermission(t, method, path, true, handler)
}

func assertPaymentGatewayScopedRoutePermission(t *testing.T, method string, path string, secureVerification bool, handler any) {
	t.Helper()
	for _, route := range systemSettingsPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, authz.SystemSettingManage, route.permission)
			assert.Equal(t, []authz.Permission{authz.PaymentGatewayManage}, route.additionalPermissions)
			assert.Equal(t, secureVerification, route.secureVerification)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}

func assertPaymentOperationsRateLimitedRoute(t *testing.T, method string, path string, handler any) {
	t.Helper()
	for _, route := range systemSettingsPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, authz.PaymentOperationsManage, route.permission)
			assert.True(t, route.secureVerification)
			assert.True(t, route.criticalRateLimit)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}

func TestSystemSettingsRoutesRegisterWithoutPathConflicts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerSystemSettingsRoutes(engine.Group("/api"))
}

func assertSystemSettingsRoutePermission(t *testing.T, method string, path string, handler any) {
	t.Helper()
	for _, route := range systemSettingsPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, authz.SystemSettingManage, route.permission)
			assert.Empty(t, route.additionalPermissions)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}
