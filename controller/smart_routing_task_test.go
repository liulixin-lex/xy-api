package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoutingCostSyncHandlerUsesSmartRoutingSetting(t *testing.T) {
	smart_routing_setting.ResetForTest()
	handler := routingCostSyncHandler{}

	assert.Equal(t, model.SystemTaskTypeRoutingCostSync, handler.Type())
	assert.False(t, handler.Enabled())

	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:         true,
		Mode:            smart_routing_setting.ModeObserve,
		SyncIntervalMin: 7,
	})
	t.Cleanup(smart_routing_setting.ResetForTest)

	assert.True(t, handler.Enabled())
	assert.Equal(t, 7*time.Minute, handler.Interval())
}

func TestRoutingAgentHandlerRequiresAgentEnabled(t *testing.T) {
	smart_routing_setting.ResetForTest()
	handler := routingAgentHandler{}

	assert.Equal(t, model.SystemTaskTypeRoutingAgent, handler.Type())
	assert.False(t, handler.Enabled())

	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:         true,
		Mode:            smart_routing_setting.ModeObserve,
		SyncIntervalMin: 5,
		AgentEnabled:    true,
	})
	t.Cleanup(smart_routing_setting.ResetForTest)

	assert.True(t, handler.Enabled())
	assert.Equal(t, time.Hour, handler.Interval())
}

func TestRunRoutingCostSyncTaskFetchesNewAPIPricingSnapshots(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		assert.Equal(t, "Bearer upstream-token", r.Header.Get("Authorization"))
		assert.Equal(t, "42", r.Header.Get("New-Api-User"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			_, _ = fmt.Fprint(w, `{
				"success": true,
				"data": {"quota": 1000000, "used_quota": 250000}
			}`)
		case "/api/pricing":
			_, _ = fmt.Fprint(w, `{
			"success": true,
			"data": [
				{"model_name":"gpt-test","quota_type":0,"model_ratio":2,"completion_ratio":3,"enable_groups":["vip"]},
				{"model_name":"image-test","quota_type":1,"model_price":0.25,"enable_groups":["all"]}
			],
			"group_ratio": {"vip": 1.5},
			"pricing_version": "version-a"
		}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     777,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		NewAPIUserID:  common.GetPointer(42),
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{NewAPIAccessToken: "upstream-token"}))
	require.NoError(t, db.Create(&binding).Error)

	summary, err := runRoutingCostSyncTask(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, requests["/api/user/self"])
	assert.Equal(t, 1, requests["/api/pricing"])
	assert.EqualValues(t, 1, summary["bindings"])
	assert.EqualValues(t, 2, summary["snapshots"])

	var snapshots []model.RoutingCostSnapshot
	require.NoError(t, db.Order("model_name asc").Find(&snapshots).Error)
	require.Len(t, snapshots, 2)
	assert.Equal(t, "gpt-test", snapshots[0].ModelName)
	assert.Equal(t, 1.5, snapshots[0].GroupRatio)
	assert.Equal(t, 2.0, snapshots[0].BaseRatio)
	assert.Equal(t, 3.0, snapshots[0].CompletionRatio)
	assert.Equal(t, "version-a", snapshots[0].PricingVersion)
	assert.Equal(t, "image-test", snapshots[1].ModelName)
	assert.Equal(t, 0.25, snapshots[1].ModelPrice)

	cost, ok := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: 777, Model: "gpt-test"})
	require.True(t, ok)
	assert.True(t, cost.Known)
	assert.Equal(t, 3.0, cost.Cost)

	balance, ok := routinghotcache.GetBalance(777)
	require.True(t, ok)
	assert.True(t, balance.Known)
	assert.Equal(t, 1.5, balance.Balance)
}

func TestRunRoutingCostSyncTaskMarksAuthFailureOnUnauthorizedUpstream(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"success":false,"message":"invalid token"}`)
	}))
	t.Cleanup(server.Close)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     778,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		NewAPIUserID:  common.GetPointer(42),
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{NewAPIAccessToken: "secret-token"}))
	require.NoError(t, db.Create(&binding).Error)

	summary, err := runRoutingCostSyncTask(context.Background())

	require.NoError(t, err)
	assert.EqualValues(t, 1, summary["bindings"])
	assert.EqualValues(t, 1, summary["errors"])
	authFailure, ok := routinghotcache.GetAuthFailure(778)
	require.True(t, ok)
	assert.True(t, authFailure.Marked)

	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", 778).First(&updated).Error)
	require.NotNil(t, updated.LastSyncError)
	assert.NotContains(t, *updated.LastSyncError, "secret-token")
}

func TestRunRoutingCostSyncTaskSkipsBindingsStillInBackoff(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{
		ChannelID:        780,
		UpstreamType:     model.RoutingUpstreamTypeNewAPI,
		BaseURL:          server.URL,
		UpstreamGroup:    "vip",
		NewAPIUserID:     common.GetPointer(42),
		Enabled:          true,
		SyncBackoffUntil: common.GetTimestamp() + 600,
	}
	require.NoError(t, db.Create(&binding).Error)

	summary, err := runRoutingCostSyncTask(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 0, requestCount)
	assert.EqualValues(t, 0, summary["bindings"])
	assert.EqualValues(t, 1, summary["skipped_backoff"])
}

func TestRunRoutingCostSyncTaskLoadsPersistedBreakerStatesIntoHotcache(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	require.NoError(t, db.Create(&model.RoutingBreakerState{
		ChannelID:     781,
		APIKeyIndex:   model.RoutingMetricSingleKeyIndex,
		ModelName:     "gpt-test",
		Group:         "vip",
		State:         model.RoutingBreakerStateOpen,
		Reason:        "5xx",
		CooldownUntil: common.GetTimestamp() + 60,
		UpdatedTime:   common.GetTimestamp(),
	}).Error)

	summary, err := runRoutingCostSyncTask(context.Background())

	require.NoError(t, err)
	assert.EqualValues(t, 1, summary["loaded_breakers"])
	cached, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 781, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, model.RoutingBreakerStateOpen, cached.State)
	assert.Equal(t, "5xx", cached.Reason)
}

func TestRefreshRoutingHotcacheFromDBLoadsRoutingSnapshots(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	now := common.GetTimestamp()

	require.NoError(t, db.Create(&model.RoutingCostSnapshot{
		ChannelID:  782,
		ModelName:  "gpt-test",
		GroupRatio: 2,
		BaseRatio:  3,
		Confidence: model.RoutingCostConfidenceFull,
		SnapshotTS: now,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelMetric{
		ChannelID:    782,
		APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
		ModelName:    "gpt-test",
		Group:        "vip",
		BucketTs:     now,
		RequestCount: 10,
		SuccessCount: 9,
		LatencyP95Ms: 250,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingBreakerState{
		ChannelID:   782,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		ModelName:   "gpt-test",
		Group:       "vip",
		State:       model.RoutingBreakerStateDegraded,
		UpdatedTime: now,
	}).Error)

	summary, err := refreshRoutingHotcacheFromDB()

	require.NoError(t, err)
	assert.EqualValues(t, 1, summary["costs"])
	assert.EqualValues(t, 1, summary["metrics"])
	assert.EqualValues(t, 1, summary["breakers"])
	metric, ok := routinghotcache.GetMetric(routinghotcache.Key{ChannelID: 782, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, 250.0, metric.P95LatencyMs)
	cost, ok := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: 782, Model: "gpt-test"})
	require.True(t, ok)
	assert.Equal(t, 6.0, cost.Cost)
	breaker, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 782, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, model.RoutingBreakerStateDegraded, breaker.State)
}

func TestRefreshRoutingHotcacheFromDBPrefersLatestRowsUnderLimit(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	now := common.GetTimestamp()

	costs := make([]model.RoutingCostSnapshot, 0, 5001)
	metrics := make([]model.RoutingChannelMetric, 0, 5001)
	for i := 0; i < 5000; i++ {
		channelID := 10_000 + i
		costs = append(costs, model.RoutingCostSnapshot{
			ChannelID:  channelID,
			ModelName:  "old-cost",
			GroupRatio: 9,
			BaseRatio:  1,
			Confidence: model.RoutingCostConfidenceFull,
			SnapshotTS: now - 10,
		})
		metrics = append(metrics, model.RoutingChannelMetric{
			ChannelID:    channelID,
			APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
			ModelName:    "old-metric",
			Group:        "vip",
			BucketTs:     now - 10,
			RequestCount: 1,
			SuccessCount: 1,
			LatencyP95Ms: 900,
		})
	}
	costs = append(costs, model.RoutingCostSnapshot{
		ChannelID:  99_999,
		ModelName:  "latest-cost",
		GroupRatio: 2,
		BaseRatio:  1,
		Confidence: model.RoutingCostConfidenceFull,
		SnapshotTS: now,
	})
	metrics = append(metrics, model.RoutingChannelMetric{
		ChannelID:    99_999,
		APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
		ModelName:    "latest-metric",
		Group:        "vip",
		BucketTs:     now,
		RequestCount: 10,
		SuccessCount: 10,
		LatencyP95Ms: 100,
	})
	require.NoError(t, db.CreateInBatches(costs, 500).Error)
	require.NoError(t, db.CreateInBatches(metrics, 500).Error)

	summary, err := refreshRoutingHotcacheFromDB()

	require.NoError(t, err)
	assert.EqualValues(t, 5000, summary["costs"])
	assert.EqualValues(t, 5000, summary["metrics"])
	cost, ok := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: 99_999, Model: "latest-cost"})
	require.True(t, ok)
	assert.Equal(t, 2.0, cost.Cost)
	metric, ok := routinghotcache.GetMetric(routinghotcache.Key{ChannelID: 99_999, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "latest-metric", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, 100.0, metric.P95LatencyMs)
}

func TestRunRoutingCostSyncTaskMasksNewAPISuccessFalseMessage(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"success":false,"message":"bad token secret-token"}`)
	}))
	t.Cleanup(server.Close)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     779,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		NewAPIUserID:  common.GetPointer(42),
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{NewAPIAccessToken: "secret-token"}))
	require.NoError(t, db.Create(&binding).Error)

	summary, err := runRoutingCostSyncTask(context.Background())

	require.NoError(t, err)
	assert.EqualValues(t, 1, summary["errors"])
	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", 779).First(&updated).Error)
	require.NotNil(t, updated.LastSyncError)
	assert.NotContains(t, *updated.LastSyncError, "secret-token")
	assert.Contains(t, *updated.LastSyncError, "***")
}

func TestFetchRoutingCostSnapshotsMapsUpstreamModelNameToLocalName(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"quota":1000000,"used_quota":0}}`)
		case "/api/pricing":
			_, _ = fmt.Fprint(w, `{
				"success": true,
				"data": [{"model_name":"upstream-a","quota_type":0,"model_ratio":1,"completion_ratio":1,"enable_groups":["vip"]}],
				"group_ratio": {"vip": 1}
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	mapping := `{"local-b":"upstream-a","local-a":"upstream-a"}`
	require.NoError(t, db.Create(&model.Channel{Id: 779, Name: "mapped", ModelMapping: &mapping}).Error)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     779,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		NewAPIUserID:  common.GetPointer(42),
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{NewAPIAccessToken: "upstream-token"}))

	snapshots, err := fetchRoutingCostSnapshots(context.Background(), binding)

	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.Equal(t, "local-a", snapshots[0].ModelName)
}

func TestFetchRoutingCostSnapshotsPreservesTieredExprAsUnknownCost(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"quota":1000000,"used_quota":0}}`)
		case "/api/pricing":
			_, _ = fmt.Fprint(w, `{
				"success": true,
				"data": [{
					"model_name":"tiered-test",
					"quota_type":0,
					"model_ratio":2,
					"completion_ratio":3,
					"billing_mode":"tiered_expr",
					"billing_expr":"tier(\"base\", p * 2.5 + c * 15)",
					"enable_groups":["vip"]
				}],
				"group_ratio": {"vip": 1.2}
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     782,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		NewAPIUserID:  common.GetPointer(42),
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{NewAPIAccessToken: "upstream-token"}))

	snapshots, err := fetchRoutingCostSnapshots(context.Background(), binding)

	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.Equal(t, "tiered-test", snapshots[0].ModelName)
	assert.Equal(t, "tiered_expr", snapshots[0].BillingMode)
	assert.Equal(t, model.RoutingCostConfidenceUnknown, snapshots[0].Confidence)
	require.NotNil(t, snapshots[0].TiersJSON)
	assert.Contains(t, *snapshots[0].TiersJSON, `"type":"expr"`)
	assert.Contains(t, *snapshots[0].TiersJSON, `tier(\"base\", p * 2.5 + c * 15)`)
}

func TestRunRoutingCostSyncTaskFetchesSub2APIPricingSnapshotsAndCachesEncryptedJWT(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	var mu sync.Mutex
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.URL.Path]++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			assert.Equal(t, http.MethodPost, r.Method)
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			assert.Contains(t, string(body), "admin@example.com")
			assert.Contains(t, string(body), "pw-secret")
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"token":"jwt-secret","expires_in":3600}}`)
		case "/api/v1/groups/available":
			assert.Equal(t, "Bearer jwt-secret", r.Header.Get("Authorization"))
			_, _ = fmt.Fprint(w, `{"code":0,"data":[{"id":"vip","rate_multiplier":1.2}]}`)
		case "/api/v1/groups/rates":
			assert.Equal(t, "Bearer jwt-secret", r.Header.Get("Authorization"))
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"vip":1.5}}`)
		case "/api/v1/channels/available":
			assert.Equal(t, "Bearer jwt-secret", r.Header.Get("Authorization"))
			_, _ = fmt.Fprint(w, `{"code":0,"data":[{"models":["claude-3"],"input_price":2,"output_price":6,"cache_price":0.4,"per_request_price":0.1,"billing_mode":"token"}]}`)
		case "/v1/usage":
			assert.Equal(t, "Bearer sk-gateway", r.Header.Get("Authorization"))
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"balance":9.25}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     880,
		UpstreamType:  model.RoutingUpstreamTypeSub2API,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "pw-secret",
		GatewayAPIKey:   "sk-gateway",
	}))
	require.NoError(t, db.Create(&binding).Error)

	for range 2 {
		summary, err := runRoutingCostSyncTask(context.Background())
		require.NoError(t, err)
		assert.EqualValues(t, 1, summary["bindings"])
		assert.EqualValues(t, 1, summary["snapshots"])
	}

	mu.Lock()
	loginRequests := requests["/api/v1/auth/login"]
	mu.Unlock()
	assert.Equal(t, 1, loginRequests)

	cached := routingSub2APICachedJWTForTest(880)
	require.NotEmpty(t, cached)
	assert.True(t, strings.HasPrefix(cached, "v1:"))
	assert.NotContains(t, cached, "jwt-secret")

	var snapshot model.RoutingCostSnapshot
	require.NoError(t, db.Where("channel_id = ? AND model_name = ?", 880, "claude-3").First(&snapshot).Error)
	assert.Equal(t, 1.5, snapshot.GroupRatio)
	assert.Equal(t, 2.0, snapshot.BaseRatio)
	assert.Equal(t, 3.0, snapshot.CompletionRatio)
	assert.Equal(t, "token", snapshot.BillingMode)
	assert.Equal(t, model.RoutingCostConfidenceFull, snapshot.Confidence)
	require.NotNil(t, snapshot.ExtrasJSON)
	assert.Contains(t, *snapshot.ExtrasJSON, "cache_price")
	assert.Contains(t, *snapshot.ExtrasJSON, "per_request_price")

	cost, ok := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: 880, Model: "claude-3"})
	require.True(t, ok)
	assert.True(t, cost.Known)
	assert.Equal(t, 3.0, cost.Cost)

	balance, ok := routinghotcache.GetBalance(880)
	require.True(t, ok)
	assert.True(t, balance.Known)
	assert.Equal(t, 9.25, balance.Balance)
}

func TestFetchRoutingCostSnapshotsSub2APILoginFailureMarksAuthAndMasksSecrets(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":401,"message":"login failed for pw-secret"}`)
	}))
	t.Cleanup(server.Close)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     881,
		UpstreamType:  model.RoutingUpstreamTypeSub2API,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "pw-secret",
	}))
	require.NoError(t, db.Create(&binding).Error)

	summary, err := runRoutingCostSyncTask(context.Background())

	require.NoError(t, err)
	assert.EqualValues(t, 1, summary["errors"])
	authFailure, ok := routinghotcache.GetAuthFailure(881)
	require.True(t, ok)
	assert.True(t, authFailure.Marked)

	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", 881).First(&updated).Error)
	require.NotNil(t, updated.LastSyncError)
	assert.NotContains(t, *updated.LastSyncError, "pw-secret")
}
