package channelrouting

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"gorm.io/gorm"
)

const (
	ErrorBudgetStatusHealthy          = "healthy"
	ErrorBudgetStatusWarning          = "warning"
	ErrorBudgetStatusCritical         = "critical"
	ErrorBudgetStatusInsufficientData = "insufficient_data"

	errorBudgetFastBurnThreshold = 14.4
	errorBudgetSlowBurnThreshold = 6.0
	errorBudgetEvaluationLease   = "routing-error-budget-evaluator"
	errorBudgetPublisherLease    = "routing-error-budget-publisher"
	errorBudgetEvaluationTTL     = 90 * time.Second
	errorBudgetPublisherTTL      = 90 * time.Second
	errorBudgetEvaluationPeriod  = 30 * time.Second
	errorBudgetEvaluationBatch   = 64
	errorBudgetPublisherBatch    = 128
	errorBudgetPublisherMaxBatch = 16
)

var ErrErrorBudgetInvalid = errors.New("invalid channel routing error budget request")
var ErrErrorBudgetRevisionRequired = errors.New("channel routing error budget requires an explicit policy revision")

type ErrorBudgetWindow struct {
	WindowSeconds          int64   `json:"window_seconds"`
	RequestCount           int64   `json:"request_count"`
	FailureCount           int64   `json:"failure_count"`
	UnisolatedRequestCount int64   `json:"unisolated_request_count"`
	UnisolatedFailureCount int64   `json:"unisolated_failure_count"`
	RevisionIsolated       bool    `json:"revision_isolated"`
	ErrorRate              float64 `json:"error_rate"`
	BurnRate               float64 `json:"burn_rate"`
	MinimumVolume          int64   `json:"minimum_volume"`
	Sufficient             bool    `json:"sufficient"`
}

type ErrorBudgetBurn struct {
	PoolID             int               `json:"pool_id"`
	PolicyRevision     int64             `json:"policy_revision"`
	AvailabilityTarget float64           `json:"availability_target"`
	ErrorBudget        float64           `json:"error_budget"`
	Status             string            `json:"status"`
	Reason             string            `json:"reason"`
	FastShort          ErrorBudgetWindow `json:"fast_short"`
	FastLong           ErrorBudgetWindow `json:"fast_long"`
	SlowShort          ErrorBudgetWindow `json:"slow_short"`
	SlowLong           ErrorBudgetWindow `json:"slow_long"`
	EvaluatedAtMs      int64             `json:"evaluated_at_ms"`
}

type ErrorBudgetState struct {
	Evaluation     ErrorBudgetBurn `json:"evaluation"`
	PolicyRevision int64           `json:"policy_revision"`
	FirstObserved  int64           `json:"first_observed_at_ms"`
	LastChanged    int64           `json:"last_changed_at_ms"`
	Persisted      bool            `json:"persisted"`
}

func EvaluateErrorBudgetBurnContext(
	ctx context.Context,
	poolID int,
	availabilityTarget float64,
	now time.Time,
) (ErrorBudgetBurn, error) {
	snapshot := currentSnapshot.Load()
	if snapshot == nil || snapshot.view.Revision == 0 || snapshot.view.Revision > math.MaxInt64 {
		return ErrorBudgetBurn{}, ErrErrorBudgetRevisionRequired
	}
	return EvaluateErrorBudgetBurnForRevisionContext(
		ctx, poolID, int64(snapshot.view.Revision), availabilityTarget, now,
	)
}

func EvaluateErrorBudgetBurnForRevisionContext(
	ctx context.Context,
	poolID int,
	policyRevision int64,
	availabilityTarget float64,
	now time.Time,
) (ErrorBudgetBurn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if poolID <= 0 || policyRevision <= 0 || now.IsZero() ||
		math.IsNaN(availabilityTarget) || math.IsInf(availabilityTarget, 0) ||
		availabilityTarget <= 0 || availabilityTarget >= 1 {
		return ErrorBudgetBurn{}, ErrErrorBudgetInvalid
	}
	errorBudget := 1 - availabilityTarget
	result := ErrorBudgetBurn{
		PoolID: poolID, PolicyRevision: policyRevision,
		AvailabilityTarget: availabilityTarget, ErrorBudget: errorBudget,
		Status: ErrorBudgetStatusInsufficientData, Reason: "insufficient_reliability_volume",
		EvaluatedAtMs: now.UnixMilli(),
	}
	windows := []struct {
		duration time.Duration
		minimum  int64
		target   *ErrorBudgetWindow
	}{
		{duration: 5 * time.Minute, minimum: 20, target: &result.FastShort},
		{duration: time.Hour, minimum: 100, target: &result.FastLong},
		{duration: 30 * time.Minute, minimum: 50, target: &result.SlowShort},
		{duration: 6 * time.Hour, minimum: 300, target: &result.SlowLong},
	}
	revisionIsolationUnavailable := false
	for _, window := range windows {
		aggregate, err := model.AggregateRoutingMetricReliabilityByRevisionContext(
			ctx, poolID, policyRevision, now.Add(-window.duration).Unix(), now.Unix()+1,
		)
		if err != nil {
			return ErrorBudgetBurn{}, err
		}
		*window.target = errorBudgetWindow(window.duration, window.minimum, errorBudget, aggregate)
		if !aggregate.RevisionIsolated {
			revisionIsolationUnavailable = true
		}
	}
	if revisionIsolationUnavailable {
		result.Reason = "revision_isolation_unavailable"
		return result, nil
	}

	fastTriggered := result.FastShort.Sufficient && result.FastLong.Sufficient &&
		result.FastShort.BurnRate >= errorBudgetFastBurnThreshold && result.FastLong.BurnRate >= errorBudgetFastBurnThreshold
	if fastTriggered {
		result.Status = ErrorBudgetStatusCritical
		result.Reason = "fast_multi_window_burn"
		return result, nil
	}
	slowTriggered := result.SlowShort.Sufficient && result.SlowLong.Sufficient &&
		result.SlowShort.BurnRate >= errorBudgetSlowBurnThreshold && result.SlowLong.BurnRate >= errorBudgetSlowBurnThreshold
	if slowTriggered {
		result.Status = ErrorBudgetStatusWarning
		result.Reason = "slow_multi_window_burn"
		return result, nil
	}
	if (result.FastShort.Sufficient && result.FastLong.Sufficient) ||
		(result.SlowShort.Sufficient && result.SlowLong.Sufficient) {
		result.Status = ErrorBudgetStatusHealthy
		result.Reason = "within_multi_window_budget"
	}
	return result, nil
}

func EvaluateAndPersistErrorBudgetContext(
	ctx context.Context,
	lease model.RoutingControlLease,
	poolID int,
	policyRevision int64,
	availabilityTarget float64,
	now time.Time,
) (ErrorBudgetState, bool, error) {
	state, transition, err := EvaluateAndPersistErrorBudgetWithTransitionContext(
		ctx, lease, poolID, policyRevision, availabilityTarget, now,
	)
	return state, transition != nil, err
}

func EvaluateAndPersistErrorBudgetWithTransitionContext(
	ctx context.Context,
	lease model.RoutingControlLease,
	poolID int,
	policyRevision int64,
	availabilityTarget float64,
	now time.Time,
) (ErrorBudgetState, *model.RoutingErrorBudgetTransition, error) {
	evaluation, err := EvaluateErrorBudgetBurnForRevisionContext(
		ctx, poolID, policyRevision, availabilityTarget, now,
	)
	if err != nil {
		return ErrorBudgetState{}, nil, err
	}
	if policyRevision <= 0 {
		return ErrorBudgetState{}, nil, ErrErrorBudgetInvalid
	}
	payload, err := common.Marshal(evaluation)
	if err != nil {
		return ErrorBudgetState{}, nil, err
	}
	nowSeconds := now.Unix()
	nowMilliseconds := now.UnixMilli()
	state := model.RoutingErrorBudgetState{
		PoolID: poolID, PolicyRevision: policyRevision, AvailabilityTarget: availabilityTarget,
		Status: evaluation.Status, Reason: evaluation.Reason, EvaluationJSON: string(payload),
		LeaseFencingToken: lease.FencingToken,
		FirstObservedAtMs: nowMilliseconds, LastEvaluatedAtMs: nowMilliseconds,
		LastChangedAtMs: nowMilliseconds, CreatedTime: nowSeconds, UpdatedTime: nowSeconds,
	}
	transition, err := model.UpsertRoutingErrorBudgetStateWithTransitionContext(ctx, lease, &state)
	if err != nil {
		return ErrorBudgetState{}, nil, err
	}
	return ErrorBudgetState{
		Evaluation: evaluation, PolicyRevision: policyRevision,
		FirstObserved: state.FirstObservedAtMs, LastChanged: state.LastChangedAtMs, Persisted: true,
	}, transition, nil
}

func GetErrorBudgetStateContext(ctx context.Context, poolID int) (ErrorBudgetState, error) {
	state, err := model.GetRoutingErrorBudgetStateContext(ctx, poolID)
	if err != nil {
		return ErrorBudgetState{}, err
	}
	return errorBudgetStateFromModel(state)
}

func GetErrorBudgetStateForRevisionContext(
	ctx context.Context,
	poolID int,
	policyRevision int64,
) (ErrorBudgetState, error) {
	state, err := model.GetRoutingErrorBudgetStateForRevisionContext(ctx, poolID, policyRevision)
	if err != nil {
		return ErrorBudgetState{}, err
	}
	return errorBudgetStateFromModel(state)
}

func errorBudgetStateFromModel(state model.RoutingErrorBudgetState) (ErrorBudgetState, error) {
	var evaluation ErrorBudgetBurn
	if err := common.UnmarshalJsonStr(state.EvaluationJSON, &evaluation); err != nil ||
		evaluation.PoolID != state.PoolID || evaluation.Status != state.Status ||
		evaluation.PolicyRevision != state.PolicyRevision || evaluation.Reason != state.Reason ||
		evaluation.EvaluatedAtMs != state.LastEvaluatedAtMs {
		return ErrorBudgetState{}, ErrErrorBudgetInvalid
	}
	return ErrorBudgetState{
		Evaluation: evaluation, PolicyRevision: state.PolicyRevision,
		FirstObserved: state.FirstObservedAtMs, LastChanged: state.LastChangedAtMs, Persisted: true,
	}, nil
}

func EvaluateEnterpriseErrorBudgetsContext(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) error {
	_, err := EvaluateEnterpriseErrorBudgetsWithTransitionsContext(ctx, setting)
	return err
}

func RunEnterpriseErrorBudgetCycleContext(ctx context.Context, setting smart_routing_setting.SmartRoutingSetting) error {
	_, publishBeforeErr := PublishPendingErrorBudgetTransitionsContext(ctx)
	var evaluateErr error
	if setting.Enabled {
		_, evaluateErr = EvaluateEnterpriseErrorBudgetsWithTransitionsContext(ctx, setting)
	}
	_, publishAfterErr := PublishPendingErrorBudgetTransitionsContext(ctx)
	return errors.Join(publishBeforeErr, evaluateErr, publishAfterErr)
}

func EvaluateEnterpriseErrorBudgetsWithTransitionsContext(
	ctx context.Context,
	setting smart_routing_setting.SmartRoutingSetting,
) ([]model.RoutingErrorBudgetTransition, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !setting.Enabled {
		return nil, nil
	}
	lease, acquired, err := model.TryAcquireRoutingControlLeaseContext(
		ctx, errorBudgetEvaluationLease, NodeEpochID(),
		int64(errorBudgetEvaluationTTL/time.Millisecond), int64(errorBudgetEvaluationPeriod/time.Millisecond), false,
	)
	if err != nil || !acquired {
		return nil, err
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = model.ReleaseRoutingControlLeaseContext(releaseCtx, lease)
	}()

	cursor, err := model.GetRoutingErrorBudgetCursorContext(ctx, model.RoutingErrorBudgetEvaluatorCursor)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		cursor = model.RoutingErrorBudgetCursor{CursorName: model.RoutingErrorBudgetEvaluatorCursor}
	}
	if cursor.PolicyRevision == 0 {
		snapshot := currentSnapshot.Load()
		if snapshot == nil || snapshot.view.Revision == 0 || snapshot.view.Revision > math.MaxInt64 {
			if err := model.CompleteRoutingControlLeaseContext(ctx, lease); err != nil {
				return nil, err
			}
			completed = true
			return nil, nil
		}
		cursor.PolicyRevision = int64(snapshot.view.Revision)
		cursor.PositionID = 0
		cursor.LeaseName = lease.LeaseName
		cursor.LeaseFencingToken = lease.FencingToken
		cursor, err = model.SetRoutingErrorBudgetCursorContext(ctx, lease, cursor)
		if err != nil {
			return nil, err
		}
	}
	if cursor.PositionID > math.MaxInt32 {
		return nil, model.ErrRoutingErrorBudgetCursorInvalid
	}
	targets, err := loadEnterpriseErrorBudgetTargetsContext(ctx, cursor.PolicyRevision, int(cursor.PositionID))
	if err != nil {
		return nil, err
	}
	batchSize := min(len(targets), errorBudgetEvaluationBatch)
	transitions := make([]model.RoutingErrorBudgetTransition, 0, batchSize)
	for index := 0; index < batchSize; index++ {
		lease, err = model.RenewRoutingControlLeaseContext(ctx, lease, int64(errorBudgetEvaluationTTL/time.Millisecond))
		if err != nil {
			return nil, err
		}
		target := targets[index]
		_, transition, err := EvaluateAndPersistErrorBudgetWithTransitionContext(
			ctx, lease, target.poolID, cursor.PolicyRevision, target.availabilityTarget, time.UnixMilli(lease.UpdatedTimeMs),
		)
		if err != nil && !errors.Is(err, model.ErrRoutingErrorBudgetStateStale) {
			return nil, err
		}
		if transition != nil {
			transitions = append(transitions, *transition)
		}
		cursor.PositionID = int64(target.poolID)
		cursor.LeaseName = lease.LeaseName
		cursor.LeaseFencingToken = lease.FencingToken
		cursor, err = model.SetRoutingErrorBudgetCursorContext(ctx, lease, cursor)
		if err != nil {
			return nil, err
		}
	}
	if len(targets) <= errorBudgetEvaluationBatch {
		cursor.PolicyRevision = 0
		cursor.PositionID = 0
		cursor.LeaseName = lease.LeaseName
		cursor.LeaseFencingToken = lease.FencingToken
		if _, err := model.SetRoutingErrorBudgetCursorContext(ctx, lease, cursor); err != nil {
			return nil, err
		}
	}
	if err := model.CompleteRoutingControlLeaseContext(ctx, lease); err != nil {
		return nil, err
	}
	completed = true
	return transitions, nil
}

type enterpriseErrorBudgetTarget struct {
	poolID             int
	availabilityTarget float64
}

func loadEnterpriseErrorBudgetTargetsContext(
	ctx context.Context,
	policyRevision int64,
	afterPoolID int,
) ([]enterpriseErrorBudgetTarget, error) {
	if policyRevision <= 0 || afterPoolID < 0 {
		return nil, ErrErrorBudgetInvalid
	}
	document, revision, err := model.LoadRoutingPolicyRevisionContext(ctx, policyRevision)
	if err != nil {
		return nil, err
	}
	if revision.Revision != policyRevision {
		return nil, ErrErrorBudgetRevisionRequired
	}
	targets := make([]enterpriseErrorBudgetTarget, 0, len(document.Pools))
	for _, pool := range document.Pools {
		if pool.PoolID <= afterPoolID || pool.DeploymentStage != model.RoutingDeploymentStageActive ||
			pool.PolicyProfile != model.RoutingPolicyProfileEnterpriseSLO {
			continue
		}
		policy, err := resolveBalancedPoolPolicy(pool.PolicyProfile, pool.Policy)
		if err != nil {
			return nil, err
		}
		targets = append(targets, enterpriseErrorBudgetTarget{
			poolID: pool.PoolID, availabilityTarget: policy.AvailabilityTarget,
		})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].poolID < targets[j].poolID })
	return targets, nil
}

func PublishPendingErrorBudgetTransitionsContext(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lease, acquired, err := model.TryAcquireRoutingControlLeaseContext(
		ctx, errorBudgetPublisherLease, NodeEpochID(), int64(errorBudgetPublisherTTL/time.Millisecond), 0, false,
	)
	if err != nil || !acquired {
		return 0, err
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = model.ReleaseRoutingControlLeaseContext(releaseCtx, lease)
	}()

	cursor, err := model.GetRoutingErrorBudgetCursorContext(ctx, model.RoutingErrorBudgetPublisherCursor)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		cursor = model.RoutingErrorBudgetCursor{CursorName: model.RoutingErrorBudgetPublisherCursor}
	}
	published := 0
	for batch := 0; batch < errorBudgetPublisherMaxBatch; batch++ {
		transitions, err := model.ListRoutingErrorBudgetTransitionsAfterContext(ctx, cursor.PositionID, errorBudgetPublisherBatch)
		if err != nil {
			return published, err
		}
		for _, transition := range transitions {
			lease, err = model.RenewRoutingControlLeaseContext(ctx, lease, int64(errorBudgetPublisherTTL/time.Millisecond))
			if err != nil {
				return published, err
			}
			if err := publishErrorBudgetTransitionContext(ctx, transition); err != nil {
				return published, err
			}
			cursor.PositionID = transition.HistoryID
			cursor.PolicyRevision = 0
			cursor.LeaseName = lease.LeaseName
			cursor.LeaseFencingToken = lease.FencingToken
			cursor, err = model.SetRoutingErrorBudgetCursorContext(ctx, lease, cursor)
			if err != nil {
				return published, err
			}
			published++
		}
		if len(transitions) < errorBudgetPublisherBatch {
			break
		}
	}
	if err := model.CompleteRoutingControlLeaseContext(ctx, lease); err != nil {
		return published, err
	}
	completed = true
	return published, nil
}

func publishErrorBudgetTransitionContext(ctx context.Context, transition model.RoutingErrorBudgetTransition) error {
	if transition.HistoryID <= 0 || transition.PolicyRevision <= 0 || transition.EvaluatedAtMs <= 0 {
		return ErrErrorBudgetInvalid
	}
	payload, err := common.Marshal(transition)
	if err != nil {
		return err
	}
	event := RoutingEvent{
		ID: uint64(transition.HistoryID), Type: RoutingEventTypeErrorBudgetChanged,
		Revision: uint64(transition.PolicyRevision), CreatedTimeMs: transition.EvaluatedAtMs, PayloadJSON: payload,
	}
	client := loadRoutingEventRedis()
	if common.RedisEnabled {
		if client == nil {
			return ErrRoutingEventTransportUnavailable
		}
		publishCtx, cancel := context.WithTimeout(ctx, routingEventRedisPublishTimeout)
		defer cancel()
		if err := broadcastRoutingEventContext(
			publishCtx, defaultRoutingEventTransport, client, NodeEpochID(), event,
		); err != nil {
			return err
		}
	}
	_, err = defaultRoutingEventHubSnapshot().publish(
		event.Type, event.Revision, event.PayloadJSON, time.UnixMilli(event.CreatedTimeMs),
	)
	return err
}

func errorBudgetWindow(
	duration time.Duration,
	minimum int64,
	errorBudget float64,
	aggregate model.RoutingErrorBudgetReliabilityAggregate,
) ErrorBudgetWindow {
	window := ErrorBudgetWindow{
		WindowSeconds:          int64(duration / time.Second),
		RequestCount:           aggregate.RequestCount,
		FailureCount:           aggregate.FailureCount,
		UnisolatedRequestCount: aggregate.UnisolatedRequestCount,
		UnisolatedFailureCount: aggregate.UnisolatedFailureCount,
		RevisionIsolated:       aggregate.RevisionIsolated,
		MinimumVolume:          minimum,
		Sufficient:             aggregate.RevisionIsolated && aggregate.RequestCount >= minimum,
	}
	if aggregate.RequestCount <= 0 {
		return window
	}
	window.ErrorRate = float64(aggregate.FailureCount) / float64(aggregate.RequestCount)
	window.BurnRate = window.ErrorRate / errorBudget
	if math.IsInf(window.BurnRate, 1) || window.BurnRate > float64(math.MaxInt32) {
		window.BurnRate = float64(math.MaxInt32)
	}
	return window
}
