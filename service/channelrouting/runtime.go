package channelrouting

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
)

const (
	observeRefreshMinimum = 30 * time.Second
	observeDisabledPoll   = 5 * time.Second
	observeBackoffBase    = time.Second
	observeBackoffCap     = time.Minute
	observeFinalTimeout   = 15 * time.Second
)

type RuntimeWorkerStats struct {
	Runs                int64  `json:"runs"`
	Failures            int64  `json:"failures"`
	ConsecutiveFailures int64  `json:"consecutive_failures"`
	LastSuccessUnix     int64  `json:"last_success_at"`
	LastFailureUnix     int64  `json:"last_failure_at"`
	LastDurationMs      int64  `json:"last_duration_ms"`
	LastError           string `json:"last_error,omitempty"`
}

type RuntimeStats struct {
	Refresh          RuntimeWorkerStats         `json:"refresh"`
	Flush            RuntimeWorkerStats         `json:"flush"`
	Audit            DecisionBufferStats        `json:"audit"`
	Telemetry        routingmetrics.StableStats `json:"telemetry"`
	SnapshotRevision uint64                     `json:"snapshot_revision"`
	SnapshotBuiltAt  int64                      `json:"snapshot_built_at"`
}

type Runtime struct {
	cancel context.CancelFunc
	done   chan struct{}
	wait   sync.WaitGroup

	deps        runtimeDeps
	wakeRefresh bool

	refreshStats runtimeWorkerState
	flushStats   runtimeWorkerState

	lastRetentionUnix atomic.Int64
	finalOnce         sync.Once
	finalDone         chan struct{}
	finalMu           sync.Mutex
	finalErr          error
}

type runtimeWorkerState struct {
	runs                atomic.Int64
	failures            atomic.Int64
	consecutiveFailures atomic.Int64
	lastSuccessUnix     atomic.Int64
	lastFailureUnix     atomic.Int64
	lastDurationMs      atomic.Int64
	lastErrorMu         sync.RWMutex
	lastError           string
}

type runtimeDeps struct {
	getSetting      func() smart_routing_setting.SmartRoutingSetting
	refresh         func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	flush           func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	retention       func(context.Context, int) (int64, error)
	topologyChanges <-chan struct{}
	wait            func(context.Context, time.Duration) bool
	jitter          common.JitterFunc
}

var activeRuntime atomic.Pointer[Runtime]

func Start(parent context.Context) *Runtime {
	runtime := newRuntime(parent, defaultRuntimeDeps())
	activeRuntime.Store(runtime)
	return runtime
}

func BootstrapContext(ctx context.Context) error {
	if !smart_routing_setting.Enabled() {
		return nil
	}
	return refreshTopologySnapshotContext(ctx)
}

func CurrentRuntimeStats() RuntimeStats {
	runtime := activeRuntime.Load()
	if runtime == nil {
		return RuntimeStats{Audit: DecisionAuditsStats(), Telemetry: routingmetrics.StableRuntimeStats()}
	}
	return runtime.Stats()
}

func defaultRuntimeDeps() runtimeDeps {
	return runtimeDeps{
		getSetting: smart_routing_setting.GetSetting,
		refresh: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			return refreshTopologySnapshotContext(ctx)
		},
		flush: func(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) error {
			if _, err := FlushStableTelemetryContext(ctx); err != nil {
				return err
			}
			for {
				flushed, err := FlushDecisionAuditsContext(ctx)
				if err != nil {
					return err
				}
				if flushed < model.RoutingDecisionAuditMaxBatch {
					break
				}
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			return nil
		},
		retention:       DeleteExpiredRoutingHistoryContext,
		topologyChanges: model.RoutingTopologyChanges(),
		wait:            waitRuntime,
		jitter:          common.FullJitter,
	}
}

func refreshTopologySnapshotContext(ctx context.Context) error {
	if _, err := model.ReconcileLegacyRoutingTopologyContext(ctx); err != nil {
		return err
	}
	_, err := RefreshSnapshotContext(ctx)
	return err
}

func newRuntime(parent context.Context, deps runtimeDeps) *Runtime {
	if parent == nil {
		parent = context.Background()
	}
	if deps.getSetting == nil {
		deps.getSetting = smart_routing_setting.GetSetting
	}
	wakeRefresh := deps.topologyChanges != nil
	if deps.wait == nil {
		deps.wait = waitRuntime
	}
	if deps.jitter == nil {
		deps.jitter = common.FullJitter
	}
	if deps.retention == nil {
		deps.retention = DeleteExpiredRoutingHistoryContext
	}
	ctx, cancel := context.WithCancel(parent)
	runtime := &Runtime{
		cancel:      cancel,
		done:        make(chan struct{}),
		deps:        deps,
		wakeRefresh: wakeRefresh,
		finalDone:   make(chan struct{}),
	}
	if err := ctx.Err(); err != nil {
		close(runtime.done)
		runtime.finalOnce.Do(func() {})
		close(runtime.finalDone)
		return runtime
	}

	runtime.wait.Add(2)
	go func() {
		defer runtime.wait.Done()
		runtime.runWorker(ctx, deps.refresh, observeRefreshInterval, &runtime.refreshStats)
	}()
	go func() {
		defer runtime.wait.Done()
		runtime.runWorker(ctx, deps.flush, observeFlushInterval, &runtime.flushStats)
	}()
	go func() {
		runtime.wait.Wait()
		close(runtime.done)
	}()
	return runtime
}

func (runtime *Runtime) Close() {
	if runtime != nil && runtime.cancel != nil {
		runtime.cancel()
	}
}

func (runtime *Runtime) Wait(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runtime.done:
	}
	runtime.startFinalFlush(ctx)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runtime.finalDone:
		runtime.finalMu.Lock()
		defer runtime.finalMu.Unlock()
		return runtime.finalErr
	}
}

func (runtime *Runtime) Stats() RuntimeStats {
	if runtime == nil {
		return RuntimeStats{}
	}
	stats := RuntimeStats{
		Refresh:   runtime.refreshStats.snapshot(),
		Flush:     runtime.flushStats.snapshot(),
		Audit:     DecisionAuditsStats(),
		Telemetry: routingmetrics.StableRuntimeStats(),
	}
	if snapshot := currentSnapshot.Load(); snapshot != nil {
		stats.SnapshotRevision = snapshot.view.Revision
		stats.SnapshotBuiltAt = snapshot.view.BuiltAtUnix
	}
	return stats
}

func (runtime *Runtime) runWorker(
	ctx context.Context,
	run func(context.Context, smart_routing_setting.SmartRoutingSetting) error,
	interval func(smart_routing_setting.SmartRoutingSetting) time.Duration,
	state *runtimeWorkerState,
) {
	consecutiveFailures := 0
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		setting := runtime.deps.getSetting()
		if !setting.Enabled || run == nil {
			consecutiveFailures = 0
			state.consecutiveFailures.Store(0)
			if !runtime.waitNext(ctx, observeDisabledPoll, state) {
				return
			}
			continue
		}

		started := time.Now()
		err := run(ctx, setting)
		if err == nil && state == &runtime.flushStats {
			err = runtime.runRetention(ctx, setting.RetentionDays)
		}
		state.runs.Add(1)
		state.lastDurationMs.Store(time.Since(started).Milliseconds())
		if err == nil {
			consecutiveFailures = 0
			state.consecutiveFailures.Store(0)
			state.lastSuccessUnix.Store(common.GetTimestamp())
			state.setLastError("")
			if !runtime.waitNext(ctx, interval(setting), state) {
				return
			}
			continue
		}

		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return
		}
		consecutiveFailures++
		state.failures.Add(1)
		state.consecutiveFailures.Store(int64(consecutiveFailures))
		state.lastFailureUnix.Store(common.GetTimestamp())
		state.setLastError(common.SanitizeErrorMessage(err.Error()))
		delay := common.CappedExponentialBackoff(
			consecutiveFailures,
			observeBackoffBase,
			observeBackoffCap,
			runtime.deps.jitter,
		)
		if !runtime.waitNext(ctx, delay, state) {
			return
		}
	}
}

func (runtime *Runtime) waitNext(ctx context.Context, duration time.Duration, state *runtimeWorkerState) bool {
	if runtime == nil || !runtime.wakeRefresh || state != &runtime.refreshStats {
		return runtime.deps.wait(ctx, duration)
	}
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-runtime.deps.topologyChanges:
		return true
	case <-timer.C:
		return true
	}
}

func (runtime *Runtime) runRetention(ctx context.Context, retentionDays int) error {
	if retentionDays < 1 {
		return nil
	}
	now := common.GetTimestamp()
	last := runtime.lastRetentionUnix.Load()
	if last > 0 && now-last < int64((6*time.Hour)/time.Second) {
		return nil
	}
	retentionCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := runtime.deps.retention(retentionCtx, retentionDays); err != nil {
		return err
	}
	runtime.lastRetentionUnix.Store(common.GetTimestamp())
	return nil
}

func (runtime *Runtime) startFinalFlush(parent context.Context) {
	runtime.finalOnce.Do(func() {
		go func() {
			defer close(runtime.finalDone)
			if runtime.deps.flush == nil ||
				(DecisionAuditsStats().Entries == 0 && routingmetrics.StableRuntimeStats().Buckets == 0) {
				return
			}
			ctx, cancel := context.WithTimeout(parent, observeFinalTimeout)
			defer cancel()
			err := runtime.deps.flush(ctx, runtime.deps.getSetting())
			runtime.finalMu.Lock()
			runtime.finalErr = err
			runtime.finalMu.Unlock()
		}()
	})
}

func (state *runtimeWorkerState) snapshot() RuntimeWorkerStats {
	state.lastErrorMu.RLock()
	lastError := state.lastError
	state.lastErrorMu.RUnlock()
	return RuntimeWorkerStats{
		Runs:                state.runs.Load(),
		Failures:            state.failures.Load(),
		ConsecutiveFailures: state.consecutiveFailures.Load(),
		LastSuccessUnix:     state.lastSuccessUnix.Load(),
		LastFailureUnix:     state.lastFailureUnix.Load(),
		LastDurationMs:      state.lastDurationMs.Load(),
		LastError:           lastError,
	}
}

func (state *runtimeWorkerState) setLastError(message string) {
	state.lastErrorMu.Lock()
	state.lastError = message
	state.lastErrorMu.Unlock()
}

func observeRefreshInterval(setting smart_routing_setting.SmartRoutingSetting) time.Duration {
	interval := time.Duration(setting.HotcacheRefreshSec) * time.Second
	if interval < observeRefreshMinimum {
		return observeRefreshMinimum
	}
	return interval
}

func observeFlushInterval(setting smart_routing_setting.SmartRoutingSetting) time.Duration {
	interval := time.Duration(setting.FlushIntervalMin) * time.Minute
	if interval <= 0 {
		return time.Minute
	}
	return interval
}

func waitRuntime(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
