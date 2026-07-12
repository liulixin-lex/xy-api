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
	observeRefreshMinimum        = 30 * time.Second
	observeDisabledPoll          = 5 * time.Second
	observeBackoffBase           = time.Second
	observeBackoffCap            = time.Minute
	observeFinalTimeout          = 15 * time.Second
	observeStreamPoll            = 50 * time.Millisecond
	observeLocalDrainRedis       = 2 * time.Second
	observeLocalDrainFallback    = 5 * time.Second
	observeRefreshTimeout        = 2 * time.Minute
	observeSnapshotFallback      = 5 * time.Minute
	routingLegacyLeaseTTL        = 3 * time.Minute
	routingLegacyMinInterval     = 5 * time.Minute
	decisionAuditFlushMaxBatches = 16
	observeFinalFlushMaxAttempts = 4
	observeFinalFlushRetryBase   = 50 * time.Millisecond
	canaryControlPollInterval    = 10 * time.Second
	canaryOperationPollInterval  = time.Second
)

const routingLegacyReconcileLeaseName = "routing-v2-legacy-reconcile"

var ErrRoutingLegacyReconcileBusy = errors.New("channel routing legacy reconcile is owned by another node")

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
	Refresh                   RuntimeWorkerStats             `json:"refresh"`
	Flush                     RuntimeWorkerStats             `json:"flush"`
	LocalDrain                RuntimeWorkerStats             `json:"local_drain"`
	TelemetryStream           RuntimeWorkerStats             `json:"telemetry_stream"`
	ConfigPublisher           RuntimeWorkerStats             `json:"config_publisher"`
	ConfigConsumer            RuntimeWorkerStats             `json:"config_consumer"`
	CanaryNodePresence        RuntimeWorkerStats             `json:"canary_node_presence"`
	CanaryEvaluator           RuntimeWorkerStats             `json:"canary_evaluator"`
	CanaryOperations          RuntimeWorkerStats             `json:"canary_operations"`
	Retention                 RuntimeWorkerStats             `json:"retention"`
	Audit                     DecisionBufferStats            `json:"audit"`
	Telemetry                 routingmetrics.StableStats     `json:"telemetry"`
	TelemetryTransport        RoutingTelemetryTransportStats `json:"telemetry_transport"`
	ConfigStream              RoutingConfigStreamStats       `json:"config_stream"`
	SnapshotRevision          uint64                         `json:"snapshot_revision"`
	SnapshotRuntimeGeneration uint64                         `json:"snapshot_runtime_generation"`
	SnapshotPolicyHash        string                         `json:"snapshot_policy_hash"`
	SnapshotBuiltAt           int64                          `json:"snapshot_built_at"`
	NodeEpochID               string                         `json:"node_epoch_id"`
}

type Runtime struct {
	cancel context.CancelFunc
	done   chan struct{}
	wait   sync.WaitGroup

	deps          runtimeDeps
	wakeRefresh   bool
	topologyDirty atomic.Bool

	refreshStats         runtimeWorkerState
	flushStats           runtimeWorkerState
	localDrainStats      runtimeWorkerState
	streamStats          runtimeWorkerState
	configPublishStats   runtimeWorkerState
	configConsumeStats   runtimeWorkerState
	canaryPresenceStats  runtimeWorkerState
	canaryEvaluateStats  runtimeWorkerState
	canaryOperationStats runtimeWorkerState
	retentionStats       runtimeWorkerState

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

type pendingRoutingWriteStats struct {
	audits           int
	stableBuckets    int64
	pendingEnvelopes int
	canaryWindows    int
}

type runtimeDeps struct {
	getSetting       func() smart_routing_setting.SmartRoutingSetting
	refresh          func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	refreshCurrent   func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	flush            func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	drainLocal       func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	consumeTelemetry func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	publishConfig    func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	consumeConfig    func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	heartbeatCanary  func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	evaluateCanary   func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	executeCanary    func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	retention        func(context.Context, int) (int64, error)
	topologyChanges  <-chan struct{}
	wait             func(context.Context, time.Duration) bool
	jitter           common.JitterFunc
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
	return refreshTopologySnapshotContext(ctx, false)
}

func CurrentRuntimeStats() RuntimeStats {
	runtime := activeRuntime.Load()
	if runtime == nil {
		return RuntimeStats{
			Audit:              DecisionAuditsStats(),
			Telemetry:          routingmetrics.StableRuntimeStats(),
			TelemetryTransport: RoutingTelemetryTransportRuntimeStats(),
			ConfigStream:       RoutingConfigStreamRuntimeStats(),
			NodeEpochID:        NodeEpochID(),
		}
	}
	return runtime.Stats()
}

func defaultRuntimeDeps() runtimeDeps {
	return runtimeDeps{
		getSetting: smart_routing_setting.GetSetting,
		refresh: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			return refreshTopologySnapshotContext(ctx, true)
		},
		refreshCurrent: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			return refreshTopologySnapshotContext(ctx, false)
		},
		flush: func(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) error {
			return flushLocalRoutingWritesContext(ctx, setting.Enabled)
		},
		drainLocal: func(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) error {
			return flushLocalRoutingWritesContext(ctx, setting.Enabled)
		},
		consumeTelemetry: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			if !common.RedisEnabled || common.RDB == nil {
				if !waitRuntime(ctx, observeDisabledPoll) {
					return ctx.Err()
				}
				return nil
			}
			_, err := ConsumeRoutingTelemetryOnceContext(ctx)
			return err
		},
		publishConfig: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			if !common.RedisEnabled || common.RDB == nil {
				if !waitRuntime(ctx, observeDisabledPoll) {
					return ctx.Err()
				}
				return nil
			}
			published, err := PublishRoutingConfigOutboxOnceContext(ctx)
			if err == nil && !published && !waitRuntime(ctx, time.Second) {
				return ctx.Err()
			}
			return err
		},
		consumeConfig: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			if !common.RedisEnabled || common.RDB == nil {
				if !waitRuntime(ctx, observeDisabledPoll) {
					return ctx.Err()
				}
				return nil
			}
			_, err := ConsumeRoutingConfigOnceContext(ctx)
			return err
		},
		heartbeatCanary: persistRoutingCanaryNodePresenceContext,
		evaluateCanary:  evaluateRoutingCanaryControlContext,
		executeCanary:   executeRoutingCanaryOperationContext,
		retention:       DeleteExpiredRoutingHistoryContext,
		topologyChanges: model.RoutingTopologyChanges(),
		wait:            waitRuntime,
		jitter:          common.FullJitter,
	}
}

func flushLocalRoutingWritesContext(ctx context.Context, ensureCanaryWindows bool) error {
	_, telemetryErr := FlushStableTelemetryContext(ctx)
	_, auditErr := flushDecisionAuditBatchesContext(ctx, decisionAuditFlushMaxBatches)
	var ensureCanaryErr error
	if ensureCanaryWindows {
		ensureCanaryErr = ensureCurrentCanaryOutcomeWindows(defaultCanaryWindowAggregator)
	}
	_, canaryErr := FlushCanaryOutcomeCheckpointsContext(ctx)
	return errors.Join(telemetryErr, auditErr, ensureCanaryErr, canaryErr)
}

func flushDecisionAuditBatchesContext(ctx context.Context, maxBatches int) (int, error) {
	if maxBatches <= 0 {
		return 0, nil
	}
	flushedTotal := 0
	for batchIndex := 0; batchIndex < maxBatches; batchIndex++ {
		flushed, err := FlushDecisionAuditsContext(ctx)
		if err != nil {
			return flushedTotal, err
		}
		flushedTotal += flushed
		if flushed < model.RoutingDecisionAuditMaxBatch {
			return flushedTotal, nil
		}
		if err := ctx.Err(); err != nil {
			return flushedTotal, err
		}
	}
	return flushedTotal, nil
}

func refreshTopologySnapshotContext(ctx context.Context, forceReconcile bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	refreshCtx, cancel := context.WithTimeout(ctx, observeRefreshTimeout)
	defer cancel()
	nowMs := time.Now().UnixMilli()
	lease, acquired, err := model.TryAcquireRoutingControlLeaseContext(
		refreshCtx,
		routingLegacyReconcileLeaseName,
		NodeEpochID(),
		nowMs,
		int64(routingLegacyLeaseTTL/time.Millisecond),
		int64(routingLegacyMinInterval/time.Millisecond),
		forceReconcile,
	)
	if err != nil {
		return err
	}
	if acquired {
		if _, err := model.ReconcileLegacyRoutingTopologyContext(refreshCtx); err != nil {
			finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer finishCancel()
			releaseErr := model.ReleaseRoutingControlLeaseContext(finishCtx, lease, time.Now().UnixMilli())
			return errors.Join(err, releaseErr)
		}
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer finishCancel()
		if err := model.CompleteRoutingControlLeaseContext(finishCtx, lease, time.Now().UnixMilli()); err != nil {
			return err
		}
	} else if forceReconcile {
		return ErrRoutingLegacyReconcileBusy
	}
	metadata, err := refreshSnapshotIfNeededContext(refreshCtx, acquired)
	if err != nil {
		return err
	}
	if err := persistRoutingConfigCheckpointContext(ctx, RoutingConfigStreamRuntimeStats().Cursor, int64(metadata.Revision)); err != nil {
		routingConfigState.markCheckpointFailure(err)
	}
	return nil
}

func refreshSnapshotIfNeededContext(ctx context.Context, force bool) (SnapshotMetadata, error) {
	head, err := model.GetRoutingPolicyHeadContext(ctx)
	if err != nil {
		return SnapshotMetadata{}, err
	}
	current, available := CurrentSnapshotMetadata()
	if available {
		if int64(current.Revision) > head.CurrentRevision {
			return SnapshotMetadata{}, ErrSnapshotRevisionRollback
		}
		if int64(current.Revision) == head.CurrentRevision &&
			(current.PolicyHash != head.CurrentHash || current.ActivationID != head.CurrentActivationID ||
				current.ActivationStage != head.CurrentStage) {
			return SnapshotMetadata{}, ErrSnapshotRevisionConflict
		}
		age := time.Since(time.Unix(current.BuiltAtUnix, 0))
		if age < 0 {
			age = 0
		}
		if !force && int64(current.Revision) == head.CurrentRevision && current.PolicyHash == head.CurrentHash &&
			current.ActivationID == head.CurrentActivationID && current.ActivationStage == head.CurrentStage &&
			age < observeSnapshotFallback {
			return current, nil
		}
	}
	view, err := RefreshSnapshotContext(ctx)
	if err != nil {
		return SnapshotMetadata{}, err
	}
	return snapshotMetadata(view), nil
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

	workerCount := 2
	if deps.drainLocal != nil {
		workerCount++
	}
	if deps.consumeTelemetry != nil {
		workerCount++
	}
	if deps.publishConfig != nil {
		workerCount++
	}
	if deps.consumeConfig != nil {
		workerCount++
	}
	if deps.heartbeatCanary != nil {
		workerCount++
	}
	if deps.evaluateCanary != nil {
		workerCount++
	}
	if deps.executeCanary != nil {
		workerCount++
	}
	if deps.retention != nil {
		workerCount++
	}
	runtime.wait.Add(workerCount)
	go func() {
		defer runtime.wait.Done()
		runtime.runWorker(ctx, deps.refresh, observeRefreshInterval, &runtime.refreshStats)
	}()
	go func() {
		defer runtime.wait.Done()
		runtime.runWorker(ctx, deps.flush, observeFlushInterval, &runtime.flushStats)
	}()
	if deps.drainLocal != nil {
		go func() {
			defer runtime.wait.Done()
			runtime.runWorker(ctx, deps.drainLocal, observeLocalDrainInterval, &runtime.localDrainStats)
		}()
	}
	if deps.consumeTelemetry != nil {
		go func() {
			defer runtime.wait.Done()
			runtime.runWorker(ctx, deps.consumeTelemetry, observeStreamInterval, &runtime.streamStats)
		}()
	}
	if deps.publishConfig != nil {
		go func() {
			defer runtime.wait.Done()
			runtime.runWorker(ctx, deps.publishConfig, observeStreamInterval, &runtime.configPublishStats)
		}()
	}
	if deps.consumeConfig != nil {
		go func() {
			defer runtime.wait.Done()
			runtime.runWorker(ctx, deps.consumeConfig, observeStreamInterval, &runtime.configConsumeStats)
		}()
	}
	if deps.heartbeatCanary != nil {
		go func() {
			defer runtime.wait.Done()
			runtime.runWorker(ctx, deps.heartbeatCanary, canaryNodePresenceInterval, &runtime.canaryPresenceStats)
		}()
	}
	if deps.evaluateCanary != nil {
		go func() {
			defer runtime.wait.Done()
			runtime.runWorker(ctx, deps.evaluateCanary, canaryControlInterval, &runtime.canaryEvaluateStats)
		}()
	}
	if deps.executeCanary != nil {
		go func() {
			defer runtime.wait.Done()
			runtime.runWorker(ctx, deps.executeCanary, canaryOperationInterval, &runtime.canaryOperationStats)
		}()
	}
	if deps.retention != nil {
		go func() {
			defer runtime.wait.Done()
			runtime.runWorker(ctx, func(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) error {
				return runtime.runRetention(ctx, setting.RetentionDays)
			}, observeRetentionInterval, &runtime.retentionStats)
		}()
	}
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
		Refresh:            runtime.refreshStats.snapshot(),
		Flush:              runtime.flushStats.snapshot(),
		LocalDrain:         runtime.localDrainStats.snapshot(),
		TelemetryStream:    runtime.streamStats.snapshot(),
		ConfigPublisher:    runtime.configPublishStats.snapshot(),
		ConfigConsumer:     runtime.configConsumeStats.snapshot(),
		CanaryNodePresence: runtime.canaryPresenceStats.snapshot(),
		CanaryEvaluator:    runtime.canaryEvaluateStats.snapshot(),
		CanaryOperations:   runtime.canaryOperationStats.snapshot(),
		Retention:          runtime.retentionStats.snapshot(),
		Audit:              DecisionAuditsStats(),
		Telemetry:          routingmetrics.StableRuntimeStats(),
		TelemetryTransport: RoutingTelemetryTransportRuntimeStats(),
		ConfigStream:       RoutingConfigStreamRuntimeStats(),
		NodeEpochID:        NodeEpochID(),
	}
	if snapshot := currentSnapshot.Load(); snapshot != nil {
		stats.SnapshotRevision = snapshot.view.Revision
		stats.SnapshotRuntimeGeneration = snapshot.view.RuntimeGeneration
		stats.SnapshotPolicyHash = snapshot.view.PolicyHash
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
		drainWhileDisabled := !setting.Enabled && runtime.shouldDrainWhileDisabled(state)
		if (!setting.Enabled && !drainWhileDisabled) || run == nil {
			consecutiveFailures = 0
			state.consecutiveFailures.Store(0)
			if !runtime.waitNext(ctx, observeDisabledPoll, state) {
				return
			}
			continue
		}

		started := time.Now()
		selectedRun := run
		forcedRefresh := false
		if state == &runtime.refreshStats && runtime.deps.refreshCurrent != nil {
			forcedRefresh = runtime.topologyDirty.Swap(false)
			if !forcedRefresh {
				selectedRun = runtime.deps.refreshCurrent
			}
		}
		err := selectedRun(ctx, setting)
		if err != nil && forcedRefresh {
			runtime.topologyDirty.Store(true)
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
		runtime.topologyDirty.Store(true)
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
			if runtime.deps.flush == nil || !hasPendingRoutingWrites() {
				return
			}
			ctx, cancel := context.WithTimeout(parent, observeFinalTimeout)
			defer cancel()
			var err error
			failedAttempts := 0
			for {
				before := pendingRoutingWrites()
				if !before.hasWrites() {
					break
				}
				err = runtime.deps.flush(ctx, runtime.deps.getSetting())
				if err != nil {
					failedAttempts++
					if failedAttempts >= observeFinalFlushMaxAttempts || ctx.Err() != nil {
						break
					}
					retryDelay := observeFinalFlushRetryBase * time.Duration(1<<(failedAttempts-1))
					if !waitRuntime(ctx, retryDelay) {
						err = ctx.Err()
						break
					}
					continue
				}
				failedAttempts = 0
				after := pendingRoutingWrites()
				if !after.hasWrites() || !after.lessThan(before) {
					break
				}
			}
			runtime.finalMu.Lock()
			runtime.finalErr = err
			runtime.finalMu.Unlock()
		}()
	})
}

func (runtime *Runtime) shouldDrainWhileDisabled(state *runtimeWorkerState) bool {
	if runtime == nil {
		return false
	}
	if state == &runtime.retentionStats {
		return true
	}
	if state != &runtime.flushStats && state != &runtime.localDrainStats {
		return false
	}
	return hasPendingRoutingWrites()
}

func hasPendingRoutingWrites() bool {
	return pendingRoutingWrites().hasWrites()
}

func pendingRoutingWrites() pendingRoutingWriteStats {
	return pendingRoutingWriteStats{
		audits:           DecisionAuditsStats().Entries,
		stableBuckets:    routingmetrics.StableRuntimeStats().Buckets,
		pendingEnvelopes: RoutingTelemetryTransportRuntimeStats().PendingEnvelopes,
		canaryWindows:    CurrentCanaryWindowStats().Entries,
	}
}

func (stats pendingRoutingWriteStats) hasWrites() bool {
	return stats.audits > 0 || stats.stableBuckets > 0 || stats.pendingEnvelopes > 0 || stats.canaryWindows > 0
}

func (stats pendingRoutingWriteStats) lessThan(previous pendingRoutingWriteStats) bool {
	return stats.audits < previous.audits ||
		stats.stableBuckets < previous.stableBuckets ||
		stats.pendingEnvelopes < previous.pendingEnvelopes ||
		stats.canaryWindows < previous.canaryWindows
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

func observeStreamInterval(smart_routing_setting.SmartRoutingSetting) time.Duration {
	return observeStreamPoll
}

func observeLocalDrainInterval(smart_routing_setting.SmartRoutingSetting) time.Duration {
	if common.RedisEnabled && common.RDB != nil {
		return observeLocalDrainRedis
	}
	return observeLocalDrainFallback
}

func canaryControlInterval(smart_routing_setting.SmartRoutingSetting) time.Duration {
	return canaryControlPollInterval
}

func canaryNodePresenceInterval(smart_routing_setting.SmartRoutingSetting) time.Duration {
	return canaryNodePresencePollInterval
}

func canaryOperationInterval(smart_routing_setting.SmartRoutingSetting) time.Duration {
	return canaryOperationPollInterval
}

func observeRetentionInterval(smart_routing_setting.SmartRoutingSetting) time.Duration {
	return 6 * time.Hour
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
