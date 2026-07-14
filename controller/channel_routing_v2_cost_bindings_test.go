package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/service/channelrouting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelRoutingCostBindingAPIUsesPagingMaskedViewsAndETagCAS(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	channelrouting.ResetRoutingEventsForTest()
	t.Cleanup(channelrouting.ResetRoutingEventsForTest)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelHealthState{},
	))
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 501, Name: "cost-one", Key: "serving-one", Status: common.ChannelStatusEnabled},
		{Id: 502, Name: "cost-two", Key: "serving-two", Status: common.ChannelStatusEnabled},
	}).Error)

	createBody := []byte(`{
		"channel_id":501,
		"upstream_type":"newapi",
		"base_url":"https://cost-one.example",
		"upstream_group":"vip",
		"new_api_user_id":42,
		"enabled":true,
		"credentials":{
			"new_api_access_token":"newapi-access-secret",
			"gateway_api_key":"gateway-secret"
		}
	}`)
	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	createContext.Request = httptest.NewRequest(
		http.MethodPost, "/api/channel-routing/v2/cost-bindings", bytes.NewReader(createBody),
	)
	createContext.Set("id", 10)
	CreateChannelRoutingCostBinding(createContext)
	require.Equal(t, http.StatusCreated, createRecorder.Code, createRecorder.Body.String())
	createETag := createRecorder.Header().Get("ETag")
	require.NotEmpty(t, createETag)
	assert.NotContains(t, createRecorder.Body.String(), "newapi-access-secret")
	assert.NotContains(t, createRecorder.Body.String(), "gateway-secret")
	assert.Contains(t, createRecorder.Body.String(), `"new_api_access_token":"****cret"`)
	assert.Contains(t, createRecorder.Body.String(), `"gateway_api_key":"****cret"`)
	assert.Contains(t, createRecorder.Body.String(), `"etag":`)

	require.NoError(t, db.Create(&model.RoutingChannelBinding{
		ChannelID: 502, UpstreamType: model.RoutingUpstreamTypeSub2API,
		BaseURL: "https://cost-two.example", UpstreamGroup: "default", Enabled: false,
	}).Error)

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(
		http.MethodGet,
		"/api/channel-routing/v2/cost-bindings?page=1&page_size=1&upstream_type=newapi&enabled=true&search=cost-one",
		nil,
	)
	ListChannelRoutingCostBindings(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	var listEnvelope struct {
		Success bool `json:"success"`
		Data    struct {
			Items    []routingBindingView `json:"items"`
			Total    int64                `json:"total"`
			Page     int                  `json:"page"`
			PageSize int                  `json:"page_size"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(listRecorder.Body.Bytes(), &listEnvelope))
	require.True(t, listEnvelope.Success)
	assert.Equal(t, int64(1), listEnvelope.Data.Total)
	assert.Equal(t, 1, listEnvelope.Data.Page)
	assert.Equal(t, 1, listEnvelope.Data.PageSize)
	require.Len(t, listEnvelope.Data.Items, 1)
	assert.Equal(t, 501, listEnvelope.Data.Items[0].ChannelID)
	assert.Equal(t, createETag, listEnvelope.Data.Items[0].ETag)

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Params = gin.Params{{Key: "channelId", Value: "501"}}
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/cost-bindings/501", nil)
	GetChannelRoutingCostBinding(getContext)
	require.Equal(t, http.StatusOK, getRecorder.Code, getRecorder.Body.String())
	assert.Equal(t, createETag, getRecorder.Header().Get("ETag"))

	updateBody := []byte(`{
		"upstream_type":"newapi",
		"base_url":"https://cost-one.example/v2",
		"upstream_group":"enterprise",
		"new_api_user_id":43,
		"enabled":false,
		"credentials":{"gateway_api_key":"rotated-gateway-secret"}
	}`)
	missingRecorder := httptest.NewRecorder()
	missingContext, _ := gin.CreateTestContext(missingRecorder)
	missingContext.Params = getContext.Params
	missingContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/cost-bindings/501", bytes.NewReader(updateBody),
	)
	UpdateChannelRoutingCostBinding(missingContext)
	assert.Equal(t, http.StatusPreconditionRequired, missingRecorder.Code)

	staleRecorder := httptest.NewRecorder()
	staleContext, _ := gin.CreateTestContext(staleRecorder)
	staleContext.Params = getContext.Params
	staleContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/cost-bindings/501", bytes.NewReader(updateBody),
	)
	staleContext.Request.Header.Set("If-Match", strings.Replace(createETag, ".", ".stale.", 1))
	UpdateChannelRoutingCostBinding(staleContext)
	assert.Equal(t, http.StatusBadRequest, staleRecorder.Code)

	validButStaleETag := staleChannelRoutingCostBindingETag(t, createETag)
	conflictRecorder := httptest.NewRecorder()
	conflictContext, _ := gin.CreateTestContext(conflictRecorder)
	conflictContext.Params = getContext.Params
	conflictContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/cost-bindings/501", bytes.NewReader(updateBody),
	)
	conflictContext.Request.Header.Set("If-Match", validButStaleETag)
	UpdateChannelRoutingCostBinding(conflictContext)
	assert.Equal(t, http.StatusConflict, conflictRecorder.Code)
	assert.Equal(t, createETag, conflictRecorder.Header().Get("ETag"))
	assert.Contains(t, conflictRecorder.Body.String(), `"code":"cost_binding_conflict"`)
	assert.NotContains(t, conflictRecorder.Body.String(), "newapi-access-secret")

	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Params = getContext.Params
	updateContext.Request = httptest.NewRequest(
		http.MethodPut, "/api/channel-routing/v2/cost-bindings/501", bytes.NewReader(updateBody),
	)
	updateContext.Request.Header.Set("If-Match", createETag)
	updateContext.Set("id", 10)
	UpdateChannelRoutingCostBinding(updateContext)
	require.Equal(t, http.StatusOK, updateRecorder.Code, updateRecorder.Body.String())
	updateETag := updateRecorder.Header().Get("ETag")
	require.NotEmpty(t, updateETag)
	assert.NotEqual(t, createETag, updateETag)
	assert.NotContains(t, updateRecorder.Body.String(), "rotated-gateway-secret")

	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", 501).First(&updated).Error)
	credentials, err := updated.GetCredentials()
	require.NoError(t, err)
	assert.Equal(t, "newapi-access-secret", credentials.NewAPIAccessToken)
	assert.Equal(t, "rotated-gateway-secret", credentials.GatewayAPIKey)
	assert.Equal(t, "enterprise", updated.UpstreamGroup)
	assert.False(t, updated.Enabled)

	events := channelrouting.RecentRoutingEvents(10, channelrouting.RoutingEventTypeCostBindingChanged)
	require.Len(t, events, 2)
	assert.NotContains(t, string(events[0].PayloadJSON), "secret")
	var audits []model.RoutingControlAudit
	require.NoError(t, db.Where("subject_type = ? AND subject_id = ?", model.RoutingControlSubjectCostBinding, 501).
		Order("id asc").Find(&audits).Error)
	require.Len(t, audits, 2)
	assert.Equal(t, model.RoutingControlActionCreate, audits[0].Action)
	assert.Equal(t, model.RoutingControlActionUpdate, audits[1].Action)
	assert.Equal(t, 10, audits[1].ActorID)
	assert.NotContains(t, audits[0].SummaryJSON+audits[1].SummaryJSON, "secret")
}

func TestDeleteChannelRoutingCostBindingPreservesServingStateAndImmutableHistory(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingChannelHealthState{},
	))
	require.NoError(t, db.Create(&model.Channel{
		Id: 601, Name: "serving-channel", Key: "serving-secret", Status: common.ChannelStatusEnabled,
	}).Error)
	binding := model.RoutingChannelBinding{
		ChannelID: 601, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://cost.example", UpstreamGroup: "vip", Enabled: true,
	}
	require.NoError(t, db.Create(&binding).Error)
	require.NoError(t, db.Create(&model.RoutingCostSnapshot{
		ChannelID: 601, ModelName: "gpt-test", SnapshotTS: common.GetTimestamp(),
	}).Error)
	require.NoError(t, db.Create(&model.RoutingCostSnapshotVersion{
		SchemaVersion: 1, PricingHash: strings.Repeat("a", 64), ContentHash: strings.Repeat("b", 64),
		ApplyToken: strings.Repeat("c", 32), AccountID: 1, AccountKey: strings.Repeat("d", 64),
		SourceType: model.RoutingUpstreamTypeNewAPI, ChannelID: 601, UpstreamGroup: "vip",
		UpstreamGroupKey: strings.Repeat("e", 64), UpstreamModel: "gpt-test",
		UpstreamModelKey: strings.Repeat("f", 64), LocalModel: "gpt-test",
		LocalModelKey: strings.Repeat("1", 64), ObservedTime: 1, EffectiveTime: 1, ExpiresTime: 2,
		PricingVersion: "v1", PricingJSON: `{}`, Confidence: "full", ConfidenceScore: 1,
		Freshness: "fresh", FreshnessScore: 1, SourceSyncStatus: "succeeded", CreatedTime: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelMetric{
		ChannelID: 601, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		ModelName: "gpt-test", Group: "vip", BucketTs: 1, RequestCount: 10,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingBreakerState{
		ChannelID: 601, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		ModelName: "gpt-test", Group: "vip", State: model.RoutingBreakerStateOpen,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelHealthState{
		ChannelID: 601, AuthFailure: true, AuthFailureReason: "serving credential rejected",
		AuthFailureUntil: common.GetTimestamp() + 60, BalanceKnown: true, Balance: 10,
		BalanceUpdatedTime: common.GetTimestamp(), UpdatedTime: common.GetTimestamp(),
	}).Error)

	key := routinghotcache.Key{
		ChannelID: 601, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip",
	}
	routinghotcache.SetCostForTest(routinghotcache.CostKey{ChannelID: 601, Model: "gpt-test"}, routinghotcache.CostSnapshot{Known: true, Cost: 1})
	routinghotcache.SetBalanceForTest(601, routinghotcache.BalanceSnapshot{Known: true, Balance: 10})
	routinghotcache.SetMetricForTest(key, routinghotcache.MetricSnapshot{RequestCount: 10})
	routinghotcache.SetBreakerForTest(key, routinghotcache.BreakerSnapshot{State: model.RoutingBreakerStateOpen})
	routinghotcache.SetAuthFailureForTest(601, routinghotcache.HealthMarker{Marked: true})

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Params = gin.Params{{Key: "channelId", Value: strconv.Itoa(binding.ChannelID)}}
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/cost-bindings/601", nil)
	GetChannelRoutingCostBinding(getContext)
	require.Equal(t, http.StatusOK, getRecorder.Code, getRecorder.Body.String())
	etag := getRecorder.Header().Get("ETag")
	require.NotEmpty(t, etag)

	staleRecorder := httptest.NewRecorder()
	staleContext, _ := gin.CreateTestContext(staleRecorder)
	staleContext.Params = getContext.Params
	staleContext.Request = httptest.NewRequest(http.MethodDelete, "/api/channel-routing/v2/cost-bindings/601", nil)
	staleContext.Request.Header.Set("If-Match", staleChannelRoutingCostBindingETag(t, etag))
	DeleteChannelRoutingCostBinding(staleContext)
	assert.Equal(t, http.StatusConflict, staleRecorder.Code)

	deleteRecorder := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRecorder)
	deleteContext.Params = getContext.Params
	deleteContext.Request = httptest.NewRequest(http.MethodDelete, "/api/channel-routing/v2/cost-bindings/601", nil)
	deleteContext.Request.Header.Set("If-Match", etag)
	deleteContext.Set("id", 10)
	DeleteChannelRoutingCostBinding(deleteContext)
	require.Equal(t, http.StatusOK, deleteRecorder.Code, deleteRecorder.Body.String())

	assert.ErrorIs(t, db.Where("channel_id = ?", 601).First(&model.RoutingChannelBinding{}).Error, gorm.ErrRecordNotFound)
	var latestCount int64
	require.NoError(t, db.Model(&model.RoutingCostSnapshot{}).Where("channel_id = ?", 601).Count(&latestCount).Error)
	assert.Zero(t, latestCount)
	var historyCount int64
	require.NoError(t, db.Model(&model.RoutingCostSnapshotVersion{}).Where("channel_id = ?", 601).Count(&historyCount).Error)
	assert.Equal(t, int64(1), historyCount)
	var metricCount int64
	require.NoError(t, db.Model(&model.RoutingChannelMetric{}).Where("channel_id = ?", 601).Count(&metricCount).Error)
	assert.Equal(t, int64(1), metricCount)
	var breakerCount int64
	require.NoError(t, db.Model(&model.RoutingBreakerState{}).Where("channel_id = ?", 601).Count(&breakerCount).Error)
	assert.Equal(t, int64(1), breakerCount)
	var health model.RoutingChannelHealthState
	require.NoError(t, db.Where("channel_id = ?", 601).First(&health).Error)
	assert.True(t, health.AuthFailure)
	assert.False(t, health.BalanceKnown)
	var deleteAudit model.RoutingControlAudit
	require.NoError(t, db.Where("subject_type = ? AND subject_id = ?", model.RoutingControlSubjectCostBinding, 601).
		Order("id desc").First(&deleteAudit).Error)
	assert.Equal(t, model.RoutingControlActionDelete, deleteAudit.Action)
	assert.Equal(t, 10, deleteAudit.ActorID)

	_, costFound := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: 601, Model: "gpt-test"})
	_, balanceFound := routinghotcache.GetBalance(601)
	_, metricFound := routinghotcache.GetMetric(key)
	_, breakerFound := routinghotcache.GetBreaker(key)
	_, authFound := routinghotcache.GetAuthFailure(601)
	assert.False(t, costFound)
	assert.False(t, balanceFound)
	assert.True(t, metricFound)
	assert.True(t, breakerFound)
	assert.True(t, authFound)

	match, err := model.RoutingChannelBindingMatchesContext(context.Background(), 601, binding.ID)
	require.NoError(t, err)
	assert.False(t, match)
}

func staleChannelRoutingCostBindingETag(t *testing.T, etag string) string {
	t.Helper()
	require.Greater(t, len(etag), 2)
	index := len(etag) - 2
	replacement := byte('a')
	if etag[index] == replacement {
		replacement = 'b'
	}
	return etag[:index] + string(replacement) + etag[index+1:]
}

func TestChannelRoutingCostBindingViewsFailClosedOnDatabaseErrorsAndDegradeCredentialErrors(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.Create(&model.Channel{
		Id: 701, Name: "cost-view", Key: "serving-key", Status: common.ChannelStatusEnabled,
	}).Error)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	views, err := channelRoutingCostBindingViews(cancelled, []model.RoutingChannelBinding{{ChannelID: 701}})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, views)

	invalidEnvelope := `{"version":2,"key_id":"missing","nonce":"AA==","ciphertext":"AA=="}`
	views, err = channelRoutingCostBindingViews(context.Background(), []model.RoutingChannelBinding{{
		ID: 17, ChannelID: 701, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://cost-view.example", UpstreamGroup: "default", Enabled: true,
		KeyVersion: model.RoutingCredentialKeyVersion, EncCredentials: &invalidEnvelope,
		CreatedTime: 100, UpdatedTime: 100,
	}})
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, "cost-view", views[0].ChannelName)
	assert.Equal(t, "Stored credentials are unavailable. Re-enter them to repair this binding.", views[0].CredentialError)
	assert.Empty(t, views[0].CredentialMasks.NewAPIAccessToken)
	assert.NotContains(t, views[0].CredentialError, invalidEnvelope)

	require.NoError(t, db.Create(&model.RoutingChannelBinding{
		ChannelID: 701, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://cost-view.example", UpstreamGroup: "default", Enabled: true,
		KeyVersion: model.RoutingCredentialKeyVersion, EncCredentials: &invalidEnvelope,
	}).Error)
	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/api/channel-routing/v2/cost-bindings", nil)
	ListChannelRoutingCostBindings(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code, listRecorder.Body.String())
	assert.Contains(t, listRecorder.Body.String(), `"credential_error":"Stored credentials are unavailable. Re-enter them to repair this binding."`)
	assert.NotContains(t, listRecorder.Body.String(), invalidEnvelope)

	legacyRecorder := httptest.NewRecorder()
	legacyContext, _ := gin.CreateTestContext(legacyRecorder)
	legacyContext.Request = httptest.NewRequest(http.MethodGet, "/api/smart-routing/bindings", nil)
	ListSmartRoutingBindings(legacyContext)
	require.Equal(t, http.StatusOK, legacyRecorder.Code, legacyRecorder.Body.String())
	assert.Contains(t, legacyRecorder.Body.String(), `"credential_error":"Stored credentials are unavailable. Re-enter them to repair this binding."`)
	assert.NotContains(t, legacyRecorder.Body.String(), invalidEnvelope)
}

func TestChannelRoutingCostBindingETagTracksConfigurationOnly(t *testing.T) {
	credentialEnvelope := "encrypted-config"
	egressPolicy := `{"allowed_private_cidrs":["10.0.0.0/24"]}`
	lastSyncError := "temporary upstream failure"
	binding := model.RoutingChannelBinding{
		ID: 23, ChannelID: 701, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://cost.example", UpstreamGroup: "vip", ServesClaudeCode: false,
		EgressPolicyJSON: &egressPolicy, EncCredentials: &credentialEnvelope,
		KeyVersion: model.RoutingCredentialKeyVersion, Enabled: true,
		CreatedTime: 100, UpdatedTime: 200,
	}
	original := channelRoutingCostBindingETag(binding)

	operationalUpdate := binding
	operationalUpdate.SyncFailureCount = 7
	operationalUpdate.SyncBackoffUntil = 9_999
	operationalUpdate.LastSyncError = &lastSyncError
	operationalUpdate.UpdatedTime = 300
	assert.Equal(t, original, channelRoutingCostBindingETag(operationalUpdate))

	configurationUpdate := operationalUpdate
	configurationUpdate.UpstreamGroup = "enterprise"
	assert.NotEqual(t, original, channelRoutingCostBindingETag(configurationUpdate))
	require.NoError(t, parseChannelRoutingCostBindingETag(original))
	assert.Error(t, parseChannelRoutingCostBindingETag(strings.Replace(original, ".1.", ".2.", 1)))
}

func TestChannelRoutingCostBindingUpdateCanReplaceUnreadableCredentials(t *testing.T) {
	db := openChannelRoutingControllerDB(t)
	withChannelRoutingControllerState(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelHealthState{},
	))
	require.NoError(t, db.Create(&model.Channel{
		Id: 702, Name: "cost-repair", Key: "serving-key", Status: common.ChannelStatusEnabled,
	}).Error)
	invalidEnvelope := `{"version":2,"key_id":"missing","nonce":"AA==","ciphertext":"AA=="}`
	binding := model.RoutingChannelBinding{
		ChannelID: 702, UpstreamType: model.RoutingUpstreamTypeNewAPI,
		BaseURL: "https://cost-repair.example", UpstreamGroup: "default", Enabled: true,
		KeyVersion: model.RoutingCredentialKeyVersion, EncCredentials: &invalidEnvelope,
	}
	require.NoError(t, db.Create(&binding).Error)

	updateBody := []byte(`{
		"channel_id":702,
		"upstream_type":"newapi",
		"base_url":"https://cost-repair.example",
		"upstream_group":"default",
		"enabled":true,
		"credentials":{"gateway_api_key":"replacement-gateway-key"}
	}`)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/channel-routing/v2/cost-bindings/702", bytes.NewReader(updateBody))
	context.Request.Header.Set("If-Match", channelRoutingCostBindingETag(binding))
	context.Params = gin.Params{{Key: "channelId", Value: "702"}}
	context.Set("id", 10)
	UpdateChannelRoutingCostBinding(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "replacement-gateway-key")

	stored, err := model.GetRoutingChannelBindingContext(context.Request.Context(), 702)
	require.NoError(t, err)
	credentials, err := stored.GetCredentials()
	require.NoError(t, err)
	assert.Equal(t, "replacement-gateway-key", credentials.GatewayAPIKey)
}
