package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectSmartChannelForGroupUsesHotcacheScoreWithinPriority(t *testing.T) {
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
	}, routinghotcache.MetricSnapshot{RequestCount: 100, SuccessCount: 10, P95LatencyMs: 900, TPS: 1})
	routinghotcache.SetMetricForTest(routinghotcache.Key{
		ChannelID:   102,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       "gpt-test",
		Group:       "default",
	}, routinghotcache.MetricSnapshot{RequestCount: 100, SuccessCount: 99, P95LatencyMs: 100, TPS: 10})

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
		WeightLatency:      1,
		WeightThroughput:   1,
		TopK:               1,
		MinVolume:          10,
		MaxEjectedPct:      100,
		SnapshotStaleSec:   300,
	}

	channel, err := selectSmartChannelForGroup(param, "default", setting)

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 102, channel.Id)
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
	}, routinghotcache.MetricSnapshot{RequestCount: 100, SuccessCount: 100, P95LatencyMs: 50, TPS: 10})
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

func TestCacheGetRandomSatisfiedChannelFallsBackLegacyWhenSmartRankingEmpty(t *testing.T) {
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
		}, routinghotcache.MetricSnapshot{RequestCount: 100, SuccessCount: 10, P95LatencyMs: 100, TPS: 10})
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
	require.NotNil(t, channel)
	assert.Equal(t, "default", group)
	assert.Contains(t, []int{211, 212}, channel.Id)
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
	})

	require.NoError(t, err)
	require.NotNil(t, channel)
	assert.Equal(t, 303, channel.Id)
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
