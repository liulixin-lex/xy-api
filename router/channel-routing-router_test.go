package router

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/service/authz"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingRoutesUseExplicitPermissions(t *testing.T) {
	expected := map[string]permissionRoute{
		"/overview":                             {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/runtime-settings":                     {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/control-audits":                       {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/nodes":                                {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/groups":                               {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/groups/:id":                           {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/groups/:id/replay-profiles":           {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/groups/:id/error-budget":              {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/channels":                             {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/channel-configurations":               {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/channel-configurations/:channelId":    {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/endpoints":                            {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/costs":                                {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/costs/:pool_id/:member_id":            {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/cost-bindings":                        {method: http.MethodGet, permission: authz.ChannelRoutingRead},
		"/cost-bindings/:channelId":             {method: http.MethodGet, permission: authz.ChannelRoutingRead},
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

	writeExpected := map[string]authz.Permission{
		http.MethodPut + " /runtime-settings":                      authz.ChannelRoutingDeploy,
		http.MethodPut + " /channel-configurations/:channelId":     authz.ChannelRoutingSensitiveWrite,
		http.MethodPost + " /decisions/:id/replay":                 authz.ChannelRoutingOperate,
		http.MethodPost + " /groups/:id/simulations":               authz.ChannelRoutingOperate,
		http.MethodPost + " /costs/sync":                           authz.ChannelRoutingOperate,
		http.MethodPost + " /cost-bindings":                        authz.ChannelRoutingSensitiveWrite,
		http.MethodPut + " /cost-bindings/:channelId":              authz.ChannelRoutingSensitiveWrite,
		http.MethodDelete + " /cost-bindings/:channelId":           authz.ChannelRoutingSensitiveWrite,
		http.MethodPost + " /cost-bindings/:channelId/test":        authz.ChannelRoutingOperate,
		http.MethodPost + " /cost-bindings/:channelId/groups":      authz.ChannelRoutingOperate,
		http.MethodPost + " /probes/run":                           authz.ChannelRoutingOperate,
		http.MethodPost + " /breakers/reset":                       authz.ChannelRoutingOperate,
		http.MethodPost + " /audit-exports":                        authz.ChannelRoutingAuditExport,
		http.MethodGet + " /audit-exports/:id/download":            authz.ChannelRoutingAuditExport,
		http.MethodPost + " /policy-drafts":                        authz.ChannelRoutingWrite,
		http.MethodPut + " /policy-drafts/:id":                     authz.ChannelRoutingWrite,
		http.MethodPost + " /policy-drafts/:id/validate":           authz.ChannelRoutingWrite,
		http.MethodPost + " /policy-drafts/:id/simulate":           authz.ChannelRoutingWrite,
		http.MethodPost + " /policy-drafts/:id/approvals":          authz.ChannelRoutingDeploy,
		http.MethodPost + " /policy-drafts/:id/publish":            authz.ChannelRoutingDeploy,
		http.MethodPost + " /policies/:version/rollback-approvals": authz.ChannelRoutingDeploy,
		http.MethodPost + " /policies/:version/rollback":           authz.ChannelRoutingDeploy,
	}
	assert.Len(t, channelRoutingWriteRoutes, len(writeExpected))
	for _, route := range channelRoutingWriteRoutes {
		expectedPermission, exists := writeExpected[route.method+" "+route.path]
		assert.True(t, exists)
		assert.Equal(t, expectedPermission, route.permission)
	}
}

func TestChannelRoutingRegistersOnlyUnversionedAPIRoot(t *testing.T) {
	engine := gin.New()
	registerChannelRoutingRoutes(engine.Group("/api"))

	routes := engine.Routes()
	require.NotEmpty(t, routes)
	for _, route := range routes {
		assert.True(t, strings.HasPrefix(route.Path, "/api/channel-routing/"), route.Path)
		assert.False(t, strings.HasPrefix(route.Path, "/api/channel-routing/v2/"), route.Path)
	}
}

func TestLegacySmartRoutingAPIIsNotRegistered(t *testing.T) {
	engine := gin.New()
	SetApiRouter(engine)

	foundChannelRouting := false
	for _, route := range engine.Routes() {
		assert.False(t, strings.HasPrefix(route.Path, "/api/smart-routing"), route.Path)
		assert.False(t, strings.HasPrefix(route.Path, "/api/channel-routing/v2"), route.Path)
		if strings.HasPrefix(route.Path, "/api/channel-routing/") {
			foundChannelRouting = true
		}
	}
	assert.True(t, foundChannelRouting)
}
