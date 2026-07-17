package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingCostConnectorRetiredDoesNotExposeLegacyData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/cost-bindings/7", nil)

	ChannelRoutingCostConnectorRetired(context)

	require.Equal(t, http.StatusGone, recorder.Code)
	assert.JSONEq(t, `{
		"success": false,
		"code": "routing_cost_connector_retired",
		"message": "routing cost connectors are retired; use channel configurations and upstream_cost_multiplier",
		"replacement_path": "/api/channel-routing/channel-configurations"
	}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "credential")
	assert.NotContains(t, recorder.Body.String(), "token")
	assert.NotContains(t, recorder.Body.String(), "password")
}

func TestChannelRoutingPolicyApprovalRetiredReturnsStableGoneContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/channel-routing/policy-drafts/7/approvals", nil)

	ChannelRoutingPolicyApprovalRetired(context)

	require.Equal(t, http.StatusGone, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"policy_approval_retired"`)
	assert.Contains(t, recorder.Body.String(), `"replacement_path":"/api/channel-routing/policy-drafts"`)
	assert.Contains(t, recorder.Body.String(), `"retryable":false`)
	assert.NotContains(t, recorder.Body.String(), "two distinct")
}
