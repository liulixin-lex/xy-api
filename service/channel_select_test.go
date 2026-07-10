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
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})

	highPriority := int64(100)
	lowPriority := int64(1)
	weight := uint(10)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 201, Name: "legacy-priority", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &highPriority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Channel{Id: 202, Name: "smart-score", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &lowPriority, Weight: &weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 201, Enabled: true, Priority: &highPriority, Weight: weight}).Error)
	require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "gpt-test", ChannelId: 202, Enabled: true, Priority: &lowPriority, Weight: weight}).Error)
	model.InitChannelCache()

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

func TestSelectSmartChannelForGroupHardFiltersFreshAuthFailureAndLowBalance(t *testing.T) {
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
		{Id: 301, Name: "authfail", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight},
		{Id: 302, Name: "low-balance", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-test", Priority: &priority, Weight: &weight},
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

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	param := &RetryParam{
		Ctx:         ctx,
		TokenGroup:  "default",
		ModelName:   "gpt-test",
		RequestPath: "/v1/chat/completions",
		Retry:       common.GetPointer(0),
	}

	channel, err := selectSmartChannelForGroup(param, "default", smart_routing_setting.SmartRoutingSetting{
		Enabled:            true,
		Mode:               smart_routing_setting.ModeBalanced,
		WeightAvailability: 1,
		TopK:               1,
		MinVolume:          10,
		MaxEjectedPct:      100,
		SnapshotStaleSec:   300,
		BalanceMarginUSD:   1,
	}, true)

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 303, channel.Id)
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

func TestAffinityAdmissibleHardFiltersOnlyFreshAuthFailureAndLowBalance(t *testing.T) {
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

	assert.False(t, AffinityAdmissible(401))
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

func TestIsMultiKeyIndexRoutingAdmissibleRejectsFreshOpenAndHardHealth(t *testing.T) {
	routinghotcache.ResetForTest()
	smart_routing_setting.ResetForTest()
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousMemoryCache
		routinghotcache.ResetForTest()
		smart_routing_setting.ResetForTest()
	})
	smart_routing_setting.UpdateSetting(smart_routing_setting.SmartRoutingSetting{
		Enabled:          true,
		Mode:             smart_routing_setting.ModeBalanced,
		SnapshotStaleSec: 300,
		BalanceMarginUSD: 1,
		HalfOpenProbes:   1,
	})
	now := common.GetTimestamp()

	routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 411, APIKeyIndex: 0, Model: "gpt-test", Group: "vip"}, routinghotcache.BreakerSnapshot{
		State:             routingselector.BreakerStateOpen,
		CooldownUntilUnix: now + 60,
		UpdatedUnix:       now,
	})
	routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 411, APIKeyIndex: 1, Model: "gpt-test", Group: "vip"}, routinghotcache.BreakerSnapshot{
		State:             routingselector.BreakerStateOpen,
		CooldownUntilUnix: now - 1,
		UpdatedUnix:       now,
	})
	routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 411, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"}, routinghotcache.BreakerSnapshot{
		State:       routingselector.BreakerStateOpen,
		Reason:      routingselector.BreakerReasonAuthFail,
		UpdatedUnix: now,
	})
	routinghotcache.SetBreakerForTest(routinghotcache.Key{ChannelID: 411, APIKeyIndex: 3, Model: "gpt-test", Group: "vip"}, routinghotcache.BreakerSnapshot{
		State:       routingselector.BreakerStateOpen,
		UpdatedUnix: now - 600,
	})

	assert.False(t, IsMultiKeyIndexRoutingAdmissible(nil, 411, 0, "gpt-test", "vip"))
	assert.True(t, IsMultiKeyIndexRoutingAdmissible(nil, 411, 1, "gpt-test", "vip"))
	assert.False(t, IsMultiKeyIndexRoutingAdmissible(nil, 411, 2, "gpt-test", "vip"))
	assert.True(t, IsMultiKeyIndexRoutingAdmissible(nil, 411, 3, "gpt-test", "vip"))

	routinghotcache.SetAuthFailureForTest(412, routinghotcache.HealthMarker{Marked: true, UpdatedUnix: now})
	routinghotcache.SetBalanceForTest(413, routinghotcache.BalanceSnapshot{Known: true, Balance: 0.25, UpdatedUnix: now})
	assert.False(t, IsMultiKeyIndexRoutingAdmissible(nil, 412, 0, "gpt-test", "vip"))
	assert.False(t, IsMultiKeyIndexRoutingAdmissible(nil, 413, 0, "gpt-test", "vip"))
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
