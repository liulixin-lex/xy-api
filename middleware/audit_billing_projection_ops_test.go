package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBillingProjectionMutationsHaveStableAutomaticAuditActions(t *testing.T) {
	assert.Equal(t, "billing_projection.stats_requeue",
		auditRouteActions["POST /api/system-info/billing-projections/stats/failed/:id/requeue"])
	assert.Equal(t, "billing_projection.log_requeue",
		auditRouteActions["POST /api/system-info/billing-projections/logs/failed/:id/requeue"])
	assert.Equal(t, "billing_projection.conflict_resolve_requeue",
		auditRouteActions["POST /api/system-info/billing-projections/log-sink-conflicts/:id/resolve-requeue"])
}
