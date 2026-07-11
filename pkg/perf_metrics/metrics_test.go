package perfmetrics

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		Buckets:         2,
		BucketEvictions: 1,
		DroppedSamples:  1,
	}, RuntimeStats())
}

func TestPerfMetricsEvictExpiredBucketsBeforeCapacity(t *testing.T) {
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
		Buckets:         1,
		BucketEvictions: 2,
		DroppedSamples:  2,
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
	flushCompletedBucketsWith(101, func(metric *model.PerfMetric) error {
		persisted = *metric
		return nil
	})

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
	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		flushCompletedBucketsWith(101, func(*model.PerfMetric) error {
			close(persistStarted)
			<-releasePersist
			return errors.New("database unavailable")
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
	<-flushDone

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
	flushDone := make(chan struct{})
	var persisted model.PerfMetric
	go func() {
		defer close(flushDone)
		flushCompletedBucketsWith(101, func(metric *model.PerfMetric) error {
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
	<-flushDone

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
	runMaintenanceWith(setting, existingKey.bucketTs+1, func(*model.PerfMetric) error {
		persisted++
		return nil
	})

	assert.Equal(t, 1, persisted)
	assert.Equal(t, Stats{}, RuntimeStats())
}
