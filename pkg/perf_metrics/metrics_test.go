package perfmetrics

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	appcore "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"

	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type rejectingRedisHook struct {
	commandCount atomic.Int64
}

func (h *rejectingRedisHook) BeforeProcess(ctx context.Context, _ redis.Cmder) (context.Context, error) {
	h.commandCount.Add(1)
	return ctx, errors.New("unexpected Redis command")
}

func (*rejectingRedisHook) AfterProcess(context.Context, redis.Cmder) error {
	return nil
}

func (h *rejectingRedisHook) BeforeProcessPipeline(ctx context.Context, commands []redis.Cmder) (context.Context, error) {
	h.commandCount.Add(int64(len(commands)))
	return ctx, errors.New("unexpected Redis pipeline")
}

func (*rejectingRedisHook) AfterProcessPipeline(context.Context, []redis.Cmder) error {
	return nil
}

func configurePerfMetricsForTest(t *testing.T, enabled bool) {
	t.Helper()
	previousSetting := perf_metrics_setting.GetSetting()
	maintenanceMu.Lock()
	previousLimits := limits
	maintenanceMu.Unlock()

	resetForTest()
	setting := previousSetting
	setting.Enabled = enabled
	perf_metrics_setting.UpdateSetting(setting)

	t.Cleanup(func() {
		resetForTest()
		maintenanceMu.Lock()
		limits = previousLimits
		maintenanceMu.Unlock()
		perf_metrics_setting.UpdateSetting(previousSetting)
	})
}

func TestRecordRelaySampleUsesLocalStoreWithoutRedis(t *testing.T) {
	configurePerfMetricsForTest(t, true)

	db, err := gorm.Open(sqlite.Open("file:perf_metrics_local_store?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.PerfMetric{}))
	previousDB := model.DB
	model.DB = db

	hook := &rejectingRedisHook{}
	redisClient := redis.NewClient(&redis.Options{
		Addr: "unused:0",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("unexpected Redis network access")
		},
		MaxRetries: -1,
	})
	redisClient.AddHook(hook)
	previousRedisEnabled := appcore.RedisEnabled
	previousRDB := appcore.RDB
	appcore.RedisEnabled = true
	appcore.RDB = redisClient
	t.Cleanup(func() {
		appcore.RedisEnabled = previousRedisEnabled
		appcore.RDB = previousRDB
		_ = redisClient.Close()
		model.DB = previousDB
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			_ = sqlDB.Close()
		}
	})

	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-local",
		UsingGroup:      "default",
		StartTime:       time.Now().Add(-time.Second),
		RetryIndex:      2,
	}
	RecordRelaySample(info, true, 40)

	assert.Zero(t, hook.commandCount.Load())
	require.Equal(t, Stats{Buckets: 1}, RuntimeStats())
	var activeKey bucketKey
	var activeCounters counters
	hotBuckets.Range(func(key, value any) bool {
		activeKey = key.(bucketKey)
		activeCounters = value.(*bucket).snapshot()
		return false
	})
	assert.Equal(t, int64(1), activeCounters.requestCount, "one logical request must remain one sample after retries")
	assert.Equal(t, int64(1), activeCounters.successCount)
	assert.Equal(t, int64(40), activeCounters.outputTokens)

	beforeFlush, err := Query(QueryParams{Model: info.OriginModelName, Hours: 1})
	require.NoError(t, err)
	require.Len(t, beforeFlush.Groups, 1)
	assert.Equal(t, "default", beforeFlush.Groups[0].Group)
	assert.Equal(t, float64(100), beforeFlush.Groups[0].SuccessRate)

	require.NoError(t, flushBucketsWith(context.Background(), activeKey.bucketTs+1, false, model.UpsertPerfMetricContext))
	assert.Equal(t, Stats{}, RuntimeStats())
	afterFlush, err := Query(QueryParams{Model: info.OriginModelName, Hours: 1})
	require.NoError(t, err)
	assert.Equal(t, beforeFlush, afterFlush)
	assert.Zero(t, hook.commandCount.Load())
}

func TestPerfMetricsEnforceBucketLimit(t *testing.T) {
	configurePerfMetricsForTest(t, true)
	maintenanceMu.Lock()
	limits = Limits{MaxBuckets: 2, BucketTTL: time.Hour}
	maintenanceMu.Unlock()

	for bucketTs := int64(1); bucketTs <= 3; bucketTs++ {
		require.True(t, recordSample(bucketKey{
			model:    "gpt-test",
			group:    "default",
			bucketTs: bucketTs,
		}, Sample{Model: "gpt-test", Group: "default", Success: true}))
	}

	assert.Equal(t, Stats{
		Buckets:        2,
		EvictedBuckets: 1,
		EvictedSamples: 1,
	}, RuntimeStats())
}

func TestPerfMetricsCapacityProtectsCurrentAndNewerBuckets(t *testing.T) {
	tests := []struct {
		name     string
		existing [2]bucketKey
		incoming bucketKey
	}{
		{
			name: "late older bucket",
			existing: [2]bucketKey{
				{model: "gpt-test", group: "default", bucketTs: 10},
				{model: "gpt-test", group: "default", bucketTs: 11},
			},
			incoming: bucketKey{model: "gpt-test", group: "default", bucketTs: 9},
		},
		{
			name: "same timestamp cardinality",
			existing: [2]bucketKey{
				{model: "gpt-a", group: "default", bucketTs: 10},
				{model: "gpt-b", group: "default", bucketTs: 10},
			},
			incoming: bucketKey{model: "gpt-c", group: "default", bucketTs: 10},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configurePerfMetricsForTest(t, true)
			maintenanceMu.Lock()
			limits = Limits{MaxBuckets: 2, BucketTTL: time.Hour}
			maintenanceMu.Unlock()

			for _, key := range test.existing {
				require.True(t, recordSample(key, Sample{Model: key.model, Group: key.group, Success: true}))
			}
			assert.False(t, recordSample(test.incoming, Sample{
				Model:   test.incoming.model,
				Group:   test.incoming.group,
				Success: true,
			}))

			for _, key := range test.existing {
				_, exists := hotBuckets.Load(key)
				assert.True(t, exists)
			}
			_, exists := hotBuckets.Load(test.incoming)
			assert.False(t, exists)
			assert.Equal(t, Stats{Buckets: 2, DroppedSamples: 1}, RuntimeStats())
		})
	}
}

func TestPerfMetricsEvictStaleBucketsBeforeCapacity(t *testing.T) {
	configurePerfMetricsForTest(t, true)
	maintenanceMu.Lock()
	limits = Limits{MaxBuckets: 3, BucketTTL: 2 * time.Second}
	maintenanceMu.Unlock()

	for _, bucketTs := range []int64{1, 2, 10} {
		require.True(t, recordSample(bucketKey{
			model:    "gpt-test",
			group:    "default",
			bucketTs: bucketTs,
		}, Sample{Model: "gpt-test", Group: "default", Success: true}))
	}

	assert.Equal(t, Stats{
		Buckets:        1,
		EvictedBuckets: 2,
		EvictedSamples: 2,
	}, RuntimeStats())
}

func TestFlushCompletedBucketRemovesItImmediately(t *testing.T) {
	configurePerfMetricsForTest(t, true)
	key := bucketKey{model: "gpt-test", group: "default", bucketTs: 100}
	require.True(t, recordSample(key, Sample{
		Model:        key.model,
		Group:        key.group,
		LatencyMs:    250,
		TtftMs:       50,
		HasTtft:      true,
		Success:      true,
		OutputTokens: 40,
		GenerationMs: 200,
	}))

	var persisted model.PerfMetric
	require.NoError(t, flushBucketsWith(context.Background(), 101, false, func(_ context.Context, metric *model.PerfMetric) error {
		persisted = *metric
		return nil
	}))

	assert.Equal(t, model.PerfMetric{
		ModelName:      key.model,
		Group:          key.group,
		BucketTs:       key.bucketTs,
		RequestCount:   1,
		SuccessCount:   1,
		TotalLatencyMs: 250,
		TtftSumMs:      50,
		TtftCount:      1,
		OutputTokens:   40,
		GenerationMs:   200,
	}, persisted)
	_, exists := hotBuckets.Load(key)
	assert.False(t, exists)
	assert.Equal(t, Stats{}, RuntimeStats())
}

func TestFlushFailureRequeuesCompleteBucket(t *testing.T) {
	configurePerfMetricsForTest(t, true)
	key := bucketKey{model: "gpt-test", group: "default", bucketTs: 100}
	require.True(t, recordSample(key, Sample{
		Model:        key.model,
		Group:        key.group,
		LatencyMs:    250,
		TtftMs:       50,
		HasTtft:      true,
		Success:      true,
		OutputTokens: 40,
		GenerationMs: 200,
	}))

	persistStarted := make(chan struct{})
	releasePersist := make(chan struct{})
	persistErr := errors.New("database unavailable")
	flushErr := make(chan error, 1)
	go func() {
		flushErr <- flushBucketsWith(context.Background(), 101, false, func(context.Context, *model.PerfMetric) error {
			close(persistStarted)
			<-releasePersist
			return persistErr
		})
	}()
	<-persistStarted
	require.True(t, recordSample(key, Sample{
		Model:        key.model,
		Group:        key.group,
		LatencyMs:    150,
		Success:      false,
		OutputTokens: 20,
		GenerationMs: 100,
	}))
	close(releasePersist)
	require.ErrorIs(t, <-flushErr, persistErr)

	actual, exists := hotBuckets.Load(key)
	require.True(t, exists)
	assert.Equal(t, counters{
		requestCount:   2,
		successCount:   1,
		totalLatencyMs: 400,
		ttftSumMs:      50,
		ttftCount:      1,
		outputTokens:   60,
		generationMs:   300,
	}, actual.(*bucket).snapshot())
	assert.Equal(t, Stats{Buckets: 1}, RuntimeStats())
}

func TestFlushFailurePreservesReservedBucketWhenCapacityIsContended(t *testing.T) {
	configurePerfMetricsForTest(t, true)
	maintenanceMu.Lock()
	limits = Limits{MaxBuckets: 1, BucketTTL: time.Hour}
	maintenanceMu.Unlock()

	oldKey := bucketKey{model: "gpt-old", group: "default", bucketTs: 100}
	require.True(t, recordSample(oldKey, Sample{
		Model:        oldKey.model,
		Group:        oldKey.group,
		LatencyMs:    250,
		TtftMs:       50,
		HasTtft:      true,
		Success:      true,
		OutputTokens: 40,
		GenerationMs: 200,
	}))

	persistErr := errors.New("database unavailable")
	err := flushBucketsWith(context.Background(), 101, false, func(context.Context, *model.PerfMetric) error {
		newKey := bucketKey{model: "gpt-new", group: "default", bucketTs: 200}
		assert.False(t, recordSample(newKey, Sample{Model: newKey.model, Group: newKey.group, Success: true}))
		return persistErr
	})

	require.ErrorIs(t, err, persistErr)
	actual, exists := hotBuckets.Load(oldKey)
	require.True(t, exists)
	assert.Equal(t, counters{
		requestCount:   1,
		successCount:   1,
		totalLatencyMs: 250,
		ttftSumMs:      50,
		ttftCount:      1,
		outputTokens:   40,
		generationMs:   200,
	}, actual.(*bucket).snapshot())
	assert.Equal(t, Stats{Buckets: 1, DroppedSamples: 1}, RuntimeStats(), "only the competing new sample may be dropped")
}

func TestFlushCanceledContextReturnsBeforeTakingBucketLock(t *testing.T) {
	configurePerfMetricsForTest(t, true)
	key := bucketKey{model: "gpt-canceled", group: "default", bucketTs: 100}
	require.True(t, recordSample(key, Sample{Model: key.model, Group: key.group, Success: true}))
	actual, ok := hotBuckets.Load(key)
	require.True(t, ok)
	bucket := actual.(*bucket)
	bucket.mu.Lock()
	locked := true
	defer func() {
		if locked {
			bucket.mu.Unlock()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() {
		result <- flushBucketsWith(ctx, 101, false, model.UpsertPerfMetricContext)
	}()

	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		bucket.mu.Unlock()
		locked = false
		<-result
		require.Fail(t, "canceled flush blocked on a bucket lock")
	}
}

func TestFlushConcurrentRecordPreservesExactPersistedAndHotTotals(t *testing.T) {
	configurePerfMetricsForTest(t, true)
	key := bucketKey{model: "gpt-test", group: "default", bucketTs: 100}
	require.True(t, recordSample(key, Sample{
		Model:        key.model,
		Group:        key.group,
		LatencyMs:    100,
		TtftMs:       10,
		HasTtft:      true,
		Success:      true,
		OutputTokens: 20,
		GenerationMs: 100,
	}))

	persistStarted := make(chan struct{})
	releasePersist := make(chan struct{})
	flushErr := make(chan error, 1)
	var persisted model.PerfMetric
	go func() {
		flushErr <- flushBucketsWith(context.Background(), 101, false, func(_ context.Context, metric *model.PerfMetric) error {
			persisted = *metric
			close(persistStarted)
			<-releasePersist
			return nil
		})
	}()
	<-persistStarted

	recorded := make(chan bool, 1)
	go func() {
		recorded <- recordSample(key, Sample{
			Model:        key.model,
			Group:        key.group,
			LatencyMs:    300,
			Success:      false,
			OutputTokens: 40,
			GenerationMs: 200,
		})
	}()
	require.True(t, <-recorded)
	close(releasePersist)
	require.NoError(t, <-flushErr)

	actual, exists := hotBuckets.Load(key)
	require.True(t, exists)
	hot := actual.(*bucket).snapshot()
	assert.Equal(t, counters{
		requestCount:   1,
		totalLatencyMs: 300,
		outputTokens:   40,
		generationMs:   200,
	}, hot)
	assert.Equal(t, counters{
		requestCount:   2,
		successCount:   1,
		totalLatencyMs: 400,
		ttftSumMs:      10,
		ttftCount:      1,
		outputTokens:   60,
		generationMs:   300,
	}, counters{
		requestCount:   persisted.RequestCount + hot.requestCount,
		successCount:   persisted.SuccessCount + hot.successCount,
		totalLatencyMs: persisted.TotalLatencyMs + hot.totalLatencyMs,
		ttftSumMs:      persisted.TtftSumMs + hot.ttftSumMs,
		ttftCount:      persisted.TtftCount + hot.ttftCount,
		outputTokens:   persisted.OutputTokens + hot.outputTokens,
		generationMs:   persisted.GenerationMs + hot.generationMs,
	})
	assert.Equal(t, Stats{Buckets: 1}, RuntimeStats())
}

func TestDisabledMetricsDoNotAllocateButMaintainExistingBuckets(t *testing.T) {
	configurePerfMetricsForTest(t, true)
	Record(Sample{Model: "gpt-existing", Group: "default", Success: true})
	require.Equal(t, Stats{Buckets: 1}, RuntimeStats())

	setting := perf_metrics_setting.GetSetting()
	setting.Enabled = false
	setting.RetentionDays = 0
	perf_metrics_setting.UpdateSetting(setting)
	Record(Sample{Model: "gpt-disabled", Group: "default", Success: true})
	require.Equal(t, Stats{Buckets: 1}, RuntimeStats())

	var existingKey bucketKey
	hotBuckets.Range(func(key any, _ any) bool {
		existingKey = key.(bucketKey)
		return false
	})
	persisted := 0
	require.NoError(t, flushBucketsWith(context.Background(), existingKey.bucketTs+1, false, func(context.Context, *model.PerfMetric) error {
		persisted++
		return nil
	}))

	assert.Equal(t, 1, persisted)
	assert.Equal(t, Stats{}, RuntimeStats())
}
