package model

import (
	"context"
	"errors"
	"math"
	"unicode/utf8"

	"gorm.io/gorm"
)

const routingErrorBudgetPayloadMaxBytes = 32 << 10

// RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion is the first rollup
// schema that may be used for revision-scoped SLO evaluation. Schema v1/v2
// merge different snapshot revisions into the same physical bucket and are
// therefore intentionally treated as incomplete by error-budget evaluation.
const RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion = 3

var ErrRoutingErrorBudgetStateInvalid = errors.New("invalid channel routing error budget state")
var ErrRoutingErrorBudgetStateStale = errors.New("stale channel routing error budget evaluation")
var ErrRoutingErrorBudgetDatabaseTime = ErrRoutingDatabaseTime
var ErrRoutingErrorBudgetSchemaNotReady = errors.New("channel routing error budget schema is not ready")
var ErrRoutingErrorBudgetAlphaDrainRequired = errors.New("channel routing alpha-v2 error budget writers must be drained")

// RoutingErrorBudgetState stores the latest evaluated error-budget state for a
// routing pool. The payload is a bounded, whitelist-only metrics document.
type RoutingErrorBudgetState struct {
	ID                 int64   `json:"id" gorm:"primaryKey"`
	PoolID             int     `json:"pool_id" gorm:"not null;uniqueIndex:idx_routing_error_budget_state_key,priority:1"`
	PolicyRevision     int64   `json:"policy_revision" gorm:"bigint;not null;index;uniqueIndex:idx_routing_error_budget_state_key,priority:2"`
	AvailabilityTarget float64 `json:"availability_target" gorm:"not null"`
	Status             string  `json:"status" gorm:"type:varchar(32);index;not null"`
	Reason             string  `json:"reason" gorm:"type:varchar(64);index;not null"`
	EvaluationJSON     string  `json:"-" gorm:"type:text;not null"`
	LeaseFencingToken  int64   `json:"lease_fencing_token" gorm:"bigint;not null"`
	FirstObservedAtMs  int64   `json:"first_observed_at_ms" gorm:"bigint;not null"`
	LastEvaluatedAtMs  int64   `json:"last_evaluated_at_ms" gorm:"bigint;index;not null"`
	LastChangedAtMs    int64   `json:"last_changed_at_ms" gorm:"bigint;index;not null"`
	CreatedTime        int64   `json:"created_time" gorm:"bigint;not null"`
	UpdatedTime        int64   `json:"updated_time" gorm:"bigint;index;not null"`
}

func (RoutingErrorBudgetState) TableName() string {
	return "routing_error_budget_states"
}

func UpsertRoutingErrorBudgetStateContext(
	ctx context.Context,
	lease RoutingControlLease,
	state *RoutingErrorBudgetState,
) (bool, error) {
	transition, err := UpsertRoutingErrorBudgetStateWithTransitionContext(ctx, lease, state)
	return transition != nil, err
}

// UpsertRoutingErrorBudgetStateWithTransitionContext persists the latest state
// and its immutable transition record atomically. The returned transition is a
// whitelist-only handoff for a durable outbox/event publisher.
func UpsertRoutingErrorBudgetStateWithTransitionContext(
	ctx context.Context,
	lease RoutingControlLease,
	state *RoutingErrorBudgetState,
) (*RoutingErrorBudgetTransition, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRoutingErrorBudgetStateInput(lease, state); err != nil {
		return nil, err
	}
	var transition *RoutingErrorBudgetTransition
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentLease RoutingControlLease
		if err := lockForUpdate(tx.WithContext(ctx)).Where("lease_name = ?", lease.LeaseName).First(&currentLease).Error; err != nil {
			return err
		}
		databaseNowMs, err := routingErrorBudgetDatabaseNowMs(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		if currentLease.HolderID != lease.HolderID || currentLease.LeaseToken != lease.LeaseToken ||
			currentLease.FencingToken != lease.FencingToken || currentLease.LeaseUntilMs <= databaseNowMs {
			return ErrRoutingControlLeaseLost
		}
		databaseNow := databaseNowMs / 1_000
		if databaseNow <= 0 {
			return ErrRoutingErrorBudgetDatabaseTime
		}

		var existing RoutingErrorBudgetState
		loadErr := lockForUpdate(tx.WithContext(ctx)).Where(
			"pool_id = ? AND policy_revision = ?", state.PoolID, state.PolicyRevision,
		).First(&existing).Error
		previousStatus := ""
		previousReason := ""
		changed := true
		switch {
		case loadErr == nil:
			if state.LastEvaluatedAtMs <= existing.LastEvaluatedAtMs {
				return ErrRoutingErrorBudgetStateStale
			}
			previousStatus = existing.Status
			previousReason = existing.Reason
			changed = existing.Status != state.Status || existing.Reason != state.Reason ||
				existing.AvailabilityTarget != state.AvailabilityTarget
			state.ID = existing.ID
			state.CreatedTime = existing.CreatedTime
			state.UpdatedTime = databaseNow
			if changed {
				state.FirstObservedAtMs = state.LastEvaluatedAtMs
				state.LastChangedAtMs = state.LastEvaluatedAtMs
			} else {
				state.FirstObservedAtMs = existing.FirstObservedAtMs
				state.LastChangedAtMs = existing.LastChangedAtMs
			}
			if err := validateRoutingErrorBudgetState(lease, state); err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Save(state).Error; err != nil {
				return err
			}
		case errors.Is(loadErr, gorm.ErrRecordNotFound):
			state.FirstObservedAtMs = state.LastEvaluatedAtMs
			state.LastChangedAtMs = state.LastEvaluatedAtMs
			state.CreatedTime = databaseNow
			state.UpdatedTime = databaseNow
			if err := validateRoutingErrorBudgetState(lease, state); err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Create(state).Error; err != nil {
				return err
			}
		default:
			return loadErr
		}
		if changed {
			created, err := createRoutingErrorBudgetHistoryTx(
				ctx, tx, *state, previousStatus, previousReason, databaseNow,
			)
			if err != nil {
				return err
			}
			transition = &created
		}
		if err := pruneRoutingErrorBudgetStatesTx(ctx, tx, state.PoolID, state.ID); err != nil {
			return err
		}
		commitNowMs, err := routingErrorBudgetDatabaseNowMs(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		if currentLease.LeaseUntilMs <= commitNowMs {
			return ErrRoutingControlLeaseLost
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return transition, nil
}

func GetRoutingErrorBudgetStateContext(ctx context.Context, poolID int) (RoutingErrorBudgetState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if poolID <= 0 {
		return RoutingErrorBudgetState{}, ErrRoutingErrorBudgetStateInvalid
	}
	var state RoutingErrorBudgetState
	err := DB.WithContext(ctx).Where("pool_id = ?", poolID).
		Order("policy_revision DESC").Order("last_evaluated_at_ms DESC").First(&state).Error
	return state, err
}

func GetRoutingErrorBudgetStateForRevisionContext(
	ctx context.Context,
	poolID int,
	policyRevision int64,
) (RoutingErrorBudgetState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if poolID <= 0 || policyRevision <= 0 {
		return RoutingErrorBudgetState{}, ErrRoutingErrorBudgetStateInvalid
	}
	var state RoutingErrorBudgetState
	err := DB.WithContext(ctx).Where(
		"pool_id = ? AND policy_revision = ?", poolID, policyRevision,
	).First(&state).Error
	return state, err
}

func ListRoutingErrorBudgetStatesContext(ctx context.Context, poolIDs []int) ([]RoutingErrorBudgetState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(poolIDs) == 0 {
		return []RoutingErrorBudgetState{}, nil
	}
	for _, poolID := range poolIDs {
		if poolID <= 0 {
			return nil, ErrRoutingErrorBudgetStateInvalid
		}
	}
	var revisions []RoutingErrorBudgetState
	err := DB.WithContext(ctx).Where("pool_id IN ?", poolIDs).
		Order("pool_id ASC").Order("policy_revision DESC").Find(&revisions).Error
	if err != nil {
		return nil, err
	}
	states := make([]RoutingErrorBudgetState, 0, len(poolIDs))
	seen := make(map[int]struct{}, len(poolIDs))
	for _, state := range revisions {
		if _, exists := seen[state.PoolID]; exists {
			continue
		}
		seen[state.PoolID] = struct{}{}
		states = append(states, state)
	}
	return states, nil
}

type RoutingErrorBudgetReliabilityAggregate struct {
	RequestCount           int64 `json:"request_count"`
	FailureCount           int64 `json:"failure_count"`
	UnisolatedRequestCount int64 `json:"unisolated_request_count"`
	UnisolatedFailureCount int64 `json:"unisolated_failure_count"`
	RevisionIsolated       bool  `json:"revision_isolated"`
}

// AggregateRoutingMetricReliabilityByRevisionContext only accepts rollups whose
// physical schema guarantees that snapshot revision is part of the bucket key.
// Legacy rows are counted separately so callers can fail closed instead of
// silently reporting a healthy SLO from mixed-revision counters.
func AggregateRoutingMetricReliabilityByRevisionContext(
	ctx context.Context,
	poolID int,
	policyRevision int64,
	fromUnix int64,
	toUnix int64,
) (RoutingErrorBudgetReliabilityAggregate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if poolID <= 0 || policyRevision <= 0 || fromUnix < 0 || toUnix <= fromUnix {
		return RoutingErrorBudgetReliabilityAggregate{}, ErrRoutingMetricRollupInvalid
	}
	var aggregate RoutingErrorBudgetReliabilityAggregate
	err := DB.WithContext(ctx).Model(&RoutingMetricRollup{}).
		Select(
			"COALESCE(SUM(CASE WHEN schema_version >= ? AND last_snapshot_revision = ? THEN reliability_request_count ELSE 0 END), 0) AS request_count, "+
				"COALESCE(SUM(CASE WHEN schema_version >= ? AND last_snapshot_revision = ? THEN reliability_failure_count ELSE 0 END), 0) AS failure_count, "+
				"COALESCE(SUM(CASE WHEN schema_version < ? THEN reliability_request_count ELSE 0 END), 0) AS unisolated_request_count, "+
				"COALESCE(SUM(CASE WHEN schema_version < ? THEN reliability_failure_count ELSE 0 END), 0) AS unisolated_failure_count",
			RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion, policyRevision,
			RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion, policyRevision,
			RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion,
			RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion,
		).
		Where("pool_id = ? AND bucket_ts >= ? AND bucket_ts < ?", poolID, fromUnix, toUnix).
		Scan(&aggregate).Error
	if err != nil {
		return RoutingErrorBudgetReliabilityAggregate{}, err
	}
	if aggregate.RequestCount < 0 || aggregate.FailureCount < 0 ||
		aggregate.FailureCount > aggregate.RequestCount || aggregate.UnisolatedRequestCount < 0 ||
		aggregate.UnisolatedFailureCount < 0 || aggregate.UnisolatedFailureCount > aggregate.UnisolatedRequestCount {
		return RoutingErrorBudgetReliabilityAggregate{}, ErrRoutingMetricRollupInvalid
	}
	aggregate.RevisionIsolated = aggregate.UnisolatedRequestCount == 0
	return aggregate, nil
}

func routingErrorBudgetDatabaseNowMs(tx *gorm.DB) (int64, error) {
	return routingDatabaseNowMs(tx)
}

func validateRoutingErrorBudgetStateInput(lease RoutingControlLease, state *RoutingErrorBudgetState) error {
	if state == nil || !validRoutingControlLeaseText(lease.LeaseName, 64) ||
		!validRoutingControlLeaseText(lease.HolderID, 128) || len(lease.LeaseToken) != 32 ||
		lease.FencingToken <= 0 || state.LeaseFencingToken != lease.FencingToken ||
		state.PoolID <= 0 || state.PolicyRevision <= 0 ||
		math.IsNaN(state.AvailabilityTarget) || math.IsInf(state.AvailabilityTarget, 0) ||
		state.AvailabilityTarget <= 0 || state.AvailabilityTarget >= 1 ||
		!validRoutingErrorBudgetText(state.Status, 32) || !validRoutingErrorBudgetText(state.Reason, 64) ||
		len(state.EvaluationJSON) == 0 || len(state.EvaluationJSON) > routingErrorBudgetPayloadMaxBytes ||
		state.FirstObservedAtMs <= 0 || state.LastEvaluatedAtMs < state.FirstObservedAtMs ||
		state.LastChangedAtMs < state.FirstObservedAtMs || state.LastChangedAtMs > state.LastEvaluatedAtMs {
		return ErrRoutingErrorBudgetStateInvalid
	}
	return nil
}

func validateRoutingErrorBudgetState(lease RoutingControlLease, state *RoutingErrorBudgetState) error {
	if err := validateRoutingErrorBudgetStateInput(lease, state); err != nil ||
		state.CreatedTime <= 0 || state.UpdatedTime < state.CreatedTime {
		return ErrRoutingErrorBudgetStateInvalid
	}
	return nil
}

func validRoutingErrorBudgetText(value string, maxRunes int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes
}
