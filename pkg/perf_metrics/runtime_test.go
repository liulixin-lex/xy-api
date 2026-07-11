package perfmetrics

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type observedWaitContext struct {
	context.Context
	checks      atomic.Int64
	afterSecond chan struct{}
}

func (ctx *observedWaitContext) Err() error {
	err := ctx.Context.Err()
	if ctx.checks.Add(1) == 2 {
		close(ctx.afterSecond)
	}
	return err
}

func TestRuntimeRunsMaintenanceImmediatelyAndFinalizesOnlyOnWait(t *testing.T) {
	configurePerfMetricsForTest(t, true)

	waits := make(chan time.Duration, 1)
	waitCanceled := make(chan struct{})
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() perf_metrics_setting.PerfMetricsSetting {
			return perf_metrics_setting.PerfMetricsSetting{
				Enabled:       true,
				FlushInterval: 7,
				BucketTime:    "hour",
			}
		},
		persist: func(context.Context, *model.PerfMetric) error { return nil },
		deleteBefore: func(context.Context, int64) error {
			return nil
		},
		wait: func(ctx context.Context, duration time.Duration) bool {
			waits <- duration
			<-ctx.Done()
			close(waitCanceled)
			return false
		},
		now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	})

	assert.Equal(t, 7*time.Minute, <-waits)
	assert.Equal(t, PerfRuntimeStats{Runs: 1}, runtime.Stats())

	runtime.Close()
	runtime.Close()
	<-waitCanceled
	assert.Equal(t, PerfRuntimeStats{Runs: 1}, runtime.Stats(), "Close must only cancel the worker")

	require.NoError(t, runtime.Wait(context.Background()))
	require.NoError(t, runtime.Wait(context.Background()))
	assert.Equal(t, PerfRuntimeStats{Runs: 2}, runtime.Stats(), "Wait must final-flush only once")
}

func TestRuntimeBacksOffAfterFailuresAndRecoversConfiguredInterval(t *testing.T) {
	configurePerfMetricsForTest(t, true)

	waits := make(chan time.Duration)
	advance := make(chan struct{})
	var deleteAttempts atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() perf_metrics_setting.PerfMetricsSetting {
			return perf_metrics_setting.PerfMetricsSetting{
				Enabled:       true,
				FlushInterval: 17,
				BucketTime:    "hour",
				RetentionDays: 1,
			}
		},
		persist: func(context.Context, *model.PerfMetric) error { return nil },
		deleteBefore: func(context.Context, int64) error {
			if deleteAttempts.Add(1) <= 2 {
				return errors.New("cleanup failed")
			}
			return nil
		},
		wait: func(ctx context.Context, duration time.Duration) bool {
			select {
			case waits <- duration:
			case <-ctx.Done():
				return false
			}
			select {
			case <-advance:
				return true
			case <-ctx.Done():
				return false
			}
		},
		now:    func() time.Time { return time.Unix(1_700_000_000, 0) },
		jitter: func(max time.Duration) time.Duration { return max },
	})
	t.Cleanup(func() {
		runtime.Close()
		require.NoError(t, runtime.Wait(context.Background()))
	})

	assert.Equal(t, time.Second, <-waits)
	assert.Equal(t, PerfRuntimeStats{Runs: 1, Errors: 1, ConsecutiveErrors: 1}, runtime.Stats())

	advance <- struct{}{}
	assert.Equal(t, 2*time.Second, <-waits)
	assert.Equal(t, PerfRuntimeStats{Runs: 2, Errors: 2, ConsecutiveErrors: 2}, runtime.Stats())

	advance <- struct{}{}
	assert.Equal(t, 17*time.Minute, <-waits)
	assert.Equal(t, PerfRuntimeStats{Runs: 3, Errors: 2}, runtime.Stats())

	advance <- struct{}{}
	assert.Equal(t, 17*time.Minute, <-waits)
	assert.Equal(t, int64(3), deleteAttempts.Load(), "successful cleanup must be throttled for six hours")
}

func TestRuntimeBackoffCapsAtOneMinute(t *testing.T) {
	configurePerfMetricsForTest(t, true)

	waits := make(chan time.Duration)
	advance := make(chan struct{})
	var attempts atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() perf_metrics_setting.PerfMetricsSetting {
			return perf_metrics_setting.PerfMetricsSetting{
				FlushInterval: 30,
				BucketTime:    "hour",
				RetentionDays: 1,
			}
		},
		persist: func(context.Context, *model.PerfMetric) error { return nil },
		deleteBefore: func(context.Context, int64) error {
			if attempts.Add(1) <= 7 {
				return errors.New("cleanup failed")
			}
			return nil
		},
		wait: func(ctx context.Context, duration time.Duration) bool {
			select {
			case waits <- duration:
			case <-ctx.Done():
				return false
			}
			select {
			case <-advance:
				return true
			case <-ctx.Done():
				return false
			}
		},
		now:    func() time.Time { return time.Unix(1_700_000_000, 0) },
		jitter: func(max time.Duration) time.Duration { return max },
	})

	expected := []time.Duration{
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		time.Minute,
	}
	for index, duration := range expected {
		assert.Equal(t, duration, <-waits)
		if index < len(expected)-1 {
			advance <- struct{}{}
		}
	}
	assert.Equal(t, PerfRuntimeStats{Runs: 7, Errors: 7, ConsecutiveErrors: 7}, runtime.Stats())

	runtime.Close()
	require.NoError(t, runtime.Wait(context.Background()))
}

func TestRuntimeFinalFlushIncludesActiveBucketWhenDisabledAndWaitIsIdempotent(t *testing.T) {
	configurePerfMetricsForTest(t, true)

	now := time.Unix(1_700_000_000, 0)
	key := bucketKey{
		model:    "gpt-final",
		group:    "default",
		bucketTs: now.Unix() - now.Unix()%3600,
	}
	require.True(t, recordSample(key, Sample{
		Model:        key.model,
		Group:        key.group,
		LatencyMs:    250,
		Success:      true,
		OutputTokens: 20,
		GenerationMs: 100,
	}))

	waitReady := make(chan struct{})
	waitCanceled := make(chan struct{})
	var persistCalls atomic.Int64
	var persisted model.PerfMetric
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() perf_metrics_setting.PerfMetricsSetting {
			return perf_metrics_setting.PerfMetricsSetting{
				Enabled:       false,
				FlushInterval: 5,
				BucketTime:    "hour",
			}
		},
		persist: func(_ context.Context, metric *model.PerfMetric) error {
			persistCalls.Add(1)
			persisted = *metric
			return nil
		},
		deleteBefore: func(context.Context, int64) error { return nil },
		wait: func(ctx context.Context, _ time.Duration) bool {
			close(waitReady)
			<-ctx.Done()
			close(waitCanceled)
			return false
		},
		now: func() time.Time { return now },
	})
	<-waitReady

	runtime.Close()
	runtime.Close()
	<-waitCanceled
	assert.Zero(t, persistCalls.Load(), "Close must not perform the final flush")

	require.NoError(t, runtime.Wait(context.Background()))
	require.NoError(t, runtime.Wait(context.Background()))
	assert.Equal(t, int64(1), persistCalls.Load())
	assert.Equal(t, model.PerfMetric{
		ModelName:      key.model,
		Group:          key.group,
		BucketTs:       key.bucketTs,
		RequestCount:   1,
		SuccessCount:   1,
		TotalLatencyMs: 250,
		OutputTokens:   20,
		GenerationMs:   100,
	}, persisted)
	assert.Equal(t, Stats{}, RuntimeStats())
	assert.Equal(t, PerfRuntimeStats{Runs: 2}, runtime.Stats())
}

func TestRuntimeCleanupUsesSafeCutoffAndThrottlesOnlySuccessfulRuns(t *testing.T) {
	configurePerfMetricsForTest(t, true)

	const secondsPerDay int64 = 24 * 60 * 60
	now := time.Unix(10*secondsPerDay+123, 0)
	deleteErr := error(nil)
	cutoffs := make([]int64, 0, 4)
	runtime := &Runtime{deps: runtimeDeps{
		persist: func(context.Context, *model.PerfMetric) error { return nil },
		deleteBefore: func(_ context.Context, cutoff int64) error {
			cutoffs = append(cutoffs, cutoff)
			return deleteErr
		},
		now: func() time.Time { return now },
	}}
	setting := perf_metrics_setting.PerfMetricsSetting{BucketTime: "hour"}

	require.NoError(t, runtime.runMaintenance(context.Background(), setting, false))
	assert.Empty(t, cutoffs, "zero retention must not delete")

	setting.RetentionDays = 2
	require.NoError(t, runtime.runMaintenance(context.Background(), setting, false))
	require.Equal(t, []int64{8*secondsPerDay + 123}, cutoffs)

	now = now.Add(5 * time.Hour)
	require.NoError(t, runtime.runMaintenance(context.Background(), setting, false))
	assert.Len(t, cutoffs, 1)

	now = now.Add(time.Hour)
	require.NoError(t, runtime.runMaintenance(context.Background(), setting, false))
	require.Equal(t, []int64{
		8*secondsPerDay + 123,
		8*secondsPerDay + 6*60*60 + 123,
	}, cutoffs)

	deleteErr = errors.New("delete unavailable")
	now = now.Add(6 * time.Hour)
	require.ErrorIs(t, runtime.runMaintenance(context.Background(), setting, false), deleteErr)
	assert.Len(t, cutoffs, 3)

	deleteErr = nil
	now = now.Add(time.Minute)
	require.NoError(t, runtime.runMaintenance(context.Background(), setting, false))
	assert.Len(t, cutoffs, 4, "failed cleanup must not advance the throttle window")

	var overflowDeleteCalls atomic.Int64
	overflowRuntime := &Runtime{deps: runtimeDeps{
		persist: func(context.Context, *model.PerfMetric) error { return nil },
		deleteBefore: func(context.Context, int64) error {
			overflowDeleteCalls.Add(1)
			return nil
		},
		now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	}}
	setting.RetentionDays = math.MaxInt
	require.NoError(t, overflowRuntime.runMaintenance(context.Background(), setting, false))
	assert.Zero(t, overflowDeleteCalls.Load(), "overflowing retention must not produce a future cutoff")

	setting.RetentionDays = 1
	require.NoError(t, overflowRuntime.runMaintenance(context.Background(), setting, false))
	assert.Equal(t, int64(1), overflowDeleteCalls.Load(), "a skipped cutoff must not throttle a later valid retention")
}

func TestRuntimeWaitReturnsFinalFlushErrorOnlyOnce(t *testing.T) {
	configurePerfMetricsForTest(t, true)

	now := time.Unix(1_700_000_000, 0)
	key := bucketKey{model: "gpt-final-error", group: "default", bucketTs: currentBucketStart(now.Unix(), "hour")}
	require.True(t, recordSample(key, Sample{Model: key.model, Group: key.group, Success: true}))

	waitReady := make(chan struct{})
	finalErr := errors.New("final persist failed")
	var persistCalls atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() perf_metrics_setting.PerfMetricsSetting {
			return perf_metrics_setting.PerfMetricsSetting{FlushInterval: 5, BucketTime: "hour"}
		},
		persist: func(context.Context, *model.PerfMetric) error {
			persistCalls.Add(1)
			return finalErr
		},
		deleteBefore: func(context.Context, int64) error { return nil },
		wait: func(ctx context.Context, _ time.Duration) bool {
			close(waitReady)
			<-ctx.Done()
			return false
		},
		now: func() time.Time { return now },
	})
	<-waitReady
	runtime.Close()

	require.ErrorIs(t, runtime.Wait(context.Background()), finalErr)
	require.ErrorIs(t, runtime.Wait(context.Background()), finalErr)
	assert.Equal(t, int64(1), persistCalls.Load())
	assert.Equal(t, Stats{Buckets: 1}, RuntimeStats(), "failed final flush must requeue the active bucket")
	assert.Equal(t, PerfRuntimeStats{Runs: 2, Errors: 1, ConsecutiveErrors: 1}, runtime.Stats())
}

func TestRuntimeConcurrentWaiterHonorsOwnContextDuringFinalFlush(t *testing.T) {
	configurePerfMetricsForTest(t, true)
	now := time.Unix(1_700_000_000, 0)
	key := bucketKey{model: "gpt-concurrent-wait", group: "default", bucketTs: currentBucketStart(now.Unix(), "hour")}
	require.True(t, recordSample(key, Sample{Model: key.model, Group: key.group, Success: true}))

	workerWaiting := make(chan struct{})
	persistStarted := make(chan struct{})
	releasePersist := make(chan struct{})
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() perf_metrics_setting.PerfMetricsSetting {
			return perf_metrics_setting.PerfMetricsSetting{FlushInterval: 5, BucketTime: "hour"}
		},
		persist: func(context.Context, *model.PerfMetric) error {
			close(persistStarted)
			<-releasePersist
			return nil
		},
		deleteBefore: func(context.Context, int64) error { return nil },
		wait: func(ctx context.Context, _ time.Duration) bool {
			close(workerWaiting)
			<-ctx.Done()
			return false
		},
		now: func() time.Time { return now },
	})
	<-workerWaiting
	runtime.Close()

	firstResult := make(chan error, 1)
	go func() { firstResult <- runtime.Wait(context.Background()) }()
	<-persistStarted

	secondBase, cancelSecond := context.WithCancel(context.Background())
	secondContext := &observedWaitContext{Context: secondBase, afterSecond: make(chan struct{})}
	secondResult := make(chan error, 1)
	go func() { secondResult <- runtime.Wait(secondContext) }()
	<-secondContext.afterSecond
	cancelSecond()

	select {
	case err := <-secondResult:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		close(releasePersist)
		require.NoError(t, <-firstResult)
		require.Fail(t, "concurrent Wait ignored its own context")
	}

	close(releasePersist)
	require.NoError(t, <-firstResult)
}

func TestRuntimeCanceledWaitAfterWorkerExitDoesNotConsumeFinalFlush(t *testing.T) {
	configurePerfMetricsForTest(t, true)

	now := time.Unix(1_700_000_000, 0)
	key := bucketKey{model: "gpt-canceled-wait", group: "default", bucketTs: currentBucketStart(now.Unix(), "hour")}
	require.True(t, recordSample(key, Sample{Model: key.model, Group: key.group, Success: true}))

	waitReady := make(chan struct{})
	workerStopped := make(chan struct{})
	var persistCalls atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() perf_metrics_setting.PerfMetricsSetting {
			return perf_metrics_setting.PerfMetricsSetting{FlushInterval: 5, BucketTime: "hour"}
		},
		persist: func(context.Context, *model.PerfMetric) error {
			persistCalls.Add(1)
			return nil
		},
		deleteBefore: func(context.Context, int64) error { return nil },
		wait: func(ctx context.Context, _ time.Duration) bool {
			close(waitReady)
			<-ctx.Done()
			close(workerStopped)
			return false
		},
		now: func() time.Time { return now },
	})
	<-waitReady
	runtime.Close()
	<-workerStopped
	<-runtime.done

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, runtime.Wait(canceled), context.Canceled)
	assert.Zero(t, persistCalls.Load())

	require.NoError(t, runtime.Wait(context.Background()))
	require.NoError(t, runtime.Wait(context.Background()))
	assert.Equal(t, int64(1), persistCalls.Load())
}

func TestRuntimeCloseCancelsPeriodicPersistBeforeFinalFlush(t *testing.T) {
	configurePerfMetricsForTest(t, true)

	now := time.Unix(1_700_000_000, 0)
	key := bucketKey{
		model:    "gpt-cancel",
		group:    "default",
		bucketTs: currentBucketStart(now.Unix(), "hour") - 3600,
	}
	require.True(t, recordSample(key, Sample{Model: key.model, Group: key.group, Success: true}))

	persistStarted := make(chan struct{})
	persistCanceled := make(chan struct{})
	var persistCalls atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() perf_metrics_setting.PerfMetricsSetting {
			return perf_metrics_setting.PerfMetricsSetting{FlushInterval: 5, BucketTime: "hour"}
		},
		persist: func(ctx context.Context, _ *model.PerfMetric) error {
			if persistCalls.Add(1) == 1 {
				close(persistStarted)
				<-ctx.Done()
				close(persistCanceled)
				return ctx.Err()
			}
			return nil
		},
		deleteBefore: func(context.Context, int64) error { return nil },
		wait:         waitRuntime,
		now:          func() time.Time { return now },
	})
	<-persistStarted

	runtime.Close()
	<-persistCanceled
	require.NoError(t, runtime.Wait(context.Background()))
	assert.Equal(t, int64(2), persistCalls.Load())
	assert.Equal(t, Stats{}, RuntimeStats())
	assert.Equal(t, PerfRuntimeStats{Runs: 2}, runtime.Stats(), "shutdown cancellation is not a maintenance error")
}

func TestRuntimeFinalMaintenanceRunsCleanupWhenThrottleExpires(t *testing.T) {
	configurePerfMetricsForTest(t, true)

	now := time.Unix(1_700_000_000, 0)
	waitReady := make(chan struct{})
	var deleteCalls atomic.Int64
	runtime := newRuntime(context.Background(), runtimeDeps{
		getSetting: func() perf_metrics_setting.PerfMetricsSetting {
			return perf_metrics_setting.PerfMetricsSetting{
				Enabled:       false,
				FlushInterval: 5,
				BucketTime:    "hour",
				RetentionDays: 1,
			}
		},
		persist: func(context.Context, *model.PerfMetric) error { return nil },
		deleteBefore: func(context.Context, int64) error {
			deleteCalls.Add(1)
			return nil
		},
		wait: func(ctx context.Context, _ time.Duration) bool {
			close(waitReady)
			<-ctx.Done()
			return false
		},
		now: func() time.Time { return now },
	})
	<-waitReady
	assert.Equal(t, int64(1), deleteCalls.Load())

	now = now.Add(6 * time.Hour)
	runtime.Close()
	require.NoError(t, runtime.Wait(context.Background()))
	require.NoError(t, runtime.Wait(context.Background()))
	assert.Equal(t, int64(2), deleteCalls.Load())
}
