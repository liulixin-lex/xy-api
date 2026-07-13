package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
