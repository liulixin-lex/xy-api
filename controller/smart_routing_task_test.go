package controller

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type blockingRoutingSub2APIEvalHook struct {
	started chan<- bool
}

func newRoutingCostTLSServerForTest(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	client := server.Client()
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	transport = transport.Clone()
	transport.DisableCompression = true
	serverAddress := server.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: time.Second}
		return dialer.DialContext(ctx, network, serverAddress)
	}
	client.Transport = transport
	client.Timeout = 5 * time.Second
	restoreRoutingCostHTTPDoerForTest(t, client)

	_, port, err := net.SplitHostPort(serverAddress)
	require.NoError(t, err)
	server.URL = "https://routing.example.com:" + port
	return server
}

func restoreRoutingCostHTTPDoerForTest(t *testing.T, replacement interface {
	Do(*http.Request) (*http.Response, error)
}) {
	t.Helper()
	previous := routingCostHTTPDoer
	routingCostHTTPDoer = replacement
	t.Cleanup(func() { routingCostHTTPDoer = previous })
}

func (h blockingRoutingSub2APIEvalHook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	if cmd.Name() != "eval" && cmd.Name() != "evalsha" {
		return ctx, nil
	}
	h.started <- ctx.Err() != nil
	<-ctx.Done()
	return ctx, ctx.Err()
}

func (blockingRoutingSub2APIEvalHook) AfterProcess(_ context.Context, cmd redis.Cmder) error {
	if cmd.Name() == "set" {
		if boolCmd, ok := cmd.(*redis.BoolCmd); ok {
			boolCmd.SetVal(true)
			boolCmd.SetErr(nil)
		}
	}
	return nil
}

func (blockingRoutingSub2APIEvalHook) BeforeProcessPipeline(ctx context.Context, _ []redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (blockingRoutingSub2APIEvalHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

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

func TestRoutingCostSyncExecutionStateReflectsAccountOutcome(t *testing.T) {
	tests := []struct {
		name    string
		summary map[string]any
		state   string
		failed  bool
	}{
		{
			name: "no bindings",
			summary: map[string]any{
				"bindings": 0, "accounts": 0, "successful_accounts": 0,
				"errors": 0, "partial_accounts": 0, "stale_bindings": 0,
			},
			state: model.RoutingCostSyncExecutionStateCompleted,
		},
		{
			name: "all accounts failed",
			summary: map[string]any{
				"bindings": 2, "accounts": 2, "successful_accounts": 0,
				"errors": 2, "partial_accounts": 0, "stale_bindings": 0,
			},
			state:  model.RoutingCostSyncExecutionStateFailed,
			failed: true,
		},
		{
			name: "partial success",
			summary: map[string]any{
				"bindings": 2, "accounts": 2, "successful_accounts": 1,
				"errors": 1, "partial_accounts": 1, "stale_bindings": 0,
			},
			state: model.RoutingCostSyncExecutionStatePartial,
		},
		{
			name: "all accounts succeeded",
			summary: map[string]any{
				"bindings": 2, "accounts": 2, "successful_accounts": 2,
				"errors": 0, "partial_accounts": 0, "stale_bindings": 0,
			},
			state: model.RoutingCostSyncExecutionStateCompleted,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := routingCostSyncExecutionState(test.summary)
			if test.failed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, test.state, state)
		})
	}
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
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	requests := map[string]int{}
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	require.NoError(t, model.UpsertRoutingChannelAuthFailure(777, true, "serving-auth-failure", common.GetTimestamp()+300))
	routinghotcache.SetAuthFailure(777, routinghotcache.HealthMarker{Marked: true, UpdatedUnix: common.GetTimestamp()})

	summary, err := runRoutingCostSyncTask(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, requests["/api/user/self"])
	assert.Equal(t, 1, requests["/api/pricing"])
	assert.EqualValues(t, 1, summary["bindings"])
	assert.EqualValues(t, 2, summary["snapshots"])
	assert.EqualValues(t, 1, summary["successful_accounts"])

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
	authFailure, ok := routinghotcache.GetAuthFailure(777)
	require.True(t, ok)
	assert.True(t, authFailure.Marked)
	var health model.RoutingChannelHealthState
	require.NoError(t, db.Where("channel_id = ?", 777).First(&health).Error)
	assert.True(t, health.AuthFailure)
	assert.Equal(t, "serving-auth-failure", health.AuthFailureReason)
}

func TestRunRoutingCostSyncTaskAggregatesNewAPIBindingsByUpstreamAccount(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelHealthState{},
	))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	requests := map[string]int{}
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota":2000000,"used_quota":500000}}`)
		case "/api/pricing":
			_, _ = io.WriteString(w, `{
				"success":true,
				"data":[{"model_name":"gpt-shared","quota_type":0,"model_ratio":2,"completion_ratio":3,"enable_groups":["all"]}],
				"group_ratio":{"basic":1,"vip":1.5},
				"pricing_version":"shared-v1"
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
	for _, binding := range []model.RoutingChannelBinding{
		{ChannelID: 790, UpstreamType: model.RoutingUpstreamTypeNewAPI, BaseURL: server.URL, UpstreamGroup: "basic", NewAPIUserID: common.GetPointer(42), Enabled: true},
		{ChannelID: 791, UpstreamType: model.RoutingUpstreamTypeNewAPI, BaseURL: server.URL, UpstreamGroup: "vip", NewAPIUserID: common.GetPointer(42), Enabled: true},
	} {
		require.NoError(t, binding.SetCredentials(model.RoutingCredentials{NewAPIAccessToken: "shared-secret-token"}))
		require.NoError(t, db.Create(&binding).Error)
	}

	summary, err := runRoutingCostSyncTask(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, requests["/api/user/self"])
	assert.Equal(t, 1, requests["/api/pricing"])
	assert.EqualValues(t, 1, summary["accounts"])
	assert.EqualValues(t, 2, summary["snapshots"])
	assert.EqualValues(t, 1, summary["successful_accounts"])
	var accounts []model.RoutingUpstreamAccount
	require.NoError(t, db.Find(&accounts).Error)
	require.Len(t, accounts, 1)
	assert.NotContains(t, accounts[0].AccountKey, "shared-secret-token")
	assert.NotContains(t, accounts[0].MaskedIdentity, "shared-secret-token")
	assert.Equal(t, model.RoutingUpstreamSyncStatusSuccess, accounts[0].LastSyncStatus)
	var versions []model.RoutingCostSnapshotVersion
	require.NoError(t, db.Order("channel_id asc").Find(&versions).Error)
	require.Len(t, versions, 2)
	assert.Equal(t, []int{790, 791}, []int{versions[0].ChannelID, versions[1].ChannelID})
	assert.Equal(t, versions[0].ObservedTime, versions[1].ObservedTime)
	var balances []model.RoutingChannelHealthState
	require.NoError(t, db.Where("channel_id IN ?", []int{790, 791}).Order("channel_id asc").Find(&balances).Error)
	require.Len(t, balances, 2)
	assert.Equal(t, 3.0, balances[0].Balance)
	assert.Equal(t, 3.0, balances[1].Balance)
}

func TestRunRoutingCostSyncTaskIsolatesInvalidGroupPricingWithinSharedAccount(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelHealthState{},
	))

	requests := 0
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/user/self" {
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota":1000000,"used_quota":0}}`)
			return
		}
		if r.URL.Path == "/api/pricing" {
			requests++
			_, _ = io.WriteString(w, `{"success":true,"data":[{"model_name":"gpt-partial","quota_type":0,"model_ratio":1,"completion_ratio":1,"enable_groups":["all"]}],"group_ratio":{"basic":1,"vip":-1}}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	for _, binding := range []model.RoutingChannelBinding{
		{ChannelID: 792, UpstreamType: model.RoutingUpstreamTypeNewAPI, BaseURL: server.URL, UpstreamGroup: "vip", NewAPIUserID: common.GetPointer(77), Enabled: true},
		{ChannelID: 793, UpstreamType: model.RoutingUpstreamTypeNewAPI, BaseURL: server.URL, UpstreamGroup: "basic", NewAPIUserID: common.GetPointer(77), Enabled: true},
	} {
		require.NoError(t, binding.SetCredentials(model.RoutingCredentials{NewAPIAccessToken: "partial-secret-token"}))
		require.NoError(t, db.Create(&binding).Error)
	}

	summary, err := runRoutingCostSyncTask(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, requests)
	assert.EqualValues(t, 1, summary["snapshots"])
	assert.EqualValues(t, 1, summary["errors"])
	assert.EqualValues(t, 1, summary["successful_accounts"])
	assert.EqualValues(t, 1, summary["partial_accounts"])
	var snapshots []model.RoutingCostSnapshot
	require.NoError(t, db.Find(&snapshots).Error)
	require.Len(t, snapshots, 1)
	assert.Equal(t, 793, snapshots[0].ChannelID)
	var failed model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", 792).First(&failed).Error)
	assert.Greater(t, failed.SyncBackoffUntil, common.GetTimestamp())
	var account model.RoutingUpstreamAccount
	require.NoError(t, db.First(&account).Error)
	assert.Equal(t, model.RoutingUpstreamSyncStatusPartial, account.LastSyncStatus)
	assert.Equal(t, model.RoutingUpstreamAccountStatusDegraded, account.Status)
}

func TestRunRoutingCostSyncTaskStoresFutureEffectivePriceWithoutActivatingLatest(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelHealthState{},
	))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	now := common.GetTimestamp()
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota":1000000,"used_quota":0}}`)
		case "/api/pricing":
			_, _ = fmt.Fprintf(w, `{"success":true,"data":[{"model_name":"gpt-future","quota_type":0,"model_ratio":1,"completion_ratio":1}],"group_ratio":{"vip":1},"pricing_version":"future-v1","effective_time":%d,"expires_time":%d}`, now+3600, now+7200)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })
	binding := model.RoutingChannelBinding{ChannelID: 794, UpstreamType: model.RoutingUpstreamTypeNewAPI, BaseURL: server.URL, UpstreamGroup: "vip", NewAPIUserID: common.GetPointer(88), Enabled: true}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{NewAPIAccessToken: "future-token"}))
	require.NoError(t, db.Create(&binding).Error)

	_, err := runRoutingCostSyncTask(context.Background())

	require.NoError(t, err)
	var version model.RoutingCostSnapshotVersion
	require.NoError(t, db.Where("channel_id = ?", 794).First(&version).Error)
	assert.Equal(t, now+3600, version.EffectiveTime)
	var latestCount int64
	require.NoError(t, db.Model(&model.RoutingCostSnapshot{}).Where("channel_id = ?", 794).Count(&latestCount).Error)
	assert.Zero(t, latestCount)
	_, cached := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: 794, Model: "gpt-future"})
	assert.False(t, cached)
}

func TestRunRoutingCostSyncTaskDoesNotMarkServingAuthFailureOnUnauthorizedUpstream(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	assert.EqualValues(t, 0, summary["successful_accounts"])
	_, cached := routinghotcache.GetAuthFailure(778)
	assert.False(t, cached)
	var marked int64
	require.NoError(t, db.Model(&model.RoutingChannelHealthState{}).
		Where("channel_id = ? AND auth_failure = ?", 778, true).
		Count(&marked).Error)
	assert.Zero(t, marked)

	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", 778).First(&updated).Error)
	require.NotNil(t, updated.LastSyncError)
	assert.NotContains(t, *updated.LastSyncError, "secret-token")
	assert.Greater(t, updated.SyncBackoffUntil, common.GetTimestamp())
}

func TestRunRoutingCostSyncTaskSkipsBindingsStillInBackoff(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))

	requestCount := 0
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestRunRoutingCostSyncTaskPersistsFailureBackoffAndClearsOnSuccess(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelHealthState{},
	))

	var succeed atomic.Bool
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !succeed.Load() {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"success":false}`)
			return
		}
		switch r.URL.Path {
		case "/api/user/self":
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota":1000000,"used_quota":0}}`)
		case "/api/pricing":
			_, _ = io.WriteString(w, `{"success":true,"data":[],"group_ratio":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{
		ChannelID:        781,
		UpstreamType:     model.RoutingUpstreamTypeNewAPI,
		BaseURL:          server.URL,
		UpstreamGroup:    "vip",
		Enabled:          true,
		SyncFailureCount: 2,
	}
	require.NoError(t, db.Create(&binding).Error)

	var nowUnix atomic.Int64
	nowUnix.Store(1_700_000_000)
	deps := defaultRoutingCostSyncDeps()
	deps.now = nowUnix.Load
	deps.jitter = func(max time.Duration) time.Duration { return max }

	summary, err := runRoutingCostSyncTaskWithDeps(context.Background(), deps)
	require.NoError(t, err)
	assert.EqualValues(t, 1, summary["errors"])
	var failed model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", binding.ChannelID).First(&failed).Error)
	assert.Equal(t, 3, failed.SyncFailureCount)
	assert.Equal(t, nowUnix.Load()+int64((4*time.Minute)/time.Second), failed.SyncBackoffUntil)
	require.NotNil(t, failed.LastSyncError)

	succeed.Store(true)
	nowUnix.Store(failed.SyncBackoffUntil + 1)
	summary, err = runRoutingCostSyncTaskWithDeps(context.Background(), deps)
	require.NoError(t, err)
	assert.EqualValues(t, 0, summary["errors"])
	var recovered model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", binding.ChannelID).First(&recovered).Error)
	assert.Zero(t, recovered.SyncFailureCount)
	assert.Zero(t, recovered.SyncBackoffUntil)
	assert.Nil(t, recovered.LastSyncError)
}

func TestRunRoutingCostSyncTaskSaturatesFailureCountAndBackoffTimestamp(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelHealthState{},
	))
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"success":false}`)
	}))
	t.Cleanup(server.Close)

	maxInt := int(^uint(0) >> 1)
	maxInt64 := int64(^uint64(0) >> 1)
	binding := model.RoutingChannelBinding{
		ChannelID:        782,
		UpstreamType:     model.RoutingUpstreamTypeNewAPI,
		BaseURL:          server.URL,
		UpstreamGroup:    "vip",
		Enabled:          true,
		SyncFailureCount: maxInt,
	}
	require.NoError(t, db.Create(&binding).Error)
	deps := defaultRoutingCostSyncDeps()
	deps.now = func() int64 { return maxInt64 - 10 }
	deps.jitter = func(max time.Duration) time.Duration { return max }

	_, err := runRoutingCostSyncTaskWithDeps(context.Background(), deps)
	require.NoError(t, err)
	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", binding.ChannelID).First(&updated).Error)
	assert.Equal(t, maxInt, updated.SyncFailureCount)
	assert.Equal(t, maxInt64, updated.SyncBackoffUntil)
}

func TestRunRoutingCostSyncTaskUsesFailureTimeForBackoff(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelHealthState{},
	))
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"success":false}`)
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{
		ChannelID:     784,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		Enabled:       true,
	}
	require.NoError(t, db.Create(&binding).Error)

	const taskStartedAt int64 = 1_700_000_000
	const failureObservedAt int64 = taskStartedAt + 120
	var nowCalls atomic.Int64
	deps := defaultRoutingCostSyncDeps()
	deps.now = func() int64 {
		if nowCalls.Add(1) == 1 {
			return taskStartedAt
		}
		return failureObservedAt
	}
	deps.jitter = func(max time.Duration) time.Duration { return max }

	_, err := runRoutingCostSyncTaskWithDeps(context.Background(), deps)
	require.NoError(t, err)
	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("id = ?", binding.ID).First(&updated).Error)
	assert.Equal(t, failureObservedAt+int64(time.Minute/time.Second), updated.SyncBackoffUntil)
}

func TestRunRoutingCostSyncTaskDoesNotReviveDeletedBindingSnapshots(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelHealthState{},
	))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	pricingStarted := make(chan struct{})
	releasePricing := make(chan struct{})
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/pricing" {
			http.NotFound(w, r)
			return
		}
		close(pricingStarted)
		<-releasePricing
		_, _ = io.WriteString(w, `{"success":true,"data":[{"model_name":"gpt-stale","quota_type":0,"model_ratio":1,"completion_ratio":1}],"group_ratio":{"vip":1}}`)
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{
		ChannelID:     785,
		UpstreamType:  "test",
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		Enabled:       true,
	}
	require.NoError(t, db.Create(&binding).Error)

	type syncResult struct {
		summary map[string]any
		err     error
	}
	result := make(chan syncResult, 1)
	go func() {
		summary, err := runRoutingCostSyncTaskWithDeps(context.Background(), defaultRoutingCostSyncDeps())
		result <- syncResult{summary: summary, err: err}
	}()
	require.Eventually(t, func() bool {
		select {
		case <-pricingStarted:
			return true
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, db.Delete(&model.RoutingChannelBinding{}, binding.ID).Error)
	close(releasePricing)

	completed := <-result
	require.NoError(t, completed.err)
	assert.EqualValues(t, 1, completed.summary["stale_bindings"])
	var snapshotCount int64
	require.NoError(t, db.Model(&model.RoutingCostSnapshot{}).Where("channel_id = ?", binding.ChannelID).Count(&snapshotCount).Error)
	assert.Zero(t, snapshotCount)
	_, cached := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: binding.ChannelID, Model: "gpt-stale"})
	assert.False(t, cached)
}

func TestRunRoutingCostSyncTaskDiscardsSnapshotsAfterBindingConfigChanges(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelHealthState{},
	))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	pricingStarted := make(chan struct{})
	releasePricing := make(chan struct{})
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/pricing" {
			http.NotFound(w, r)
			return
		}
		close(pricingStarted)
		<-releasePricing
		_, _ = io.WriteString(w, `{"success":true,"data":[{"model_name":"gpt-old-config","quota_type":0,"model_ratio":1,"completion_ratio":1}],"group_ratio":{"vip":1}}`)
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{
		ChannelID:     786,
		UpstreamType:  "test",
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		Enabled:       true,
	}
	require.NoError(t, db.Create(&binding).Error)

	type syncResult struct {
		summary map[string]any
		err     error
	}
	result := make(chan syncResult, 1)
	go func() {
		summary, err := runRoutingCostSyncTaskWithDeps(context.Background(), defaultRoutingCostSyncDeps())
		result <- syncResult{summary: summary, err: err}
	}()
	require.Eventually(t, func() bool {
		select {
		case <-pricingStarted:
			return true
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, db.Model(&model.RoutingChannelBinding{}).Where("id = ?", binding.ID).Updates(map[string]any{
		"upstream_group": "changed",
		"updated_time":   binding.UpdatedTime + 1,
	}).Error)
	close(releasePricing)

	completed := <-result
	require.NoError(t, completed.err)
	assert.EqualValues(t, 1, completed.summary["stale_bindings"])
	var snapshotCount int64
	require.NoError(t, db.Model(&model.RoutingCostSnapshot{}).Where("channel_id = ?", binding.ChannelID).Count(&snapshotCount).Error)
	assert.Zero(t, snapshotCount)
	_, cached := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: binding.ChannelID, Model: "gpt-old-config"})
	assert.False(t, cached)
}

func TestRunRoutingCostSyncTaskSnapshotPersistenceHonorsContext(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelHealthState{},
	))
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/pricing" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"data":[{"model_name":"gpt-context","quota_type":0,"model_ratio":1,"completion_ratio":1}],"group_ratio":{"vip":1}}`)
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{
		ChannelID:     787,
		UpstreamType:  "test",
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		Enabled:       true,
	}
	require.NoError(t, db.Create(&binding).Error)

	createStarted := make(chan struct{}, 1)
	releaseCreate := make(chan struct{})
	const callbackName = "test:block_routing_cost_snapshot_create"
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.RoutingCostSnapshot{}).TableName() {
			return
		}
		createStarted <- struct{}{}
		select {
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		case <-releaseCreate:
			tx.AddError(errors.New("routing cost snapshot create did not receive task context"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := runRoutingCostSyncTaskWithDeps(ctx, defaultRoutingCostSyncDeps())
		result <- err
	}()
	<-createStarted
	cancel()

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		close(releaseCreate)
		<-result
		require.Fail(t, "routing cost snapshot persistence ignored task context")
	}
}

func TestFetchRoutingCostSnapshotsModelMappingHonorsContext(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/pricing" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"data":[{"model_name":"gpt-context-map","quota_type":0,"model_ratio":1,"completion_ratio":1}],"group_ratio":{"vip":1}}`)
	}))
	t.Cleanup(server.Close)

	queryStarted := make(chan struct{}, 1)
	releaseQuery := make(chan struct{})
	const callbackName = "test:block_routing_model_mapping_query"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "channels" {
			return
		}
		queryStarted <- struct{}{}
		select {
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		case <-releaseQuery:
			tx.AddError(errors.New("routing model mapping query did not receive task context"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := fetchRoutingCostSnapshots(ctx, model.RoutingChannelBinding{
			ChannelID:     788,
			UpstreamType:  "test",
			BaseURL:       server.URL,
			UpstreamGroup: "vip",
		})
		result <- err
	}()
	<-queryStarted
	cancel()

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		close(releaseQuery)
		<-result
		require.Fail(t, "routing model mapping query ignored task context")
	}
}

func TestFetchRoutingUpstreamBalanceDoesNotReviveDeletedBindingState(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/user/self" {
			http.NotFound(w, r)
			return
		}
		close(requestStarted)
		<-releaseResponse
		_, _ = io.WriteString(w, `{"success":true,"data":{"quota":1000000,"used_quota":0}}`)
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{
		ChannelID:     789,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		Enabled:       true,
	}
	require.NoError(t, db.Create(&binding).Error)

	result := make(chan error, 1)
	go func() {
		result <- fetchRoutingUpstreamBalance(context.Background(), binding, model.RoutingCredentials{})
	}()
	<-requestStarted
	require.NoError(t, db.Delete(&model.RoutingChannelBinding{}, binding.ID).Error)
	close(releaseResponse)

	require.ErrorIs(t, <-result, model.ErrRoutingBindingChanged)
	var healthCount int64
	require.NoError(t, db.Model(&model.RoutingChannelHealthState{}).Where("channel_id = ?", binding.ChannelID).Count(&healthCount).Error)
	assert.Zero(t, healthCount)
	_, cached := routinghotcache.GetBalance(binding.ChannelID)
	assert.False(t, cached)
}

func TestFetchRoutingSub2APIBalanceDoesNotReviveDeletedBindingState(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/usage" {
			http.NotFound(w, r)
			return
		}
		close(requestStarted)
		<-releaseResponse
		_, _ = io.WriteString(w, `{"code":0,"data":{"balance":9.25}}`)
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{
		ChannelID:     790,
		UpstreamType:  model.RoutingUpstreamTypeSub2API,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		Enabled:       true,
	}
	require.NoError(t, db.Create(&binding).Error)

	result := make(chan error, 1)
	go func() {
		result <- fetchRoutingSub2APIBalance(
			context.Background(),
			binding,
			model.RoutingCredentials{GatewayAPIKey: "gateway-token"},
			"jwt-token",
		)
	}()
	<-requestStarted
	require.NoError(t, db.Delete(&model.RoutingChannelBinding{}, binding.ID).Error)
	close(releaseResponse)

	require.ErrorIs(t, <-result, model.ErrRoutingBindingChanged)
	var healthCount int64
	require.NoError(t, db.Model(&model.RoutingChannelHealthState{}).Where("channel_id = ?", binding.ChannelID).Count(&healthCount).Error)
	assert.Zero(t, healthCount)
	_, cached := routinghotcache.GetBalance(binding.ChannelID)
	assert.False(t, cached)
}

func TestPersistRoutingBalanceDoesNotPublishSameTimestampNoop(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	for index, upstreamType := range []string{
		model.RoutingUpstreamTypeNewAPI,
		model.RoutingUpstreamTypeSub2API,
	} {
		t.Run(upstreamType, func(t *testing.T) {
			channelID := 791 + index
			binding := model.RoutingChannelBinding{
				ChannelID:     channelID,
				UpstreamType:  upstreamType,
				BaseURL:       "https://balance.example",
				UpstreamGroup: "vip",
				Enabled:       true,
			}
			require.NoError(t, db.Create(&binding).Error)
			const updatedTime int64 = 500
			require.NoError(t, db.Create(&model.RoutingChannelHealthState{
				ChannelID:          channelID,
				BalanceKnown:       true,
				Balance:            9.25,
				BalanceUpdatedTime: updatedTime,
				UpdatedTime:        updatedTime + 10,
			}).Error)
			routinghotcache.SetBalanceForTest(channelID, routinghotcache.BalanceSnapshot{
				Known:       true,
				Balance:     9.25,
				UpdatedUnix: updatedTime,
			})

			require.NoError(t, persistRoutingBalance(context.Background(), binding, 1.25, updatedTime))

			var health model.RoutingChannelHealthState
			require.NoError(t, db.Where("channel_id = ?", channelID).First(&health).Error)
			assert.Equal(t, 9.25, health.Balance)
			assert.Equal(t, updatedTime, health.BalanceUpdatedTime)
			cached, ok := routinghotcache.GetBalance(channelID)
			require.True(t, ok)
			assert.Equal(t, 9.25, cached.Balance)
			assert.Equal(t, updatedTime, cached.UpdatedUnix)
		})
	}
}

func TestRoutingCostSyncBindingStateUpdateFailureFailsSystemTaskAndSanitizesError(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.SystemTask{},
		&model.SystemTaskLock{},
		&model.RoutingOperation{},
		&model.RoutingChannelBinding{},
		&model.RoutingCostSnapshot{},
		&model.RoutingChannelMetric{},
		&model.RoutingBreakerState{},
		&model.RoutingChannelHealthState{},
	))
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"success":false}`)
	}))
	t.Cleanup(server.Close)
	require.NoError(t, db.Create(&model.RoutingChannelBinding{
		ChannelID:     783,
		UpstreamType:  model.RoutingUpstreamTypeNewAPI,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		Enabled:       true,
	}).Error)

	forcedErr := errors.New("forced Authorization: Bearer sk-secret\nstate update failure")
	callbackName := "test:fail_routing_binding_state_update"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "routing_channel_bindings" {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	task, err := model.CreateSystemTask(model.SystemTaskTypeRoutingCostSync, nil, nil)
	require.NoError(t, err)
	const runnerID = "routing-cost-test-runner"
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, ok)

	(routingCostSyncHandler{}).Run(context.Background(), claimed, runnerID)

	finished, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, finished)
	assert.Equal(t, model.SystemTaskStatusFailed, finished.Status)
	assert.NotContains(t, finished.Error, "sk-secret")
	assert.NotContains(t, finished.Error, "\n")
	assert.NotContains(t, finished.Result, "sk-secret")
}

func TestRunRoutingCostSyncTaskLoadsPersistedBreakerStatesIntoHotcache(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
	})
	require.NoError(t, db.Create(&model.Channel{Id: 781, Name: "single", Key: "single-key"}).Error)

	require.NoError(t, db.Create(&model.RoutingBreakerState{
		ChannelID:       781,
		APIKeyIndex:     model.RoutingMetricSingleKeyIndex,
		ModelName:       "gpt-test",
		Group:           "vip",
		State:           model.RoutingBreakerStateOpen,
		Reason:          "5xx",
		SemanticVersion: model.RoutingBreakerSemanticVersion,
		CooldownUntil:   common.GetTimestamp() + 60,
		UpdatedTime:     common.GetTimestamp(),
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
	require.NoError(t, db.AutoMigrate(&model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
	})
	now := common.GetTimestamp()
	require.NoError(t, db.Create(&model.Channel{Id: 782, Name: "single", Key: "single-key"}).Error)

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
		ChannelID:       782,
		APIKeyIndex:     model.RoutingMetricSingleKeyIndex,
		ModelName:       "gpt-test",
		Group:           "vip",
		State:           model.RoutingBreakerStateDegraded,
		SemanticVersion: model.RoutingBreakerSemanticVersion,
		UpdatedTime:     now,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingChannelHealthState{
		ChannelID:          782,
		AuthFailure:        true,
		AuthFailureReason:  "unauthorized",
		AuthFailureUntil:   now + 300,
		BalanceKnown:       true,
		Balance:            0.5,
		BalanceUpdatedTime: now,
		UpdatedTime:        now,
	}).Error)

	summary, err := refreshRoutingHotcacheFromDB(context.Background(), smart_routing_setting.GetSetting())

	require.NoError(t, err)
	assert.EqualValues(t, 1, summary["costs"])
	assert.EqualValues(t, 1, summary["metrics"])
	assert.EqualValues(t, 1, summary["breakers"])
	assert.EqualValues(t, 1, summary["health"])
	metric, ok := routinghotcache.GetMetric(routinghotcache.Key{ChannelID: 782, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, 250.0, metric.P95LatencyMs)
	cost, ok := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: 782, Model: "gpt-test"})
	require.True(t, ok)
	assert.Equal(t, 6.0, cost.Cost)
	breaker, ok := routinghotcache.GetBreaker(routinghotcache.Key{ChannelID: 782, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, model.RoutingBreakerStateDegraded, breaker.State)
	authFailure, ok := routinghotcache.GetAuthFailure(782)
	require.True(t, ok)
	assert.True(t, authFailure.Marked)
	assert.Equal(t, now, authFailure.UpdatedUnix)
	balance, ok := routinghotcache.GetBalance(782)
	require.True(t, ok)
	assert.True(t, balance.Known)
	assert.Equal(t, 0.5, balance.Balance)
}

func TestRefreshRoutingHotcacheFromDBPrefersLatestRowsUnderLimit(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	routinghotcache.ResetForTest()
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
	})
	now := common.GetTimestamp()
	const channelID = 99_999
	require.NoError(t, db.Create(&model.Channel{Id: channelID, Name: "single", Key: "single-key"}).Error)

	costs := make([]model.RoutingCostSnapshot, 0, 5001)
	metrics := make([]model.RoutingChannelMetric, 0, 5001)
	for i := 0; i < 5000; i++ {
		costs = append(costs, model.RoutingCostSnapshot{
			ChannelID:  channelID,
			ModelName:  fmt.Sprintf("old-cost-%d", i),
			GroupRatio: 9,
			BaseRatio:  1,
			Confidence: model.RoutingCostConfidenceFull,
			SnapshotTS: now - 10,
		})
		metrics = append(metrics, model.RoutingChannelMetric{
			ChannelID:    channelID,
			APIKeyIndex:  model.RoutingMetricSingleKeyIndex,
			ModelName:    fmt.Sprintf("old-metric-%d", i),
			Group:        "vip",
			BucketTs:     now - 10,
			RequestCount: 1,
			SuccessCount: 1,
			LatencyP95Ms: 900,
		})
	}
	costs = append(costs, model.RoutingCostSnapshot{
		ChannelID:  channelID,
		ModelName:  "latest-cost",
		GroupRatio: 2,
		BaseRatio:  1,
		Confidence: model.RoutingCostConfidenceFull,
		SnapshotTS: now,
	})
	metrics = append(metrics, model.RoutingChannelMetric{
		ChannelID:    channelID,
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

	summary, err := refreshRoutingHotcacheFromDB(context.Background(), smart_routing_setting.GetSetting())

	require.NoError(t, err)
	assert.EqualValues(t, 5000, summary["costs"])
	assert.EqualValues(t, 5000, summary["metrics"])
	cost, ok := routinghotcache.GetCost(routinghotcache.CostKey{ChannelID: channelID, Model: "latest-cost"})
	require.True(t, ok)
	assert.Equal(t, 2.0, cost.Cost)
	metric, ok := routinghotcache.GetMetric(routinghotcache.Key{ChannelID: channelID, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "latest-metric", Group: "vip"})
	require.True(t, ok)
	assert.Equal(t, 100.0, metric.P95LatencyMs)
}

func TestRunRoutingCostSyncTaskMasksNewAPISuccessFalseMessage(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	responseBody, err := common.Marshal(map[string]any{
		"success": false,
		"message": "bad token secret-token\r\nCookie: session=cookie-secret\r\n" + strings.Repeat("尾", common.SafeErrorMaxRunes+100),
	})
	require.NoError(t, err)
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
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
	assert.NotContains(t, *updated.LastSyncError, "cookie-secret")
	assert.NotContains(t, *updated.LastSyncError, "\r")
	assert.NotContains(t, *updated.LastSyncError, "\n")
	assert.LessOrEqual(t, utf8.RuneCountInString(*updated.LastSyncError), common.SafeErrorMaxRunes)
	assert.Contains(t, *updated.LastSyncError, "***")
}

func TestFetchRoutingCostSnapshotsMapsUpstreamModelNameToLocalName(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	var mu sync.Mutex
	requests := map[string]int{}
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	require.NoError(t, model.UpsertRoutingChannelAuthFailure(880, true, "authfail", common.GetTimestamp()+300))
	routinghotcache.SetAuthFailure(880, routinghotcache.HealthMarker{Marked: true, UpdatedUnix: common.GetTimestamp()})

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

	var health model.RoutingChannelHealthState
	require.NoError(t, db.Where("channel_id = ?", 880).First(&health).Error)
	assert.True(t, health.AuthFailure)
	assert.Equal(t, "authfail", health.AuthFailureReason)
	assert.NotZero(t, health.AuthFailureUntil)
	assert.True(t, health.BalanceKnown)
	assert.Equal(t, 9.25, health.Balance)
	assert.NotZero(t, health.BalanceUpdatedTime)
	authFailure, ok := routinghotcache.GetAuthFailure(880)
	require.True(t, ok)
	assert.True(t, authFailure.Marked)
}

func TestRoutingSub2APIJWTCoalescesConcurrentLogin(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelHealthState{}))
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
	})

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	const callers = 8
	ready := make(chan struct{}, callers)
	start := make(chan struct{})
	entered := make(chan struct{}, callers)
	respond := make(chan struct{})
	type loginResult struct {
		token string
		err   error
	}
	results := make(chan loginResult, callers)

	var loginMu sync.Mutex
	loginCount := 0
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/login" {
			http.NotFound(w, r)
			return
		}
		loginMu.Lock()
		loginCount++
		loginMu.Unlock()
		<-respond
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"token":"shared-jwt","expires_in":3600}}`)
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{ChannelID: 887, BaseURL: server.URL}
	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "pw-secret",
	}
	for range callers {
		go func() {
			ready <- struct{}{}
			<-start
			entered <- struct{}{}
			token, err := routingSub2APIJWT(context.Background(), binding, credentials)
			results <- loginResult{token: token, err: err}
		}()
	}
	for range callers {
		<-ready
	}
	close(start)
	for range callers {
		<-entered
	}
	close(respond)

	collectedResults := make([]loginResult, 0, callers)
	for range callers {
		collectedResults = append(collectedResults, <-results)
	}
	for _, result := range collectedResults {
		require.NoError(t, result.err)
		assert.Equal(t, "shared-jwt", result.token)
	}
	loginMu.Lock()
	actualLoginCount := loginCount
	loginMu.Unlock()
	assert.Equal(t, 1, actualLoginCount)
}

func TestRoutingSub2APIRedisUnlockUsesIndependentBoundedContext(t *testing.T) {
	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	evalStarted := make(chan bool, 1)
	redisClient := redis.NewClient(&redis.Options{
		Addr: "unused:0",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("unexpected Redis network access")
		},
		MaxRetries: -1,
	})
	redisClient.AddHook(blockingRoutingSub2APIEvalHook{started: evalStarted})
	common.RedisEnabled = true
	common.RDB = redisClient
	t.Cleanup(func() {
		_ = redisClient.Close()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
	})

	previousUnlockTimeout := routingSub2APIUnlockTimeout
	routingSub2APIUnlockTimeout = 10 * time.Millisecond
	t.Cleanup(func() { routingSub2APIUnlockTimeout = previousUnlockTimeout })

	sharedCtx, cancelShared := context.WithCancel(context.Background())
	unlock, err := acquireRoutingSub2APIRedisLock(sharedCtx, routingSub2APITestAuthKey(890))
	require.NoError(t, err)
	require.NotNil(t, unlock)
	cancelShared()

	done := make(chan struct{})
	go func() {
		unlock()
		close(done)
	}()

	select {
	case canceledAtStart := <-evalStarted:
		assert.False(t, canceledAtStart)
	case <-time.After(time.Second):
		require.FailNow(t, "Redis unlock did not execute EVAL")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t, "Redis unlock did not stop at its context deadline")
	}
}

func TestRoutingSub2APIJWTLeaderCancellationDoesNotCancelSharedLogin(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelHealthState{}))
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
	})

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	started := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	var startedOnce sync.Once
	var completedOnce sync.Once
	var loginMu sync.Mutex
	loginCount := 0
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/login" {
			http.NotFound(w, r)
			return
		}
		loginMu.Lock()
		loginCount++
		loginMu.Unlock()
		startedOnce.Do(func() { close(started) })
		defer completedOnce.Do(func() { close(completed) })

		select {
		case <-release:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"token":"shared-jwt","expires_in":3600}}`)
		case <-r.Context().Done():
			return
		}
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{ChannelID: 888, BaseURL: server.URL}
	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "pw-secret",
	}
	type loginResult struct {
		token string
		err   error
	}
	callerResults := make(chan loginResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		token, err := routingSub2APIJWT(ctx, binding, credentials)
		callerResults <- loginResult{token: token, err: err}
	}()

	<-started
	cancel()
	callerResult := <-callerResults
	assert.ErrorIs(t, callerResult.err, context.Canceled)
	assert.Empty(t, callerResult.token)

	close(release)
	<-completed
	token, err := routingSub2APIJWT(context.Background(), binding, credentials)

	require.NoError(t, err)
	assert.Equal(t, "shared-jwt", token)
	loginMu.Lock()
	actualLoginCount := loginCount
	loginMu.Unlock()
	assert.Equal(t, 1, actualLoginCount)
}

func TestRoutingSub2APIJWTResetRetiresInFlightLogin(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelHealthState{}))
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
	})

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	firstLoginStarted := make(chan struct{})
	secondLoginStarted := make(chan struct{})
	firstLoginRelease := make(chan struct{})
	var releaseFirstLoginOnce sync.Once
	releaseFirstLogin := func() {
		releaseFirstLoginOnce.Do(func() { close(firstLoginRelease) })
	}
	defer releaseFirstLogin()
	var loginMu sync.Mutex
	loginCount := 0
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/login" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		loginMu.Lock()
		loginCount++
		requestNumber := loginCount
		loginMu.Unlock()

		if requestNumber == 1 {
			close(firstLoginStarted)
			<-firstLoginRelease
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"token":"retired-jwt","expires_in":3600}}`)
			return
		}
		if requestNumber == 2 {
			close(secondLoginStarted)
		}
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"token":"current-jwt","expires_in":3600}}`)
	}))
	t.Cleanup(server.Close)

	binding := model.RoutingChannelBinding{ChannelID: 889, BaseURL: server.URL}
	credentials := model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "pw-secret",
	}
	type loginResult struct {
		token string
		err   error
	}
	retiredResult := make(chan loginResult, 1)
	go func() {
		token, err := routingSub2APIJWT(context.Background(), binding, credentials)
		retiredResult <- loginResult{token: token, err: err}
	}()

	<-firstLoginStarted
	resetRoutingSub2APITestState()
	currentResult := make(chan loginResult, 1)
	go func() {
		token, err := routingSub2APIJWT(context.Background(), binding, credentials)
		currentResult <- loginResult{token: token, err: err}
	}()

	select {
	case <-secondLoginStarted:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "reset login joined the retired singleflight group")
	}
	var current loginResult
	select {
	case current = <-currentResult:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "reset login did not finish while the retired login was blocked")
	}
	require.NoError(t, current.err)
	assert.Equal(t, "current-jwt", current.token)
	select {
	case result := <-retiredResult:
		require.FailNowf(t, "retired login finished before release", "token=%q err=%v", result.token, result.err)
	default:
	}

	releaseFirstLogin()
	var retired loginResult
	select {
	case retired = <-retiredResult:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "retired login did not finish after release")
	}
	require.NoError(t, retired.err)
	assert.Equal(t, "retired-jwt", retired.token)
	cachedToken, err := common.DecryptAESGCMString(routingSub2APICachedJWTForTest(binding.ChannelID))
	require.NoError(t, err)
	assert.Equal(t, "current-jwt", cachedToken)
	loginMu.Lock()
	actualLoginCount := loginCount
	loginMu.Unlock()
	assert.Equal(t, 2, actualLoginCount)
}

func TestRoutingSub2APIJWTCachePrunesExpiredAndOldestEntries(t *testing.T) {
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	now := common.GetTimestamp()
	routingSub2APIJWTCache.Lock()
	routingSub2APIJWTCache.values = map[routingSub2APIAuthKey]routingSub2APIJWTCacheEntry{
		routingSub2APITestAuthKey(10): {Ciphertext: "expired", ExpiresAt: now},
		routingSub2APITestAuthKey(20): {Ciphertext: "oldest", ExpiresAt: now + 10},
		routingSub2APITestAuthKey(30): {Ciphertext: "tied-smaller-channel", ExpiresAt: now + 20},
		routingSub2APITestAuthKey(40): {Ciphertext: "tied-larger-channel", ExpiresAt: now + 20},
		routingSub2APITestAuthKey(50): {Ciphertext: "newest", ExpiresAt: now + 30},
	}
	pruneRoutingSub2APIJWTCacheLocked(now, 2)
	_, hasExpired := routingSub2APIJWTCache.values[routingSub2APITestAuthKey(10)]
	_, hasOldest := routingSub2APIJWTCache.values[routingSub2APITestAuthKey(20)]
	_, hasTiedSmallerChannel := routingSub2APIJWTCache.values[routingSub2APITestAuthKey(30)]
	_, hasTiedLargerChannel := routingSub2APIJWTCache.values[routingSub2APITestAuthKey(40)]
	_, hasNewest := routingSub2APIJWTCache.values[routingSub2APITestAuthKey(50)]
	cacheSize := len(routingSub2APIJWTCache.values)
	routingSub2APIJWTCache.Unlock()

	assert.False(t, hasExpired)
	assert.False(t, hasOldest)
	assert.False(t, hasTiedSmallerChannel)
	assert.True(t, hasTiedLargerChannel)
	assert.True(t, hasNewest)
	assert.Equal(t, 2, cacheSize)
	assert.Equal(t, RoutingSub2APIJWTCacheStats{
		Entries:     2,
		Bytes:       25,
		Expirations: 1,
		Evictions:   2,
	}, RoutingSub2APIJWTCacheRuntimeStats())
}

func TestRoutingSub2APIJWTCacheNeverExceedsLimitOnSet(t *testing.T) {
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
	})

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	routingSub2APIJWTCache.Lock()
	previousMaxEntries := routingSub2APIMaxJWTEntries
	routingSub2APIMaxJWTEntries = 2
	routingSub2APIJWTCache.Unlock()
	t.Cleanup(func() {
		routingSub2APIJWTCache.Lock()
		routingSub2APIMaxJWTEntries = previousMaxEntries
		routingSub2APIJWTCache.Unlock()
	})

	ctx := context.Background()
	setRoutingSub2APICachedJWT(ctx, routingSub2APITestAuthKey(901), "jwt-oldest", time.Hour)
	setRoutingSub2APICachedJWT(ctx, routingSub2APITestAuthKey(902), "jwt-middle", 2*time.Hour)
	setRoutingSub2APICachedJWT(ctx, routingSub2APITestAuthKey(903), "jwt-newest", 3*time.Hour)

	routingSub2APIJWTCache.Lock()
	cacheSize := len(routingSub2APIJWTCache.values)
	routingSub2APIJWTCache.Unlock()
	latestToken, latestFound := getRoutingSub2APICachedJWT(ctx, routingSub2APITestAuthKey(903))
	_, oldestFound := getRoutingSub2APICachedJWT(ctx, routingSub2APITestAuthKey(901))

	assert.LessOrEqual(t, cacheSize, 2)
	assert.True(t, latestFound)
	assert.Equal(t, "jwt-newest", latestToken)
	assert.False(t, oldestFound)
	assert.Equal(t, RoutingSub2APIJWTCacheStats{
		Entries:   2,
		Bytes:     110,
		Evictions: 1,
	}, RoutingSub2APIJWTCacheRuntimeStats())
}

func TestRoutingSub2APIJWTCacheGetCountsExpiredEntries(t *testing.T) {
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	previousRedisEnabled := common.RedisEnabled
	previousRDB := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
	})

	now := common.GetTimestamp()
	routingSub2APIJWTCache.Lock()
	routingSub2APIJWTCache.values = map[routingSub2APIAuthKey]routingSub2APIJWTCacheEntry{
		routingSub2APITestAuthKey(910): {Ciphertext: "expired", ExpiresAt: now},
		routingSub2APITestAuthKey(911): {Ciphertext: "live", ExpiresAt: now + 60},
	}
	routingSub2APIJWTCache.Unlock()

	_, found := getRoutingSub2APICachedJWT(context.Background(), routingSub2APITestAuthKey(910))

	assert.False(t, found)
	assert.Equal(t, RoutingSub2APIJWTCacheStats{
		Entries:     1,
		Bytes:       4,
		Expirations: 1,
	}, RoutingSub2APIJWTCacheRuntimeStats())
}

func TestResetRoutingSub2APIJWTCacheClearsRuntimeStats(t *testing.T) {
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	now := common.GetTimestamp()
	routingSub2APIJWTCache.Lock()
	routingSub2APIJWTCache.values = map[routingSub2APIAuthKey]routingSub2APIJWTCacheEntry{
		routingSub2APITestAuthKey(920): {Ciphertext: "expired", ExpiresAt: now},
		routingSub2APITestAuthKey(921): {Ciphertext: "oldest", ExpiresAt: now + 10},
		routingSub2APITestAuthKey(922): {Ciphertext: "newest", ExpiresAt: now + 20},
	}
	pruneRoutingSub2APIJWTCacheLocked(now, 1)
	routingSub2APIJWTCache.Unlock()
	require.Equal(t, RoutingSub2APIJWTCacheStats{
		Entries:     1,
		Bytes:       6,
		Expirations: 1,
		Evictions:   1,
	}, RoutingSub2APIJWTCacheRuntimeStats())

	resetRoutingSub2APITestState()

	assert.Equal(t, RoutingSub2APIJWTCacheStats{}, RoutingSub2APIJWTCacheRuntimeStats())
}

func TestFetchRoutingCostSnapshotsSub2APILoginFailureDoesNotMarkServingAuthAndMasksSecrets(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	_, cached := routinghotcache.GetAuthFailure(881)
	assert.False(t, cached)
	var marked int64
	require.NoError(t, db.Model(&model.RoutingChannelHealthState{}).
		Where("channel_id = ? AND auth_failure = ?", 881, true).
		Count(&marked).Error)
	assert.Zero(t, marked)

	var updated model.RoutingChannelBinding
	require.NoError(t, db.Where("channel_id = ?", 881).First(&updated).Error)
	require.NotNil(t, updated.LastSyncError)
	assert.NotContains(t, *updated.LastSyncError, "pw-secret")
	assert.Greater(t, updated.SyncBackoffUntil, common.GetTimestamp())
}

func TestFetchRoutingCostSnapshotsSub2APISuccessDoesNotClearServingAuthFailure(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelBinding{}, &model.RoutingCostSnapshot{}, &model.RoutingChannelMetric{}, &model.RoutingBreakerState{}, &model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)
	resetRoutingSub2APITestState()
	t.Cleanup(resetRoutingSub2APITestState)

	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/usage":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"balance":3.5}}`)
		case "/api/v1/groups/available":
			_, _ = fmt.Fprint(w, `{"code":0,"data":[{"id":"vip","rate_multiplier":1}]}`)
		case "/api/v1/groups/rates":
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"vip":1}}`)
		case "/api/v1/channels/available":
			_, _ = fmt.Fprint(w, `{"code":0,"data":[{"models":["claude-3"],"input_price":2,"output_price":4}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	require.NoError(t, model.UpsertRoutingChannelAuthFailure(882, true, "authfail", common.GetTimestamp()+300))
	routinghotcache.SetAuthFailure(882, routinghotcache.HealthMarker{Marked: true, UpdatedUnix: common.GetTimestamp()})

	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-routing-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() { common.CryptoSecret = previousSecret })

	binding := model.RoutingChannelBinding{
		ChannelID:     882,
		UpstreamType:  model.RoutingUpstreamTypeSub2API,
		BaseURL:       server.URL,
		UpstreamGroup: "vip",
		Enabled:       true,
	}
	require.NoError(t, binding.SetCredentials(model.RoutingCredentials{Sub2APIToken: "jwt-secret"}))

	snapshots, err := fetchRoutingCostSnapshots(context.Background(), binding)

	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	authFailure, cached := routinghotcache.GetAuthFailure(882)
	require.True(t, cached)
	assert.True(t, authFailure.Marked)

	var health model.RoutingChannelHealthState
	require.NoError(t, db.Where("channel_id = ?", 882).First(&health).Error)
	assert.True(t, health.AuthFailure)
	assert.Equal(t, "authfail", health.AuthFailureReason)
	assert.NotZero(t, health.AuthFailureUntil)
}

func TestRoutingSub2APIRequestDoesNotMarkAuthFailureForNonAuthErrors(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "http 500",
			statusCode: http.StatusInternalServerError,
			body:       `{"code":500,"message":"database is unavailable"}`,
		},
		{
			name:       "envelope code non auth",
			statusCode: http.StatusOK,
			body:       `{"code":5001,"message":"provider capacity exhausted for sk-secret"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			t.Cleanup(server.Close)

			binding := model.RoutingChannelBinding{ChannelID: 883, BaseURL: server.URL}
			credentials := model.RoutingCredentials{Sub2APIToken: "sk-secret"}

			_, err := routingSub2APIRequest(context.Background(), binding, credentials, http.MethodGet, "/api/test", "sk-secret", nil)

			require.Error(t, err)
			assert.False(t, routingUpstreamAuthError(err))
			assert.NotContains(t, err.Error(), "sk-secret")
			_, cached := routinghotcache.GetAuthFailure(883)
			assert.False(t, cached)

			var count int64
			require.NoError(t, db.Model(&model.RoutingChannelHealthState{}).Where("channel_id = ? AND auth_failure = ?", 883, true).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestRoutingSub2APIRequestReturnsTypedAuthErrorsWithoutMarkingServingHealth(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.RoutingChannelHealthState{}))
	routinghotcache.ResetForTest()
	t.Cleanup(routinghotcache.ResetForTest)

	tests := []struct {
		name       string
		channelID  int
		statusCode int
		body       string
	}{
		{
			name:       "http 401",
			channelID:  884,
			statusCode: http.StatusUnauthorized,
			body:       `{"code":401,"message":"expired token sk-secret"}`,
		},
		{
			name:       "http 403",
			channelID:  885,
			statusCode: http.StatusForbidden,
			body:       `{"code":403,"message":"forbidden"}`,
		},
		{
			name:       "auth message",
			channelID:  886,
			statusCode: http.StatusOK,
			body:       `{"code":1001,"message":"invalid token sk-secret"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			t.Cleanup(server.Close)

			binding := model.RoutingChannelBinding{ChannelID: tt.channelID, BaseURL: server.URL}
			credentials := model.RoutingCredentials{Sub2APIToken: "sk-secret"}

			_, err := routingSub2APIRequest(context.Background(), binding, credentials, http.MethodGet, "/api/test", "sk-secret", nil)

			require.Error(t, err)
			assert.True(t, routingUpstreamAuthError(err))
			_, cached := routinghotcache.GetAuthFailure(tt.channelID)
			assert.False(t, cached)
			var marked int64
			require.NoError(t, db.Model(&model.RoutingChannelHealthState{}).
				Where("channel_id = ? AND auth_failure = ?", tt.channelID, true).
				Count(&marked).Error)
			assert.Zero(t, marked)
		})
	}
}

func TestRoutingCostEndpointsRejectNonJSONContentType(t *testing.T) {
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/api/pricing":
			_, _ = io.WriteString(w, `{"success":true,"data":[],"group_ratio":{}}`)
		case "/api/user/self":
			_, _ = io.WriteString(w, `{"success":false,"message":"not JSON"}`)
		case "/api/test":
			_, _ = io.WriteString(w, `{"code":0,"success":true,"data":{"ok":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	binding := model.RoutingChannelBinding{ChannelID: 990, BaseURL: server.URL, UpstreamGroup: "default"}

	_, err := fetchRoutingPricingPayload(context.Background(), binding)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Content-Type")

	err = fetchRoutingUpstreamBalance(context.Background(), binding, model.RoutingCredentials{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Content-Type")

	_, err = routingSub2APIRequest(context.Background(), binding, model.RoutingCredentials{}, http.MethodGet, "/api/test", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Content-Type")
}

func TestRoutingCostRequestsRejectInvalidEgressPolicyBeforeSending(t *testing.T) {
	var calls atomic.Int32
	restoreRoutingCostHTTPDoerForTest(t, routingCostDoerFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("request must not be sent")
	}))

	invalidPolicy := `{not-json}`
	binding := model.RoutingChannelBinding{
		ChannelID:        994,
		BaseURL:          "https://routing.example.com",
		UpstreamGroup:    "vip",
		EgressPolicyJSON: &invalidPolicy,
	}
	_, err := fetchRoutingNewAPIPricingPayload(context.Background(), binding, model.RoutingCredentials{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid routing cost egress policy")
	assert.Zero(t, calls.Load())

	binding.EgressPolicyJSON = nil
	_, err = routingSub2APIRequest(
		context.Background(), binding,
		model.RoutingCredentials{CustomCAPEM: "not-a-certificate"},
		http.MethodGet, "/api/test", "", nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid routing cost custom CA")
	assert.Zero(t, calls.Load())
}

func TestRoutingSub2APIRequestDecodesGzipJSON(t *testing.T) {
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		if _, err := io.WriteString(writer, `{"code":0,"success":true,"data":{"ok":true}}`); err != nil {
			t.Errorf("write gzip response: %v", err)
			return
		}
		if err := writer.Close(); err != nil {
			t.Errorf("close gzip response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	raw, err := routingSub2APIRequest(
		context.Background(),
		model.RoutingChannelBinding{ChannelID: 991, BaseURL: server.URL},
		model.RoutingCredentials{},
		http.MethodGet,
		"/api/test",
		"",
		nil,
	)

	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(raw))
}

func TestRoutingCostMalformedJSONErrorsDoNotEchoUpstreamLiterals(t *testing.T) {
	longNumber := strings.Repeat("9", 2048)
	server := newRoutingCostTLSServerForTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/pricing":
			_, _ = fmt.Fprintf(w, `{"success":true,"data":[{"quota_type":%s}]}`, longNumber)
		case "/api/test":
			_, _ = fmt.Fprintf(w, `{"code":%s,"data":{}}`, longNumber)
		case "/api/v1/auth/login":
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"token":"token","expires_in":%s}}`, longNumber)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	binding := model.RoutingChannelBinding{ChannelID: 993, BaseURL: server.URL, UpstreamGroup: "vip"}

	_, pricingErr := fetchRoutingPricingPayload(context.Background(), binding)
	require.Error(t, pricingErr)
	assert.NotContains(t, pricingErr.Error(), strings.Repeat("9", 64))

	_, sub2APIErr := routingSub2APIRequest(context.Background(), binding, model.RoutingCredentials{}, http.MethodGet, "/api/test", "", nil)
	require.Error(t, sub2APIErr)
	assert.NotContains(t, sub2APIErr.Error(), strings.Repeat("9", 64))

	_, _, loginErr := loginRoutingSub2API(context.Background(), binding, model.RoutingCredentials{
		Sub2APIEmail:    "admin@example.com",
		Sub2APIPassword: "password",
	})
	require.Error(t, loginErr)
	assert.NotContains(t, loginErr.Error(), strings.Repeat("9", 64))
}
