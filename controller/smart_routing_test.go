package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRoutingBindingViewMasksCredentials(t *testing.T) {
	token := "sk-1234567890abcdef"
	binding := model.RoutingChannelBinding{
		ID:             10,
		ChannelID:      20,
		UpstreamType:   model.RoutingUpstreamTypeNewAPI,
		BaseURL:        "https://upstream.example.com",
		UpstreamGroup:  "vip",
		EncCredentials: &token,
		Enabled:        true,
	}

	view := buildRoutingBindingView(binding, model.RoutingCredentials{
		NewAPIAccessToken: token,
		GatewayAPIKey:     "gw-secret-987654",
		Sub2APIPassword:   "password-secret",
	})

	require.Equal(t, binding.ChannelID, view.ChannelID)
	assert.Equal(t, "****cdef", view.CredentialMasks.NewAPIAccessToken)
	assert.Equal(t, "****7654", view.CredentialMasks.GatewayAPIKey)
	assert.Equal(t, "********", view.CredentialMasks.Sub2APIPassword)
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

func TestRoutingBindingFromRequestClearsUpstreamSpecificFields(t *testing.T) {
	userID := 42
	newAPIBinding := routingBindingFromRequest(routingBindingRequest{
		ChannelID:        11,
		UpstreamType:     model.RoutingUpstreamTypeNewAPI,
		BaseURL:          "https://newapi.example.com",
		UpstreamGroup:    "vip",
		ServesClaudeCode: true,
		NewAPIUserID:     &userID,
	})
	assert.False(t, newAPIBinding.ServesClaudeCode)
	require.NotNil(t, newAPIBinding.NewAPIUserID)

	sub2apiBinding := routingBindingFromRequest(routingBindingRequest{
		ChannelID:        12,
		UpstreamType:     model.RoutingUpstreamTypeSub2API,
		BaseURL:          "https://sub2api.example.com",
		UpstreamGroup:    "vip",
		ServesClaudeCode: true,
		NewAPIUserID:     &userID,
	})
	assert.True(t, sub2apiBinding.ServesClaudeCode)
	assert.Nil(t, sub2apiBinding.NewAPIUserID)
}

func TestValidateRoutingBindingRequestRejectsUnsafeBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "userinfo", baseURL: "https://token@example.com"},
		{name: "sensitive query", baseURL: "https://example.com?access_token=secret"},
		{name: "unsupported scheme", baseURL: "file:///tmp/socket"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRoutingBindingRequest(routingBindingRequest{
				ChannelID:     1,
				UpstreamType:  model.RoutingUpstreamTypeNewAPI,
				BaseURL:       tt.baseURL,
				UpstreamGroup: "vip",
			}, true)

			require.Error(t, err)
		})
	}
}

func TestUpdateSmartRoutingBindingMergesCredentialFields(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}))
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     55,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       "https://upstream.example.com",
		UpstreamGroup: "vip",
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
		"credentials":{"gateway_api_key":"new-gateway-key"}
	}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "55"}}
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/smart-routing/bindings/55", bytes.NewReader(body))

	UpdateSmartRoutingBinding(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", 55).First(&updated).Error)
	credentials, err := updated.GetCredentials()
	require.NoError(t, err)
	assert.Equal(t, "old-access-token", credentials.NewAPIAccessToken)
	assert.Equal(t, "new-gateway-key", credentials.GatewayAPIKey)
	assert.Equal(t, "old-password", credentials.Sub2APIPassword)
}

func TestLoadSmartRoutingBindingGroupsAcceptsInlineCreateBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer upstream-token", r.Header.Get("Authorization"))
		assert.Equal(t, "42", r.Header.Get("New-Api-User"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"quota":1000000,"used_quota":0}}`)
		case "/api/pricing":
			_, _ = fmt.Fprint(w, `{"success":true,"data":[{"model_name":"gpt-test","enable_groups":["vip"]}],"group_ratio":{"vip":1}}`)
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

func TestDeleteSmartRoutingBindingCleansAssociatedState(t *testing.T) {
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

	require.NoError(t, db.Create(&model.RoutingChannelBinding{ChannelID: 66, UpstreamType: model.RoutingUpstreamTypeNewAPI, BaseURL: "https://upstream.example.com", UpstreamGroup: "vip", Enabled: true}).Error)
	require.NoError(t, db.Create(&model.RoutingCostSnapshot{ChannelID: 66, ModelName: "gpt-test"}).Error)
	require.NoError(t, db.Create(&model.RoutingBreakerState{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "vip", State: model.RoutingBreakerStateOpen}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelMetric{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "vip", BucketTs: 60, RequestCount: 1}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelHealthState{ChannelID: 66, AuthFailure: true, UpdatedTime: common.GetTimestamp()}).Error)
	routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}, routinghotcache.BreakerSnapshot{State: model.RoutingBreakerStateOpen})
	routinghotcache.SetCostForTest(routinghotcache.CostKey{ChannelID: 66, Model: "gpt-test"}, routinghotcache.CostSnapshot{Known: true, Cost: 1})
	routinghotcache.SetMetricForTest(routinghotcache.Key{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}, routinghotcache.MetricSnapshot{RequestCount: 1})
	routinghotcache.SetAuthFailureForTest(66, routinghotcache.HealthMarker{Marked: true})
	routinghotcache.SetBalanceForTest(66, routinghotcache.BalanceSnapshot{Known: true, Balance: 1})
	routingbreaker.RecordAttempt(routingbreaker.Key{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}, false, http.StatusBadGateway, 0)
	statsBeforeClear := routingbreaker.RuntimeStats()
	require.Equal(t, routingbreaker.Stats{Entries: 1, Dirty: 1}, statsBeforeClear)
	routingmetrics.RequeueSnapshots([]model.RoutingChannelMetric{{
		ChannelID:    66,
		APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
		ModelName:    "gpt-test",
		Group:        "vip",
		BucketTs:     120,
		RequestCount: 1,
	}})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "channelId", Value: "66"}}
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/smart-routing/bindings/66", nil)

	DeleteSmartRoutingBinding(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	for _, table := range []any{&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingBreakerState{}, &model.RoutingChannelMetric{}, &model.RoutingChannelHealthState{}} {
		var count int64
		require.NoError(t, db.Model(table).Where("channel_id = ?", 66).Count(&count).Error)
		assert.Zero(t, count)
	}
	_, breakerOK := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	_, costOK := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: 66, Model: "gpt-test"})
	_, metricOK := routinghotcache.GetMetric(routinghotcache.Key{ChannelID: 66, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	_, authOK := routinghotcache.GetAuthFailure(66)
	_, balanceOK := routinghotcache.GetBalance(66)
	assert.False(t, breakerOK)
	assert.False(t, costOK)
	assert.False(t, metricOK)
	assert.False(t, authOK)
	assert.False(t, balanceOK)
	statsAfterClear := routingbreaker.RuntimeStats()
	assert.Zero(t, statsAfterClear.Entries)
	assert.Zero(t, statsAfterClear.Dirty)
	assert.Equal(t, statsBeforeClear.Evictions, statsAfterClear.Evictions)
	_, err := flushRoutingRuntimeState(smart_routing_setting.GetSetting())
	require.NoError(t, err)
	for _, table := range []any{&model.RoutingBreakerState{}, &model.RoutingChannelMetric{}} {
		var count int64
		require.NoError(t, db.Model(table).Where("channel_id = ?", 66).Count(&count).Error)
		assert.Zero(t, count)
	}
}

func TestResetSmartRoutingBreakerClearsStoredAndHotcacheState(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingBreakerState{}))
	routinghotcache.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	state := model.RoutingBreakerState{
		ChannelID:           41,
		APIKeyIndex:         model.RoutingMetricSingleKeyIndex,
		ModelName:           "gpt-test",
		Group:               "default",
		State:               model.RoutingBreakerStateOpen,
		Reason:              "5xx",
		ConsecutiveFailures: 5,
		EjectionCount:       1,
	}
	require.NoError(t, db.Create(&state).Error)
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

	require.Equal(t, http.StatusOK, recorder.Code)
	var count int64
	breakerQuery := db.Model(&model.RoutingBreakerState{}).Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND `group` = ?", state.ChannelID, state.APIKeyIndex, state.ModelName, state.Group)
	require.NoError(t, breakerQuery.Count(&count).Error)
	assert.Zero(t, count)
	_, ok := routinghotcache.GetBreaker(routinghotcache.Key{
		ChannelID:   state.ChannelID,
		APIKeyIndex: state.APIKeyIndex,
		Model:       state.ModelName,
		Group:       state.Group,
	})
	assert.False(t, ok)
	_, err := flushRoutingRuntimeState(smart_routing_setting.GetSetting())
	require.NoError(t, err)
	require.NoError(t, breakerQuery.Count(&count).Error)
	assert.Zero(t, count)
}
