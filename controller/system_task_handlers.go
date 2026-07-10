package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
)

var smartRoutingFlushMu sync.Mutex
var smartRoutingRetentionLast atomic.Int64
var smartRoutingBreakerConfigMu sync.Mutex
var smartRoutingBreakerConfigLast routingBreakerConfigIdentity

type SmartRoutingRuntime struct {
	cancel context.CancelFunc
	wait   sync.WaitGroup
	close  sync.Once
}

type smartRoutingRuntimeDeps struct {
	getSetting  func() smart_routing_setting.SmartRoutingSetting
	refresh     func(smart_routing_setting.SmartRoutingSetting)
	flush       func(smart_routing_setting.SmartRoutingSetting)
	waitRefresh func(context.Context, time.Duration) bool
	waitFlush   func(context.Context, time.Duration) bool
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
	return newSmartRoutingRuntime(parent, smartRoutingRuntimeDeps{
		getSetting: smart_routing_setting.GetSetting,
		refresh: func(setting smart_routing_setting.SmartRoutingSetting) {
			if setting.Enabled {
				syncRoutingBreakerConfigFromSetting(setting)
				_, _ = refreshRoutingHotcacheFromDB(setting)
			}
			routinghotcache.Prune(common.GetTimestamp(), int64(setting.SnapshotStaleSec))
		},
		flush: func(setting smart_routing_setting.SmartRoutingSetting) {
			if !setting.Enabled {
				return
			}
			syncRoutingBreakerConfigFromSetting(setting)
			_, _ = flushRoutingRuntimeState(setting)
		},
		waitRefresh: waitRoutingRuntime,
		waitFlush:   waitRoutingRuntime,
	})
}

func newSmartRoutingRuntime(parent context.Context, deps smartRoutingRuntimeDeps) *SmartRoutingRuntime {
	ctx, cancel := context.WithCancel(parent)
	runtime := &SmartRoutingRuntime{cancel: cancel}
	runtime.wait.Add(2)

	go func() {
		defer runtime.wait.Done()
		for {
			if ctx.Err() != nil {
				return
			}
			setting := deps.getSetting()
			deps.refresh(setting)
			interval := time.Duration(setting.HotcacheRefreshSec) * time.Second
			if interval <= 0 {
				interval = 3 * time.Second
			}
			if !deps.waitRefresh(ctx, interval) || ctx.Err() != nil {
				return
			}
		}
	}()

	go func() {
		defer runtime.wait.Done()
		for {
			if ctx.Err() != nil {
				return
			}
			setting := deps.getSetting()
			deps.flush(setting)
			interval := time.Duration(setting.FlushIntervalMin) * time.Minute
			if interval <= 0 {
				interval = time.Minute
			}
			if !deps.waitFlush(ctx, interval) || ctx.Err() != nil {
				return
			}
		}
	}()

	return runtime
}

func (runtime *SmartRoutingRuntime) Close() {
	runtime.close.Do(runtime.cancel)
	runtime.wait.Wait()
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
	ModelName       string   `json:"model_name"`
	QuotaType       int      `json:"quota_type"`
	ModelRatio      float64  `json:"model_ratio"`
	ModelPrice      float64  `json:"model_price"`
	CompletionRatio float64  `json:"completion_ratio"`
	EnableGroups    []string `json:"enable_groups"`
	BillingMode     string   `json:"billing_mode"`
	BillingExpr     string   `json:"billing_expr"`
}

func flushRoutingRuntimeState(setting smart_routing_setting.SmartRoutingSetting) (map[string]any, error) {
	smartRoutingFlushMu.Lock()
	defer smartRoutingFlushMu.Unlock()

	summary := map[string]any{
		"metrics":  0,
		"breakers": 0,
	}
	drainedMetrics := routingmetrics.DrainSnapshots()
	for i := range drainedMetrics {
		metric := drainedMetrics[i]
		if err := model.UpsertRoutingChannelMetric(&metric); err != nil {
			routingmetrics.RequeueSnapshots(drainedMetrics[i:])
			return summary, err
		}
	}
	routinghotcache.LoadMetricSnapshots(drainedMetrics, setting.MetricBucketSec)
	summary["metrics"] = len(drainedMetrics)

	dirtyBreakers := routingbreaker.DirtySnapshots()
	for i, snapshot := range dirtyBreakers {
		state := routingBreakerSnapshotToModel(snapshot)
		if err := model.UpsertRoutingBreakerState(&state); err != nil {
			routingbreaker.RequeueDirtySnapshots(dirtyBreakers[i:])
			return summary, err
		}
	}
	summary["breakers"] = len(dirtyBreakers)

	now := common.GetTimestamp()
	const retentionIntervalSeconds int64 = 6 * 60 * 60
	if setting.RetentionDays > 0 && now-smartRoutingRetentionLast.Load() >= retentionIntervalSeconds {
		cutoffTs := now - int64(setting.RetentionDays)*86400
		deleted, err := model.DeleteRoutingMetricsBefore(cutoffTs)
		if err != nil {
			return summary, err
		}
		summary["retained_metrics_deleted"] = deleted
		smartRoutingRetentionLast.Store(now)
	}
	return summary, nil
}

func refreshRoutingHotcacheFromDB(setting smart_routing_setting.SmartRoutingSetting) (map[string]any, error) {
	summary := map[string]any{
		"costs":    0,
		"metrics":  0,
		"breakers": 0,
		"health":   0,
	}
	now := common.GetTimestamp()
	staleSeconds := int64(setting.SnapshotStaleSec)
	if staleSeconds <= 0 {
		staleSeconds = 1800
	}

	var costs []model.RoutingCostSnapshot
	if err := model.DB.Where("snapshot_ts >= ?", now-staleSeconds).Order("snapshot_ts desc").Limit(5000).Find(&costs).Error; err != nil {
		return summary, err
	}
	routinghotcache.LoadCostSnapshots(costs)
	summary["costs"] = len(costs)

	metricWindow := staleSeconds
	if bucketWindow := int64(setting.MetricBucketSec * 5); bucketWindow > metricWindow {
		metricWindow = bucketWindow
	}
	var metrics []model.RoutingChannelMetric
	if err := model.DB.Where("bucket_ts >= ?", now-metricWindow).Order("bucket_ts desc").Limit(5000).Find(&metrics).Error; err != nil {
		return summary, err
	}
	routinghotcache.LoadMetricSnapshots(metrics, setting.MetricBucketSec)
	summary["metrics"] = len(metrics)

	var breakerStates []model.RoutingBreakerState
	if err := model.DB.Order("updated_time desc").Limit(5000).Find(&breakerStates).Error; err != nil {
		return summary, err
	}
	routingbreaker.HydrateDefaultSnapshots(routingBreakerModelsToSnapshots(breakerStates))
	summary["breakers"] = len(breakerStates)

	var healthStates []model.RoutingChannelHealthState
	if err := model.DB.Order("updated_time desc").Limit(5000).Find(&healthStates).Error; err != nil {
		return summary, err
	}
	routinghotcache.LoadHealthSnapshots(healthStates, now)
	summary["health"] = len(healthStates)
	return summary, nil
}

func runRoutingCostSyncTask(ctx context.Context) (map[string]any, error) {
	setting := smart_routing_setting.GetSetting()
	syncRoutingBreakerConfigFromSetting(setting)

	summary := map[string]any{
		"bindings":        0,
		"snapshots":       0,
		"metrics":         0,
		"breakers":        0,
		"loaded_breakers": 0,
		"errors":          0,
		"skipped_backoff": 0,
	}

	flushSummary, err := flushRoutingRuntimeState(setting)
	if err != nil {
		return summary, err
	}
	summary["metrics"] = flushSummary["metrics"]
	summary["breakers"] = flushSummary["breakers"]

	refreshSummary, err := refreshRoutingHotcacheFromDB(setting)
	if err != nil {
		return summary, err
	}
	summary["loaded_breakers"] = refreshSummary["breakers"]

	var bindings []model.RoutingChannelBinding
	if err := model.DB.Where("enabled = ?", true).Order("channel_id asc").Find(&bindings).Error; err != nil {
		return summary, err
	}
	now := common.GetTimestamp()
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
	syncErrors := 0
	for _, binding := range eligibleBindings {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		snapshots, err := fetchRoutingCostSnapshots(ctx, binding)
		if err != nil {
			if ctx.Err() != nil {
				return summary, ctx.Err()
			}
			syncErrors++
			message := err.Error()
			_ = model.DB.Model(&model.RoutingChannelBinding{}).
				Where("id = ?", binding.ID).
				Updates(map[string]any{
					"last_sync_error":    &message,
					"sync_backoff_until": common.GetTimestamp() + 60,
				}).Error
			continue
		}
		for i := range snapshots {
			snapshot := snapshots[i]
			if err := model.UpsertRoutingCostSnapshot(&snapshot); err != nil {
				return summary, err
			}
			syncedSnapshots++
		}
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		routinghotcache.LoadCostSnapshots(snapshots)
		_ = model.DB.Model(&model.RoutingChannelBinding{}).
			Where("id = ?", binding.ID).
			Updates(map[string]any{
				"last_sync_error":    nil,
				"sync_backoff_until": 0,
			}).Error
	}
	summary["snapshots"] = syncedSnapshots
	summary["errors"] = syncErrors
	return summary, nil
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
	modelNameMap := routingModelReverseMapping(binding.ChannelID)

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

func routingModelReverseMapping(channelID int) map[string]string {
	if channelID <= 0 {
		return nil
	}
	var channel model.Channel
	if err := model.DB.Select("id", "model_mapping").Where("id = ?", channelID).First(&channel).Error; err != nil {
		return nil
	}
	if channel.ModelMapping == nil || strings.TrimSpace(*channel.ModelMapping) == "" {
		return nil
	}
	var mapping map[string]string
	if err := common.UnmarshalJsonStr(*channel.ModelMapping, &mapping); err != nil {
		return nil
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
	return reverse
}

func fetchRoutingPricingPayload(ctx context.Context, binding model.RoutingChannelBinding) (routingPricingResponse, error) {
	credentials, err := binding.GetCredentials()
	if err != nil {
		return routingPricingResponse{}, err
	}
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(binding.BaseURL, "/")+"/api/pricing", nil)
	if err != nil {
		return routingPricingResponse{}, err
	}
	applyRoutingAuthHeaders(request, binding, credentials)

	client := &http.Client{Timeout: time.Duration(defaultTimeoutSeconds) * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return routingPricingResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			markRoutingAuthFailure(binding.ChannelID)
			return routingPricingResponse{}, routingAuthErrorf("pricing endpoint returned %s", response.Status)
		}
		return routingPricingResponse{}, fmt.Errorf("pricing endpoint returned %s", response.Status)
	}

	var payload routingPricingResponse
	if err = common.DecodeJson(io.LimitReader(response.Body, maxRatioConfigBytes), &payload); err != nil {
		return routingPricingResponse{}, err
	}
	if !payload.Success {
		markRoutingAuthFailure(binding.ChannelID)
		if payload.Message == "" {
			payload.Message = "pricing endpoint returned success=false"
		}
		return routingPricingResponse{}, routingAuthErrorf("%s", routingCleanCredentialErrorMessage(payload.Message, credentials))
	}
	clearRoutingAuthFailure(binding.ChannelID)
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

func fetchRoutingUpstreamBalance(ctx context.Context, binding model.RoutingChannelBinding, credentials model.RoutingCredentials) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(binding.BaseURL, "/")+"/api/user/self", nil)
	if err != nil {
		return err
	}
	applyRoutingAuthHeaders(request, binding, credentials)

	client := &http.Client{Timeout: time.Duration(defaultTimeoutSeconds) * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		markRoutingAuthFailure(binding.ChannelID)
		return routingAuthErrorf("user self endpoint returned %s", response.Status)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("user self endpoint returned %s", response.Status)
	}

	var payload routingUserSelfResponse
	if err = common.DecodeJson(io.LimitReader(response.Body, maxRatioConfigBytes), &payload); err != nil {
		return err
	}
	if !payload.Success {
		markRoutingAuthFailure(binding.ChannelID)
		if payload.Message == "" {
			payload.Message = "user self endpoint returned success=false"
		}
		return routingAuthErrorf("%s", routingCleanCredentialErrorMessage(payload.Message, credentials))
	}

	balanceQuota := payload.Data.Quota - payload.Data.UsedQuota
	routinghotcache.SetBalance(binding.ChannelID, routinghotcache.BalanceSnapshot{
		Known:       true,
		Balance:     balanceQuota / common.QuotaPerUnit,
		UpdatedUnix: common.GetTimestamp(),
	})
	_ = model.UpsertRoutingChannelBalance(binding.ChannelID, balanceQuota/common.QuotaPerUnit, common.GetTimestamp())
	clearRoutingAuthFailure(binding.ChannelID)
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

func markRoutingAuthFailure(channelID int) {
	until := common.GetTimestamp() + 300
	routinghotcache.SetAuthFailure(channelID, routinghotcache.HealthMarker{
		Marked:      true,
		UpdatedUnix: common.GetTimestamp(),
	})
	if err := model.UpsertRoutingChannelAuthFailure(channelID, true, "authfail", until); err != nil {
		common.SysError(fmt.Sprintf("persist routing auth failure failed: channel_id=%d err=%v", channelID, err))
	}
}

func clearRoutingAuthFailure(channelID int) {
	routinghotcache.ClearAuthFailure(channelID)
	if err := model.ClearRoutingChannelAuthFailure(channelID, common.GetTimestamp()); err != nil {
		common.SysError(fmt.Sprintf("clear routing auth failure failed: channel_id=%d err=%v", channelID, err))
	}
}

func routingCleanUpstreamErrorMessage(message string) string {
	message = strings.TrimSpace(common.MaskSensitiveInfo(message))
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
		if state.ChannelID <= 0 || state.ModelName == "" || state.Group == "" {
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
		errorMessage = runErr.Error()
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, status, result, errorMessage); err != nil {
		common.SysLog(fmt.Sprintf("system task %s failed to persist result: %v", task.TaskID, err))
	}
}
