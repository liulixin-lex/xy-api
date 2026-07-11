package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service/channelrouting"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectSmartChannelForGroupUsesReliabilityAvailabilityWithinPriority(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 101, Name: "weak", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 102, Name: "strong", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 101, Enabled: true, Priority: &priority, Weight: weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 102, Enabled: true, Priority: &priority, Weight: weight}).Error)
	model.InitChannelCache()

	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID:   101,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       "gpt-test",
		Group:       "default",
	}, routinghotcache.MetricSnapshot{
		RequestCount:            100,
		SuccessCount:            99,
		ReliabilityRequestCount: 10,
		ReliabilityFailureCount: 9,
		P95LatencyMs:            900,
		TPS:                     1,
	})
	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID:   102,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       "gpt-test",
		Group:       "default",
	}, routinghotcache.MetricSnapshot{
		RequestCount:            100,
		SuccessCount:            10,
		ReliabilityRequestCount: 10,
		ReliabilityFailureCount: 1,
		P95LatencyMs:            100,
		TPS:                     10,
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	param := &RetryParam{
		Ctx:         ctx,
		TokenGroup:  "default",
		ModelName:   "gpt-test",
		RequestPath: "/v1/chat/completions",
		Retry:       common.GetPointer(0),
	}
	setting := smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeBalanced,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
		MaxEjectedPct:      100,
		SnapshotStaleSec:   300,
	}

	channel, err := selectSmartChannelForGroup(param, "default", setting, true)

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 102, channel.Id)
}

func TestSmartRoutingCandidatesIgnoreLegacyMetricBreakerInflightAndCapacityForMultiKey(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeObserve,
	})

	priority := int64(10)
	weight := uint(10)
	channels := []model.Channel{
		{Id: 141, Name: "single", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight},
		{Id: 142, Name: "multi", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight, ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
	}
	for i := range channels {
		require.NoError(t, model.DB.Create(&channels[i]).Error)
		require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: channels[i].Id, Enabled: true, Priority: &priority, Weight: weight}).Error)
	}
	model.InitChannelCache()

	now := time.Now()
	singleAggregate := routinghotcache.Key{ChannelID: 141, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	singlePositive := routinghotcache.Key{ChannelID: 141, APIKeyIndex: 2, Model: "gpt-test", Group: "default"}
	multiAggregate := routinghotcache.Key{ChannelID: 142, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	multiPositive := routinghotcache.Key{ChannelID: 142, APIKeyIndex: 2, Model: "gpt-test", Group: "default"}

	routinghotcache.SetMetricForTest(singleAggregate, routinghotcache.MetricSnapshot{RequestCount: 11, ReliabilityRequestCount: 10, ReliabilityFailureCount: 1, P95TTFTMs: 123})
	routinghotcache.SetMetricForTest(singlePositive, routinghotcache.MetricSnapshot{RequestCount: 99, ReliabilityRequestCount: 99, ReliabilityFailureCount: 99})
	routinghotcache.SetBreakerForTest(singleAggregate, routinghotcache.BreakerSnapshot{State: routingselector.BreakerStateDegraded, UpdatedUnix: now.Unix()})
	routinghotcache.SetBreakerForTest(singlePositive, routinghotcache.BreakerSnapshot{State: routingselector.BreakerStateOpen, UpdatedUnix: now.Unix()})
	routinghotcache.SetCapacityCooldownForTest(singleAggregate, routinghotcache.CapacityCooldownSnapshot{SourceStatusCode: http.StatusTooManyRequests, CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(), UpdatedUnixMilli: now.UnixMilli()})
	routinghotcache.SetCapacityCooldownForTest(singlePositive, routinghotcache.CapacityCooldownSnapshot{SourceStatusCode: http.StatusPaymentRequired, CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(), UpdatedUnixMilli: now.UnixMilli()})

	for _, key := range []routinghotcache.Key{multiAggregate, multiPositive} {
		routinghotcache.SetMetricForTest(key, routinghotcache.MetricSnapshot{RequestCount: 88, ReliabilityRequestCount: 88, ReliabilityFailureCount: 80})
		routinghotcache.SetBreakerForTest(key, routinghotcache.BreakerSnapshot{State: routingselector.BreakerStateOpen, UpdatedUnix: now.Unix()})
		routinghotcache.SetCapacityCooldownForTest(key, routinghotcache.CapacityCooldownSnapshot{SourceStatusCode: http.StatusTooManyRequests, CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(), UpdatedUnixMilli: now.UnixMilli()})
	}
	routinghotcache.SetCostForTest(multiAggregate.CostKey(), routinghotcache.CostSnapshot{Known: true, Confidence: model.RoutingCostConfidenceFull, BillingMode: "per_request", GroupRatio: 2, ModelPrice: 0.25, UpdatedUnix: now.Unix()})

	legacyInflightRelease := routingmetrics.BeginInflight(nil, &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 142, ChannelIsMultiKey: false},
	}, 142)
	t.Cleanup(legacyInflightRelease)
	require.Equal(t, int64(1), routingmetrics.InflightCount(routingmetrics.InflightKey{
		ChannelID: 142, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default",
	}))

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	param := &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0)}
	candidates, err := smartRoutingCandidatesForGroup(param, "default")
	require.NoError(t, err)
	require.Len(t, candidates, 2)

	byChannelID := make(map[int]routingselector.Candidate, len(candidates))
	for _, candidate := range candidates {
		byChannelID[candidate.Channel.Id] = candidate
	}
	single := byChannelID[141]
	require.NotNil(t, single.Metric)
	assert.Equal(t, int64(11), single.Metric.RequestCount)
	assert.Equal(t, 123.0, single.Metric.P95TTFTMs)
	require.NotNil(t, single.Breaker)
	assert.Equal(t, routingselector.BreakerStateDegraded, single.Breaker.State)
	require.NotNil(t, single.Capacity)
	assert.Equal(t, http.StatusTooManyRequests, single.Capacity.SourceStatusCode)

	multi := byChannelID[142]
	assert.Nil(t, multi.Metric)
	assert.Nil(t, multi.Breaker)
	assert.Nil(t, multi.Capacity)
	require.NotNil(t, multi.Cost)
	assert.Equal(t, 0.5, multi.Cost.Cost)

	routinghotcache.SetCapacityCooldownForTest(multiAggregate, routinghotcache.CapacityCooldownSnapshot{SourceStatusCode: http.StatusPaymentRequired, CooldownUntilUnixMilli: now.Add(2 * time.Minute).UnixMilli(), UpdatedUnixMilli: now.Add(time.Second).UnixMilli()})
	memoized, err := smartRoutingCandidatesForGroup(param, "default")
	require.NoError(t, err)
	for _, candidate := range memoized {
		if candidate.Channel.Id == 142 {
			assert.Nil(t, candidate.Capacity)
		}
	}
}

func TestRoutingSelectorSettingsPrefersTTFTOnlyForStreamingContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	streamCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(streamCtx, constant.ContextKeyIsStream, true)
	nonStreamCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(nonStreamCtx, constant.ContextKeyIsStream, false)

	tests := []struct {
		name string
		ctx  *gin.Context
		want bool
	}{
		{name: "stream", ctx: streamCtx, want: true},
		{name: "non-stream", ctx: nonStreamCtx, want: false},
		{name: "nil context", ctx: nil, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := routingSelectorSettings(smart_routing_setting.SmartRoutingSetting{}, test.ctx)
			assert.Equal(t, test.want, settings.PreferTTFT)
		})
	}
}

func TestSelectSmartChannelForGroupCapacityCooldownBlocksHalfOpenProbeAndRestoresAtDeadline(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 151, Name: "capacity-half-open", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 151, Enabled: true, Priority: &priority, Weight: weight}).Error)
	model.InitChannelCache()

	cacheKey := routinghotcache.Key{ChannelID: 151, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	breakerKey := routingbreaker.Key{ChannelID: 151, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	routinghotcache.SetMetricForTest(cacheKey, routinghotcache.MetricSnapshot{
		RequestCount:            100,
		SuccessCount:            99,
		ReliabilityRequestCount: 100,
		ReliabilityFailureCount: 1,
		P95LatencyMs:            100,
		TPS:                     10,
	})
	routinghotcache.SetBreakerForTest(cacheKey, routinghotcache.BreakerSnapshot{State: routingselector.BreakerStateHalfOpen, UpdatedUnix: common.GetTimestamp()})
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{Key: breakerKey, State: routingbreaker.StateHalfOpen, EjectionCount: 1, UpdatedAt: time.Now()}})
	now := time.Now()
	routinghotcache.SetCapacityCooldownForTest(cacheKey, routinghotcache.CapacityCooldownSnapshot{
		SourceStatusCode:       429,
		CooldownUntilUnixMilli: now.Add(time.Minute).UnixMilli(),
		UpdatedUnixMilli:       now.UnixMilli(),
	})
	setting := smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeBalanced,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
		HalfOpenProbes:     1,
		MaxEjectedPct:      100,
		SnapshotStaleSec:   300,
	}

	activeCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	channel, err := selectSmartChannelForGroup(&RetryParam{Ctx: activeCtx, TokenGroup: "default", ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0)}, "default", setting, true)
	require.NoError(t, err)
	assert.Nil(t, channel)
	probes, ok := common.GetContextKeyType[map[routingbreaker.Key]struct{}](activeCtx, constant.ContextKeyRoutingHalfOpenProbes)
	assert.False(t, ok)
	assert.Empty(t, probes)

	routinghotcache.ClearCapacityCooldown(cacheKey)
	routinghotcache.SetCapacityCooldownForTest(cacheKey, routinghotcache.CapacityCooldownSnapshot{
		SourceStatusCode:       429,
		CooldownUntilUnixMilli: time.Now().UnixMilli(),
		UpdatedUnixMilli:       time.Now().UnixMilli(),
	})
	recoveredCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	channel, err = selectSmartChannelForGroup(&RetryParam{Ctx: recoveredCtx, TokenGroup: "default", ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0)}, "default", setting, true)
	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 151, channel.Id)
	ReleaseAllRoutingHalfOpenProbes(recoveredCtx)
}

func TestSelectSmartChannelForGroupRefreshesCapacityOnMemoizedRetry(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	priority := int64(10)
	weight := uint(10)
	for _, channel := range []model.Channel{
		{Id: 152, Name: "first", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight},
		{Id: 153, Name: "cooling-retry", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight},
	} {
		channel := channel
		require.NoError(t, model.DB.Create(&channel).Error)
		require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: channel.Id, Enabled: true, Priority: &priority, Weight: weight}).Error)
	}
	model.InitChannelCache()

	firstKey := routinghotcache.Key{ChannelID: 152, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	retryKey := routinghotcache.Key{ChannelID: 153, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	routinghotcache.SetMetricForTest(firstKey, routinghotcache.MetricSnapshot{ReliabilityRequestCount: 100, ReliabilityFailureCount: 0})
	routinghotcache.SetMetricForTest(retryKey, routinghotcache.MetricSnapshot{ReliabilityRequestCount: 100, ReliabilityFailureCount: 10})
	routinghotcache.SetBreakerForTest(retryKey, routinghotcache.BreakerSnapshot{State: routingselector.BreakerStateHalfOpen, UpdatedUnix: common.GetTimestamp()})
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:       routingbreaker.Key{ChannelID: 153, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"},
		State:     routingbreaker.StateHalfOpen,
		UpdatedAt: time.Now(),
	}})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	param := &RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0)}
	setting := smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeBalanced,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
		HalfOpenProbes:     1,
		MaxEjectedPct:      100,
		MaxSwitches:        5,
	}

	selected, err := selectSmartChannelForGroup(param, "default", setting, true)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, 152, selected.Id)
	MarkRoutingTried(ctx, selected.Id)
	routinghotcache.SetCapacityCooldownForTest(retryKey, routinghotcache.CapacityCooldownSnapshot{
		SourceStatusCode:       http.StatusTooManyRequests,
		CooldownUntilUnixMilli: time.Now().Add(time.Minute).UnixMilli(),
		UpdatedUnixMilli:       time.Now().UnixMilli(),
	})

	selected, err = selectSmartChannelForGroup(param, "default", setting, true)
	require.NoError(t, err)
	assert.Nil(t, selected)
	probes, ok := common.GetContextKeyType[map[routingbreaker.Key]struct{}](ctx, constant.ContextKeyRoutingHalfOpenProbes)
	assert.False(t, ok)
	assert.Empty(t, probes)
}

func TestCacheGetRandomSatisfiedChannelUsesLegacyWhenSmartRoutingObserves(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-observe-audit-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		common.CryptoSecret = previousSecret
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	highPriority := int64(100)
	lowPriority := int64(1)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 201, Name: "legacy-priority", Key: "key-201", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &highPriority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 202, Name: "smart-score", Key: "key-202", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &lowPriority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 201, Enabled: true, Priority: &highPriority, Weight: weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 202, Enabled: true, Priority: &lowPriority, Weight: weight}).Error)
	model.InitChannelCache()
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	_, err = channelrouting.RefreshSnapshotContext(context.Background())
	require.NoError(t, err)

	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID:   202,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       "gpt-test",
		Group:       "default",
	}, routinghotcache.MetricSnapshot{RequestCount: 100, SuccessCount: 100, ReliabilityRequestCount: 100, ReliabilityFailureCount: 0, P95LatencyMs: 50, TPS: 10})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeObserve,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	channel, group, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:         ctx,
		TokenGroup:  "default",
		ModelName:   "gpt-test",
		RequestPath: "/v1/chat/completions",
		Retry:       common.GetPointer(0),
	})

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, "default", group)
	assert.Equal(t, 201, channel.Id)
	decision, ok := common.GetContextKeyType[routingselector.Decision](ctx, constant.ContextKeyRoutingLastDecision)
	require.True(t, ok)
	require.NotNil(t, decision.Selected)
	assert.NotEmpty(t, decision.Ranked)

	flushed, err := channelrouting.FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	var audits []model.RoutingDecisionAudit
	require.NoError(t, model.DB.Find(&audits).Error)
	require.Len(t, audits, 1)
	assert.Equal(t, 201, audits[0].ActualChannelID)
	assert.Equal(t, decision.Selected.Channel.Id, audits[0].ObservedChannelID)
	assert.NotZero(t, audits[0].PoolID)
	assert.NotZero(t, audits[0].SnapshotRevision)
}

func TestObserveCandidatesUseStableMultiKeyTelemetryWithoutLegacyAggregate(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.Exec("DELETE FROM routing_topology_metadata").Error)
	routinghotcache.ResetForTest()
	routingmetrics.ResetForTest()
	smart_routing_setting.ResetForTest()
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true, Mode: smart_routing_setting.ModeObserve, MinVolume: 1,
	})
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	previousSecret := common.CryptoSecret
	common.CryptoSecret = "stable-observe-multikey-secret"
	t.Setenv("CRYPTO_SECRET", common.CryptoSecret)
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		common.CryptoSecret = previousSecret
		routinghotcache.ResetForTest()
		routingmetrics.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 203, Name: "multi-observe", Key: "key-a\nkey-b", Status: common.ChannelStatusEnabled,
		Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true},
	}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: "default", Model: "gpt-test", ChannelId: 203, Enabled: true, Priority: &priority, Weight: weight,
	}).Error)
	model.InitChannelCache()
	_, err := model.ReconcileLegacyRoutingTopologyContext(context.Background())
	require.NoError(t, err)
	first, err := channelrouting.RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	member := first.Pools[0].Members[0]
	require.Len(t, member.CredentialIDs, 2)
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: member.PoolID, PoolMemberID: member.ID, CredentialID: member.CredentialIDs[0], ChannelID: 203,
		Model: "gpt-test", BucketTs: time.Now().Unix(), LastSnapshotRevision: first.Revision,
		RequestCount: 10, SuccessCount: 9, FailureCount: 1,
		ReliabilityRequestCount: 10, ReliabilityFailureCount: 1,
		OutputTokens: 500, GenerationMs: 2000,
	}})
	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID: 203, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default",
	}, routinghotcache.MetricSnapshot{RequestCount: 999, SuccessCount: 999, TPS: 999})
	_, err = channelrouting.RefreshSnapshotContext(context.Background())
	require.NoError(t, err)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	candidates, err := smartRoutingCandidatesForGroup(&RetryParam{
		Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	}, "default")
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.NotNil(t, candidates[0].Metric)
	assert.Equal(t, int64(10), candidates[0].Metric.RequestCount)
	assert.Equal(t, int64(9), candidates[0].Metric.SuccessCount)
	assert.Equal(t, float64(250), candidates[0].Metric.TPS)
	assert.Nil(t, candidates[0].Breaker)
	assert.Nil(t, candidates[0].Capacity)

	balancedSetting := smart_routing_setting.GetSetting()
	balancedSetting.Mode = smart_routing_setting.ModeBalanced
	smart_routing_setting.UpdateSetting(balancedSetting)
	balancedCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	balancedCandidates, err := smartRoutingCandidatesForGroup(&RetryParam{
		Ctx: balancedCtx, TokenGroup: "default", ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0),
	}, "default")
	require.NoError(t, err)
	require.Len(t, balancedCandidates, 1)
	assert.Nil(t, balancedCandidates[0].Metric)
}

func TestCacheGetRandomSatisfiedChannelDoesNotFallbackWhenSmartSafetyFiltersEmptyPool(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(100)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 211, Name: "legacy-a", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 212, Name: "legacy-b", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 211, Enabled: true, Priority: &priority, Weight: weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 212, Enabled: true, Priority: &priority, Weight: weight}).Error)
	model.InitChannelCache()

	for _, channelID := range []int{211, 212} {
		routinghotcache.SetMetricForTest(routinghotcache.Key{
			ChannelID:   channelID,
			APIKeyIndex: model.RoutingMetricSingleKeyIndex,
			Model:       "gpt-test",
			Group:       "default",
		}, routinghotcache.MetricSnapshot{RequestCount: 100, SuccessCount: 10, ReliabilityRequestCount: 100, ReliabilityFailureCount: 90, P95LatencyMs: 100, TPS: 10})
	}
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeBalanced,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
		AvailabilityFloor:  0.95,
		MaxEjectedPct:      100,
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	channel, group, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:         ctx,
		TokenGroup:  "default",
		ModelName:   "gpt-test",
		RequestPath: "/v1/chat/completions",
		Retry:       common.GetPointer(0),
	})

	require.NoError(t, err)
	require.Nil(t, channel)
	assert.Equal(t, "default", group)
}

func TestSmartRoutingFallsBackToLegacyWhenMemoryCacheDisabled(t *testing.T) {
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
	})

	assert.False(t, shouldActivateSmartRouting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeBalanced,
	}))
	assert.False(t, shouldObserveSmartRouting(smart_routing_setting.SmartRoutingSetting{
		Enabled: true,
		Mode:    smart_routing_setting.ModeObserve,
	}))
}

func TestSmartRoutingCandidatesIgnoreLegacyAuthMarkerAndRetainBalanceMarker(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(10)
	weight := uint(10)
	for _, channel := range []model.Channel{
		{Id: 301, Name: "authfail", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight, ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
		{Id: 302, Name: "low-balance", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight, ChannelInfo: model.ChannelInfo{IsMultiKey: true}},
		{Id: 303, Name: "healthy", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight},
	} {
		channel := channel
		require.NoError(t, model.DB.Create(&channel).Error)
		require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: channel.Id, Enabled: true, Priority: &priority, Weight: weight}).Error)
	}
	model.InitChannelCache()

	now := common.GetTimestamp()
	routinghotcache.SetAuthFailureForTest(301, routinghotcache.HealthMarker{Marked: true, UpdatedUnix: now})
	routinghotcache.SetBalanceForTest(302, routinghotcache.BalanceSnapshot{Known: true, Balance: 0.25, UpdatedUnix: now})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:          true,
		Mode:             smart_routing_setting.ModeBalanced,
		SnapshotStaleSec: 300,
		BalanceMarginUSD: 1,
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	param := &RetryParam{
		Ctx:         ctx,
		TokenGroup:  "default",
		ModelName:   "gpt-test",
		RequestPath: "/v1/chat/completions",
		Retry:       common.GetPointer(0),
	}

	candidates, err := smartRoutingCandidatesForGroup(param, "default")

	require.NoError(t, err)
	require.Len(t, candidates, 3)
	byChannelID := make(map[int]routingselector.Candidate, len(candidates))
	for _, candidate := range candidates {
		byChannelID[candidate.Channel.Id] = candidate
	}
	assert.Nil(t, byChannelID[301].Breaker)
	require.NotNil(t, byChannelID[302].Breaker)
	assert.Equal(t, routingselector.BreakerReasonBalance, byChannelID[302].Breaker.Reason)
	assert.Nil(t, byChannelID[303].Breaker)
}

func TestSelectSmartChannelForGroupReservesHalfOpenProbe(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 311, Name: "half-open", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 311, Enabled: true, Priority: &priority, Weight: weight}).Error)
	model.InitChannelCache()

	now := time.Now()
	breakerKey := routingbreaker.Key{ChannelID: 311, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:           breakerKey,
		State:         routingbreaker.StateHalfOpen,
		EjectionCount: 1,
		UpdatedAt:     now,
	}})
	cacheKey := routinghotcache.Key{ChannelID: 311, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	routinghotcache.SetMetricForTest(cacheKey, routinghotcache.MetricSnapshot{RequestCount: 100, SuccessCount: 99, ReliabilityRequestCount: 100, ReliabilityFailureCount: 1, P95LatencyMs: 100, TPS: 10})
	routinghotcache.SetBreakerForTest(cacheKey, routinghotcache.BreakerSnapshot{State: routingselector.BreakerStateHalfOpen, UpdatedUnix: common.GetTimestamp()})
	setting := smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeBalanced,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
		HalfOpenProbes:     1,
		MaxEjectedPct:      100,
		SnapshotStaleSec:   300,
	}

	firstCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	first, err := selectSmartChannelForGroup(&RetryParam{Ctx: firstCtx, TokenGroup: "default", ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0)}, "default", setting, true)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, 311, first.Id)
	cached, ok := routinghotcache.GetBreaker(cacheKey)
	require.True(t, ok)
	assert.Equal(t, int64(1), cached.HalfOpenInflight)

	secondCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	second, err := selectSmartChannelForGroup(&RetryParam{Ctx: secondCtx, TokenGroup: "default", ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0)}, "default", setting, true)
	require.NoError(t, err)
	assert.Nil(t, second)
}

func TestSelectSmartChannelForGroupFallsBackToLocalHalfOpenProbeWhenRedisLeaseFails(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	previousMemoryCache := common.MemoryCacheEnabled
	previousRedisEnabled := common.RedisEnabled
	previousRedis := common.RDB
	common.MemoryCacheEnabled = true
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, errors.New("redis unavailable")
		},
		MaxRetries:      -1,
		MinRetryBackoff: -1,
		MaxRetryBackoff: -1,
	})
	t.Cleanup(func() {
		_ = common.RDB.Close()
		common.RDB = previousRedis
		common.RedisEnabled = previousRedisEnabled
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 312, Name: "half-open-local", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 312, Enabled: true, Priority: &priority, Weight: weight}).Error)
	model.InitChannelCache()

	breakerKey := routingbreaker.Key{ChannelID: 312, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:           breakerKey,
		State:         routingbreaker.StateHalfOpen,
		EjectionCount: 1,
		UpdatedAt:     time.Now(),
	}})
	cacheKey := routinghotcache.Key{ChannelID: 312, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	routinghotcache.SetMetricForTest(cacheKey, routinghotcache.MetricSnapshot{RequestCount: 100, SuccessCount: 99, ReliabilityRequestCount: 100, ReliabilityFailureCount: 1, P95LatencyMs: 100, TPS: 10})
	routinghotcache.SetBreakerForTest(cacheKey, routinghotcache.BreakerSnapshot{State: routingselector.BreakerStateHalfOpen, UpdatedUnix: common.GetTimestamp()})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	channel, err := selectSmartChannelForGroup(&RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0)}, "default", smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeBalanced,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
		HalfOpenProbes:     1,
		MaxEjectedPct:      100,
		SnapshotStaleSec:   300,
	}, true)

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 312, channel.Id)
	cached, ok := routinghotcache.GetBreaker(cacheKey)
	require.True(t, ok)
	assert.Equal(t, int64(1), cached.HalfOpenInflight)
}

func TestReleaseAllRoutingHalfOpenProbesReleasesReservedProbe(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	key := routingbreaker.Key{ChannelID: 313, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	now := time.Now()
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:       key,
		State:     routingbreaker.StateHalfOpen,
		UpdatedAt: now,
	}})
	_, ok := routingbreaker.AcquireDefaultHalfOpenProbe(key, 1)
	require.True(t, ok)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyRoutingHalfOpenProbes, map[routingbreaker.Key]struct{}{key: {}})
	cacheKey := routinghotcache.Key{ChannelID: 313, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	cached, ok := routinghotcache.GetBreaker(cacheKey)
	require.True(t, ok)
	require.Equal(t, int64(1), cached.HalfOpenInflight)

	ReleaseAllRoutingHalfOpenProbes(ctx)

	cached, ok = routinghotcache.GetBreaker(cacheKey)
	require.True(t, ok)
	assert.Zero(t, cached.HalfOpenInflight)
	probes, _ := common.GetContextKeyType[map[routingbreaker.Key]struct{}](ctx, constant.ContextKeyRoutingHalfOpenProbes)
	assert.Empty(t, probes)
}

func TestRecordSmartRoutingDecisionDoesNotReserveHalfOpenProbe(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
		routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	})

	priority := int64(10)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 312, Name: "observe-half-open", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 312, Enabled: true, Priority: &priority, Weight: weight}).Error)
	model.InitChannelCache()

	now := time.Now()
	routingbreaker.HydrateDefaultSnapshots([]routingbreaker.Snapshot{{
		Key:           routingbreaker.Key{ChannelID: 312, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"},
		State:         routingbreaker.StateHalfOpen,
		EjectionCount: 1,
		UpdatedAt:     now,
	}})
	cacheKey := routinghotcache.Key{ChannelID: 312, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	routinghotcache.SetMetricForTest(cacheKey, routinghotcache.MetricSnapshot{RequestCount: 100, SuccessCount: 99, ReliabilityRequestCount: 100, ReliabilityFailureCount: 1, P95LatencyMs: 100, TPS: 10})
	routinghotcache.SetBreakerForTest(cacheKey, routinghotcache.BreakerSnapshot{State: routingselector.BreakerStateHalfOpen, UpdatedUnix: common.GetTimestamp()})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	recordSmartRoutingDecision(&RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: "gpt-test", RequestPath: "/v1/chat/completions", Retry: common.GetPointer(0)}, smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeObserve,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
		HalfOpenProbes:     1,
		MaxEjectedPct:      100,
		SnapshotStaleSec:   300,
	})

	decision, ok := common.GetContextKeyType[routingselector.Decision](ctx, constant.ContextKeyRoutingLastDecision)
	require.True(t, ok)
	require.NotNil(t, decision.Selected)
	assert.Equal(t, 312, decision.Selected.Channel.Id)
	cached, ok := routinghotcache.GetBreaker(cacheKey)
	require.True(t, ok)
	assert.Zero(t, cached.HalfOpenInflight)
	_, ok = common.GetContextKeyType[map[int]routingbreaker.Key](ctx, constant.ContextKeyRoutingHalfOpenProbes)
	assert.False(t, ok)
}

func TestRecordSmartRoutingDecisionClearsStaleEvaluationWhenNoCandidates(t *testing.T) {
	truncate(t)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyRoutingObserveDecision, smartRoutingObserveEvaluation{
		Group: "stale-group",
	})

	recordSmartRoutingDecision(&RetryParam{
		Ctx: ctx, TokenGroup: "auto", ModelName: "gpt-test", Retry: common.GetPointer(0),
	}, smart_routing_setting.SmartRoutingSetting{Enabled: true, Mode: smart_routing_setting.ModeObserve})

	_, ok := common.GetContextKeyType[smartRoutingObserveEvaluation](ctx, constant.ContextKeyRoutingObserveDecision)
	assert.False(t, ok)
}

func TestObserveAuditRejectsEvaluationFromDifferentPool(t *testing.T) {
	channelrouting.ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { channelrouting.ResetDecisionAuditsForTest() })
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyRoutingObserveDecision, smartRoutingObserveEvaluation{
		Group: "pool-a",
		Decision: routingselector.Decision{
			Selected: &routingselector.RankedCandidate{Channel: &model.Channel{Id: 1}},
		},
	})

	recordChannelRoutingObserveAudit(&RetryParam{
		Ctx: ctx, ModelName: "gpt-test", Retry: common.GetPointer(0),
	}, "pool-b", &model.Channel{Id: 2}, 0)

	assert.Zero(t, channelrouting.DecisionAuditsStats().Entries)
}

func TestAffinityAdmissibleIgnoresLegacyAuthFailureAndStillFiltersLowBalance(t *testing.T) {
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	t.Cleanup(func() {
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:          true,
		Mode:             smart_routing_setting.ModeBalanced,
		SnapshotStaleSec: 300,
		BalanceMarginUSD: 1,
	})

	now := common.GetTimestamp()
	routinghotcache.SetAuthFailureForTest(401, routinghotcache.HealthMarker{Marked: true, UpdatedUnix: now})
	routinghotcache.SetBalanceForTest(402, routinghotcache.BalanceSnapshot{Known: true, Balance: 0.25, UpdatedUnix: now})
	routinghotcache.SetAuthFailureForTest(403, routinghotcache.HealthMarker{Marked: true, UpdatedUnix: now - 600})
	routinghotcache.SetBalanceForTest(404, routinghotcache.BalanceSnapshot{Known: true, Balance: 9, UpdatedUnix: now})

	assert.True(t, AffinityAdmissible(401))
	assert.False(t, AffinityAdmissible(402))
	assert.True(t, AffinityAdmissible(403))
	assert.True(t, AffinityAdmissible(404))
	assert.True(t, AffinityAdmissible(405))
}

func TestGetAdmissibleAffinityChannelRejectsPreferredFilteredBySmartRouting(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(10)
	weight := uint(10)
	for _, channel := range []model.Channel{
		{Id: 421, Name: "affinity-open", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight},
		{Id: 422, Name: "healthy", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight},
	} {
		channel := channel
		require.NoError(t, model.DB.Create(&channel).Error)
		require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: channel.Id, Enabled: true, Priority: &priority, Weight: weight}).Error)
	}
	model.InitChannelCache()
	now := common.GetTimestamp()
	routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 421, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}, routinghotcache.BreakerSnapshot{
		State:             routingselector.BreakerStateOpen,
		CooldownUntilUnix: now + 60,
		UpdatedUnix:       now,
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeBalanced,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
		MaxEjectedPct:      100,
		SnapshotStaleSec:   300,
	})
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	channel, group, ok := GetAdmissibleAffinityChannel(ctx, 421, "gpt-test", "default", "/v1/chat/completions")

	assert.False(t, ok)
	assert.Nil(t, channel)
	assert.Empty(t, group)

	channel, group, ok = GetAdmissibleAffinityChannel(ctx, 422, "gpt-test", "default", "/v1/chat/completions")
	require.True(t, ok)
	require.NotNil(t, channel)
	assert.Equal(t, 422, channel.Id)
	assert.Equal(t, "default", group)
}

func TestRoutingCostForRequestUsesPromptProxyAndEstimatedOutput(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyRoutingPromptProxy, 1000)
	common.SetContextKey(ctx, constant.ContextKeyRoutingEstimatedOutput, 200)

	cost := routingCostForRequest(ctx, routinghotcache.CostSnapshot{
		Known:           true,
		Confidence:      model.RoutingCostConfidenceFull,
		QuotaType:       0,
		GroupRatio:      1.5,
		BaseRatio:       2,
		CompletionRatio: 3,
		UpdatedUnix:     common.GetTimestamp(),
	})

	require.NotNil(t, cost)
	assert.True(t, cost.Known)
	assert.InDelta(t, 0.0096, cost.Cost, 0.000001)
}

func TestRoutingCostForRequestUsesPerRequestPrice(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	cost := routingCostForRequest(ctx, routinghotcache.CostSnapshot{
		Known:       true,
		Confidence:  model.RoutingCostConfidenceFull,
		QuotaType:   1,
		GroupRatio:  1.5,
		ModelPrice:  0.25,
		UpdatedUnix: common.GetTimestamp(),
	})

	require.NotNil(t, cost)
	assert.True(t, cost.Known)
	assert.Equal(t, 0.375, cost.Cost)
}

func TestMarkRoutingTriedTracksExcludedChannelsAndSwitchCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	assert.Equal(t, 0, MarkRoutingTried(ctx, 101))
	assert.Equal(t, 0, MarkRoutingTried(ctx, 101))
	assert.Equal(t, 1, MarkRoutingTried(ctx, 102))
	assert.Equal(t, 2, MarkRoutingTried(ctx, 103))

	excluded, ok := common.GetContextKeyType[map[int]struct{}](ctx, constant.ContextKeyRoutingExcludedChannels)
	require.True(t, ok)
	assert.Contains(t, excluded, 101)
	assert.Contains(t, excluded, 102)
	assert.Contains(t, excluded, 103)
	assert.Equal(t, 2, common.GetContextKeyInt(ctx, constant.ContextKeyRoutingSwitchCount))
}

func TestFilterSmartRoutingExcludedCandidatesHonorsSwitchLimit(t *testing.T) {
	candidates := []routingselector.Candidate{
		{Channel: &model.Channel{Id: 1}},
		{Channel: &model.Channel{Id: 2}},
		{Channel: &model.Channel{Id: 3}},
	}

	filtered := filterSmartRoutingExcludedCandidates(candidates, map[int]struct{}{1: {}}, 0, 2)
	require.Len(t, filtered, 2)
	assert.Equal(t, 2, filtered[0].Channel.Id)

	filtered = filterSmartRoutingExcludedCandidates(candidates, map[int]struct{}{1: {}, 2: {}}, 1, 2)
	require.Len(t, filtered, 1)
	assert.Equal(t, 3, filtered[0].Channel.Id)

	filtered = filterSmartRoutingExcludedCandidates(candidates, map[int]struct{}{1: {}, 2: {}, 3: {}}, 2, 2)
	assert.Empty(t, filtered)
}

func TestSelectSmartChannelForGroupDoesNotFallBackWhenSwitchLimitExhausted(t *testing.T) {
	truncate(t)
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	priority := int64(100)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 501, Name: "already-used", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 501, Enabled: true, Priority: &priority, Weight: weight}).Error)
	model.InitChannelCache()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyRoutingExcludedChannels, map[int]struct{}{501: {}})
	common.SetContextKey(ctx, constant.ContextKeyRoutingSwitchCount, 1)
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeBalanced,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
		MaxSwitches:        1,
		MaxEjectedPct:      100,
	})

	channel, _, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx:         ctx,
		TokenGroup:  "default",
		ModelName:   "gpt-test",
		RequestPath: "/v1/chat/completions",
		Retry:       common.GetPointer(1),
	})

	require.NoError(t, err)
	assert.Nil(t, channel)
}
