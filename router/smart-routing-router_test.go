package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/service/authz"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLegacySmartRoutingRoutesUseChannelRoutingPermissions(t *testing.T) {
	expected := map[string]authz.Permission{
		http.MethodGet + " /settings":                           authz.ChannelRoutingRead,
		http.MethodPut + " /settings":                           authz.ChannelRoutingDeploy,
		http.MethodGet + " /bindings":                           authz.ChannelRoutingRead,
		http.MethodPost + " /bindings":                          authz.ChannelRoutingSensitiveWrite,
		http.MethodGet + " /bindings/:channelId":                authz.ChannelRoutingRead,
		http.MethodPut + " /bindings/:channelId":                authz.ChannelRoutingSensitiveWrite,
		http.MethodDelete + " /bindings/:channelId":             authz.ChannelRoutingSensitiveWrite,
		http.MethodPost + " /bindings/:channelId/test":          authz.ChannelRoutingOperate,
		http.MethodPost + " /bindings/:channelId/groups":        authz.ChannelRoutingOperate,
		http.MethodGet + " /metrics":                            authz.ChannelRoutingRead,
		http.MethodGet + " /snapshots":                          authz.ChannelRoutingRead,
		http.MethodGet + " /breakers":                           authz.ChannelRoutingRead,
		http.MethodPost + " /breakers/:id/reset":                authz.ChannelRoutingOperate,
		http.MethodPost + " /sync":                              authz.ChannelRoutingOperate,
		http.MethodGet + " /agent/recommendations":              authz.ChannelRoutingRead,
		http.MethodPost + " /agent/recommendations/:id/approve": authz.ChannelRoutingWrite,
		http.MethodPost + " /agent/recommendations/:id/reject":  authz.ChannelRoutingWrite,
	}

	require.Len(t, smartRoutingPermissionRoutes, len(expected))
	seen := make(map[string]struct{}, len(smartRoutingPermissionRoutes))
	for _, route := range smartRoutingPermissionRoutes {
		key := route.method + " " + route.path
		expectedPermission, exists := expected[key]
		assert.True(t, exists, "unexpected legacy route %s", key)
		assert.Equal(t, expectedPermission, route.permission, key)
		_, duplicate := seen[key]
		assert.False(t, duplicate, "duplicate legacy route %s", key)
		seen[key] = struct{}{}
	}
}

func TestLegacySmartRoutingAPIsExposeMigrationHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	legacy := engine.Group("/api/smart-routing")
	legacy.Use(smartRoutingDeprecationHeaders())
	legacy.GET("/settings", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/smart-routing/settings", nil))

	require.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, smartRoutingDeprecation, recorder.Header().Get("Deprecation"))
	_, err := http.ParseTime(recorder.Header().Get("Sunset"))
	require.NoError(t, err)
	assert.Contains(t, recorder.Header().Get("Link"), "/api/channel-routing/v2")
	assert.Contains(t, recorder.Header().Get("Link"), `rel="successor-version"`)
	assert.True(t, strings.HasPrefix(recorder.Header().Get("X-Migration-Hint"), "Migrate to "))
}
