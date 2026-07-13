package router

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/service/authz"

	"github.com/stretchr/testify/assert"
)

func TestChannelRoutingV2RoutesUseExplicitPermissions(t *testing.T) {
	expected := map[string]permissionRoute{
		"/overview":                             {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/nodes":                                {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/groups":                               {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/groups/:id":                           {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/groups/:id/replay-profiles":           {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/groups/:id/error-budget":              {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/channels":                             {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/endpoints":                            {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/costs":                                {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/costs/:pool_id/:member_id":            {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/probes":                               {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/decisions":                            {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/decisions/:id":                        {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/decisions/:id/candidates":             {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/events":                               {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/policy-drafts":                        {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/policy-drafts/:id":                    {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/policy-drafts/:id/approvals":          {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/policies/current":                     {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/policies/:version":                    {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/policies/:version/rollback-approvals": {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/operations":                           {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/operations/:id":                       {method: http.MethodGet, permission: authz.ChannelRoutingRead},
	}
	assert.Len(t, channelRoutingReadRoutes, len(expected))
	paths := make(map[string]struct{}, len(channelRoutingReadRoutes))
	for _, route := range channelRoutingReadRoutes {
		expectedRoute, exists := expected[route.path]
		assert.True(t, exists)
		assert.Equal(t, expectedRoute.method, route.method)
		assert.Equal(t, expectedRoute.permission, route.permission)
		_, duplicate := paths[route.path]
		assert.False(t, duplicate)
		paths[route.path] = struct{}{}
	}

	writeExpected := map[string]permissionRoute{
		"/decisions/:id/replay":                 {method: http.MethodPost, permission: authz.ChannelRoutingOperate},
		"/groups/:id/simulations":               {method: http.MethodPost, permission: authz.ChannelRoutingOperate},
		"/costs/sync":                           {method: http.MethodPost, permission: authz.ChannelRoutingOperate},
		"/probes/run":                           {method: http.MethodPost, permission: authz.ChannelRoutingOperate},
		"/breakers/reset":                       {method: http.MethodPost, permission: authz.ChannelRoutingOperate},
		"/audit-exports":                        {method: http.MethodPost, permission: authz.ChannelRoutingAuditExport},
		"/audit-exports/:id/download":           {method: http.MethodGet, permission: authz.ChannelRoutingAuditExport},
		"/policy-drafts":                        {method: http.MethodPost, permission: authz.ChannelRoutingWrite},
		"/policy-drafts/:id":                    {method: http.MethodPut, permission: authz.ChannelRoutingWrite},
		"/policy-drafts/:id/validate":           {method: http.MethodPost, permission: authz.ChannelRoutingWrite},
		"/policy-drafts/:id/simulate":           {method: http.MethodPost, permission: authz.ChannelRoutingWrite},
		"/policy-drafts/:id/approvals":          {method: http.MethodPost, permission: authz.ChannelRoutingDeploy},
		"/policy-drafts/:id/publish":            {method: http.MethodPost, permission: authz.ChannelRoutingDeploy},
		"/policies/:version/rollback-approvals": {method: http.MethodPost, permission: authz.ChannelRoutingDeploy},
		"/policies/:version/rollback":           {method: http.MethodPost, permission: authz.ChannelRoutingDeploy},
	}
	assert.Len(t, channelRoutingWriteRoutes, len(writeExpected))
	for _, route := range channelRoutingWriteRoutes {
		expectedRoute, exists := writeExpected[route.path]
		assert.True(t, exists)
		assert.Equal(t, expectedRoute.method, route.method)
		assert.Equal(t, expectedRoute.permission, route.permission)
	}
}
