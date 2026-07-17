package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeChannelRoutingRuntimeSettingsRequiresCompleteStrictFiniteDocument(t *testing.T) {
	smart_routing_setting.ResetForTest()
	t.Cleanup(smart_routing_setting.ResetForTest)
	valid, err := common.Marshal(smart_routing_setting.GetStoredSetting())
	require.NoError(t, err)

	var fields map[string]json.RawMessage
	require.NoError(t, common.Unmarshal(valid, &fields))

	tests := []struct {
		name       string
		mutate     func(map[string]json.RawMessage) []byte
		wantField  string
		wantReason string
	}{
		{
			name: "unknown field",
			mutate: func(candidate map[string]json.RawMessage) []byte {
				candidate["future_mode"] = json.RawMessage(`true`)
				encoded, marshalErr := common.Marshal(candidate)
				require.NoError(t, marshalErr)
				return encoded
			},
			wantField: "future_mode", wantReason: "unknown_field",
		},
		{
			name: "missing field",
			mutate: func(candidate map[string]json.RawMessage) []byte {
				delete(candidate, "mode")
				encoded, marshalErr := common.Marshal(candidate)
				require.NoError(t, marshalErr)
				return encoded
			},
			wantField: "mode", wantReason: "required",
		},
		{
			name: "wrong boolean type",
			mutate: func(candidate map[string]json.RawMessage) []byte {
				candidate["enabled"] = json.RawMessage(`"true"`)
				encoded, marshalErr := common.Marshal(candidate)
				require.NoError(t, marshalErr)
				return encoded
			},
			wantField: "enabled", wantReason: "expected_boolean",
		},
		{
			name: "fractional integer",
			mutate: func(candidate map[string]json.RawMessage) []byte {
				candidate["top_k"] = json.RawMessage(`1.5`)
				encoded, marshalErr := common.Marshal(candidate)
				require.NoError(t, marshalErr)
				return encoded
			},
			wantField: "top_k", wantReason: "expected_integer",
		},
		{
			name: "non finite float overflow",
			mutate: func(candidate map[string]json.RawMessage) []byte {
				candidate["weight_availability"] = json.RawMessage(`1e10000`)
				encoded, marshalErr := common.Marshal(candidate)
				require.NoError(t, marshalErr)
				return encoded
			},
			wantField: "weight_availability", wantReason: "invalid_value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := make(map[string]json.RawMessage, len(fields))
			for key, value := range fields {
				candidate[key] = append(json.RawMessage(nil), value...)
			}
			_, decodeErr := decodeChannelRoutingRuntimeSettings(bytes.NewReader(test.mutate(candidate)))
			require.Error(t, decodeErr)
			var fieldErr *channelRoutingRuntimeSettingsFieldError
			require.True(t, errors.As(decodeErr, &fieldErr))
			assert.Equal(t, test.wantField, fieldErr.Field)
			assert.Equal(t, test.wantReason, fieldErr.Reason)
		})
	}

	decoded, err := decodeChannelRoutingRuntimeSettings(bytes.NewReader(valid))
	require.NoError(t, err)
	assert.Equal(t, smart_routing_setting.GetStoredSetting(), decoded)
	_, err = decodeChannelRoutingRuntimeSettings(bytes.NewBufferString(`{"enabled":NaN}`))
	assert.ErrorIs(t, err, model.ErrRoutingRuntimeSettingsInvalid)
}

func TestChannelRoutingRuntimeSettingsValidationErrorMapsToFormField(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/runtime-settings", nil)
	GetChannelRoutingRuntimeSettings(getContext)
	require.Equal(t, http.StatusOK, getRecorder.Code)

	var initial struct {
		Data channelRoutingRuntimeSettingsView `json:"data"`
	}
	require.NoError(t, common.Unmarshal(getRecorder.Body.Bytes(), &initial))
	fields := map[string]any{}
	body, err := common.Marshal(initial.Data.StoredSettings)
	require.NoError(t, err)
	require.NoError(t, common.Unmarshal(body, &fields))
	delete(fields, "active_probe_timeout_ms")
	body, err = common.Marshal(fields)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/channel-routing/runtime-settings", bytes.NewReader(body))
	ctx.Request.Header.Set("If-Match", getRecorder.Header().Get("ETag"))
	ctx.Set("id", 10)
	UpdateChannelRoutingRuntimeSettings(ctx)

	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	var response struct {
		Code        string            `json:"code"`
		Field       string            `json:"field"`
		Reason      string            `json:"reason"`
		FieldErrors map[string]string `json:"field_errors"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "invalid_runtime_settings", response.Code)
	assert.Equal(t, "active_probe_timeout_ms", response.Field)
	assert.Equal(t, "required", response.Reason)
	assert.Equal(t, "required", response.FieldErrors["active_probe_timeout_ms"])
}

func TestChannelRoutingRuntimeSettingsUsesStrongETagCASAndPersistentAudit(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	channelrouting.ResetRoutingEventsForTest()
	t.Cleanup(channelrouting.ResetRoutingEventsForTest)

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/runtime-settings", nil)
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
	missingContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel-routing/runtime-settings", bytes.NewReader(body))
	missingContext.Set("id", 10)
	UpdateChannelRoutingRuntimeSettings(missingContext)
	assert.Equal(t, http.StatusPreconditionRequired, missingRecorder.Code)

	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel-routing/runtime-settings", bytes.NewReader(body))
	updateContext.Request.Header.Set("If-Match", initialETag)
	updateContext.Set("id", 10)
	UpdateChannelRoutingRuntimeSettings(updateContext)
	require.Equal(t, http.StatusOK, updateRecorder.Code, updateRecorder.Body.String())
	updatedETag := updateRecorder.Header().Get("ETag")
	require.NotEmpty(t, updatedETag)
	assert.NotEqual(t, initialETag, updatedETag)
	var updatedResponse struct {
		Data channelRoutingRuntimeSettingsView `json:"data"`
	}
	require.NoError(t, common.Unmarshal(updateRecorder.Body.Bytes(), &updatedResponse))
	events := channelrouting.RecentRoutingEvents(1, channelrouting.RoutingEventTypeRuntimeSettingsChanged)
	require.Len(t, events, 1)
	assert.Equal(t, uint64(updatedResponse.Data.Revision), events[0].Revision)
	assert.JSONEq(t, `{"revision":2,"document_hash":"`+updatedResponse.Data.DocumentHash+`","updated_by":10}`, string(events[0].PayloadJSON))

	staleRecorder := httptest.NewRecorder()
	staleContext, _ := gin.CreateTestContext(staleRecorder)
	staleContext.Request = httptest.NewRequest(http.MethodPut, "/api/channel-routing/runtime-settings", bytes.NewReader(body))
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

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/control-audits?limit=10", nil)
	ListChannelRoutingControlAudits(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	assert.Contains(t, listRecorder.Body.String(), `"event_type":"runtime_settings.update"`)
	assert.Contains(t, listRecorder.Body.String(), `"actor_name":`)
	assert.NotContains(t, listRecorder.Body.String(), `"before_hash"`)
	assert.NotContains(t, listRecorder.Body.String(), `"after_hash"`)

	technicalRecorder := httptest.NewRecorder()
	technicalContext, _ := gin.CreateTestContext(technicalRecorder)
	technicalContext.Params = gin.Params{{Key: "id", Value: fmt.Sprintf("%d", audits[0].ID)}}
	technicalContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/control-audits/1/technical", nil)
	GetChannelRoutingControlAuditTechnical(technicalContext)
	require.Equal(t, http.StatusOK, technicalRecorder.Code, technicalRecorder.Body.String())
	assert.Contains(t, technicalRecorder.Body.String(), initial.Data.DocumentHash)
	assert.Contains(t, technicalRecorder.Body.String(), audits[0].AfterHash)
}
