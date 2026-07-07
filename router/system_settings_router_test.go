package router

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/stretchr/testify/assert"
)

func TestSystemSettingsRoutesUseSystemSettingPermission(t *testing.T) {
	assertSystemSettingsRoutePermission(t, http.MethodGet, "/option/", controller.GetOptions)
	assertSystemSettingsRoutePermission(t, http.MethodPut, "/option/", controller.UpdateOption)
	assertSystemSettingsRoutePermission(t, http.MethodPost, "/option/payment_compliance", controller.ConfirmPaymentCompliance)
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

func assertSystemSettingsRoutePermission(t *testing.T, method string, path string, handler any) {
	t.Helper()
	for _, route := range systemSettingsPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, authz.SystemSettingManage, route.permission)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}
