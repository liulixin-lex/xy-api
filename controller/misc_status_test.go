package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/console_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetStatusExposesApiInfoActionVisibility(t *testing.T) {
	original := *console_setting.GetConsoleSetting()
	t.Cleanup(func() {
		*console_setting.GetConsoleSetting() = original
	})

	consoleSetting := console_setting.GetConsoleSetting()
	consoleSetting.ApiInfoEnabled = true
	consoleSetting.ApiInfoTestLatencyEnabled = false
	consoleSetting.ApiInfoExternalSpeedTestEnabled = true
	consoleSetting.ApiInfoOpenNewTabEnabled = false

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)

	GetStatus(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)

	var payload struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success)

	assert.Equal(t, false, payload.Data["api_info_test_latency_enabled"])
	assert.Equal(t, true, payload.Data["api_info_external_speed_test_enabled"])
	assert.Equal(t, false, payload.Data["api_info_open_new_tab_enabled"])
}
