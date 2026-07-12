package routingmetrics

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stableTestContext(
	channelID int,
	poolID int,
	memberID int,
	credentialID int,
	revision uint64,
	multiKey bool,
	selectedKey string,
) *gin.Context {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, channelID)
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, multiKey)
	common.SetContextKey(ctx, constant.ContextKeyChannelKey, selectedKey)
	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, poolID)
	common.SetContextKey(ctx, constant.ContextKeyRoutingMemberID, memberID)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCredentialID, credentialID)
	common.SetContextKey(ctx, constant.ContextKeyRoutingSnapshotRevision, revision)
	return ctx
}

func stableTestRelayInfo(channelID int, modelName string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: modelName,
		StartTime:       time.Now().Add(-10 * time.Millisecond),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:               channelID,
			RoutingSnapshotRevision: 999,
			RoutingPoolID:           999,
			RoutingMemberID:         999,
			RoutingCredentialID:     999,
			ApiKey:                  "stale-key",
		},
	}
}

func TestStableMetricsDoNotInitializeWhenDisabled(t *testing.T) {
	configureRoutingMetricsForTest(t, false)
	ctx := stableTestContext(1, 10, 100, 1000, 7, true, "selected")
	info := stableTestRelayInfo(1, "gpt-test")

	release := BeginInflight(ctx, info, 1)
	RecordClassifiedAttempt(ctx, info, 1, true, nil, routingerror.Classification{Rule: "success"})
	release()

	assert.Nil(t, stableStorePointer.Load())
	assert.Empty(t, StableSnapshots())
	assert.Empty(t, DrainStableSnapshots())
	assert.Equal(t, StableStats{}, StableRuntimeStats())
}

func TestStableMetricsRecordSingleAndMultiKeyBeforeLegacy(t *testing.T) {
	enableRoutingMetricsForTest(t)
	singleCtx := stableTestContext(101, 1, 11, 0, 7, false, "")
	singleInfo := stableTestRelayInfo(101, "gpt-single")
	singleRelease := BeginInflight(singleCtx, singleInfo, 101)
	assert.Equal(t, int64(1), StableInflightCount(StableInflightKey{
		PoolMemberID: 11,
		CredentialID: 0,
		Model:        "gpt-single",
	}))
	recordTestAttempt(singleCtx, singleInfo, 101, nil)
	singleRelease()

	multiCtx := stableTestContext(202, 2, 22, 222, 8, true, "selected")
	multiInfo := stableTestRelayInfo(202, "gpt-multi")
	multiRelease := BeginInflight(multiCtx, multiInfo, 202)
	assert.Equal(t, int64(1), StableInflightCount(StableInflightKey{
		PoolMemberID: 22,
		CredentialID: 222,
		Model:        "gpt-multi",
	}))
	recordTestAttempt(multiCtx, multiInfo, 202, nil)
	multiRelease()

	stable := StableSnapshots()
	require.Len(t, stable, 2)
	assert.Equal(t, StableSnapshot{
		PoolID:                  1,
		PoolMemberID:            11,
		CredentialID:            0,
		ChannelID:               101,
		Model:                   "gpt-single",
		BucketTs:                stable[0].BucketTs,
		LastSnapshotRevision:    7,
		SketchCodecVersion:      stable[0].SketchCodecVersion,
		LatencySampleCount:      stable[0].LatencySampleCount,
		LatencySketch:           stable[0].LatencySketch,
		RequestCount:            1,
		SuccessCount:            1,
		ReliabilityRequestCount: 1,
		TotalLatencyMs:          stable[0].TotalLatencyMs,
	}, stable[0])
	assert.Equal(t, 2, stable[1].PoolID)
	assert.Equal(t, 22, stable[1].PoolMemberID)
	assert.Equal(t, 222, stable[1].CredentialID)
	assert.Equal(t, 202, stable[1].ChannelID)
	assert.Equal(t, uint64(8), stable[1].LastSnapshotRevision)
	assert.Equal(t, int64(1), stable[1].RequestCount)
	assert.Equal(t, int64(1), stable[1].SuccessCount)

	legacy := Snapshots()
	require.Len(t, legacy, 1)
	assert.Equal(t, 101, legacy[0].ChannelID)
	assert.Equal(t, "gpt-single", legacy[0].ModelName)
	assert.Equal(t, StableStats{Initialized: true, Buckets: 2}, StableRuntimeStats())
}

func TestStableMetricsContextIdentityOverridesRelayInfoAndClearsStaleIdentity(t *testing.T) {
	enableRoutingMetricsForTest(t)
	ctx := stableTestContext(303, 3, 33, 333, 9, true, "selected")
	info := stableTestRelayInfo(303, "gpt-context")
	recordTestAttempt(ctx, info, 303, nil)

	snapshots := StableSnapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, 3, snapshots[0].PoolID)
	assert.Equal(t, 33, snapshots[0].PoolMemberID)
	assert.Equal(t, 333, snapshots[0].CredentialID)
	assert.Equal(t, uint64(9), snapshots[0].LastSnapshotRevision)

	common.SetContextKey(ctx, constant.ContextKeyRoutingPoolID, 0)
	common.SetContextKey(ctx, constant.ContextKeyRoutingMemberID, 0)
	common.SetContextKey(ctx, constant.ContextKeyRoutingCredentialID, 0)
	common.SetContextKey(ctx, constant.ContextKeyRoutingSnapshotRevision, uint64(0))
	recordTestAttempt(ctx, info, 303, nil)

	assert.Equal(t, int64(1), StableSnapshots()[0].RequestCount)
	assert.Equal(t, int64(1), StableRuntimeStats().IdentityDrops)
}

func TestStableMetricsSeparatesSnapshotRevisionsWithinBucket(t *testing.T) {
	enableRoutingMetricsForTest(t)
	ctx := stableTestContext(304, 3, 34, 334, 21, true, "selected")
	info := stableTestRelayInfo(304, "gpt-revision-boundary")
	recordTestAttempt(ctx, info, 304, nil)

	common.SetContextKey(ctx, constant.ContextKeyRoutingSnapshotRevision, uint64(22))
	recordTestAttempt(ctx, info, 304, nil)

	snapshots := StableSnapshots()
	require.Len(t, snapshots, 2)
	assert.Equal(t, []uint64{21, 22}, []uint64{
		snapshots[0].LastSnapshotRevision,
		snapshots[1].LastSnapshotRevision,
	})
	assert.Equal(t, int64(1), snapshots[0].RequestCount)
	assert.Equal(t, int64(1), snapshots[1].RequestCount)
	assert.Equal(t, int64(2), StableRuntimeStats().Buckets)

	drained := DrainStableSnapshots()
	require.Len(t, drained, 2)
	RequeueStableSnapshots(drained)
	assert.Equal(t, drained, StableSnapshots())
}

func TestStableMetricsCredentialAndModelIdentityRules(t *testing.T) {
	enableRoutingMetricsForTest(t)

	multiCtx := stableTestContext(401, 4, 41, 0, 10, true, "selected")
	multiInfo := stableTestRelayInfo(401, "gpt-multi")
	multiRelease := BeginInflight(multiCtx, multiInfo, 401)
	recordTestAttempt(multiCtx, multiInfo, 401, nil)
	multiRelease()

	keyedSingleCtx := stableTestContext(402, 4, 42, 0, 10, false, "selected")
	keyedSingleInfo := stableTestRelayInfo(402, "gpt-single")
	recordTestAttempt(keyedSingleCtx, keyedSingleInfo, 402, nil)

	validModel := strings.Repeat("界", maxStableModelRunes)
	keylessCtx := stableTestContext(403, 4, 43, 0, 10, false, "")
	keylessInfo := stableTestRelayInfo(403, validModel)
	recordTestAttempt(keylessCtx, keylessInfo, 403, nil)

	tooManyRunesInfo := stableTestRelayInfo(404, strings.Repeat("界", maxStableModelRunes+1))
	tooManyRunesCtx := stableTestContext(404, 4, 44, 444, 10, true, "selected")
	recordTestAttempt(tooManyRunesCtx, tooManyRunesInfo, 404, nil)
	invalidUTF8Info := stableTestRelayInfo(405, string([]byte{0xff}))
	invalidUTF8Ctx := stableTestContext(405, 4, 45, 445, 10, true, "selected")
	recordTestAttempt(invalidUTF8Ctx, invalidUTF8Info, 405, nil)

	snapshots := StableSnapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, 43, snapshots[0].PoolMemberID)
	assert.Zero(t, snapshots[0].CredentialID)
	assert.Equal(t, validModel, snapshots[0].Model)
	assert.Equal(t, StableStats{
		Initialized:           true,
		Buckets:               1,
		IdentityDrops:         4,
		InflightIdentityDrops: 1,
	}, StableRuntimeStats())

	legacy := Snapshots()
	require.Len(t, legacy, 2)
	assert.Equal(t, 402, legacy[0].ChannelID)
	assert.Equal(t, 403, legacy[1].ChannelID)
}

func TestStableMetricsCountFailuresUnknownClassificationsAndRetryAfter(t *testing.T) {
	enableRoutingMetricsForTest(t)
	ctx := stableTestContext(501, 5, 51, 0, 11, false, "")
	info := stableTestRelayInfo(501, "gpt-failures")
	RecordClassifiedAttempt(ctx, info, 501, true, nil, routingerror.Classification{Rule: "success"})

	providerErr := types.NewErrorWithStatusCode(errors.New("provider"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	RecordClassifiedAttempt(ctx, info, 501, false, providerErr, routingerror.Classification{
		Responsibility: routingerror.ResponsibilityProvider,
		HealthEffect:   routingerror.HealthDegrade,
		Rule:           "provider_5xx",
	})

	fallbackErr := types.NewErrorWithStatusCode(errors.New("fallback"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
	fallbackMetadata, err := common.Marshal(map[string]int64{"retry_after_ms": 1500})
	require.NoError(t, err)
	fallbackErr.Metadata = fallbackMetadata
	RecordClassifiedAttempt(ctx, info, 501, false, fallbackErr, routingerror.Classification{
		Responsibility: routingerror.ResponsibilityGateway,
		HealthEffect:   routingerror.HealthIgnore,
		Rule:           "conservative_gateway_fallback",
	})

	emptyRuleErr := types.NewErrorWithStatusCode(errors.New("unknown"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	emptyRuleMetadata, err := common.Marshal(map[string]int64{"retry_after_ms": 2500})
	require.NoError(t, err)
	emptyRuleErr.Metadata = emptyRuleMetadata
	RecordClassifiedAttempt(ctx, info, 501, false, emptyRuleErr, routingerror.Classification{})

	snapshots := StableSnapshots()
	require.Len(t, snapshots, 1)
	metric := snapshots[0]
	assert.Equal(t, int64(4), metric.RequestCount)
	assert.Equal(t, int64(1), metric.SuccessCount)
	assert.Equal(t, int64(3), metric.FailureCount)
	assert.Equal(t, int64(2), metric.UnknownClassificationCount)
	assert.Equal(t, int64(2), metric.ReliabilityRequestCount)
	assert.Equal(t, int64(1), metric.ReliabilityFailureCount)
	assert.Equal(t, int64(2), metric.Err5xx)
	assert.Equal(t, int64(1), metric.Err429)
	assert.Equal(t, int64(2), metric.RetryAfterCount)
	assert.Equal(t, int64(4000), metric.RetryAfterTotalMs)
}

func TestStableMetricsDrainAndRequeueMergeOnlyAdditiveData(t *testing.T) {
	enableRoutingMetricsForTest(t)
	first := StableSnapshot{
		PoolID: 1, PoolMemberID: 61, CredentialID: 601, ChannelID: 601,
		Model: "gpt-merge", BucketTs: 60, LastSnapshotRevision: 12,
		RequestCount: 2, SuccessCount: 1, FailureCount: 1, UnknownClassificationCount: 1,
		ReliabilityRequestCount: 2, ReliabilityFailureCount: 1,
		TotalLatencyMs: 300, TtftSumMs: 80, TtftCount: 1,
		OutputTokens: 20, GenerationMs: 200, Err5xx: 1,
		RetryAfterCount: 1, RetryAfterTotalMs: 1000,
	}
	second := StableSnapshot{
		PoolID: 1, PoolMemberID: 61, CredentialID: 601, ChannelID: 601,
		Model: "gpt-merge", BucketTs: 60, LastSnapshotRevision: 12,
		RequestCount: 3, SuccessCount: 2, FailureCount: 1,
		ReliabilityRequestCount: 2, ReliabilityFailureCount: 1,
		TotalLatencyMs: 700, TtftSumMs: 120, TtftCount: 2,
		OutputTokens: 30, GenerationMs: 300, Err429: 1,
		RetryAfterCount: 2, RetryAfterTotalMs: 2500,
	}
	RequeueStableSnapshots([]StableSnapshot{first, second})

	merged := StableSnapshots()
	require.Len(t, merged, 1)
	assert.Equal(t, 1, merged[0].PoolID)
	assert.Equal(t, 601, merged[0].ChannelID)
	assert.Equal(t, uint64(12), merged[0].LastSnapshotRevision)
	assert.Equal(t, int64(5), merged[0].RequestCount)
	assert.Equal(t, int64(3), merged[0].SuccessCount)
	assert.Equal(t, int64(2), merged[0].FailureCount)
	assert.Equal(t, int64(1), merged[0].UnknownClassificationCount)
	assert.Equal(t, int64(1000), merged[0].TotalLatencyMs)
	assert.Equal(t, int64(200), merged[0].TtftSumMs)
	assert.Equal(t, int64(3), merged[0].TtftCount)
	assert.Equal(t, int64(50), merged[0].OutputTokens)
	assert.Equal(t, int64(500), merged[0].GenerationMs)
	assert.Equal(t, int64(3), merged[0].RetryAfterCount)
	assert.Equal(t, int64(3500), merged[0].RetryAfterTotalMs)

	drained := DrainStableSnapshots()
	require.Len(t, drained, 1)
	assert.Empty(t, StableSnapshots())
	RequeueStableSnapshots(drained)
	assert.Equal(t, drained, StableSnapshots())
}

func TestStableMetricsRecordDrainAndRequeueMergeableDistributions(t *testing.T) {
	enableRoutingMetricsForTest(t)
	ctx := stableTestContext(611, 6, 61, 601, 12, true, "selected")
	info := stableTestRelayInfo(611, "gpt-distribution")
	info.IsStream = true
	info.ResetStreamAttemptState()
	attemptStart := info.RoutingAttemptStartTime()
	info.FirstResponseTime = attemptStart.Add(25 * time.Millisecond)
	info.ObserveRoutingOutputTokensAt(10, attemptStart.Add(100*time.Millisecond))
	RecordClassifiedAttempt(ctx, info, 611, true, nil, routingerror.Classification{Rule: "success"})

	snapshots := StableSnapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, routingdistribution.SketchCodecVersion, snapshots[0].SketchCodecVersion)
	assert.Equal(t, int64(1), snapshots[0].LatencySampleCount)
	assert.Equal(t, int64(1), snapshots[0].TtftSampleCount)
	latency := stableTestDecodeSketch(t, snapshots[0].LatencySketch, snapshots[0].SketchCodecVersion)
	latencyP95, err := latency.Quantile(0.95)
	require.NoError(t, err)
	require.True(t, latencyP95.Known)
	assert.InDelta(t, 100, latencyP95.ValueMilliseconds, 2)
	ttft := stableTestDecodeSketch(t, snapshots[0].TtftSketch, snapshots[0].SketchCodecVersion)
	ttftP95, err := ttft.Quantile(0.95)
	require.NoError(t, err)
	require.True(t, ttftP95.Known)
	assert.InDelta(t, 25, ttftP95.ValueMilliseconds, 1)

	drained := DrainStableSnapshots()
	require.Len(t, drained, 1)
	secondLatency := routingdistribution.NewDurationSketch()
	for range 19 {
		_, err = secondLatency.AddMillis(1000)
		require.NoError(t, err)
	}
	secondLatencyBytes, err := secondLatency.MarshalBinary()
	require.NoError(t, err)
	drained = append(drained, StableSnapshot{
		PoolID: 6, PoolMemberID: 61, CredentialID: 601, ChannelID: 611,
		Model: "gpt-distribution", BucketTs: drained[0].BucketTs, LastSnapshotRevision: 12,
		RequestCount: 19, SuccessCount: 19, ReliabilityRequestCount: 19, TotalLatencyMs: 19_000,
		SketchCodecVersion: routingdistribution.SketchCodecVersion,
		LatencySampleCount: 19, LatencySketch: secondLatencyBytes,
	})
	RequeueStableSnapshots(drained)

	merged := StableSnapshots()
	require.Len(t, merged, 1)
	assert.Equal(t, int64(20), merged[0].RequestCount)
	assert.Equal(t, int64(20), merged[0].LatencySampleCount)
	mergedLatency := stableTestDecodeSketch(t, merged[0].LatencySketch, merged[0].SketchCodecVersion)
	mergedP95, err := mergedLatency.Quantile(0.95)
	require.NoError(t, err)
	require.True(t, mergedP95.Known)
	assert.InDelta(t, 1000, mergedP95.ValueMilliseconds, 20)
}

func TestStableMetricsCorruptRequeueKeepsCountersAndDropsDistribution(t *testing.T) {
	enableRoutingMetricsForTest(t)
	RequeueStableSnapshots([]StableSnapshot{{
		PoolID: 7, PoolMemberID: 71, CredentialID: 701, ChannelID: 711,
		Model: "gpt-corrupt", BucketTs: 60, LastSnapshotRevision: 14,
		RequestCount: 1, SuccessCount: 1,
		SketchCodecVersion: routingdistribution.SketchCodecVersion,
		LatencySampleCount: 1, LatencySketch: []byte("corrupt"),
	}})

	snapshots := StableSnapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(1), snapshots[0].RequestCount)
	assert.Zero(t, snapshots[0].LatencySampleCount)
	assert.Empty(t, snapshots[0].LatencySketch)
	assert.Equal(t, int64(1), StableRuntimeStats().DistributionDrops)
}

func TestDrainStableSnapshotsLimitedUsesOldestFirstAndKeepsUndrainedBuckets(t *testing.T) {
	enableRoutingMetricsForTest(t)
	for _, bucketTs := range []int64{3, 1, 2} {
		RequeueStableSnapshots([]StableSnapshot{{
			PoolID: 8, PoolMemberID: int(bucketTs), CredentialID: int(bucketTs), ChannelID: int(bucketTs),
			Model: "gpt-limited", BucketTs: bucketTs, LastSnapshotRevision: 15, RequestCount: 1,
		}})
	}

	drained := DrainStableSnapshotsLimited(2, 1<<20)
	require.Len(t, drained, 2)
	assert.Equal(t, []int64{1, 2}, []int64{drained[0].BucketTs, drained[1].BucketTs})
	remaining := StableSnapshots()
	require.Len(t, remaining, 1)
	assert.Equal(t, int64(3), remaining[0].BucketTs)

	assert.Empty(t, DrainStableSnapshotsLimited(1, 1))
	remaining = StableSnapshots()
	require.Len(t, remaining, 1)
	assert.Equal(t, int64(3), remaining[0].BucketTs)
}

func stableTestDecodeSketch(t *testing.T, data []byte, version int) *routingdistribution.DurationSketch {
	t.Helper()
	sketch, err := routingdistribution.DecodeDurationSketch(data, version)
	require.NoError(t, err)
	return sketch
}

func TestClearLegacyChannelPreservesStableTelemetry(t *testing.T) {
	enableRoutingMetricsForTest(t)
	ctx := stableTestContext(701, 7, 71, 711, 14, true, "selected")
	info := stableTestRelayInfo(701, "gpt-stable")
	recordTestAttempt(ctx, info, 701, nil)
	require.Len(t, StableSnapshots(), 1)

	ClearChannel(701)
	require.Len(t, StableSnapshots(), 1)

	ClearStableChannel(701)
	assert.Empty(t, StableSnapshots())
}

func TestStableMetricsCapacityTTLEvictionAndInflightStats(t *testing.T) {
	t.Run("capacity", func(t *testing.T) {
		enableRoutingMetricsForTest(t)
		maintenanceMu.Lock()
		limits = Limits{MaxBuckets: 2, BucketTTL: time.Hour, MaxInflightKeys: 1}
		maintenanceMu.Unlock()

		for bucketTs := int64(1); bucketTs <= 3; bucketTs++ {
			RequeueStableSnapshots([]StableSnapshot{{
				PoolID: 1, PoolMemberID: int(bucketTs), CredentialID: int(bucketTs), ChannelID: int(bucketTs),
				Model: "gpt-capacity", BucketTs: bucketTs, LastSnapshotRevision: 1, RequestCount: 1,
			}})
		}
		snapshots := StableSnapshots()
		require.Len(t, snapshots, 2)
		assert.Equal(t, []int64{2, 3}, []int64{snapshots[0].BucketTs, snapshots[1].BucketTs})
		assert.Equal(t, StableStats{
			Initialized:             true,
			Buckets:                 2,
			BucketEvictions:         1,
			CapacityBucketEvictions: 1,
		}, StableRuntimeStats())
	})

	t.Run("ttl", func(t *testing.T) {
		enableRoutingMetricsForTest(t)
		maintenanceMu.Lock()
		limits = Limits{MaxBuckets: 3, BucketTTL: 2 * time.Second, MaxInflightKeys: 1}
		maintenanceMu.Unlock()

		for _, bucketTs := range []int64{1, 2, 4} {
			RequeueStableSnapshots([]StableSnapshot{{
				PoolID: 1, PoolMemberID: int(bucketTs), CredentialID: int(bucketTs), ChannelID: int(bucketTs),
				Model: "gpt-ttl", BucketTs: bucketTs, LastSnapshotRevision: 1, RequestCount: 1,
			}})
		}
		snapshots := StableSnapshots()
		require.Len(t, snapshots, 1)
		assert.Equal(t, int64(4), snapshots[0].BucketTs)
		assert.Equal(t, StableStats{
			Initialized:            true,
			Buckets:                1,
			BucketEvictions:        2,
			ExpiredBucketEvictions: 2,
		}, StableRuntimeStats())
	})

	t.Run("inflight", func(t *testing.T) {
		enableRoutingMetricsForTest(t)
		maintenanceMu.Lock()
		limits = Limits{MaxBuckets: 2, BucketTTL: time.Hour, MaxInflightKeys: 1}
		maintenanceMu.Unlock()

		firstCtx := stableTestContext(701, 7, 71, 701, 14, true, "first")
		secondCtx := stableTestContext(702, 7, 72, 702, 14, true, "second")
		firstRelease := BeginInflight(firstCtx, stableTestRelayInfo(701, "gpt-inflight"), 701)
		secondRelease := BeginInflight(secondCtx, stableTestRelayInfo(702, "gpt-inflight"), 702)

		assert.Equal(t, int64(1), StableInflightCount(StableInflightKey{PoolMemberID: 71, CredentialID: 701, Model: "gpt-inflight"}))
		assert.Zero(t, StableInflightCount(StableInflightKey{PoolMemberID: 72, CredentialID: 702, Model: "gpt-inflight"}))
		assert.Equal(t, StableStats{Initialized: true, InflightKeys: 1, InflightDrops: 1}, StableRuntimeStats())

		secondRelease()
		firstRelease()
		assert.Equal(t, StableStats{Initialized: true, InflightDrops: 1}, StableRuntimeStats())
	})
}

func TestStableSnapshotExcludesNonMergeablePercentileScalars(t *testing.T) {
	typeOfSnapshot := reflect.TypeOf(StableSnapshot{})
	for _, fieldName := range []string{"LatencyP95Ms", "TtftP95Ms", "P95LatencyMs", "P95TTFTMs"} {
		_, exists := typeOfSnapshot.FieldByName(fieldName)
		assert.False(t, exists, fieldName)
	}
}

func TestStableMetricsBoundExtremeAttemptValuesWithoutOverflow(t *testing.T) {
	enableRoutingMetricsForTest(t)
	ctx := stableTestContext(801, 8, 81, 801, 16, true, "selected")
	info := stableTestRelayInfo(801, "gpt-bounded")
	apiErr := types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	metadata, err := common.Marshal(map[string]int64{"retry_after_ms": math.MaxInt64})
	require.NoError(t, err)
	apiErr.Metadata = metadata

	recordStableClassifiedAttempt(
		ctx,
		info,
		801,
		time.Unix(60, 0),
		math.MaxInt64,
		math.MaxInt64,
		true,
		math.MaxInt64,
		math.MaxInt64,
		false,
		apiErr,
		routingerror.Classification{
			Responsibility: routingerror.ResponsibilityProvider,
			HealthEffect:   routingerror.HealthDegrade,
			Rule:           "provider_429",
		},
	)

	snapshots := StableSnapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(1), snapshots[0].RequestCount)
	assert.Equal(t, routingdistribution.MaxDurationMilliseconds, snapshots[0].TotalLatencyMs)
	assert.Equal(t, routingdistribution.MaxDurationMilliseconds, snapshots[0].TtftSumMs)
	assert.Equal(t, routingdistribution.MaxDurationMilliseconds, snapshots[0].GenerationMs)
	assert.Equal(t, maxStableAttemptTokens, snapshots[0].OutputTokens)
	assert.Equal(t, maxStableRetryAfterMs, snapshots[0].RetryAfterTotalMs)
	assert.GreaterOrEqual(t, snapshots[0].TotalLatencyMs, int64(0))
	assert.GreaterOrEqual(t, StableRuntimeStats().CounterSaturations, int64(5))
	assert.Equal(t, int64(2), StableRuntimeStats().DistributionClamps)
}

func TestStableMetricsRejectInvalidRequeueWithoutPoisoningBucket(t *testing.T) {
	enableRoutingMetricsForTest(t)
	invalid := StableSnapshot{
		PoolID: 9, PoolMemberID: 91, CredentialID: 901, ChannelID: 911,
		Model: "gpt-invalid", BucketTs: 60, LastSnapshotRevision: 17,
		RequestCount: math.MaxInt64,
	}
	RequeueStableSnapshots([]StableSnapshot{invalid})
	assert.Empty(t, StableSnapshots())
	assert.Equal(t, int64(1), StableRuntimeStats().InvalidSnapshotDrops)

	valid := invalid
	valid.RequestCount = 1
	valid.SuccessCount = 1
	valid.ReliabilityRequestCount = 1
	RequeueStableSnapshots([]StableSnapshot{valid})
	snapshots := StableSnapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(1), snapshots[0].RequestCount)
	assert.Equal(t, int64(1), snapshots[0].SuccessCount)
}

func TestStableMetricsRejectMergePastBucketBoundAndCanDrainRequeue(t *testing.T) {
	enableRoutingMetricsForTest(t)
	full := StableSnapshot{
		PoolID: 10, PoolMemberID: 101, CredentialID: 1001, ChannelID: 1011,
		Model: "gpt-full", BucketTs: 60, LastSnapshotRevision: 18,
		RequestCount: maxStableBucketCount,
	}
	RequeueStableSnapshots([]StableSnapshot{full})
	extra := full
	extra.RequestCount = 1
	RequeueStableSnapshots([]StableSnapshot{extra})

	snapshots := StableSnapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, maxStableBucketCount, snapshots[0].RequestCount)
	assert.Equal(t, int64(1), StableRuntimeStats().CounterSaturations)

	drained := DrainStableSnapshots()
	require.Len(t, drained, 1)
	assert.Empty(t, StableSnapshots())
	RequeueStableSnapshots(drained)
	requeued := StableSnapshots()
	require.Len(t, requeued, 1)
	assert.Equal(t, maxStableBucketCount, requeued[0].RequestCount)
}
