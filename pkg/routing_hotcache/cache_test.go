package routinghotcache

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHotcacheStoresMetricCostAndBreakerSnapshots(t *testing.T) {
	ResetForTest()
	key := Key{ChannelID: 12, APIKeyIndex: -1, Model: "gpt-test", Group: "default"}

	SetMetricForTest(key, MetricSnapshot{RequestCount: 100, SuccessCount: 99, P95LatencyMs: 350, TPS: 42})
	SetCostForTest(key.CostKey(), CostSnapshot{Known: true, Cost: 0.25, Confidence: "full", UpdatedUnix: 1000})
	SetBreakerForTest(key, BreakerSnapshot{State: "degraded", Reason: "5xx", UpdatedUnix: 1000})

	metric, ok := GetMetric(key)
	assert.True(t, ok)
	assert.Equal(t, int64(100), metric.RequestCount)
	assert.Equal(t, float64(350), metric.P95LatencyMs)

	cost, ok := GetCost(key.CostKey())
	assert.True(t, ok)
	assert.True(t, cost.Known)
	assert.Equal(t, 0.25, cost.Cost)

	breaker, ok := GetBreaker(key)
	assert.True(t, ok)
	assert.Equal(t, "degraded", breaker.State)
}

func TestHotcacheLoadsVersionedNormalizedPricing(t *testing.T) {
	ResetForTest()
	pricingJSON := `{"quota_type":0,"billing_mode":"token","currency":"USD","unit":"million_tokens","input_cost_per_million":2,"output_cost_per_million":10,"tiers":{},"extras":{}}`
	LoadCostSnapshots([]model.RoutingCostSnapshot{{
		AccountID: 7, ChannelID: 12, ModelName: "gpt-test", SnapshotTS: 100,
		PricingHash: strings.Repeat("a", 64), PricingVersion: "provider-v1", PricingJSON: &pricingJSON,
		ObservedTime: 100, EffectiveTime: 90, ExpiresTime: 200,
		VersionConfidence: model.RoutingCostConfidenceExact, ConfidenceScore: 1,
		Freshness: model.RoutingCostFreshnessFresh, FreshnessScore: 1,
		AccountSourceType: model.RoutingUpstreamTypeNewAPI, AccountKeyHash: strings.Repeat("b", 64),
	}})

	cost, ok := GetCost(CostKey{ChannelID: 12, Model: "gpt-test"})
	require.True(t, ok)
	assert.True(t, cost.PricingKnown)
	assert.Equal(t, "token", cost.Pricing.BillingMode)
	assert.Equal(t, "million_tokens", cost.Pricing.Unit)
	assert.Equal(t, strings.Repeat("a", 64), cost.PricingHash)
	assert.Equal(t, strings.Repeat("b", 64), cost.AccountKeyHash)
}

func TestHotcacheResetClearsSnapshots(t *testing.T) {
	key := Key{ChannelID: 12, APIKeyIndex: -1, Model: "gpt-test", Group: "default"}
	SetMetricForTest(key, MetricSnapshot{RequestCount: 1})
	ResetForTest()

	_, ok := GetMetric(key)
	assert.False(t, ok)
}

func TestHotcachePruneRemovesStaleSnapshotsAcrossAllMaps(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	oldKey := Key{ChannelID: 1, APIKeyIndex: -1, Model: "old", Group: "default"}
	newKey := Key{ChannelID: 2, APIKeyIndex: -1, Model: "new", Group: "default"}
	SetMetricForTest(oldKey, MetricSnapshot{UpdatedUnix: 100})
	SetMetricForTest(newKey, MetricSnapshot{UpdatedUnix: 200})
	SetCostForTest(oldKey.CostKey(), CostSnapshot{UpdatedUnix: 100})
	SetCostForTest(newKey.CostKey(), CostSnapshot{UpdatedUnix: 200})
	SetBreakerForTest(oldKey, BreakerSnapshot{UpdatedUnix: 100})
	SetBreakerForTest(newKey, BreakerSnapshot{UpdatedUnix: 200})
	SetAuthFailureForTest(1, HealthMarker{Marked: true, UpdatedUnix: 100})
	SetAuthFailureForTest(2, HealthMarker{Marked: true, UpdatedUnix: 200})
	SetBalanceForTest(1, BalanceSnapshot{Known: true, UpdatedUnix: 100})
	SetBalanceForTest(2, BalanceSnapshot{Known: true, UpdatedUnix: 200})

	deleted := Prune(300, 150)

	assert.Equal(t, 5, deleted)
	assert.Equal(t, Stats{
		Metrics:      1,
		Costs:        1,
		Breakers:     1,
		AuthFailures: 1,
		Balances:     1,
		Evictions:    5,
	}, RuntimeStats())
	_, ok := GetMetric(oldKey)
	assert.False(t, ok)
	_, ok = GetMetric(newKey)
	assert.True(t, ok)
	_, ok = GetCost(oldKey.CostKey())
	assert.False(t, ok)
	_, ok = GetCost(newKey.CostKey())
	assert.True(t, ok)
	_, ok = GetBreaker(oldKey)
	assert.False(t, ok)
	_, ok = GetBreaker(newKey)
	assert.True(t, ok)
	_, ok = GetAuthFailure(1)
	assert.False(t, ok)
	_, ok = GetAuthFailure(2)
	assert.True(t, ok)
	_, ok = GetBalance(1)
	assert.False(t, ok)
	_, ok = GetBalance(2)
	assert.True(t, ok)
}

func TestHotcacheSetSnapshotsRespectHardLimitsAndKeepNewest(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	cache.Lock()
	cache.limits = Limits{MaxMetrics: 2, MaxCosts: 2, MaxBreakers: 2, MaxHealth: 2}
	cache.Unlock()

	metricKeys := []Key{
		{ChannelID: 10, APIKeyIndex: -1, Model: "metric-old", Group: "default"},
		{ChannelID: 11, APIKeyIndex: -1, Model: "metric-newest", Group: "default"},
		{ChannelID: 12, APIKeyIndex: -1, Model: "metric-new", Group: "default"},
	}
	SetMetricForTest(metricKeys[0], MetricSnapshot{UpdatedUnix: 10})
	SetMetricForTest(metricKeys[1], MetricSnapshot{UpdatedUnix: 30})
	SetMetricForTest(metricKeys[2], MetricSnapshot{UpdatedUnix: 20})

	costKeys := []CostKey{{ChannelID: 20, Model: "old"}, {ChannelID: 21, Model: "newest"}, {ChannelID: 22, Model: "new"}}
	SetCostForTest(costKeys[0], CostSnapshot{UpdatedUnix: 10})
	SetCostForTest(costKeys[1], CostSnapshot{UpdatedUnix: 30})
	SetCostForTest(costKeys[2], CostSnapshot{UpdatedUnix: 20})

	breakerKeys := []Key{
		{ChannelID: 30, APIKeyIndex: -1, Model: "breaker-old", Group: "default"},
		{ChannelID: 31, APIKeyIndex: -1, Model: "breaker-newest", Group: "default"},
		{ChannelID: 32, APIKeyIndex: -1, Model: "breaker-new", Group: "default"},
	}
	SetBreakerForTest(breakerKeys[0], BreakerSnapshot{UpdatedUnix: 10})
	SetBreakerForTest(breakerKeys[1], BreakerSnapshot{UpdatedUnix: 30})
	SetBreakerForTest(breakerKeys[2], BreakerSnapshot{UpdatedUnix: 20})

	SetAuthFailureForTest(40, HealthMarker{Marked: true, UpdatedUnix: 10})
	SetAuthFailureForTest(41, HealthMarker{Marked: true, UpdatedUnix: 30})
	SetAuthFailureForTest(42, HealthMarker{Marked: true, UpdatedUnix: 20})
	SetBalanceForTest(50, BalanceSnapshot{Known: true, UpdatedUnix: 10})
	SetBalanceForTest(51, BalanceSnapshot{Known: true, UpdatedUnix: 30})
	SetBalanceForTest(52, BalanceSnapshot{Known: true, UpdatedUnix: 20})

	assert.Equal(t, Stats{
		Metrics:      2,
		Costs:        2,
		Breakers:     2,
		AuthFailures: 2,
		Balances:     2,
		Evictions:    5,
	}, RuntimeStats())
	_, ok := GetMetric(metricKeys[0])
	assert.False(t, ok)
	_, ok = GetMetric(metricKeys[1])
	assert.True(t, ok)
	_, ok = GetMetric(metricKeys[2])
	assert.True(t, ok)
	_, ok = GetCost(costKeys[0])
	assert.False(t, ok)
	_, ok = GetBreaker(breakerKeys[0])
	assert.False(t, ok)
	_, ok = GetAuthFailure(40)
	assert.False(t, ok)
	_, ok = GetBalance(50)
	assert.False(t, ok)
}

func TestHotcacheCapacityEvictionUsesStableKeyOrder(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	cache.Lock()
	cache.limits = Limits{MaxMetrics: 2, MaxCosts: 2, MaxBreakers: 2, MaxHealth: 2}
	cache.Unlock()

	for _, channelID := range []int{3, 1, 2} {
		SetMetricForTest(Key{ChannelID: channelID, APIKeyIndex: -1, Model: "same", Group: "default"}, MetricSnapshot{UpdatedUnix: 100})
		SetCostForTest(CostKey{ChannelID: channelID, Model: "same"}, CostSnapshot{UpdatedUnix: 100})
		SetAuthFailureForTest(channelID, HealthMarker{Marked: true, UpdatedUnix: 100})
	}

	_, metricOne := GetMetric(Key{ChannelID: 1, APIKeyIndex: -1, Model: "same", Group: "default"})
	_, metricTwo := GetMetric(Key{ChannelID: 2, APIKeyIndex: -1, Model: "same", Group: "default"})
	_, metricThree := GetMetric(Key{ChannelID: 3, APIKeyIndex: -1, Model: "same", Group: "default"})
	assert.False(t, metricOne)
	assert.True(t, metricTwo)
	assert.True(t, metricThree)
	_, costOne := GetCost(CostKey{ChannelID: 1, Model: "same"})
	_, costTwo := GetCost(CostKey{ChannelID: 2, Model: "same"})
	_, costThree := GetCost(CostKey{ChannelID: 3, Model: "same"})
	assert.False(t, costOne)
	assert.True(t, costTwo)
	assert.True(t, costThree)
	_, authOne := GetAuthFailure(1)
	_, authTwo := GetAuthFailure(2)
	_, authThree := GetAuthFailure(3)
	assert.False(t, authOne)
	assert.True(t, authTwo)
	assert.True(t, authThree)
	assert.Equal(t, int64(3), RuntimeStats().Evictions)
}

func TestLoadMetricSnapshotsBuildsSelectorMetric(t *testing.T) {
	ResetForTest()
	LoadMetricSnapshots([]model.RoutingChannelMetric{{
		ChannelID:               77,
		APIKeyIndex:             model.RoutingMetricSingleKeyIndex,
		ModelName:               "gpt-test",
		Group:                   "default",
		BucketTs:                120,
		RequestCount:            4,
		SuccessCount:            3,
		ReliabilityRequestCount: 3,
		ReliabilityFailureCount: 1,
		TotalLatencyMs:          800,
		LatencyP95Ms:            750,
		TtftCount:               4,
		TtftP95Ms:               120,
		OutputTokens:            120,
		GenerationMs:            2000,
	}}, 60)

	metric, ok := GetMetric(Key{ChannelID: 77, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, int64(4), metric.RequestCount)
	assert.Equal(t, int64(3), metric.SuccessCount)
	assert.Equal(t, int64(3), metric.ReliabilityRequestCount)
	assert.Equal(t, int64(1), metric.ReliabilityFailureCount)
	assert.Equal(t, 750.0, metric.P95LatencyMs)
	assert.Equal(t, 120.0, metric.P95TTFTMs)
	assert.Equal(t, int64(120), metric.OutputTokens)
	assert.Equal(t, int64(2000), metric.GenerationMs)
	assert.Equal(t, 60.0, metric.TPS)
	assert.Equal(t, int64(120), metric.UpdatedUnix)
}

func TestApplyMetricDeltasRecomputesTPSFromSameBucketTotals(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	key := Key{ChannelID: 79, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	ApplyMetricDeltas([]model.RoutingChannelMetric{
		{ChannelID: 79, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "default", BucketTs: 120, RequestCount: 1, OutputTokens: 100, GenerationMs: 1000},
		{ChannelID: 79, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "default", BucketTs: 120, RequestCount: 1, OutputTokens: 50, GenerationMs: 500},
	}, 60)

	metric, ok := GetMetric(key)
	require.True(t, ok)
	assert.Equal(t, int64(150), metric.OutputTokens)
	assert.Equal(t, int64(1500), metric.GenerationMs)
	assert.Equal(t, 100.0, metric.TPS)
}

func TestLoadMetricSnapshotsUsesZeroTPSForNonPositiveThroughputInputs(t *testing.T) {
	testCases := []struct {
		name         string
		outputTokens int64
		generationMs int64
	}{
		{name: "zero output tokens", outputTokens: 0, generationMs: 2000},
		{name: "zero generation time", outputTokens: 120, generationMs: 0},
		{name: "negative output tokens", outputTokens: -1, generationMs: 2000},
		{name: "negative generation time", outputTokens: 120, generationMs: -1},
	}

	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ResetForTest()
			key := Key{ChannelID: 80 + index, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
			LoadMetricSnapshots([]model.RoutingChannelMetric{{
				ChannelID:    key.ChannelID,
				APIKeyIndex:  key.APIKeyIndex,
				ModelName:    key.Model,
				Group:        key.Group,
				BucketTs:     120,
				RequestCount: 1,
				OutputTokens: testCase.outputTokens,
				GenerationMs: testCase.generationMs,
			}}, 60)

			metric, ok := GetMetric(key)
			require.True(t, ok)
			assert.Equal(t, 0.0, metric.TPS)
		})
	}
	ResetForTest()
}

func TestLoadMetricAndBreakerSnapshotsRejectPositiveAPIKeyIndexes(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	LoadMetricSnapshots([]model.RoutingChannelMetric{
		{ChannelID: 771, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "single", Group: "default", BucketTs: 120, RequestCount: 1},
		{ChannelID: 772, APIKeyIndex: 2, ModelName: "positive", Group: "default", BucketTs: 120, RequestCount: 1},
	}, 60)
	ApplyMetricDeltas([]model.RoutingChannelMetric{
		{ChannelID: 773, APIKeyIndex: 3, ModelName: "positive-delta", Group: "default", BucketTs: 120, RequestCount: 1},
	}, 60)
	LoadBreakerSnapshots([]model.RoutingBreakerState{
		{ChannelID: 771, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "single", Group: "default", State: "open", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 120},
		{ChannelID: 772, APIKeyIndex: 2, ModelName: "positive", Group: "default", State: "open", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 120},
	})

	_, singleMetric := GetMetric(Key{ChannelID: 771, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "single", Group: "default"})
	_, positiveMetric := GetMetric(Key{ChannelID: 772, APIKeyIndex: 2, Model: "positive", Group: "default"})
	_, positiveDelta := GetMetric(Key{ChannelID: 773, APIKeyIndex: 3, Model: "positive-delta", Group: "default"})
	_, singleBreaker := GetBreaker(Key{ChannelID: 771, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "single", Group: "default"})
	_, positiveBreaker := GetBreaker(Key{ChannelID: 772, APIKeyIndex: 2, Model: "positive", Group: "default"})
	assert.True(t, singleMetric)
	assert.False(t, positiveMetric)
	assert.False(t, positiveDelta)
	assert.True(t, singleBreaker)
	assert.False(t, positiveBreaker)
	assert.Equal(t, Stats{Metrics: 1, Breakers: 1}, RuntimeStats())
}

func TestLoadSnapshotsKeepsLatestMetricAndCost(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	metricKey := Key{ChannelID: 78, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	LoadMetricSnapshots([]model.RoutingChannelMetric{
		{ChannelID: 78, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "default", BucketTs: 200, RequestCount: 10, SuccessCount: 10, TotalLatencyMs: 1000, LatencyP95Ms: 100},
		{ChannelID: 78, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "default", BucketTs: 100, RequestCount: 10, SuccessCount: 1, TotalLatencyMs: 9000, LatencyP95Ms: 900},
	}, 60)
	metric, ok := GetMetric(metricKey)
	require.True(t, ok)
	assert.Equal(t, int64(200), metric.UpdatedUnix)
	assert.Equal(t, int64(10), metric.SuccessCount)
	assert.Equal(t, 100.0, metric.P95LatencyMs)
	LoadMetricSnapshots([]model.RoutingChannelMetric{{
		ChannelID: 78, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "default", BucketTs: 200, RequestCount: 10, SuccessCount: 0, TotalLatencyMs: 5000, LatencyP95Ms: 500,
	}}, 60)
	metric, ok = GetMetric(metricKey)
	require.True(t, ok)
	assert.Equal(t, int64(10), metric.RequestCount)
	assert.Equal(t, int64(10), metric.SuccessCount)
	assert.Equal(t, 100.0, metric.P95LatencyMs)

	costKey := CostKey{ChannelID: 78, Model: "gpt-test"}
	LoadCostSnapshots([]model.RoutingCostSnapshot{
		{AccountID: 17, ChannelID: 78, ModelName: "gpt-test", GroupRatio: 2, BaseRatio: 1, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 200},
		{ChannelID: 78, ModelName: "gpt-test", GroupRatio: 9, BaseRatio: 1, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 100},
	})
	cost, ok := GetCost(costKey)
	require.True(t, ok)
	assert.Equal(t, int64(200), cost.UpdatedUnix)
	assert.Equal(t, 2.0, cost.Cost)
	assert.Equal(t, 17, cost.AccountID)
	LoadCostSnapshots([]model.RoutingCostSnapshot{
		{ChannelID: 78, ModelName: "gpt-test", GroupRatio: 5, BaseRatio: 1, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 200},
	})
	cost, ok = GetCost(costKey)
	require.True(t, ok)
	assert.Equal(t, 2.0, cost.Cost)

	breakerKey := Key{ChannelID: 78, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	LoadBreakerSnapshots([]model.RoutingBreakerState{
		{ChannelID: 78, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "default", State: "open", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 200},
		{ChannelID: 78, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "gpt-test", Group: "default", State: "healthy", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 100},
	})
	breaker, ok := GetBreaker(breakerKey)
	require.True(t, ok)
	assert.Equal(t, int64(200), breaker.UpdatedUnix)
	assert.Equal(t, "open", breaker.State)
}

func TestReconcileCostConnectorSnapshotsPreservesActiveChannelsOutsideDetailLimit(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	activeCostKey := CostKey{ChannelID: 301, Model: "active"}
	removedCostKey := CostKey{ChannelID: 302, Model: "removed"}
	SetCostForTest(activeCostKey, CostSnapshot{Known: true, Cost: 1, UpdatedUnix: 100})
	SetCostForTest(CostKey{ChannelID: 301, Model: "active-second-model"}, CostSnapshot{Known: true, Cost: 3, UpdatedUnix: 100})
	SetCostForTest(removedCostKey, CostSnapshot{Known: true, Cost: 2, UpdatedUnix: 100})
	SetBalanceForTest(301, BalanceSnapshot{Known: true, Balance: 1, UpdatedUnix: 100})
	SetBalanceForTest(302, BalanceSnapshot{Known: true, Balance: 2, UpdatedUnix: 100})
	cachedCostKeys, cachedBalanceChannels := CostConnectorCachedState()
	assert.ElementsMatch(t, []CostKey{
		activeCostKey,
		{ChannelID: 301, Model: "active-second-model"},
		removedCostKey,
	}, cachedCostKeys)
	assert.ElementsMatch(t, []int{301, 302}, cachedBalanceChannels)

	ReconcileCostConnectorSnapshots(CostConnectorReconcileSnapshot{
		CachedCostKeys: cachedCostKeys,
		CachedCosts: []model.RoutingCostSnapshot{
			{ChannelID: 301, ModelName: "active", GroupRatio: 1, BaseRatio: 2, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 101},
			{ChannelID: 301, ModelName: "active-second-model", GroupRatio: 1, BaseRatio: 3, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 101},
		},
		CachedBalanceChannels: cachedBalanceChannels,
		CachedHealth: []model.RoutingChannelHealthState{
			{ChannelID: 301, BalanceKnown: true, Balance: 3, BalanceUpdatedTime: 101},
		},
	})

	_, activeCostFound := GetCost(activeCostKey)
	_, removedCostFound := GetCost(removedCostKey)
	_, activeBalanceFound := GetBalance(301)
	_, removedBalanceFound := GetBalance(302)
	assert.True(t, activeCostFound)
	assert.False(t, removedCostFound)
	assert.True(t, activeBalanceFound)
	assert.False(t, removedBalanceFound)
}

func TestReconcileCostConnectorSnapshotsRemovesMissingModelWithinActiveChannel(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	removedKey := CostKey{ChannelID: 401, Model: "removed-model"}
	retainedKey := CostKey{ChannelID: 401, Model: "retained-model"}
	SetCostForTest(removedKey, CostSnapshot{Known: true, Cost: 1, UpdatedUnix: 100})
	SetCostForTest(retainedKey, CostSnapshot{Known: true, Cost: 1, UpdatedUnix: 100})

	cachedCostKeys, _ := CostConnectorCachedState()
	newKey := CostKey{ChannelID: 401, Model: "new-model"}
	ReconcileCostConnectorSnapshots(CostConnectorReconcileSnapshot{
		CachedCostKeys: cachedCostKeys,
		RecentCosts: []model.RoutingCostSnapshot{
			{ChannelID: 401, ModelName: "removed-model", GroupRatio: 1, BaseRatio: 9, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 101},
			{ChannelID: 401, ModelName: "new-model", GroupRatio: 1, BaseRatio: 4, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 101},
		},
		CachedCosts: []model.RoutingCostSnapshot{
			{ChannelID: 401, ModelName: "retained-model", GroupRatio: 1, BaseRatio: 2, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 101},
		},
	})

	_, removedFound := GetCost(removedKey)
	retained, retainedFound := GetCost(retainedKey)
	newCost, newFound := GetCost(newKey)
	assert.False(t, removedFound)
	require.True(t, retainedFound)
	assert.Equal(t, 2.0, retained.Cost)
	require.True(t, newFound)
	assert.Equal(t, 4.0, newCost.Cost)
}

func TestReconcileCostConnectorSnapshotsUpdatesCachedDetailsWithEqualTimestamp(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	costKey := CostKey{ChannelID: 402, Model: "outside-detail-limit"}
	SetCostForTest(costKey, CostSnapshot{Known: true, Cost: 1, UpdatedUnix: 100})
	SetBalanceForTest(402, BalanceSnapshot{Known: true, Balance: 1, UpdatedUnix: 100})

	cachedCostKeys, cachedBalanceChannels := CostConnectorCachedState()
	ReconcileCostConnectorSnapshots(CostConnectorReconcileSnapshot{
		CachedCostKeys: cachedCostKeys,
		CachedCosts: []model.RoutingCostSnapshot{{
			ChannelID:  402,
			ModelName:  "outside-detail-limit",
			GroupRatio: 1,
			BaseRatio:  9,
			Confidence: model.RoutingCostConfidenceFull,
			SnapshotTS: 100,
		}},
		CachedBalanceChannels: cachedBalanceChannels,
		CachedHealth: []model.RoutingChannelHealthState{{
			ChannelID:          402,
			BalanceKnown:       true,
			Balance:            9,
			BalanceUpdatedTime: 100,
		}},
	})

	cost, costFound := GetCost(costKey)
	balance, balanceFound := GetBalance(402)
	require.True(t, costFound)
	assert.Equal(t, 9.0, cost.Cost)
	require.True(t, balanceFound)
	assert.Equal(t, 9.0, balance.Balance)
}

func TestHotcacheLoadSnapshotsRespectHardLimitsAndKeepNewest(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	cache.Lock()
	cache.limits = Limits{MaxMetrics: 2, MaxCosts: 2, MaxBreakers: 2, MaxHealth: 2}
	cache.Unlock()

	LoadMetricSnapshots([]model.RoutingChannelMetric{
		{ChannelID: 101, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "old", Group: "default", BucketTs: 10, RequestCount: 1},
		{ChannelID: 102, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "newest", Group: "default", BucketTs: 30, RequestCount: 1},
		{ChannelID: 103, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "new", Group: "default", BucketTs: 20, RequestCount: 1},
	}, 60)
	LoadCostSnapshots([]model.RoutingCostSnapshot{
		{ChannelID: 201, ModelName: "old", BaseRatio: 1, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 10},
		{ChannelID: 202, ModelName: "newest", BaseRatio: 1, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 30},
		{ChannelID: 203, ModelName: "new", BaseRatio: 1, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 20},
	})
	LoadBreakerSnapshots([]model.RoutingBreakerState{
		{ChannelID: 301, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "old", Group: "default", State: "healthy", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 10},
		{ChannelID: 302, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "newest", Group: "default", State: "open", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 30},
		{ChannelID: 303, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "new", Group: "default", State: "degraded", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 20},
	})
	LoadHealthSnapshots([]model.RoutingChannelHealthState{
		{ChannelID: 401, AuthFailure: true, AuthFailureUntil: 100, UpdatedTime: 10, BalanceKnown: true, Balance: 1, BalanceUpdatedTime: 10},
		{ChannelID: 402, AuthFailure: true, AuthFailureUntil: 100, UpdatedTime: 30, BalanceKnown: true, Balance: 2, BalanceUpdatedTime: 30},
		{ChannelID: 403, AuthFailure: true, AuthFailureUntil: 100, UpdatedTime: 20, BalanceKnown: true, Balance: 3, BalanceUpdatedTime: 20},
	}, 0)

	assert.Equal(t, Stats{
		Metrics:      2,
		Costs:        2,
		Breakers:     2,
		AuthFailures: 2,
		Balances:     2,
		Evictions:    5,
	}, RuntimeStats())
	_, ok := GetMetric(Key{ChannelID: 101, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "old", Group: "default"})
	assert.False(t, ok)
	_, ok = GetMetric(Key{ChannelID: 102, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "newest", Group: "default"})
	assert.True(t, ok)
	_, ok = GetMetric(Key{ChannelID: 103, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "new", Group: "default"})
	assert.True(t, ok)
	_, ok = GetCost(CostKey{ChannelID: 201, Model: "old"})
	assert.False(t, ok)
	_, ok = GetBreaker(Key{ChannelID: 301, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "old", Group: "default"})
	assert.False(t, ok)
	_, ok = GetAuthFailure(401)
	assert.False(t, ok)
	_, ok = GetBalance(401)
	assert.False(t, ok)
}

func TestLoadBreakerSnapshotsRejectsLegacySemanticVersion(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	LoadBreakerSnapshots([]model.RoutingBreakerState{
		{ChannelID: 601, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "legacy", Group: "default", State: "open", SemanticVersion: 0, UpdatedTime: 100},
		{ChannelID: 602, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "current", Group: "default", State: "open", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 100},
	})

	_, legacy := GetBreaker(Key{ChannelID: 601, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "legacy", Group: "default"})
	_, current := GetBreaker(Key{ChannelID: 602, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "current", Group: "default"})
	assert.False(t, legacy)
	assert.True(t, current)
}

func TestLoadMetricSnapshotsCountsOnlyFinalBatchEviction(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	cache.Lock()
	cache.limits = Limits{MaxMetrics: 1, MaxCosts: 1, MaxBreakers: 1, MaxHealth: 1}
	cache.Unlock()

	SetMetricForTest(Key{ChannelID: 501, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "retained", Group: "default"}, MetricSnapshot{UpdatedUnix: 100})
	LoadMetricSnapshots([]model.RoutingChannelMetric{
		{ChannelID: 502, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "batch", Group: "default", BucketTs: 10, RequestCount: 1},
		{ChannelID: 502, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "batch", Group: "default", BucketTs: 20, RequestCount: 1},
		{ChannelID: 502, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "batch", Group: "default", BucketTs: 30, RequestCount: 1},
	}, 60)

	assert.Equal(t, Stats{Metrics: 1, Evictions: 1}, RuntimeStats())
	_, ok := GetMetric(Key{ChannelID: 501, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "retained", Group: "default"})
	assert.True(t, ok)
	_, ok = GetMetric(Key{ChannelID: 502, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "batch", Group: "default"})
	assert.False(t, ok)
}

func TestHotcacheResetRestoresDefaultLimitsAndEvictionStats(t *testing.T) {
	ResetForTest()
	cache.Lock()
	cache.limits = Limits{MaxMetrics: 1, MaxCosts: 1, MaxBreakers: 1, MaxHealth: 1}
	cache.Unlock()
	SetMetricForTest(Key{ChannelID: 1, APIKeyIndex: -1, Model: "first", Group: "default"}, MetricSnapshot{UpdatedUnix: 1})
	SetMetricForTest(Key{ChannelID: 2, APIKeyIndex: -1, Model: "second", Group: "default"}, MetricSnapshot{UpdatedUnix: 2})
	require.Equal(t, int64(1), RuntimeStats().Evictions)

	ResetForTest()
	t.Cleanup(ResetForTest)
	SetMetricForTest(Key{ChannelID: 1, APIKeyIndex: -1, Model: "first", Group: "default"}, MetricSnapshot{UpdatedUnix: 1})
	SetMetricForTest(Key{ChannelID: 2, APIKeyIndex: -1, Model: "second", Group: "default"}, MetricSnapshot{UpdatedUnix: 2})
	SetMetricForTest(Key{ChannelID: 3, APIKeyIndex: -1, Model: "third", Group: "default"}, MetricSnapshot{UpdatedUnix: 3})

	assert.Equal(t, Stats{Metrics: 3}, RuntimeStats())
}

func TestHotcacheStoresAuthFailureAndBalanceMarkers(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	SetAuthFailureForTest(99, HealthMarker{Marked: true, UpdatedUnix: 100})
	SetBalanceForTest(99, BalanceSnapshot{Known: true, Balance: 0.75, UpdatedUnix: 101})

	authFailure, ok := GetAuthFailure(99)
	require.True(t, ok)
	assert.True(t, authFailure.Marked)
	assert.Equal(t, int64(100), authFailure.UpdatedUnix)

	balance, ok := GetBalance(99)
	require.True(t, ok)
	assert.True(t, balance.Known)
	assert.Equal(t, 0.75, balance.Balance)
	assert.Equal(t, int64(101), balance.UpdatedUnix)
}
