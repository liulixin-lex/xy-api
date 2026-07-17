package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const channelRoutingChannelConfigurationsPath = "/api/channel-routing/channel-configurations"
const channelRoutingPoliciesPath = "/api/channel-routing/policy-drafts"

// ChannelRoutingCostConnectorRetired keeps the retired connector surface
// explicit without exposing any historical binding, account, or credential
// material. Channel cost is now configured through channel configurations.
func ChannelRoutingCostConnectorRetired(c *gin.Context) {
	c.JSON(http.StatusGone, gin.H{
		"success":          false,
		"code":             "routing_cost_connector_retired",
		"message":          "routing cost connectors are retired; use channel configurations and upstream_cost_multiplier",
		"replacement_path": channelRoutingChannelConfigurationsPath,
	})
}

// ChannelRoutingPolicyApprovalRetired preserves an explicit compatibility
// response while historical approval rows remain read-only for audit use.
func ChannelRoutingPolicyApprovalRetired(c *gin.Context) {
	c.JSON(http.StatusGone, gin.H{
		"success":          false,
		"code":             "policy_approval_retired",
		"message":          "manual policy approvals are retired; authorized administrators can validate and deploy directly",
		"replacement_path": channelRoutingPoliciesPath,
		"retryable":        false,
		"impact":           "approval_not_recorded",
	})
}
