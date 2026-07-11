package perfmetrics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/perf_metrics_setting"
)

const (
	maintenanceBackoffBase = time.Second
	maintenanceBackoffCap  = time.Minute
	cleanupInterval        = 6 * time.Hour
)

// Runtime owns the performance-metric maintenance lifecycle.
type Runtime struct {
	cancel      context.CancelFunc
	done        chan struct{}
	close       sync.Once
	finalize    sync.Once
	finalErr    error
	deps        runtimeDeps
	stats       runtimeStats
	lastCleanup time.Time
}

type runtimeDeps struct {
	getSetting   func() perf_metrics_setting.PerfMetricsSetting
	persist      func(context.Context, *model.PerfMetric) error
	deleteBefore func(context.Context, int64) error
	wait         func(context.Context, time.Duration) bool
	now          func() time.Time
	jitter       common.JitterFunc
}

// PerfRuntimeStats exposes low-cardinality maintenance counters.
type PerfRuntimeStats struct {
	Runs              int64
	Errors            int64
	ConsecutiveErrors int64
}

type runtimeStats struct {
	runs              atomic.Int64
	errors            atomic.Int64
	consecutiveErrors atomic.Int64
}

// Start begins performance-metric maintenance and runs the first pass
// immediately.
func Start(parent context.Context) *Runtime {
	return newRuntime(parent, defaultRuntimeDeps())
}

func defaultRuntimeDeps() runtimeDeps {
	return runtimeDeps{
		getSetting:   perf_metrics_setting.GetSetting,
		persist:      model.UpsertPerfMetricContext,
		deleteBefore: model.DeletePerfMetricsBeforeContext,
		wait:         waitRuntime,
		now:          time.Now,
		jitter:       common.FullJitter,
	}
}

func newRuntime(parent context.Context, deps runtimeDeps) *Runtime {
	defaults := defaultRuntimeDeps()
	if deps.getSetting == nil {
		deps.getSetting = defaults.getSetting
	}
	if deps.persist == nil {
		deps.persist = defaults.persist
	}
	if deps.deleteBefore == nil {
		deps.deleteBefore = defaults.deleteBefore
	}
	if deps.wait == nil {
		deps.wait = defaults.wait
	}
	if deps.now == nil {
		deps.now = defaults.now
	}
	if deps.jitter == nil {
		deps.jitter = defaults.jitter
	}
	ctx, cancel := context.WithCancel(parent)
	runtime := &Runtime{
		cancel: cancel,
		done:   make(chan struct{}),
		deps:   deps,
	}
	go runtime.run(ctx)
	return runtime
}

func waitRuntime(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (runtime *Runtime) run(ctx context.Context) {
	defer close(runtime.done)
	for ctx.Err() == nil {
		setting := runtime.deps.getSetting()
		err := runtime.runMaintenance(ctx, setting, false)
		if ctx.Err() != nil {
			return
		}

		delay := flushInterval(setting.FlushInterval)
		if err != nil {
			delay = common.CappedExponentialBackoff(
				int(runtime.stats.consecutiveErrors.Load()),
				maintenanceBackoffBase,
				maintenanceBackoffCap,
				runtime.deps.jitter,
			)
		}
		if !runtime.deps.wait(ctx, delay) {
			return
		}
	}
}

func (runtime *Runtime) runMaintenance(ctx context.Context, setting perf_metrics_setting.PerfMetricsSetting, includeActive bool) error {
	runtime.stats.runs.Add(1)
	now := runtime.deps.now()
	flushErr := flushBucketsWith(ctx, currentBucketStart(now.Unix(), setting.BucketTime), includeActive, runtime.deps.persist)
	var cleanupErr error
	if ctx.Err() == nil {
		cleanupErr = runtime.cleanup(ctx, setting.RetentionDays, now)
	}
	err := errors.Join(flushErr, cleanupErr)
	if err != nil {
		if ctx.Err() == nil {
			runtime.stats.errors.Add(1)
			runtime.stats.consecutiveErrors.Add(1)
		}
		return err
	}
	runtime.stats.consecutiveErrors.Store(0)
	return nil
}

func (runtime *Runtime) cleanup(ctx context.Context, retentionDays int, now time.Time) error {
	if retentionDays <= 0 {
		return nil
	}

	if !runtime.lastCleanup.IsZero() && now.Sub(runtime.lastCleanup) < cleanupInterval {
		return nil
	}

	cutoff, ok := retentionCutoff(now, retentionDays)
	if !ok {
		return nil
	}
	if err := runtime.deps.deleteBefore(ctx, cutoff); err != nil {
		if ctx.Err() == nil {
			common.SysError("failed to cleanup expired perf metrics: " + err.Error())
		}
		return fmt.Errorf("cleanup expired perf metrics: %w", err)
	}
	runtime.lastCleanup = now
	return nil
}

func currentBucketStart(timestamp int64, bucketTime string) int64 {
	bucketSeconds := int64(3600)
	switch bucketTime {
	case "minute":
		bucketSeconds = 60
	case "5min":
		bucketSeconds = 300
	}
	return timestamp - timestamp%bucketSeconds
}

func retentionCutoff(now time.Time, retentionDays int) (int64, bool) {
	if retentionDays <= 0 {
		return 0, false
	}
	const secondsPerDay int64 = 24 * 60 * 60
	nowUnix := now.Unix()
	if nowUnix <= 0 {
		return 0, false
	}
	days := int64(retentionDays)
	if days > nowUnix/secondsPerDay {
		return 0, false
	}
	cutoff := nowUnix - days*secondsPerDay
	if cutoff <= 0 || cutoff > nowUnix {
		return 0, false
	}
	return cutoff, true
}

func flushInterval(minutes int) time.Duration {
	if minutes <= 0 {
		return time.Minute
	}
	const maxDuration = time.Duration(1<<63 - 1)
	if int64(minutes) > int64(maxDuration/time.Minute) {
		return maxDuration
	}
	return time.Duration(minutes) * time.Minute
}

// Close cancels periodic maintenance. It is idempotent and does not wait or
// perform final persistence.
func (runtime *Runtime) Close() {
	runtime.close.Do(runtime.cancel)
}

// Wait waits for periodic maintenance to exit, then performs the final flush
// and any due cleanup exactly once.
func (runtime *Runtime) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-runtime.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	runtime.finalize.Do(func() {
		runtime.finalErr = runtime.runMaintenance(ctx, runtime.deps.getSetting(), true)
	})
	return runtime.finalErr
}

// Stats returns a low-cardinality snapshot of maintenance activity.
func (runtime *Runtime) Stats() PerfRuntimeStats {
	return PerfRuntimeStats{
		Runs:              runtime.stats.runs.Load(),
		Errors:            runtime.stats.errors.Load(),
		ConsecutiveErrors: runtime.stats.consecutiveErrors.Load(),
	}
}
