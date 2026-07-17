package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/channelrouting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
)

var smartRoutingRuntimeStateMu = newRoutingContextMutex()
var smartRoutingRetentionLast atomic.Int64
var smartRoutingBreakerConfigMu sync.Mutex
var smartRoutingBreakerConfigLast routingBreakerConfigIdentity

type SmartRoutingRuntime struct {
	cancel       context.CancelFunc
	wait         sync.WaitGroup
	done         chan struct{}
	finalDone    chan struct{}
	close        sync.Once
	finalStarted atomic.Bool
	finalErr     error
	deps         smartRoutingRuntimeDeps
	refreshStats smartRoutingWorkerStats
	flushStats   smartRoutingWorkerStats
	finalRuns    atomic.Int64
	finalErrors  atomic.Int64
}

type routingContextMutex struct {
	token chan struct{}
}

func newRoutingContextMutex() *routingContextMutex {
	mutex := &routingContextMutex{token: make(chan struct{}, 1)}
	mutex.token <- struct{}{}
	return mutex
}

func (mutex *routingContextMutex) Lock() {
	<-mutex.token
}

func (mutex *routingContextMutex) LockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-mutex.token:
		return nil
	}
}

func (mutex *routingContextMutex) Unlock() {
	mutex.token <- struct{}{}
}

type smartRoutingRuntimeDeps struct {
	getSetting  func() smart_routing_setting.SmartRoutingSetting
	refresh     func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	flush       func(context.Context, smart_routing_setting.SmartRoutingSetting) error
	waitRefresh func(context.Context, time.Duration) bool
	waitFlush   func(context.Context, time.Duration) bool
	jitter      common.JitterFunc
}

type SmartRoutingRuntimeStats struct {
	RefreshRuns              int64
	RefreshErrors            int64
	RefreshConsecutiveErrors int64
	RefreshRecoveries        int64
	FlushRuns                int64
	FlushErrors              int64
	FlushConsecutiveErrors   int64
	FlushRecoveries          int64
	FinalFlushRuns           int64
	FinalFlushErrors         int64
}

type smartRoutingWorkerStats struct {
	runs              atomic.Int64
	errors            atomic.Int64
	consecutiveErrors atomic.Int64
	recoveries        atomic.Int64
}

type routingBreakerConfigIdentity struct {
	consecutive5xx int
	failureRatePct int
	minVolume      int
	baseCooldown   int
	maxCooldown    int
}

// RegisterScheduledSystemTasks wires the periodic channel test, upstream model
// update, and async task polling (Midjourney / Suno / video) jobs into the
// system task framework so a DB lease dedups execution across multiple master
// instances and each run is recorded as one task row. Call this before
// service.StartSystemTaskRunner.
func RegisterScheduledSystemTasks() {
	service.RegisterSystemTaskHandler(channelTestHandler{})
	service.RegisterSystemTaskHandler(modelUpdateHandler{})
	service.RegisterSystemTaskHandler(midjourneyPollHandler{})
	service.RegisterSystemTaskHandler(asyncTaskPollHandler{})
	service.RegisterSystemTaskHandler(asyncBillingRecoveryHandler{})
	service.RegisterSystemTaskHandler(billingLogAuditHandler{})
}

func syncRoutingBreakerConfigFromSetting(setting smart_routing_setting.SmartRoutingSetting) {
	identity := routingBreakerConfigIdentity{
		consecutive5xx: setting.Consecutive5xx,
		failureRatePct: setting.FailureRatePct,
		minVolume:      setting.MinVolume,
		baseCooldown:   setting.BaseCooldownSec,
		maxCooldown:    setting.MaxCooldownSec,
	}
	smartRoutingBreakerConfigMu.Lock()
	defer smartRoutingBreakerConfigMu.Unlock()
	if identity == smartRoutingBreakerConfigLast {
		return
	}
	routingbreaker.ConfigureDefault(routingBreakerConfigFromSetting(setting))
	smartRoutingBreakerConfigLast = identity
}

func StartSmartRoutingRuntime(parent context.Context) *SmartRoutingRuntime {
	return newSmartRoutingRuntime(parent, defaultSmartRoutingRuntimeDeps())
}

func BootstrapSmartRoutingHotcacheContext(ctx context.Context) error {
	setting := smart_routing_setting.GetSetting()
	if !setting.Enabled {
		return nil
	}
	_, err := refreshRoutingHotcacheFromDB(ctx, setting)
	return err
}

func defaultSmartRoutingRuntimeDeps() smartRoutingRuntimeDeps {
	return smartRoutingRuntimeDeps{
		getSetting: smart_routing_setting.GetSetting,
		refresh: func(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) error {
			var err error
			if setting.Enabled {
				syncRoutingBreakerConfigFromSetting(setting)
				if err = channelrouting.RefreshRuntimeHealthContext(ctx); err == nil {
					_, err = refreshRoutingHotcacheFromDB(ctx, setting)
				}
			}
			routinghotcache.Prune(common.GetTimestamp(), int64(setting.SnapshotStaleSec))
			return err
		},
		flush: func(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) error {
			syncRoutingBreakerConfigFromSetting(setting)
			_, stateErr := flushRoutingRuntimeState(ctx, setting)
			healthErr := channelrouting.FlushRuntimeHealthContext(ctx)
			return errors.Join(stateErr, healthErr)
		},
		waitRefresh: waitRoutingRuntime,
		waitFlush:   waitRoutingRuntime,
		jitter:      common.FullJitter,
	}
}

func newSmartRoutingRuntime(parent context.Context, deps smartRoutingRuntimeDeps) *SmartRoutingRuntime {
	if deps.waitRefresh == nil {
		deps.waitRefresh = waitRoutingRuntime
	}
	if deps.waitFlush == nil {
		deps.waitFlush = waitRoutingRuntime
	}
	ctx, cancel := context.WithCancel(parent)
	runtime := &SmartRoutingRuntime{
		cancel:    cancel,
		done:      make(chan struct{}),
		finalDone: make(chan struct{}),
		deps:      deps,
	}
	runtime.wait.Add(2)

	go func() {
		defer runtime.wait.Done()
		runtime.runWorker(ctx, deps.refresh, func(smartSetting smart_routing_setting.SmartRoutingSetting) time.Duration {
			interval := time.Duration(smartSetting.HotcacheRefreshSec) * time.Second
			if interval <= 0 {
				return 3 * time.Second
			}
			return interval
		}, deps.waitRefresh, nil, &runtime.refreshStats)
	}()

	go func() {
		defer runtime.wait.Done()
		runtime.runWorker(ctx, deps.flush, func(smartSetting smart_routing_setting.SmartRoutingSetting) time.Duration {
			interval := time.Duration(smartSetting.FlushIntervalMin) * time.Minute
			if interval <= 0 {
				return time.Minute
			}
			return interval
		}, deps.waitFlush, func(smartSetting smart_routing_setting.SmartRoutingSetting) bool {
			return smartSetting.Enabled
		}, &runtime.flushStats)
	}()
	go func() {
		runtime.wait.Wait()
		close(runtime.done)
	}()

	return runtime
}

func (runtime *SmartRoutingRuntime) Close() {
	runtime.close.Do(runtime.cancel)
}

func (runtime *SmartRoutingRuntime) Wait(ctx context.Context) error {
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

	if runtime.finalStarted.CompareAndSwap(false, true) {
		runtime.finalRuns.Add(1)
		runtime.finalErr = runtime.deps.flush(ctx, runtime.deps.getSetting())
		if runtime.finalErr != nil {
			runtime.finalErrors.Add(1)
		}
		close(runtime.finalDone)
		return runtime.finalErr
	}
	select {
	case <-runtime.finalDone:
		return runtime.finalErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (runtime *SmartRoutingRuntime) Stats() SmartRoutingRuntimeStats {
	return SmartRoutingRuntimeStats{
		RefreshRuns:              runtime.refreshStats.runs.Load(),
		RefreshErrors:            runtime.refreshStats.errors.Load(),
		RefreshConsecutiveErrors: runtime.refreshStats.consecutiveErrors.Load(),
		RefreshRecoveries:        runtime.refreshStats.recoveries.Load(),
		FlushRuns:                runtime.flushStats.runs.Load(),
		FlushErrors:              runtime.flushStats.errors.Load(),
		FlushConsecutiveErrors:   runtime.flushStats.consecutiveErrors.Load(),
		FlushRecoveries:          runtime.flushStats.recoveries.Load(),
		FinalFlushRuns:           runtime.finalRuns.Load(),
		FinalFlushErrors:         runtime.finalErrors.Load(),
	}
}

func (runtime *SmartRoutingRuntime) runWorker(
	ctx context.Context,
	run func(context.Context, smart_routing_setting.SmartRoutingSetting) error,
	interval func(smart_routing_setting.SmartRoutingSetting) time.Duration,
	wait func(context.Context, time.Duration) bool,
	enabled func(smart_routing_setting.SmartRoutingSetting) bool,
	stats *smartRoutingWorkerStats,
) {
	for ctx.Err() == nil {
		setting := runtime.deps.getSetting()
		if enabled != nil && !enabled(setting) {
			if !wait(ctx, interval(setting)) {
				return
			}
			continue
		}

		stats.runs.Add(1)
		err := run(ctx, setting)
		if ctx.Err() != nil {
			return
		}

		delay := interval(setting)
		if err != nil {
			stats.errors.Add(1)
			failures := stats.consecutiveErrors.Add(1)
			delay = common.CappedExponentialBackoff(int(failures), time.Second, time.Minute, runtime.deps.jitter)
		} else if stats.consecutiveErrors.Swap(0) > 0 {
			stats.recoveries.Add(1)
		}
		if !wait(ctx, delay) {
			return
		}
	}
}

func waitRoutingRuntime(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// channelTestHandler runs the scheduled "test all channels" job. Enablement and
// cadence still come from the monitor settings; only the execution path moved
// into the system task runner.
type channelTestHandler struct{}

func (channelTestHandler) Type() string { return model.SystemTaskTypeChannelTest }

func (channelTestHandler) Enabled() bool {
	return operation_setting.GetMonitorSetting().AutoTestChannelEnabled
}

func (channelTestHandler) Interval() time.Duration {
	minutes := operation_setting.GetMonitorSetting().AutoTestChannelMinutes
	if minutes <= 0 {
		minutes = 10
	}
	return time.Duration(minutes * float64(time.Minute))
}

func (channelTestHandler) NewPayload() any { return nil }

// channelTestTaskPayload controls one channel_test run. A nil/empty payload is a
// scheduled run, which uses the configured monitor ChannelTestMode and does not
// notify. A manual "test all channels" trigger sets Mode=scheduled_all and
// Notify=true to reproduce the legacy manual behavior (test every channel and
// notify root on completion).
type channelTestTaskPayload struct {
	Mode   string `json:"mode,omitempty"`
	Notify bool   `json:"notify,omitempty"`
}

func (channelTestHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := channelTestTaskPayload{}
	if err := task.DecodePayload(&payload); err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	summary, err := runChannelTestTask(ctx, payload.Mode, payload.Notify, service.NewSystemTaskProgressReporter(task, runnerID))
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// modelUpdateHandler runs the scheduled upstream model update detection job.
type modelUpdateHandler struct{}

func (modelUpdateHandler) Type() string { return model.SystemTaskTypeModelUpdate }

func (modelUpdateHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_ENABLED", true)
}

func (modelUpdateHandler) Interval() time.Duration {
	intervalMinutes := common.GetEnvOrDefault(
		"CHANNEL_UPSTREAM_MODEL_UPDATE_TASK_INTERVAL_MINUTES",
		channelUpstreamModelUpdateTaskDefaultIntervalMinutes,
	)
	if intervalMinutes < 1 {
		intervalMinutes = channelUpstreamModelUpdateTaskDefaultIntervalMinutes
	}
	return time.Duration(intervalMinutes) * time.Minute
}

func (modelUpdateHandler) NewPayload() any { return nil }

// modelUpdateTaskPayload controls one model_update run. A scheduled run
// (Manual=false) respects the per-channel minimum check interval and may
// auto-apply detected models when a channel has auto-sync enabled. A manual
// "detect all" trigger sets Manual=true to reproduce the legacy detect-all
// semantics: force a re-check regardless of the interval and never auto-apply,
// so the admin reviews and applies changes explicitly.
type modelUpdateTaskPayload struct {
	Manual bool `json:"manual,omitempty"`
}

func (modelUpdateHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := modelUpdateTaskPayload{}
	if err := task.DecodePayload(&payload); err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	summary := runChannelUpstreamModelUpdateTaskOnce(ctx, payload.Manual, !payload.Manual, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// midjourneyPollHandler runs one Midjourney polling pass per scheduled run.
// Enabled() folds the "are there unfinished tasks?" check into enablement so the
// scheduler creates no row when the system is idle; only when at least one
// Midjourney task is in progress does a row get scheduled.
type midjourneyPollHandler struct{}

func (midjourneyPollHandler) Type() string { return model.SystemTaskTypeMidjourneyPoll }

func (midjourneyPollHandler) Enabled() bool {
	return (constant.UpdateTask && model.HasUnfinishedMidjourneyTasks()) || model.HasPendingMidjourneyBillingOperations()
}

func (midjourneyPollHandler) Interval() time.Duration { return 15 * time.Second }

func (midjourneyPollHandler) NewPayload() any { return nil }

func (midjourneyPollHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	summary := runMidjourneyTaskUpdateOnce(ctx, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

// asyncTaskPollHandler runs one async-task (Suno/video) polling pass per
// scheduled run. Like midjourneyPollHandler, Enabled() folds in the unfinished
// task existence check so an idle system schedules no rows.
type asyncTaskPollHandler struct{}

func (asyncTaskPollHandler) Type() string { return model.SystemTaskTypeAsyncTaskPoll }

func (asyncTaskPollHandler) Enabled() bool {
	return (constant.UpdateTask && model.HasUnfinishedSyncTasks()) || model.HasRecoverableTaskBillingOperations()
}

func (asyncTaskPollHandler) Interval() time.Duration { return 15 * time.Second }

func (asyncTaskPollHandler) NewPayload() any { return nil }

func (asyncTaskPollHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	summary := service.RunTaskPollingOnce(ctx, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

type asyncBillingRecoveryHandler struct{}

func (asyncBillingRecoveryHandler) Type() string { return model.SystemTaskTypeAsyncBillingRecovery }

func (asyncBillingRecoveryHandler) Enabled() bool {
	return model.HasAsyncBillingRecoveryWork(time.Now())
}

func (asyncBillingRecoveryHandler) Interval() time.Duration { return 30 * time.Second }

func (handler asyncBillingRecoveryHandler) NewPayload() any {
	cursor := service.AsyncBillingRecoveryCursor{}
	latest, err := model.GetLatestSystemTask(handler.Type())
	if err != nil || latest == nil || strings.TrimSpace(latest.Result) == "" {
		return cursor
	}
	var previous service.AsyncBillingRecoverySummary
	if err := common.UnmarshalJsonStr(latest.Result, &previous); err != nil {
		return cursor
	}
	cursor.TaskTerminalAfterID = previous.NextTaskTerminalAfterID
	cursor.MidjourneyTerminalAfterID = previous.NextMidjourneyTerminalAfterID
	cursor.ReceiptCleanupAfterID = previous.NextReceiptCleanupAfterID
	return cursor
}

func (asyncBillingRecoveryHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	cursor := service.AsyncBillingRecoveryCursor{}
	if task != nil && strings.TrimSpace(task.Payload) != "" {
		if err := common.UnmarshalJsonStr(task.Payload, &cursor); err != nil {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
			return
		}
	}
	summary := service.RunAsyncBillingRecoveryOnceWithCursor(ctx, runnerID, cursor)
	status := model.SystemTaskStatusSucceeded
	var runErr error
	if summary.Errors > 0 {
		status = model.SystemTaskStatusFailed
		runErr = fmt.Errorf("async billing recovery completed with %d errors", summary.Errors)
	}
	finishSystemTaskHandler(task, runnerID, status, summary, runErr)
}

const billingLogAuditInterval = 5 * time.Minute
const (
	billingProjectionCleanupPageSize  = 500
	billingLogConflictAuditPageSize   = 1000
	billingLogConflictAuditMaxPages   = 20
	billingLogConflictAuditTimeBudget = 30 * time.Second
	billingLogConflictAuditOverlap    = 15 * time.Minute
	// The scheduler waits another audit interval after completion. Scanning up
	// to 350k rows per type preserves at least ~833 candidates/second even when
	// a run consumes its full two-minute budget.
	billingProjectionCleanupMaxPagesPerRun = 700
	billingProjectionCleanupTimeBudget     = 2 * time.Minute
)

type billingLogAuditPayload struct {
	InsertedAfter    int64  `json:"inserted_after"`
	AuditThrough     int64  `json:"audit_through,omitempty"`
	ConflictAfterKey string `json:"conflict_after_key,omitempty"`
	StatsAfterID     int64  `json:"stats_after_id,omitempty"`
	LogsAfterID      int64  `json:"logs_after_id,omitempty"`
	AdminOpsAfterID  int64  `json:"admin_ops_after_id,omitempty"`
}

type billingLogAuditResult struct {
	InsertedAfter           int64  `json:"inserted_after"`
	AuditThrough            int64  `json:"audit_through"`
	ConflictCount           int    `json:"conflict_count"`
	OpenConflictCount       int64  `json:"open_conflict_count"`
	ConflictPages           int    `json:"conflict_pages"`
	ConflictBudgetExhausted bool   `json:"conflict_budget_exhausted"`
	NextConflictAfterKey    string `json:"next_conflict_after_key,omitempty"`
	StatsDeleted            int64  `json:"stats_deleted"`
	LogsDeleted             int64  `json:"logs_deleted"`
	StatsPages              int    `json:"stats_pages"`
	LogsPages               int    `json:"logs_pages"`
	AdminOpsDeleted         int64  `json:"admin_ops_deleted"`
	AdminOpsHasMore         bool   `json:"admin_ops_has_more"`
	BudgetExhausted         bool   `json:"budget_exhausted"`
	NextStatsAfterID        int64  `json:"next_stats_after_id,omitempty"`
	NextLogsAfterID         int64  `json:"next_logs_after_id,omitempty"`
	NextAdminOpsAfterID     int64  `json:"next_admin_ops_after_id,omitempty"`
}

type billingProjectionCleanupResult struct {
	StatsDeleted     int64
	LogsDeleted      int64
	StatsPages       int
	LogsPages        int
	BudgetExhausted  bool
	NextStatsAfterID int64
	NextLogsAfterID  int64
}

type billingProjectionCleanupDeps struct {
	now       func() time.Time
	statsPage func(context.Context, time.Time, int64, int) (int64, int64, bool, error)
	logsPage  func(context.Context, time.Time, int64, int) (int64, int64, bool, error)
}

func drainExpiredBillingProjectionPages(
	ctx context.Context,
	cleanupNow time.Time,
	statsAfterID int64,
	logsAfterID int64,
	deps billingProjectionCleanupDeps,
) (billingProjectionCleanupResult, error) {
	result := billingProjectionCleanupResult{
		NextStatsAfterID: statsAfterID,
		NextLogsAfterID:  logsAfterID,
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cleanupNow.IsZero() {
		cleanupNow = time.Now()
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.statsPage == nil {
		deps.statsPage = model.CleanupExpiredBillingStatsProjectionsPage
	}
	if deps.logsPage == nil {
		deps.logsPage = model.CleanupExpiredBillingLogProjectionsPage
	}

	cleanupDeadline := cleanupNow.Add(billingProjectionCleanupTimeBudget)
	statsDone := false
	logsDone := false
	for !statsDone || !logsDone {
		progressed := false
		if !statsDone && result.StatsPages < billingProjectionCleanupMaxPagesPerRun &&
			deps.now().Before(cleanupDeadline) {
			deleted, afterID, hasMore, err := deps.statsPage(
				ctx, cleanupNow, result.NextStatsAfterID, billingProjectionCleanupPageSize,
			)
			if err != nil {
				return result, err
			}
			result.StatsDeleted += deleted
			result.StatsPages++
			progressed = true
			if hasMore {
				result.NextStatsAfterID = afterID
			} else {
				result.NextStatsAfterID = 0
				statsDone = true
			}
		}
		if !logsDone && result.LogsPages < billingProjectionCleanupMaxPagesPerRun &&
			deps.now().Before(cleanupDeadline) {
			deleted, afterID, hasMore, err := deps.logsPage(
				ctx, cleanupNow, result.NextLogsAfterID, billingProjectionCleanupPageSize,
			)
			if err != nil {
				return result, err
			}
			result.LogsDeleted += deleted
			result.LogsPages++
			progressed = true
			if hasMore {
				result.NextLogsAfterID = afterID
			} else {
				result.NextLogsAfterID = 0
				logsDone = true
			}
		}
		if !progressed {
			break
		}
	}
	result.BudgetExhausted = !statsDone || !logsDone
	return result, nil
}

type billingLogAuditHandler struct{}

func (billingLogAuditHandler) Type() string { return model.SystemTaskTypeBillingLogAudit }

func (billingLogAuditHandler) Enabled() bool {
	return model.BillingLogSinkConflictAuditEnabled() || model.BillingProjectionMaintenanceEnabled()
}

func (billingLogAuditHandler) Interval() time.Duration { return billingLogAuditInterval }

func (billingLogAuditHandler) NewPayload() any {
	now := time.Now()
	payload := billingLogAuditPayload{InsertedAfter: now.Add(-24 * time.Hour).Unix()}
	latest, err := model.GetLatestSystemTask(model.SystemTaskTypeBillingLogAudit)
	if err != nil || latest == nil {
		return payload
	}
	if latest.Status == model.SystemTaskStatusSucceeded {
		var previous billingLogAuditResult
		if common.UnmarshalJsonStr(latest.Result, &previous) == nil {
			payload.StatsAfterID = previous.NextStatsAfterID
			payload.LogsAfterID = previous.NextLogsAfterID
			payload.AdminOpsAfterID = previous.NextAdminOpsAfterID
			if previous.ConflictBudgetExhausted && previous.InsertedAfter > 0 &&
				previous.AuditThrough > previous.InsertedAfter && previous.NextConflictAfterKey != "" {
				payload.InsertedAfter = previous.InsertedAfter
				payload.AuditThrough = previous.AuditThrough
				payload.ConflictAfterKey = previous.NextConflictAfterKey
				return payload
			}
			if previous.AuditThrough > 0 {
				payload.InsertedAfter = time.Unix(previous.AuditThrough, 0).
					Add(-billingLogConflictAuditOverlap).Unix()
				return payload
			}
		}
		// Transitional fallback for tasks written before audit_through existed.
		payload.InsertedAfter = time.Unix(latest.UpdatedAt, 0).
			Add(-billingLogConflictAuditOverlap).Unix()
		return payload
	}
	var previous billingLogAuditPayload
	if latest.DecodePayload(&previous) == nil && previous.InsertedAfter > 0 &&
		previous.InsertedAfter <= now.Add(time.Minute).Unix() && previous.AuditThrough >= 0 &&
		(previous.AuditThrough == 0 || previous.AuditThrough > previous.InsertedAfter) &&
		previous.StatsAfterID >= 0 && previous.LogsAfterID >= 0 && previous.AdminOpsAfterID >= 0 &&
		len(previous.ConflictAfterKey) <= 64 {
		payload = previous
	}
	return payload
}

func (billingLogAuditHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := billingLogAuditPayload{}
	if err := task.DecodePayload(&payload); err != nil || payload.InsertedAfter <= 0 ||
		payload.InsertedAfter > time.Now().Add(time.Minute).Unix() || payload.AuditThrough < 0 ||
		(payload.AuditThrough > 0 && payload.AuditThrough <= payload.InsertedAfter) ||
		payload.StatsAfterID < 0 || payload.LogsAfterID < 0 || payload.AdminOpsAfterID < 0 ||
		len(payload.ConflictAfterKey) > 64 {
		if err == nil {
			err = errors.New("billing log audit payload is invalid")
		}
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	auditThrough := time.Unix(payload.AuditThrough, 0)
	if payload.AuditThrough == 0 {
		var err error
		auditThrough, err = model.BillingLogSinkAuditWindowEnd(ctx)
		if err != nil {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
			return
		}
	}
	conflictCount := 0
	conflictPages := 0
	conflictAfterKey := payload.ConflictAfterKey
	conflictHasMore := false
	conflictDeadline := time.Now().Add(billingLogConflictAuditTimeBudget)
	for conflictPages < billingLogConflictAuditMaxPages && time.Now().Before(conflictDeadline) {
		page, err := model.AuditBillingLogSinkConflictsPage(
			ctx, time.Unix(payload.InsertedAfter, 0), auditThrough,
			conflictAfterKey, billingLogConflictAuditPageSize,
		)
		if err != nil {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
			return
		}
		conflictPages++
		if err := model.QuarantineBillingLogSinkConflicts(ctx, page.Conflicts, time.Now()); err != nil {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
			return
		}
		conflictCount += len(page.Conflicts)
		conflictHasMore = page.HasMore
		if !page.HasMore {
			conflictAfterKey = ""
			break
		}
		if page.NextOperationKey == "" || page.NextOperationKey <= conflictAfterKey {
			finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil,
				errors.New("billing log conflict audit cursor did not advance"))
			return
		}
		conflictAfterKey = page.NextOperationKey
	}
	openConflictCount, err := model.CountOpenBillingLogSinkConflicts(ctx)
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	cleanup, err := drainExpiredBillingProjectionPages(
		ctx, time.Now(), payload.StatsAfterID, payload.LogsAfterID, billingProjectionCleanupDeps{},
	)
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	adminOpsDeleted, nextAdminOpsAfterID, adminOpsHasMore, err :=
		model.CleanupExpiredBillingProjectionAdminOperationsPage(
			ctx, time.Now(), payload.AdminOpsAfterID, billingProjectionCleanupPageSize,
		)
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, billingLogAuditResult{
		InsertedAfter:           payload.InsertedAfter,
		AuditThrough:            auditThrough.Unix(),
		ConflictCount:           conflictCount,
		OpenConflictCount:       openConflictCount,
		ConflictPages:           conflictPages,
		ConflictBudgetExhausted: conflictHasMore,
		NextConflictAfterKey:    conflictAfterKey,
		StatsDeleted:            cleanup.StatsDeleted,
		LogsDeleted:             cleanup.LogsDeleted,
		StatsPages:              cleanup.StatsPages,
		LogsPages:               cleanup.LogsPages,
		AdminOpsDeleted:         adminOpsDeleted,
		AdminOpsHasMore:         adminOpsHasMore,
		BudgetExhausted:         cleanup.BudgetExhausted,
		NextStatsAfterID:        cleanup.NextStatsAfterID,
		NextLogsAfterID:         cleanup.NextLogsAfterID,
		NextAdminOpsAfterID:     nextAdminOpsAfterID,
	}, nil)
}

func flushRoutingRuntimeState(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) (summary map[string]any, resultErr error) {
	if err := smartRoutingRuntimeStateMu.LockContext(ctx); err != nil {
		return map[string]any{"metrics": 0, "breakers": 0}, err
	}
	defer smartRoutingRuntimeStateMu.Unlock()

	summary = map[string]any{
		"metrics":  0,
		"breakers": 0,
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	acceptedChannelFences := make(map[int]model.RoutingChannelStateFence)
	fencedChannelFences := make(map[int]model.RoutingChannelStateFence)
	rejectedChannels := make(map[int]struct{})
	persistedMetricsByChannel := make(map[int][]model.RoutingChannelMetric)
	persistedBreakerCounts := make(map[int]int)
	defer func() {
		persistedMetrics := make([]model.RoutingChannelMetric, 0)
		for channelID, metrics := range persistedMetricsByChannel {
			if _, accepted := acceptedChannelFences[channelID]; accepted {
				persistedMetrics = append(persistedMetrics, metrics...)
			}
		}
		routinghotcache.ApplyMetricDeltas(persistedMetrics, setting.MetricBucketSec)

		metricCount, breakerCount, verifyErr := finalizeFlushedRoutingChannelState(
			ctx,
			acceptedChannelFences,
			persistedMetricsByChannel,
			persistedBreakerCounts,
		)
		if verifyErr != nil {
			if resultErr == nil {
				resultErr = verifyErr
			}
			return
		}
		if resultErr == nil {
			summary["metrics"] = metricCount
			summary["breakers"] = breakerCount
		}
	}()

	drainedMetrics := routingmetrics.DrainSnapshots()
	eligibilityByChannel := make(map[int]model.LegacyRoutingStateEligibility)
	validMetrics := make([]model.RoutingChannelMetric, 0, len(drainedMetrics))
	for metricIndex, metric := range drainedMetrics {
		if metric.APIKeyIndex != model.RoutingMetricSingleKeyIndex {
			continue
		}
		eligibility, checked := eligibilityByChannel[metric.ChannelID]
		if !checked {
			var err error
			eligibility, err = model.ResolveLegacyRoutingStateEligibilityContext(ctx, metric.ChannelID, metric.APIKeyIndex)
			if err != nil {
				retryMetrics := make([]model.RoutingChannelMetric, 0, len(validMetrics)+len(drainedMetrics)-metricIndex)
				retryMetrics = append(retryMetrics, validMetrics...)
				for _, pending := range drainedMetrics[metricIndex:] {
					if pending.APIKeyIndex != model.RoutingMetricSingleKeyIndex {
						continue
					}
					if knownEligibility, known := eligibilityByChannel[pending.ChannelID]; known && !knownEligibility.Supported() {
						continue
					}
					retryMetrics = append(retryMetrics, pending)
				}
				routingmetrics.RequeueSnapshots(retryMetrics)
				return summary, err
			}
			eligibilityByChannel[metric.ChannelID] = eligibility
		}
		if eligibility.Supported() {
			validMetrics = append(validMetrics, metric)
		}
	}
	for i := range validMetrics {
		metric := validMetrics[i]
		if _, rejected := rejectedChannels[metric.ChannelID]; rejected {
			clearRoutingRuntimeChannelState(metric.ChannelID)
			continue
		}
		eligibility := eligibilityByChannel[metric.ChannelID]
		expectedFence := fencedChannelFences[metric.ChannelID]
		fence, stateAccepted, err := eligibility.UpsertRoutingChannelMetricForChannelContext(ctx, &metric, expectedFence)
		if err != nil {
			routingmetrics.RequeueSnapshots(validMetrics[i:])
			if ctxErr := ctx.Err(); ctxErr != nil {
				return summary, ctxErr
			}
			return summary, err
		}
		if !fence.Valid() {
			clearRoutingRuntimeChannelState(metric.ChannelID)
			rejectedChannels[metric.ChannelID] = struct{}{}
			delete(acceptedChannelFences, metric.ChannelID)
			delete(persistedMetricsByChannel, metric.ChannelID)
			delete(persistedBreakerCounts, metric.ChannelID)
			continue
		}
		if !expectedFence.Valid() {
			fencedChannelFences[metric.ChannelID] = fence
		} else if fence != expectedFence {
			clearRoutingRuntimeChannelState(metric.ChannelID)
			rejectedChannels[metric.ChannelID] = struct{}{}
			delete(acceptedChannelFences, metric.ChannelID)
			delete(persistedMetricsByChannel, metric.ChannelID)
			delete(persistedBreakerCounts, metric.ChannelID)
			continue
		}
		if !stateAccepted {
			if metric.ChannelGeneration != "" && metric.ChannelGeneration != fence.Generation {
				continue
			}
			clearRoutingRuntimeChannelState(metric.ChannelID)
			rejectedChannels[metric.ChannelID] = struct{}{}
			delete(acceptedChannelFences, metric.ChannelID)
			delete(persistedMetricsByChannel, metric.ChannelID)
			delete(persistedBreakerCounts, metric.ChannelID)
			continue
		}
		matches, verifyErr := model.RoutingChannelStateFenceMatchesContext(ctx, fence)
		if verifyErr != nil {
			if acceptedFence, accepted := acceptedChannelFences[metric.ChannelID]; accepted && acceptedFence != fence {
				clearRoutingRuntimeChannelState(metric.ChannelID)
				rejectedChannels[metric.ChannelID] = struct{}{}
				delete(acceptedChannelFences, metric.ChannelID)
				delete(persistedMetricsByChannel, metric.ChannelID)
				delete(persistedBreakerCounts, metric.ChannelID)
			} else {
				acceptedChannelFences[metric.ChannelID] = fence
				persistedMetricsByChannel[metric.ChannelID] = append(persistedMetricsByChannel[metric.ChannelID], metric)
			}
			routingmetrics.RequeueSnapshots(validMetrics[i+1:])
			return summary, verifyErr
		}
		if !matches {
			clearRoutingRuntimeChannelState(metric.ChannelID)
			rejectedChannels[metric.ChannelID] = struct{}{}
			delete(acceptedChannelFences, metric.ChannelID)
			delete(persistedMetricsByChannel, metric.ChannelID)
			delete(persistedBreakerCounts, metric.ChannelID)
			continue
		}
		if acceptedFence, accepted := acceptedChannelFences[metric.ChannelID]; accepted && acceptedFence != fence {
			clearRoutingRuntimeChannelState(metric.ChannelID)
			rejectedChannels[metric.ChannelID] = struct{}{}
			delete(acceptedChannelFences, metric.ChannelID)
			delete(persistedMetricsByChannel, metric.ChannelID)
			delete(persistedBreakerCounts, metric.ChannelID)
			continue
		}
		acceptedChannelFences[metric.ChannelID] = fence
		persistedMetricsByChannel[metric.ChannelID] = append(persistedMetricsByChannel[metric.ChannelID], metric)
	}

	dirtyBreakers := routingbreaker.DirtySnapshots()
	validBreakers := make([]routingbreaker.Snapshot, 0, len(dirtyBreakers))
	for breakerIndex, snapshot := range dirtyBreakers {
		if snapshot.Key.APIKeyIndex != model.RoutingMetricSingleKeyIndex {
			continue
		}
		eligibility, checked := eligibilityByChannel[snapshot.Key.ChannelID]
		if !checked {
			var err error
			eligibility, err = model.ResolveLegacyRoutingStateEligibilityContext(ctx, snapshot.Key.ChannelID, snapshot.Key.APIKeyIndex)
			if err != nil {
				retryBreakers := make([]routingbreaker.Snapshot, 0, len(validBreakers)+len(dirtyBreakers)-breakerIndex)
				retryBreakers = append(retryBreakers, validBreakers...)
				for _, pending := range dirtyBreakers[breakerIndex:] {
					if pending.Key.APIKeyIndex != model.RoutingMetricSingleKeyIndex {
						continue
					}
					if knownEligibility, known := eligibilityByChannel[pending.Key.ChannelID]; known && !knownEligibility.Supported() {
						continue
					}
					retryBreakers = append(retryBreakers, pending)
				}
				routingbreaker.RequeueDirtySnapshots(retryBreakers)
				return summary, err
			}
			eligibilityByChannel[snapshot.Key.ChannelID] = eligibility
		}
		if eligibility.Supported() {
			validBreakers = append(validBreakers, snapshot)
		}
	}
	for i, snapshot := range validBreakers {
		if _, rejected := rejectedChannels[snapshot.Key.ChannelID]; rejected {
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			continue
		}
		state := routingBreakerSnapshotToModel(snapshot)
		eligibility := eligibilityByChannel[snapshot.Key.ChannelID]
		expectedFence := fencedChannelFences[snapshot.Key.ChannelID]
		fence, stateAccepted, err := eligibility.UpsertRoutingBreakerStateForChannelContext(ctx, &state, expectedFence)
		if err != nil {
			routingbreaker.RequeueDirtySnapshots(validBreakers[i:])
			if ctxErr := ctx.Err(); ctxErr != nil {
				return summary, ctxErr
			}
			return summary, err
		}
		if !fence.Valid() {
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
			delete(acceptedChannelFences, snapshot.Key.ChannelID)
			delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
			delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			continue
		}
		if !expectedFence.Valid() {
			fencedChannelFences[snapshot.Key.ChannelID] = fence
		} else if fence != expectedFence {
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
			delete(acceptedChannelFences, snapshot.Key.ChannelID)
			delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
			delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			continue
		}
		if !stateAccepted {
			if state.ChannelGeneration != "" && state.ChannelGeneration != fence.Generation {
				continue
			}
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
			delete(acceptedChannelFences, snapshot.Key.ChannelID)
			delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
			delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			continue
		}
		matches, verifyErr := model.RoutingChannelStateFenceMatchesContext(ctx, fence)
		if verifyErr != nil {
			if acceptedFence, accepted := acceptedChannelFences[snapshot.Key.ChannelID]; accepted && acceptedFence != fence {
				clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
				rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
				delete(acceptedChannelFences, snapshot.Key.ChannelID)
				delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
				delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			} else {
				acceptedChannelFences[snapshot.Key.ChannelID] = fence
				persistedBreakerCounts[snapshot.Key.ChannelID]++
			}
			routingbreaker.RequeueDirtySnapshots(validBreakers[i+1:])
			return summary, verifyErr
		}
		if !matches {
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
			delete(acceptedChannelFences, snapshot.Key.ChannelID)
			delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
			delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			continue
		}
		if acceptedFence, accepted := acceptedChannelFences[snapshot.Key.ChannelID]; accepted && acceptedFence != fence {
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
			delete(acceptedChannelFences, snapshot.Key.ChannelID)
			delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
			delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			continue
		}
		acceptedChannelFences[snapshot.Key.ChannelID] = fence
		persistedBreakerCounts[snapshot.Key.ChannelID]++
	}

	now := common.GetTimestamp()
	const (
		retentionIntervalSeconds int64 = 6 * 60 * 60
		secondsPerDay            int64 = 24 * 60 * 60
	)
	if (setting.RetentionDays > 0 || setting.HedgeAuditRetentionDays > 0) &&
		now-smartRoutingRetentionLast.Load() >= retentionIntervalSeconds {
		if setting.RetentionDays > 0 {
			cutoffTs := int64(0)
			retentionDays := int64(setting.RetentionDays)
			if retentionDays <= now/secondsPerDay {
				cutoffTs = now - retentionDays*secondsPerDay
			}
			deleted, err := model.DeleteRoutingMetricsBeforeContext(ctx, cutoffTs)
			if err != nil {
				return summary, err
			}
			summary["retained_metrics_deleted"] = deleted
		}
		if setting.HedgeAuditRetentionDays > 0 {
			cutoffMs := int64(0)
			retentionDays := int64(setting.HedgeAuditRetentionDays)
			if retentionDays <= now/secondsPerDay {
				cutoffMs = (now - retentionDays*secondsPerDay) * 1_000
			}
			deleted, err := model.DeleteRoutingHedgeAttemptAuditsBeforeContext(ctx, cutoffMs)
			if err != nil {
				return summary, err
			}
			summary["retained_hedge_audits_deleted"] = deleted
		}
		smartRoutingRetentionLast.Store(now)
	}
	return summary, nil
}

func clearRoutingRuntimeChannelState(channelID int) {
	routingmetrics.ClearChannel(channelID)
	routingbreaker.ClearDefaultChannelWithCache(channelID, routinghotcache.ClearChannel)
}

func finalizeFlushedRoutingChannelState(
	ctx context.Context,
	acceptedChannelFences map[int]model.RoutingChannelStateFence,
	persistedMetricsByChannel map[int][]model.RoutingChannelMetric,
	persistedBreakerCounts map[int]int,
) (int, int, error) {
	for channelID, fence := range acceptedChannelFences {
		matches, err := model.RoutingChannelStateFenceMatchesContext(ctx, fence)
		if err != nil {
			for acceptedChannelID := range acceptedChannelFences {
				clearRoutingRuntimeChannelState(acceptedChannelID)
			}
			return 0, 0, err
		}
		if matches {
			continue
		}
		clearRoutingRuntimeChannelState(channelID)
		delete(persistedMetricsByChannel, channelID)
		delete(persistedBreakerCounts, channelID)
	}

	metricCount := 0
	for channelID, metrics := range persistedMetricsByChannel {
		if _, accepted := acceptedChannelFences[channelID]; accepted {
			metricCount += len(metrics)
		}
	}
	breakerCount := 0
	for channelID, count := range persistedBreakerCounts {
		if _, accepted := acceptedChannelFences[channelID]; accepted {
			breakerCount += count
		}
	}
	return metricCount, breakerCount, nil
}

func refreshRoutingHotcacheFromDB(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) (map[string]any, error) {
	if err := smartRoutingRuntimeStateMu.LockContext(ctx); err != nil {
		return map[string]any{"metrics": 0, "breakers": 0, "health": 0}, err
	}
	defer smartRoutingRuntimeStateMu.Unlock()

	summary := map[string]any{
		"metrics":  0,
		"breakers": 0,
		"health":   0,
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	now := common.GetTimestamp()
	staleSeconds := int64(setting.SnapshotStaleSec)
	if staleSeconds <= 0 {
		staleSeconds = 1800
	}

	metricWindow := staleSeconds
	if bucketWindow := int64(setting.MetricBucketSec * 5); bucketWindow > metricWindow {
		metricWindow = bucketWindow
	}
	const routingSnapshotLimit = 5000
	eligibilityByChannel := make(map[int]model.LegacyRoutingStateEligibility)
	validMetrics := make([]model.RoutingChannelMetric, 0, routingSnapshotLimit)
	lastMetricBucketTs := int64(0)
	lastMetricID := 0
	for len(validMetrics) < routingSnapshotLimit {
		query := model.DB.WithContext(ctx).
			Where("bucket_ts >= ? AND api_key_index = ?", now-metricWindow, model.RoutingMetricSingleKeyIndex).
			Where(`EXISTS (
				SELECT 1 FROM channels
				WHERE channels.id = routing_channel_metrics.channel_id
					AND channels.routing_generation = routing_channel_metrics.channel_generation
			)`)
		if lastMetricID > 0 {
			query = query.Where("(bucket_ts < ? OR (bucket_ts = ? AND id < ?))", lastMetricBucketTs, lastMetricBucketTs, lastMetricID)
		}
		var page []model.RoutingChannelMetric
		if err := query.Order("bucket_ts desc").Order("id desc").Limit(routingSnapshotLimit).Find(&page).Error; err != nil {
			return summary, err
		}
		if len(page) == 0 {
			break
		}
		for _, metric := range page {
			eligibility, checked := eligibilityByChannel[metric.ChannelID]
			if !checked {
				resolved, resolveErr := model.ResolveLegacyRoutingStateEligibilityContext(ctx, metric.ChannelID, metric.APIKeyIndex)
				if resolveErr != nil {
					return summary, resolveErr
				}
				eligibility = resolved
				eligibilityByChannel[metric.ChannelID] = eligibility
			}
			if eligibility.Supported() {
				validMetrics = append(validMetrics, metric)
				if len(validMetrics) == routingSnapshotLimit {
					break
				}
			}
		}
		lastMetric := page[len(page)-1]
		lastMetricBucketTs = lastMetric.BucketTs
		lastMetricID = lastMetric.ID
		if len(page) < routingSnapshotLimit {
			break
		}
	}
	routinghotcache.LoadMetricSnapshots(validMetrics, setting.MetricBucketSec)
	summary["metrics"] = len(validMetrics)

	breakerCutoffUpdatedTime := time.Unix(now, 0).Add(-routingbreaker.DefaultEntryTTL()).Unix()
	validBreakerStates := make([]model.RoutingBreakerState, 0, routingSnapshotLimit)
	lastBreakerUpdatedTime := int64(0)
	lastBreakerID := 0
	for len(validBreakerStates) < routingSnapshotLimit {
		page, err := model.GetRoutingBreakerStatesForHydrationPageContext(
			ctx,
			routingSnapshotLimit,
			breakerCutoffUpdatedTime,
			lastBreakerUpdatedTime,
			lastBreakerID,
		)
		if err != nil {
			return summary, err
		}
		if len(page) == 0 {
			break
		}
		for _, state := range page {
			eligibility, checked := eligibilityByChannel[state.ChannelID]
			if !checked {
				resolved, resolveErr := model.ResolveLegacyRoutingStateEligibilityContext(ctx, state.ChannelID, state.APIKeyIndex)
				if resolveErr != nil {
					return summary, resolveErr
				}
				eligibility = resolved
				eligibilityByChannel[state.ChannelID] = eligibility
			}
			if eligibility.Supported() {
				validBreakerStates = append(validBreakerStates, state)
				if len(validBreakerStates) == routingSnapshotLimit {
					break
				}
			}
		}
		lastBreaker := page[len(page)-1]
		lastBreakerUpdatedTime = lastBreaker.UpdatedTime
		lastBreakerID = lastBreaker.ID
		if len(page) < routingSnapshotLimit {
			break
		}
	}
	accepted := routingBreakerModelsToSnapshots(validBreakerStates)
	retained := routingbreaker.HydrateDefaultSnapshots(accepted)
	summary["breakers"] = len(retained)

	var healthStates []model.RoutingChannelHealthState
	if err := model.DB.WithContext(ctx).Where(`EXISTS (
		SELECT 1 FROM channels
		WHERE channels.id = routing_channel_health_states.channel_id
			AND channels.routing_generation = routing_channel_health_states.channel_generation
	)`).Order("updated_time desc").Limit(5000).Find(&healthStates).Error; err != nil {
		return summary, err
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	routinghotcache.LoadHealthSnapshots(healthStates, now)
	summary["health"] = len(healthStates)
	return summary, nil
}

func routingBreakerSnapshotToModel(snapshot routingbreaker.Snapshot) model.RoutingBreakerState {
	state := model.RoutingBreakerState{
		ChannelID:           snapshot.Key.ChannelID,
		ChannelGeneration:   snapshot.Key.ChannelGeneration,
		APIKeyIndex:         snapshot.Key.APIKeyIndex,
		ModelName:           snapshot.Key.Model,
		Group:               snapshot.Key.Group,
		SemanticVersion:     model.RoutingBreakerSemanticVersion,
		ResetGeneration:     snapshot.ResetGeneration,
		State:               string(snapshot.State),
		Reason:              snapshot.Reason,
		ConsecutiveFailures: int64(snapshot.ConsecutiveFailures),
		Consecutive5xx:      int64(snapshot.Consecutive5xx),
		EjectionCount:       int64(snapshot.EjectionCount),
		WindowRequests:      int64(snapshot.WindowRequests),
		WindowFailures:      int64(snapshot.WindowFailures),
	}
	if !snapshot.UpdatedAt.IsZero() {
		state.UpdatedTime = snapshot.UpdatedAt.Unix()
	} else {
		state.UpdatedTime = common.GetTimestamp()
	}
	if !snapshot.OpenedAt.IsZero() {
		state.OpenedAt = snapshot.OpenedAt.Unix()
	}
	if !snapshot.CooldownUntil.IsZero() {
		state.CooldownUntil = snapshot.CooldownUntil.Unix()
	}
	return state
}

func routingBreakerModelsToSnapshots(states []model.RoutingBreakerState) []routingbreaker.Snapshot {
	snapshots := make([]routingbreaker.Snapshot, 0, len(states))
	for _, state := range states {
		if state.SemanticVersion != model.RoutingBreakerSemanticVersion ||
			state.APIKeyIndex != model.RoutingMetricSingleKeyIndex ||
			state.ChannelID <= 0 || state.ModelName == "" || state.Group == "" {
			continue
		}
		snapshot := routingbreaker.Snapshot{
			Key: routingbreaker.Key{
				ChannelID:         state.ChannelID,
				ChannelGeneration: state.ChannelGeneration,
				APIKeyIndex:       state.APIKeyIndex,
				Model:             state.ModelName,
				Group:             state.Group,
			},
			State:               routingbreaker.State(state.State),
			ResetGeneration:     state.ResetGeneration,
			Reason:              state.Reason,
			ConsecutiveFailures: int(state.ConsecutiveFailures),
			Consecutive5xx:      int(state.Consecutive5xx),
			EjectionCount:       int(state.EjectionCount),
			HalfOpenInflight:    0,
			WindowRequests:      int(state.WindowRequests),
			WindowFailures:      int(state.WindowFailures),
		}
		if state.OpenedAt > 0 {
			snapshot.OpenedAt = time.Unix(state.OpenedAt, 0)
		}
		if state.CooldownUntil > 0 {
			snapshot.CooldownUntil = time.Unix(state.CooldownUntil, 0)
		}
		if state.UpdatedTime > 0 {
			snapshot.UpdatedAt = time.Unix(state.UpdatedTime, 0)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots
}

func finishSystemTaskHandler(task *model.SystemTask, runnerID string, status model.SystemTaskStatus, result any, runErr error) {
	errorMessage := ""
	if runErr != nil {
		errorMessage = common.SanitizeErrorMessage(runErr.Error())
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, status, result, errorMessage); err != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %s", task.TaskID, common.SanitizeErrorMessage(err.Error())))
	}
}
