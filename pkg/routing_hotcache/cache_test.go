package routinghotcache

import (
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

func TestHotcacheResetClearsSnapshots(t *testing.T) {
	key := Key{ChannelID: 12, APIKeyIndex: -1, Model: "gpt-test", Group: "default"}
	SetMetricForTest(key, MetricSnapshot{RequestCount: 1})
	ResetForTest()

	_, ok := GetMetric(key)
	assert.False(t, ok)
}

func TestLoadMetricSnapshotsBuildsSelectorMetric(t *testing.T) {
	ResetForTest()
	LoadMetricSnapshots([]model.RoutingChannelMetric{{
		ChannelID:      77,
		APIKeyIndex:    model.RoutingMetricSingleKeyIndex,
		ModelName:      "gpt-test",
		Group:          "default",
		BucketTs:       120,
		RequestCount:   4,
		SuccessCount:   3,
		TotalLatencyMs: 800,
		LatencyP95Ms:   750,
		TtftCount:      4,
		TtftP95Ms:      120,
	}}, 60)

	metric, ok := GetMetric(Key{ChannelID: 77, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"})
	require.True(t, ok)
	assert.Equal(t, int64(4), metric.RequestCount)
	assert.Equal(t, int64(3), metric.SuccessCount)
	assert.Equal(t, 750.0, metric.P95LatencyMs)
	assert.Equal(t, 120.0, metric.P95TTFTMs)
	assert.InDelta(t, 4.0/60.0, metric.TPS, 0.000001)
	assert.Equal(t, int64(120), metric.UpdatedUnix)
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
		{ChannelID: 78, ModelName: "gpt-test", GroupRatio: 2, BaseRatio: 1, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 200},
		{ChannelID: 78, ModelName: "gpt-test", GroupRatio: 9, BaseRatio: 1, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 100},
	})
	cost, ok := GetCost(costKey)
	require.True(t, ok)
	assert.Equal(t, int64(200), cost.UpdatedUnix)
	assert.Equal(t, 2.0, cost.Cost)
	LoadCostSnapshots([]model.RoutingCostSnapshot{
		{ChannelID: 78, ModelName: "gpt-test", GroupRatio: 5, BaseRatio: 1, Confidence: model.RoutingCostConfidenceFull, SnapshotTS: 200},
	})
	cost, ok = GetCost(costKey)
	require.True(t, ok)
	assert.Equal(t, 2.0, cost.Cost)
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
