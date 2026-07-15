package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
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
	"gorm.io/gorm"
)

var smartRoutingRuntimeStateMu = newRoutingContextMutex()
var smartRoutingRetentionLast atomic.Int64
var smartRoutingBreakerConfigMu sync.Mutex
var smartRoutingBreakerConfigLast routingBreakerConfigIdentity

type routingCostDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var routingCostHTTPDoer routingCostDoer = service.GetRoutingCostHTTPClient()

type routingCostSyncDeps struct {
	now    func() int64
	jitter common.JitterFunc
}

func defaultRoutingCostSyncDeps() routingCostSyncDeps {
	return routingCostSyncDeps{
		now:    common.GetTimestamp,
		jitter: common.FullJitter,
	}
}

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
	service.RegisterSystemTaskHandler(routingCostSyncHandler{})
	service.RegisterSystemTaskHandler(routingAgentHandler{})
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

type routingCostSyncHandler struct{}

func (routingCostSyncHandler) Type() string { return model.SystemTaskTypeRoutingCostSync }

func (routingCostSyncHandler) Enabled() bool {
	return smart_routing_setting.GetSetting().Enabled
}

func (routingCostSyncHandler) Interval() time.Duration {
	minutes := smart_routing_setting.GetSetting().SyncIntervalMin
	if minutes < 1 {
		minutes = 1
	}
	return time.Duration(minutes) * time.Minute
}

func (routingCostSyncHandler) NewPayload() any { return nil }

func (routingCostSyncHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	const operationLease = time.Minute
	nowMs := time.Now().UnixMilli()
	if err := model.ClaimRoutingCostSyncOperationsContext(
		ctx, task.TaskID, runnerID, nowMs, int64(operationLease/time.Millisecond),
	); err != nil {
		message := common.SanitizeErrorMessage(err.Error())
		if message == "" {
			message = "cost sync operation claim failed"
		}
		if _, finishErr := model.FinishRoutingCostSyncTaskContext(
			ctx, task.TaskID, runnerID, model.SystemTaskStatusFailed, nil, message, time.Now().UnixMilli(),
		); finishErr != nil {
			common.SysLog(fmt.Sprintf("system task %s failed to persist claim failure: %s", task.TaskID, common.SanitizeErrorMessage(finishErr.Error())))
		}
		return
	}

	runCtx, cancel := context.WithCancel(ctx)
	stopHeartbeat := make(chan struct{})
	heartbeatResult := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(operationLease / 4)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				heartbeatResult <- nil
				return
			case <-runCtx.Done():
				heartbeatResult <- runCtx.Err()
				return
			case <-ticker.C:
				err := model.RenewRoutingCostSyncOperationsContext(
					runCtx, task.TaskID, runnerID, time.Now().UnixMilli(), int64(operationLease/time.Millisecond),
				)
				if err != nil {
					heartbeatResult <- err
					cancel()
					return
				}
			}
		}
	}()

	summary, runErr := runRoutingCostSyncTask(runCtx)
	close(stopHeartbeat)
	heartbeatErr := <-heartbeatResult
	cancel()
	if runErr == nil && heartbeatErr != nil {
		runErr = heartbeatErr
	}
	executionState := model.RoutingCostSyncExecutionStateFailed
	if runErr == nil {
		executionState, runErr = routingCostSyncExecutionState(summary)
	}
	summary["execution_state"] = executionState
	status := model.SystemTaskStatusSucceeded
	errorMessage := ""
	if runErr != nil {
		status = model.SystemTaskStatusFailed
		errorMessage = common.SanitizeErrorMessage(runErr.Error())
		if errorMessage == "" {
			errorMessage = "cost sync task failed"
		}
	}
	operationCount, finishErr := model.FinishRoutingCostSyncTaskContext(
		ctx, task.TaskID, runnerID, status, summary, errorMessage, time.Now().UnixMilli(),
	)
	if finishErr != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %s", task.TaskID, common.SanitizeErrorMessage(finishErr.Error())))
		return
	}
	publishChannelRoutingControlEvent(channelrouting.RoutingEventTypeCostSyncCompleted, 0, map[string]any{
		"system_task_id": task.TaskID, "status": status, "operation_count": operationCount,
	})
}

func routingCostSyncExecutionState(summary map[string]any) (string, error) {
	bindingCount, _ := summary["bindings"].(int)
	accountCount, _ := summary["accounts"].(int)
	successfulAccounts, _ := summary["successful_accounts"].(int)
	syncErrors, _ := summary["errors"].(int)
	partialAccounts, _ := summary["partial_accounts"].(int)
	staleBindings, _ := summary["stale_bindings"].(int)
	hasAnomaly := syncErrors > 0 || partialAccounts > 0 || staleBindings > 0
	if (accountCount > 0 || bindingCount > 0) && successfulAccounts == 0 && hasAnomaly {
		return model.RoutingCostSyncExecutionStateFailed, errors.New("cost sync failed for all eligible upstream accounts")
	}
	if successfulAccounts > 0 && (successfulAccounts < accountCount || hasAnomaly) {
		return model.RoutingCostSyncExecutionStatePartial, nil
	}
	return model.RoutingCostSyncExecutionStateCompleted, nil
}

type routingPricingResponse struct {
	Success              bool                                   `json:"success"`
	Data                 []routingPricingItem                   `json:"data"`
	GroupRatio           map[string]float64                     `json:"group_ratio"`
	UsableGroup          map[string]string                      `json:"usable_group"`
	PricingVersion       string                                 `json:"pricing_version"`
	ObservedTime         int64                                  `json:"observed_time"`
	EffectiveTime        int64                                  `json:"effective_time"`
	ExpiresTime          int64                                  `json:"expires_time"`
	Message              string                                 `json:"message"`
	AccountGroupErrors   map[string]string                      `json:"-"`
	Sub2APIGroupMeta     map[string]routingSub2APIGroupMetadata `json:"-"`
	QuotaPerUnit         float64                                `json:"-"`
	CatalogAuthenticated bool                                   `json:"-"`
}

type routingNewAPIStatusResponse struct {
	Success bool `json:"success"`
	Data    struct {
		QuotaPerUnit *float64 `json:"quota_per_unit"`
	} `json:"data"`
	Message string `json:"message"`
}

type routingUserSelfResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Quota     *float64 `json:"quota"`
		UsedQuota float64  `json:"used_quota"`
	} `json:"data"`
	Message string `json:"message"`
}

type routingNewAPIUserGroup struct {
	Ratio json.RawMessage `json:"ratio"`
	Desc  string          `json:"desc"`
}

type routingNewAPIUserGroupsResponse struct {
	Success bool                              `json:"success"`
	Data    map[string]routingNewAPIUserGroup `json:"data"`
	Message string                            `json:"message"`
}

type routingNewAPIUserModelsResponse struct {
	Success bool     `json:"success"`
	Data    []string `json:"data"`
	Message string   `json:"message"`
}

type routingNewAPIGatewayModelsResponse struct {
	Success *bool `json:"success"`
	Data    []struct {
		ID string `json:"id"`
	} `json:"data"`
	Message string `json:"message"`
}

type routingPricingItem struct {
	ModelName            string          `json:"model_name"`
	QuotaType            int             `json:"quota_type"`
	ModelRatio           *float64        `json:"model_ratio"`
	ModelPrice           *float64        `json:"model_price"`
	CompletionRatio      *float64        `json:"completion_ratio"`
	CacheRatio           *float64        `json:"cache_ratio"`
	CreateCacheRatio     *float64        `json:"create_cache_ratio"`
	ImageRatio           *float64        `json:"image_ratio"`
	AudioRatio           *float64        `json:"audio_ratio"`
	AudioCompletionRatio *float64        `json:"audio_completion_ratio"`
	PerRequestPrice      *float64        `json:"per_request_price"`
	EnableGroups         []string        `json:"enable_groups"`
	BillingMode          string          `json:"billing_mode"`
	BillingExpr          string          `json:"billing_expr"`
	Tiers                json.RawMessage `json:"tiers"`
	PricingVersion       string          `json:"pricing_version"`
	EffectiveTime        int64           `json:"effective_time"`
	ExpiresTime          int64           `json:"expires_time"`
}

type routingCostAccountIdentity struct {
	AccountKey     string
	StableIdentity string
	MaskedIdentity string
}

type routingCostBindingSource struct {
	Binding                  model.RoutingChannelBinding
	Credentials              model.RoutingCredentials
	ServingModels            map[string]struct{}
	Sub2APIProfile           *routingSub2APIProfileObservation
	Sub2APIAuthFingerprint   [32]byte
	AccountIdentityConfirmed bool
}

type routingCostAccountGroup struct {
	Identity       routingCostAccountIdentity
	Sources        []routingCostBindingSource
	BackoffSources []model.RoutingChannelBinding
}

type routingCostAccountPayload struct {
	SourceType       string
	AccountIdentity  *routingCostAccountIdentity
	ObservedTime     int64
	EffectiveTime    int64
	ExpiresTime      int64
	PricingVersion   string
	BalanceKnown     bool
	Balance          float64
	BalanceUpdatedAt int64
	SyncStatus       string
	SyncError        string
	NewAPI           *routingPricingResponse
	Sub2API          *routingSub2APIAccountPricing
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
		return map[string]any{"costs": 0, "metrics": 0, "breakers": 0, "health": 0}, err
	}
	defer smartRoutingRuntimeStateMu.Unlock()

	summary := map[string]any{
		"costs":    0,
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

	costCutoff := now - staleSeconds
	var costs []model.RoutingCostSnapshot
	if err := model.DB.WithContext(ctx).Where("snapshot_ts >= ?", costCutoff).Order("snapshot_ts desc").Limit(5000).Find(&costs).Error; err != nil {
		return summary, err
	}
	summary["costs"] = len(costs)

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
		query := model.DB.WithContext(ctx).Where("bucket_ts >= ? AND api_key_index = ?", now-metricWindow, model.RoutingMetricSingleKeyIndex)
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
	if err := model.DB.WithContext(ctx).Order("updated_time desc").Limit(5000).Find(&healthStates).Error; err != nil {
		return summary, err
	}
	var trafficBindings []model.RoutingChannelBinding
	if model.DB.Migrator().HasTable(&model.RoutingChannelBinding{}) {
		if err := model.DB.WithContext(ctx).
			Select("channel_id", "serves_claude_code").
			Find(&trafficBindings).Error; err != nil {
			return summary, err
		}
	}
	cachedCostKeys, cachedBalanceChannels := routinghotcache.CostConnectorCachedState()
	cachedCosts := make([]model.RoutingCostSnapshot, 0, len(cachedCostKeys))
	const costReconcileBatchSize = 200
	for start := 0; start < len(cachedCostKeys); start += costReconcileBatchSize {
		end := start + costReconcileBatchSize
		if end > len(cachedCostKeys) {
			end = len(cachedCostKeys)
		}
		conditions := make([]string, 0, end-start)
		args := make([]any, 0, 1+(end-start)*2)
		args = append(args, costCutoff)
		for _, key := range cachedCostKeys[start:end] {
			conditions = append(conditions, "(channel_id = ? AND model_name = ?)")
			args = append(args, key.ChannelID, key.Model)
		}
		var batch []model.RoutingCostSnapshot
		if err := model.DB.WithContext(ctx).
			Where("snapshot_ts >= ? AND ("+strings.Join(conditions, " OR ")+")", args...).
			Find(&batch).Error; err != nil {
			return summary, err
		}
		cachedCosts = append(cachedCosts, batch...)
	}

	cachedHealth := make([]model.RoutingChannelHealthState, 0, len(cachedBalanceChannels))
	const balanceReconcileBatchSize = 500
	for start := 0; start < len(cachedBalanceChannels); start += balanceReconcileBatchSize {
		end := start + balanceReconcileBatchSize
		if end > len(cachedBalanceChannels) {
			end = len(cachedBalanceChannels)
		}
		var batch []model.RoutingChannelHealthState
		if err := model.DB.WithContext(ctx).
			Where("channel_id IN ?", cachedBalanceChannels[start:end]).
			Find(&batch).Error; err != nil {
			return summary, err
		}
		cachedHealth = append(cachedHealth, batch...)
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	routinghotcache.LoadHealthSnapshots(healthStates, now)
	routinghotcache.ReplaceChannelTrafficPolicies(trafficBindings, now)
	routinghotcache.ReconcileCostConnectorSnapshots(routinghotcache.CostConnectorReconcileSnapshot{
		CachedCostKeys:        cachedCostKeys,
		RecentCosts:           costs,
		CachedCosts:           cachedCosts,
		CachedBalanceChannels: cachedBalanceChannels,
		RecentHealth:          healthStates,
		CachedHealth:          cachedHealth,
	})
	summary["health"] = len(healthStates)
	return summary, nil
}

func runRoutingCostSyncTask(ctx context.Context) (map[string]any, error) {
	return runRoutingCostSyncTaskWithDeps(ctx, defaultRoutingCostSyncDeps())
}

func runRoutingCostSyncTaskWithDeps(ctx context.Context, deps routingCostSyncDeps) (map[string]any, error) {
	defaults := defaultRoutingCostSyncDeps()
	if deps.now == nil {
		deps.now = defaults.now
	}
	if deps.jitter == nil {
		deps.jitter = defaults.jitter
	}
	setting := smart_routing_setting.GetSetting()
	syncRoutingBreakerConfigFromSetting(setting)

	summary := map[string]any{
		"bindings":             0,
		"accounts":             0,
		"snapshots":            0,
		"versions_created":     0,
		"metrics":              0,
		"breakers":             0,
		"loaded_breakers":      0,
		"errors":               0,
		"successful_accounts":  0,
		"partial_accounts":     0,
		"skipped_backoff":      0,
		"stale_bindings":       0,
		"credentials_scanned":  0,
		"credentials_rotated":  0,
		"credential_conflicts": 0,
	}

	credentialCursor := 0
	for {
		batch, err := model.ReencryptRoutingChannelBindingCredentialsBatchContext(ctx, credentialCursor, 100)
		if err != nil {
			return summary, err
		}
		summary["credentials_scanned"] = summary["credentials_scanned"].(int) + batch.Scanned
		summary["credentials_rotated"] = summary["credentials_rotated"].(int) + batch.Changed
		summary["credential_conflicts"] = summary["credential_conflicts"].(int) + batch.Conflicts
		if batch.Done {
			break
		}
		if batch.NextID <= credentialCursor {
			return summary, model.ErrRoutingBindingChanged
		}
		credentialCursor = batch.NextID
	}

	flushSummary, err := flushRoutingRuntimeState(ctx, setting)
	if err != nil {
		return summary, err
	}
	summary["metrics"] = flushSummary["metrics"]
	summary["breakers"] = flushSummary["breakers"]

	refreshSummary, err := refreshRoutingHotcacheFromDB(ctx, setting)
	if err != nil {
		return summary, err
	}
	summary["loaded_breakers"] = refreshSummary["breakers"]

	var bindings []model.RoutingChannelBinding
	if err := model.DB.WithContext(ctx).Where("enabled = ?", true).Order("channel_id asc").Find(&bindings).Error; err != nil {
		return summary, err
	}
	now := deps.now()
	eligibleBindings := make([]model.RoutingChannelBinding, 0, len(bindings))
	backoffBindings := make([]model.RoutingChannelBinding, 0, len(bindings))
	skippedBackoff := 0
	for _, binding := range bindings {
		if binding.SyncBackoffUntil > now {
			skippedBackoff++
			backoffBindings = append(backoffBindings, binding)
			continue
		}
		eligibleBindings = append(eligibleBindings, binding)
	}
	summary["bindings"] = len(eligibleBindings)
	summary["skipped_backoff"] = skippedBackoff

	syncedSnapshots := 0
	createdVersions := 0
	syncErrors := 0
	staleBindings := 0
	accountGroups := make(map[string]*routingCostAccountGroup)
	type routingSub2APIProfilePreflight struct {
		profile routingSub2APIProfileObservation
		err     error
	}
	sub2APIProfileByAuth := make(map[[32]byte]routingSub2APIProfilePreflight)
	sub2APIAccountKeyByAuth := make(map[[32]byte]string)
	for _, binding := range eligibleBindings {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		credentials, err := binding.GetCredentials()
		if err != nil {
			_, stale, _, updateErr := recordRoutingCostSyncFailure(ctx, binding, credentials, err, deps, nil, nil)
			if updateErr != nil {
				return summary, updateErr
			}
			if stale {
				staleBindings++
			} else {
				syncErrors++
			}
			continue
		}
		if _, _, err = canonicalRoutingUpstreamBaseURL(binding.BaseURL); err == nil {
			_, err = withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
		}
		var identity routingCostAccountIdentity
		var sub2APIProfile *routingSub2APIProfileObservation
		var sub2APIAuthFingerprint [32]byte
		if err == nil && binding.UpstreamType == model.RoutingUpstreamTypeSub2API {
			sub2APIAuthFingerprint, err = routingSub2APIAccountAuthFingerprint(binding, credentials)
			if err == nil {
				preflight, exists := sub2APIProfileByAuth[sub2APIAuthFingerprint]
				if !exists {
					preflight.profile.Profile, preflight.err = fetchRoutingSub2APIAccountProfile(ctx, binding, credentials)
					if preflight.err == nil {
						preflight.profile.ObservedAt = common.GetTimestamp()
					}
					sub2APIProfileByAuth[sub2APIAuthFingerprint] = preflight
				}
				if preflight.err != nil {
					err = preflight.err
				} else {
					profile := preflight.profile
					sub2APIProfile = &profile
					identity, err = routingSub2APIProfileAccountIdentity(binding, profile.Profile)
				}
			}
		} else if err == nil {
			identity, err = routingUpstreamAccountIdentity(binding, credentials)
		}
		if err != nil {
			_, stale, _, updateErr := recordRoutingCostSyncFailure(ctx, binding, credentials, err, deps, nil, nil)
			if updateErr != nil {
				return summary, updateErr
			}
			if stale {
				staleBindings++
			} else {
				syncErrors++
			}
			continue
		}
		binding, err = model.ApplyRoutingCostAccountKeyContext(ctx, binding, identity.AccountKey)
		if err != nil {
			if errors.Is(err, model.ErrRoutingBindingChanged) {
				staleBindings++
				continue
			}
			return summary, fmt.Errorf("persist routing upstream account association: %w", err)
		}
		group := accountGroups[identity.AccountKey]
		if group == nil {
			group = &routingCostAccountGroup{Identity: identity}
			accountGroups[identity.AccountKey] = group
		}
		group.Sources = append(group.Sources, routingCostBindingSource{
			Binding:                  binding,
			Credentials:              credentials,
			Sub2APIProfile:           sub2APIProfile,
			Sub2APIAuthFingerprint:   sub2APIAuthFingerprint,
			AccountIdentityConfirmed: sub2APIProfile != nil,
		})
		if sub2APIProfile != nil {
			sub2APIAccountKeyByAuth[sub2APIAuthFingerprint] = identity.AccountKey
		}
	}
	backoffAccountKeyByChannel := make(map[int]string, len(backoffBindings))
	if len(backoffBindings) > 0 {
		channelIDs := make([]int, 0, len(backoffBindings))
		for _, binding := range backoffBindings {
			channelIDs = append(channelIDs, binding.ChannelID)
		}
		ambiguousChannels := make(map[int]struct{})
		const accountLookupBatchSize = 500
		for start := 0; start < len(channelIDs); start += accountLookupBatchSize {
			end := min(start+accountLookupBatchSize, len(channelIDs))
			var accountRows []struct {
				ChannelID      int
				AccountKeyHash string
			}
			if err := model.DB.WithContext(ctx).
				Model(&model.RoutingCostSnapshot{}).
				Select("channel_id, account_key_hash").
				Where("channel_id IN ? AND account_key_hash <> ?", channelIDs[start:end], "").
				Find(&accountRows).Error; err != nil {
				return summary, err
			}
			for _, row := range accountRows {
				accountKey := strings.TrimSpace(row.AccountKeyHash)
				if accountKey == "" {
					continue
				}
				if existing, exists := backoffAccountKeyByChannel[row.ChannelID]; exists && existing != accountKey {
					delete(backoffAccountKeyByChannel, row.ChannelID)
					ambiguousChannels[row.ChannelID] = struct{}{}
					continue
				}
				if _, ambiguous := ambiguousChannels[row.ChannelID]; !ambiguous {
					backoffAccountKeyByChannel[row.ChannelID] = accountKey
				}
			}
		}
	}
	for _, binding := range backoffBindings {
		accountKey := strings.TrimSpace(binding.AccountKeyHash)
		if binding.UpstreamType == model.RoutingUpstreamTypeNewAPI {
			identity, identityErr := routingUpstreamAccountIdentity(binding, model.RoutingCredentials{})
			if identityErr == nil {
				accountKey = identity.AccountKey
			}
		} else if accountKey == "" {
			accountKey = backoffAccountKeyByChannel[binding.ChannelID]
			if accountKey == "" {
				credentials, credentialErr := binding.GetCredentials()
				if credentialErr == nil {
					fingerprint, fingerprintErr := routingSub2APIAccountAuthFingerprint(binding, credentials)
					if fingerprintErr == nil {
						accountKey = sub2APIAccountKeyByAuth[fingerprint]
					}
				}
			}
		}
		if group := accountGroups[accountKey]; group != nil {
			group.BackoffSources = append(group.BackoffSources, binding)
		}
	}

	accountKeys := make([]string, 0, len(accountGroups))
	for accountKey := range accountGroups {
		accountKeys = append(accountKeys, accountKey)
	}
	sort.Strings(accountKeys)
	summary["accounts"] = len(accountKeys)
	successfulAccounts := 0
	partialAccounts := 0
	for _, accountKey := range accountKeys {
		group := accountGroups[accountKey]
		accountIdentity := group.Identity
		accountConfirmationFences := make([]model.RoutingChannelBinding, 0, len(group.Sources))
		if routingCostConnectorSourceType(group.Sources[0].Binding.UpstreamType) == model.RoutingUpstreamTypeSub2API {
			for _, source := range group.Sources {
				if source.AccountIdentityConfirmed {
					accountConfirmationFences = append(accountConfirmationFences, source.Binding)
				}
			}
		}
		accountIdentityConfirmed := len(accountConfirmationFences) > 0
		type routingCostSourceFailure struct {
			source routingCostBindingSource
			err    error
		}
		type routingCostSyncBatch struct {
			sources                 []routingCostBindingSource
			preloadedGroups         *routingNewAPIUserGroupsResponse
			preloadedSub2APIProfile *routingSub2APIProfileObservation
		}
		failures := make([]routingCostSourceFailure, 0)
		batches := make([]routingCostSyncBatch, 0, len(group.Sources))

		if routingCostConnectorSourceType(group.Sources[0].Binding.UpstreamType) == model.RoutingUpstreamTypeNewAPI {
			type authPreflightResult struct {
				groups routingNewAPIUserGroupsResponse
				err    error
			}
			type servingPreflightResult struct {
				models map[string]struct{}
				err    error
			}
			preflightByCredential := make(map[[32]byte]authPreflightResult, len(group.Sources))
			servingPreflightByCredential := make(map[[32]byte]servingPreflightResult, len(group.Sources))
			batchIndexByCredential := make(map[[32]byte]int, len(group.Sources))
			for _, source := range group.Sources {
				credentials := source.Credentials.ForUpstream(source.Binding.UpstreamType)
				egressPolicy := ""
				if source.Binding.EgressPolicyJSON != nil {
					egressPolicy = *source.Binding.EgressPolicyJSON
				}
				fingerprint := sha256.Sum256([]byte(strings.Join([]string{
					strings.TrimSpace(source.Binding.BaseURL),
					credentials.NewAPIAccessToken,
					credentials.CustomCAPEM,
					egressPolicy,
				}, "\x00")))
				preflight, exists := preflightByCredential[fingerprint]
				if !exists {
					if credentials.NewAPIAccessToken == "" {
						preflight.err = routingAuthErrorf("newapi access token is required for management endpoints")
					} else {
						preflight.groups, preflight.err = fetchRoutingNewAPIUserGroups(
							ctx, source.Binding, source.Credentials,
						)
					}
					preflightByCredential[fingerprint] = preflight
				}
				if preflight.err != nil {
					failures = append(failures, routingCostSourceFailure{source: source, err: preflight.err})
					continue
				}
				source.AccountIdentityConfirmed = true
				accountConfirmationFences = append(accountConfirmationFences, source.Binding)
				accountIdentityConfirmed = true
				servingFingerprint := sha256.Sum256([]byte(strings.Join([]string{
					strings.TrimSpace(source.Binding.BaseURL),
					credentials.GatewayAPIKey,
					credentials.CustomCAPEM,
					egressPolicy,
				}, "\x00")))
				servingPreflight, exists := servingPreflightByCredential[servingFingerprint]
				if !exists {
					servingPreflight.models, servingPreflight.err = fetchRoutingNewAPIGatewayModels(
						ctx,
						source.Binding,
						source.Credentials,
					)
					servingPreflightByCredential[servingFingerprint] = servingPreflight
				}
				if servingPreflight.err != nil {
					failures = append(failures, routingCostSourceFailure{source: source, err: servingPreflight.err})
					continue
				}
				source.ServingModels = servingPreflight.models
				if batchIndex, exists := batchIndexByCredential[fingerprint]; exists {
					batches[batchIndex].sources = append(batches[batchIndex].sources, source)
				} else {
					groups := preflight.groups
					batchIndexByCredential[fingerprint] = len(batches)
					batches = append(batches, routingCostSyncBatch{
						sources:         []routingCostBindingSource{source},
						preloadedGroups: &groups,
					})
				}
			}
		} else {
			type sub2APIBatchKey struct {
				authFingerprint [32]byte
				upstreamGroup   string
			}
			batchIndexByCredential := make(map[sub2APIBatchKey]int, len(group.Sources))
			for _, source := range group.Sources {
				batchKey := sub2APIBatchKey{
					authFingerprint: source.Sub2APIAuthFingerprint,
					upstreamGroup:   strings.TrimSpace(source.Binding.UpstreamGroup),
				}
				if batchIndex, exists := batchIndexByCredential[batchKey]; exists {
					batches[batchIndex].sources = append(batches[batchIndex].sources, source)
					continue
				}
				batchIndexByCredential[batchKey] = len(batches)
				batches = append(batches, routingCostSyncBatch{
					sources:                 []routingCostBindingSource{source},
					preloadedSub2APIProfile: source.Sub2APIProfile,
				})
			}
		}

		type mappedRoutingCostSource struct {
			source  routingCostBindingSource
			payload routingCostAccountPayload
			writes  []model.RoutingCostSnapshotVersionWrite
		}
		mappedSources := make([]mappedRoutingCostSource, 0, len(group.Sources))
		payloadWasSuccessful := false
		payloadWasPartial := false
		payloadSyncError := ""
		for _, batch := range batches {
			representative := batch.sources[0]
			requestedGroups := make([]string, 0, len(batch.sources))
			seenGroups := make(map[string]struct{}, len(batch.sources))
			for _, source := range batch.sources {
				upstreamGroup := strings.TrimSpace(source.Binding.UpstreamGroup)
				if upstreamGroup == "" {
					continue
				}
				if _, exists := seenGroups[upstreamGroup]; exists {
					continue
				}
				seenGroups[upstreamGroup] = struct{}{}
				requestedGroups = append(requestedGroups, upstreamGroup)
			}
			sort.Strings(requestedGroups)
			payload, err := fetchRoutingCostAccountPayloadWithSub2APIProfile(
				ctx, representative.Binding, representative.Credentials, setting,
				batch.preloadedGroups, batch.preloadedSub2APIProfile, requestedGroups...,
			)
			if err == nil && payload.AccountIdentity != nil &&
				payload.AccountIdentity.AccountKey != group.Identity.AccountKey {
				err = errors.New("sub2api authenticated account changed during cost sync")
			}
			if err != nil {
				if ctx.Err() != nil {
					return summary, ctx.Err()
				}
				for _, source := range batch.sources {
					failures = append(failures, routingCostSourceFailure{source: source, err: err})
				}
				continue
			}
			payloadWasSuccessful = true
			if payload.AccountIdentity != nil {
				accountIdentity = *payload.AccountIdentity
			}
			if payload.SyncStatus == model.RoutingUpstreamSyncStatusPartial {
				payloadWasPartial = true
				if payloadSyncError == "" {
					payloadSyncError = common.SanitizeErrorMessage(payload.SyncError, routingCredentialSecrets(representative.Credentials)...)
				}
			}
			for _, source := range batch.sources {
				writes, mapErr := routingCostVersionWritesForBinding(ctx, source.Binding, payload)
				if mapErr == nil && source.Binding.UpstreamType == model.RoutingUpstreamTypeNewAPI {
					writes, mapErr = filterRoutingNewAPIWritesByServingModels(writes, source.ServingModels)
				}
				if mapErr != nil {
					failures = append(failures, routingCostSourceFailure{source: source, err: mapErr})
					continue
				}
				mappedSources = append(mappedSources, mappedRoutingCostSource{
					source:  source,
					payload: payload,
					writes:  writes,
				})
			}
		}

		type appliedRoutingCostFailure struct {
			fence   model.RoutingChannelBinding
			message string
		}
		appliedFailures := make([]appliedRoutingCostFailure, 0, len(failures)+len(group.BackoffSources))
		for _, binding := range group.BackoffSources {
			message := "routing cost sync remains in backoff"
			if binding.LastSyncError != nil {
				if sanitized := common.SanitizeErrorMessage(*binding.LastSyncError); sanitized != "" {
					message = sanitized
				}
			}
			appliedFailures = append(appliedFailures, appliedRoutingCostFailure{
				fence:   binding,
				message: message,
			})
		}
		failureSyncStatus := model.RoutingUpstreamSyncStatusFailed
		if payloadWasSuccessful {
			failureSyncStatus = model.RoutingUpstreamSyncStatusPartial
		}
		failureAccountStatusApplied := false
		for _, failure := range failures {
			message := common.SanitizeErrorMessage(
				failure.err.Error(),
				routingCredentialSecrets(failure.source.Credentials)...,
			)
			if message == "" {
				message = "routing cost sync failed"
			}
			var failureAccountSpec *model.RoutingUpstreamAccountSpec
			failureConfirmationFences := accountConfirmationFences
			if accountIdentityConfirmed {
				if failure.source.AccountIdentityConfirmed {
					failureConfirmationFences = []model.RoutingChannelBinding{failure.source.Binding}
				}
				spec := model.RoutingUpstreamAccountSpec{
					SourceType:      routingCostConnectorSourceType(failure.source.Binding.UpstreamType),
					StableIdentity:  accountIdentity.StableIdentity,
					MaskedIdentity:  accountIdentity.MaskedIdentity,
					Status:          model.RoutingUpstreamAccountStatusDegraded,
					PreserveBalance: true,
					LastSyncStatus:  failureSyncStatus,
					LastSyncError:   message,
				}
				failureAccountSpec = &spec
			}
			updatedBinding, stale, accountStatusApplied, updateErr := recordRoutingCostSyncFailure(
				ctx,
				failure.source.Binding,
				failure.source.Credentials,
				failure.err,
				deps,
				failureConfirmationFences,
				failureAccountSpec,
			)
			if updateErr != nil {
				return summary, updateErr
			}
			if stale {
				staleBindings++
			} else {
				if failure.source.AccountIdentityConfirmed {
					for index := range accountConfirmationFences {
						if accountConfirmationFences[index].ID == failure.source.Binding.ID {
							accountConfirmationFences[index] = updatedBinding
							break
						}
					}
				}
				failureAccountStatusApplied = failureAccountStatusApplied || accountStatusApplied
				syncErrors++
				appliedFailures = append(appliedFailures, appliedRoutingCostFailure{
					fence:   updatedBinding,
					message: message,
				})
			}
		}

		accountSucceeded := false
		accountWasPartial := payloadWasPartial || failureAccountStatusApplied || len(group.BackoffSources) > 0
		for _, mapped := range mappedSources {
			source := mapped.source
			payload := mapped.payload
			channelBalanceNotApplicable := false
			if source.Binding.UpstreamType == model.RoutingUpstreamTypeSub2API && payload.Sub2API != nil {
				group, found := payload.Sub2API.Groups[strings.TrimSpace(source.Binding.UpstreamGroup)]
				channelBalanceNotApplicable = found && routingSub2APIGroupUsesSubscription(group)
			}

			maxAttempts := len(appliedFailures) + 1
			for attempts := 0; attempts <= maxAttempts; attempts++ {
				failureFences := make([]model.RoutingChannelBinding, 0, len(appliedFailures))
				failureSyncError := payloadSyncError
				if accountIdentityConfirmed {
					for _, failure := range appliedFailures {
						failureFences = append(failureFences, failure.fence)
						if failureSyncError == "" {
							failureSyncError = failure.message
						}
					}
				}
				accountPartial := payloadWasPartial || len(failureFences) > 0
				accountStatus := model.RoutingUpstreamAccountStatusActive
				accountSyncStatus := model.RoutingUpstreamSyncStatusSuccess
				if accountPartial {
					accountStatus = model.RoutingUpstreamAccountStatusDegraded
					accountSyncStatus = model.RoutingUpstreamSyncStatusPartial
				}
				accountSpec := model.RoutingUpstreamAccountSpec{
					SourceType:                  routingCostConnectorSourceType(source.Binding.UpstreamType),
					StableIdentity:              accountIdentity.StableIdentity,
					MaskedIdentity:              accountIdentity.MaskedIdentity,
					Status:                      accountStatus,
					PreserveBalance:             !payload.BalanceKnown,
					BalanceKnown:                payload.BalanceKnown,
					Balance:                     payload.Balance,
					BalanceUpdatedAt:            payload.BalanceUpdatedAt,
					ChannelBalanceNotApplicable: channelBalanceNotApplicable,
					LastSyncStatus:              accountSyncStatus,
					LastSyncError:               failureSyncError,
				}

				if err := smartRoutingRuntimeStateMu.LockContext(ctx); err != nil {
					return summary, err
				}
				persisted, persistErr := model.CompleteRoutingCostVersionSyncWithAccountFencesContext(
					ctx,
					source.Binding,
					failureFences,
					accountSpec,
					mapped.writes,
				)
				if persistErr == nil {
					routinghotcache.ReplaceCostSnapshotsForChannel(source.Binding.ChannelID, persisted.Latest)
					if accountSpec.ChannelBalanceNotApplicable {
						routinghotcache.ClearBalance(source.Binding.ChannelID)
					} else if payload.BalanceKnown {
						routinghotcache.SetBalance(source.Binding.ChannelID, routinghotcache.BalanceSnapshot{
							Known:       true,
							Balance:     payload.Balance,
							UpdatedUnix: payload.BalanceUpdatedAt,
						})
					}
				}
				smartRoutingRuntimeStateMu.Unlock()
				if persistErr == nil {
					syncedSnapshots += len(mapped.writes)
					accountSucceeded = true
					accountWasPartial = accountPartial
					for _, version := range persisted.Versions {
						if version.Created {
							createdVersions++
						}
					}
					break
				}
				if !errors.Is(persistErr, model.ErrRoutingBindingChanged) {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return summary, ctxErr
					}
					return summary, fmt.Errorf("persist routing cost versions: %w", persistErr)
				}

				sourceMatches, matchErr := model.RoutingChannelBindingMatchesSyncContext(ctx, source.Binding)
				if matchErr != nil {
					return summary, matchErr
				}
				if !sourceMatches {
					staleBindings++
					break
				}
				currentFailures := make([]appliedRoutingCostFailure, 0, len(appliedFailures))
				for _, failure := range appliedFailures {
					matches, failureMatchErr := model.RoutingChannelBindingMatchesSyncContext(ctx, failure.fence)
					if failureMatchErr != nil {
						return summary, failureMatchErr
					}
					if matches {
						currentFailures = append(currentFailures, failure)
					}
				}
				if len(currentFailures) == len(appliedFailures) {
					staleBindings++
					break
				}
				staleBindings += len(appliedFailures) - len(currentFailures)
				appliedFailures = currentFailures
			}
		}
		if accountWasPartial {
			partialAccounts++
		}
		if accountSucceeded {
			successfulAccounts++
		}
	}
	summary["snapshots"] = syncedSnapshots
	summary["versions_created"] = createdVersions
	summary["errors"] = syncErrors
	summary["successful_accounts"] = successfulAccounts
	summary["stale_bindings"] = staleBindings
	summary["partial_accounts"] = partialAccounts
	return summary, nil
}

func recordRoutingCostSyncFailure(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	syncErr error,
	deps routingCostSyncDeps,
	accountFences []model.RoutingChannelBinding,
	accountSpec *model.RoutingUpstreamAccountSpec,
) (model.RoutingChannelBinding, bool, bool, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return model.RoutingChannelBinding{}, false, false, ctxErr
	}
	failureCount := binding.SyncFailureCount
	if failureCount < 0 {
		failureCount = 0
	}
	maxInt := int(^uint(0) >> 1)
	if failureCount < maxInt {
		failureCount++
	}
	delay := common.CappedExponentialBackoff(failureCount, time.Minute, time.Hour, deps.jitter)
	delaySeconds := int64(delay / time.Second)
	if delay%time.Second != 0 {
		delaySeconds++
	}
	if delaySeconds <= 0 {
		delaySeconds = 1
	}
	failureObservedAt := deps.now()
	backoffUntil := failureObservedAt
	maxInt64 := int64(^uint64(0) >> 1)
	if failureObservedAt > maxInt64-delaySeconds {
		backoffUntil = maxInt64
	} else {
		backoffUntil += delaySeconds
	}
	message := "routing cost sync failed"
	if syncErr != nil {
		message = common.SanitizeErrorMessage(syncErr.Error(), routingCredentialSecrets(credentials)...)
		if message == "" {
			message = "routing cost sync failed"
		}
	}
	var updated model.RoutingChannelBinding
	var err error
	accountStatusApplied := false
	if accountSpec == nil {
		updated, err = model.ApplyRoutingCostSyncFailureContext(ctx, binding, failureCount, backoffUntil, message)
	} else {
		for _, confirmationFence := range accountFences {
			updated, err = model.ApplyRoutingCostSyncFailureWithAccountFencesContext(
				ctx,
				binding,
				failureCount,
				backoffUntil,
				message,
				[]model.RoutingChannelBinding{confirmationFence},
				*accountSpec,
			)
			if err == nil {
				accountStatusApplied = true
				break
			}
			if !errors.Is(err, model.ErrRoutingBindingChanged) {
				break
			}
			bindingMatches, matchErr := model.RoutingChannelBindingMatchesSyncContext(ctx, binding)
			if matchErr != nil {
				return model.RoutingChannelBinding{}, false, false, matchErr
			}
			if !bindingMatches {
				return model.RoutingChannelBinding{}, true, false, nil
			}
			confirmationMatches, fenceErr := model.RoutingChannelBindingMatchesSyncContext(ctx, confirmationFence)
			if fenceErr != nil {
				return model.RoutingChannelBinding{}, false, false, fenceErr
			}
			if confirmationMatches {
				break
			}
		}
		if !accountStatusApplied && (err == nil || errors.Is(err, model.ErrRoutingBindingChanged)) {
			updated, err = model.ApplyRoutingCostSyncFailureContext(
				ctx, binding, failureCount, backoffUntil, message,
			)
		}
	}
	if err != nil {
		if errors.Is(err, model.ErrRoutingBindingChanged) {
			return model.RoutingChannelBinding{}, true, false, nil
		}
		return model.RoutingChannelBinding{}, false, false, fmt.Errorf("persist routing cost sync failure state: %w", err)
	}
	return updated, false, accountStatusApplied, nil
}

func routingUpstreamAccountIdentity(binding model.RoutingChannelBinding, credentials model.RoutingCredentials) (routingCostAccountIdentity, error) {
	sourceType := routingCostConnectorSourceType(binding.UpstreamType)
	identitySubject := ""
	maskedSubject := ""
	switch sourceType {
	case model.RoutingUpstreamTypeNewAPI:
		if binding.NewAPIUserID == nil || *binding.NewAPIUserID <= 0 {
			return routingCostAccountIdentity{}, errors.New("newapi upstream account identity requires a valid user ID")
		}
		identitySubject = fmt.Sprintf("user:%d", *binding.NewAPIUserID)
		maskedSubject = fmt.Sprintf("user %d", *binding.NewAPIUserID)
	case model.RoutingUpstreamTypeSub2API:
		return routingCostAccountIdentity{}, errors.New("sub2api upstream account identity requires an authenticated user profile")
	}
	return routingUpstreamAccountIdentityForSubject(binding, sourceType, identitySubject, maskedSubject)
}

func routingSub2APIProfileAccountIdentity(
	binding model.RoutingChannelBinding,
	profile routingSub2APIUserProfile,
) (routingCostAccountIdentity, error) {
	if profile.ID <= 0 {
		return routingCostAccountIdentity{}, errors.New("sub2api account profile does not contain a stable user ID")
	}
	maskedSubject := fmt.Sprintf("user %d", profile.ID)
	return routingUpstreamAccountIdentityForSubject(
		binding,
		model.RoutingUpstreamTypeSub2API,
		fmt.Sprintf("user:%d", profile.ID),
		maskedSubject,
	)
}

func routingUpstreamAccountIdentityForSubject(
	binding model.RoutingChannelBinding,
	sourceType string,
	identitySubject string,
	maskedSubject string,
) (routingCostAccountIdentity, error) {
	canonicalBase, hostLabel, err := canonicalRoutingUpstreamBaseURL(binding.BaseURL)
	if err != nil {
		return routingCostAccountIdentity{}, err
	}

	identitySubject = strings.TrimSpace(identitySubject)
	maskedSubject = strings.TrimSpace(maskedSubject)
	if identitySubject == "" || maskedSubject == "" {
		return routingCostAccountIdentity{}, errors.New("invalid routing upstream account identity")
	}
	identityVersion := "routing-upstream-identity:v1"
	if sourceType == model.RoutingUpstreamTypeSub2API {
		identityVersion = "routing-upstream-identity:sub2api:v2"
	}
	identityParts := []string{identityVersion, sourceType, canonicalBase, identitySubject}
	maskedIdentity := hostLabel + " / " + maskedSubject
	identityHash := sha256.Sum256([]byte(strings.Join(identityParts, "\x00")))
	stableIdentity := fmt.Sprintf("%x", identityHash[:])
	return routingCostAccountIdentity{
		AccountKey:     model.RoutingUpstreamAccountKey(sourceType, stableIdentity),
		StableIdentity: stableIdentity,
		MaskedIdentity: maskedIdentity,
	}, nil
}

func canonicalRoutingUpstreamBaseURL(value string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" ||
		!strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", "", errors.New("invalid routing upstream account base URL")
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" {
		return "", "", errors.New("invalid routing upstream account base URL")
	}
	port := parsed.Port()
	if port == "443" {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		if port == "" {
			host = "[" + hostname + "]"
		} else {
			host = net.JoinHostPort(hostname, port)
		}
	} else if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	parsed.Scheme = "https"
	parsed.Host = host
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	canonicalBase := parsed.String()
	hostLabel := parsed.Host + parsed.EscapedPath()
	if len(hostLabel) > 192 {
		hostLabel = hostLabel[:192]
	}
	return canonicalBase, hostLabel, nil
}

func routingCostConnectorSourceType(upstreamType string) string {
	if upstreamType == model.RoutingUpstreamTypeSub2API {
		return model.RoutingUpstreamTypeSub2API
	}
	return model.RoutingUpstreamTypeNewAPI
}

func routingSub2APIAccountAuthFingerprint(
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
) ([32]byte, error) {
	canonicalBase, _, err := canonicalRoutingUpstreamBaseURL(binding.BaseURL)
	if err != nil {
		return [32]byte{}, err
	}
	credentials = credentials.ForUpstream(model.RoutingUpstreamTypeSub2API)
	authMode := "managed"
	authIdentity := strings.Join([]string{credentials.Sub2APIEmail, credentials.Sub2APIPassword}, "\x00")
	if credentials.Sub2APIToken != "" {
		authMode = "token"
		authIdentity = credentials.Sub2APIToken
	}
	egressPolicy := ""
	if binding.EgressPolicyJSON != nil {
		egressPolicy = *binding.EgressPolicyJSON
	}
	return sha256.Sum256([]byte(strings.Join([]string{
		canonicalBase,
		authMode,
		authIdentity,
		credentials.CustomCAPEM,
		egressPolicy,
	}, "\x00"))), nil
}

func fetchRoutingCostAccountPayload(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	setting smart_routing_setting.SmartRoutingSetting,
	preloadedNewAPIGroups *routingNewAPIUserGroupsResponse,
	requestedGroups ...string,
) (routingCostAccountPayload, error) {
	return fetchRoutingCostAccountPayloadWithSub2APIProfile(
		ctx,
		binding,
		credentials,
		setting,
		preloadedNewAPIGroups,
		nil,
		requestedGroups...,
	)
}

func fetchRoutingCostAccountPayloadWithSub2APIProfile(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	setting smart_routing_setting.SmartRoutingSetting,
	preloadedNewAPIGroups *routingNewAPIUserGroupsResponse,
	preloadedSub2APIProfile *routingSub2APIProfileObservation,
	requestedGroups ...string,
) (routingCostAccountPayload, error) {
	credentials = credentials.ForUpstream(binding.UpstreamType)
	switch routingCostConnectorSourceType(binding.UpstreamType) {
	case model.RoutingUpstreamTypeNewAPI:
		if binding.NewAPIUserID == nil || *binding.NewAPIUserID <= 0 {
			return routingCostAccountPayload{}, routingAuthErrorf("newapi user ID is required for management endpoints")
		}
		if credentials.NewAPIAccessToken == "" {
			return routingCostAccountPayload{}, routingAuthErrorf("newapi access token is required for management endpoints")
		}
	case model.RoutingUpstreamTypeSub2API:
		if !credentials.ReadyForUpstream(model.RoutingUpstreamTypeSub2API) {
			return routingCostAccountPayload{}, routingAuthErrorf("sub2api JWT or email and password are required for management endpoints")
		}
	}
	observedTime := common.GetTimestamp()
	intervalMinutes := setting.SyncIntervalMin
	if intervalMinutes < 1 {
		intervalMinutes = 1
	}
	expiresTime := observedTime + int64(2*time.Duration(intervalMinutes)*time.Minute/time.Second)
	payload := routingCostAccountPayload{
		SourceType:     routingCostConnectorSourceType(binding.UpstreamType),
		ObservedTime:   observedTime,
		EffectiveTime:  observedTime,
		ExpiresTime:    expiresTime,
		SyncStatus:     model.RoutingUpstreamSyncStatusSuccess,
		PricingVersion: "",
	}

	switch payload.SourceType {
	case model.RoutingUpstreamTypeNewAPI:
		if len(requestedGroups) == 0 && strings.TrimSpace(binding.UpstreamGroup) != "" {
			requestedGroups = []string{strings.TrimSpace(binding.UpstreamGroup)}
		}
		upstreamQuotaPerUnit, err := fetchRoutingNewAPIQuotaPerUnit(ctx, binding, credentials)
		if err != nil {
			return routingCostAccountPayload{}, err
		}
		balance, balanceKnown, balanceErr := fetchRoutingNewAPIBalanceValue(
			ctx,
			binding,
			credentials,
			upstreamQuotaPerUnit,
		)
		if balanceErr != nil && routingUpstreamAuthError(balanceErr) {
			return routingCostAccountPayload{}, balanceErr
		}
		pricing, err := fetchRoutingNewAPIAccountPricingPayloadWithQuotaPerUnit(
			ctx,
			binding,
			credentials,
			requestedGroups,
			upstreamQuotaPerUnit,
			preloadedNewAPIGroups,
		)
		if err != nil {
			return routingCostAccountPayload{}, err
		}
		payload.NewAPI = &pricing
		payload.BalanceKnown = balanceKnown
		payload.Balance = balance
		if balanceKnown {
			payload.BalanceUpdatedAt = observedTime
		}
		if balanceErr != nil {
			payload.SyncStatus = model.RoutingUpstreamSyncStatusPartial
			payload.SyncError = common.SanitizeErrorMessage(balanceErr.Error(), routingCredentialSecrets(credentials)...)
		}
		if pricing.ObservedTime > 0 {
			payload.ObservedTime = pricing.ObservedTime
		}
		if pricing.EffectiveTime > 0 {
			payload.EffectiveTime = pricing.EffectiveTime
		} else {
			payload.EffectiveTime = payload.ObservedTime
		}
		if pricing.ExpiresTime > 0 {
			payload.ExpiresTime = pricing.ExpiresTime
		} else {
			payload.ExpiresTime = payload.ObservedTime + int64(2*time.Duration(intervalMinutes)*time.Minute/time.Second)
		}
		payload.PricingVersion = strings.TrimSpace(pricing.PricingVersion)
		if payload.PricingVersion == "" {
			payload.PricingVersion = routingCostContentVersion("newapi", pricing)
		}
	case model.RoutingUpstreamTypeSub2API:
		pricing, err := fetchRoutingSub2APIAccountPricingForGroupsWithProfile(
			ctx,
			binding,
			credentials,
			preloadedSub2APIProfile,
			requestedGroups,
		)
		if err != nil {
			return routingCostAccountPayload{}, err
		}
		payload.Sub2API = &pricing
		accountIdentity, err := routingSub2APIProfileAccountIdentity(binding, pricing.Profile)
		if err != nil {
			return routingCostAccountPayload{}, err
		}
		payload.AccountIdentity = &accountIdentity
		payload.BalanceKnown = pricing.BalanceKnown
		payload.Balance = pricing.Balance
		payload.BalanceUpdatedAt = pricing.BalanceUpdatedAt
		payload.SyncStatus = pricing.SyncStatus
		payload.SyncError = pricing.SyncError
		payload.PricingVersion = routingCostContentVersion("sub2api", pricing.VersionMaterial())
	}
	return payload, nil
}

func routingCostContentVersion(prefix string, value any) string {
	encoded, err := common.Marshal(value)
	if err != nil {
		return prefix + ":unknown"
	}
	hash := sha256.Sum256(encoded)
	return fmt.Sprintf("%s:%x", prefix, hash[:])
}

func routingCostVersionWritesForBinding(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	payload routingCostAccountPayload,
) ([]model.RoutingCostSnapshotVersionWrite, error) {
	modelNameMap, err := routingModelReverseMapping(ctx, binding.ChannelID)
	if err != nil {
		return nil, err
	}
	switch routingCostConnectorSourceType(binding.UpstreamType) {
	case model.RoutingUpstreamTypeNewAPI:
		if payload.NewAPI == nil {
			return nil, errors.New("missing newapi pricing payload")
		}
		return routingNewAPICostVersionWrites(binding, modelNameMap, payload)
	case model.RoutingUpstreamTypeSub2API:
		if payload.Sub2API == nil {
			return nil, errors.New("missing sub2api pricing payload")
		}
		return routingSub2APICostVersionWrites(binding, modelNameMap, payload)
	}
	return nil, errors.New("unsupported routing upstream account type")
}

func routingNewAPICostVersionWrites(
	binding model.RoutingChannelBinding,
	modelNameMap map[string]string,
	payload routingCostAccountPayload,
) ([]model.RoutingCostSnapshotVersionWrite, error) {
	pricingPayload := payload.NewAPI
	if message := strings.TrimSpace(pricingPayload.AccountGroupErrors[binding.UpstreamGroup]); message != "" {
		return nil, errors.New(message)
	}
	groupRatio, groupRatioKnown := pricingPayload.GroupRatio[binding.UpstreamGroup]
	if !groupRatioKnown {
		return nil, errors.New("newapi bound group is not available to the account")
	}
	if !routingCostNonNegativeFinite(groupRatio) {
		return nil, errors.New("newapi returned an invalid group ratio")
	}
	items := append([]routingPricingItem(nil), pricingPayload.Data...)
	sort.SliceStable(items, func(left int, right int) bool {
		return items[left].ModelName < items[right].ModelName
	})
	writes := make([]model.RoutingCostSnapshotVersionWrite, 0, len(items))
	seenModels := make(map[string]struct{})
	for _, item := range items {
		upstreamModel := strings.TrimSpace(item.ModelName)
		if upstreamModel == "" || !routingPricingItemServesGroup(item.EnableGroups, binding.UpstreamGroup) {
			continue
		}
		localModel := upstreamModel
		if mapped, ok := modelNameMap[upstreamModel]; ok {
			localModel = mapped
		}
		if _, duplicate := seenModels[localModel]; duplicate {
			return nil, fmt.Errorf("newapi returned duplicate pricing for local model %s", localModel)
		}
		seenModels[localModel] = struct{}{}

		pricing, confidence, confidenceScore, err := routingNewAPINormalizedPricing(
			item,
			groupRatio,
			true,
			pricingPayload.QuotaPerUnit,
		)
		if err != nil {
			return nil, fmt.Errorf("invalid newapi price for model %s: %w", upstreamModel, err)
		}
		if confidence == model.RoutingCostConfidenceUnknown {
			return nil, fmt.Errorf("newapi returned model %s without usable pricing", upstreamModel)
		}
		effectiveTime := payload.EffectiveTime
		if item.EffectiveTime > 0 {
			effectiveTime = item.EffectiveTime
		}
		expiresTime := payload.ExpiresTime
		if item.ExpiresTime > 0 {
			expiresTime = item.ExpiresTime
		}
		pricingVersion := strings.TrimSpace(item.PricingVersion)
		if pricingVersion == "" {
			pricingVersion = payload.PricingVersion
		}
		if payload.SyncStatus == model.RoutingUpstreamSyncStatusPartial && confidenceScore > 0.8 {
			confidenceScore = 0.8
		}
		writes = append(writes, model.RoutingCostSnapshotVersionWrite{
			ChannelID:        binding.ChannelID,
			UpstreamGroup:    binding.UpstreamGroup,
			UpstreamModel:    upstreamModel,
			LocalModel:       localModel,
			ObservedTime:     payload.ObservedTime,
			EffectiveTime:    effectiveTime,
			ExpiresTime:      expiresTime,
			PricingVersion:   pricingVersion,
			Confidence:       confidence,
			ConfidenceScore:  confidenceScore,
			Freshness:        model.RoutingCostFreshnessFresh,
			FreshnessScore:   1,
			SourceSyncStatus: payload.SyncStatus,
			SourceSyncError:  payload.SyncError,
			Pricing:          pricing,
		})
	}
	if len(writes) == 0 {
		return nil, errors.New("newapi returned no priced models for the bound group")
	}
	return writes, nil
}

func filterRoutingNewAPIWritesByServingModels(
	writes []model.RoutingCostSnapshotVersionWrite,
	servingModels map[string]struct{},
) ([]model.RoutingCostSnapshotVersionWrite, error) {
	if len(servingModels) == 0 {
		return nil, errors.New("newapi serving model contract is unavailable")
	}
	filtered := make([]model.RoutingCostSnapshotVersionWrite, 0, len(writes))
	for _, write := range writes {
		if _, available := servingModels[strings.TrimSpace(write.UpstreamModel)]; available {
			filtered = append(filtered, write)
		}
	}
	if len(filtered) == 0 {
		return nil, errors.New("newapi gateway API key exposes none of the priced account models for the bound group")
	}
	return filtered, nil
}

func routingNewAPINormalizedPricing(
	item routingPricingItem,
	groupRatio float64,
	groupRatioKnown bool,
	upstreamQuotaPerUnit float64,
) (model.RoutingNormalizedPricing, string, float64, error) {
	if !routingCostNonNegativeFinite(groupRatio) ||
		!routingCostNonNegativeFinite(upstreamQuotaPerUnit) || upstreamQuotaPerUnit <= 0 ||
		!routingCostNonNegativeFinite(common.QuotaPerUnit) || common.QuotaPerUnit <= 0 ||
		item.QuotaType < 0 || item.QuotaType > 1 {
		return model.RoutingNormalizedPricing{}, "", 0, model.ErrRoutingCostV2Invalid
	}
	for _, value := range []*float64{
		item.ModelRatio,
		item.ModelPrice,
		item.CompletionRatio,
		item.CacheRatio,
		item.CreateCacheRatio,
		item.ImageRatio,
		item.AudioRatio,
		item.AudioCompletionRatio,
		item.PerRequestPrice,
	} {
		if value != nil && !routingCostNonNegativeFinite(*value) {
			return model.RoutingNormalizedPricing{}, "", 0, model.ErrRoutingCostV2Invalid
		}
	}
	billingMode := strings.ToLower(strings.TrimSpace(item.BillingMode))
	if billingMode == "" {
		if item.QuotaType == 1 {
			billingMode = "per_request"
		} else {
			billingMode = "token"
		}
	}
	billingExpression := strings.TrimSpace(item.BillingExpr)
	if billingMode == "token" && billingExpression == "" && item.CompletionRatio == nil {
		return model.RoutingNormalizedPricing{}, "", 0, model.ErrRoutingCostV2Invalid
	}
	completionRatio := 1.0
	if item.CompletionRatio != nil {
		completionRatio = *item.CompletionRatio
	}
	perRequestCost := item.ModelPrice
	if item.PerRequestPrice != nil {
		perRequestCost = item.PerRequestPrice
	}
	modelRatio := 0.0
	if item.ModelRatio != nil {
		modelRatio = *item.ModelRatio
	}
	baseRatio := modelRatio * common.QuotaPerUnit / upstreamQuotaPerUnit
	inputCostPerMillion := modelRatio * 1_000_000 / upstreamQuotaPerUnit
	outputCostPerMillion := inputCostPerMillion * completionRatio
	if item.ModelRatio != nil && (!routingCostNonNegativeFinite(baseRatio) ||
		!routingCostNonNegativeFinite(inputCostPerMillion) ||
		!routingCostNonNegativeFinite(outputCostPerMillion)) {
		return model.RoutingNormalizedPricing{}, "", 0, model.ErrRoutingCostV2Invalid
	}
	pricing := model.RoutingNormalizedPricing{
		QuotaType:         item.QuotaType,
		BillingMode:       billingMode,
		Currency:          "USD",
		GroupRatio:        routingCostFloatPointer(groupRatio),
		Tiers:             item.Tiers,
		BillingExpression: billingExpression,
	}
	if item.CompletionRatio != nil {
		pricing.CompletionRatio = routingCostFloatPointer(completionRatio)
	}
	if item.ModelRatio != nil {
		pricing.BaseRatio = routingCostFloatPointer(baseRatio)
		pricing.InputCostPerMillion = routingCostFloatPointer(inputCostPerMillion)
		pricing.OutputCostPerMillion = routingCostFloatPointer(outputCostPerMillion)
	}
	if item.ModelPrice != nil {
		pricing.ModelPrice = routingCostFloatPointer(*item.ModelPrice)
	}
	if perRequestCost != nil {
		pricing.PerRequestCost = routingCostFloatPointer(*perRequestCost)
	}
	if item.CacheRatio != nil && item.ModelRatio != nil && item.QuotaType == 0 {
		pricing.CacheReadCostPerMillion = routingCostFloatPointer(inputCostPerMillion * *item.CacheRatio)
	}
	if item.ModelRatio != nil && item.QuotaType == 0 {
		createCacheRatio := 1.25
		if item.CreateCacheRatio != nil {
			createCacheRatio = *item.CreateCacheRatio
		}
		cacheWriteCostPerMillion := inputCostPerMillion * createCacheRatio
		pricing.CacheWriteCostPerMillion = routingCostFloatPointer(cacheWriteCostPerMillion)
		pricing.CacheWrite1hCostPerMillion = routingCostFloatPointer(cacheWriteCostPerMillion * (6.0 / 3.75))
	}
	if item.ImageRatio != nil && item.ModelRatio != nil && item.QuotaType == 0 {
		pricing.ImageInputCostPerMillion = routingCostFloatPointer(inputCostPerMillion * *item.ImageRatio)
	}
	if item.ModelRatio != nil && item.QuotaType == 0 {
		audioRatio := 1.0
		if item.AudioRatio != nil {
			audioRatio = *item.AudioRatio
		}
		audioCompletionRatio := 1.0
		if item.AudioCompletionRatio != nil {
			audioCompletionRatio = *item.AudioCompletionRatio
		}
		audioInputCostPerMillion := inputCostPerMillion * audioRatio
		pricing.AudioInputCostPerMillion = routingCostFloatPointer(audioInputCostPerMillion)
		pricing.AudioOutputCostPerMillion = routingCostFloatPointer(audioInputCostPerMillion * audioCompletionRatio)
	}
	for _, value := range []*float64{
		pricing.CacheReadCostPerMillion,
		pricing.CacheWriteCostPerMillion,
		pricing.CacheWrite1hCostPerMillion,
		pricing.ImageInputCostPerMillion,
		pricing.AudioInputCostPerMillion,
		pricing.AudioOutputCostPerMillion,
	} {
		if value != nil && !routingCostNonNegativeFinite(*value) {
			return model.RoutingNormalizedPricing{}, "", 0, model.ErrRoutingCostV2Invalid
		}
	}
	extras := map[string]any{
		"catalog_scope": model.RoutingCostCatalogScopeNewAPIPricing,
		"always_uncatalogued_surcharge": strings.HasSuffix(
			strings.ToLower(strings.TrimSpace(item.ModelName)), "search-preview",
		),
	}
	if item.ImageRatio != nil {
		extras["image_ratio"] = *item.ImageRatio
	}
	if item.CacheRatio != nil {
		extras["cache_ratio"] = *item.CacheRatio
	}
	if item.CreateCacheRatio != nil {
		extras["create_cache_ratio"] = *item.CreateCacheRatio
	}
	if item.AudioRatio != nil {
		extras["audio_ratio"] = *item.AudioRatio
	}
	if item.AudioCompletionRatio != nil {
		extras["audio_completion_ratio"] = *item.AudioCompletionRatio
	}
	if len(extras) > 0 {
		encoded, err := common.Marshal(extras)
		if err != nil {
			return model.RoutingNormalizedPricing{}, "", 0, err
		}
		pricing.Extras = encoded
	}
	if pricing.BillingExpression != "" && len(strings.TrimSpace(string(pricing.Tiers))) == 0 {
		encoded, err := common.Marshal(map[string]string{"type": "expr", "expr": pricing.BillingExpression})
		if err != nil {
			return model.RoutingNormalizedPricing{}, "", 0, err
		}
		pricing.Tiers = encoded
	}
	if groupRatioKnown && groupRatio == 0 {
		return pricing, model.RoutingCostConfidenceExact, 1, nil
	}
	known := item.ModelRatio != nil || perRequestCost != nil || pricing.BillingExpression != "" ||
		len(strings.TrimSpace(string(pricing.Tiers))) > 0
	if !known {
		return pricing, model.RoutingCostConfidenceUnknown, 0, nil
	}
	if groupRatioKnown {
		return pricing, model.RoutingCostConfidenceExact, 1, nil
	}
	return pricing, model.RoutingCostConfidenceGroupOnly, 0.7, nil
}

func routingSub2APICostVersionWrites(
	binding model.RoutingChannelBinding,
	modelNameMap map[string]string,
	payload routingCostAccountPayload,
) ([]model.RoutingCostSnapshotVersionWrite, error) {
	pricingPayload := payload.Sub2API
	groupInfo, groupFound := pricingPayload.Groups[binding.UpstreamGroup]
	if !groupFound {
		return nil, errors.New("sub2api bound group is not available to the account")
	}
	if groupInfo.ClaudeCodeOnly && !binding.ServesClaudeCode {
		return nil, errors.New("sub2api bound group is restricted to Claude Code traffic")
	}
	if groupFound && groupInfo.PeakRateEnabled {
		return nil, errors.New("sub2api peak group pricing requires unavailable server timezone context")
	}
	groupRatio := routingSub2APIGroupRatio(groupInfo)
	if ratio, ok := routingSub2APIResolvedGroupRate(pricingPayload.Groups, pricingPayload.Rates, binding.UpstreamGroup); ok {
		if !routingCostNonNegativeFinite(ratio) || ratio <= 0 {
			return nil, errors.New("sub2api returned an invalid group ratio")
		}
		groupRatio = ratio
		groupFound = true
	}
	if !routingCostNonNegativeFinite(groupRatio) || groupRatio <= 0 {
		return nil, errors.New("sub2api returned an invalid group ratio")
	}
	channels := append([]routingSub2APIChannel(nil), pricingPayload.Channels...)
	sort.SliceStable(channels, func(left int, right int) bool {
		leftModels := strings.Join(routingSub2APIChannelModels(channels[left]), "\x00")
		rightModels := strings.Join(routingSub2APIChannelModels(channels[right]), "\x00")
		if leftModels != rightModels {
			return leftModels < rightModels
		}
		return channels[left].Platform < channels[right].Platform
	})
	writes := make([]model.RoutingCostSnapshotVersionWrite, 0, len(channels))
	seenModels := make(map[string][sha256.Size]byte)
	for _, channel := range channels {
		if !routingSub2APIChannelServesBinding(channel, binding) {
			continue
		}
		pricing, confidence, confidenceScore, err := routingSub2APINormalizedPricing(channel, groupRatio, groupFound)
		if err != nil {
			return nil, fmt.Errorf("invalid sub2api channel pricing: %w", err)
		}
		if confidence == model.RoutingCostConfidenceUnknown {
			return nil, errors.New("sub2api returned a model without usable pricing")
		}
		if payload.SyncStatus == model.RoutingUpstreamSyncStatusPartial && confidenceScore > 0.8 {
			confidenceScore = 0.8
		}
		pricingForFingerprint := pricing
		if len(pricingForFingerprint.Extras) > 0 {
			var extras map[string]any
			if err := common.Unmarshal(pricingForFingerprint.Extras, &extras); err != nil {
				return nil, fmt.Errorf("invalid sub2api pricing metadata: %w", err)
			}
			delete(extras, "platform")
			if len(extras) == 0 {
				pricingForFingerprint.Extras = nil
			} else {
				encoded, err := common.Marshal(extras)
				if err != nil {
					return nil, err
				}
				pricingForFingerprint.Extras = encoded
			}
		}
		pricingMaterial, err := common.Marshal(pricingForFingerprint)
		if err != nil {
			return nil, err
		}
		pricingFingerprint := sha256.Sum256(pricingMaterial)
		for _, upstreamModel := range routingSub2APIChannelModels(channel) {
			localModel := upstreamModel
			if mapped, ok := modelNameMap[upstreamModel]; ok {
				localModel = mapped
			}
			if previousFingerprint, duplicate := seenModels[localModel]; duplicate {
				if previousFingerprint != pricingFingerprint {
					return nil, fmt.Errorf("sub2api returned conflicting pricing for local model %s", localModel)
				}
				continue
			}
			seenModels[localModel] = pricingFingerprint
			writes = append(writes, model.RoutingCostSnapshotVersionWrite{
				ChannelID:        binding.ChannelID,
				UpstreamGroup:    binding.UpstreamGroup,
				UpstreamModel:    upstreamModel,
				LocalModel:       localModel,
				ObservedTime:     payload.ObservedTime,
				EffectiveTime:    payload.EffectiveTime,
				ExpiresTime:      payload.ExpiresTime,
				PricingVersion:   payload.PricingVersion,
				Confidence:       confidence,
				ConfidenceScore:  confidenceScore,
				Freshness:        model.RoutingCostFreshnessFresh,
				FreshnessScore:   1,
				SourceSyncStatus: payload.SyncStatus,
				SourceSyncError:  payload.SyncError,
				Pricing:          pricing,
			})
		}
	}
	if len(writes) == 0 {
		return nil, errors.New("sub2api returned no pricing for the bound group")
	}
	return writes, nil
}

func routingSub2APINormalizedPricing(
	channel routingSub2APIChannel,
	groupRatio float64,
	groupFound bool,
) (model.RoutingNormalizedPricing, string, float64, error) {
	if channel.PerTokenPrices {
		return routingSub2APIOfficialNormalizedPricing(channel, groupRatio, groupFound)
	}
	values := []float64{
		channel.InputPrice,
		channel.OutputPrice,
		channel.CachePrice,
		channel.PerRequestPrice,
		channel.ImagePrice,
		channel.Price,
		channel.Rate,
		channel.Ratio,
		channel.Input,
		channel.Output,
		channel.Cache,
		channel.PerRequest,
		channel.Image,
	}
	for _, value := range values {
		if !routingCostNonNegativeFinite(value) {
			return model.RoutingNormalizedPricing{}, "", 0, model.ErrRoutingCostV2Invalid
		}
	}
	inputCost := firstPositiveFloat(channel.InputPrice, channel.Input, channel.Price, channel.Rate, channel.Ratio)
	outputCost := firstPositiveFloat(channel.OutputPrice, channel.Output)
	cacheCost := firstPositiveFloat(channel.CachePrice, channel.Cache)
	perRequestCost := firstPositiveFloat(channel.PerRequestPrice, channel.PerRequest)
	imageCost := firstPositiveFloat(channel.ImagePrice, channel.Image)
	completionRatio := 1.0
	if inputCost > 0 && outputCost > 0 {
		completionRatio = outputCost / inputCost
	}
	billingMode := strings.ToLower(strings.TrimSpace(channel.BillingMode))
	if billingMode == "" {
		if inputCost <= 0 && perRequestCost > 0 {
			billingMode = "per_request"
		} else {
			billingMode = "token"
		}
	}
	pricing := model.RoutingNormalizedPricing{
		QuotaType:       0,
		BillingMode:     billingMode,
		Currency:        "USD",
		GroupRatio:      routingCostFloatPointer(groupRatio),
		CompletionRatio: routingCostFloatPointer(completionRatio),
	}
	if inputCost > 0 {
		pricing.BaseRatio = routingCostFloatPointer(inputCost)
		pricing.InputCostPerMillion = routingCostFloatPointer(inputCost)
	}
	if outputCost > 0 {
		pricing.OutputCostPerMillion = routingCostFloatPointer(outputCost)
	}
	if cacheCost > 0 {
		pricing.CacheReadCostPerMillion = routingCostFloatPointer(cacheCost)
	}
	if perRequestCost > 0 {
		pricing.PerRequestCost = routingCostFloatPointer(perRequestCost)
		if billingMode == "per_request" {
			pricing.ModelPrice = routingCostFloatPointer(perRequestCost)
		}
	}
	if imageCost > 0 {
		pricing.ImageCost = routingCostFloatPointer(imageCost)
		pricing.PerImageCost = routingCostFloatPointer(imageCost)
	}
	extras := map[string]float64{}
	if outputCost > 0 {
		extras["output_price"] = outputCost
	}
	if cacheCost > 0 {
		extras["cache_price"] = cacheCost
	}
	if perRequestCost > 0 {
		extras["per_request_price"] = perRequestCost
	}
	if imageCost > 0 {
		extras["image_price"] = imageCost
	}
	if len(extras) > 0 {
		encoded, err := common.Marshal(extras)
		if err != nil {
			return model.RoutingNormalizedPricing{}, "", 0, err
		}
		pricing.Extras = encoded
	}
	if inputCost <= 0 && perRequestCost <= 0 && imageCost <= 0 {
		return pricing, model.RoutingCostConfidenceUnknown, 0, nil
	}
	if groupFound {
		return pricing, model.RoutingCostConfidenceExact, 1, nil
	}
	return pricing, model.RoutingCostConfidenceGroupOnly, 0.7, nil
}

func routingSub2APIOfficialNormalizedPricing(
	channel routingSub2APIChannel,
	groupRatio float64,
	groupFound bool,
) (model.RoutingNormalizedPricing, string, float64, error) {
	if channel.OfficialPricing == nil {
		return model.RoutingNormalizedPricing{
			QuotaType: 0, BillingMode: "token", Currency: "USD",
			GroupRatio: routingCostFloatPointer(groupRatio),
		}, model.RoutingCostConfidenceUnknown, 0, nil
	}
	pricingPayload := *channel.OfficialPricing
	billingMode := strings.ToLower(strings.TrimSpace(pricingPayload.BillingMode))
	if billingMode == "" {
		billingMode = "token"
	}
	pricing := model.RoutingNormalizedPricing{
		QuotaType:   0,
		BillingMode: billingMode,
		Currency:    "USD",
		GroupRatio:  routingCostFloatPointer(groupRatio),
	}
	confidence := model.RoutingCostConfidenceGroupOnly
	confidenceScore := 0.7
	if groupFound {
		confidence = model.RoutingCostConfidenceExact
		confidenceScore = 1
	}

	switch billingMode {
	case "token":
		expression, known, err := routingSub2APITokenPricingExpression(pricingPayload)
		if err != nil {
			return model.RoutingNormalizedPricing{}, "", 0, err
		}
		if !known {
			return pricing, model.RoutingCostConfidenceUnknown, 0, nil
		}
		pricing.BillingExpression = expression
		encoded, err := common.Marshal(map[string]string{"type": "expr", "expr": expression})
		if err != nil {
			return model.RoutingNormalizedPricing{}, "", 0, err
		}
		pricing.Tiers = encoded
		if len(pricingPayload.Intervals) > 0 {
			pricing.BillingMode = "tiered_expr"
			if confidence == model.RoutingCostConfidenceExact {
				confidence = model.RoutingCostConfidenceDerived
				confidenceScore = 0.9
			}
		} else {
			inputPrice := routingSub2APIPointerValue(pricingPayload.InputPrice)
			outputPrice := routingSub2APIPointerValue(pricingPayload.OutputPrice)
			pricing.BaseRatio = routingCostFloatPointer(inputPrice * common.QuotaPerUnit)
			if inputPrice > 0 {
				completionRatio := outputPrice / inputPrice
				if !routingCostNonNegativeFinite(completionRatio) {
					return model.RoutingNormalizedPricing{}, "", 0, errors.New("sub2api completion ratio overflows normalized units")
				}
				pricing.CompletionRatio = routingCostFloatPointer(completionRatio)
			}
			pricing.InputCostPerMillion = routingCostFloatPointer(inputPrice * 1_000_000)
			pricing.OutputCostPerMillion = routingCostFloatPointer(outputPrice * 1_000_000)
			pricing.CacheWriteCostPerMillion = routingCostFloatPointer(routingSub2APIPointerValue(pricingPayload.CacheWritePrice) * 1_000_000)
			pricing.CacheReadCostPerMillion = routingCostFloatPointer(routingSub2APIPointerValue(pricingPayload.CacheReadPrice) * 1_000_000)
			pricing.ImageOutputCostPerMillion = routingCostFloatPointer(routingSub2APIPointerValue(pricingPayload.ImageOutputPrice) * 1_000_000)
			// The user-facing channels contract does not expose Sub2API's private
			// long-context and priority-service-tier overrides, so flat prices are
			// useful but cannot be represented as exact for every request shape.
			if confidence == model.RoutingCostConfidenceExact {
				confidence = model.RoutingCostConfidenceDerived
				confidenceScore = 0.8
			}
		}
	case "image":
		return model.RoutingNormalizedPricing{}, "", 0, errors.New("sub2api image pricing requires group-specific size and multiplier semantics")
	case "per_request":
		if len(pricingPayload.Intervals) > 0 {
			return model.RoutingNormalizedPricing{}, "", 0, errors.New("sub2api per-request tier labels cannot be mapped safely")
		}
		if pricingPayload.PerRequestPrice == nil {
			return pricing, model.RoutingCostConfidenceUnknown, 0, nil
		}
		if !routingCostNonNegativeFinite(*pricingPayload.PerRequestPrice) {
			return model.RoutingNormalizedPricing{}, "", 0, model.ErrRoutingCostV2Invalid
		}
		pricing.QuotaType = 1
		pricing.ModelPrice = routingCostFloatPointer(*pricingPayload.PerRequestPrice)
		pricing.PerRequestCost = routingCostFloatPointer(*pricingPayload.PerRequestPrice)
	default:
		return model.RoutingNormalizedPricing{}, "", 0, errors.New("sub2api returned an unsupported billing mode")
	}

	priceUnit := "usd_per_token"
	if billingMode == "per_request" {
		priceUnit = "usd_per_request"
	}
	extras, err := common.Marshal(map[string]any{
		"platform":            channel.Platform,
		"source_billing_mode": billingMode,
		"price_unit":          priceUnit,
		"sub2api_contract":    model.RoutingCostSub2APIDisplayContractV1,
		"has_intervals":       len(pricingPayload.Intervals) > 0,
	})
	if err != nil {
		return model.RoutingNormalizedPricing{}, "", 0, err
	}
	pricing.Extras = extras
	return pricing, confidence, confidenceScore, nil
}

func routingCostNonNegativeFinite(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func routingCostFloatPointer(value float64) *float64 {
	return &value
}

func fetchRoutingCostSnapshots(ctx context.Context, binding model.RoutingChannelBinding) ([]model.RoutingCostSnapshot, error) {
	if binding.UpstreamType == model.RoutingUpstreamTypeSub2API {
		credentials, err := binding.GetCredentials()
		if err != nil {
			return nil, err
		}
		ctx, err = withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
		if err != nil {
			return nil, err
		}
		return fetchRoutingSub2APICostSnapshots(ctx, binding, credentials)
	}

	payload, err := fetchRoutingPricingPayload(ctx, binding)
	if err != nil {
		return nil, err
	}

	now := common.GetTimestamp()
	groupRatio, hasGroupRatio := payload.GroupRatio[binding.UpstreamGroup]
	if !hasGroupRatio {
		return nil, errors.New("newapi bound group is not available to the account")
	}
	if !routingCostNonNegativeFinite(groupRatio) {
		return nil, errors.New("newapi returned an invalid group ratio")
	}
	confidence := model.RoutingCostConfidenceFull
	modelNameMap, err := routingModelReverseMapping(ctx, binding.ChannelID)
	if err != nil {
		return nil, err
	}

	snapshots := make([]model.RoutingCostSnapshot, 0, len(payload.Data))
	for _, item := range payload.Data {
		if strings.TrimSpace(item.ModelName) == "" || !routingPricingItemServesGroup(item.EnableGroups, binding.UpstreamGroup) {
			continue
		}
		modelName := strings.TrimSpace(item.ModelName)
		if localName, ok := modelNameMap[modelName]; ok {
			modelName = localName
		}
		baseRatio := 0.0
		if item.ModelRatio != nil {
			baseRatio = *item.ModelRatio
		}
		completionRatio := 1.0
		if item.CompletionRatio != nil {
			completionRatio = *item.CompletionRatio
		}
		modelPrice := 0.0
		if item.ModelPrice != nil {
			modelPrice = *item.ModelPrice
		}
		snapshot := model.RoutingCostSnapshot{
			ChannelID:       binding.ChannelID,
			ModelName:       modelName,
			QuotaType:       item.QuotaType,
			GroupRatio:      groupRatio,
			BaseRatio:       baseRatio,
			CompletionRatio: completionRatio,
			ModelPrice:      modelPrice,
			BillingMode:     item.BillingMode,
			Confidence:      confidence,
			SnapshotTS:      now,
			PricingVersion:  payload.PricingVersion,
		}
		if strings.TrimSpace(item.BillingExpr) != "" {
			tiersJSON, err := common.Marshal(map[string]string{
				"type": "expr",
				"expr": item.BillingExpr,
			})
			if err != nil {
				return nil, err
			}
			encoded := string(tiersJSON)
			snapshot.TiersJSON = &encoded
			snapshot.Confidence = model.RoutingCostConfidenceUnknown
		} else if strings.TrimSpace(item.BillingMode) == "tiered_expr" {
			snapshot.Confidence = model.RoutingCostConfidenceUnknown
		}
		if item.QuotaType == 0 && snapshot.BaseRatio <= 0 {
			snapshot.Confidence = model.RoutingCostConfidenceUnknown
		}
		if item.QuotaType == 1 && snapshot.ModelPrice <= 0 {
			snapshot.Confidence = model.RoutingCostConfidenceUnknown
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func routingModelReverseMapping(ctx context.Context, channelID int) (map[string]string, error) {
	if channelID <= 0 {
		return nil, nil
	}
	var channel model.Channel
	if err := model.DB.WithContext(ctx).Select("id", "model_mapping").Where("id = ?", channelID).First(&channel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if channel.ModelMapping == nil || strings.TrimSpace(*channel.ModelMapping) == "" {
		return nil, nil
	}
	var mapping map[string]string
	if err := common.UnmarshalJsonStr(*channel.ModelMapping, &mapping); err != nil {
		return nil, nil
	}
	localNames := make([]string, 0, len(mapping))
	for localName := range mapping {
		if strings.TrimSpace(localName) != "" {
			localNames = append(localNames, localName)
		}
	}
	sort.Strings(localNames)
	reverse := make(map[string]string, len(mapping))
	for _, localName := range localNames {
		upstreamName := strings.TrimSpace(mapping[localName])
		if upstreamName == "" {
			continue
		}
		if _, exists := reverse[upstreamName]; !exists {
			reverse[upstreamName] = localName
		}
	}
	return reverse, nil
}

func fetchRoutingPricingPayload(ctx context.Context, binding model.RoutingChannelBinding) (_ routingPricingResponse, err error) {
	credentials, err := binding.GetCredentials()
	if err != nil {
		return routingPricingResponse{}, routingSafeErrorWithCredentials(err, model.RoutingCredentials{})
	}
	defer func() {
		if err != nil {
			err = routingSafeErrorWithCredentials(err, credentials)
		}
	}()
	ctx, err = withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
	if err != nil {
		return routingPricingResponse{}, err
	}
	if binding.UpstreamType == model.RoutingUpstreamTypeSub2API {
		upstreamGroup := strings.TrimSpace(binding.UpstreamGroup)
		if upstreamGroup == "" {
			return routingPricingResponse{}, errors.New("sub2api bound group is required for pricing test")
		}
		pricing, err := fetchRoutingSub2APIAccountPricingForGroups(
			ctx,
			binding,
			credentials,
			[]string{upstreamGroup},
		)
		if err != nil {
			return routingPricingResponse{}, err
		}
		pricingVersion := routingCostContentVersion("sub2api", pricing.VersionMaterial())
		groupRatio := make(map[string]float64)
		usableGroup := make(map[string]string)
		groupMeta := make(map[string]routingSub2APIGroupMetadata)
		uniqueGroups := make(map[string]routingSub2APIGroup)
		for _, group := range pricing.Groups {
			identity := strings.TrimSpace(string(group.ID))
			if identity != "" {
				uniqueGroups[identity] = group
			}
		}
		groupIDs := make([]string, 0, len(uniqueGroups))
		for groupID := range uniqueGroups {
			groupIDs = append(groupIDs, groupID)
		}
		sort.Strings(groupIDs)
		for _, groupID := range groupIDs {
			group := uniqueGroups[groupID]
			groupName := strings.TrimSpace(group.Name)
			if groupName == "" {
				groupName = groupID
			}
			usableGroup[groupName] = groupID
			groupMeta[groupName] = routingSub2APIGroupMetadata{
				ID:               groupID,
				Name:             groupName,
				Platform:         strings.TrimSpace(group.Platform),
				SubscriptionType: strings.ToLower(strings.TrimSpace(group.SubscriptionType)),
				ClaudeCodeOnly:   group.ClaudeCodeOnly,
			}
			ratio := routingSub2APIGroupRatio(group)
			if resolved, ok := routingSub2APIResolvedGroupRate(pricing.Groups, pricing.Rates, groupName); ok {
				ratio = resolved
			}
			if routingCostNonNegativeFinite(ratio) && ratio > 0 {
				groupRatio[groupName] = ratio
			}
		}
		response := routingPricingResponse{
			Success:          true,
			GroupRatio:       groupRatio,
			UsableGroup:      usableGroup,
			Sub2APIGroupMeta: groupMeta,
			PricingVersion:   pricingVersion,
		}
		if strings.TrimSpace(binding.UpstreamGroup) == "" {
			modelSet := make(map[string]struct{})
			for _, channel := range pricing.Channels {
				if channel.OfficialPricing == nil {
					continue
				}
				for _, modelName := range routingSub2APIChannelModels(channel) {
					modelSet[modelName] = struct{}{}
				}
			}
			modelNames := make([]string, 0, len(modelSet))
			for modelName := range modelSet {
				modelNames = append(modelNames, modelName)
			}
			sort.Strings(modelNames)
			response.Data = make([]routingPricingItem, 0, len(modelNames))
			for _, modelName := range modelNames {
				response.Data = append(response.Data, routingPricingItem{ModelName: modelName})
			}
			return response, nil
		}

		modelNameMap, mapErr := routingModelReverseMapping(ctx, binding.ChannelID)
		if mapErr != nil {
			return routingPricingResponse{}, mapErr
		}
		now := common.GetTimestamp()
		writes, writeErr := routingSub2APICostVersionWrites(binding, modelNameMap, routingCostAccountPayload{
			SourceType:   model.RoutingUpstreamTypeSub2API,
			ObservedTime: now, EffectiveTime: now, ExpiresTime: now + 3_600,
			PricingVersion: pricingVersion, SyncStatus: pricing.SyncStatus,
			SyncError: pricing.SyncError, Sub2API: &pricing,
		})
		if writeErr != nil {
			return routingPricingResponse{}, writeErr
		}
		response.Data = make([]routingPricingItem, 0, len(writes))
		for _, write := range writes {
			item := routingPricingItem{
				ModelName: write.LocalModel, QuotaType: write.Pricing.QuotaType,
				EnableGroups: []string{binding.UpstreamGroup}, BillingMode: write.Pricing.BillingMode,
				BillingExpr: write.Pricing.BillingExpression, Tiers: write.Pricing.Tiers,
				PricingVersion: pricingVersion,
			}
			if write.Pricing.BaseRatio != nil {
				item.ModelRatio = routingCostFloatPointer(*write.Pricing.BaseRatio)
			}
			if write.Pricing.CompletionRatio != nil {
				item.CompletionRatio = routingCostFloatPointer(*write.Pricing.CompletionRatio)
			}
			if write.Pricing.ModelPrice != nil {
				item.ModelPrice = routingCostFloatPointer(*write.Pricing.ModelPrice)
			} else if write.Pricing.PerRequestCost != nil {
				item.ModelPrice = routingCostFloatPointer(*write.Pricing.PerRequestCost)
			}
			response.Data = append(response.Data, item)
		}
		return response, nil
	}
	if binding.UpstreamType != model.RoutingUpstreamTypeNewAPI {
		return routingPricingResponse{}, errors.New("unsupported routing upstream account type")
	}
	requestedGroups := []string(nil)
	if strings.TrimSpace(binding.UpstreamGroup) != "" {
		requestedGroups = []string{strings.TrimSpace(binding.UpstreamGroup)}
	}
	if len(requestedGroups) == 0 {
		return fetchRoutingNewAPIAccountPricingPayload(ctx, binding, credentials, nil)
	}
	servingModels, err := fetchRoutingNewAPIGatewayModels(ctx, binding, credentials)
	if err != nil {
		return routingPricingResponse{}, err
	}
	upstreamQuotaPerUnit, err := fetchRoutingNewAPIQuotaPerUnit(ctx, binding, credentials)
	if err != nil {
		return routingPricingResponse{}, err
	}
	if _, _, balanceErr := fetchRoutingNewAPIBalanceValue(
		ctx,
		binding,
		credentials,
		upstreamQuotaPerUnit,
	); balanceErr != nil && routingUpstreamAuthError(balanceErr) {
		return routingPricingResponse{}, balanceErr
	}
	pricing, err := fetchRoutingNewAPIAccountPricingPayloadWithQuotaPerUnit(
		ctx,
		binding,
		credentials,
		requestedGroups,
		upstreamQuotaPerUnit,
		nil,
	)
	if err != nil {
		return pricing, err
	}
	modelNameMap, err := routingModelReverseMapping(ctx, binding.ChannelID)
	if err != nil {
		return routingPricingResponse{}, err
	}
	now := common.GetTimestamp()
	writes, err := routingNewAPICostVersionWrites(binding, modelNameMap, routingCostAccountPayload{
		SourceType: model.RoutingUpstreamTypeNewAPI, ObservedTime: now,
		EffectiveTime: now, ExpiresTime: now + 3_600, PricingVersion: pricing.PricingVersion,
		SyncStatus: model.RoutingUpstreamSyncStatusSuccess, NewAPI: &pricing,
	})
	if err != nil {
		return routingPricingResponse{}, err
	}
	writes, err = filterRoutingNewAPIWritesByServingModels(writes, servingModels)
	if err != nil {
		return routingPricingResponse{}, err
	}
	pricing.Data = make([]routingPricingItem, 0, len(writes))
	for _, write := range writes {
		item := routingPricingItem{
			ModelName: write.LocalModel, QuotaType: write.Pricing.QuotaType,
			EnableGroups: []string{binding.UpstreamGroup}, BillingMode: write.Pricing.BillingMode,
			BillingExpr: write.Pricing.BillingExpression, Tiers: write.Pricing.Tiers,
			PricingVersion: pricing.PricingVersion,
		}
		if write.Pricing.BaseRatio != nil {
			item.ModelRatio = routingCostFloatPointer(*write.Pricing.BaseRatio)
		}
		if write.Pricing.CompletionRatio != nil {
			item.CompletionRatio = routingCostFloatPointer(*write.Pricing.CompletionRatio)
		}
		if write.Pricing.ModelPrice != nil {
			item.ModelPrice = routingCostFloatPointer(*write.Pricing.ModelPrice)
		} else if write.Pricing.PerRequestCost != nil {
			item.ModelPrice = routingCostFloatPointer(*write.Pricing.PerRequestCost)
		}
		pricing.Data = append(pricing.Data, item)
	}
	return pricing, nil
}

func fetchRoutingNewAPIQuotaPerUnit(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
) (float64, error) {
	ctx, err := withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(binding.BaseURL, "/")+"/api/status",
		nil,
	)
	if err != nil {
		return 0, err
	}
	applyRoutingAuthHeaders(request, binding, credentials)

	response, err := routingCostHTTPDoer.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return 0, routingAuthErrorf("status endpoint returned %s", response.Status)
	}
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status endpoint returned %s", response.Status)
	}

	body, err := readRoutingCostJSON(response, defaultRoutingJSONLimits)
	if err != nil {
		return 0, err
	}
	var payload routingNewAPIStatusResponse
	if err := common.Unmarshal(body, &payload); err != nil {
		return 0, errors.New("invalid newapi status response")
	}
	if !payload.Success {
		message := strings.TrimSpace(payload.Message)
		if message == "" {
			message = "status endpoint returned success=false"
		}
		return 0, errors.New(routingCleanCredentialErrorMessage(message, credentials))
	}
	if payload.Data.QuotaPerUnit == nil ||
		!routingCostNonNegativeFinite(*payload.Data.QuotaPerUnit) ||
		*payload.Data.QuotaPerUnit <= 0 {
		return 0, errors.New("newapi status response contains an invalid quota_per_unit")
	}
	return *payload.Data.QuotaPerUnit, nil
}

func fetchRoutingNewAPIPricingPayload(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
) (routingPricingResponse, error) {
	ctx, err := withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
	if err != nil {
		return routingPricingResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(binding.BaseURL, "/")+"/api/pricing", nil)
	if err != nil {
		return routingPricingResponse{}, err
	}
	applyRoutingAuthHeaders(request, binding, credentials)

	response, err := routingCostHTTPDoer.Do(request)
	if err != nil {
		return routingPricingResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized {
			return routingPricingResponse{}, routingAuthErrorf("pricing endpoint returned %s", response.Status)
		}
		if response.StatusCode == http.StatusForbidden {
			message := "pricing endpoint is unavailable"
			if body, readErr := readRoutingCostJSON(response, defaultRoutingJSONLimits); readErr == nil {
				var errorPayload struct {
					Message string `json:"message"`
				}
				if common.Unmarshal(body, &errorPayload) == nil && strings.TrimSpace(errorPayload.Message) != "" {
					message = routingCleanCredentialErrorMessage(errorPayload.Message, credentials)
				}
			}
			return routingPricingResponse{}, errors.New(message)
		}
		return routingPricingResponse{}, fmt.Errorf("pricing endpoint returned %s", response.Status)
	}

	body, err := readRoutingCostJSON(response, defaultRoutingJSONLimits)
	if err != nil {
		return routingPricingResponse{}, err
	}
	var payload routingPricingResponse
	if err = common.Unmarshal(body, &payload); err != nil {
		return routingPricingResponse{}, errors.New("invalid routing pricing response")
	}
	if !payload.Success {
		return routingPricingResponse{}, routingNewAPISuccessFalseError(
			response,
			payload.Message,
			"pricing endpoint returned success=false",
			credentials,
		)
	}
	payload.CatalogAuthenticated = strings.TrimSpace(response.Header.Get("Auth-Version")) != ""
	return payload, nil
}

func fetchRoutingNewAPIAccountPricingPayload(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	requestedGroups []string,
) (routingPricingResponse, error) {
	requiresPricing := false
	for _, groupName := range requestedGroups {
		if strings.TrimSpace(groupName) != "" {
			requiresPricing = true
			break
		}
	}
	if !requiresPricing {
		return fetchRoutingNewAPIAccountPricingPayloadWithQuotaPerUnit(
			ctx,
			binding,
			credentials,
			requestedGroups,
			0,
			nil,
		)
	}
	upstreamQuotaPerUnit, err := fetchRoutingNewAPIQuotaPerUnit(ctx, binding, credentials)
	if err != nil {
		return routingPricingResponse{}, err
	}
	return fetchRoutingNewAPIAccountPricingPayloadWithQuotaPerUnit(
		ctx,
		binding,
		credentials,
		requestedGroups,
		upstreamQuotaPerUnit,
		nil,
	)
}

func fetchRoutingNewAPIAccountPricingPayloadWithQuotaPerUnit(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	requestedGroups []string,
	upstreamQuotaPerUnit float64,
	preloadedGroups *routingNewAPIUserGroupsResponse,
) (routingPricingResponse, error) {
	groupsPayload := routingNewAPIUserGroupsResponse{}
	if preloadedGroups != nil {
		groupsPayload = *preloadedGroups
	} else {
		var err error
		groupsPayload, err = fetchRoutingNewAPIUserGroups(ctx, binding, credentials)
		if err != nil {
			return routingPricingResponse{}, err
		}
	}
	groupRatio := make(map[string]float64, len(groupsPayload.Data))
	usableGroup := make(map[string]string, len(groupsPayload.Data))
	for groupName, group := range groupsPayload.Data {
		groupName = strings.TrimSpace(groupName)
		if groupName == "" || common.GetJsonType(group.Ratio) != "number" {
			continue
		}
		var ratio float64
		if err := common.Unmarshal(group.Ratio, &ratio); err != nil || !routingCostNonNegativeFinite(ratio) {
			continue
		}
		groupRatio[groupName] = ratio
		usableGroup[groupName] = strings.TrimSpace(group.Desc)
	}
	if len(groupRatio) == 0 {
		return routingPricingResponse{}, errors.New("newapi returned no account groups with stable pricing ratios")
	}

	normalizedGroups := make([]string, 0, len(requestedGroups))
	seenGroups := make(map[string]struct{}, len(requestedGroups))
	for _, groupName := range requestedGroups {
		groupName = strings.TrimSpace(groupName)
		if groupName == "" {
			continue
		}
		if _, exists := seenGroups[groupName]; exists {
			continue
		}
		seenGroups[groupName] = struct{}{}
		normalizedGroups = append(normalizedGroups, groupName)
	}
	sort.Strings(normalizedGroups)
	groupErrors := make(map[string]string)
	validGroups := make([]string, 0, len(normalizedGroups))
	for _, groupName := range normalizedGroups {
		_, exists := groupsPayload.Data[groupName]
		if !exists {
			groupErrors[groupName] = fmt.Sprintf("newapi bound group %s is not available to the account", groupName)
			continue
		}
		if _, valid := groupRatio[groupName]; !valid {
			groupErrors[groupName] = fmt.Sprintf("newapi bound group %s has no stable numeric ratio", groupName)
			continue
		}
		validGroups = append(validGroups, groupName)
	}

	result := routingPricingResponse{
		Success:            true,
		GroupRatio:         groupRatio,
		UsableGroup:        usableGroup,
		AccountGroupErrors: groupErrors,
		QuotaPerUnit:       upstreamQuotaPerUnit,
	}
	if len(normalizedGroups) == 0 {
		result.PricingVersion = routingCostContentVersion("newapi-account-groups", struct {
			GroupRatio  map[string]float64 `json:"group_ratio"`
			UsableGroup map[string]string  `json:"usable_group"`
		}{GroupRatio: groupRatio, UsableGroup: usableGroup})
		return result, nil
	}
	if len(validGroups) == 0 {
		result.PricingVersion = routingCostContentVersion("newapi-account-groups", struct {
			GroupRatio  map[string]float64 `json:"group_ratio"`
			UsableGroup map[string]string  `json:"usable_group"`
		}{GroupRatio: groupRatio, UsableGroup: usableGroup})
		return result, nil
	}
	if !routingCostNonNegativeFinite(upstreamQuotaPerUnit) || upstreamQuotaPerUnit <= 0 {
		return routingPricingResponse{}, errors.New("newapi status response contains an invalid quota_per_unit")
	}

	catalog, err := fetchRoutingNewAPIPricingPayload(ctx, binding, credentials)
	if err != nil {
		return routingPricingResponse{}, err
	}
	catalogByModel := make(map[string]routingPricingItem, len(catalog.Data))
	for _, item := range catalog.Data {
		modelName := strings.TrimSpace(item.ModelName)
		if modelName == "" {
			continue
		}
		if _, duplicate := catalogByModel[modelName]; duplicate {
			return routingPricingResponse{}, fmt.Errorf("newapi pricing directory returned duplicate model %s", modelName)
		}
		item.ModelName = modelName
		catalogByModel[modelName] = item
	}

	modelsByGroup := make(map[string][]string, len(validGroups))
	groupsByModel := make(map[string][]string)
	for _, groupName := range validGroups {
		models, err := fetchRoutingNewAPIUserModels(ctx, binding, credentials, groupName)
		if err != nil {
			if routingUpstreamAuthError(err) {
				return routingPricingResponse{}, err
			}
			groupErrors[groupName] = routingCleanCredentialErrorMessage(err.Error(), credentials)
			continue
		}
		if len(models) == 0 {
			groupErrors[groupName] = fmt.Sprintf("newapi account returned no models for bound group %s", groupName)
			continue
		}
		modelsByGroup[groupName] = models
		missingPrice := ""
		for _, modelName := range models {
			if _, priced := catalogByModel[modelName]; !priced {
				missingPrice = modelName
				break
			}
		}
		if missingPrice != "" {
			if !catalog.CatalogAuthenticated {
				groupErrors[groupName] = fmt.Sprintf(
					"newapi pricing directory has no reliable price for account model %s in group %s; catalog scope may be anonymous, configure upstream pricing.requireAuth=true",
					missingPrice, groupName,
				)
			} else {
				groupErrors[groupName] = fmt.Sprintf(
					"newapi authenticated pricing directory has no reliable price for account model %s in group %s",
					missingPrice, groupName,
				)
			}
			continue
		}
		for _, modelName := range models {
			groupsByModel[modelName] = append(groupsByModel[modelName], groupName)
		}
	}

	modelNames := make([]string, 0, len(groupsByModel))
	for modelName := range groupsByModel {
		modelNames = append(modelNames, modelName)
	}
	sort.Strings(modelNames)
	result.Data = make([]routingPricingItem, 0, len(modelNames))
	for _, modelName := range modelNames {
		item := catalogByModel[modelName]
		item.EnableGroups = append([]string(nil), groupsByModel[modelName]...)
		item.PricingVersion = ""
		result.Data = append(result.Data, item)
	}
	result.ObservedTime = catalog.ObservedTime
	result.EffectiveTime = catalog.EffectiveTime
	result.ExpiresTime = catalog.ExpiresTime
	result.PricingVersion = routingCostContentVersion("newapi-account-pricing", struct {
		CatalogVersion string               `json:"catalog_version"`
		QuotaPerUnit   float64              `json:"quota_per_unit"`
		GroupRatio     map[string]float64   `json:"group_ratio"`
		ModelsByGroup  map[string][]string  `json:"models_by_group"`
		Data           []routingPricingItem `json:"data"`
	}{
		CatalogVersion: strings.TrimSpace(catalog.PricingVersion),
		QuotaPerUnit:   upstreamQuotaPerUnit,
		GroupRatio:     groupRatio,
		ModelsByGroup:  modelsByGroup,
		Data:           result.Data,
	})
	return result, nil
}

func fetchRoutingNewAPIUserGroups(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
) (routingNewAPIUserGroupsResponse, error) {
	ctx, err := withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
	if err != nil {
		return routingNewAPIUserGroupsResponse{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, strings.TrimRight(binding.BaseURL, "/")+"/api/user/self/groups", nil,
	)
	if err != nil {
		return routingNewAPIUserGroupsResponse{}, err
	}
	applyRoutingAuthHeaders(request, binding, credentials)
	response, err := routingCostHTTPDoer.Do(request)
	if err != nil {
		return routingNewAPIUserGroupsResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return routingNewAPIUserGroupsResponse{}, routingAuthErrorf("user groups endpoint returned %s", response.Status)
	}
	if response.StatusCode != http.StatusOK {
		return routingNewAPIUserGroupsResponse{}, fmt.Errorf("user groups endpoint returned %s", response.Status)
	}
	body, err := readRoutingCostJSON(response, defaultRoutingJSONLimits)
	if err != nil {
		return routingNewAPIUserGroupsResponse{}, err
	}
	var payload routingNewAPIUserGroupsResponse
	if err := common.Unmarshal(body, &payload); err != nil {
		return routingNewAPIUserGroupsResponse{}, errors.New("invalid newapi user groups response")
	}
	if !payload.Success {
		return routingNewAPIUserGroupsResponse{}, routingNewAPISuccessFalseError(
			response,
			payload.Message,
			"user groups endpoint returned success=false",
			credentials,
		)
	}
	if payload.Data == nil {
		return routingNewAPIUserGroupsResponse{}, errors.New("newapi user groups response is missing data")
	}
	return payload, nil
}

func fetchRoutingNewAPIUserModels(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	groupName string,
) ([]string, error) {
	ctx, err := withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(binding.BaseURL, "/") + "/api/user/models"
	if strings.TrimSpace(groupName) != "" {
		endpoint += "?" + url.Values{"group": []string{strings.TrimSpace(groupName)}}.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	applyRoutingAuthHeaders(request, binding, credentials)
	response, err := routingCostHTTPDoer.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, routingAuthErrorf("user models endpoint returned %s", response.Status)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user models endpoint returned %s", response.Status)
	}
	body, err := readRoutingCostJSON(response, defaultRoutingJSONLimits)
	if err != nil {
		return nil, err
	}
	var payload routingNewAPIUserModelsResponse
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("invalid newapi user models response")
	}
	if !payload.Success {
		return nil, routingNewAPISuccessFalseError(
			response,
			payload.Message,
			"user models endpoint returned success=false",
			credentials,
		)
	}
	modelSet := make(map[string]struct{}, len(payload.Data))
	for _, modelName := range payload.Data {
		modelName = strings.TrimSpace(modelName)
		if modelName != "" {
			modelSet[modelName] = struct{}{}
		}
	}
	models := make([]string, 0, len(modelSet))
	for modelName := range modelSet {
		models = append(models, modelName)
	}
	sort.Strings(models)
	return models, nil
}

func fetchRoutingNewAPIGatewayModels(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
) (map[string]struct{}, error) {
	credentials = credentials.ForUpstream(model.RoutingUpstreamTypeNewAPI)
	gatewayAPIKey := strings.TrimSpace(credentials.GatewayAPIKey)
	if gatewayAPIKey == "" {
		return nil, errors.New("newapi gateway API key is required to verify serving models")
	}
	ctx, err := withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(binding.BaseURL, "/")+"/v1/models",
		nil,
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+gatewayAPIKey)
	response, err := routingCostHTTPDoer.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, errors.New("newapi gateway API key was rejected by models endpoint")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("newapi gateway models endpoint returned %s", response.Status)
	}
	body, err := readRoutingCostJSON(response, defaultRoutingJSONLimits)
	if err != nil {
		return nil, err
	}
	var payload routingNewAPIGatewayModelsResponse
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("invalid newapi gateway models response")
	}
	if payload.Success == nil || !*payload.Success {
		message := routingCleanCredentialErrorMessage(payload.Message, credentials)
		if message == "" {
			message = "newapi gateway models endpoint returned success=false"
		}
		return nil, errors.New(message)
	}
	models := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		modelName := strings.TrimSpace(item.ID)
		if modelName != "" {
			models[modelName] = struct{}{}
		}
	}
	if len(models) == 0 {
		return nil, errors.New("newapi gateway models endpoint returned no serving models")
	}
	return models, nil
}

type routingAuthError struct {
	message string
}

func routingNewAPISuccessFalseError(
	response *http.Response,
	message string,
	fallback string,
	credentials model.RoutingCredentials,
) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = fallback
	}
	message = routingCleanCredentialErrorMessage(message, credentials)
	if response != nil && strings.TrimSpace(response.Header.Get("Auth-Version")) != "" {
		return errors.New(message)
	}
	if routingNewAPIMessageIndicatesAuthFailure(message) {
		return routingAuthErrorf("%s", message)
	}
	return errors.New(message)
}

func routingNewAPIMessageIndicatesAuthFailure(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	for _, marker := range []string{
		"unauthorized",
		"not logged in",
		"invalid access token",
		"invalid token",
		"access token invalid",
		"new-api-user",
		"user has been banned",
		"insufficient privilege",
		"未登录",
		"access token 无效",
		"access token 無效",
		"用户已被封禁",
		"使用者已被封禁",
		"用户信息无效",
		"使用者資訊無效",
		"权限不足",
		"權限不足",
		"未提供 new-api-user",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (err routingAuthError) Error() string {
	return err.message
}

func routingAuthErrorf(format string, args ...any) error {
	return routingAuthError{message: fmt.Sprintf(format, args...)}
}

func routingUpstreamAuthError(err error) bool {
	var authErr routingAuthError
	return errors.As(err, &authErr)
}

type routingSafeError struct {
	cause   error
	message string
}

func (err routingSafeError) Error() string { return err.message }

func (err routingSafeError) Unwrap() error { return err.cause }

func routingSafeErrorWithCredentials(err error, credentials model.RoutingCredentials) error {
	if err == nil {
		return nil
	}
	message := common.SanitizeErrorMessage(err.Error(), routingCredentialSecrets(credentials)...)
	if message == "" {
		message = "routing upstream request failed"
	}
	return routingSafeError{cause: err, message: message}
}

func fetchRoutingUpstreamBalance(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) error {
	balance, known, err := fetchRoutingUpstreamBalanceValue(ctx, binding, credentials)
	if err != nil || !known {
		return err
	}
	return persistRoutingBalance(ctx, binding, balance, common.GetTimestamp())
}

func fetchRoutingUpstreamBalanceValue(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
) (float64, bool, error) {
	upstreamQuotaPerUnit, err := fetchRoutingNewAPIQuotaPerUnit(ctx, binding, credentials)
	if err != nil {
		return 0, false, err
	}
	return fetchRoutingNewAPIBalanceValue(ctx, binding, credentials, upstreamQuotaPerUnit)
}

func fetchRoutingNewAPIBalanceValue(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	upstreamQuotaPerUnit float64,
) (float64, bool, error) {
	if !routingCostNonNegativeFinite(upstreamQuotaPerUnit) || upstreamQuotaPerUnit <= 0 {
		return 0, false, errors.New("newapi status response contains an invalid quota_per_unit")
	}
	ctx, err := withRoutingCostBindingEgressPolicy(ctx, binding, credentials)
	if err != nil {
		return 0, false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(binding.BaseURL, "/")+"/api/user/self", nil)
	if err != nil {
		return 0, false, err
	}
	applyRoutingAuthHeaders(request, binding, credentials)

	response, err := routingCostHTTPDoer.Do(request)
	if err != nil {
		return 0, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return 0, false, routingAuthErrorf("user self endpoint returned %s", response.Status)
	}
	if response.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("user self endpoint returned %s", response.Status)
	}

	body, err := readRoutingCostJSON(response, defaultRoutingJSONLimits)
	if err != nil {
		return 0, false, err
	}
	var payload routingUserSelfResponse
	if err = common.Unmarshal(body, &payload); err != nil {
		return 0, false, errors.New("invalid routing user response")
	}
	if !payload.Success {
		return 0, false, routingNewAPISuccessFalseError(
			response,
			payload.Message,
			"user self endpoint returned success=false",
			credentials,
		)
	}

	// NewAPI quota is the remaining balance; used_quota is cumulative usage and
	// must not be subtracted from it a second time.
	if payload.Data.Quota == nil {
		return 0, false, nil
	}
	balance := *payload.Data.Quota / upstreamQuotaPerUnit
	if math.IsNaN(balance) || math.IsInf(balance, 0) {
		return 0, false, errors.New("invalid routing upstream balance")
	}
	return balance, true, nil
}

func withRoutingCostBindingEgressPolicy(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
) (context.Context, error) {
	allowedPrivateCIDRs, err := binding.GetEgressAllowedPrivateCIDRs()
	if err != nil {
		return nil, errors.New("invalid routing cost egress policy")
	}
	return service.WithRoutingCostEgressPolicy(ctx, allowedPrivateCIDRs, credentials.CustomCAPEM)
}

func persistRoutingBalance(ctx context.Context, binding model.RoutingChannelBinding, balance float64, updatedTime int64) error {
	if err := smartRoutingRuntimeStateMu.LockContext(ctx); err != nil {
		return err
	}
	defer smartRoutingRuntimeStateMu.Unlock()

	applied, err := model.UpdateRoutingChannelBalanceForBindingContext(ctx, binding, balance, updatedTime)
	if err != nil || !applied {
		return err
	}
	routinghotcache.SetBalance(binding.ChannelID, routinghotcache.BalanceSnapshot{
		Known:       true,
		Balance:     balance,
		UpdatedUnix: updatedTime,
	})
	return nil
}

func applyRoutingAuthHeaders(request *http.Request, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) {
	if token := routingBearerToken(binding.UpstreamType, credentials); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if binding.NewAPIUserID != nil && *binding.NewAPIUserID > 0 {
		request.Header.Set("New-Api-User", fmt.Sprintf("%d", *binding.NewAPIUserID))
	}
}

func routingCleanUpstreamErrorMessage(message string) string {
	message = common.SanitizeErrorMessage(message)
	if message == "" {
		return "upstream auth failed"
	}
	return message
}

func routingBearerToken(upstreamType string, credentials model.RoutingCredentials) string {
	credentials = credentials.ForUpstream(upstreamType)
	switch strings.ToLower(strings.TrimSpace(upstreamType)) {
	case model.RoutingUpstreamTypeNewAPI:
		if credentials.NewAPIAccessToken != "" {
			return credentials.NewAPIAccessToken
		}
	case model.RoutingUpstreamTypeSub2API:
		if credentials.Sub2APIToken != "" {
			return credentials.Sub2APIToken
		}
	}
	return ""
}

func routingPricingItemServesGroup(enableGroups []string, group string) bool {
	if len(enableGroups) == 0 {
		return true
	}
	for _, enabledGroup := range enableGroups {
		if enabledGroup == "all" || enabledGroup == group {
			return true
		}
	}
	return false
}

const routingPricingGroupOutputLimit = 500

func routingPricingGroups(payload routingPricingResponse) ([]string, int) {
	groupSet := map[string]struct{}{}
	for group := range payload.GroupRatio {
		groupSet[group] = struct{}{}
	}
	for group := range payload.UsableGroup {
		groupSet[group] = struct{}{}
	}
	for _, item := range payload.Data {
		for _, group := range item.EnableGroups {
			if group != "" && group != "all" {
				groupSet[group] = struct{}{}
			}
		}
	}
	groups := make([]string, 0, len(groupSet))
	for group := range groupSet {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	total := len(groups)
	if len(groups) > routingPricingGroupOutputLimit {
		groups = groups[:routingPricingGroupOutputLimit]
	}
	return groups, total
}

func routingBreakerSnapshotToModel(snapshot routingbreaker.Snapshot) model.RoutingBreakerState {
	state := model.RoutingBreakerState{
		ChannelID:           snapshot.Key.ChannelID,
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
				ChannelID:   state.ChannelID,
				APIKeyIndex: state.APIKeyIndex,
				Model:       state.ModelName,
				Group:       state.Group,
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

type routingAgentHandler struct{}

func (routingAgentHandler) Type() string { return model.SystemTaskTypeRoutingAgent }

func (routingAgentHandler) Enabled() bool {
	setting := smart_routing_setting.GetSetting()
	return setting.Enabled && setting.AgentEnabled
}

func (routingAgentHandler) Interval() time.Duration { return time.Hour }

func (routingAgentHandler) NewPayload() any { return nil }

func (routingAgentHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, map[string]any{
		"analyzed": false,
		"reason":   "routing agent is read-only until v2 providers are configured",
	}, nil)
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
