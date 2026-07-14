package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelRoutingRuntimeSettingsUsesStrongETagCASAndPersistentAudit(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/runtime-settings", nil)
	GetChannelRoutingRuntimeSettings(getContext)
	require.Equal(t, http.StatusOK, getRecorder.Code, getRecorder.Body.String())
	initialETag := getRecorder.Header().Get("ETag")
	require.NotEmpty(t, initialETag)
	var initial struct {
		Success bool                              `json:"success"`
		Data    channelRoutingRuntimeSettingsView `json:"data"`
	}
	require.NoError(t, common.Unmarshal(getRecorder.Body.Bytes(), &initial))
	require.True(t, initial.Success)
	assert.Equal(t, int64(1), initial.Data.Revision)
	assert.Equal(t, initialETag, initial.Data.ETag)

	updated := initial.Data.StoredSettings
	updated.Mode = smart_routing_setting.ModeBalanced
	updated.Consecutive5xx++
	body, err := common.Marshal(updated)
	require.NoError(t, err)

	missingRecorder := httptest.NewRecorder()
	missingContext, _ := gin.CreateTestContext(missingRecorder)
	missingContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel-routing/v2/runtime-settings", bytes.NewReader(body))
	missingContext.Set("id", 10)
	UpdateChannelRoutingRuntimeSettings(missingContext)
	assert.Equal(t, http.StatusPreconditionRequired, missingRecorder.Code)

	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel-routing/v2/runtime-settings", bytes.NewReader(body))
	updateContext.Request.Header.Set("If-Match", initialETag)
	updateContext.Set("id", 10)
	UpdateChannelRoutingRuntimeSettings(updateContext)
	require.Equal(t, http.StatusOK, updateRecorder.Code, updateRecorder.Body.String())
	updatedETag := updateRecorder.Header().Get("ETag")
	require.NotEmpty(t, updatedETag)
	assert.NotEqual(t, initialETag, updatedETag)

	staleRecorder := httptest.NewRecorder()
	staleContext, _ := gin.CreateTestContext(staleRecorder)
	staleContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel-routing/v2/runtime-settings", bytes.NewReader(body))
	staleContext.Request.Header.Set("If-Match", initialETag)
	staleContext.Set("id", 11)
	UpdateChannelRoutingRuntimeSettings(staleContext)
	assert.Equal(t, http.StatusConflict, staleRecorder.Code, staleRecorder.Body.String())
	assert.Equal(t, updatedETag, staleRecorder.Header().Get("ETag"))
	assert.Contains(t, staleRecorder.Body.String(), `"current_etag"`)

	audits, err := model.ListRoutingControlAuditsContext(context.Background(), model.RoutingControlAuditFilter{
		SubjectType: model.RoutingControlSubjectRuntimeSettings,
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, audits, 2)
	assert.Equal(t, model.RoutingControlActionUpdate, audits[0].Action)
	assert.Equal(t, 10, audits[0].ActorID)
	assert.Equal(t, initial.Data.DocumentHash, audits[0].BeforeHash)
	assert.NotEqual(t, audits[0].BeforeHash, audits[0].AfterHash)
	assert.Equal(t, model.RoutingControlActionBootstrap, audits[1].Action)
}

func TestLegacySmartRoutingSettingsCannotBypassRuntimeCAS(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)

	currentRecorder := httptest.NewRecorder()
	currentContext, _ := gin.CreateTestContext(currentRecorder)
	currentContext.Request = httptest.NewRequest(http.MethodGet, "/api/smart-routing/settings", nil)
	GetSmartRoutingSettings(currentContext)
	require.Equal(t, http.StatusOK, currentRecorder.Code)

	var envelope struct {
		Data smart_routing_setting.SmartRoutingSetting `json:"data"`
	}
	require.NoError(t, common.Unmarshal(currentRecorder.Body.Bytes(), &envelope))
	body, err := common.Marshal(envelope.Data)
	require.NoError(t, err)

	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/settings", bytes.NewReader(body))
	updateContext.Set("id", 10)
	UpdateSmartRoutingSettings(updateContext)

	assert.Equal(t, http.StatusPreconditionRequired, updateRecorder.Code)
}
