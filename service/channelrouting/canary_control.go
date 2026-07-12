package channelrouting

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"gorm.io/gorm"
)

const (
	routingCanaryEvaluatorLeaseName  = "routing-v2-canary-evaluator"
	routingCanaryOperationLeaseName  = "routing-v2-canary-operations"
	canaryControlLeaseTTL            = 2 * time.Minute
	canaryOperationClaimTTL          = time.Minute
	canaryEvaluatorSettleDelay       = 5 * time.Second
	canaryEvaluatorMaxCatchUpWindows = 10
	canaryEvaluatorMaxNodes          = 4_096
	canaryEvaluatorPageSize          = model.RoutingRuntimeCheckpointMaxPageSize
)

var ErrCanaryControlInvalid = errors.New("invalid channel routing canary control state")

type canaryEvaluationTarget struct {
	PolicyRevision      int64
	PolicyHash          string
	ActivationID        int64
	ActivationCreatedMs int64
	PoolID              int
	TrafficBasisPoints  int
	RolloutKey          RolloutKey
	Policy              model.RoutingCanaryEvaluationPolicy
}

type canaryWindowAggregate struct {
	Control                 canaryCohortAggregate
	Canary                  canaryCohortAggregate
	ExpectedNodes           int
	ReportedNodes           int
	NodeCoverageBasisPoints int
}

type canaryCohortAggregate struct {
	LogicalRequests             int64
	Successes                   int64
	Failures                    int64
	RoutingFailures             int64
	Attempts                    int64
	CostKnownRequests           int64
	ExpectedPlatformCostNanoUSD int64
	TTFTSampleCount             int64
	TTFT                        *routingdistribution.DurationSketch
}

type canaryEvaluationSchedule struct {
	mu              sync.Mutex
	completedWindow map[RolloutKey]int64
}

type pendingCanaryEvaluation struct {
	target        canaryEvaluationTarget
	windowStartMs int64
	windowEndMs   int64
}

var defaultCanaryEvaluationSchedule = canaryEvaluationSchedule{
	completedWindow: make(map[RolloutKey]int64),
}

func evaluateRoutingCanaryControlContext(
	ctx context.Context,
	setting smart_routing_setting.SmartRoutingSetting,
) error {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || snapshot.view.ActivationStage != model.RoutingDeploymentStageCanary {
		return nil
	}
	now := time.Now()
	lease, acquired, err := model.TryAcquireRoutingControlLeaseContext(
		ctx,
		routingCanaryEvaluatorLeaseName,
		NodeEpochID(),
		int64(canaryControlLeaseTTL/time.Millisecond),
		int64(canaryControlPollInterval/time.Millisecond),
		false,
	)
	if err != nil || !acquired {
		return err
	}

	evaluateErr := evaluateRoutingCanarySweepContext(ctx, setting, now)
	if evaluateErr != nil {
		releaseErr := model.ReleaseRoutingControlLeaseContext(ctx, lease)
		return errors.Join(evaluateErr, releaseErr)
	}
	return model.CompleteRoutingControlLeaseContext(ctx, lease)
}

func evaluateRoutingCanarySweepContext(
	ctx context.Context,
	_ smart_routing_setting.SmartRoutingSetting,
	now time.Time,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		return ErrCanaryControlInvalid
	}
	targets, err := currentCanaryEvaluationTargets()
	if err != nil || len(targets) == 0 {
		return err
	}
	defaultCanaryEvaluationSchedule.prune(targets)
	pending := make([]pendingCanaryEvaluation, 0, len(targets))
	for index := range targets {
		windows, windowErr := pendingCanaryEvaluationWindows(
			targets[index],
			now,
			defaultCanaryEvaluationSchedule.completedWindowEnd(targets[index].RolloutKey),
		)
		if windowErr != nil {
			return windowErr
		}
		pending = append(pending, windows...)
	}
	if len(pending) == 0 {
		return nil
	}
	nodeCheckpoints, invalidNodeCheckpoints, err := loadCanaryNodePresenceCheckpointsContext(
		ctx,
		pending[0].target,
		now,
	)
	if err != nil {
		return err
	}
	for index := range pending {
		activeNodes, activeErr := activeCanaryNodeIDsForWindow(
			nodeCheckpoints,
			pending[index].windowStartMs,
			pending[index].windowEndMs,
			now,
		)
		if activeErr != nil {
			return activeErr
		}
		invalidNodes, invalidErr := activeCanaryNodeIDsForWindow(
			invalidNodeCheckpoints,
			pending[index].windowStartMs,
			pending[index].windowEndMs,
			now,
		)
		if invalidErr != nil {
			return invalidErr
		}
		if err := evaluateRoutingCanaryTargetContext(
			ctx,
			pending[index].target,
			activeNodes,
			len(invalidNodes) > 0,
			pending[index].windowStartMs,
			pending[index].windowEndMs,
			now,
		); err != nil {
			return err
		}
		defaultCanaryEvaluationSchedule.markCompleted(pending[index].target.RolloutKey, pending[index].windowEndMs)
	}
	return nil
}

func currentCanaryEvaluationTargets() ([]canaryEvaluationTarget, error) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || snapshot.view.ActivationStage != model.RoutingDeploymentStageCanary {
		return nil, nil
	}
	view := snapshot.view
	if view.Revision == 0 || view.Revision > math.MaxInt64 || view.ActivationID <= 0 ||
		view.ActivationCreatedTime <= 0 || view.TrafficBasisPoints < model.RoutingPolicyCanaryMinBasisPoints ||
		view.TrafficBasisPoints > model.RoutingPolicyCanaryMaxBasisPoints || len(view.PolicyHash) != 64 {
		return nil, ErrCanaryControlInvalid
	}
	if view.ActivationCreatedTime > math.MaxInt64/1_000 {
		return nil, ErrCanaryControlInvalid
	}
	targets := make([]canaryEvaluationTarget, 0)
	for index := range view.Pools {
		pool := view.Pools[index]
		if pool.DeploymentStage != model.RoutingDeploymentStageCanary {
			continue
		}
		policy, err := model.NormalizeRoutingCanaryPolicy(pool.CanaryPolicy)
		if err != nil {
			return nil, err
		}
		rolloutKey, err := CanaryRolloutKey(pool.ID, view.ActivationID, view.Revision, view.TrafficBasisPoints)
		if err != nil {
			return nil, err
		}
		targets = append(targets, canaryEvaluationTarget{
			PolicyRevision:      int64(view.Revision),
			PolicyHash:          view.PolicyHash,
			ActivationID:        view.ActivationID,
			ActivationCreatedMs: view.ActivationCreatedTime * 1_000,
			PoolID:              pool.ID,
			TrafficBasisPoints:  view.TrafficBasisPoints,
			RolloutKey:          rolloutKey,
			Policy:              policy.Evaluation,
		})
	}
	sort.Slice(targets, func(left int, right int) bool {
		return targets[left].PoolID < targets[right].PoolID
	})
	return targets, nil
}

func pendingCanaryEvaluationWindows(
	target canaryEvaluationTarget,
	now time.Time,
	completedWindowEndMs int64,
) ([]pendingCanaryEvaluation, error) {
	latestWindowStartMs, latestWindowEndMs, exists, err := canaryEvaluationWindow(target, now)
	if err != nil || !exists {
		return nil, err
	}
	if completedWindowEndMs >= latestWindowEndMs {
		return nil, nil
	}
	windowMs := latestWindowEndMs - latestWindowStartMs
	catchUpWindows := target.Policy.ConsecutiveBreachWindows
	if windowMs <= 0 || catchUpWindows < 1 || catchUpWindows > canaryEvaluatorMaxCatchUpWindows ||
		target.ActivationCreatedMs <= 0 {
		return nil, ErrCanaryControlInvalid
	}
	lookbackMs := int64(catchUpWindows-1) * windowMs
	if lookbackMs < 0 || latestWindowEndMs <= lookbackMs {
		return nil, ErrCanaryControlInvalid
	}
	firstWindowEndMs := latestWindowEndMs - lookbackMs

	activationWindowStartMs := target.ActivationCreatedMs / windowMs * windowMs
	if activationWindowStartMs > math.MaxInt64-windowMs {
		return nil, ErrCanaryControlInvalid
	}
	firstActivationWindowEndMs := activationWindowStartMs + windowMs
	if firstWindowEndMs < firstActivationWindowEndMs {
		firstWindowEndMs = firstActivationWindowEndMs
	}
	if completedWindowEndMs > 0 {
		if completedWindowEndMs%windowMs != 0 || completedWindowEndMs > math.MaxInt64-windowMs {
			return nil, ErrCanaryControlInvalid
		}
		nextWindowEndMs := completedWindowEndMs + windowMs
		if firstWindowEndMs < nextWindowEndMs {
			firstWindowEndMs = nextWindowEndMs
		}
	}
	if firstWindowEndMs > latestWindowEndMs {
		return nil, nil
	}

	windowCount := int((latestWindowEndMs-firstWindowEndMs)/windowMs) + 1
	if windowCount < 1 || windowCount > canaryEvaluatorMaxCatchUpWindows {
		return nil, ErrCanaryControlInvalid
	}
	windows := make([]pendingCanaryEvaluation, 0, windowCount)
	for index := 0; index < windowCount; index++ {
		windowEndMs := firstWindowEndMs + int64(index)*windowMs
		windows = append(windows, pendingCanaryEvaluation{
			target:        target,
			windowStartMs: windowEndMs - windowMs,
			windowEndMs:   windowEndMs,
		})
	}
	return windows, nil
}

func activeCanaryNodeIDsForWindow(
	checkpoints []model.RoutingRuntimeCheckpoint,
	windowStartMs int64,
	windowEndMs int64,
	now time.Time,
) (map[string]struct{}, error) {
	if windowStartMs <= 0 || windowEndMs <= windowStartMs || now.IsZero() {
		return nil, ErrCanaryControlInvalid
	}
	windowEndUnix := windowEndMs / 1_000
	nowUnix := now.Unix()
	nodes := make(map[string]struct{}, len(checkpoints))
	for index := range checkpoints {
		checkpoint := checkpoints[index]
		if checkpoint.CreatedTime <= 0 || checkpoint.CreatedTime > windowEndUnix ||
			checkpoint.ObservedTime <= 0 || checkpoint.ObservedTime > nowUnix {
			continue
		}
		nodes[checkpoint.NodeID] = struct{}{}
	}
	return nodes, nil
}

func evaluateRoutingCanaryTargetContext(
	ctx context.Context,
	target canaryEvaluationTarget,
	activeNodes map[string]struct{},
	forceTelemetryGap bool,
	windowStartMs int64,
	windowEndMs int64,
	now time.Time,
) error {
	windowMs := int64(target.Policy.WindowSeconds) * 1_000
	_, latestWindowEndMs, exists, err := canaryEvaluationWindow(target, now)
	if err != nil || !exists {
		return err
	}
	if windowMs <= 0 || windowStartMs <= 0 || windowEndMs-windowStartMs != windowMs ||
		windowEndMs%windowMs != 0 || windowEndMs > latestWindowEndMs {
		return ErrCanaryControlInvalid
	}

	evaluation, err := model.GetRoutingCanaryEvaluationWindowContext(
		ctx,
		string(target.RolloutKey),
		target.PoolID,
		windowStartMs,
		windowEndMs,
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		aggregate, aggregateErr := loadCanaryWindowAggregateContext(
			ctx,
			target,
			activeNodes,
			forceTelemetryGap,
			windowStartMs,
			windowEndMs,
			now.Unix(),
		)
		if aggregateErr != nil {
			return aggregateErr
		}
		spec, buildErr := buildRoutingCanaryEvaluationSpec(target, aggregate, windowStartMs, windowEndMs)
		if buildErr != nil {
			return buildErr
		}
		evaluation, _, err = model.CreateRoutingCanaryEvaluationContext(ctx, spec)
	}
	if err != nil {
		return err
	}
	if evaluation.PolicyRevision != target.PolicyRevision || evaluation.ActivationID != target.ActivationID ||
		evaluation.PoolID != target.PoolID || evaluation.RolloutKey != string(target.RolloutKey) ||
		evaluation.WindowStartMs != windowStartMs || evaluation.WindowEndMs != windowEndMs {
		return ErrCanaryControlInvalid
	}
	return ensureCanaryRollbackOperationContext(ctx, target, evaluation)
}

func canaryEvaluationWindow(target canaryEvaluationTarget, now time.Time) (int64, int64, bool, error) {
	windowMs := int64(target.Policy.WindowSeconds) * 1_000
	latenessMs := int64(target.Policy.CheckpointLatenessSeconds) * 1_000
	settleMs := int64(canaryEvaluatorSettleDelay / time.Millisecond)
	nowMs := now.UnixMilli()
	if now.IsZero() || windowMs <= 0 || latenessMs < 0 || latenessMs > math.MaxInt64-settleMs ||
		nowMs <= latenessMs+settleMs {
		return 0, 0, false, ErrCanaryControlInvalid
	}
	closedAtMs := nowMs - latenessMs - settleMs
	windowEndMs := closedAtMs / windowMs * windowMs
	if windowEndMs <= windowMs {
		return 0, 0, false, nil
	}
	return windowEndMs - windowMs, windowEndMs, true, nil
}

func loadCanaryWindowAggregateContext(
	ctx context.Context,
	target canaryEvaluationTarget,
	activeNodes map[string]struct{},
	forceTelemetryGap bool,
	windowStartMs int64,
	windowEndMs int64,
	nowUnix int64,
) (canaryWindowAggregate, error) {
	scope := CanaryWindowCheckpointScope(CanaryCohortWindowCheckpoint{
		PoolID: target.PoolID, ActivationID: target.ActivationID,
		PolicyRevision: uint64(target.PolicyRevision), TrafficBasisPoints: target.TrafficBasisPoints,
		RolloutKey: target.RolloutKey, WindowEndUnixMs: windowEndMs,
	})
	checkpoints, err := listCanaryCheckpointsContext(ctx, CanaryCohortWindowCheckpointKind, scope, nowUnix)
	if err != nil {
		return canaryWindowAggregate{}, err
	}
	aggregate := canaryWindowAggregate{
		Control:       canaryCohortAggregate{TTFT: routingdistribution.NewDurationSketch()},
		Canary:        canaryCohortAggregate{TTFT: routingdistribution.NewDurationSketch()},
		ExpectedNodes: len(activeNodes),
	}
	reported := make(map[string]struct{}, len(checkpoints))
	for index := range checkpoints {
		checkpoint := checkpoints[index]
		if _, expected := activeNodes[checkpoint.NodeID]; !expected {
			continue
		}
		if _, duplicate := reported[checkpoint.NodeID]; duplicate || checkpoint.Sequence != 1 ||
			checkpoint.PolicyRevision != target.PolicyRevision || checkpoint.ObservedTime != windowEndMs/1_000 {
			continue
		}
		payload, decodeErr := DecodeCanaryCohortWindowCheckpoint(checkpoint)
		if decodeErr != nil || payload.PoolID != target.PoolID || payload.ActivationID != target.ActivationID ||
			payload.PolicyRevision != uint64(target.PolicyRevision) || payload.TrafficBasisPoints != target.TrafficBasisPoints ||
			payload.RolloutKey != target.RolloutKey || payload.WindowSeconds != target.Policy.WindowSeconds ||
			payload.WindowStartUnixMs != windowStartMs || payload.WindowEndUnixMs != windowEndMs {
			continue
		}
		if err := mergeCanaryCohortAggregate(&aggregate.Control, payload.Control); err != nil {
			return canaryWindowAggregate{}, err
		}
		if err := mergeCanaryCohortAggregate(&aggregate.Canary, payload.Canary); err != nil {
			return canaryWindowAggregate{}, err
		}
		reported[checkpoint.NodeID] = struct{}{}
	}
	aggregate.ReportedNodes = len(reported)
	aggregate.NodeCoverageBasisPoints = canaryBasisPoints(int64(aggregate.ReportedNodes), int64(aggregate.ExpectedNodes))
	if forceTelemetryGap {
		aggregate.NodeCoverageBasisPoints = 0
	}
	return aggregate, nil
}

func listCanaryCheckpointsContext(
	ctx context.Context,
	kind string,
	scope string,
	nowUnix int64,
) ([]model.RoutingRuntimeCheckpoint, error) {
	checkpoints := make([]model.RoutingRuntimeCheckpoint, 0, canaryEvaluatorPageSize)
	beforeObservedTime := int64(0)
	beforeID := int64(0)
	for {
		page, hasMore, err := model.ListActiveRoutingRuntimeCheckpointsContext(
			ctx, kind, scope, nowUnix, beforeObservedTime, beforeID, canaryEvaluatorPageSize,
		)
		if err != nil {
			return nil, err
		}
		if len(checkpoints) > canaryEvaluatorMaxNodes-len(page) {
			return nil, ErrCanaryControlInvalid
		}
		checkpoints = append(checkpoints, page...)
		if !hasMore {
			return checkpoints, nil
		}
		if len(page) == 0 {
			return nil, ErrCanaryControlInvalid
		}
		last := page[len(page)-1]
		beforeObservedTime = last.ObservedTime
		beforeID = last.ID
	}
}

func mergeCanaryCohortAggregate(target *canaryCohortAggregate, stats CanaryCohortWindowStats) error {
	if target == nil || target.TTFT == nil || !validFrozenCanaryCohort(stats) {
		return ErrCanaryControlInvalid
	}
	values := []*int64{
		&target.LogicalRequests,
		&target.Successes,
		&target.Failures,
		&target.RoutingFailures,
		&target.Attempts,
		&target.CostKnownRequests,
		&target.ExpectedPlatformCostNanoUSD,
		&target.TTFTSampleCount,
	}
	deltas := []int64{
		stats.LogicalRequests,
		stats.Successes,
		stats.Failures,
		stats.RoutingFailures,
		stats.Attempts,
		stats.CostKnownRequests,
		stats.ExpectedPlatformCostNanoUSD,
		stats.TTFTSampleCount,
	}
	for index := range values {
		if deltas[index] < 0 || *values[index] > math.MaxInt64-deltas[index] {
			return ErrCanaryWindowOverflow
		}
	}
	if stats.TTFTSampleCount > 0 {
		sketch, err := routingdistribution.DecodeDurationSketch(stats.TTFTSketch, stats.TTFTSketchCodecVersion)
		if err != nil || sketch.Count() != stats.TTFTSampleCount {
			return ErrCanaryControlInvalid
		}
		if err := target.TTFT.Merge(sketch); err != nil {
			return err
		}
	}
	for index := range values {
		*values[index] += deltas[index]
	}
	if target.TTFT.Count() != target.TTFTSampleCount {
		return ErrCanaryControlInvalid
	}
	return nil
}

func buildRoutingCanaryEvaluationSpec(
	target canaryEvaluationTarget,
	aggregate canaryWindowAggregate,
	windowStartMs int64,
	windowEndMs int64,
) (model.RoutingCanaryEvaluationSpec, error) {
	control, controlP95, err := canaryCohortEvaluationMetrics(aggregate.Control)
	if err != nil {
		return model.RoutingCanaryEvaluationSpec{}, err
	}
	canary, canaryP95, err := canaryCohortEvaluationMetrics(aggregate.Canary)
	if err != nil {
		return model.RoutingCanaryEvaluationSpec{}, err
	}
	controlRate := canaryBasisPoints(control.SuccessCount, control.RequestCount)
	canaryRate := canaryBasisPoints(canary.SuccessCount, canary.RequestCount)
	costCoverage := min(
		canaryBasisPoints(control.CostSampleCount, control.RequestCount),
		canaryBasisPoints(canary.CostSampleCount, canary.RequestCount),
	)
	ttftRatio, ttftRatioKnown := canaryMetricRatioBasisPoints(canaryP95, controlP95)
	costRatio, costRatioKnown := canaryAverageRatioBasisPoints(
		aggregate.Canary.ExpectedPlatformCostNanoUSD,
		aggregate.Canary.CostKnownRequests,
		aggregate.Control.ExpectedPlatformCostNanoUSD,
		aggregate.Control.CostKnownRequests,
	)
	retryRatio, retryRatioKnown := canaryAverageRatioBasisPoints(
		canary.RetryCount,
		canary.RequestCount,
		control.RetryCount,
		control.RequestCount,
	)

	spec := model.RoutingCanaryEvaluationSpec{
		PolicyRevision:                     target.PolicyRevision,
		ActivationID:                       target.ActivationID,
		PoolID:                             target.PoolID,
		RolloutKey:                         string(target.RolloutKey),
		WindowStartMs:                      windowStartMs,
		WindowEndMs:                        windowEndMs,
		Control:                            control,
		Canary:                             canary,
		NodeCoverageBasisPoints:            aggregate.NodeCoverageBasisPoints,
		CostCoverageBasisPoints:            costCoverage,
		ControlSuccessRateBasisPoints:      controlRate,
		CanarySuccessRateBasisPoints:       canaryRate,
		SuccessRateDropBasisPoints:         controlRate - canaryRate,
		P95TTFTRatioBasisPoints:            ttftRatio,
		P95TTFTDeltaMilliseconds:           canaryP95 - controlP95,
		CostRatioBasisPoints:               costRatio,
		RetryAmplificationRatioBasisPoints: retryRatio,
		TrafficBasisPoints:                 target.TrafficBasisPoints,
	}

	graceMs := int64(target.Policy.RolloutGraceSeconds) * 1_000
	if target.ActivationCreatedMs > math.MaxInt64-graceMs {
		return model.RoutingCanaryEvaluationSpec{}, ErrCanaryControlInvalid
	}
	if windowStartMs < target.ActivationCreatedMs+graceMs {
		spec.Status = model.RoutingCanaryEvaluationStatusRolloutGrace
		spec.Reason = "window overlaps rollout grace"
		return spec, nil
	}
	if aggregate.NodeCoverageBasisPoints < target.Policy.MinNodeCoverageBasisPoints {
		if target.Policy.RollbackOnTelemetryGap {
			spec.Status = model.RoutingCanaryEvaluationStatusBreached
			spec.Reason = "node checkpoint coverage below policy"
		} else {
			spec.Status = model.RoutingCanaryEvaluationStatusInconclusive
			spec.Reason = "node checkpoint coverage is incomplete"
		}
		return spec, nil
	}
	if canary.RequestCount < int64(target.Policy.MinCanaryRequests) ||
		control.RequestCount < int64(target.Policy.MinControlRequests) {
		spec.Status = model.RoutingCanaryEvaluationStatusInconclusive
		spec.Reason = "logical request sample count below policy"
		return spec, nil
	}
	if canary.TTFTSampleCount < int64(target.Policy.MinTTFTSamples) ||
		control.TTFTSampleCount < int64(target.Policy.MinTTFTSamples) || !ttftRatioKnown {
		spec.Status = model.RoutingCanaryEvaluationStatusInconclusive
		spec.Reason = "ttft sample count below policy"
		return spec, nil
	}
	if costCoverage < target.Policy.MinCostCoverageBasisPoints ||
		(target.Policy.MinCostCoverageBasisPoints > 0 && !costRatioKnown) {
		spec.Status = model.RoutingCanaryEvaluationStatusInconclusive
		spec.Reason = "cost coverage below policy"
		return spec, nil
	}
	if !retryRatioKnown {
		return model.RoutingCanaryEvaluationSpec{}, ErrCanaryControlInvalid
	}

	breaches := make([]string, 0, 5)
	controlLower, _, controlWilsonKnown := canaryWilsonInterval(control.SuccessCount, control.RequestCount)
	_, canaryUpper, canaryWilsonKnown := canaryWilsonInterval(canary.SuccessCount, canary.RequestCount)
	if !controlWilsonKnown || !canaryWilsonKnown {
		return model.RoutingCanaryEvaluationSpec{}, ErrCanaryControlInvalid
	}
	if canaryUpper*10_000 < float64(target.Policy.HardMinSuccessRateBasisPoints) {
		breaches = append(breaches, "hard success-rate floor breached")
	}
	if (controlLower-canaryUpper)*10_000 > float64(target.Policy.MaxSuccessRateDropBasisPoints) {
		breaches = append(breaches, "success-rate regression breached")
	}
	if ttftRatio > int64(target.Policy.MaxP95TTFTRatioBasisPoints) &&
		spec.P95TTFTDeltaMilliseconds >= float64(target.Policy.MinP95TTFTDeltaMilliseconds) {
		breaches = append(breaches, "p95 ttft regression breached")
	}
	if costRatioKnown && costRatio > int64(target.Policy.MaxCostRatioBasisPoints) {
		breaches = append(breaches, "expected cost ratio breached")
	}
	if retryRatio > int64(target.Policy.MaxRetryAmplificationRatioBasisPoints) {
		breaches = append(breaches, "retry amplification breached")
	}
	if len(breaches) == 0 {
		spec.Status = model.RoutingCanaryEvaluationStatusPassed
		spec.Reason = "canary window passed"
		return spec, nil
	}
	spec.Status = model.RoutingCanaryEvaluationStatusBreached
	spec.Reason = strings.Join(breaches, "; ")
	return spec, nil
}

func canaryCohortEvaluationMetrics(
	aggregate canaryCohortAggregate,
) (model.RoutingCanaryCohortMetrics, float64, error) {
	if aggregate.LogicalRequests < 0 || aggregate.RoutingFailures < 0 ||
		aggregate.LogicalRequests < aggregate.RoutingFailures ||
		aggregate.TTFT == nil || aggregate.TTFT.Count() != aggregate.TTFTSampleCount {
		return model.RoutingCanaryCohortMetrics{}, 0, ErrCanaryControlInvalid
	}
	attemptedRequests := aggregate.LogicalRequests - aggregate.RoutingFailures
	if aggregate.Attempts < attemptedRequests {
		return model.RoutingCanaryCohortMetrics{}, 0, ErrCanaryControlInvalid
	}
	p95 := float64(0)
	if aggregate.TTFTSampleCount > 0 {
		quantile, err := aggregate.TTFT.Quantile(0.95)
		if err != nil || !quantile.Known {
			return model.RoutingCanaryCohortMetrics{}, 0, ErrCanaryControlInvalid
		}
		p95 = quantile.ValueMilliseconds
	}
	retries := aggregate.Attempts - attemptedRequests
	metrics := model.RoutingCanaryCohortMetrics{
		RequestCount:        aggregate.LogicalRequests,
		SuccessCount:        aggregate.Successes,
		TTFTSampleCount:     aggregate.TTFTSampleCount,
		P95TTFTMilliseconds: p95,
		CostSampleCount:     aggregate.CostKnownRequests,
		ExpectedCostTotal:   float64(aggregate.ExpectedPlatformCostNanoUSD) / 1_000_000_000,
		AttemptCount:        aggregate.Attempts,
		RetryCount:          retries,
	}
	return metrics, p95, nil
}

func canaryMetricRatioBasisPoints(numerator float64, denominator float64) (int64, bool) {
	if numerator == 0 && denominator == 0 {
		return 10_000, true
	}
	if denominator == 0 && numerator > 0 && !math.IsNaN(numerator) && !math.IsInf(numerator, 0) {
		return math.MaxInt64, true
	}
	return canaryRatioBasisPoints(numerator, denominator)
}

func canaryAverageRatioBasisPoints(
	numeratorTotal int64,
	numeratorCount int64,
	denominatorTotal int64,
	denominatorCount int64,
) (int64, bool) {
	if numeratorTotal < 0 || numeratorCount <= 0 || denominatorTotal < 0 || denominatorCount <= 0 {
		return 0, false
	}
	numerator := float64(numeratorTotal) / float64(numeratorCount)
	denominator := float64(denominatorTotal) / float64(denominatorCount)
	return canaryMetricRatioBasisPoints(numerator, denominator)
}

func ensureCanaryRollbackOperationContext(
	ctx context.Context,
	target canaryEvaluationTarget,
	evaluation model.RoutingCanaryEvaluation,
) error {
	if evaluation.Status != model.RoutingCanaryEvaluationStatusBreached || !target.Policy.AutoRollbackEnabled {
		return nil
	}
	required := target.Policy.ConsecutiveBreachWindows
	if required < 1 || required > 10 {
		return ErrCanaryControlInvalid
	}
	consecutive := 1
	expectedEndMs := evaluation.WindowStartMs
	if required > 1 {
		previous, err := model.ListRoutingCanaryEvaluationsBeforeContext(
			ctx,
			evaluation.RolloutKey,
			evaluation.PoolID,
			evaluation.WindowEndMs,
			required-1,
		)
		if err != nil {
			return err
		}
		for index := range previous {
			item := previous[index]
			if item.WindowEndMs != expectedEndMs || item.Status != model.RoutingCanaryEvaluationStatusBreached ||
				item.WindowEndMs-item.WindowStartMs != evaluation.WindowEndMs-evaluation.WindowStartMs {
				break
			}
			consecutive++
			expectedEndMs = item.WindowStartMs
		}
	}
	if consecutive < required {
		return nil
	}
	_, _, err := model.CreateRoutingOperationContext(ctx, model.RoutingOperationSpec{
		Type:                 model.RoutingOperationTypeCanaryAutoRollback,
		EvaluationHash:       evaluation.EvaluationHash,
		PoolID:               evaluation.PoolID,
		ExpectedRevision:     evaluation.PolicyRevision,
		ExpectedActivationID: evaluation.ActivationID,
		ActorID:              0,
		Reason:               evaluation.Reason,
	})
	return err
}

func executeRoutingCanaryOperationContext(
	ctx context.Context,
	_ smart_routing_setting.SmartRoutingSetting,
) error {
	now := time.Now()
	operationLease, acquired, err := model.TryAcquireRoutingControlLeaseContext(
		ctx,
		routingCanaryOperationLeaseName,
		NodeEpochID(),
		int64(canaryControlLeaseTTL/time.Millisecond),
		0,
		false,
	)
	if err != nil || !acquired {
		return err
	}
	hasRunnableOperation, err := model.HasRunnableRoutingOperationContext(
		ctx,
		model.RoutingOperationTypeCanaryAutoRollback,
		now.UnixMilli(),
	)
	if err != nil || !hasRunnableOperation {
		releaseErr := model.ReleaseRoutingControlLeaseContext(ctx, operationLease)
		return errors.Join(err, releaseErr)
	}
	// Freeze evaluator writes while the rollback transaction snapshots every breached pool in this rollout.
	evaluatorLease, acquired, err := model.TryAcquireRoutingControlLeaseContext(
		ctx,
		routingCanaryEvaluatorLeaseName,
		NodeEpochID(),
		int64(canaryControlLeaseTTL/time.Millisecond),
		0,
		true,
	)
	if err != nil || !acquired {
		releaseErr := model.ReleaseRoutingControlLeaseContext(ctx, operationLease)
		return errors.Join(err, releaseErr)
	}
	operation, err := model.ClaimRoutingOperationContext(
		ctx,
		model.RoutingOperationTypeCanaryAutoRollback,
		now.UnixMilli(),
		int64(canaryOperationClaimTTL/time.Millisecond),
	)
	if err != nil {
		operationReleaseErr := model.ReleaseRoutingControlLeaseContext(ctx, operationLease)
		evaluatorReleaseErr := model.ReleaseRoutingControlLeaseContext(ctx, evaluatorLease)
		return errors.Join(err, operationReleaseErr, evaluatorReleaseErr)
	}
	if operation == nil {
		operationReleaseErr := model.ReleaseRoutingControlLeaseContext(ctx, operationLease)
		evaluatorReleaseErr := model.ReleaseRoutingControlLeaseContext(ctx, evaluatorLease)
		return errors.Join(operationReleaseErr, evaluatorReleaseErr)
	}

	_, operationErr := model.AutoRollbackRoutingCanaryPoolContext(ctx, model.RoutingCanaryAutoRollbackRequest{
		Operation:            *operation,
		Lease:                evaluatorLease,
		ExpectedRevision:     operation.ExpectedRevision,
		ExpectedActivationID: operation.ExpectedActivationID,
		PoolID:               operation.PoolID,
		NowMs:                time.Now().UnixMilli(),
	})
	if operationErr == nil {
		operationCompleteErr := model.CompleteRoutingControlLeaseContext(ctx, operationLease)
		evaluatorReleaseErr := model.ReleaseRoutingControlLeaseContext(ctx, evaluatorLease)
		return errors.Join(operationCompleteErr, evaluatorReleaseErr)
	}

	transitionErr := transitionFailedCanaryOperationContext(ctx, *operation, operationErr, time.Now())
	operationReleaseErr := model.ReleaseRoutingControlLeaseContext(ctx, operationLease)
	evaluatorReleaseErr := model.ReleaseRoutingControlLeaseContext(ctx, evaluatorLease)
	return errors.Join(operationErr, transitionErr, operationReleaseErr, evaluatorReleaseErr)
}

func transitionFailedCanaryOperationContext(
	ctx context.Context,
	operation model.RoutingOperation,
	operationErr error,
	now time.Time,
) error {
	if errors.Is(operationErr, model.ErrRoutingControlLeaseLost) ||
		errors.Is(operationErr, model.ErrRoutingOperationClaimLost) ||
		errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded) {
		return nil
	}
	nowMs := now.UnixMilli()
	if errors.Is(operationErr, model.ErrRoutingCanaryAutoRollbackInvalid) ||
		errors.Is(operationErr, model.ErrRoutingCanaryEvaluationInvalid) ||
		errors.Is(operationErr, model.ErrRoutingPolicyContentCorrupt) ||
		errors.Is(operationErr, model.ErrRoutingPolicyInvalid) {
		return model.FailRoutingOperationContext(ctx, operation.ID, operation.ClaimToken, nowMs, operationErr)
	}
	delay := time.Second
	for attempt := 1; attempt < operation.Attempts && delay < 5*time.Minute; attempt++ {
		if delay > 5*time.Minute/2 {
			delay = 5 * time.Minute
			break
		}
		delay *= 2
	}
	if nowMs > math.MaxInt64-delay.Milliseconds() {
		return fmt.Errorf("%w: retry timestamp overflow", ErrCanaryControlInvalid)
	}
	return model.RetryRoutingOperationContext(
		ctx,
		operation.ID,
		operation.ClaimToken,
		nowMs,
		nowMs+delay.Milliseconds(),
		operationErr,
	)
}

func (schedule *canaryEvaluationSchedule) prune(targets []canaryEvaluationTarget) {
	if schedule == nil {
		return
	}
	active := make(map[RolloutKey]struct{}, len(targets))
	for index := range targets {
		active[targets[index].RolloutKey] = struct{}{}
	}
	schedule.mu.Lock()
	if schedule.completedWindow == nil {
		schedule.completedWindow = make(map[RolloutKey]int64, len(active))
	}
	for rolloutKey := range schedule.completedWindow {
		if _, exists := active[rolloutKey]; !exists {
			delete(schedule.completedWindow, rolloutKey)
		}
	}
	schedule.mu.Unlock()
}

func (schedule *canaryEvaluationSchedule) completedWindowEnd(rolloutKey RolloutKey) int64 {
	if schedule == nil || rolloutKey == "" {
		return 0
	}
	schedule.mu.Lock()
	completedWindowEndMs := schedule.completedWindow[rolloutKey]
	schedule.mu.Unlock()
	return completedWindowEndMs
}

func (schedule *canaryEvaluationSchedule) markCompleted(rolloutKey RolloutKey, windowEndMs int64) {
	if schedule == nil || rolloutKey == "" || windowEndMs <= 0 {
		return
	}
	schedule.mu.Lock()
	if schedule.completedWindow == nil {
		schedule.completedWindow = make(map[RolloutKey]int64)
	}
	if windowEndMs > schedule.completedWindow[rolloutKey] {
		schedule.completedWindow[rolloutKey] = windowEndMs
	}
	schedule.mu.Unlock()
}

func ResetCanaryControlForTest() {
	defaultCanaryEvaluationSchedule.mu.Lock()
	defaultCanaryEvaluationSchedule.completedWindow = make(map[RolloutKey]int64)
	defaultCanaryEvaluationSchedule.mu.Unlock()
}
