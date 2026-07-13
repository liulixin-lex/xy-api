package authz

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingPermissionsAreIndependentAndLeastPrivilegeByDefault(t *testing.T) {
	var definition ResourceDefinition
	found := false
	for _, resource := range Catalog() {
		if resource.Resource == ResourceChannelRouting {
			definition = resource
			found = true
			break
		}
	}
	require.True(t, found)
	assert.Equal(t, ResourceChannelRouting, definition.Resource)

	adminPermissions := PermissionsForRole(BuiltInRoleAdmin)
	assert.Contains(t, adminPermissions, ChannelRoutingRead)
	assert.Contains(t, adminPermissions, ChannelRoutingOperate)
	assert.Contains(t, adminPermissions, ChannelRoutingWrite)
	assert.NotContains(t, adminPermissions, ChannelRoutingDeploy)
	assert.NotContains(t, adminPermissions, ChannelRoutingSensitiveWrite)
	assert.NotContains(t, adminPermissions, ChannelRoutingAuditExport)

	assert.NotEqual(t, ChannelRead, ChannelRoutingRead)
	assert.NotEqual(t, ChannelWrite, ChannelRoutingWrite)
}
