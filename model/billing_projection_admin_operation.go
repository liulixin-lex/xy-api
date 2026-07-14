package model

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BillingProjectionAdminActionStatsRequeue    = "stats_requeue"
	BillingProjectionAdminActionLogRequeue      = "log_requeue"
	BillingProjectionAdminActionConflictResolve = "conflict_resolve"

	BillingProjectionAdminStateRunning   = "running"
	BillingProjectionAdminStateCompleted = "completed"

	BillingProjectionAdminOutcomeSucceeded          = "succeeded"
	BillingProjectionAdminOutcomePreconditionFailed = "precondition_failed"
	BillingProjectionAdminOutcomeNotFound           = "not_found"
	BillingProjectionAdminOutcomeNotRequeueable     = "not_requeueable"

	billingProjectionAdminHashBytes           = 64
	billingProjectionAdminFailureCodeMaxBytes = 64
	billingProjectionAdminReasonMaxBytes      = 1024
	billingProjectionAdminRetention           = 365 * 24 * time.Hour
	billingProjectionAdminCleanupMaxBatch     = 500
)

var (
	ErrBillingProjectionAdminInvalid             = errors.New("billing projection admin operation is invalid")
	ErrBillingProjectionAdminIdempotencyConflict = errors.New("billing projection admin idempotency key conflicts with another request")
	ErrBillingProjectionAdminPrecondition        = errors.New("billing projection admin precondition failed")
	ErrBillingProjectionAdminNotFound            = errors.New("billing projection target was not found")
	ErrBillingProjectionAdminNotRequeueable      = errors.New("billing projection cannot be requeued")
)

// BillingProjectionAdminOperation is a bounded, payload-free idempotency
// receipt for high-risk operator mutations. Raw keys, request bodies, frozen
// log payloads, and errors are never persisted here.
type BillingProjectionAdminOperation struct {
	ID                  int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	IdempotencyKeyHash  string `json:"-" gorm:"type:varchar(64);not null;uniqueIndex:uidx_billing_projection_admin_key"`
	RequestHash         string `json:"-" gorm:"type:varchar(64);not null"`
	Action              string `json:"action" gorm:"type:varchar(32);not null;index"`
	TargetID            int64  `json:"target_id" gorm:"not null;index"`
	ActorUserID         int    `json:"actor_user_id" gorm:"not null;index"`
	ExpectedRevision    int64  `json:"-" gorm:"not null"`
	ExpectedFailureCode string `json:"-" gorm:"type:varchar(64);not null"`
	State               string `json:"state" gorm:"type:varchar(16);not null;index"`
	Outcome             string `json:"outcome" gorm:"type:varchar(32);not null"`
	CreatedTimeMs       int64  `json:"created_time_ms" gorm:"not null;index"`
	UpdatedTimeMs       int64  `json:"updated_time_ms" gorm:"not null"`
	CompletedTimeMs     int64  `json:"completed_time_ms" gorm:"index"`
}

type BillingProjectionAdminOperationSpec struct {
	Action              string
	TargetID            int64
	ActorUserID         int
	ExpectedRevision    int64
	ExpectedFailureCode string
	Reason              string
	IdempotencyKeyHash  string
	RequestHash         string
}

type BillingProjectionAdminOperationResult struct {
	Operation BillingProjectionAdminOperation
	Replayed  bool
}

func validBillingProjectionAdminHash(value string) bool {
	if len(value) != billingProjectionAdminHashBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		if (value[index] < '0' || value[index] > '9') && (value[index] < 'a' || value[index] > 'f') {
			return false
		}
	}
	return true
}

func validateBillingProjectionAdminOperationSpec(spec *BillingProjectionAdminOperationSpec) error {
	if spec == nil || DB == nil || spec.TargetID <= 0 || spec.ActorUserID <= 0 || spec.ExpectedRevision <= 0 ||
		!validBillingProjectionAdminHash(spec.IdempotencyKeyHash) || !validBillingProjectionAdminHash(spec.RequestHash) {
		return ErrBillingProjectionAdminInvalid
	}
	spec.ExpectedFailureCode = strings.TrimSpace(spec.ExpectedFailureCode)
	spec.Reason = strings.TrimSpace(spec.Reason)
	switch spec.Action {
	case BillingProjectionAdminActionStatsRequeue:
		if spec.ExpectedFailureCode == "" || len(spec.ExpectedFailureCode) > billingProjectionAdminFailureCodeMaxBytes ||
			!utf8.ValidString(spec.ExpectedFailureCode) || spec.Reason != "" {
			return ErrBillingProjectionAdminInvalid
		}
	case BillingProjectionAdminActionLogRequeue:
		if spec.ExpectedFailureCode == "" || len(spec.ExpectedFailureCode) > billingProjectionAdminFailureCodeMaxBytes ||
			!utf8.ValidString(spec.ExpectedFailureCode) || spec.Reason != "" ||
			spec.ExpectedFailureCode == BillingLogProjectionFailureInvalidPayload ||
			spec.ExpectedFailureCode == BillingLogProjectionFailureSinkReceiptConflict ||
			spec.ExpectedFailureCode == BillingLogProjectionFailureSinkReceiptConflictLate {
			return ErrBillingProjectionAdminNotRequeueable
		}
	case BillingProjectionAdminActionConflictResolve:
		if spec.ExpectedFailureCode != "" || spec.Reason == "" ||
			len(spec.Reason) > billingProjectionAdminReasonMaxBytes || !utf8.ValidString(spec.Reason) ||
			strings.ContainsAny(spec.Reason, "\r\n\x00") {
			return ErrBillingProjectionAdminInvalid
		}
	default:
		return ErrBillingProjectionAdminInvalid
	}
	return nil
}

func sameBillingProjectionAdminOperation(
	operation *BillingProjectionAdminOperation,
	spec BillingProjectionAdminOperationSpec,
) bool {
	return operation != nil && operation.IdempotencyKeyHash == spec.IdempotencyKeyHash &&
		operation.RequestHash == spec.RequestHash && operation.Action == spec.Action &&
		operation.TargetID == spec.TargetID && operation.ActorUserID == spec.ActorUserID &&
		operation.ExpectedRevision == spec.ExpectedRevision &&
		operation.ExpectedFailureCode == spec.ExpectedFailureCode
}

func beginBillingProjectionAdminOperation(
	ctx context.Context,
	spec BillingProjectionAdminOperationSpec,
	now time.Time,
) (BillingProjectionAdminOperation, bool, error) {
	seed := BillingProjectionAdminOperation{
		IdempotencyKeyHash:  spec.IdempotencyKeyHash,
		RequestHash:         spec.RequestHash,
		Action:              spec.Action,
		TargetID:            spec.TargetID,
		ActorUserID:         spec.ActorUserID,
		ExpectedRevision:    spec.ExpectedRevision,
		ExpectedFailureCode: spec.ExpectedFailureCode,
		State:               BillingProjectionAdminStateRunning,
		CreatedTimeMs:       now.UnixMilli(),
		UpdatedTimeMs:       now.UnixMilli(),
	}
	created := DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "idempotency_key_hash"}},
		DoNothing: true,
	}).Create(&seed)
	if created.Error != nil {
		return BillingProjectionAdminOperation{}, false, created.Error
	}
	var stored BillingProjectionAdminOperation
	if err := DB.WithContext(ctx).Where("idempotency_key_hash = ?", spec.IdempotencyKeyHash).
		First(&stored).Error; err != nil {
		return BillingProjectionAdminOperation{}, false, err
	}
	if !sameBillingProjectionAdminOperation(&stored, spec) {
		return BillingProjectionAdminOperation{}, false, ErrBillingProjectionAdminIdempotencyConflict
	}
	if stored.State != BillingProjectionAdminStateRunning && stored.State != BillingProjectionAdminStateCompleted {
		return BillingProjectionAdminOperation{}, false, ErrBillingProjectionAdminInvalid
	}
	wasCreated := created.RowsAffected == 1 && seed.ID > 0 && seed.ID == stored.ID
	return stored, wasCreated, nil
}

func completeBillingProjectionAdminOperation(
	ctx context.Context,
	operation BillingProjectionAdminOperation,
	outcome string,
	now time.Time,
) (BillingProjectionAdminOperation, error) {
	updated := DB.WithContext(ctx).Model(&BillingProjectionAdminOperation{}).
		Where("id = ? AND state = ? AND request_hash = ?",
			operation.ID, BillingProjectionAdminStateRunning, operation.RequestHash).
		Updates(map[string]any{
			"state": BillingProjectionAdminStateCompleted, "outcome": outcome,
			"updated_time_ms": now.UnixMilli(), "completed_time_ms": now.UnixMilli(),
		})
	if updated.Error != nil {
		return BillingProjectionAdminOperation{}, updated.Error
	}
	var stored BillingProjectionAdminOperation
	if err := DB.WithContext(ctx).Where("id = ?", operation.ID).First(&stored).Error; err != nil {
		return BillingProjectionAdminOperation{}, err
	}
	if stored.State != BillingProjectionAdminStateCompleted || stored.Outcome != outcome ||
		stored.RequestHash != operation.RequestHash {
		return BillingProjectionAdminOperation{}, ErrBillingProjectionAdminIdempotencyConflict
	}
	return stored, nil
}

func billingProjectionAdminOutcomeError(outcome string) error {
	switch outcome {
	case BillingProjectionAdminOutcomeSucceeded:
		return nil
	case BillingProjectionAdminOutcomePreconditionFailed:
		return ErrBillingProjectionAdminPrecondition
	case BillingProjectionAdminOutcomeNotFound:
		return ErrBillingProjectionAdminNotFound
	case BillingProjectionAdminOutcomeNotRequeueable:
		return ErrBillingProjectionAdminNotRequeueable
	default:
		return ErrBillingProjectionAdminInvalid
	}
}

type billingProjectionAdminMutation func() error
type billingProjectionAdminRecovery func(error) (bool, error)
type billingProjectionAdminClassifier func(error) (string, bool)

func runBillingProjectionAdminOperation(
	ctx context.Context,
	spec BillingProjectionAdminOperationSpec,
	now time.Time,
	mutate billingProjectionAdminMutation,
	recoverMutation billingProjectionAdminRecovery,
	classify billingProjectionAdminClassifier,
) (BillingProjectionAdminOperationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := validateBillingProjectionAdminOperationSpec(&spec); err != nil {
		return BillingProjectionAdminOperationResult{}, err
	}
	stored, created, err := beginBillingProjectionAdminOperation(ctx, spec, now)
	if err != nil {
		return BillingProjectionAdminOperationResult{}, err
	}
	result := BillingProjectionAdminOperationResult{Operation: stored, Replayed: !created}
	if stored.State == BillingProjectionAdminStateCompleted {
		return result, billingProjectionAdminOutcomeError(stored.Outcome)
	}
	mutationErr := mutate()
	if mutationErr != nil && !created && recoverMutation != nil {
		recovered, recoveryErr := recoverMutation(mutationErr)
		if recoveryErr != nil {
			return result, recoveryErr
		}
		if recovered {
			mutationErr = nil
		}
	}
	outcome := BillingProjectionAdminOutcomeSucceeded
	if mutationErr != nil {
		var terminal bool
		outcome, terminal = classify(mutationErr)
		if !terminal {
			return result, mutationErr
		}
	}
	stored, err = completeBillingProjectionAdminOperation(ctx, stored, outcome, now)
	if err != nil {
		return result, err
	}
	result.Operation = stored
	return result, billingProjectionAdminOutcomeError(outcome)
}

func RequeueFailedBillingStatsProjectionAdmin(
	ctx context.Context,
	spec BillingProjectionAdminOperationSpec,
	now time.Time,
) (BillingProjectionAdminOperationResult, error) {
	spec.Action = BillingProjectionAdminActionStatsRequeue
	return runBillingProjectionAdminOperation(ctx, spec, now, func() error {
		return RequeueFailedBillingStatsProjectionAtVersion(
			ctx, spec.TargetID, spec.ExpectedFailureCode, spec.ExpectedRevision, now,
		)
	}, func(mutationErr error) (bool, error) {
		if !errors.Is(mutationErr, ErrBillingStatsProjectionConflict) {
			return false, nil
		}
		projection, err := GetBillingStatsProjection(ctx, spec.TargetID)
		if err != nil {
			return false, err
		}
		return projection.State != BillingStatsProjectionStateFailed ||
			projection.FailureCode != spec.ExpectedFailureCode ||
			projection.UpdatedTimeMs != spec.ExpectedRevision, nil
	}, func(mutationErr error) (string, bool) {
		switch {
		case errors.Is(mutationErr, ErrBillingStatsProjectionConflict):
			return BillingProjectionAdminOutcomePreconditionFailed, true
		case errors.Is(mutationErr, gorm.ErrRecordNotFound):
			return BillingProjectionAdminOutcomeNotFound, true
		case errors.Is(mutationErr, ErrBillingStatsProjectionInvalid):
			return BillingProjectionAdminOutcomeNotRequeueable, true
		default:
			return "", false
		}
	})
}

func RequeueFailedBillingLogProjectionAdmin(
	ctx context.Context,
	spec BillingProjectionAdminOperationSpec,
	now time.Time,
) (BillingProjectionAdminOperationResult, error) {
	spec.Action = BillingProjectionAdminActionLogRequeue
	return runBillingProjectionAdminOperation(ctx, spec, now, func() error {
		return RequeueFailedBillingLogProjectionAtVersion(
			ctx, spec.TargetID, spec.ExpectedFailureCode, spec.ExpectedRevision, now,
		)
	}, func(mutationErr error) (bool, error) {
		if !errors.Is(mutationErr, ErrBillingLogProjectionConflict) {
			return false, nil
		}
		projection, err := GetBillingLogProjection(ctx, spec.TargetID)
		if err != nil {
			return false, err
		}
		return projection.State != BillingLogProjectionStateFailed ||
			projection.FailureCode != spec.ExpectedFailureCode ||
			projection.UpdatedTimeMs != spec.ExpectedRevision, nil
	}, func(mutationErr error) (string, bool) {
		switch {
		case errors.Is(mutationErr, ErrBillingLogProjectionConflict):
			return BillingProjectionAdminOutcomePreconditionFailed, true
		case errors.Is(mutationErr, gorm.ErrRecordNotFound):
			return BillingProjectionAdminOutcomeNotFound, true
		case errors.Is(mutationErr, ErrBillingLogProjectionInvalid):
			return BillingProjectionAdminOutcomeNotRequeueable, true
		default:
			return "", false
		}
	})
}

func ResolveAndRequeueBillingLogSinkConflictAdmin(
	ctx context.Context,
	spec BillingProjectionAdminOperationSpec,
	now time.Time,
) (BillingProjectionAdminOperationResult, error) {
	spec.Action = BillingProjectionAdminActionConflictResolve
	return runBillingProjectionAdminOperation(ctx, spec, now, func() error {
		return ResolveAndRequeueBillingLogSinkConflict(
			ctx, spec.TargetID, spec.ExpectedRevision, spec.ActorUserID, spec.Reason, now,
		)
	}, nil, func(mutationErr error) (string, bool) {
		switch {
		case errors.Is(mutationErr, ErrBillingLogSinkConflictPrecondition):
			return BillingProjectionAdminOutcomePreconditionFailed, true
		case errors.Is(mutationErr, gorm.ErrRecordNotFound):
			return BillingProjectionAdminOutcomeNotFound, true
		case errors.Is(mutationErr, ErrBillingLogSinkConflictInvalid):
			return BillingProjectionAdminOutcomeNotRequeueable, true
		default:
			return "", false
		}
	})
}

func CleanupExpiredBillingProjectionAdminOperationsPage(
	ctx context.Context,
	now time.Time,
	afterID int64,
	limit int,
) (int64, int64, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil || afterID < 0 {
		return 0, afterID, false, ErrBillingProjectionAdminInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	if limit <= 0 || limit > billingProjectionAdminCleanupMaxBatch {
		limit = 100
	}
	var ids []int64
	if err := DB.WithContext(ctx).Model(&BillingProjectionAdminOperation{}).Select("id").
		Where("id > ? AND created_time_ms > 0 AND created_time_ms <= ?", afterID,
			now.Add(-billingProjectionAdminRetention).UnixMilli()).
		Order("id asc").Limit(limit+1).Pluck("id", &ids).Error; err != nil {
		return 0, afterID, false, err
	}
	hasMore := len(ids) > limit
	if hasMore {
		ids = ids[:limit]
	}
	nextID := int64(0)
	if hasMore {
		nextID = ids[len(ids)-1]
	}
	if len(ids) == 0 {
		return 0, nextID, hasMore, nil
	}
	deleted := DB.WithContext(ctx).Where("id IN ?", ids).Delete(&BillingProjectionAdminOperation{})
	return deleted.RowsAffected, nextID, hasMore, deleted.Error
}
