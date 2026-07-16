package channelrouting

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingbreaker "github.com/QuantumNous/new-api/pkg/routing_breaker"
	routingerror "github.com/QuantumNous/new-api/pkg/routing_error"
	routinghotcache "github.com/QuantumNous/new-api/pkg/routing_hotcache"
	routingselector "github.com/QuantumNous/new-api/service/routing"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"
)

const (
	activeProbeLeasePrefix        = "routing-probe-"
	activeProbeLeaseGrace         = 10 * time.Second
	activeProbeMaximumJitter      = 5 * time.Second
	activeProbeScheduleRetry      = 5 * time.Second
	activeProbeTargetRotation     = time.Minute
	activeProbeUnknownCostNanoUSD = int64(math.MaxInt64)
	activeProbeEstimatedTokens    = int64(32)
)

var (
	ErrActiveProbeExecutorUnavailable = errors.New("channel routing active probe executor is unavailable")
	ErrActiveProbeTargetStale         = errors.New("channel routing active probe target is stale")
	activeProbeExecutorRegistry       struct {
		sync.RWMutex
		executor ActiveProbeExecutor
	}
)

type ActiveProbeTarget struct {
	TargetKey            string
	SnapshotRevision     uint64
	PoolID               int
	MemberID             int
	ChannelID            int
	CredentialID         int
	GroupName            string
	ModelName            string
	EndpointHost         string
	EndpointAuthority    string
	Region               string
	BreakerScope         string
	BreakerState         string
	BreakerCooldownUntil int64
	AuthFailure          bool
	MultiKey             bool
	Interval             time.Duration
	EstimatedTokens      int64
	EstimatedCostNanoUSD int64
}

type ActiveProbeExecution struct {
	StatusCode       int
	ErrorCode        string
	Err              error
	LocalError       bool
	Classification   routingerror.Classification
	RetryAfterMs     int64
	PromptTokens     int64
	CompletionTokens int64
	CostNanoUSD      int64
}

type ActiveProbeExecutor func(context.Context, ActiveProbeTarget) ActiveProbeExecution

type ActiveProbeStats struct {
	Cycles              int64 `json:"cycles"`
	TargetsConsidered   int64 `json:"targets_considered"`
	TargetsSelected     int64 `json:"targets_selected"`
	SkippedNotDue       int64 `json:"skipped_not_due"`
	SkippedBudget       int64 `json:"skipped_budget"`
	LeaseContended      int64 `json:"lease_contended"`
	LeaseErrors         int64 `json:"lease_errors"`
	Executed            int64 `json:"executed"`
	Succeeded           int64 `json:"succeeded"`
	Failed              int64 `json:"failed"`
	TimedOut            int64 `json:"timed_out"`
	Canceled            int64 `json:"canceled"`
	LocalErrors         int64 `json:"local_errors"`
	PersistenceErrors   int64 `json:"persistence_errors"`
	CompletionErrors    int64 `json:"completion_errors"`
	EffectErrors        int64 `json:"effect_errors"`
	ReservedTokens      int64 `json:"reserved_tokens"`
	ReservedCostNanoUSD int64 `json:"reserved_cost_nano_usd"`
	Inflight            int64 `json:"inflight"`
	MaxInflight         int64 `json:"max_inflight"`
}

type activeProbeAtomicStats struct {
	cycles              atomic.Int64
	targetsConsidered   atomic.Int64
	targetsSelected     atomic.Int64
	skippedNotDue       atomic.Int64
	skippedBudget       atomic.Int64
	leaseContended      atomic.Int64
	leaseErrors         atomic.Int64
	executed            atomic.Int64
	succeeded           atomic.Int64
	failed              atomic.Int64
	timedOut            atomic.Int64
	canceled            atomic.Int64
	localErrors         atomic.Int64
	persistenceErrors   atomic.Int64
	completionErrors    atomic.Int64
	effectErrors        atomic.Int64
	reservedTokens      atomic.Int64
	reservedCostNanoUSD atomic.Int64
	inflight            atomic.Int64
	maxInflight         atomic.Int64
}

type ActiveProbeScheduler struct {
	deps activeProbeDeps

	scheduleMu sync.Mutex
	nextDue    map[string]time.Time
	stats      activeProbeAtomicStats
}

type activeProbeDeps struct {
	now       func() time.Time
	enabled   func() bool
	targets   func(smart_routing_setting.SmartRoutingSetting, time.Time) []ActiveProbeTarget
	executor  func() ActiveProbeExecutor
	validate  func(ActiveProbeTarget) error
	acquire   func(context.Context, ActiveProbeTarget, int64, int64, int64) (model.RoutingControlLease, bool, error)
	persist   func(context.Context, model.RoutingControlLease, model.RoutingProbeResult) error
	complete  func(context.Context, model.RoutingControlLease, int64) error
	release   func(context.Context, model.RoutingControlLease, int64) error
	waitUntil func(context.Context, time.Time) bool
}

type activeProbeJob struct {
	target ActiveProbeTarget
	due    time.Time
}

type activeProbeCycleBudget struct {
	mu            sync.Mutex
	tokens        int64
	costNanoUSD   int64
	reservedToken int64
	reservedCost  int64
}

type activeProbeRankedTarget struct {
	target   ActiveProbeTarget
	priority int
	rank     uint64
}

type activeProbeTargetHeap []activeProbeRankedTarget

func RegisterActiveProbeExecutor(executor ActiveProbeExecutor) {
	activeProbeExecutorRegistry.Lock()
	activeProbeExecutorRegistry.executor = executor
	activeProbeExecutorRegistry.Unlock()
}

func NewActiveProbeScheduler() *ActiveProbeScheduler {
	return newActiveProbeScheduler(defaultActiveProbeDeps())
}

func newActiveProbeScheduler(deps activeProbeDeps) *ActiveProbeScheduler {
	defaults := defaultActiveProbeDeps()
	if deps.now == nil {
		deps.now = defaults.now
	}
	if deps.enabled == nil {
		deps.enabled = func() bool { return true }
	}
	if deps.targets == nil {
		deps.targets = defaults.targets
	}
	if deps.executor == nil {
		deps.executor = defaults.executor
	}
	if deps.acquire == nil {
		deps.acquire = defaults.acquire
	}
	if deps.persist == nil {
		deps.persist = defaults.persist
	}
	if deps.complete == nil {
		deps.complete = defaults.complete
	}
	if deps.release == nil {
		deps.release = defaults.release
	}
	if deps.waitUntil == nil {
		deps.waitUntil = defaults.waitUntil
	}
	return &ActiveProbeScheduler{deps: deps, nextDue: make(map[string]time.Time)}
}

func (scheduler *ActiveProbeScheduler) RunCycle(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) error {
	if scheduler == nil || !activeProbeEnabled(setting) {
		return nil
	}
	if !scheduler.deps.enabled() {
		return nil
	}
	setting = smart_routing_setting.Normalize(setting)
	if ctx == nil {
		ctx = context.Background()
	}
	executor := scheduler.deps.executor()
	if executor == nil {
		return ErrActiveProbeExecutorUnavailable
	}

	now := scheduler.deps.now()
	targets := scheduler.deps.targets(setting, now)
	scheduler.stats.cycles.Add(1)
	scheduler.stats.targetsConsidered.Add(int64(len(targets)))
	jobs := scheduler.dueJobs(targets, now)
	scheduler.stats.targetsSelected.Add(int64(len(jobs)))
	if len(jobs) == 0 {
		return nil
	}

	budget := &activeProbeCycleBudget{
		tokens:      int64(setting.ActiveProbeTokenBudget),
		costNanoUSD: activeProbeCostBudgetNanoUSD(setting.ActiveProbeCostBudgetUSD),
	}
	hostLimits := make(map[string]chan struct{}, len(jobs))
	for _, job := range jobs {
		if _, exists := hostLimits[job.target.EndpointHost]; !exists {
			hostLimits[job.target.EndpointHost] = make(chan struct{}, setting.ActiveProbePerHost)
		}
	}

	workerCount := min(setting.ActiveProbeConcurrency, len(jobs))
	jobQueue := make(chan activeProbeJob, len(jobs))
	for _, job := range jobs {
		jobQueue <- job
	}
	close(jobQueue)

	var firstError error
	var errorOnce sync.Once
	recordError := func(err error) {
		if err != nil {
			errorOnce.Do(func() { firstError = err })
		}
	}
	var wait sync.WaitGroup
	wait.Add(workerCount)
	for range workerCount {
		go func() {
			defer wait.Done()
			for job := range jobQueue {
				if err := ctx.Err(); err != nil {
					return
				}
				if !scheduler.deps.enabled() {
					return
				}
				if !scheduler.deps.waitUntil(ctx, job.due) {
					return
				}
				if !scheduler.deps.enabled() {
					return
				}
				if err := scheduler.runTarget(ctx, setting, executor, job.target, hostLimits[job.target.EndpointHost], budget); err != nil {
					recordError(err)
				}
			}
		}()
	}
	wait.Wait()
	reservedTokens, reservedCost := budget.used()
	scheduler.stats.reservedTokens.Add(reservedTokens)
	scheduler.stats.reservedCostNanoUSD.Add(reservedCost)
	if err := ctx.Err(); err != nil {
		return err
	}
	return firstError
}

func (scheduler *ActiveProbeScheduler) Stats() ActiveProbeStats {
	if scheduler == nil {
		return ActiveProbeStats{}
	}
	return ActiveProbeStats{
		Cycles:              scheduler.stats.cycles.Load(),
		TargetsConsidered:   scheduler.stats.targetsConsidered.Load(),
		TargetsSelected:     scheduler.stats.targetsSelected.Load(),
		SkippedNotDue:       scheduler.stats.skippedNotDue.Load(),
		SkippedBudget:       scheduler.stats.skippedBudget.Load(),
		LeaseContended:      scheduler.stats.leaseContended.Load(),
		LeaseErrors:         scheduler.stats.leaseErrors.Load(),
		Executed:            scheduler.stats.executed.Load(),
		Succeeded:           scheduler.stats.succeeded.Load(),
		Failed:              scheduler.stats.failed.Load(),
		TimedOut:            scheduler.stats.timedOut.Load(),
		Canceled:            scheduler.stats.canceled.Load(),
		LocalErrors:         scheduler.stats.localErrors.Load(),
		PersistenceErrors:   scheduler.stats.persistenceErrors.Load(),
		CompletionErrors:    scheduler.stats.completionErrors.Load(),
		EffectErrors:        scheduler.stats.effectErrors.Load(),
		ReservedTokens:      scheduler.stats.reservedTokens.Load(),
		ReservedCostNanoUSD: scheduler.stats.reservedCostNanoUSD.Load(),
		Inflight:            scheduler.stats.inflight.Load(),
		MaxInflight:         scheduler.stats.maxInflight.Load(),
	}
}

func (scheduler *ActiveProbeScheduler) runTarget(
	ctx context.Context,
	setting smart_routing_setting.SmartRoutingSetting,
	executor ActiveProbeExecutor,
	target ActiveProbeTarget,
	hostLimit chan struct{},
	budget *activeProbeCycleBudget,
) error {
	select {
	case hostLimit <- struct{}{}:
		defer func() { <-hostLimit }()
	case <-ctx.Done():
		scheduler.setNextDue(target.TargetKey, scheduler.deps.now().Add(activeProbeScheduleRetry))
		return ctx.Err()
	}
	if !scheduler.deps.enabled() {
		scheduler.setNextDue(target.TargetKey, scheduler.deps.now().Add(activeProbeScheduleRetry))
		return nil
	}

	now := scheduler.deps.now()
	timeout := time.Duration(setting.ActiveProbeTimeoutMs) * time.Millisecond
	lease, acquired, err := scheduler.deps.acquire(
		ctx,
		target,
		now.UnixMilli(),
		int64((timeout+activeProbeLeaseGrace)/time.Millisecond),
		int64(target.Interval/time.Millisecond),
	)
	if err != nil {
		scheduler.stats.leaseErrors.Add(1)
		scheduler.setNextDue(target.TargetKey, now.Add(activeProbeScheduleRetry))
		return err
	}
	if !acquired {
		scheduler.stats.leaseContended.Add(1)
		scheduler.setNextDue(target.TargetKey, now.Add(min(target.Interval/4, 30*time.Second)))
		return nil
	}

	if !budget.reserve(target.EstimatedTokens, target.EstimatedCostNanoUSD) {
		scheduler.stats.skippedBudget.Add(1)
		scheduler.setNextDue(target.TargetKey, now.Add(activeProbeScheduleRetry))
		return scheduler.releaseLease(lease)
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	started := scheduler.deps.now()
	inflight := scheduler.stats.inflight.Add(1)
	updateAtomicMaximum(&scheduler.stats.maxInflight, inflight)
	execution := executor(probeCtx, target)
	finished := scheduler.deps.now()
	probeContextErr := probeCtx.Err()
	scheduler.stats.inflight.Add(-1)
	cancel()
	scheduler.stats.executed.Add(1)
	targetStale := false
	if scheduler.deps.validate != nil {
		if validateErr := scheduler.deps.validate(target); validateErr != nil {
			targetStale = true
			execution = ActiveProbeExecution{
				Err:        validateErr,
				LocalError: true,
				Classification: routingerror.Classification{
					Responsibility: routingerror.ResponsibilityConfig,
					Scope:          routingerror.ScopeRequest,
					Retryability:   routingerror.RetryBeforeCommit,
					HealthEffect:   routingerror.HealthIgnore,
					CapacityEffect: routingerror.CapacityNone,
					Component:      routingerror.ComponentServing,
					Rule:           "active_probe_target_stale",
				},
			}
		}
	}

	result := activeProbeResult(target, lease, execution, started, finished, probeContextErr, ctx.Err())
	scheduler.recordOutcome(result.Outcome)
	if ctx.Err() != nil {
		scheduler.setNextDue(target.TargetKey, now.Add(activeProbeScheduleRetry))
		return errors.Join(ctx.Err(), scheduler.releaseLease(lease))
	}
	persistCtx, persistCancel := context.WithTimeout(ctx, 5*time.Second)
	persistErr := scheduler.deps.persist(persistCtx, lease, result)
	persistCancel()
	if persistErr != nil {
		scheduler.stats.persistenceErrors.Add(1)
		completeErr := scheduler.completeLease(lease, finished.UnixMilli())
		if completeErr != nil {
			scheduler.stats.completionErrors.Add(1)
		}
		scheduler.setNextDue(target.TargetKey, now.Add(target.Interval))
		return errors.Join(persistErr, completeErr)
	}
	if completeErr := scheduler.completeLease(lease, finished.UnixMilli()); completeErr != nil {
		scheduler.stats.completionErrors.Add(1)
		scheduler.setNextDue(target.TargetKey, now.Add(activeProbeScheduleRetry))
		return completeErr
	}
	if targetStale {
		scheduler.setNextDue(target.TargetKey, now.Add(activeProbeScheduleRetry))
		return nil
	}
	if scheduler.deps.validate != nil {
		if validateErr := scheduler.deps.validate(target); validateErr != nil {
			scheduler.setNextDue(target.TargetKey, now.Add(activeProbeScheduleRetry))
			return nil
		}
	}
	effectCtx, effectCancel := context.WithTimeout(ctx, 5*time.Second)
	effectErr := applyActiveProbeBreakerOutcome(effectCtx, setting, target, execution, result.Outcome, finished)
	effectCancel()
	if effectErr != nil {
		scheduler.stats.effectErrors.Add(1)
		scheduler.setNextDue(target.TargetKey, now.Add(activeProbeScheduleRetry))
		return effectErr
	}
	scheduler.setNextDue(target.TargetKey, now.Add(target.Interval))
	return nil
}

func (scheduler *ActiveProbeScheduler) dueJobs(targets []ActiveProbeTarget, now time.Time) []activeProbeJob {
	scheduler.scheduleMu.Lock()
	defer scheduler.scheduleMu.Unlock()

	selected := make(map[string]struct{}, len(targets))
	jobs := make([]activeProbeJob, 0, len(targets))
	for _, target := range targets {
		selected[target.TargetKey] = struct{}{}
		nextDue := scheduler.nextDue[target.TargetKey]
		if target.BreakerState == model.RoutingBreakerStateOpen && target.BreakerCooldownUntil > now.Unix() {
			cooldown := time.Unix(target.BreakerCooldownUntil, 0)
			if nextDue.Before(cooldown) {
				nextDue = cooldown
				scheduler.nextDue[target.TargetKey] = cooldown
			}
		}
		if nextDue.After(now) {
			scheduler.stats.skippedNotDue.Add(1)
			continue
		}
		jitter := deterministicActiveProbeJitter(target, now)
		jobs = append(jobs, activeProbeJob{target: target, due: now.Add(jitter)})
		scheduler.nextDue[target.TargetKey] = now.Add(activeProbeScheduleRetry)
	}
	for targetKey := range scheduler.nextDue {
		if _, exists := selected[targetKey]; !exists {
			delete(scheduler.nextDue, targetKey)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].due.Equal(jobs[j].due) {
			return jobs[i].due.Before(jobs[j].due)
		}
		return jobs[i].target.TargetKey < jobs[j].target.TargetKey
	})
	return jobs
}

func (scheduler *ActiveProbeScheduler) setNextDue(targetKey string, due time.Time) {
	scheduler.scheduleMu.Lock()
	if _, exists := scheduler.nextDue[targetKey]; exists {
		scheduler.nextDue[targetKey] = due
	}
	scheduler.scheduleMu.Unlock()
}

func (scheduler *ActiveProbeScheduler) completeLease(lease model.RoutingControlLease, completedAtMs int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return scheduler.deps.complete(ctx, lease, completedAtMs)
}

func (scheduler *ActiveProbeScheduler) releaseLease(lease model.RoutingControlLease) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return scheduler.deps.release(ctx, lease, scheduler.deps.now().UnixMilli())
}

func (scheduler *ActiveProbeScheduler) recordOutcome(outcome string) {
	switch outcome {
	case model.RoutingProbeOutcomeSuccess:
		scheduler.stats.succeeded.Add(1)
	case model.RoutingProbeOutcomeTimeout:
		scheduler.stats.timedOut.Add(1)
		scheduler.stats.failed.Add(1)
	case model.RoutingProbeOutcomeCanceled:
		scheduler.stats.canceled.Add(1)
		scheduler.stats.failed.Add(1)
	case model.RoutingProbeOutcomeLocalError:
		scheduler.stats.localErrors.Add(1)
		scheduler.stats.failed.Add(1)
	default:
		scheduler.stats.failed.Add(1)
	}
}

func defaultActiveProbeDeps() activeProbeDeps {
	return activeProbeDeps{
		now: time.Now,
		enabled: func() bool {
			return activeProbeEnabled(smart_routing_setting.GetSetting())
		},
		targets: currentActiveProbeTargets,
		executor: func() ActiveProbeExecutor {
			activeProbeExecutorRegistry.RLock()
			defer activeProbeExecutorRegistry.RUnlock()
			return activeProbeExecutorRegistry.executor
		},
		validate: validateActiveProbeTarget,
		acquire: func(ctx context.Context, target ActiveProbeTarget, nowMs int64, ttlMs int64, minimumIntervalMs int64) (model.RoutingControlLease, bool, error) {
			return model.TryAcquireRoutingControlLeaseContext(
				ctx, activeProbeLeaseName(target.TargetKey), NodeEpochID(), ttlMs, minimumIntervalMs, false,
			)
		},
		persist: func(ctx context.Context, lease model.RoutingControlLease, result model.RoutingProbeResult) error {
			_, _, err := model.CreateRoutingProbeResultContext(ctx, lease, result)
			return err
		},
		complete: func(ctx context.Context, lease model.RoutingControlLease, _ int64) error {
			return model.CompleteRoutingControlLeaseContext(ctx, lease)
		},
		release: func(ctx context.Context, lease model.RoutingControlLease, _ int64) error {
			return model.ReleaseRoutingControlLeaseContext(ctx, lease)
		},
		waitUntil: func(ctx context.Context, due time.Time) bool {
			delay := time.Until(due)
			if delay <= 0 {
				return ctx.Err() == nil
			}
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-timer.C:
				return true
			}
		},
	}
}

func validateActiveProbeTarget(target ActiveProbeTarget) error {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || snapshot.view.Revision != target.SnapshotRevision ||
		target.PoolID <= 0 || target.MemberID <= 0 || target.ChannelID <= 0 ||
		target.GroupName == "" || target.ModelName == "" {
		return ErrActiveProbeTargetStale
	}
	poolID, exists := snapshot.poolByGroup[target.GroupName]
	if !exists || poolID != target.PoolID ||
		snapshot.memberByPoolChannel[poolChannelKey{PoolID: poolID, ChannelID: target.ChannelID}] != target.MemberID {
		return ErrActiveProbeTargetStale
	}
	if _, exists := snapshot.modelByMemberModel[memberModelKey{memberID: target.MemberID, model: target.ModelName}]; !exists {
		return ErrActiveProbeTargetStale
	}
	channel, exists := snapshot.channelByID[target.ChannelID]
	if !exists || channel.Status != common.ChannelStatusEnabled || channel.MultiKey != target.MultiKey ||
		EndpointHost(channel.Endpoint, channel.ID) != target.EndpointHost ||
		EndpointAuthority(channel.Endpoint, channel.ID) != target.EndpointAuthority ||
		RoutingRegion() != target.Region {
		return ErrActiveProbeTargetStale
	}
	if target.CredentialID == 0 {
		if channel.CredentialRequired || len(channel.CredentialIDs) > 0 {
			return ErrActiveProbeTargetStale
		}
		return nil
	}
	credential, exists := snapshot.credentialByID[target.CredentialID]
	if !exists || credential.ChannelID != target.ChannelID || !credential.Operational {
		return ErrActiveProbeTargetStale
	}
	for _, credentialID := range channel.CredentialIDs {
		if credentialID == target.CredentialID {
			return nil
		}
	}
	return ErrActiveProbeTargetStale
}

func currentActiveProbeTargets(setting smart_routing_setting.SmartRoutingSetting, now time.Time) []ActiveProbeTarget {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || snapshot.view.Revision == 0 || snapshot.view.Revision > math.MaxInt64 {
		return nil
	}
	maxTargets := setting.ActiveProbeMaxTargets
	if maxTargets < 1 {
		return nil
	}
	seed := uint64(now.Unix() / int64(activeProbeTargetRotation/time.Second))
	targets := make(activeProbeTargetHeap, 0, maxTargets)
	heap.Init(&targets)
	for _, pool := range snapshot.view.Pools {
		if pool.DeploymentStage != model.RoutingDeploymentStageActive {
			continue
		}
		for _, member := range pool.Members {
			channel, exists := snapshot.channelByID[member.ChannelID]
			if !exists || channel.Status != common.ChannelStatusEnabled {
				continue
			}
			credentialIDs := append([]int(nil), member.CredentialIDs...)
			if len(credentialIDs) == 0 && !channel.CredentialRequired {
				credentialIDs = []int{0}
			}
			if member.CredentialsTruncated || len(credentialIDs) == 0 {
				continue
			}
			for _, observation := range member.Models {
				if strings.TrimSpace(observation.ModelName) == "" {
					continue
				}
				breakerState := strings.ToLower(strings.TrimSpace(observation.BreakerState))
				if breakerState == "" {
					breakerState = model.RoutingBreakerStateHealthy
				}
				breakerCooldownUntil := observation.BreakerCooldownUntil
				breakerScope := "member"
				endpointBreaker, endpointAuthority, region := endpointBreakerForChannel(
					channel, now, setting.SnapshotStaleSec,
				)
				memberBreaker := &routingselector.BreakerSnapshot{
					State: breakerState, CooldownUntilUnix: observation.BreakerCooldownUntil,
					UpdatedUnix: observation.BreakerUpdatedUnix,
				}
				mergedBreaker, endpointSelected := mergeRoutingBreaker(memberBreaker, endpointBreaker)
				if mergedBreaker != nil {
					breakerState = mergedBreaker.State
					breakerCooldownUntil = mergedBreaker.CooldownUntilUnix
				}
				if endpointSelected {
					breakerScope = BreakerScopeEndpoint
				}
				for _, credentialID := range credentialIDs {
					if credentialID > 0 {
						credential, operational := snapshot.credentialByID[credentialID]
						if !operational || credential.ChannelID != member.ChannelID || !credential.Operational {
							continue
						}
					}
					credentialAuthFailure := false
					if credentialID > 0 {
						if state, known := CredentialRuntimeHealth(credentialID); known {
							credentialAuthFailure = state.AuthFailure &&
								(state.AuthFailureUntilMs <= 0 || state.AuthFailureUntilMs > now.UnixMilli())
						}
					}
					targetBreakerState := breakerState
					targetBreakerCooldown := breakerCooldownUntil
					if credentialAuthFailure {
						targetBreakerState = model.RoutingBreakerStateOpen
						targetBreakerCooldown = now.Unix()
					}
					target := ActiveProbeTarget{
						SnapshotRevision:     snapshot.view.Revision,
						PoolID:               pool.ID,
						MemberID:             member.ID,
						ChannelID:            member.ChannelID,
						CredentialID:         credentialID,
						GroupName:            pool.GroupName,
						ModelName:            observation.ModelName,
						EndpointHost:         EndpointHost(channel.Endpoint, member.ChannelID),
						EndpointAuthority:    endpointAuthority,
						Region:               region,
						BreakerScope:         breakerScope,
						BreakerState:         targetBreakerState,
						BreakerCooldownUntil: targetBreakerCooldown,
						AuthFailure:          credentialAuthFailure || (!member.MultiKey && channel.AuthFailure),
						MultiKey:             member.MultiKey,
						Interval:             activeProbeTargetInterval(setting, targetBreakerState),
						EstimatedTokens:      activeProbeEstimatedTokens,
						EstimatedCostNanoUSD: activeProbeEstimatedCostNanoUSD(observation),
					}
					target.TargetKey = activeProbeTargetKey(target)
					ranked := activeProbeRankedTarget{
						target: target, priority: activeProbeTargetPriority(target.BreakerState),
						rank: activeProbeTargetRank(target.TargetKey, seed),
					}
					if len(targets) < maxTargets {
						heap.Push(&targets, ranked)
					} else if activeProbeRankedTargetLess(ranked, targets[0]) {
						heap.Pop(&targets)
						heap.Push(&targets, ranked)
					}
				}
			}
		}
	}
	selected := make([]ActiveProbeTarget, 0, len(targets))
	for len(targets) > 0 {
		selected = append(selected, heap.Pop(&targets).(activeProbeRankedTarget).target)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].TargetKey < selected[j].TargetKey })
	return selected
}

func activeProbeResult(
	target ActiveProbeTarget,
	lease model.RoutingControlLease,
	execution ActiveProbeExecution,
	started time.Time,
	finished time.Time,
	probeContextErr error,
	parentContextErr error,
) model.RoutingProbeResult {
	if finished.Before(started) {
		finished = started
	}
	outcome := model.RoutingProbeOutcomeSuccess
	if execution.Err != nil {
		outcome = model.RoutingProbeOutcomeFailure
	}
	if errors.Is(probeContextErr, context.DeadlineExceeded) || errors.Is(execution.Err, context.DeadlineExceeded) {
		outcome = model.RoutingProbeOutcomeTimeout
	}
	if execution.LocalError {
		outcome = model.RoutingProbeOutcomeLocalError
	}
	if parentContextErr != nil || errors.Is(probeContextErr, context.Canceled) || errors.Is(execution.Err, context.Canceled) {
		outcome = model.RoutingProbeOutcomeCanceled
	}
	effectiveErr := execution.Err
	if effectiveErr == nil {
		effectiveErr = probeContextErr
	}
	if effectiveErr == nil {
		effectiveErr = parentContextErr
	}
	message := ""
	if effectiveErr != nil {
		message = truncateActiveProbeText(common.SanitizeErrorMessage(effectiveErr.Error()), 1_024)
	}
	classification := execution.Classification
	return model.RoutingProbeResult{
		ProbeID:            activeProbeResultID(target.TargetKey, lease.FencingToken),
		TargetKey:          target.TargetKey,
		ProbeType:          model.RoutingProbeTypeServing,
		SnapshotRevision:   int64(target.SnapshotRevision),
		PoolID:             target.PoolID,
		MemberID:           target.MemberID,
		ChannelID:          target.ChannelID,
		CredentialID:       target.CredentialID,
		GroupName:          target.GroupName,
		ModelName:          target.ModelName,
		EndpointHost:       target.EndpointHost,
		EndpointAuthority:  target.EndpointAuthority,
		Region:             target.Region,
		BreakerScope:       target.BreakerScope,
		EvidenceCount:      1,
		NodeCount:          1,
		BreakerState:       target.BreakerState,
		Outcome:            outcome,
		Responsibility:     string(classification.Responsibility),
		Scope:              string(classification.Scope),
		Retryability:       string(classification.Retryability),
		HealthEffect:       string(classification.HealthEffect),
		CapacityEffect:     string(classification.CapacityEffect),
		ClassificationRule: classification.Rule,
		StatusCode:         min(max(execution.StatusCode, 0), 599),
		ErrorCode:          truncateActiveProbeText(execution.ErrorCode, 128),
		ErrorMessage:       message,
		PromptTokens:       max(execution.PromptTokens, 0),
		CompletionTokens:   max(execution.CompletionTokens, 0),
		CostNanoUSD:        max(execution.CostNanoUSD, 0),
		LatencyMs:          max(finished.Sub(started).Milliseconds(), 0),
		StartedTimeMs:      started.UnixMilli(),
		FinishedTimeMs:     finished.UnixMilli(),
		LeaseFencingToken:  lease.FencingToken,
		NodeEpochID:        lease.HolderID,
		CreatedTime:        finished.UnixMilli(),
	}
}

func applyActiveProbeBreakerOutcome(
	ctx context.Context,
	setting smart_routing_setting.SmartRoutingSetting,
	target ActiveProbeTarget,
	execution ActiveProbeExecution,
	outcome string,
	now time.Time,
) error {
	if target.ChannelID <= 0 || target.ModelName == "" || target.GroupName == "" ||
		target.EndpointAuthority == "" || target.Region == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	setting = smart_routing_setting.Normalize(setting)
	if now.IsZero() {
		now = time.Now()
	}
	if outcome == model.RoutingProbeOutcomeLocalError || outcome == model.RoutingProbeOutcomeCanceled {
		return nil
	}
	endpointKey := routingbreaker.NewEndpointKey(target.EndpointAuthority, target.Region)
	networkFailure := outcome == model.RoutingProbeOutcomeTimeout ||
		(outcome == model.RoutingProbeOutcomeFailure &&
			execution.Classification.Responsibility == routingerror.ResponsibilityNetwork &&
			execution.Classification.Scope == routingerror.ScopeEndpoint &&
			(execution.Classification.HealthEffect == routingerror.HealthDegrade ||
				execution.Classification.HealthEffect == routingerror.HealthOpen))
	endpointReachable := outcome == model.RoutingProbeOutcomeSuccess ||
		(execution.StatusCode > 0 && execution.Classification.Responsibility != routingerror.ResponsibilityNetwork) ||
		execution.Classification.Responsibility == routingerror.ResponsibilityProvider ||
		execution.Classification.Responsibility == routingerror.ResponsibilityCapacity ||
		execution.Classification.Responsibility == routingerror.ResponsibilityCredential
	if networkFailure {
		routingbreaker.RecordReliabilityFailure(endpointKey, routingbreaker.FailureNetwork)
	} else if endpointReachable {
		routingbreaker.RecordActiveProbeSuccess(endpointKey)
	}
	key := routingbreaker.Key{
		ChannelID:   target.ChannelID,
		APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		Model:       target.ModelName,
		Group:       target.GroupName,
	}
	if outcome == model.RoutingProbeOutcomeSuccess {
		if target.CredentialID > 0 {
			ClearCredentialAuthFailure(target.CredentialID, target.ChannelID, now)
			ClearCredentialCapacityCooldown(target.CredentialID, target.ChannelID, now)
		}
		if target.MultiKey {
			return nil
		}
		_, cachedAuthFailure := routinghotcache.GetAuthFailure(target.ChannelID)
		if target.AuthFailure || cachedAuthFailure {
			applied, err := model.ApplyRoutingChannelProbeAuthStateContext(
				ctx, target.ChannelID, target.CredentialID, false, "", now.Unix(),
			)
			if err != nil {
				return err
			}
			if !applied {
				return nil
			}
			routinghotcache.ClearAuthFailure(target.ChannelID)
		}
		routinghotcache.ClearCapacityCooldown(key.HotcacheKey())
		if target.BreakerState == model.RoutingBreakerStateOpen || target.BreakerState == model.RoutingBreakerStateHalfOpen ||
			target.BreakerState == model.RoutingBreakerStateDegraded {
			routingbreaker.RecordActiveProbeSuccess(key)
		}
		return nil
	}

	if execution.StatusCode == http.StatusUnauthorized || execution.StatusCode == http.StatusForbidden {
		nowUnix := now.Unix()
		staleSeconds := int64(setting.SnapshotStaleSec)
		until := int64(math.MaxInt64)
		if staleSeconds <= math.MaxInt64-nowUnix {
			until = nowUnix + staleSeconds
		}
		reason := "active_probe_http_" + strconv.Itoa(execution.StatusCode)
		if target.CredentialID > 0 {
			RecordCredentialAuthFailure(
				target.CredentialID,
				target.ChannelID,
				reason,
				time.Unix(until, 0),
				now,
			)
		}
		if target.MultiKey {
			return nil
		}
		applied, err := model.ApplyRoutingChannelProbeAuthStateContext(
			ctx, target.ChannelID, target.CredentialID, true, reason, until,
		)
		if err != nil || !applied {
			return err
		}
		routinghotcache.SetAuthFailure(target.ChannelID, routinghotcache.HealthMarker{Marked: true, UpdatedUnix: nowUnix})
		return nil
	}
	if execution.Classification.CapacityEffect == routingerror.CapacityCooldown {
		maxCooldownSeconds := int64(setting.MaxCooldownSec)
		maximumDurationSeconds := int64(math.MaxInt64) / int64(time.Second)
		if maxCooldownSeconds > maximumDurationSeconds {
			maxCooldownSeconds = maximumDurationSeconds
		}
		maxCooldown := time.Duration(maxCooldownSeconds) * time.Second
		baseCooldownMilliseconds := int64(setting.BackoffBaseMs429)
		maximumDurationMilliseconds := int64(math.MaxInt64) / int64(time.Millisecond)
		if baseCooldownMilliseconds > maximumDurationMilliseconds {
			baseCooldownMilliseconds = maximumDurationMilliseconds
		}
		baseCooldown := time.Duration(baseCooldownMilliseconds) * time.Millisecond
		retryAfterMs := min(max(execution.RetryAfterMs, 0), maxCooldown.Milliseconds())
		retryAfter := time.Duration(retryAfterMs) * time.Millisecond
		cooldown := retryAfter
		if cooldown <= 0 {
			cooldown = baseCooldown
		}
		if maxCooldown > 0 && cooldown > maxCooldown {
			cooldown = maxCooldown
		}
		if target.CredentialID > 0 &&
			execution.Classification.Responsibility == routingerror.ResponsibilityCapacity &&
			execution.Classification.Scope != routingerror.ScopeChannel {
			RecordCredentialCapacityCooldown(
				target.CredentialID, target.ChannelID, execution.StatusCode, now.Add(cooldown), now,
			)
		}
		if !target.MultiKey {
			routinghotcache.RecordCapacityCooldown(
				key.HotcacheKey(), execution.StatusCode, retryAfter, baseCooldown, maxCooldown, now,
			)
		}
	}
	if target.MultiKey {
		return nil
	}
	if outcome == model.RoutingProbeOutcomeTimeout {
		return nil
	}
	if outcome != model.RoutingProbeOutcomeFailure {
		return nil
	}
	if execution.Classification.HealthEffect != routingerror.HealthDegrade && execution.Classification.HealthEffect != routingerror.HealthOpen {
		return nil
	}
	switch execution.Classification.Responsibility {
	case routingerror.ResponsibilityProvider:
		routingbreaker.RecordReliabilityFailure(key, routingbreaker.FailureProvider5xx)
	}
	return nil
}

func activeProbeEnabled(setting smart_routing_setting.SmartRoutingSetting) bool {
	return setting.ActiveProbeEnabled &&
		smart_routing_setting.ResolveEffectiveMode(setting).AllowsActiveProbe()
}

func activeProbeTargetInterval(setting smart_routing_setting.SmartRoutingSetting, breakerState string) time.Duration {
	switch breakerState {
	case model.RoutingBreakerStateOpen, model.RoutingBreakerStateHalfOpen:
		return time.Duration(setting.ActiveProbeOpenSec) * time.Second
	case model.RoutingBreakerStateDegraded:
		return time.Duration(setting.ActiveProbeDegradedSec) * time.Second
	default:
		return time.Duration(setting.ActiveProbeHealthySec) * time.Second
	}
}

func activeProbeEstimatedCostNanoUSD(observation ModelSnapshot) int64 {
	if !observation.CostKnown {
		return activeProbeUnknownCostNanoUSD
	}
	cost, err := shadowExpectedCost(observation, RequestProfile{
		PromptTokenEstimate:     int(activeProbeEstimatedTokens / 2),
		CompletionTokenEstimate: int(activeProbeEstimatedTokens - activeProbeEstimatedTokens/2),
	})
	if err != nil || cost == nil || !cost.Known || cost.Cost < 0 || math.IsNaN(cost.Cost) || math.IsInf(cost.Cost, 0) {
		return activeProbeUnknownCostNanoUSD
	}
	if cost.Cost >= float64(math.MaxInt64)/1_000_000_000 {
		return math.MaxInt64
	}
	return int64(math.Ceil(cost.Cost * 1_000_000_000))
}

func activeProbeCostBudgetNanoUSD(costUSD float64) int64 {
	if costUSD <= 0 || math.IsNaN(costUSD) || math.IsInf(costUSD, 0) {
		return 0
	}
	if costUSD >= float64(math.MaxInt64)/1_000_000_000 {
		return math.MaxInt64
	}
	return int64(math.Floor(costUSD * 1_000_000_000))
}

func activeProbeTargetKey(target ActiveProbeTarget) string {
	payload := fmt.Sprintf(
		"routing-probe-target:v5\x00%d\x00%d\x00%d\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s",
		target.SnapshotRevision,
		target.PoolID,
		target.MemberID,
		target.ChannelID,
		target.CredentialID,
		target.GroupName,
		target.ModelName,
		target.EndpointHost,
		target.EndpointAuthority,
		target.Region,
	)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func activeProbeTargetRank(targetKey string, seed uint64) uint64 {
	sum := sha256.Sum256([]byte(targetKey + ":" + strconv.FormatUint(seed, 10)))
	return binary.BigEndian.Uint64(sum[:8])
}

func activeProbeTargetPriority(breakerState string) int {
	switch breakerState {
	case model.RoutingBreakerStateOpen, model.RoutingBreakerStateHalfOpen:
		return 0
	case model.RoutingBreakerStateDegraded:
		return 1
	default:
		return 2
	}
}

func activeProbeRankedTargetLess(left activeProbeRankedTarget, right activeProbeRankedTarget) bool {
	if left.priority != right.priority {
		return left.priority < right.priority
	}
	if left.rank != right.rank {
		return left.rank < right.rank
	}
	return left.target.TargetKey < right.target.TargetKey
}

func deterministicActiveProbeJitter(target ActiveProbeTarget, now time.Time) time.Duration {
	maximum := min(activeProbeMaximumJitter, target.Interval/10)
	if maximum <= 0 {
		return 0
	}
	bucket := now.UnixNano() / max(target.Interval.Nanoseconds(), 1)
	sum := sha256.Sum256([]byte(target.TargetKey + ":" + strconv.FormatInt(bucket, 10)))
	nanos := binary.BigEndian.Uint64(sum[:8]) % uint64(maximum)
	return time.Duration(nanos)
}

func activeProbeLeaseName(targetKey string) string {
	if len(targetKey) > 32 {
		targetKey = targetKey[:32]
	}
	return activeProbeLeasePrefix + targetKey
}

func activeProbeResultID(targetKey string, fencingToken int64) string {
	sum := sha256.Sum256([]byte(targetKey + ":" + strconv.FormatInt(fencingToken, 10)))
	return hex.EncodeToString(sum[:])
}

func truncateActiveProbeText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func (budget *activeProbeCycleBudget) reserve(tokens int64, costNanoUSD int64) bool {
	if budget == nil {
		return false
	}
	tokens = max(tokens, 0)
	costNanoUSD = max(costNanoUSD, 0)
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if tokens > budget.tokens-budget.reservedToken || costNanoUSD > budget.costNanoUSD-budget.reservedCost {
		return false
	}
	budget.reservedToken += tokens
	budget.reservedCost += costNanoUSD
	return true
}

func (budget *activeProbeCycleBudget) used() (int64, int64) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.reservedToken, budget.reservedCost
}

func updateAtomicMaximum(target *atomic.Int64, value int64) {
	for {
		current := target.Load()
		if value <= current || target.CompareAndSwap(current, value) {
			return
		}
	}
}

func (targets activeProbeTargetHeap) Len() int { return len(targets) }

func (targets activeProbeTargetHeap) Less(i, j int) bool {
	if targets[i].priority != targets[j].priority {
		return targets[i].priority > targets[j].priority
	}
	if targets[i].rank != targets[j].rank {
		return targets[i].rank > targets[j].rank
	}
	return targets[i].target.TargetKey > targets[j].target.TargetKey
}

func (targets activeProbeTargetHeap) Swap(i, j int) { targets[i], targets[j] = targets[j], targets[i] }

func (targets *activeProbeTargetHeap) Push(value any) {
	*targets = append(*targets, value.(activeProbeRankedTarget))
}

func (targets *activeProbeTargetHeap) Pop() any {
	old := *targets
	last := len(old) - 1
	value := old[last]
	old[last] = activeProbeRankedTarget{}
	*targets = old[:last]
	return value
}
