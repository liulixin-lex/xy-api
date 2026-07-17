package routinghotcache

import (
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHotcacheStoresMetricAndBreakerSnapshots(t *testing.T) {
	ResetForTest()
	key := Key{ChannelID: 12, APIKeyIndex: -1, Model: "gpt-test", Group: "default"}

	SetMetricForTest(key, MetricSnapshot{RequestCount: 100, SuccessCount: 99, P95LatencyMs: 350, TPS: 42})
	SetBreakerForTest(key, BreakerSnapshot{State: "degraded", Reason: "5xx", UpdatedUnix: 1000})

	metric, ok := GetMetric(key)
	assert.True(t, ok)
	assert.Equal(t, int64(100), metric.RequestCount)
	assert.Equal(t, float64(350), metric.P95LatencyMs)

	breaker, ok := GetBreaker(key)
	assert.True(t, ok)
	assert.Equal(t, "degraded", breaker.State)
}

func TestHotcacheSeparatesReusedChannelLifecycles(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	oldKey := Key{
		ChannelID: 77, ChannelGeneration: "old-generation",
		APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default",
	}
	currentKey := oldKey
	currentKey.ChannelGeneration = "current-generation"
	oldLifecycle := ChannelLifecycleKey{ChannelID: 77, ChannelGeneration: oldKey.ChannelGeneration}
	currentLifecycle := ChannelLifecycleKey{ChannelID: 77, ChannelGeneration: currentKey.ChannelGeneration}

	SetMetricForTest(oldKey, MetricSnapshot{RequestCount: 9, UpdatedUnix: 100})
	SetBreakerForTest(oldKey, BreakerSnapshot{State: "open", UpdatedUnix: 100})
	SetCapacityCooldownForTest(oldKey, CapacityCooldownSnapshot{
		SourceStatusCode: 429, CooldownUntilUnixMilli: 200_000, UpdatedUnixMilli: 100_000,
	})
	SetAuthFailureForGeneration(77, oldLifecycle.ChannelGeneration, HealthMarker{Marked: true, UpdatedUnix: 100})
	SetChannelBalanceUnavailableForGenerationForTest(77, oldLifecycle.ChannelGeneration, ChannelBalanceUnavailableSnapshot{
		SourceStatusCode: 402, CooldownUntilUnixMilli: 200_000, UpdatedUnixMilli: 100_000,
	})

	_, metricFound := GetMetric(currentKey)
	_, breakerFound := GetBreaker(currentKey)
	_, capacityFound := GetCapacityCooldown(currentKey)
	_, authFound := GetAuthFailureForGeneration(currentLifecycle.ChannelID, currentLifecycle.ChannelGeneration)
	_, balanceFound := GetChannelBalanceUnavailableForGeneration(currentLifecycle.ChannelID, currentLifecycle.ChannelGeneration)
	assert.False(t, metricFound)
	assert.False(t, breakerFound)
	assert.False(t, capacityFound)
	assert.False(t, authFound)
	assert.False(t, balanceFound)

	SetMetricForTest(currentKey, MetricSnapshot{RequestCount: 1, UpdatedUnix: 200})
	SetBreakerForTest(currentKey, BreakerSnapshot{State: "healthy", UpdatedUnix: 200})
	SetCapacityCooldownForTest(currentKey, CapacityCooldownSnapshot{
		SourceStatusCode: 429, CooldownUntilUnixMilli: 300_000, UpdatedUnixMilli: 200_000,
	})
	SetAuthFailureForGeneration(77, currentLifecycle.ChannelGeneration, HealthMarker{Marked: false, UpdatedUnix: 200})
	SetChannelBalanceUnavailableForGenerationForTest(77, currentLifecycle.ChannelGeneration, ChannelBalanceUnavailableSnapshot{
		SourceStatusCode: 402, CooldownUntilUnixMilli: 300_000, UpdatedUnixMilli: 200_000,
	})

	currentMetric, metricFound := GetMetric(currentKey)
	currentBreaker, breakerFound := GetBreaker(currentKey)
	currentCapacity, capacityFound := GetCapacityCooldown(currentKey)
	currentAuth, authFound := GetAuthFailureForGeneration(currentLifecycle.ChannelID, currentLifecycle.ChannelGeneration)
	currentBalance, balanceFound := GetChannelBalanceUnavailableForGeneration(currentLifecycle.ChannelID, currentLifecycle.ChannelGeneration)
	require.True(t, metricFound)
	require.True(t, breakerFound)
	require.True(t, capacityFound)
	require.True(t, authFound)
	require.True(t, balanceFound)
	assert.Equal(t, int64(1), currentMetric.RequestCount)
	assert.Equal(t, "healthy", currentBreaker.State)
	assert.Equal(t, int64(300_000), currentCapacity.CooldownUntilUnixMilli)
	assert.False(t, currentAuth.Marked)
	assert.Equal(t, int64(300_000), currentBalance.CooldownUntilUnixMilli)
	assert.Equal(t, Stats{
		Metrics: 2, Breakers: 2, CapacityCooldowns: 2,
		ChannelBalanceUnavailable: 2, AuthFailures: 2,
	}, RuntimeStats())
}

func TestHotcacheResetClearsSnapshots(t *testing.T) {
	key := Key{ChannelID: 12, APIKeyIndex: -1, Model: "gpt-test", Group: "default"}
	SetMetricForTest(key, MetricSnapshot{RequestCount: 1})
	ResetForTest()

	_, ok := GetMetric(key)
	assert.False(t, ok)
}

func TestChannelTrafficPoliciesAreAuthoritativeAndResettable(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	_, initialized := GetChannelTrafficPolicy(41)
	assert.False(t, initialized)
	ReplaceChannelTrafficConfigurations([]model.RoutingChannelConfiguration{
		{ChannelID: 41, TrafficClass: model.RoutingChannelTrafficClassClaudeCodeOnly},
		{ChannelID: 42, TrafficClass: model.RoutingChannelTrafficClassAll},
	}, 100)

	policy, initialized := GetChannelTrafficPolicy(41)
	require.True(t, initialized)
	assert.True(t, policy.ClaudeCodeOnly)
	policy, initialized = GetChannelTrafficPolicy(42)
	require.True(t, initialized)
	assert.False(t, policy.ClaudeCodeOnly)
	assert.Equal(t, ChannelTrafficPolicyState{Initialized: true, LoadedAtUnix: 100, RestrictedChannels: 1}, ChannelTrafficPoliciesState())

	SetChannelTrafficPolicy(42, true, 101)
	policy, initialized = GetChannelTrafficPolicy(42)
	require.True(t, initialized)
	assert.True(t, policy.ClaudeCodeOnly)
	assert.Equal(t, int64(100), ChannelTrafficPoliciesState().LoadedAtUnix)
	DeleteChannelTrafficPolicy(41, 102)
	policy, initialized = GetChannelTrafficPolicy(41)
	require.True(t, initialized)
	assert.False(t, policy.ClaudeCodeOnly)

	ResetForTest()
	_, initialized = GetChannelTrafficPolicy(42)
	assert.False(t, initialized)
}

func TestChannelTrafficPolicyRefreshCannotOverwriteNewerTargetedMutation(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	// The targeted mutation may arrive while the first database refresh is in
	// flight. It must be retained even though the cache is not initialized yet.
	SetChannelTrafficPolicy(51, true, 101)
	ReplaceChannelTrafficConfigurations([]model.RoutingChannelConfiguration{
		{ChannelID: 51, TrafficClass: model.RoutingChannelTrafficClassAll},
	}, 100)
	policy, initialized := GetChannelTrafficPolicy(51)
	require.True(t, initialized)
	assert.True(t, policy.ClaudeCodeOnly)
	assert.Equal(t, int64(100), ChannelTrafficPoliciesState().LoadedAtUnix)

	// A later authoritative refresh retires the local override and may publish
	// the durable database value.
	ReplaceChannelTrafficConfigurations([]model.RoutingChannelConfiguration{
		{ChannelID: 51, TrafficClass: model.RoutingChannelTrafficClassAll},
	}, 102)
	policy, initialized = GetChannelTrafficPolicy(51)
	require.True(t, initialized)
	assert.False(t, policy.ClaudeCodeOnly)
	assert.Equal(t, int64(102), ChannelTrafficPoliciesState().LoadedAtUnix)

	// A slower refresh that started before the authoritative one cannot move
	// the cache generation backwards or republish its older contents.
	ReplaceChannelTrafficConfigurations([]model.RoutingChannelConfiguration{
		{ChannelID: 51, TrafficClass: model.RoutingChannelTrafficClassClaudeCodeOnly},
	}, 101)
	policy, initialized = GetChannelTrafficPolicy(51)
	require.True(t, initialized)
	assert.False(t, policy.ClaudeCodeOnly)
	assert.Equal(t, int64(102), ChannelTrafficPoliciesState().LoadedAtUnix)
}

func TestHotcachePruneRemovesStaleSnapshotsAcrossAllMaps(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	oldKey := Key{ChannelID: 1, APIKeyIndex: -1, Model: "old", Group: "default"}
	newKey := Key{ChannelID: 2, APIKeyIndex: -1, Model: "new", Group: "default"}
	SetMetricForTest(oldKey, MetricSnapshot{UpdatedUnix: 100})
	SetMetricForTest(newKey, MetricSnapshot{UpdatedUnix: 200})
	SetBreakerForTest(oldKey, BreakerSnapshot{UpdatedUnix: 100})
	SetBreakerForTest(newKey, BreakerSnapshot{UpdatedUnix: 200})
	SetAuthFailureForTest(1, HealthMarker{Marked: true, UpdatedUnix: 100})
	SetAuthFailureForTest(2, HealthMarker{Marked: true, UpdatedUnix: 200})

	deleted := Prune(300, 150)

	assert.Equal(t, 3, deleted)
	assert.Equal(t, Stats{
		Metrics:      1,
		Breakers:     1,
		AuthFailures: 1,
		Evictions:    3,
	}, RuntimeStats())
	_, ok := GetMetric(oldKey)
	assert.False(t, ok)
	_, ok = GetMetric(newKey)
	assert.True(t, ok)
	_, ok = GetBreaker(oldKey)
	assert.False(t, ok)
	_, ok = GetBreaker(newKey)
	assert.True(t, ok)
	_, ok = GetAuthFailure(1)
	assert.False(t, ok)
	_, ok = GetAuthFailure(2)
	assert.True(t, ok)
}

func TestHotcacheSetSnapshotsRespectHardLimitsAndKeepNewest(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	cache.Lock()
	cache.limits = Limits{MaxMetrics: 2, MaxBreakers: 2, MaxHealth: 2}
	cache.Unlock()

	metricKeys := []Key{
		{ChannelID: 10, APIKeyIndex: -1, Model: "metric-old", Group: "default"},
		{ChannelID: 11, APIKeyIndex: -1, Model: "metric-newest", Group: "default"},
		{ChannelID: 12, APIKeyIndex: -1, Model: "metric-new", Group: "default"},
	}
	SetMetricForTest(metricKeys[0], MetricSnapshot{UpdatedUnix: 10})
	SetMetricForTest(metricKeys[1], MetricSnapshot{UpdatedUnix: 30})
	SetMetricForTest(metricKeys[2], MetricSnapshot{UpdatedUnix: 20})

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

	assert.Equal(t, Stats{
		Metrics:      2,
		Breakers:     2,
		AuthFailures: 2,
		Evictions:    3,
	}, RuntimeStats())
	_, ok := GetMetric(metricKeys[0])
	assert.False(t, ok)
	_, ok = GetMetric(metricKeys[1])
	assert.True(t, ok)
	_, ok = GetMetric(metricKeys[2])
	assert.True(t, ok)
	_, ok = GetBreaker(breakerKeys[0])
	assert.False(t, ok)
	_, ok = GetAuthFailure(40)
	assert.False(t, ok)
}

func TestHotcacheCapacityEvictionUsesStableKeyOrder(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	cache.Lock()
	cache.limits = Limits{MaxMetrics: 2, MaxBreakers: 2, MaxHealth: 2}
	cache.Unlock()

	for _, channelID := range []int{3, 1, 2} {
		SetMetricForTest(Key{ChannelID: channelID, APIKeyIndex: -1, Model: "same", Group: "default"}, MetricSnapshot{UpdatedUnix: 100})
		SetAuthFailureForTest(channelID, HealthMarker{Marked: true, UpdatedUnix: 100})
	}

	_, metricOne := GetMetric(Key{ChannelID: 1, APIKeyIndex: -1, Model: "same", Group: "default"})
	_, metricTwo := GetMetric(Key{ChannelID: 2, APIKeyIndex: -1, Model: "same", Group: "default"})
	_, metricThree := GetMetric(Key{ChannelID: 3, APIKeyIndex: -1, Model: "same", Group: "default"})
	assert.False(t, metricOne)
	assert.True(t, metricTwo)
	assert.True(t, metricThree)
	_, authOne := GetAuthFailure(1)
	_, authTwo := GetAuthFailure(2)
	_, authThree := GetAuthFailure(3)
	assert.False(t, authOne)
	assert.True(t, authTwo)
	assert.True(t, authThree)
	assert.Equal(t, int64(2), RuntimeStats().Evictions)
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

func TestLoadSnapshotsKeepLatestMetricAndBreaker(t *testing.T) {
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

func TestHotcacheLoadSnapshotsRespectHardLimitsAndKeepNewest(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	cache.Lock()
	cache.limits = Limits{MaxMetrics: 2, MaxBreakers: 2, MaxHealth: 2}
	cache.Unlock()

	LoadMetricSnapshots([]model.RoutingChannelMetric{
		{ChannelID: 101, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "old", Group: "default", BucketTs: 10, RequestCount: 1},
		{ChannelID: 102, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "newest", Group: "default", BucketTs: 30, RequestCount: 1},
		{ChannelID: 103, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "new", Group: "default", BucketTs: 20, RequestCount: 1},
	}, 60)
	LoadBreakerSnapshots([]model.RoutingBreakerState{
		{ChannelID: 301, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "old", Group: "default", State: "healthy", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 10},
		{ChannelID: 302, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "newest", Group: "default", State: "open", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 30},
		{ChannelID: 303, APIKeyIndex: model.RoutingMetricSingleKeyIndex, ModelName: "new", Group: "default", State: "degraded", SemanticVersion: model.RoutingBreakerSemanticVersion, UpdatedTime: 20},
	})
	LoadHealthSnapshots([]model.RoutingChannelHealthState{
		{ChannelID: 401, AuthFailure: true, AuthFailureUntil: 100, UpdatedTime: 10},
		{ChannelID: 402, AuthFailure: true, AuthFailureUntil: 100, UpdatedTime: 30},
		{ChannelID: 403, AuthFailure: true, AuthFailureUntil: 100, UpdatedTime: 20},
	}, 0)

	assert.Equal(t, Stats{
		Metrics:      2,
		Breakers:     2,
		AuthFailures: 2,
		Evictions:    3,
	}, RuntimeStats())
	_, ok := GetMetric(Key{ChannelID: 101, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "old", Group: "default"})
	assert.False(t, ok)
	_, ok = GetMetric(Key{ChannelID: 102, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "newest", Group: "default"})
	assert.True(t, ok)
	_, ok = GetMetric(Key{ChannelID: 103, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "new", Group: "default"})
	assert.True(t, ok)
	_, ok = GetBreaker(Key{ChannelID: 301, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "old", Group: "default"})
	assert.False(t, ok)
	_, ok = GetAuthFailure(401)
	assert.False(t, ok)
}

func TestLoadHealthSnapshotsHydratesOnlyCurrentAuthenticationState(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	LoadHealthSnapshots([]model.RoutingChannelHealthState{{
		ChannelID: 501, UpdatedTime: 100,
	}}, 100)

	assert.Equal(t, Stats{}, RuntimeStats())
	_, ok := GetAuthFailure(501)
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
	cache.limits = Limits{MaxMetrics: 1, MaxBreakers: 1, MaxHealth: 1}
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
	cache.limits = Limits{MaxMetrics: 1, MaxBreakers: 1, MaxHealth: 1}
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

func TestHotcacheStoresAuthFailureMarkers(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	SetAuthFailureForTest(99, HealthMarker{Marked: true, UpdatedUnix: 100})

	authFailure, ok := GetAuthFailure(99)
	require.True(t, ok)
	assert.True(t, authFailure.Marked)
	assert.Equal(t, int64(100), authFailure.UpdatedUnix)
}
