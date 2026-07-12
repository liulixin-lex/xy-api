package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
				_, err = refreshRoutingHotcacheFromDB(ctx, setting)
			}
			routinghotcache.Prune(common.GetTimestamp(), int64(setting.SnapshotStaleSec))
			return err
		},
		flush: func(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) error {
			syncRoutingBreakerConfigFromSetting(setting)
			_, err := flushRoutingRuntimeState(ctx, setting)
			return err
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
	return constant.UpdateTask && model.HasUnfinishedMidjourneyTasks()
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
	return constant.UpdateTask && model.HasUnfinishedSyncTasks()
}

func (asyncTaskPollHandler) Interval() time.Duration { return 15 * time.Second }

func (asyncTaskPollHandler) NewPayload() any { return nil }

func (asyncTaskPollHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	summary := service.RunTaskPollingOnce(ctx, service.NewSystemTaskProgressReporter(task, runnerID))
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
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
	summary, err := runRoutingCostSyncTask(ctx)
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, summary, err)
		return
	}
	finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusSucceeded, summary, nil)
}

type routingPricingResponse struct {
	Success        bool                 `json:"success"`
	Data           []routingPricingItem `json:"data"`
	GroupRatio     map[string]float64   `json:"group_ratio"`
	UsableGroup    map[string]string    `json:"usable_group"`
	PricingVersion string               `json:"pricing_version"`
	ObservedTime   int64                `json:"observed_time"`
	EffectiveTime  int64                `json:"effective_time"`
	ExpiresTime    int64                `json:"expires_time"`
	Message        string               `json:"message"`
}

type routingUserSelfResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Quota     float64 `json:"quota"`
		UsedQuota float64 `json:"used_quota"`
	} `json:"data"`
	Message string `json:"message"`
}

type routingPricingItem struct {
	ModelName            string          `json:"model_name"`
	QuotaType            int             `json:"quota_type"`
	ModelRatio           float64         `json:"model_ratio"`
	ModelPrice           float64         `json:"model_price"`
	CompletionRatio      float64         `json:"completion_ratio"`
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
	Binding     model.RoutingChannelBinding
	Credentials model.RoutingCredentials
}

type routingCostAccountGroup struct {
	Identity routingCostAccountIdentity
	Sources  []routingCostBindingSource
}

type routingCostAccountPayload struct {
	SourceType       string
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
	acceptedBindingIDs := make(map[int]int)
	fencedBindingIDs := make(map[int]int)
	rejectedChannels := make(map[int]struct{})
	persistedMetricsByChannel := make(map[int][]model.RoutingChannelMetric)
	persistedBreakerCounts := make(map[int]int)
	defer func() {
		persistedMetrics := make([]model.RoutingChannelMetric, 0)
		for channelID, metrics := range persistedMetricsByChannel {
			if _, accepted := acceptedBindingIDs[channelID]; accepted {
				persistedMetrics = append(persistedMetrics, metrics...)
			}
		}
		routinghotcache.ApplyMetricDeltas(persistedMetrics, setting.MetricBucketSec)

		metricCount, breakerCount, verifyErr := finalizeFlushedRoutingBindingState(
			ctx,
			acceptedBindingIDs,
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
		expectedBindingID := fencedBindingIDs[metric.ChannelID]
		bindingID, stateAccepted, err := eligibility.UpsertRoutingChannelMetricForBindingContext(ctx, &metric, expectedBindingID)
		if err != nil {
			routingmetrics.RequeueSnapshots(validMetrics[i:])
			if ctxErr := ctx.Err(); ctxErr != nil {
				return summary, ctxErr
			}
			return summary, err
		}
		if bindingID == 0 {
			clearRoutingRuntimeChannelState(metric.ChannelID)
			rejectedChannels[metric.ChannelID] = struct{}{}
			delete(acceptedBindingIDs, metric.ChannelID)
			delete(persistedMetricsByChannel, metric.ChannelID)
			delete(persistedBreakerCounts, metric.ChannelID)
			continue
		}
		if expectedBindingID == 0 {
			fencedBindingIDs[metric.ChannelID] = bindingID
		} else if bindingID != expectedBindingID {
			clearRoutingRuntimeChannelState(metric.ChannelID)
			rejectedChannels[metric.ChannelID] = struct{}{}
			delete(acceptedBindingIDs, metric.ChannelID)
			delete(persistedMetricsByChannel, metric.ChannelID)
			delete(persistedBreakerCounts, metric.ChannelID)
			continue
		}
		if !stateAccepted {
			clearRoutingRuntimeChannelState(metric.ChannelID)
			rejectedChannels[metric.ChannelID] = struct{}{}
			delete(acceptedBindingIDs, metric.ChannelID)
			delete(persistedMetricsByChannel, metric.ChannelID)
			delete(persistedBreakerCounts, metric.ChannelID)
			continue
		}
		matches, verifyErr := model.RoutingChannelBindingMatchesContext(ctx, metric.ChannelID, bindingID)
		if verifyErr != nil {
			if acceptedBindingID, accepted := acceptedBindingIDs[metric.ChannelID]; accepted && acceptedBindingID != bindingID {
				clearRoutingRuntimeChannelState(metric.ChannelID)
				rejectedChannels[metric.ChannelID] = struct{}{}
				delete(acceptedBindingIDs, metric.ChannelID)
				delete(persistedMetricsByChannel, metric.ChannelID)
				delete(persistedBreakerCounts, metric.ChannelID)
			} else {
				acceptedBindingIDs[metric.ChannelID] = bindingID
				persistedMetricsByChannel[metric.ChannelID] = append(persistedMetricsByChannel[metric.ChannelID], metric)
			}
			routingmetrics.RequeueSnapshots(validMetrics[i+1:])
			return summary, verifyErr
		}
		if !matches {
			clearRoutingRuntimeChannelState(metric.ChannelID)
			rejectedChannels[metric.ChannelID] = struct{}{}
			delete(acceptedBindingIDs, metric.ChannelID)
			delete(persistedMetricsByChannel, metric.ChannelID)
			delete(persistedBreakerCounts, metric.ChannelID)
			continue
		}
		if acceptedBindingID, accepted := acceptedBindingIDs[metric.ChannelID]; accepted && acceptedBindingID != bindingID {
			clearRoutingRuntimeChannelState(metric.ChannelID)
			rejectedChannels[metric.ChannelID] = struct{}{}
			delete(acceptedBindingIDs, metric.ChannelID)
			delete(persistedMetricsByChannel, metric.ChannelID)
			delete(persistedBreakerCounts, metric.ChannelID)
			continue
		}
		acceptedBindingIDs[metric.ChannelID] = bindingID
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
		expectedBindingID := fencedBindingIDs[snapshot.Key.ChannelID]
		bindingID, stateAccepted, err := eligibility.UpsertRoutingBreakerStateForBindingContext(ctx, &state, expectedBindingID)
		if err != nil {
			routingbreaker.RequeueDirtySnapshots(validBreakers[i:])
			if ctxErr := ctx.Err(); ctxErr != nil {
				return summary, ctxErr
			}
			return summary, err
		}
		if bindingID == 0 {
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
			delete(acceptedBindingIDs, snapshot.Key.ChannelID)
			delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
			delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			continue
		}
		if expectedBindingID == 0 {
			fencedBindingIDs[snapshot.Key.ChannelID] = bindingID
		} else if bindingID != expectedBindingID {
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
			delete(acceptedBindingIDs, snapshot.Key.ChannelID)
			delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
			delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			continue
		}
		if !stateAccepted {
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
			delete(acceptedBindingIDs, snapshot.Key.ChannelID)
			delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
			delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			continue
		}
		matches, verifyErr := model.RoutingChannelBindingMatchesContext(ctx, snapshot.Key.ChannelID, bindingID)
		if verifyErr != nil {
			if acceptedBindingID, accepted := acceptedBindingIDs[snapshot.Key.ChannelID]; accepted && acceptedBindingID != bindingID {
				clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
				rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
				delete(acceptedBindingIDs, snapshot.Key.ChannelID)
				delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
				delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			} else {
				acceptedBindingIDs[snapshot.Key.ChannelID] = bindingID
				persistedBreakerCounts[snapshot.Key.ChannelID]++
			}
			routingbreaker.RequeueDirtySnapshots(validBreakers[i+1:])
			return summary, verifyErr
		}
		if !matches {
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
			delete(acceptedBindingIDs, snapshot.Key.ChannelID)
			delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
			delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			continue
		}
		if acceptedBindingID, accepted := acceptedBindingIDs[snapshot.Key.ChannelID]; accepted && acceptedBindingID != bindingID {
			clearRoutingRuntimeChannelState(snapshot.Key.ChannelID)
			rejectedChannels[snapshot.Key.ChannelID] = struct{}{}
			delete(acceptedBindingIDs, snapshot.Key.ChannelID)
			delete(persistedMetricsByChannel, snapshot.Key.ChannelID)
			delete(persistedBreakerCounts, snapshot.Key.ChannelID)
			continue
		}
		acceptedBindingIDs[snapshot.Key.ChannelID] = bindingID
		persistedBreakerCounts[snapshot.Key.ChannelID]++
	}

	now := common.GetTimestamp()
	const (
		retentionIntervalSeconds int64 = 6 * 60 * 60
		secondsPerDay            int64 = 24 * 60 * 60
	)
	if setting.RetentionDays > 0 && now-smartRoutingRetentionLast.Load() >= retentionIntervalSeconds {
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
		smartRoutingRetentionLast.Store(now)
	}
	return summary, nil
}

func clearRoutingRuntimeChannelState(channelID int) {
	routingmetrics.ClearChannel(channelID)
	routingbreaker.ClearDefaultChannelWithCache(channelID, routinghotcache.ClearChannel)
}

func finalizeFlushedRoutingBindingState(
	ctx context.Context,
	acceptedBindingIDs map[int]int,
	persistedMetricsByChannel map[int][]model.RoutingChannelMetric,
	persistedBreakerCounts map[int]int,
) (int, int, error) {
	for channelID, bindingID := range acceptedBindingIDs {
		matches, err := model.RoutingChannelBindingMatchesContext(ctx, channelID, bindingID)
		if err != nil {
			for acceptedChannelID := range acceptedBindingIDs {
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
		if _, accepted := acceptedBindingIDs[channelID]; accepted {
			metricCount += len(metrics)
		}
	}
	breakerCount := 0
	for channelID, count := range persistedBreakerCounts {
		if _, accepted := acceptedBindingIDs[channelID]; accepted {
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
		"bindings":         0,
		"accounts":         0,
		"snapshots":        0,
		"versions_created": 0,
		"metrics":          0,
		"breakers":         0,
		"loaded_breakers":  0,
		"errors":           0,
		"partial_accounts": 0,
		"skipped_backoff":  0,
		"stale_bindings":   0,
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
	skippedBackoff := 0
	for _, binding := range bindings {
		if binding.SyncBackoffUntil > now {
			skippedBackoff++
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
	for _, binding := range eligibleBindings {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		credentials, err := binding.GetCredentials()
		if err != nil {
			stale, updateErr := recordRoutingCostSyncFailure(ctx, binding, credentials, err, deps)
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
		identity, err := routingUpstreamAccountIdentity(binding, credentials)
		if err != nil {
			stale, updateErr := recordRoutingCostSyncFailure(ctx, binding, credentials, err, deps)
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
		group := accountGroups[identity.AccountKey]
		if group == nil {
			group = &routingCostAccountGroup{Identity: identity}
			accountGroups[identity.AccountKey] = group
		}
		group.Sources = append(group.Sources, routingCostBindingSource{Binding: binding, Credentials: credentials})
	}

	accountKeys := make([]string, 0, len(accountGroups))
	for accountKey := range accountGroups {
		accountKeys = append(accountKeys, accountKey)
	}
	sort.Strings(accountKeys)
	summary["accounts"] = len(accountKeys)
	partialAccounts := 0
	for _, accountKey := range accountKeys {
		group := accountGroups[accountKey]
		representative := group.Sources[0]
		payload, err := fetchRoutingCostAccountPayload(ctx, representative.Binding, representative.Credentials, setting)
		if err != nil {
			if ctx.Err() != nil {
				return summary, ctx.Err()
			}
			safeErr := routingSafeErrorWithCredentials(err, representative.Credentials)
			_, accountErr := model.UpsertRoutingUpstreamAccountContext(ctx, model.RoutingUpstreamAccountSpec{
				SourceType:      routingCostConnectorSourceType(representative.Binding.UpstreamType),
				StableIdentity:  group.Identity.StableIdentity,
				MaskedIdentity:  group.Identity.MaskedIdentity,
				Status:          model.RoutingUpstreamAccountStatusDegraded,
				PreserveBalance: true,
				LastSyncStatus:  model.RoutingUpstreamSyncStatusFailed,
				LastSyncError:   safeErr.Error(),
			})
			if accountErr != nil {
				return summary, fmt.Errorf("persist routing upstream account failure: %w", accountErr)
			}
			for _, source := range group.Sources {
				stale, updateErr := recordRoutingCostSyncFailure(ctx, source.Binding, source.Credentials, err, deps)
				if updateErr != nil {
					return summary, updateErr
				}
				if stale {
					staleBindings++
				} else {
					syncErrors++
				}
			}
			continue
		}
		if payload.SyncStatus == model.RoutingUpstreamSyncStatusPartial {
			partialAccounts++
		}
		accountMappingPartial := false
		accountMappingError := ""

		for _, source := range group.Sources {
			writes, mapErr := routingCostVersionWritesForBinding(ctx, source.Binding, payload)
			if mapErr != nil {
				accountMappingPartial = true
				if accountMappingError == "" {
					accountMappingError = common.SanitizeErrorMessage(mapErr.Error(), routingCredentialSecrets(source.Credentials)...)
				}
				stale, updateErr := recordRoutingCostSyncFailure(ctx, source.Binding, source.Credentials, mapErr, deps)
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
			accountStatus := model.RoutingUpstreamAccountStatusActive
			if payload.SyncStatus == model.RoutingUpstreamSyncStatusPartial {
				accountStatus = model.RoutingUpstreamAccountStatusDegraded
			}
			accountSpec := model.RoutingUpstreamAccountSpec{
				SourceType:       routingCostConnectorSourceType(source.Binding.UpstreamType),
				StableIdentity:   group.Identity.StableIdentity,
				MaskedIdentity:   group.Identity.MaskedIdentity,
				Status:           accountStatus,
				PreserveBalance:  !payload.BalanceKnown,
				BalanceKnown:     payload.BalanceKnown,
				Balance:          payload.Balance,
				BalanceUpdatedAt: payload.BalanceUpdatedAt,
				LastSyncStatus:   payload.SyncStatus,
				LastSyncError:    payload.SyncError,
			}

			if err := smartRoutingRuntimeStateMu.LockContext(ctx); err != nil {
				return summary, err
			}
			persisted, persistErr := model.CompleteRoutingCostVersionSyncContext(ctx, source.Binding, accountSpec, writes)
			if persistErr == nil {
				routinghotcache.LoadCostSnapshots(persisted.Latest)
				if payload.BalanceKnown {
					routinghotcache.SetBalance(source.Binding.ChannelID, routinghotcache.BalanceSnapshot{
						Known:       true,
						Balance:     payload.Balance,
						UpdatedUnix: payload.BalanceUpdatedAt,
					})
				}
			}
			smartRoutingRuntimeStateMu.Unlock()
			if persistErr != nil {
				if errors.Is(persistErr, model.ErrRoutingBindingChanged) {
					staleBindings++
					continue
				}
				if ctxErr := ctx.Err(); ctxErr != nil {
					return summary, ctxErr
				}
				return summary, fmt.Errorf("persist routing cost versions: %w", persistErr)
			}
			syncedSnapshots += len(writes)
			for _, version := range persisted.Versions {
				if version.Created {
					createdVersions++
				}
			}
		}
		if accountMappingPartial {
			if payload.SyncStatus != model.RoutingUpstreamSyncStatusPartial {
				partialAccounts++
			}
			if _, err := model.UpsertRoutingUpstreamAccountContext(ctx, model.RoutingUpstreamAccountSpec{
				SourceType:      routingCostConnectorSourceType(representative.Binding.UpstreamType),
				StableIdentity:  group.Identity.StableIdentity,
				MaskedIdentity:  group.Identity.MaskedIdentity,
				Status:          model.RoutingUpstreamAccountStatusDegraded,
				PreserveBalance: true,
				LastSyncStatus:  model.RoutingUpstreamSyncStatusPartial,
				LastSyncError:   accountMappingError,
			}); err != nil {
				return summary, fmt.Errorf("persist routing upstream account partial status: %w", err)
			}
		}
	}
	summary["snapshots"] = syncedSnapshots
	summary["versions_created"] = createdVersions
	summary["errors"] = syncErrors
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
) (bool, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
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
	if err := model.UpdateRoutingCostSyncFailureContext(ctx, binding, failureCount, backoffUntil, message); err != nil {
		if errors.Is(err, model.ErrRoutingBindingChanged) {
			return true, nil
		}
		return false, fmt.Errorf("persist routing cost sync failure state: %w", err)
	}
	return false, nil
}

func routingUpstreamAccountIdentity(binding model.RoutingChannelBinding, credentials model.RoutingCredentials) (routingCostAccountIdentity, error) {
	parsed, err := url.Parse(strings.TrimSpace(binding.BaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return routingCostAccountIdentity{}, errors.New("invalid routing upstream account base URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	canonicalBase := parsed.String()
	hostLabel := strings.ToLower(parsed.Hostname())
	if hostLabel == "" {
		hostLabel = "upstream"
	}

	sourceType := routingCostConnectorSourceType(binding.UpstreamType)
	identityParts := []string{"routing-upstream-identity:v1", sourceType, canonicalBase}
	maskedIdentity := hostLabel
	switch sourceType {
	case model.RoutingUpstreamTypeNewAPI:
		if binding.NewAPIUserID != nil && *binding.NewAPIUserID > 0 {
			identityParts = append(identityParts, fmt.Sprintf("user:%d", *binding.NewAPIUserID))
			maskedIdentity += fmt.Sprintf(" / user %d", *binding.NewAPIUserID)
		} else {
			token := routingBearerToken(credentials)
			identityParts = append(identityParts, "token:"+token)
			maskedIdentity += " / " + maskRoutingToken(token)
		}
	case model.RoutingUpstreamTypeSub2API:
		email := strings.ToLower(strings.TrimSpace(credentials.Sub2APIEmail))
		if email != "" {
			identityParts = append(identityParts, "email:"+email)
			maskedIdentity += " / " + maskRoutingEmail(email)
		} else {
			token := strings.TrimSpace(credentials.Sub2APIToken)
			if token == "" {
				token = strings.TrimSpace(credentials.GatewayAPIKey)
			}
			identityParts = append(identityParts, "token:"+token)
			maskedIdentity += " / " + maskRoutingToken(token)
		}
	}
	identityHash := sha256.Sum256([]byte(strings.Join(identityParts, "\x00")))
	stableIdentity := fmt.Sprintf("%x", identityHash[:])
	return routingCostAccountIdentity{
		AccountKey:     model.RoutingUpstreamAccountKey(sourceType, stableIdentity),
		StableIdentity: stableIdentity,
		MaskedIdentity: maskedIdentity,
	}, nil
}

func routingCostConnectorSourceType(upstreamType string) string {
	if upstreamType == model.RoutingUpstreamTypeSub2API {
		return model.RoutingUpstreamTypeSub2API
	}
	return model.RoutingUpstreamTypeNewAPI
}

func fetchRoutingCostAccountPayload(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
	setting smart_routing_setting.SmartRoutingSetting,
) (routingCostAccountPayload, error) {
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
		balance, balanceKnown, balanceErr := fetchRoutingUpstreamBalanceValue(ctx, binding, credentials)
		if balanceErr != nil && routingUpstreamAuthError(balanceErr) {
			return routingCostAccountPayload{}, balanceErr
		}
		pricing, err := fetchRoutingNewAPIPricingPayload(ctx, binding, credentials)
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
		pricing, err := fetchRoutingSub2APIAccountPricing(ctx, binding, credentials)
		if err != nil {
			return routingCostAccountPayload{}, err
		}
		payload.Sub2API = &pricing
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
	groupRatio := 1.0
	groupRatioKnown := false
	if ratio, ok := pricingPayload.GroupRatio[binding.UpstreamGroup]; ok {
		if !routingCostNonNegativeFinite(ratio) || ratio <= 0 {
			return nil, errors.New("newapi returned an invalid group ratio")
		}
		groupRatio = ratio
		groupRatioKnown = true
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

		pricing, confidence, confidenceScore, err := routingNewAPINormalizedPricing(item, groupRatio, groupRatioKnown)
		if err != nil {
			return nil, fmt.Errorf("invalid newapi price for model %s: %w", upstreamModel, err)
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
	return writes, nil
}

func routingNewAPINormalizedPricing(
	item routingPricingItem,
	groupRatio float64,
	groupRatioKnown bool,
) (model.RoutingNormalizedPricing, string, float64, error) {
	if item.QuotaType < 0 || item.QuotaType > 1 || !routingCostNonNegativeFinite(item.ModelRatio) ||
		!routingCostNonNegativeFinite(item.ModelPrice) || !routingCostNonNegativeFinite(item.CompletionRatio) {
		return model.RoutingNormalizedPricing{}, "", 0, model.ErrRoutingCostV2Invalid
	}
	for _, value := range []*float64{
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
	completionRatio := item.CompletionRatio
	if completionRatio == 0 {
		completionRatio = 1
	}
	billingMode := strings.ToLower(strings.TrimSpace(item.BillingMode))
	if billingMode == "" {
		if item.QuotaType == 1 {
			billingMode = "per_request"
		} else {
			billingMode = "token"
		}
	}
	perRequestCost := item.ModelPrice
	if item.PerRequestPrice != nil {
		perRequestCost = *item.PerRequestPrice
	}
	inputCostPerMillion := item.ModelRatio * 1_000_000 / common.QuotaPerUnit
	outputCostPerMillion := inputCostPerMillion * completionRatio
	pricing := model.RoutingNormalizedPricing{
		QuotaType:         item.QuotaType,
		BillingMode:       billingMode,
		Currency:          "USD",
		GroupRatio:        routingCostFloatPointer(groupRatio),
		CompletionRatio:   routingCostFloatPointer(completionRatio),
		Tiers:             item.Tiers,
		BillingExpression: strings.TrimSpace(item.BillingExpr),
	}
	if item.ModelRatio > 0 {
		pricing.BaseRatio = routingCostFloatPointer(item.ModelRatio)
		pricing.InputCostPerMillion = routingCostFloatPointer(inputCostPerMillion)
		pricing.OutputCostPerMillion = routingCostFloatPointer(outputCostPerMillion)
	}
	if item.ModelPrice > 0 {
		pricing.ModelPrice = routingCostFloatPointer(item.ModelPrice)
	}
	if perRequestCost > 0 {
		pricing.PerRequestCost = routingCostFloatPointer(perRequestCost)
	}
	if item.CacheRatio != nil && inputCostPerMillion > 0 {
		pricing.CacheReadCostPerMillion = routingCostFloatPointer(inputCostPerMillion * *item.CacheRatio)
	}
	if item.CreateCacheRatio != nil && inputCostPerMillion > 0 {
		pricing.CacheWriteCostPerMillion = routingCostFloatPointer(inputCostPerMillion * *item.CreateCacheRatio)
	}
	if item.AudioRatio != nil && inputCostPerMillion > 0 {
		pricing.AudioInputCostPerMillion = routingCostFloatPointer(inputCostPerMillion * *item.AudioRatio)
	}
	if item.AudioCompletionRatio != nil && inputCostPerMillion > 0 {
		pricing.AudioOutputCostPerMillion = routingCostFloatPointer(inputCostPerMillion * *item.AudioCompletionRatio)
	}
	extras := map[string]any{}
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
	known := item.ModelRatio > 0 || perRequestCost > 0 || pricing.BillingExpression != "" ||
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
	groupRatio := routingSub2APIGroupRatio(groupInfo)
	if ratio, ok := pricingPayload.Rates[binding.UpstreamGroup]; ok {
		if !routingCostNonNegativeFinite(ratio) || ratio <= 0 {
			return nil, errors.New("sub2api returned an invalid group ratio")
		}
		groupRatio = ratio
		groupFound = true
	}
	if groupRatio <= 0 {
		groupRatio = 1
	}
	channels := append([]routingSub2APIChannel(nil), pricingPayload.Channels...)
	sort.SliceStable(channels, func(left int, right int) bool {
		return strings.Join(routingSub2APIChannelModels(channels[left]), "\x00") <
			strings.Join(routingSub2APIChannelModels(channels[right]), "\x00")
	})
	writes := make([]model.RoutingCostSnapshotVersionWrite, 0, len(channels))
	seenModels := make(map[string]struct{})
	for _, channel := range channels {
		if !routingSub2APIChannelServesBinding(channel, binding) {
			continue
		}
		pricing, confidence, confidenceScore, err := routingSub2APINormalizedPricing(channel, groupRatio, groupFound)
		if err != nil {
			return nil, fmt.Errorf("invalid sub2api channel pricing: %w", err)
		}
		if payload.SyncStatus == model.RoutingUpstreamSyncStatusPartial && confidenceScore > 0.8 {
			confidenceScore = 0.8
		}
		for _, upstreamModel := range routingSub2APIChannelModels(channel) {
			localModel := upstreamModel
			if mapped, ok := modelNameMap[upstreamModel]; ok {
				localModel = mapped
			}
			if _, duplicate := seenModels[localModel]; duplicate {
				return nil, fmt.Errorf("sub2api returned duplicate pricing for local model %s", localModel)
			}
			seenModels[localModel] = struct{}{}
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
	return writes, nil
}

func routingSub2APINormalizedPricing(
	channel routingSub2APIChannel,
	groupRatio float64,
	groupFound bool,
) (model.RoutingNormalizedPricing, string, float64, error) {
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
		return fetchRoutingSub2APICostSnapshots(ctx, binding, credentials)
	}

	payload, err := fetchRoutingPricingPayload(ctx, binding)
	if err != nil {
		return nil, err
	}

	now := common.GetTimestamp()
	groupRatio, hasGroupRatio := payload.GroupRatio[binding.UpstreamGroup]
	if !hasGroupRatio || groupRatio <= 0 {
		groupRatio = 1
	}
	confidence := model.RoutingCostConfidenceFull
	if !hasGroupRatio {
		confidence = model.RoutingCostConfidenceGroupOnly
	}
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
		snapshot := model.RoutingCostSnapshot{
			ChannelID:       binding.ChannelID,
			ModelName:       modelName,
			QuotaType:       item.QuotaType,
			GroupRatio:      groupRatio,
			BaseRatio:       item.ModelRatio,
			CompletionRatio: item.CompletionRatio,
			ModelPrice:      item.ModelPrice,
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
	if binding.UpstreamType == model.RoutingUpstreamTypeSub2API {
		snapshots, err := fetchRoutingSub2APICostSnapshots(ctx, binding, credentials)
		if err != nil {
			return routingPricingResponse{}, err
		}
		items := make([]routingPricingItem, 0, len(snapshots))
		groupRatio := map[string]float64{binding.UpstreamGroup: 1}
		for _, snapshot := range snapshots {
			groupRatio[binding.UpstreamGroup] = snapshot.GroupRatio
			items = append(items, routingPricingItem{
				ModelName:       snapshot.ModelName,
				QuotaType:       0,
				ModelRatio:      snapshot.BaseRatio,
				ModelPrice:      snapshot.ModelPrice,
				CompletionRatio: snapshot.CompletionRatio,
				EnableGroups:    []string{binding.UpstreamGroup},
				BillingMode:     snapshot.BillingMode,
			})
		}
		return routingPricingResponse{
			Success:     true,
			Data:        items,
			GroupRatio:  groupRatio,
			UsableGroup: map[string]string{binding.UpstreamGroup: binding.UpstreamGroup},
		}, nil
	}
	if binding.UpstreamType == model.RoutingUpstreamTypeNewAPI {
		if err = fetchRoutingUpstreamBalance(ctx, binding, credentials); err != nil && routingUpstreamAuthError(err) {
			return routingPricingResponse{}, err
		}
	}
	return fetchRoutingNewAPIPricingPayload(ctx, binding, credentials)
}

func fetchRoutingNewAPIPricingPayload(
	ctx context.Context,
	binding model.RoutingChannelBinding,
	credentials model.RoutingCredentials,
) (routingPricingResponse, error) {
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
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return routingPricingResponse{}, routingAuthErrorf("pricing endpoint returned %s", response.Status)
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
		if payload.Message == "" {
			payload.Message = "pricing endpoint returned success=false"
		}
		return routingPricingResponse{}, routingAuthErrorf("%s", routingCleanCredentialErrorMessage(payload.Message, credentials))
	}
	return payload, nil
}

type routingAuthError struct {
	message string
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
		if payload.Message == "" {
			payload.Message = "user self endpoint returned success=false"
		}
		return 0, false, routingAuthErrorf("%s", routingCleanCredentialErrorMessage(payload.Message, credentials))
	}

	balanceQuota := payload.Data.Quota - payload.Data.UsedQuota
	balance := balanceQuota / common.QuotaPerUnit
	if math.IsNaN(balance) || math.IsInf(balance, 0) {
		return 0, false, errors.New("invalid routing upstream balance")
	}
	return balance, true, nil
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
	if token := routingBearerToken(credentials); token != "" {
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

func routingBearerToken(credentials model.RoutingCredentials) string {
	switch {
	case strings.TrimSpace(credentials.NewAPIAccessToken) != "":
		return strings.TrimSpace(credentials.NewAPIAccessToken)
	case strings.TrimSpace(credentials.Sub2APIToken) != "":
		return strings.TrimSpace(credentials.Sub2APIToken)
	case strings.TrimSpace(credentials.GatewayAPIKey) != "":
		return strings.TrimSpace(credentials.GatewayAPIKey)
	default:
		return ""
	}
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

func routingPricingGroups(payload routingPricingResponse) []string {
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
	return groups
}

func routingBreakerSnapshotToModel(snapshot routingbreaker.Snapshot) model.RoutingBreakerState {
	state := model.RoutingBreakerState{
		ChannelID:           snapshot.Key.ChannelID,
		APIKeyIndex:         snapshot.Key.APIKeyIndex,
		ModelName:           snapshot.Key.Model,
		Group:               snapshot.Key.Group,
		SemanticVersion:     model.RoutingBreakerSemanticVersion,
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
