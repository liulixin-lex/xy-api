package routingmetrics

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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
		PoolID: 2, PoolMemberID: 61, CredentialID: 601, ChannelID: 602,
		Model: "gpt-merge", BucketTs: 60, LastSnapshotRevision: 13,
		RequestCount: 3, SuccessCount: 2, FailureCount: 1,
		ReliabilityRequestCount: 2, ReliabilityFailureCount: 1,
		TotalLatencyMs: 700, TtftSumMs: 120, TtftCount: 2,
		OutputTokens: 30, GenerationMs: 300, Err429: 1,
		RetryAfterCount: 2, RetryAfterTotalMs: 2500,
	}
	RequeueStableSnapshots([]StableSnapshot{first, second})

	merged := StableSnapshots()
	require.Len(t, merged, 1)
	assert.Equal(t, 2, merged[0].PoolID)
	assert.Equal(t, 602, merged[0].ChannelID)
	assert.Equal(t, uint64(13), merged[0].LastSnapshotRevision)
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
