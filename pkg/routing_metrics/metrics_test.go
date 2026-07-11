package routingmetrics

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureRoutingMetricsForTest(t *testing.T, enabled bool) {
	t.Helper()
	previousSetting := smart_routing_setting.GetSetting()
	maintenanceMu.Lock()
	previousLimits := limits
	maintenanceMu.Unlock()

	if enabled {
		t.Setenv("SMART_ROUTING_ENABLED", "true")
	} else {
		t.Setenv("SMART_ROUTING_ENABLED", "false")
	}
	t.Setenv("SMART_ROUTING_MODE", smart_routing_setting.ModeObserve)
	ResetForTest()
	setting := previousSetting
	setting.Enabled = enabled
	setting.Mode = smart_routing_setting.ModeObserve
	smart_routing_setting.UpdateSetting(setting)

	t.Cleanup(func() {
		ResetForTest()
		maintenanceMu.Lock()
		limits = previousLimits
		maintenanceMu.Unlock()
		smart_routing_setting.UpdateSetting(previousSetting)
	})
}

func enableRoutingMetricsForTest(t *testing.T) {
	t.Helper()
	configureRoutingMetricsForTest(t, true)
}

func recordTestAttempt(c *gin.Context, info *relaycommon.RelayInfo, channelID int, apiErr *types.NewAPIError) {
	classification := routingerror.ClassifyAPIError(apiErr, routingerror.Context{
		Component: routingerror.ComponentServing,
		Operation: routingerror.OperationRelay,
	})
	RecordClassifiedAttempt(c, info, channelID, apiErr == nil, apiErr, classification)
}

func TestRoutingMetricsDoNotAllocateWhenDisabled(t *testing.T) {
	configureRoutingMetricsForTest(t, false)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 1},
	}

	release := BeginInflight(nil, info, 1)
	recordTestAttempt(nil, info, 1, nil)
	release()

	assert.Empty(t, Snapshots())
	assert.Equal(t, Stats{}, RuntimeStats())
}

func TestRoutingMetricsEnforceBucketLimitAndEvictOldest(t *testing.T) {
	enableRoutingMetricsForTest(t)
	maintenanceMu.Lock()
	limits = Limits{MaxBuckets: 2, BucketTTL: time.Hour, MaxInflightKeys: 2}
	maintenanceMu.Unlock()

	for bucketTs := int64(1); bucketTs <= 3; bucketTs++ {
		recordBucket(bucketKey{
			channelID:   1,
			apiKeyIndex: model.RoutingMetricSingleKeyIndex,
			modelName:   "gpt-test",
			group:       "default",
			bucketTs:    bucketTs,
		}, 1, 0, false, 1, 0, true, nil, routingerror.Classification{})
	}

	snapshots := Snapshots()
	require.Len(t, snapshots, 2)
	assert.Equal(t, []int64{2, 3}, []int64{snapshots[0].BucketTs, snapshots[1].BucketTs})
	assert.Equal(t, Stats{Buckets: 2, BucketEvictions: 1}, RuntimeStats())
}

func TestRoutingMetricsEvictOldestUsesStableTieBreakOrder(t *testing.T) {
	const bucketTs = int64(100)
	type visibleBucketKey struct {
		ChannelID   int
		APIKeyIndex int
		ModelName   string
		Group       string
		BucketTs    int64
	}
	tests := []struct {
		name string
		keys []bucketKey
		want []visibleBucketKey
	}{
		{
			name: "channel",
			keys: []bucketKey{
				{channelID: 1, apiKeyIndex: 3, modelName: "c-model", group: "c-group", bucketTs: bucketTs},
				{channelID: 2, apiKeyIndex: 2, modelName: "b-model", group: "b-group", bucketTs: bucketTs},
				{channelID: 3, apiKeyIndex: 1, modelName: "a-model", group: "a-group", bucketTs: bucketTs},
			},
			want: []visibleBucketKey{
				{ChannelID: 2, APIKeyIndex: 2, ModelName: "b-model", Group: "b-group", BucketTs: bucketTs},
				{ChannelID: 3, APIKeyIndex: 1, ModelName: "a-model", Group: "a-group", BucketTs: bucketTs},
			},
		},
		{
			name: "api_key",
			keys: []bucketKey{
				{channelID: 10, apiKeyIndex: 1, modelName: "c-model", group: "c-group", bucketTs: bucketTs},
				{channelID: 10, apiKeyIndex: 2, modelName: "b-model", group: "b-group", bucketTs: bucketTs},
				{channelID: 10, apiKeyIndex: 3, modelName: "a-model", group: "a-group", bucketTs: bucketTs},
			},
			want: []visibleBucketKey{
				{ChannelID: 10, APIKeyIndex: 2, ModelName: "b-model", Group: "b-group", BucketTs: bucketTs},
				{ChannelID: 10, APIKeyIndex: 3, ModelName: "a-model", Group: "a-group", BucketTs: bucketTs},
			},
		},
		{
			name: "model",
			keys: []bucketKey{
				{channelID: 10, apiKeyIndex: 5, modelName: "a-model", group: "c-group", bucketTs: bucketTs},
				{channelID: 10, apiKeyIndex: 5, modelName: "b-model", group: "b-group", bucketTs: bucketTs},
				{channelID: 10, apiKeyIndex: 5, modelName: "c-model", group: "a-group", bucketTs: bucketTs},
			},
			want: []visibleBucketKey{
				{ChannelID: 10, APIKeyIndex: 5, ModelName: "b-model", Group: "b-group", BucketTs: bucketTs},
				{ChannelID: 10, APIKeyIndex: 5, ModelName: "c-model", Group: "a-group", BucketTs: bucketTs},
			},
		},
		{
			name: "group",
			keys: []bucketKey{
				{channelID: 10, apiKeyIndex: 5, modelName: "same-model", group: "a-group", bucketTs: bucketTs},
				{channelID: 10, apiKeyIndex: 5, modelName: "same-model", group: "b-group", bucketTs: bucketTs},
				{channelID: 10, apiKeyIndex: 5, modelName: "same-model", group: "c-group", bucketTs: bucketTs},
			},
			want: []visibleBucketKey{
				{ChannelID: 10, APIKeyIndex: 5, ModelName: "same-model", Group: "b-group", BucketTs: bucketTs},
				{ChannelID: 10, APIKeyIndex: 5, ModelName: "same-model", Group: "c-group", BucketTs: bucketTs},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			enableRoutingMetricsForTest(t)
			maintenanceMu.Lock()
			limits = Limits{MaxBuckets: 2, BucketTTL: time.Hour, MaxInflightKeys: 2}
			maintenanceMu.Unlock()

			for _, key := range test.keys {
				recordBucket(key, 1, 0, false, 1, 0, true, nil, routingerror.Classification{})
			}

			snapshots := Snapshots()
			require.Len(t, snapshots, 2)
			retained := make([]visibleBucketKey, 0, len(snapshots))
			for _, snapshot := range snapshots {
				retained = append(retained, visibleBucketKey{
					ChannelID:   snapshot.ChannelID,
					APIKeyIndex: snapshot.APIKeyIndex,
					ModelName:   snapshot.ModelName,
					Group:       snapshot.Group,
					BucketTs:    snapshot.BucketTs,
				})
			}
			assert.Equal(t, test.want, retained)
			assert.Equal(t, Stats{Buckets: 2, BucketEvictions: 1}, RuntimeStats())
		})
	}
}

func TestRoutingMetricsEvictExpiredBucketsBeforeCapacity(t *testing.T) {
	enableRoutingMetricsForTest(t)
	maintenanceMu.Lock()
	limits = Limits{MaxBuckets: 2, BucketTTL: 2 * time.Second, MaxInflightKeys: 2}
	maintenanceMu.Unlock()

	for _, bucketTs := range []int64{1, 2, 10} {
		recordBucket(bucketKey{
			channelID:   1,
			apiKeyIndex: model.RoutingMetricSingleKeyIndex,
			modelName:   "gpt-test",
			group:       "default",
			bucketTs:    bucketTs,
		}, 1, 0, false, 1, 0, true, nil, routingerror.Classification{})
	}

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(10), snapshots[0].BucketTs)
	assert.Equal(t, Stats{Buckets: 1, BucketEvictions: 2}, RuntimeStats())
}

func TestRoutingMetricsBucketTTLDoesNotExpirePartialSecond(t *testing.T) {
	enableRoutingMetricsForTest(t)
	maintenanceMu.Lock()
	limits = Limits{MaxBuckets: 2, BucketTTL: 1500 * time.Millisecond, MaxInflightKeys: 2}
	maintenanceMu.Unlock()

	for _, bucketTs := range []int64{9, 10} {
		recordBucket(bucketKey{
			channelID:   1,
			apiKeyIndex: model.RoutingMetricSingleKeyIndex,
			modelName:   "gpt-test",
			group:       "default",
			bucketTs:    bucketTs,
		}, 1, 0, false, 1, 0, true, nil, routingerror.Classification{})
	}

	snapshots := Snapshots()
	require.Len(t, snapshots, 2)
	assert.Equal(t, []int64{9, 10}, []int64{snapshots[0].BucketTs, snapshots[1].BucketTs})
	assert.Equal(t, Stats{Buckets: 2}, RuntimeStats())
}

func TestRoutingMetricsDropNewInflightKeyAtLimit(t *testing.T) {
	enableRoutingMetricsForTest(t)
	maintenanceMu.Lock()
	limits = Limits{MaxBuckets: 2, BucketTTL: time.Hour, MaxInflightKeys: 1}
	maintenanceMu.Unlock()
	firstInfo := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 1},
	}
	secondInfo := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 2},
	}
	firstKey := InflightKey{ChannelID: 1, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	secondKey := InflightKey{ChannelID: 2, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}

	releaseFirst := BeginInflight(nil, firstInfo, 1)
	releaseSecond := BeginInflight(nil, secondInfo, 2)

	assert.Equal(t, int64(1), InflightCount(firstKey))
	assert.Zero(t, InflightCount(secondKey))
	assert.Equal(t, Stats{InflightKeys: 1, InflightDrops: 1}, RuntimeStats())
	releaseSecond()
	releaseFirst()
	assert.Equal(t, Stats{InflightDrops: 1}, RuntimeStats())
}

func TestRoutingMetricsNormalizeNonPositiveLimits(t *testing.T) {
	enableRoutingMetricsForTest(t)
	maintenanceMu.Lock()
	limits = Limits{}
	maintenanceMu.Unlock()
	metricKey := bucketKey{
		channelID:   1,
		apiKeyIndex: model.RoutingMetricSingleKeyIndex,
		modelName:   "gpt-test",
		group:       "default",
		bucketTs:    1,
	}
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 1},
	}
	inflightKey := InflightKey{ChannelID: 1, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}

	recordBucket(metricKey, 1, 0, false, 1, 0, true, nil, routingerror.Classification{})
	release := BeginInflight(nil, info, 1)

	require.Len(t, Snapshots(), 1)
	assert.Equal(t, int64(1), InflightCount(inflightKey))
	assert.Equal(t, Stats{Buckets: 1, InflightKeys: 1}, RuntimeStats())
	release()
	assert.Equal(t, Stats{Buckets: 1}, RuntimeStats())
}

func TestRoutingMetricsConcurrentFinalReleaseAndBeginPreserveActiveKey(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 41},
	}
	key := InflightKey{ChannelID: 41, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	oldRelease := BeginInflight(nil, info, 41)
	require.Equal(t, int64(1), InflightCount(key))

	start := make(chan struct{})
	oldReleaseResult := make(chan struct{}, 1)
	newReleaseResult := make(chan func(), 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		oldRelease()
		oldReleaseResult <- struct{}{}
	}()
	go func() {
		defer wg.Done()
		<-start
		newReleaseResult <- BeginInflight(nil, info, 41)
	}()
	close(start)
	wg.Wait()
	<-oldReleaseResult
	newRelease := <-newReleaseResult

	assert.Equal(t, int64(1), InflightCount(key))
	assert.Equal(t, Stats{InflightKeys: 1}, RuntimeStats())
	newRelease()
	assert.Zero(t, InflightCount(key))
	assert.Equal(t, Stats{}, RuntimeStats())
}

func TestRoutingMetricsClearChannelOldReleaseDoesNotAffectReplacement(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 42},
	}
	key := InflightKey{ChannelID: 42, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	oldRelease := BeginInflight(nil, info, 42)
	require.Equal(t, int64(1), InflightCount(key))

	ClearChannel(42)
	require.Zero(t, InflightCount(key))
	require.Equal(t, Stats{}, RuntimeStats())
	replacementRelease := BeginInflight(nil, info, 42)
	require.Equal(t, int64(1), InflightCount(key))

	oldRelease()
	assert.Equal(t, int64(1), InflightCount(key))
	assert.Equal(t, Stats{InflightKeys: 1}, RuntimeStats())
	replacementRelease()
	assert.Zero(t, InflightCount(key))
	assert.Equal(t, Stats{}, RuntimeStats())
}

func TestRoutingMetricsResetOldReleaseDoesNotAffectReplacement(t *testing.T) {
	enableRoutingMetricsForTest(t)
	testLimits := Limits{MaxBuckets: 2, BucketTTL: time.Hour, MaxInflightKeys: 2}
	maintenanceMu.Lock()
	limits = testLimits
	maintenanceMu.Unlock()
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 43},
	}
	key := InflightKey{ChannelID: 43, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "default"}
	oldRelease := BeginInflight(nil, info, 43)
	require.Equal(t, int64(1), InflightCount(key))

	ResetForTest()
	routingSetting := smart_routing_setting.GetSetting()
	routingSetting.Enabled = true
	routingSetting.Mode = smart_routing_setting.ModeObserve
	smart_routing_setting.UpdateSetting(routingSetting)
	maintenanceMu.Lock()
	limits = testLimits
	maintenanceMu.Unlock()
	require.Zero(t, InflightCount(key))
	require.Equal(t, Stats{}, RuntimeStats())
	replacementRelease := BeginInflight(nil, info, 43)
	require.Equal(t, int64(1), InflightCount(key))

	oldRelease()
	assert.Equal(t, int64(1), InflightCount(key))
	assert.Equal(t, Stats{InflightKeys: 1}, RuntimeStats())
	replacementRelease()
	assert.Zero(t, InflightCount(key))
	assert.Equal(t, Stats{}, RuntimeStats())
}

func TestRecordClassifiedAttemptNormalizesSingleKeyAndCapturesTiming(t *testing.T) {
	enableRoutingMetricsForTest(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set(string(constant.ContextKeyUsingGroup), "default")

	start := time.Now().Add(-2 * time.Second)
	info := &relaycommon.RelayInfo{
		UsingGroup:        "default",
		OriginModelName:   "gpt-test",
		StartTime:         start,
		FirstResponseTime: start.Add(300 * time.Millisecond),
		IsStream:          true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         11,
			ChannelIsMultiKey: false,
		},
	}

	recordTestAttempt(ctx, info, 11, nil)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	metric := snapshots[0]
	assert.Equal(t, 11, metric.ChannelID)
	assert.Equal(t, -1, metric.APIKeyIndex)
	assert.Equal(t, "gpt-test", metric.ModelName)
	assert.Equal(t, "default", metric.Group)
	assert.Equal(t, int64(1), metric.RequestCount)
	assert.Equal(t, int64(1), metric.SuccessCount)
	assert.GreaterOrEqual(t, metric.TotalLatencyMs, int64(1900))
	assert.Equal(t, int64(300), metric.TtftSumMs)
	assert.Equal(t, int64(1), metric.TtftCount)
}

func TestRecordClassifiedAttemptCapturesOutputTokensAndGenerationDuration(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		StartTime:       time.Now().Add(-10 * time.Second),
		IsStream:        true,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 28},
	}
	info.ResetStreamAttemptState()
	attemptStart := info.RoutingAttemptStartTime()
	info.FirstResponseTime = attemptStart.Add(500 * time.Millisecond)
	info.ObserveRoutingOutputTokens(150)
	time.Sleep(550 * time.Millisecond)

	recordTestAttempt(nil, info, 28, nil)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(150), snapshots[0].OutputTokens)
	assert.Less(t, snapshots[0].TotalLatencyMs, int64(2000))
	assert.Less(t, snapshots[0].TtftP95Ms, int64(1000))
	assert.Greater(t, snapshots[0].GenerationMs, int64(0))
}

func TestRecordClassifiedAttemptDoesNotAddGenerationWithoutOutputTokens(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		StartTime:       time.Now().Add(-10 * time.Second),
		IsStream:        true,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 29},
	}
	info.ResetStreamAttemptState()
	time.Sleep(10 * time.Millisecond)

	recordTestAttempt(nil, info, 29, nil)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Zero(t, snapshots[0].OutputTokens)
	assert.Zero(t, snapshots[0].GenerationMs)
}

func TestRecordClassifiedAttemptCapturesErrorStatus(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         22,
			ChannelIsMultiKey: false,
		},
	}

	recordTestAttempt(nil, info, 22, types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests))
	recordTestAttempt(nil, info, 22, types.NewErrorWithStatusCode(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway))
	recordTestAttempt(nil, info, 22, types.NewErrorWithStatusCode(errors.New("bad request"), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest))

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	for _, metric := range snapshots {
		assert.Equal(t, model.RoutingMetricSingleKeyIndex, metric.APIKeyIndex)
		assert.Equal(t, int64(3), metric.RequestCount)
		assert.Zero(t, metric.SuccessCount)
		assert.Equal(t, int64(1), metric.Err429)
		assert.Equal(t, int64(1), metric.Err5xx)
		assert.Equal(t, int64(1), metric.Err4xx)
	}
}

func TestRecordClassifiedAttemptSeparatesReliabilityFromCapacityAndCallerErrors(t *testing.T) {
	tests := []struct {
		name           string
		success        bool
		status         int
		classification routingerror.Classification
		want           model.RoutingChannelMetric
	}{
		{
			name:    "success enters reliability sample",
			success: true,
			classification: routingerror.Classification{
				HealthEffect: routingerror.HealthIgnore,
			},
			want: model.RoutingChannelMetric{RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1},
		},
		{
			name:   "provider 502 is reliability failure",
			status: http.StatusBadGateway,
			classification: routingerror.Classification{
				Responsibility: routingerror.ResponsibilityProvider,
				HealthEffect:   routingerror.HealthDegrade,
			},
			want: model.RoutingChannelMetric{RequestCount: 1, ReliabilityRequestCount: 1, ReliabilityFailureCount: 1, Err5xx: 1},
		},
		{
			name:   "network failure is reliability failure",
			status: http.StatusGatewayTimeout,
			classification: routingerror.Classification{
				Responsibility: routingerror.ResponsibilityNetwork,
				HealthEffect:   routingerror.HealthDegrade,
			},
			want: model.RoutingChannelMetric{RequestCount: 1, ReliabilityRequestCount: 1, ReliabilityFailureCount: 1, Err5xx: 1},
		},
		{
			name:   "429 is capacity only",
			status: http.StatusTooManyRequests,
			classification: routingerror.Classification{
				Responsibility: routingerror.ResponsibilityCapacity,
				HealthEffect:   routingerror.HealthIgnore,
				CapacityEffect: routingerror.CapacityCooldown,
			},
			want: model.RoutingChannelMetric{RequestCount: 1, Err429: 1},
		},
		{
			name:   "529 is capacity only and not generic 5xx",
			status: 529,
			classification: routingerror.Classification{
				Responsibility: routingerror.ResponsibilityCapacity,
				HealthEffect:   routingerror.HealthIgnore,
				CapacityEffect: routingerror.CapacityCooldown,
			},
			want: model.RoutingChannelMetric{RequestCount: 1, Err529: 1},
		},
		{
			name:   "caller 400 does not enter reliability sample",
			status: http.StatusBadRequest,
			classification: routingerror.Classification{
				Responsibility: routingerror.ResponsibilityCaller,
				HealthEffect:   routingerror.HealthIgnore,
			},
			want: model.RoutingChannelMetric{RequestCount: 1, Err4xx: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enableRoutingMetricsForTest(t)
			info := &relaycommon.RelayInfo{
				UsingGroup:      "default",
				OriginModelName: "gpt-test",
				StartTime:       time.Now(),
				ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 101},
			}
			var apiErr *types.NewAPIError
			if tt.status != 0 {
				apiErr = types.NewErrorWithStatusCode(errors.New("attempt failed"), types.ErrorCodeBadResponseStatusCode, tt.status)
			}

			RecordClassifiedAttempt(nil, info, 101, tt.success, apiErr, tt.classification)

			snapshots := Snapshots()
			require.Len(t, snapshots, 1)
			got := snapshots[0]
			assert.Equal(t, tt.want.RequestCount, got.RequestCount)
			assert.Equal(t, tt.want.SuccessCount, got.SuccessCount)
			assert.Equal(t, tt.want.ReliabilityRequestCount, got.ReliabilityRequestCount)
			assert.Equal(t, tt.want.ReliabilityFailureCount, got.ReliabilityFailureCount)
			assert.Equal(t, tt.want.Err4xx, got.Err4xx)
			assert.Equal(t, tt.want.Err5xx, got.Err5xx)
			assert.Equal(t, tt.want.Err429, got.Err429)
			assert.Equal(t, tt.want.Err529, got.Err529)
		})
	}
}

func TestRecordClassifiedAttemptUsesSourceStatusAndSeparates529(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 102},
	}
	mapped429 := types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	mapped429.SetResponseStatusCode(http.StatusServiceUnavailable)
	errorsToRecord := []*types.NewAPIError{
		mapped429,
		types.NewErrorWithStatusCode(errors.New("overloaded"), types.ErrorCodeBadResponseStatusCode, 529),
		types.NewErrorWithStatusCode(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway),
	}

	for _, apiErr := range errorsToRecord {
		RecordClassifiedAttempt(nil, info, 102, false, apiErr, routingerror.ClassifyAPIError(apiErr, routingerror.Context{
			Component: routingerror.ComponentServing,
			Operation: routingerror.OperationRelay,
		}))
	}

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(1), snapshots[0].Err429)
	assert.Equal(t, int64(1), snapshots[0].Err529)
	assert.Equal(t, int64(1), snapshots[0].Err5xx)
}

func TestRoutingMetricsIgnoreCurrentMultiKeyAttempt(t *testing.T) {
	enableRoutingMetricsForTest(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, true)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         25,
			ChannelIsMultiKey: false,
		},
	}
	aggregate := InflightKey{ChannelID: 25, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
	perKey := InflightKey{ChannelID: 25, APIKeyIndex: 3, Model: "gpt-test", Group: "vip"}

	release := BeginInflight(ctx, info, 25)
	recordTestAttempt(ctx, info, 25, nil)

	assert.Empty(t, Snapshots())
	assert.Zero(t, InflightCount(aggregate))
	assert.Zero(t, InflightCount(perKey))
	assert.Equal(t, Stats{}, RuntimeStats())
	release()
	assert.Equal(t, Stats{}, RuntimeStats())
}

func TestRoutingMetricsUseOnlyMinusOneForCurrentSingleKeyAttempt(t *testing.T) {
	enableRoutingMetricsForTest(t)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelIsMultiKey, false)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:            26,
			ChannelIsMultiKey:    true,
			ChannelMultiKeyIndex: 2,
		},
	}
	aggregate := InflightKey{ChannelID: 26, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
	perKey := InflightKey{ChannelID: 26, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"}

	release := BeginInflight(ctx, info, 26)
	recordTestAttempt(ctx, info, 26, nil)

	assert.Equal(t, int64(1), InflightCount(aggregate))
	assert.Zero(t, InflightCount(perKey))
	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, snapshots[0].APIKeyIndex)
	assert.Equal(t, Stats{Buckets: 1, InflightKeys: 1}, RuntimeStats())
	release()
	assert.Zero(t, InflightCount(aggregate))
	assert.Zero(t, InflightCount(perKey))
	assert.Equal(t, Stats{Buckets: 1}, RuntimeStats())
}

func TestRecordClassifiedAttemptCapturesRetryAfterMax(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 23},
	}
	firstMetadata, err := common.Marshal(map[string]int64{"retry_after_ms": 1500})
	require.NoError(t, err)
	secondMetadata, err := common.Marshal(map[string]int64{"retry_after_ms": 2500})
	require.NoError(t, err)
	firstErr := types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	firstErr.Metadata = firstMetadata
	secondErr := types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests)
	secondErr.Metadata = secondMetadata

	recordTestAttempt(nil, info, 23, firstErr)
	recordTestAttempt(nil, info, 23, secondErr)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(2500), snapshots[0].RetryAfterMaxMs)
}

func TestRecordClassifiedAttemptComputesLatencyAndTTFTP95(t *testing.T) {
	enableRoutingMetricsForTest(t)
	now := time.Now()
	info := &relaycommon.RelayInfo{
		UsingGroup:        "vip",
		OriginModelName:   "gpt-test",
		IsStream:          true,
		ChannelMeta:       &relaycommon.ChannelMeta{ChannelId: 27},
		FirstResponseTime: now,
	}

	for _, duration := range []time.Duration{100, 200, 300, 400, 500} {
		start := time.Now().Add(-duration * time.Millisecond)
		info.StartTime = start
		info.FirstResponseTime = start.Add(duration * time.Millisecond)
		recordTestAttempt(nil, info, 27, nil)
	}

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(5), snapshots[0].RequestCount)
	assert.InDelta(t, 500, snapshots[0].LatencyP95Ms, 20)
	assert.InDelta(t, 500, snapshots[0].TtftP95Ms, 20)
}

func TestInflightCountersUseRoutingKeyAndReleaseOnce(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:         24,
			ChannelIsMultiKey: false,
		},
	}
	key := InflightKey{
		ChannelID:   24,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       "gpt-test",
		Group:       "vip",
	}

	release := BeginInflight(nil, info, 24)
	assert.Equal(t, int64(1), InflightCount(key))
	release()
	release()

	assert.Zero(t, InflightCount(key))
}

func TestRoutingMetricsConcurrentDrainAndRecordPreserveAttempts(t *testing.T) {
	enableRoutingMetricsForTest(t)
	key := bucketKey{
		channelID:   44,
		apiKeyIndex: model.RoutingMetricSingleKeyIndex,
		modelName:   "gpt-test",
		group:       "default",
		bucketTs:    1,
	}
	recordBucket(key, 1, 0, false, 1, 0, true, nil, routingerror.Classification{})
	value, ok := buckets.Load(key)
	require.True(t, ok)
	b := value.(*bucket)

	b.mu.Lock()
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	drainResult := make(chan []model.RoutingChannelMetric, 1)
	recordResult := make(chan struct{}, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ready <- struct{}{}
		<-start
		drainResult <- DrainSnapshots()
	}()
	go func() {
		defer wg.Done()
		ready <- struct{}{}
		<-start
		recordBucket(key, 1, 0, false, 1, 0, true, nil, routingerror.Classification{})
		recordResult <- struct{}{}
	}()
	<-ready
	<-ready
	close(start)
	b.mu.Unlock()
	wg.Wait()
	drained := <-drainResult
	<-recordResult

	// The second record may be drained or remain in a replacement bucket.
	remaining := Snapshots()
	totalRequests := int64(0)
	for _, snapshot := range drained {
		totalRequests += snapshot.RequestCount
	}
	for _, snapshot := range remaining {
		totalRequests += snapshot.RequestCount
	}
	assert.Equal(t, int64(2), totalRequests)
	assert.Equal(t, Stats{Buckets: int64(len(remaining))}, RuntimeStats())
}

func TestDrainSnapshotsClearsInMemoryBuckets(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 33},
	}
	recordTestAttempt(nil, info, 33, nil)

	first := DrainSnapshots()
	require.Len(t, first, 1)
	assert.Empty(t, DrainSnapshots())
	assert.Empty(t, Snapshots())
}

func TestRequeueSnapshotsRestoresDrainedBuckets(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 34},
	}
	recordTestAttempt(nil, info, 34, nil)
	drained := DrainSnapshots()
	require.Len(t, drained, 1)
	require.Empty(t, Snapshots())

	RequeueSnapshots(drained)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, drained[0].ChannelID, snapshots[0].ChannelID)
	assert.Equal(t, drained[0].ModelName, snapshots[0].ModelName)
	assert.Equal(t, drained[0].RequestCount, snapshots[0].RequestCount)
	assert.Equal(t, drained[0].SuccessCount, snapshots[0].SuccessCount)
}

func TestDrainAndRequeuePreserveReliabilityCounters(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 35},
	}
	for _, statusCode := range []int{http.StatusBadGateway, http.StatusTooManyRequests, 529} {
		apiErr := types.NewErrorWithStatusCode(errors.New("attempt failed"), types.ErrorCodeBadResponseStatusCode, statusCode)
		RecordClassifiedAttempt(nil, info, 35, false, apiErr, routingerror.ClassifyAPIError(apiErr, routingerror.Context{
			Component: routingerror.ComponentServing,
			Operation: routingerror.OperationRelay,
		}))
	}

	drained := DrainSnapshots()
	require.Len(t, drained, 1)
	require.Empty(t, Snapshots())
	RequeueSnapshots(drained)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(3), snapshots[0].RequestCount)
	assert.Equal(t, int64(1), snapshots[0].ReliabilityRequestCount)
	assert.Equal(t, int64(1), snapshots[0].ReliabilityFailureCount)
	assert.Equal(t, int64(1), snapshots[0].Err5xx)
	assert.Equal(t, int64(1), snapshots[0].Err429)
	assert.Equal(t, int64(1), snapshots[0].Err529)
}
