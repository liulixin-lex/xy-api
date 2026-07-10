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

func TestRoutingMetricsDoNotAllocateWhenDisabled(t *testing.T) {
	configureRoutingMetricsForTest(t, false)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 1},
	}

	release := BeginInflight(nil, info, 1)
	RecordAttempt(nil, info, 1, nil)
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
		}, 1, 0, false, 1, nil)
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
				recordBucket(key, 1, 0, false, 1, nil)
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
		}, 1, 0, false, 1, nil)
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
		}, 1, 0, false, 1, nil)
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

	recordBucket(metricKey, 1, 0, false, 1, nil)
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

func TestRecordAttemptNormalizesSingleKeyAndCapturesTiming(t *testing.T) {
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

	RecordAttempt(ctx, info, 11, nil)

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

func TestRecordAttemptClassifiesErrorStatus(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:            22,
			ChannelIsMultiKey:    true,
			ChannelMultiKeyIndex: 3,
		},
	}

	RecordAttempt(nil, info, 22, types.NewErrorWithStatusCode(errors.New("rate limited"), types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests))
	RecordAttempt(nil, info, 22, types.NewErrorWithStatusCode(errors.New("bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway))
	RecordAttempt(nil, info, 22, types.NewErrorWithStatusCode(errors.New("bad request"), types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest))

	snapshots := Snapshots()
	require.Len(t, snapshots, 2)
	for _, metric := range snapshots {
		assert.Contains(t, []int{model.RoutingMetricSingleKeyIndex, 3}, metric.APIKeyIndex)
		assert.Equal(t, int64(3), metric.RequestCount)
		assert.Zero(t, metric.SuccessCount)
		assert.Equal(t, int64(1), metric.Err429)
		assert.Equal(t, int64(1), metric.Err5xx)
		assert.Equal(t, int64(1), metric.Err4xx)
	}
}

func TestRecordAttemptAddsAggregateSnapshotForMultiKeyChannels(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		StartTime:       time.Now(),
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:            25,
			ChannelIsMultiKey:    true,
			ChannelMultiKeyIndex: 3,
		},
	}

	RecordAttempt(nil, info, 25, nil)

	snapshots := Snapshots()
	require.Len(t, snapshots, 2)
	assert.Equal(t, model.RoutingMetricSingleKeyIndex, snapshots[0].APIKeyIndex)
	assert.Equal(t, 3, snapshots[1].APIKeyIndex)
	for _, metric := range snapshots {
		assert.Equal(t, 25, metric.ChannelID)
		assert.Equal(t, "gpt-test", metric.ModelName)
		assert.Equal(t, "vip", metric.Group)
		assert.Equal(t, int64(1), metric.RequestCount)
		assert.Equal(t, int64(1), metric.SuccessCount)
	}
}

func TestInflightCountersAlsoTrackAggregateForMultiKeyChannels(t *testing.T) {
	enableRoutingMetricsForTest(t)
	info := &relaycommon.RelayInfo{
		UsingGroup:      "vip",
		OriginModelName: "gpt-test",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:            26,
			ChannelIsMultiKey:    true,
			ChannelMultiKeyIndex: 2,
		},
	}
	aggregate := InflightKey{ChannelID: 26, APIKeyIndex: model.RoutingMetricSingleKeyIndex, Model: "gpt-test", Group: "vip"}
	perKey := InflightKey{ChannelID: 26, APIKeyIndex: 2, Model: "gpt-test", Group: "vip"}

	release := BeginInflight(nil, info, 26)

	assert.Equal(t, int64(1), InflightCount(aggregate))
	assert.Equal(t, int64(1), InflightCount(perKey))
	release()
	assert.Zero(t, InflightCount(aggregate))
	assert.Zero(t, InflightCount(perKey))
}

func TestRecordAttemptCapturesRetryAfterMax(t *testing.T) {
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

	RecordAttempt(nil, info, 23, firstErr)
	RecordAttempt(nil, info, 23, secondErr)

	snapshots := Snapshots()
	require.Len(t, snapshots, 1)
	assert.Equal(t, int64(2500), snapshots[0].RetryAfterMaxMs)
}

func TestRecordAttemptComputesLatencyAndTTFTP95(t *testing.T) {
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
		RecordAttempt(nil, info, 27, nil)
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
			ChannelId:            24,
			ChannelIsMultiKey:    true,
			ChannelMultiKeyIndex: 2,
		},
	}
	key := InflightKey{
		ChannelID:   24,
		APIKeyIndex: 2,
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
	recordBucket(key, 1, 0, false, 1, nil)
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
		recordBucket(key, 1, 0, false, 1, nil)
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
	RecordAttempt(nil, info, 33, nil)

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
	RecordAttempt(nil, info, 34, nil)
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
