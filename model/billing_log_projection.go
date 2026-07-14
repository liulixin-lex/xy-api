package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BillingLogProjectionProtocol = 1

	BillingLogProjectionKindAccepted           = BillingStatsProjectionKindAccepted
	BillingLogProjectionKindTaskTerminal       = BillingStatsProjectionKindTaskTerminal
	BillingLogProjectionKindMidjourneyTerminal = BillingStatsProjectionKindMidjourneyTerminal

	BillingLogProjectionDispositionPending        = "pending"
	BillingLogProjectionDispositionNotRequired    = "not_required"
	BillingLogProjectionDispositionInvalidPayload = "invalid_payload"

	BillingLogProjectionStatePending   = "pending"
	BillingLogProjectionStateRunning   = "running"
	BillingLogProjectionStateCompleted = "completed"
	BillingLogProjectionStateFailed    = "failed"

	BillingLogProjectionOutcomeWritten     = "written"
	BillingLogProjectionOutcomeNotRequired = "not_required"

	BillingLogProjectionFailureInvalidPayload          = "invalid_frozen_payload"
	BillingLogProjectionFailureRetryExhausted          = "retry_exhausted"
	BillingLogProjectionFailureSinkReceiptConflict     = "sink_receipt_conflict"
	BillingLogProjectionFailureSinkReceiptConflictLate = "sink_receipt_conflict_late"

	billingLogProjectionMaxLease        = 10 * time.Minute
	billingLogProjectionSinkTimeout     = 30 * time.Second
	billingLogProjectionMutationTimeout = 3 * time.Second
	billingLogProjectionOwnerMaxBytes   = 128
	billingLogProjectionFailureMaxBytes = 64
	billingLogProjectionErrorMaxBytes   = 1024
	billingLogProjectionMaxAttempts     = 100
)

var (
	ErrBillingLogProjectionInvalid      = errors.New("billing log projection is invalid")
	ErrBillingLogProjectionConflict     = errors.New("billing log projection conflicts with the durable receipt")
	ErrBillingLogProjectionNotClaimed   = errors.New("billing log projection is not claimed by this worker")
	ErrBillingLogProjectionLeaseExpired = errors.New("billing log projection lease has expired")
)

type BillingLogProjection struct {
	ID              int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	OperationKey    string `json:"operation_key" gorm:"type:varchar(64);not null;uniqueIndex:uidx_billing_log_projection_operation"`
	ProtocolVersion int    `json:"protocol_version" gorm:"not null"`
	Kind            string `json:"kind" gorm:"type:varchar(24);not null;index;uniqueIndex:uidx_billing_log_projection_source,priority:1"`
	ReferenceID     int64  `json:"reference_id" gorm:"not null;index;uniqueIndex:uidx_billing_log_projection_source,priority:2"`
	Required        bool   `json:"required" gorm:"not null"`
	Disposition     string `json:"disposition" gorm:"type:varchar(24);not null"`

	PayloadProtocol int    `json:"payload_protocol" gorm:"not null"`
	PayloadHash     string `json:"payload_hash,omitempty" gorm:"type:varchar(64)"`
	Payload         string `json:"payload,omitempty" gorm:"type:text"`

	State        string `json:"state" gorm:"type:varchar(16);not null;index:idx_billing_log_projection_recovery,priority:1"`
	LeaseOwner   string `json:"lease_owner,omitempty" gorm:"type:varchar(128)"`
	LeaseUntilMs int64  `json:"lease_until_ms" gorm:"index:idx_billing_log_projection_lease"`
	Attempts     int    `json:"attempts" gorm:"not null"`
	NextRetryMs  int64  `json:"next_retry_ms" gorm:"index:idx_billing_log_projection_recovery,priority:2"`
	LastError    string `json:"last_error,omitempty" gorm:"type:varchar(1024)"`
	FailureCode  string `json:"failure_code,omitempty" gorm:"type:varchar(64)"`
	Outcome      string `json:"outcome,omitempty" gorm:"type:varchar(32)"`

	CreatedTimeMs   int64 `json:"created_time_ms" gorm:"not null"`
	UpdatedTimeMs   int64 `json:"updated_time_ms" gorm:"not null"`
	CompletedTimeMs int64 `json:"completed_time_ms"`
}

type BillingLogProjectionSpec struct {
	OperationKey string
	Kind         string
	ReferenceID  int64
	Required     bool
	Entry        *Log
}

func validBillingLogProjectionIdentity(spec BillingLogProjectionSpec) bool {
	operationKey := strings.TrimSpace(spec.OperationKey)
	if operationKey == "" || len(operationKey) > maxBillingLogOperationKeyBytes ||
		!utf8.ValidString(operationKey) || spec.ReferenceID <= 0 {
		return false
	}
	switch spec.Kind {
	case BillingLogProjectionKindAccepted, BillingLogProjectionKindTaskTerminal,
		BillingLogProjectionKindMidjourneyTerminal:
		return true
	default:
		return false
	}
}

func sameBillingLogProjectionIntent(existing, intended *BillingLogProjection) bool {
	return existing != nil && intended != nil &&
		existing.OperationKey == intended.OperationKey &&
		existing.ProtocolVersion == intended.ProtocolVersion && existing.Kind == intended.Kind &&
		existing.ReferenceID == intended.ReferenceID && existing.Required == intended.Required &&
		existing.Disposition == intended.Disposition && existing.PayloadProtocol == intended.PayloadProtocol &&
		existing.PayloadHash == intended.PayloadHash && existing.Payload == intended.Payload
}

// CreateBillingLogProjectionTx freezes the external-log intent in the caller's
// authoritative transaction. Invalid payloads become durable failed audit rows
// without retaining raw content or rolling back otherwise valid billing work.
func CreateBillingLogProjectionTx(
	tx *gorm.DB,
	spec BillingLogProjectionSpec,
	now time.Time,
) (*BillingLogProjection, bool, error) {
	if tx == nil || !validBillingLogProjectionIdentity(spec) {
		return nil, false, ErrBillingLogProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	operationKey := strings.TrimSpace(spec.OperationKey)
	projection := &BillingLogProjection{
		OperationKey: operationKey, ProtocolVersion: BillingLogProjectionProtocol,
		Kind: spec.Kind, ReferenceID: spec.ReferenceID, Required: spec.Required,
		CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}
	switch {
	case !spec.Required:
		projection.Disposition = BillingLogProjectionDispositionNotRequired
		projection.State = BillingLogProjectionStateCompleted
		projection.Outcome = BillingLogProjectionOutcomeNotRequired
		projection.CompletedTimeMs = now.UnixMilli()
	default:
		payload, payloadHash, payloadProtocol, err := freezeBillingLogPayload(operationKey, spec.Entry)
		if err != nil {
			projection.Disposition = BillingLogProjectionDispositionInvalidPayload
			projection.State = BillingLogProjectionStateFailed
			projection.FailureCode = BillingLogProjectionFailureInvalidPayload
			projection.CompletedTimeMs = now.UnixMilli()
		} else {
			projection.Disposition = BillingLogProjectionDispositionPending
			projection.Payload = payload
			projection.PayloadHash = payloadHash
			projection.PayloadProtocol = payloadProtocol
			projection.State = BillingLogProjectionStatePending
		}
	}

	created := tx.Clauses(clause.OnConflict{
		DoNothing: true,
	}).Create(projection)
	if created.Error != nil {
		return nil, false, created.Error
	}
	insertedID := projection.ID
	var existing BillingLogProjection
	if err := lockForUpdate(tx).Where("operation_key = ?", operationKey).First(&existing).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, err
		}
		if sourceErr := lockForUpdate(tx).Where("kind = ? AND reference_id = ?", projection.Kind, projection.ReferenceID).
			First(&existing).Error; sourceErr != nil {
			return nil, false, sourceErr
		}
		return &existing, false, ErrBillingLogProjectionConflict
	}
	if !sameBillingLogProjectionIntent(&existing, projection) {
		return &existing, false, ErrBillingLogProjectionConflict
	}
	return &existing, created.RowsAffected == 1 && insertedID > 0 && insertedID == existing.ID, nil
}

func GetBillingLogProjection(ctx context.Context, projectionID int64) (*BillingLogProjection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if projectionID <= 0 {
		return nil, ErrBillingLogProjectionInvalid
	}
	var projection BillingLogProjection
	if err := DB.WithContext(ctx).Where("id = ?", projectionID).First(&projection).Error; err != nil {
		return nil, err
	}
	return &projection, nil
}

func HasRecoverableBillingLogProjections(now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	var id int64
	err := DB.Model(&BillingLogProjection{}).Select("id").
		Where("(state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?)",
			BillingLogProjectionStatePending, now.UnixMilli(),
			BillingLogProjectionStateRunning, now.UnixMilli()).
		Order("id asc").Limit(1).Pluck("id", &id).Error
	return err == nil && id > 0
}

func ClaimBillingLogProjection(
	ctx context.Context,
	projectionID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*BillingLogProjection, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if projectionID <= 0 || owner == "" || len(owner) > billingLogProjectionOwnerMaxBytes ||
		!utf8.ValidString(owner) || leaseDuration <= 0 || leaseDuration > billingLogProjectionMaxLease {
		return nil, false, ErrBillingLogProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	leaseUntilMs := now.Add(leaseDuration).UnixMilli()
	var projection BillingLogProjection
	claimed := false
	permanentFailureCode := ""
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", projectionID).First(&projection).Error; err != nil {
			return err
		}
		eligible := (projection.State == BillingLogProjectionStatePending && projection.NextRetryMs <= nowMs) ||
			(projection.State == BillingLogProjectionStateRunning && projection.LeaseUntilMs <= nowMs)
		if !eligible {
			return nil
		}
		if projection.Disposition != BillingLogProjectionDispositionPending || !projection.Required {
			updated := tx.Model(&BillingLogProjection{}).
				Where("id = ? AND ((state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?))",
					projection.ID, BillingLogProjectionStatePending, nowMs,
					BillingLogProjectionStateRunning, nowMs).
				Updates(map[string]any{
					"state": BillingLogProjectionStateFailed, "lease_owner": "", "lease_until_ms": 0,
					"next_retry_ms": 0, "last_error": "", "failure_code": BillingLogProjectionFailureInvalidPayload,
					"updated_time_ms": nowMs, "completed_time_ms": nowMs,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 1 {
				permanentFailureCode = BillingLogProjectionFailureInvalidPayload
				projection.State = BillingLogProjectionStateFailed
				projection.FailureCode = permanentFailureCode
				projection.CompletedTimeMs = nowMs
			}
			return nil
		}
		if projection.Attempts >= billingLogProjectionMaxAttempts {
			updated := tx.Model(&BillingLogProjection{}).
				Where("id = ? AND ((state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?))",
					projection.ID, BillingLogProjectionStatePending, nowMs,
					BillingLogProjectionStateRunning, nowMs).
				Updates(map[string]any{
					"state": BillingLogProjectionStateFailed, "lease_owner": "", "lease_until_ms": 0,
					"next_retry_ms": 0, "last_error": "", "failure_code": BillingLogProjectionFailureRetryExhausted,
					"updated_time_ms": nowMs, "completed_time_ms": nowMs,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 1 {
				permanentFailureCode = BillingLogProjectionFailureRetryExhausted
				projection.State = BillingLogProjectionStateFailed
				projection.FailureCode = permanentFailureCode
				projection.CompletedTimeMs = nowMs
			}
			return nil
		}
		attempts := projection.Attempts
		if attempts < math.MaxInt32 {
			attempts++
		}
		updated := tx.Model(&BillingLogProjection{}).
			Where("id = ? AND ((state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?))",
				projection.ID, BillingLogProjectionStatePending, nowMs,
				BillingLogProjectionStateRunning, nowMs).
			Updates(map[string]any{
				"state": BillingLogProjectionStateRunning, "lease_owner": owner,
				"lease_until_ms": leaseUntilMs, "attempts": attempts, "updated_time_ms": nowMs,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return nil
		}
		claimed = true
		projection.State = BillingLogProjectionStateRunning
		projection.LeaseOwner = owner
		projection.LeaseUntilMs = leaseUntilMs
		projection.Attempts = attempts
		projection.UpdatedTimeMs = nowMs
		return nil
	})
	if err == nil && permanentFailureCode != "" {
		common.SysError(fmt.Sprintf("billing log projection requires manual audit: id=%d code=%s",
			projectionID, permanentFailureCode))
	}
	return &projection, claimed, err
}

func ClaimNextBillingLogProjection(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*BillingLogProjection, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	var ids []int64
	err := DB.WithContext(ctx).Model(&BillingLogProjection{}).Select("id").
		Where("(state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?)",
			BillingLogProjectionStatePending, now.UnixMilli(),
			BillingLogProjectionStateRunning, now.UnixMilli()).
		Order("id asc").Limit(100).Pluck("id", &ids).Error
	if err != nil {
		return nil, false, err
	}
	for _, id := range ids {
		projection, claimed, claimErr := ClaimBillingLogProjection(ctx, id, owner, now, leaseDuration)
		if claimErr != nil {
			return nil, false, claimErr
		}
		if claimed {
			return projection, true, nil
		}
	}
	return nil, false, nil
}

// DeliverClaimedBillingLogProjection crosses the external LOG_DB boundary. A
// committed sink write followed by a lost main-db ack is safe: the next lease
// holder verifies the immutable sink receipt and completes the same projection.
func DeliverClaimedBillingLogProjection(
	ctx context.Context,
	projectionID int64,
	owner string,
) (*BillingLogProjection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if projectionID <= 0 || owner == "" || len(owner) > billingLogProjectionOwnerMaxBytes || !utf8.ValidString(owner) {
		return nil, ErrBillingLogProjectionInvalid
	}
	var projection BillingLogProjection
	if err := DB.WithContext(ctx).Where("id = ?", projectionID).First(&projection).Error; err != nil {
		return nil, err
	}
	if projection.State == BillingLogProjectionStateCompleted {
		return &projection, nil
	}
	now := time.Now()
	if projection.State != BillingLogProjectionStateRunning || projection.LeaseOwner != owner {
		return &projection, ErrBillingLogProjectionNotClaimed
	}
	if projection.LeaseUntilMs <= now.UnixMilli() {
		return &projection, ErrBillingLogProjectionLeaseExpired
	}
	if projection.ProtocolVersion != BillingLogProjectionProtocol ||
		projection.Disposition != BillingLogProjectionDispositionPending || !projection.Required ||
		projection.PayloadProtocol != billingLogPayloadProtocol || projection.PayloadHash == "" || projection.Payload == "" {
		return &projection, ErrBillingLogProjectionInvalid
	}

	sinkCtx, sinkCancel := context.WithTimeout(ctx, billingLogProjectionSinkTimeout)
	err := writeFrozenBillingLog(
		sinkCtx, projection.OperationKey, projection.Payload, projection.PayloadHash, projection.PayloadProtocol,
	)
	sinkCancel()
	if err != nil {
		return &projection, err
	}

	ackNow := time.Now()
	ackCtx, ackCancel := context.WithTimeout(context.WithoutCancel(ctx), billingLogProjectionMutationTimeout)
	defer ackCancel()
	updated := DB.WithContext(ackCtx).Model(&BillingLogProjection{}).
		Where("id = ? AND state = ? AND lease_owner = ? AND lease_until_ms > ?",
			projection.ID, BillingLogProjectionStateRunning, owner, ackNow.UnixMilli()).
		Updates(map[string]any{
			"state": BillingLogProjectionStateCompleted, "lease_owner": "", "lease_until_ms": 0,
			"next_retry_ms": 0, "last_error": "", "failure_code": "",
			"outcome":         BillingLogProjectionOutcomeWritten,
			"updated_time_ms": ackNow.UnixMilli(), "completed_time_ms": ackNow.UnixMilli(),
		})
	if updated.Error != nil {
		return &projection, updated.Error
	}
	if updated.RowsAffected != 1 {
		return &projection, ErrBillingLogProjectionNotClaimed
	}
	projection.State = BillingLogProjectionStateCompleted
	projection.LeaseOwner = ""
	projection.LeaseUntilMs = 0
	projection.Outcome = BillingLogProjectionOutcomeWritten
	projection.UpdatedTimeMs = ackNow.UnixMilli()
	projection.CompletedTimeMs = ackNow.UnixMilli()
	return &projection, nil
}

func RetryClaimedBillingLogProjection(
	ctx context.Context,
	projectionID int64,
	owner string,
	now time.Time,
	cause error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if projectionID <= 0 || owner == "" || cause == nil || !utf8.ValidString(owner) ||
		len(owner) > billingLogProjectionOwnerMaxBytes {
		return ErrBillingLogProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	detachedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingLogProjectionMutationTimeout)
	defer cancel()
	var projection BillingLogProjection
	if err := DB.WithContext(detachedCtx).Select("id", "attempts").
		Where("id = ? AND state = ? AND lease_owner = ?", projectionID, BillingLogProjectionStateRunning, owner).
		First(&projection).Error; err != nil {
		return err
	}
	if projection.Attempts >= billingLogProjectionMaxAttempts {
		return failClaimedBillingLogProjection(detachedCtx, projectionID, owner, now, BillingLogProjectionFailureRetryExhausted)
	}
	nextRetry := now.Add(billingProjectionRetryDelay(projection.Attempts))
	updated := DB.WithContext(detachedCtx).Model(&BillingLogProjection{}).
		Where("id = ? AND state = ? AND lease_owner = ? AND lease_until_ms > ?",
			projectionID, BillingLogProjectionStateRunning, owner, now.UnixMilli()).
		Updates(map[string]any{
			"state": BillingLogProjectionStatePending, "lease_owner": "", "lease_until_ms": 0,
			"next_retry_ms": nextRetry.UnixMilli(), "last_error": boundedBillingLogProjectionError(cause.Error()),
			"updated_time_ms": now.UnixMilli(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrBillingLogProjectionNotClaimed
	}
	return nil
}

func FailClaimedBillingLogProjection(
	ctx context.Context,
	projectionID int64,
	owner string,
	now time.Time,
	failureCode string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	failureCode = strings.TrimSpace(failureCode)
	if projectionID <= 0 || owner == "" || failureCode == "" || !utf8.ValidString(owner) ||
		!utf8.ValidString(failureCode) || len(owner) > billingLogProjectionOwnerMaxBytes ||
		len(failureCode) > billingLogProjectionFailureMaxBytes {
		return ErrBillingLogProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	detachedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingLogProjectionMutationTimeout)
	defer cancel()
	return failClaimedBillingLogProjection(detachedCtx, projectionID, owner, now, failureCode)
}

func failClaimedBillingLogProjection(
	ctx context.Context,
	projectionID int64,
	owner string,
	now time.Time,
	failureCode string,
) error {
	updated := DB.WithContext(ctx).Model(&BillingLogProjection{}).
		Where("id = ? AND state = ? AND lease_owner = ? AND lease_until_ms > ?",
			projectionID, BillingLogProjectionStateRunning, owner, now.UnixMilli()).
		Updates(map[string]any{
			"state": BillingLogProjectionStateFailed, "lease_owner": "", "lease_until_ms": 0,
			"next_retry_ms": 0, "last_error": "", "failure_code": failureCode,
			"updated_time_ms": now.UnixMilli(), "completed_time_ms": now.UnixMilli(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrBillingLogProjectionNotClaimed
	}
	common.SysError(fmt.Sprintf("billing log projection requires manual audit: id=%d code=%s", projectionID, failureCode))
	return nil
}

func boundedBillingLogProjectionError(message string) string {
	message = common.SanitizeErrorMessage(message)
	if len(message) <= billingLogProjectionErrorMaxBytes {
		return message
	}
	end := billingLogProjectionErrorMaxBytes
	for end > 0 && !utf8.ValidString(message[:end]) {
		end--
	}
	return strings.TrimSpace(message[:end])
}
