package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type routingCostDoerFunc func(*http.Request) (*http.Response, error)

func (do routingCostDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return do(request)
}

func setSmartRoutingBindingIfMatchForTest(t *testing.T, ctx *gin.Context, channelID int) {
	t.Helper()
	require.NotNil(t, ctx)
	require.NotNil(t, ctx.Request)
	binding, err := model.GetRoutingChannelBindingContext(context.Background(), channelID)
	require.NoError(t, err)
	if ctx.GetInt("id") <= 0 {
		ctx.Set("id", 1)
	}
	ctx.Request.Header.Set("If-Match", channelRoutingCostBindingETag(binding))
}

func TestBuildRoutingBindingViewSanitizesStoredLastSyncError(t *testing.T) {
	secret := "stored-routing-secret"
	unsafeMessage := "Authorization: Bearer " + secret + "\nhttps://upstream.example.com/path?oauth_token=" + secret

	view := buildRoutingBindingView(model.RoutingChannelBinding{
		ID:            1,
		ChannelID:     2,
		LastSyncError: &unsafeMessage,
	}, model.RoutingCredentials{NewAPIAccessToken: secret})

	require.NotNil(t, view.LastSyncError)
	assert.NotContains(t, *view.LastSyncError, secret)
	assert.NotContains(t, *view.LastSyncError, "\n")
	assert.LessOrEqual(t, utf8.RuneCountInString(*view.LastSyncError), common.SafeErrorMaxRunes)
}

func TestUpdateSmartRoutingSettingsPersistenceFailureKeepsPublishedState(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.RoutingRuntimeSettingsState{}, &model.RoutingControlAudit{}))
	smart_routing_setting.ResetForTest()
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		routinghotcache.ResetForTest()
	})
	t.Setenv("SMART_ROUTING_ENABLED", "invalid")
	t.Setenv("SMART_ROUTING_MODE", smart_routing_setting.ModeObserve)
	t.Setenv("SMART_ROUTING_AGENT_ENABLED", "invalid")

	oldSetting := smart_routing_setting.GetSetting()
	oldSetting.Consecutive5xx = 8
	oldSetting = smart_routing_setting.UpdateSetting(oldSetting)
	routingbreaker.ResetDefaultForTest(routingBreakerConfigFromSetting(oldSetting))

	oldValues, err := config.ConfigToMap(oldSetting)
	require.NoError(t, err)
	oldOptions := make(map[string]string, len(oldValues))
	for key, value := range oldValues {
		oldOptions["smart_routing_setting."+key] = value
	}
	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	common.OptionMap = maps.Clone(oldOptions)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	forcedErr := errors.New("forced option write failure")
	optionTable := db.NamingStrategy.TableName("Option")
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(
		"test:fail_smart_routing_option_create",
		func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Table == optionTable {
				tx.AddError(forcedErr)
			}
		},
	))

	request := oldSetting
	request.Mode = smart_routing_setting.ModeBalanced
	request.Consecutive5xx = 1
	body, err := common.Marshal(request)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/settings", bytes.NewReader(body))
	ctx.Set("id", 10)
	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/smart-routing/settings", nil)
	GetSmartRoutingSettings(getContext)
	require.Equal(t, http.StatusOK, getRecorder.Code)
	ctx.Request.Header.Set("If-Match", getRecorder.Header().Get("ETag"))

	UpdateSmartRoutingSettings(ctx)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	var response struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Equal(t, oldSetting, smart_routing_setting.GetSetting())
	common.OptionMapRWMutex.RLock()
	currentOptions := maps.Clone(common.OptionMap)
	common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, oldOptions, currentOptions)

	breakerSnapshot := routingbreaker.RecordReliabilityFailure(routingbreaker.Key{
		ChannelID:   991,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       "gpt-test",
		Group:       "default",
	}, routingbreaker.FailureProvider5xx)
	assert.Equal(t, routingbreaker.StateHealthy, breakerSnapshot.State)
}

func TestUpdateSmartRoutingSettingsDoesNotPersistEnvironmentOverrides(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.RoutingRuntimeSettingsState{}, &model.RoutingControlAudit{}))
	smart_routing_setting.ResetForTest()
	t.Cleanup(func() {
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	t.Setenv("SMART_ROUTING_ENABLED", "invalid")
	t.Setenv("SMART_ROUTING_MODE", smart_routing_setting.ModeObserve)
	t.Setenv("SMART_ROUTING_AGENT_ENABLED", "invalid")
	request := smart_routing_setting.GetSetting()
	request.Enabled = false
	request.Mode = smart_routing_setting.ModeBalanced
	request.AgentEnabled = false
	request.WeightAvailability = 2
	request.WeightLatency = 1
	request.WeightThroughput = 1
	request.WeightCost = 0

	t.Setenv("SMART_ROUTING_ENABLED", "true")
	t.Setenv("SMART_ROUTING_MODE", smart_routing_setting.ModeEnterpriseSLO)
	t.Setenv("SMART_ROUTING_AGENT_ENABLED", "true")

	common.OptionMapRWMutex.Lock()
	previousOptionMap := maps.Clone(common.OptionMap)
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	body, err := common.Marshal(request)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/settings", bytes.NewReader(body))
	ctx.Set("id", 10)
	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/smart-routing/settings", nil)
	GetSmartRoutingSettings(getContext)
	require.Equal(t, http.StatusOK, getRecorder.Code)
	ctx.Request.Header.Set("If-Match", getRecorder.Header().Get("ETag"))

	UpdateSmartRoutingSettings(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool                                      `json:"success"`
		Data    smart_routing_setting.SmartRoutingSetting `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.True(t, response.Data.Enabled)
	assert.Equal(t, smart_routing_setting.ModeEnterpriseSLO, response.Data.Mode)
	assert.True(t, response.Data.AgentEnabled)

	expectedPersisted := map[string]string{
		"smart_routing_setting.enabled":             "false",
		"smart_routing_setting.mode":                smart_routing_setting.ModeBalanced,
		"smart_routing_setting.agent_enabled":       "false",
		"smart_routing_setting.weight_availability": "0.5",
		"smart_routing_setting.weight_latency":      "0.25",
		"smart_routing_setting.weight_throughput":   "0.25",
		"smart_routing_setting.weight_cost":         "0",
	}
	for key, expected := range expectedPersisted {
		var option model.Option
		require.NoError(t, db.First(&option, "key = ?", key).Error)
		assert.Equal(t, expected, option.Value, key)
	}
	common.OptionMapRWMutex.RLock()
	for key, expected := range expectedPersisted {
		assert.Equal(t, expected, common.OptionMap[key], key)
	}
	common.OptionMapRWMutex.RUnlock()
}

func TestBuildRoutingBindingViewMasksCredentials(t *testing.T) {
	token := "sk-1234567890abcdef"
	binding := model.RoutingChannelBinding{
		ID:               10,
		ChannelID:        20,
		UpstreamType:     model.RoutingUpstreamTypeNewAPI,
		BaseURL:          "https://upstream.example.com",
		UpstreamGroup:    "vip",
		EncCredentials:   &token,
		Enabled:          true,
		SyncFailureCount: 3,
	}

	view := buildRoutingBindingView(binding, model.RoutingCredentials{
		NewAPIAccessToken: token,
		GatewayAPIKey:     "gw-secret-987654",
		Sub2APIPassword:   "password-secret",
	})

	require.Equal(t, binding.ChannelID, view.ChannelID)
	assert.Equal(t, 3, view.SyncFailureCount)
	assert.Equal(t, "****cdef", view.CredentialMasks.NewAPIAccessToken)
	assert.Equal(t, "****7654", view.CredentialMasks.GatewayAPIKey)
	assert.Empty(t, view.CredentialMasks.Sub2APIPassword)
	assert.NotContains(t, view.CredentialMasks.NewAPIAccessToken, "1234567890")
}

func TestBuildRoutingBindingViewMasksSub2APIEmail(t *testing.T) {
	view := buildRoutingBindingView(model.RoutingChannelBinding{ChannelID: 20}, model.RoutingCredentials{
		Sub2APIEmail: "operator@example.com",
	})

	assert.Equal(t, "o******r@example.com", view.CredentialMasks.Sub2APIEmail)
	assert.NotContains(t, view.CredentialMasks.Sub2APIEmail, "operator")
}

func TestBuildRoutingCredentialsKeepsEmptyEditFieldsUnset(t *testing.T) {
	request := routingBindingRequest{
		UpstreamType: model.RoutingUpstreamTypeSub2API,
		Credentials: routingCredentialRequest{
			Sub2APIEmail:    common.GetPointer("admin@example.com"),
			Sub2APIPassword: common.GetPointer(""),
			Sub2APIToken:    common.GetPointer("jwt-token"),
		},
	}

	credentials := buildRoutingCredentials(request)

	assert.Equal(t, "admin@example.com", credentials.Sub2APIEmail)
	assert.Empty(t, credentials.Sub2APIPassword)
	assert.Equal(t, "jwt-token", credentials.Sub2APIToken)
}

func TestBuildRoutingCredentialsDropsCredentialsFromAnotherProvider(t *testing.T) {
	newAPICredentials := buildRoutingCredentials(routingBindingRequest{
		UpstreamType: model.RoutingUpstreamTypeNewAPI,
		Credentials: routingCredentialRequest{
			NewAPIAccessToken: common.GetPointer("newapi-token"),
			Sub2APIEmail:      common.GetPointer("must-not-survive@example.com"),
			Sub2APIToken:      common.GetPointer("must-not-survive"),
		},
	}, model.RoutingCredentials{
		Sub2APIEmail:    "stored@example.com",
		Sub2APIPassword: "stored-password",
		Sub2APIToken:    "stored-token",
	})
	assert.Equal(t, "newapi-token", newAPICredentials.NewAPIAccessToken)
	assert.Empty(t, newAPICredentials.Sub2APIEmail)
	assert.Empty(t, newAPICredentials.Sub2APIPassword)
	assert.Empty(t, newAPICredentials.Sub2APIToken)

	sub2APICredentials := buildRoutingCredentials(routingBindingRequest{
		UpstreamType: model.RoutingUpstreamTypeSub2API,
		Credentials: routingCredentialRequest{
			NewAPIAccessToken: common.GetPointer("must-not-survive"),
			Sub2APIToken:      common.GetPointer("sub2api-token"),
		},
	}, model.RoutingCredentials{NewAPIAccessToken: "stored-newapi-token"})
	assert.Empty(t, sub2APICredentials.NewAPIAccessToken)
	assert.Equal(t, "sub2api-token", sub2APICredentials.Sub2APIToken)
}

func TestRoutingBindingFromRequestClearsUpstreamSpecificFields(t *testing.T) {
	userID := 42
	newAPIBinding, err := routingBindingFromRequest(routingBindingRequest{
		ChannelID:        11,
		UpstreamType:     model.RoutingUpstreamTypeNewAPI,
		BaseURL:          "https://newapi.example.com",
		UpstreamGroup:    "vip",
		ServesClaudeCode: true,
		NewAPIUserID:     &userID,
	})
	require.NoError(t, err)
	assert.False(t, newAPIBinding.ServesClaudeCode)
	require.NotNil(t, newAPIBinding.NewAPIUserID)

	sub2apiBinding, err := routingBindingFromRequest(routingBindingRequest{
		ChannelID:        12,
		UpstreamType:     model.RoutingUpstreamTypeSub2API,
		BaseURL:          "https://sub2api.example.com",
		UpstreamGroup:    "vip",
		ServesClaudeCode: true,
		NewAPIUserID:     &userID,
	})
	require.NoError(t, err)
	assert.True(t, sub2apiBinding.ServesClaudeCode)
	assert.Nil(t, sub2apiBinding.NewAPIUserID)
}

func TestValidateRoutingBindingRequestRejectsUnsafeBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "plain http", baseURL: "http://example.com"},
		{name: "userinfo", baseURL: "https://token@example.com"},
		{name: "query", baseURL: "https://example.com?tenant=example"},
		{name: "empty query", baseURL: "https://example.com?"},
		{name: "fragment", baseURL: "https://example.com#tenant"},
		{name: "loopback", baseURL: "https://127.0.0.1"},
		{name: "private network", baseURL: "https://10.0.0.1"},
		{name: "metadata", baseURL: "https://169.254.169.254"},
		{name: "unsupported scheme", baseURL: "file:///tmp/socket"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRoutingBindingRequest(routingBindingRequest{
				ChannelID:     1,
				UpstreamType:  model.RoutingUpstreamTypeNewAPI,
				BaseURL:       tt.baseURL,
				UpstreamGroup: "vip",
			}, true, true)

			require.Error(t, err)
		})
	}
}

func TestUpdateSmartRoutingBindingMergesOnlyProviderCompatibleCredentialFields(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelHealthState{}))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     55,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       "https://upstream.example.com",
		UpstreamGroup: "vip",
		NewAPIUserID:  common.GetPointer(42),
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{
		NewAPIAccessToken: "old-access-token",
		Sub2APIPassword:   "old-password",
	}))
	require.NoError(t, db.Create(&binding).Error)

	body := []byte(`{
		"upstream_type":"newapi",
		"base_url":"https://upstream.example.com",
		"upstream_group":"vip",
		"new_api_user_id":42,
		"credentials":{"gateway_api_key":"new-gateway-key"}
	}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "55"}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/bindings/55", bytes.NewReader(body))
	setSmartRoutingBindingIfMatchForTest(t, ctx, binding.ChannelID)

	UpdateSmartRoutingBinding(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", 55).First(&updated).Error)
	credentials, err := updated.GetCredentials()
	require.NoError(t, err)
	assert.Equal(t, "old-access-token", credentials.NewAPIAccessToken)
	assert.Equal(t, "new-gateway-key", credentials.GatewayAPIKey)
	assert.Empty(t, credentials.Sub2APIPassword)
}

func TestUpdateSmartRoutingBindingInvalidatesCostState(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelHealthState{}))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-invalidate-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	binding := model.RoutingChannelBinding{
		ChannelID:     56,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       "https://old.example.com",
		UpstreamGroup: "old-group",
		NewAPIUserID:  common.GetPointer(42),
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{
		NewAPIAccessToken: "access-token",
		GatewayAPIKey:     "gateway-key",
	}))
	require.NoError(t, db.Create(&binding).Error)
	require.NoError(t, db.Create(&model.RoutingCostSnapshot{ChannelID: binding.ChannelID, ModelName: "gpt-old", SnapshotTS: common.GetTimestamp()}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelHealthState{
		ChannelID:          binding.ChannelID,
		AuthFailure:        true,
		AuthFailureReason:  "serving credential rejected",
		AuthFailureUntil:   common.GetTimestamp() + 60,
		BalanceKnown:       true,
		Balance:            10,
		BalanceUpdatedTime: common.GetTimestamp(),
		UpdatedTime:        common.GetTimestamp(),
	}).Error)
	routinghotcache.SetCostForTest(routinghotcache.CostKey{ChannelID: binding.ChannelID, Model: "gpt-old"}, routinghotcache.CostSnapshot{Known: true, Cost: 1})
	routinghotcache.SetBalanceForTest(binding.ChannelID, routinghotcache.BalanceSnapshot{Known: true, Balance: 10})
	routingKey := routinghotcache.Key{ChannelID: binding.ChannelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-old", Group: "old-group"}
	routinghotcache.SetMetricForTest(routingKey, routinghotcache.MetricSnapshot{RequestCount: 1})
	routinghotcache.SetBreakerForTest(routingKey, routinghotcache.BreakerSnapshot{State: model.RoutingBreakerStateOpen})
	routinghotcache.SetAuthFailureForTest(binding.ChannelID, routinghotcache.HealthMarker{Marked: true})

	body := []byte(`{
		"upstream_type":"newapi",
		"base_url":"https://new.example.com",
		"upstream_group":"new-group",
		"new_api_user_id":42,
		"enabled":true
	}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "56"}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/bindings/56", bytes.NewReader(body))
	setSmartRoutingBindingIfMatchForTest(t, ctx, binding.ChannelID)

	UpdateSmartRoutingBinding(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var snapshotCount int64
	require.NoError(t, db.Model(&model.RoutingCostSnapshot{}).Where("channel_id = ?", binding.ChannelID).Count(&snapshotCount).Error)
	assert.Zero(t, snapshotCount)
	var health model.RoutingChannelHealthState
	require.NoError(t, db.Where("channel_id = ?", binding.ChannelID).First(&health).Error)
	assert.False(t, health.BalanceKnown)
	assert.True(t, health.AuthFailure)
	_, costFound := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: binding.ChannelID, Model: "gpt-old"})
	_, balanceFound := routinghotcache.GetBalance(binding.ChannelID)
	_, metricFound := routinghotcache.GetMetric(routingKey)
	_, breakerFound := routinghotcache.GetBreaker(routingKey)
	_, authFound := routinghotcache.GetAuthFailure(binding.ChannelID)
	assert.False(t, costFound)
	assert.False(t, balanceFound)
	assert.True(t, metricFound)
	assert.True(t, breakerFound)
	assert.True(t, authFound)
}

func TestUpdateSmartRoutingBindingDoesNotReviveDeletedBinding(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelHealthState{}))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-delete-race-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     57,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       "https://old.example.com",
		UpstreamGroup: "old-group",
		NewAPIUserID:  common.GetPointer(42),
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{
		NewAPIAccessToken: "access-token",
		GatewayAPIKey:     "gateway-key",
	}))
	require.NoError(t, db.Create(&binding).Error)

	require.NoError(t, smartRoutingRuntimeStateMu.LockContext(context.Background()))
	stateLocked := true
	t.Cleanup(func() {
		if stateLocked {
			smartRoutingRuntimeStateMu.Unlock()
		}
	})

	queryLoaded := make(chan struct{}, 1)
	const callbackName = "test:observe_routing_binding_load_before_update"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "routing_channel_bindings" {
			return
		}
		select {
		case queryLoaded <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	body := []byte(`{
		"upstream_type":"newapi",
		"base_url":"https://new.example.com",
		"upstream_group":"new-group",
		"new_api_user_id":42,
		"enabled":true
	}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "57"}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/bindings/57", bytes.NewReader(body))
	setSmartRoutingBindingIfMatchForTest(t, ctx, binding.ChannelID)

	done := make(chan struct{})
	go func() {
		UpdateSmartRoutingBinding(ctx)
		close(done)
	}()
	select {
	case <-queryLoaded:
	case <-time.After(time.Second):
		require.FailNow(t, "update did not load the routing binding")
	}
	require.NoError(t, db.Delete(&model.RoutingChannelBinding{}, binding.ID).Error)
	smartRoutingRuntimeStateMu.Unlock()
	stateLocked = false
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "update did not finish after releasing the state lock")
	}

	assert.Contains(t, recorder.Body.String(), `"success":false`)
	var bindingCount int64
	require.NoError(t, db.Model(&model.RoutingChannelBinding{}).Where("channel_id = ?", binding.ChannelID).Count(&bindingCount).Error)
	assert.Zero(t, bindingCount)
}

func TestLoadSmartRoutingBindingGroupsAcceptsInlineCreateBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.CasbinRule{}, &model.AuthzRole{}, &model.RoutingChannelBinding{},
	))
	require.NoError(t, db.Create(&model.User{
		Id: 1, Username: "routing-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&model.Channel{Id: 991, Name: "inline-cost-source", Key: "channel-key"}).Error)
	require.NoError(t, authz.Init(db))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer upstream-token", r.Header.Get("Authorization"))
		assert.Equal(t, "42", r.Header.Get("New-Api-User"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"quota_per_unit":%g}}`, common.QuotaPerUnit)
		case "/api/user/self":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"quota":1000000,"used_quota":0}}`)
		case "/api/user/self/groups":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"vip":{"ratio":1}}}`)
		case "/api/user/models":
			_, _ = fmt.Fprint(w, `{"success":true,"data":["gpt-test"]}`)
		case "/api/pricing":
			_, _ = fmt.Fprint(w, `{"success":true,"data":[{"model_name":"gpt-test","quota_type":0,"model_ratio":1,"completion_ratio":1,"enable_groups":["vip"]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	body, err := common.Marshal(routingBindingRequest{
		ChannelID:     991,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		NewAPIUserID:  common.GetPointer(42),
		Credentials: routingCredentialRequest{
			NewAPIAccessToken: common.GetPointer("upstream-token"),
		},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "new"}}
	ctx.Set("id", 1)
	ctx.Set("role", common.RoleRootUser)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/smart-routing/bindings/new/groups", bytes.NewReader(body))

	LoadSmartRoutingBindingGroups(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			ChannelID int      `json:"channel_id"`
			Groups    []string `json:"groups"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	assert.Equal(t, 991, response.Data.ChannelID)
	assert.Contains(t, response.Data.Groups, "vip")
}

func TestRoutingCostAPIErrorsDoNotExposeUpstreamSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     992,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       "https://routing.example.com",
		UpstreamGroup: "vip",
		NewAPIUserID:  common.GetPointer(42),
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{
		NewAPIAccessToken: "sk-secret",
		GatewayAPIKey:     "gw-secret",
		Sub2APIPassword:   "pw-secret",
	}))
	require.NoError(t, db.Create(&binding).Error)
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("Authorization: Bearer sk-secret\r\nX-Api-Key: gw-secret\r\nCookie: session=cookie-secret\r\npassword=pw-secret https://api.example.com/path?token=query-secret")
	}))

	tests := []struct {
		name    string
		handler func(*gin.Context)
	}{
		{name: "test binding", handler: TestSmartRoutingBinding},
		{name: "load groups", handler: LoadSmartRoutingBindingGroups},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = gin.Params{{Key: "channelId", Value: "992"}}
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/smart-routing/bindings/992", nil)

			tt.handler(ctx)

			body := recorder.Body.String()
			for _, secret := range []string{"sk-secret", "gw-secret", "pw-secret", "cookie-secret", "query-secret", "api.example.com"} {
				assert.NotContains(t, body, secret)
			}
			assert.NotContains(t, body, `\r`)
			assert.NotContains(t, body, `\n`)
			assert.Contains(t, body, "***")
		})
	}
}

func TestRoutingSafeErrorPreservesCancellationAndAuthClassification(t *testing.T) {
	authErr := routingAuthErrorf("Bearer sk-secret")
	safeAuthErr := routingSafeErrorWithCredentials(authErr, model.RoutingCredentials{NewAPIAccessToken: "sk-secret"})
	assert.True(t, routingUpstreamAuthError(safeAuthErr))
	assert.NotContains(t, safeAuthErr.Error(), "sk-secret")
	safeCAErr := routingSafeErrorWithCredentials(
		errors.New("certificate parse failed for custom-ca-secret"),
		model.RoutingCredentials{CustomCAPEM: "custom-ca-secret"},
	)
	assert.NotContains(t, safeCAErr.Error(), "custom-ca-secret")

	safeCanceled := routingSafeErrorWithCredentials(context.Canceled, model.RoutingCredentials{})
	assert.ErrorIs(t, safeCanceled, context.Canceled)
}

func TestLoadSmartRoutingBindingGroupsRequiresSensitiveWriteForInlineCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body, err := common.Marshal(routingBindingRequest{
		ChannelID:     991,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       "https://upstream.example.com",
		UpstreamGroup: "vip",
		Credentials: routingCredentialRequest{
			NewAPIAccessToken: common.GetPointer("upstream-token"),
		},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "new"}}
	ctx.Set("id", 2)
	ctx.Set("role", common.RoleAdminUser)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/smart-routing/bindings/new/groups", bytes.NewReader(body))

	LoadSmartRoutingBindingGroups(ctx)

	assert.NotEqual(t, http.StatusOK, recorder.Code)
}

func TestRoutingBindingForActionRejectsInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "new"}}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/smart-routing/bindings/new/groups", strings.NewReader(`{`))

	_, ok := routingBindingForAction(ctx)

	assert.False(t, ok)
	assert.NotEqual(t, http.StatusOK, recorder.Code)
}

func TestDeleteSmartRoutingBindingPreservesServingState(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingBreakerState{}, &model.RoutingChannelMetric{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	routingmetrics.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		routingmetrics.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	require.NoError(t, db.Create(&model.Channel{Id: 66, Name: "serving", Key: "serving-key"}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelBinding{ChannelID: 66, UpstreamType: model.RoutingUpstreamTypeNewAPI, BaseURL: "https://upstream.example.com", UpstreamGroup: "vip", Enabled: true}).Error)
	require.NoError(t, db.Create(&model.RoutingCostSnapshot{ChannelID: 66, ModelName: "gpt-test"}).Error)
	require.NoError(t, db.Create(&model.RoutingBreakerState{
		ChannelID:       66,
		APIKeyIndex:     model.RoutingMetricSingleKeyIndex,
		ModelName:       "gpt-test",
		Group:           "vip",
		State:           model.RoutingBreakerStateOpen,
		SemanticVersion: model.RoutingBreakerSemanticVersion,
	}).Error)
	bucketTs := common.GetTimestamp() / 60 * 60
	require.NoError(t, db.Create(&model.RoutingChannelMetric{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "vip", BucketTs: bucketTs, RequestCount: 1}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelHealthState{
		ChannelID: 66, AuthFailure: true, BalanceKnown: true, Balance: 1,
		BalanceUpdatedTime: common.GetTimestamp(), UpdatedTime: common.GetTimestamp(),
	}).Error)
	routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}, routinghotcache.BreakerSnapshot{State: model.RoutingBreakerStateOpen})
	routinghotcache.SetCostForTest(routinghotcache.CostKey{ChannelID: 66, Model: "gpt-test"}, routinghotcache.CostSnapshot{Known: true, Cost: 1})
	routinghotcache.SetMetricForTest(routinghotcache.Key{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}, routinghotcache.MetricSnapshot{RequestCount: 1})
	routinghotcache.SetAuthFailureForTest(66, routinghotcache.HealthMarker{Marked: true})
	routinghotcache.SetBalanceForTest(66, routinghotcache.BalanceSnapshot{Known: true, Balance: 1})
	routingbreaker.RecordReliabilityFailure(
		routingbreaker.Key{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"},
		routingbreaker.FailureProvider5xx,
	)
	statsBeforeClear := routingbreaker.RuntimeStats()
	require.Equal(t, routingbreaker.Stats{Entries: 1, Dirty: 1}, statsBeforeClear)
	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{{
		ChannelID:    66,
		APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
		ModelName:    "gpt-test",
		Group:        "vip",
		BucketTs:     bucketTs,
		RequestCount: 1,
	}})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "66"}}
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/smart-routing/bindings/66", nil)
	setSmartRoutingBindingIfMatchForTest(t, ctx, 66)

	DeleteSmartRoutingBinding(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	for _, table := range []any{&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}} {
		var count int64
		require.NoError(t, db.Model(table).Where("channel_id = ?", 66).Count(&count).Error)
		assert.Zero(t, count)
	}
	for _, table := range []any{&model.RoutingBreakerState{}, &model.RoutingChannelMetric{}, &model.RoutingChannelHealthState{}} {
		var count int64
		require.NoError(t, db.Model(table).Where("channel_id = ?", 66).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	}
	var health model.RoutingChannelHealthState
	require.NoError(t, db.Where("channel_id = ?", 66).First(&health).Error)
	assert.True(t, health.AuthFailure)
	assert.False(t, health.BalanceKnown)
	_, breakerOK := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	_, costOK := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: 66, Model: "gpt-test"})
	_, metricOK := routinghotcache.GetMetric(routinghotcache.Key{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	_, authOK := routinghotcache.GetAuthFailure(66)
	_, balanceOK := routinghotcache.GetBalance(66)
	assert.True(t, breakerOK)
	assert.False(t, costOK)
	assert.True(t, metricOK)
	assert.True(t, authOK)
	assert.False(t, balanceOK)
	statsAfterClear := routingbreaker.RuntimeStats()
	assert.Equal(t, statsBeforeClear.Entries, statsAfterClear.Entries)
	assert.Equal(t, statsBeforeClear.Dirty, statsAfterClear.Dirty)
	assert.Equal(t, statsBeforeClear.Evictions, statsAfterClear.Evictions)
	_, err := flushRoutingRuntimeState(context.Background(), smart_routing_setting.GetSetting())
	require.NoError(t, err)
	var breakerCount int64
	require.NoError(t, db.Model(&model.RoutingBreakerState{}).Where("channel_id = ?", 66).Count(&breakerCount).Error)
	assert.Equal(t, int64(1), breakerCount)
	var metricCount int64
	require.NoError(t, db.Model(&model.RoutingChannelMetric{}).Where("channel_id = ?", 66).Count(&metricCount).Error)
	assert.Equal(t, int64(1), metricCount)
	var metric model.RoutingChannelMetric
	require.NoError(t, db.Where("channel_id = ?", 66).First(&metric).Error)
	assert.Equal(t, int64(2), metric.RequestCount)
}

func TestDeleteSmartRoutingBindingCanceledRequestDoesNotMutateState(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelMetric{},
		&model.RoutingChannelHealthState{},
	))
	require.NoError(t, db.Create(&model.RoutingChannelBinding{
		ChannelID:     67,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       "https://upstream.example.com",
		UpstreamGroup: "vip",
		Enabled:       true,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingCostSnapshot{ChannelID: 67, ModelName: "gpt-test"}).Error)

	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "67"}}
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/smart-routing/bindings/67", nil).WithContext(requestContext)
	setSmartRoutingBindingIfMatchForTest(t, ctx, 67)

	DeleteSmartRoutingBinding(ctx)

	assert.Contains(t, recorder.Body.String(), `"success":false`)
	var bindingCount int64
	require.NoError(t, db.Model(&model.RoutingChannelBinding{}).Where("channel_id = ?", 67).Count(&bindingCount).Error)
	assert.Equal(t, int64(1), bindingCount)
	var snapshotCount int64
	require.NoError(t, db.Model(&model.RoutingCostSnapshot{}).Where("channel_id = ?", 67).Count(&snapshotCount).Error)
	assert.Equal(t, int64(1), snapshotCount)
}

func TestResetSmartRoutingBreakerClearsStoredAndHotcacheState(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingBreakerState{}, &model.RoutingOperation{}, &model.RoutingBreakerResetCommand{},
		&model.RoutingBreakerResetTombstone{}, &model.RoutingBreakerResetOutbox{},
		&model.RoutingEndpointEvidence{}, &model.RoutingEndpointSharedState{},
		&model.RoutingPolicyHead{}, &model.RoutingPolicyPoolRevision{}, &model.RoutingPolicyMemberRevision{},
	))
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	channelrouting.ResetSnapshotForTest()
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
		channelrouting.ResetSnapshotForTest()
	})

	state := model.RoutingBreakerState{
		ChannelID:           41,
		APIKeyIndex:         model.RoutingMetricSingleKeyIndex,
		ModelName:           "gpt-test",
		Group:               "default",
		State:               model.RoutingBreakerStateOpen,
		Reason:              "5xx",
		SemanticVersion:     model.RoutingBreakerSemanticVersion,
		ConsecutiveFailures: 5,
		EjectionCount:       1,
	}
	require.NoError(t, db.Create(&state).Error)
	channelrouting.SetSnapshotForTest(channelrouting.SnapshotView{
		Revision: 1, ActivationID: 1,
		Pools: []channelrouting.PoolSnapshot{{
			ID: 11, GroupName: state.Group, Members: []channelrouting.PoolMemberSnapshot{{
				ID: 22, PoolID: 11, ChannelID: state.ChannelID,
				Models: []channelrouting.ModelSnapshot{{ModelName: state.ModelName}},
			}},
		}},
	})
	seedChannelRoutingBreakerPolicy(t, db, 11, 22, state.ChannelID, state.Group, 1, 1)
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key: routingbreaker.Key{
			ChannelID:   state.ChannelID,
			APIKeyIndex: state.APIKeyIndex,
			Model:       state.ModelName,
			Group:       state.Group,
		},
		State:     routingbreaker.StateOpen,
		Reason:    "5xx",
		UpdatedAt: time.Now(),
	}})
	routinghotcache.SetBreakerForTest(routinghotcache.Key{
		ChannelID:   state.ChannelID,
		APIKeyIndex: state.APIKeyIndex,
		Model:       state.ModelName,
		Group:       state.Group,
	}, routinghotcache.BreakerSnapshot{State: model.RoutingBreakerStateOpen, Reason: "5xx"})

	router := gin.New()
	router.POST("/breakers/:id/reset", ResetSmartRoutingBreaker)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/breakers/1/reset", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusAccepted, recorder.Code)
	firstIdempotencyKey := recorder.Header().Get("Idempotency-Key")
	assert.Contains(t, firstIdempotencyKey, "legacy-breaker-reset-1-0-")
	replayRecorder := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "/breakers/1/reset", nil)
	router.ServeHTTP(replayRecorder, replayRequest)
	require.Equal(t, http.StatusOK, replayRecorder.Code)
	assert.Equal(t, firstIdempotencyKey, replayRecorder.Header().Get("Idempotency-Key"))
	var operationCount int64
	require.NoError(t, db.Model(&model.RoutingOperation{}).Count(&operationCount).Error)
	assert.Equal(t, int64(1), operationCount)
	require.NoError(t, channelrouting.RunBreakerResetControlCycleContext(context.Background()))
	var count int64
	breakerQuery := db.Model(&model.RoutingBreakerState{}).Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND `group` = ?", state.ChannelID, state.APIKeyIndex, state.ModelName, state.Group)
	require.NoError(t, breakerQuery.Count(&count).Error)
	assert.Zero(t, count)
	resetSnapshot, ok := routinghotcache.GetBreaker(routinghotcache.Key{
		ChannelID:   state.ChannelID,
		APIKeyIndex: state.APIKeyIndex,
		Model:       state.ModelName,
		Group:       state.Group,
	})
	require.True(t, ok)
	assert.Equal(t, model.RoutingBreakerStateHealthy, resetSnapshot.State)
	assert.Empty(t, routingbreaker.DirtySnapshots())
	require.NoError(t, breakerQuery.Count(&count).Error)
	assert.Zero(t, count)

	completedReplay := httptest.NewRecorder()
	router.ServeHTTP(completedReplay, httptest.NewRequest(http.MethodPost, "/breakers/1/reset", nil))
	require.Equal(t, http.StatusOK, completedReplay.Code)
	assert.Equal(t, firstIdempotencyKey, completedReplay.Header().Get("Idempotency-Key"))
	assert.Contains(t, completedReplay.Body.String(), `"status":"succeeded"`)

	reusedState := model.RoutingBreakerState{
		ChannelID: state.ChannelID, APIKeyIndex: state.APIKeyIndex,
		ModelName: state.ModelName, Group: state.Group, ResetGeneration: 1,
		State: model.RoutingBreakerStateOpen, Reason: "5xx",
		SemanticVersion: model.RoutingBreakerSemanticVersion, ConsecutiveFailures: 1,
	}
	require.NoError(t, db.Create(&reusedState).Error)
	require.Equal(t, state.ID, reusedState.ID, "SQLite may reuse the deleted highest row ID")
	reusedRecorder := httptest.NewRecorder()
	router.ServeHTTP(reusedRecorder, httptest.NewRequest(http.MethodPost, "/breakers/1/reset", nil))
	require.Equal(t, http.StatusAccepted, reusedRecorder.Code, reusedRecorder.Body.String())
	reusedIdempotencyKey := reusedRecorder.Header().Get("Idempotency-Key")
	assert.Contains(t, reusedIdempotencyKey, "legacy-breaker-reset-1-1-")
	assert.NotEqual(t, firstIdempotencyKey, reusedIdempotencyKey)
	require.NoError(t, db.Model(&model.RoutingOperation{}).Count(&operationCount).Error)
	assert.Equal(t, int64(2), operationCount)
	require.NoError(t, channelrouting.RunBreakerResetControlCycleContext(context.Background()))
	require.NoError(t, breakerQuery.Count(&count).Error)
	assert.Zero(t, count)
}
