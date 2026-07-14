package router

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/service/authz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAsyncBillingReviewRoutesSeparateReadAndResolvePermissions(t *testing.T) {
	require.Len(t, asyncBillingReviewPermissionRoutes, 2)
	routes := make(map[string]permissionRoute, len(asyncBillingReviewPermissionRoutes))
	for _, route := range asyncBillingReviewPermissionRoutes {
		routes[route.method+" "+route.path] = route
	}
	readRoute, ok := routes[http.MethodGet+" /system-info/async-billing/manual-review"]
	require.True(t, ok)
	assert.Equal(t, authz.BillingReviewRead, readRoute.permission)
	resolveRoute, ok := routes[http.MethodPost+" /system-info/async-billing/manual-review/:id/resolve"]
	require.True(t, ok)
	assert.Equal(t, authz.BillingReviewResolve, resolveRoute.permission)
	assert.NotEqual(t, readRoute.permission, resolveRoute.permission)
}

func TestBillingProjectionOpsRoutesUseIndependentReadAndMutationPermissions(t *testing.T) {
	require.Len(t, billingProjectionOpsPermissionRoutes, 6)
	expected := map[string]authz.Permission{
		http.MethodGet + " /system-info/billing-projections/stats/failed":                            authz.BillingProjectionRead,
		http.MethodPost + " /system-info/billing-projections/stats/failed/:id/requeue":               authz.BillingProjectionRequeue,
		http.MethodGet + " /system-info/billing-projections/logs/failed":                             authz.BillingProjectionRead,
		http.MethodPost + " /system-info/billing-projections/logs/failed/:id/requeue":                authz.BillingProjectionRequeue,
		http.MethodGet + " /system-info/billing-projections/log-sink-conflicts/open":                 authz.BillingProjectionRead,
		http.MethodPost + " /system-info/billing-projections/log-sink-conflicts/:id/resolve-requeue": authz.BillingProjectionResolve,
	}
	seen := make(map[string]struct{}, len(billingProjectionOpsPermissionRoutes))
	for _, route := range billingProjectionOpsPermissionRoutes {
		key := route.method + " " + route.path
		permission, ok := expected[key]
		require.True(t, ok)
		assert.Equal(t, permission, route.permission)
		_, duplicate := seen[key]
		assert.False(t, duplicate)
		seen[key] = struct{}{}
	}
}
