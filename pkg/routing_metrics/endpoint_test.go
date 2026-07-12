package routingmetrics

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	"github.com/QuantumNous/new-api/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEndpointMetricsSeparateNetworkHealthByRegion(t *testing.T) {
	enableRoutingMetricsForTest(t)
	ctx := stableTestContext(801, 8, 81, 0, 18, false, "")
	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointHost, "api.example.test")
	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointAuthority, "https://api.example.test:443")
	common.SetContextKey(ctx, constant.ContextKeyRoutingRegion, "us-east-1")
	info := stableTestRelayInfo(801, "gpt-endpoint")

	RecordClassifiedAttempt(ctx, info, 801, true, nil, routingerror.Classification{Rule: "success"})
	networkErr := types.NewError(errors.New("dial failed"), types.ErrorCodeDoRequestFailed)
	RecordClassifiedAttempt(ctx, info, 801, false, networkErr, routingerror.Classification{
		Responsibility: routingerror.ResponsibilityNetwork,
		Scope:          routingerror.ScopeEndpoint,
		HealthEffect:   routingerror.HealthDegrade,
		Rule:           "network_cause",
	})
	upstreamCallerErr := types.NewErrorWithStatusCode(errors.New("bad request"), types.ErrorCodeBadResponseStatusCode, 400)
	RecordClassifiedAttempt(ctx, info, 801, false, upstreamCallerErr, routingerror.Classification{
		Responsibility: routingerror.ResponsibilityCaller,
		Scope:          routingerror.ScopeRequest,
		HealthEffect:   routingerror.HealthIgnore,
		Rule:           "caller_status",
	})
	localCallerErr := types.NewError(errors.New("local validation"), types.ErrorCodeInvalidRequest)
	RecordClassifiedAttempt(ctx, info, 801, false, localCallerErr, routingerror.Classification{
		Responsibility: routingerror.ResponsibilityCaller,
		Scope:          routingerror.ScopeRequest,
		HealthEffect:   routingerror.HealthIgnore,
		Rule:           "local_validation",
	})

	snapshots := EndpointSnapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, "api.example.test", snapshots[0].EndpointHost)
	assert.Equal(t, "https://api.example.test:443", snapshots[0].EndpointAuthority)
	assert.Equal(t, "us-east-1", snapshots[0].Region)
	assert.Equal(t, int64(3), snapshots[0].RequestCount)
	assert.Equal(t, int64(2), snapshots[0].ReachableCount)
	assert.Equal(t, int64(1), snapshots[0].NetworkFailureCount)
	assert.Equal(t, EndpointStats{Buckets: 1}, EndpointRuntimeStats())

	common.SetContextKey(ctx, constant.ContextKeyRoutingRegion, "us-west-1")
	RecordClassifiedAttempt(ctx, info, 801, true, nil, routingerror.Classification{Rule: "success"})
	assert.Len(t, EndpointSnapshots(), 2)
}

func TestEndpointMetricsKeepLatePreResetRecordsInOldGeneration(t *testing.T) {
	enableRoutingMetricsForTest(t)
	routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig())
	t.Cleanup(func() { routingbreaker.ResetDefaultForTest(routingbreaker.DefaultConfig()) })
	authority := "https://api.reset.test:443"
	region := "reset-region"
	breakerKey := routingbreaker.NewEndpointKey(authority, region)
	_, applied := routingbreaker.ApplyDefaultResetGeneration(breakerKey, 1)
	require.True(t, applied)
	ClearEndpoint(authority, region)

	lateKey := EndpointKey{
		EndpointAuthority: authority, Region: region, BucketTs: bucketStart(1_800_000_000), ResetGeneration: 0,
	}
	shard := &endpointMetrics.shards[endpointShardIndex(lateKey)]
	shard.Lock()
	if shard.buckets == nil {
		shard.buckets = make(map[EndpointKey]*endpointBucket)
	}
	shard.buckets[lateKey] = &endpointBucket{endpointHost: "api.reset.test", requestCount: 1, reachableCount: 1}
	endpointMetrics.buckets.Add(1)
	currentKey := lateKey
	currentKey.ResetGeneration = 1
	shard.buckets[currentKey] = &endpointBucket{endpointHost: "api.reset.test", requestCount: 1, reachableCount: 1}
	endpointMetrics.buckets.Add(1)
	shard.Unlock()

	snapshots := EndpointSnapshots()
	require.Len(t, snapshots, 2)
	assert.Equal(t, int64(0), snapshots[0].ResetGeneration)
	assert.Equal(t, int64(1), snapshots[1].ResetGeneration)
}

func TestEndpointMetricsDropWholeAttemptAfterRequestCountSaturates(t *testing.T) {
	enableRoutingMetricsForTest(t)
	ctx := stableTestContext(802, 8, 82, 0, 18, false, "")
	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointHost, "api.saturated.test")
	common.SetContextKey(ctx, constant.ContextKeyRoutingEndpointAuthority, "https://api.saturated.test:443")
	common.SetContextKey(ctx, constant.ContextKeyRoutingRegion, "saturated-region")
	info := stableTestRelayInfo(802, "gpt-endpoint")
	now := time.Unix(1_800_000_000, 0)
	key := EndpointKey{
		EndpointAuthority: "https://api.saturated.test:443", Region: "saturated-region",
		BucketTs: bucketStart(now.Unix()),
	}
	shard := &endpointMetrics.shards[endpointShardIndex(key)]
	shard.Lock()
	shard.buckets = map[EndpointKey]*endpointBucket{
		key: {
			endpointHost: "api.saturated.test", requestCount: maxStableBucketCount,
			reachableCount: maxStableBucketCount, totalLatencyMs: 100,
		},
	}
	shard.Unlock()
	endpointMetrics.buckets.Store(1)
	networkErr := types.NewError(errors.New("dial failed"), types.ErrorCodeDoRequestFailed)

	recordEndpointClassifiedAttempt(ctx, info, now, 50, 25, true, false, networkErr, routingerror.Classification{
		Responsibility: routingerror.ResponsibilityNetwork,
		Scope:          routingerror.ScopeEndpoint,
		HealthEffect:   routingerror.HealthOpen,
		Rule:           "network_cause",
	})

	snapshots := EndpointSnapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, maxStableBucketCount, snapshots[0].RequestCount)
	assert.Equal(t, maxStableBucketCount, snapshots[0].ReachableCount)
	assert.Zero(t, snapshots[0].NetworkFailureCount)
	assert.Equal(t, int64(100), snapshots[0].TotalLatencyMs)
	assert.Zero(t, snapshots[0].TtftCount)
	assert.Equal(t, EndpointStats{Buckets: 1, Drops: 1}, EndpointRuntimeStats())
}
