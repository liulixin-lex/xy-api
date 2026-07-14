package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BillingLogSinkConflictStateOpen     = "open"
	BillingLogSinkConflictStateResolved = "resolved"

	maxBillingLogSinkConflictReceiptsBytes = 4096
	maxBillingLogSinkConflictReasonBytes   = 1024
)

var (
	ErrBillingLogSinkConflictInvalid      = errors.New("billing log sink conflict is invalid")
	ErrBillingLogSinkConflictPrecondition = errors.New("billing log sink conflict precondition failed")
)

// BillingLogSinkConflictAudit is the durable main-database quarantine marker
// for conflicting raw ClickHouse receipts. It survives log TTL and keeps the
// authoritative async source protected until an operator verifies remediation.
type BillingLogSinkConflictAudit struct {
	ID                      int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	OperationKey            string `json:"operation_key" gorm:"type:varchar(64);not null;uniqueIndex:uidx_billing_log_sink_conflict_operation"`
	ProjectionID            int64  `json:"projection_id" gorm:"not null;index"`
	ExpectedPayloadHash     string `json:"expected_payload_hash,omitempty" gorm:"type:varchar(64)"`
	ExpectedPayloadProtocol int    `json:"expected_payload_protocol" gorm:"not null"`
	Receipts                string `json:"receipts" gorm:"type:text;not null"`
	DistinctReceipts        int64  `json:"distinct_receipts" gorm:"not null"`
	PhysicalRows            int64  `json:"physical_rows" gorm:"not null"`
	State                   string `json:"state" gorm:"type:varchar(16);not null;index"`
	Version                 int64  `json:"version" gorm:"not null"`
	FirstDetectedMs         int64  `json:"first_detected_ms" gorm:"not null"`
	LastDetectedMs          int64  `json:"last_detected_ms" gorm:"not null;index"`
	LastResolvedMs          int64  `json:"last_resolved_ms"`
	LastResolvedBy          int    `json:"last_resolved_by"`
	LastResolutionReason    string `json:"last_resolution_reason,omitempty" gorm:"type:varchar(1024)"`
}

// BillingLogSinkConflictResolution is immutable operator evidence for each
// verified remediation. A later conflicting append reopens the audit without
// erasing earlier decisions.
type BillingLogSinkConflictResolution struct {
	ID                      int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	ConflictAuditID         int64  `json:"conflict_audit_id" gorm:"not null;uniqueIndex:uidx_billing_log_sink_conflict_resolution,priority:1"`
	ConflictVersion         int64  `json:"conflict_version" gorm:"not null;uniqueIndex:uidx_billing_log_sink_conflict_resolution,priority:2"`
	OperationKey            string `json:"operation_key" gorm:"type:varchar(64);not null;index"`
	ActorUserID             int    `json:"actor_user_id" gorm:"not null;index"`
	Reason                  string `json:"reason" gorm:"type:varchar(1024);not null"`
	VerifiedPayloadHash     string `json:"verified_payload_hash" gorm:"type:varchar(64);not null"`
	VerifiedPayloadProtocol int    `json:"verified_payload_protocol" gorm:"not null"`
	ResolvedTimeMs          int64  `json:"resolved_time_ms" gorm:"not null;index"`
}

func BillingLogSinkConflictETag(auditID, version int64) string {
	return fmt.Sprintf("\"billing-log-sink-conflict-%d-v%d\"", auditID, version)
}

func validBillingLogSinkConflict(conflict BillingLogSinkConflict) bool {
	operationKey := strings.TrimSpace(conflict.OperationKey)
	return operationKey != "" && operationKey == conflict.OperationKey &&
		len(operationKey) <= maxBillingLogOperationKeyBytes && utf8.ValidString(operationKey) &&
		conflict.DistinctReceipts > 1 && conflict.PhysicalRows >= conflict.DistinctReceipts &&
		conflict.PhysicalRows > 1 && conflict.Receipts != "" &&
		len(conflict.Receipts) <= maxBillingLogSinkConflictReceiptsBytes && utf8.ValidString(conflict.Receipts)
}

func QuarantineBillingLogSinkConflicts(
	ctx context.Context,
	conflicts []BillingLogSinkConflict,
	now time.Time,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil {
		return errors.New("billing database is unavailable")
	}
	if now.IsZero() {
		now = time.Now()
	}
	for _, conflict := range conflicts {
		if !validBillingLogSinkConflict(conflict) {
			return ErrBillingLogSinkConflictInvalid
		}
	}
	nowMs := now.UnixMilli()
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, conflict := range conflicts {
			seed := &BillingLogSinkConflictAudit{
				OperationKey: conflict.OperationKey, Receipts: conflict.Receipts,
				DistinctReceipts: conflict.DistinctReceipts, PhysicalRows: conflict.PhysicalRows,
				State: BillingLogSinkConflictStateOpen, Version: 1,
				FirstDetectedMs: nowMs, LastDetectedMs: nowMs,
			}
			created := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "operation_key"}},
				DoNothing: true,
			}).Create(seed)
			if created.Error != nil {
				return created.Error
			}
			insertedID := seed.ID

			var audit BillingLogSinkConflictAudit
			if err := lockForUpdate(tx).Where("operation_key = ?", conflict.OperationKey).First(&audit).Error; err != nil {
				return err
			}
			previous := audit
			wasCreated := created.RowsAffected == 1 && insertedID > 0 && insertedID == audit.ID
			audit.Receipts = conflict.Receipts
			audit.DistinctReceipts = conflict.DistinctReceipts
			audit.PhysicalRows = conflict.PhysicalRows
			audit.State = BillingLogSinkConflictStateOpen
			audit.LastDetectedMs = nowMs

			var projection BillingLogProjection
			projectionErr := lockForUpdate(tx).Where("operation_key = ?", conflict.OperationKey).
				First(&projection).Error
			switch {
			case projectionErr == nil:
				audit.ProjectionID = projection.ID
				audit.ExpectedPayloadHash = projection.PayloadHash
				audit.ExpectedPayloadProtocol = projection.PayloadProtocol
				failureCode := BillingLogProjectionFailureSinkReceiptConflict
				if projection.FailureCode == BillingLogProjectionFailureSinkReceiptConflict ||
					projection.FailureCode == BillingLogProjectionFailureSinkReceiptConflictLate {
					failureCode = projection.FailureCode
				} else if projection.State == BillingLogProjectionStateCompleted {
					failureCode = BillingLogProjectionFailureSinkReceiptConflictLate
				}
				if projection.State != BillingLogProjectionStateFailed ||
					projection.FailureCode == BillingLogProjectionFailureSinkReceiptConflict ||
					projection.FailureCode == BillingLogProjectionFailureSinkReceiptConflictLate {
					detail := boundedBillingLogProjectionError(fmt.Sprintf(
						"raw sink receipt conflict: receipts=%s distinct_receipts=%d physical_rows=%d",
						conflict.Receipts, conflict.DistinctReceipts, conflict.PhysicalRows,
					))
					updates := map[string]any{
						"state": BillingLogProjectionStateFailed, "lease_owner": "", "lease_until_ms": 0,
						"next_retry_ms": 0, "last_error": detail, "failure_code": failureCode,
						"updated_time_ms": nowMs,
					}
					if projection.State != BillingLogProjectionStateFailed {
						updates["completed_time_ms"] = nowMs
					}
					unchangedProjection := projection.State == BillingLogProjectionStateFailed &&
						projection.FailureCode == failureCode &&
						projection.LastError == detail && projection.LeaseOwner == "" && projection.LeaseUntilMs == 0 &&
						projection.NextRetryMs == 0
					if !unchangedProjection {
						if err := tx.Model(&BillingLogProjection{}).Where("id = ?", projection.ID).
							Updates(updates).Error; err != nil {
							return err
						}
					}
				}
			case errors.Is(projectionErr, gorm.ErrRecordNotFound):
				logger.LogWarn(ctx, fmt.Sprintf(
					"billing log conflict has no main-database projection: operation=%s", conflict.OperationKey,
				))
			default:
				return projectionErr
			}
			evidenceChanged := previous.State != audit.State || previous.Receipts != audit.Receipts ||
				previous.DistinctReceipts != audit.DistinctReceipts || previous.PhysicalRows != audit.PhysicalRows ||
				previous.ProjectionID != audit.ProjectionID ||
				previous.ExpectedPayloadHash != audit.ExpectedPayloadHash ||
				previous.ExpectedPayloadProtocol != audit.ExpectedPayloadProtocol
			if !wasCreated && evidenceChanged && audit.Version < math.MaxInt64 {
				audit.Version++
			}

			if err := tx.Model(&BillingLogSinkConflictAudit{}).Where("id = ?", audit.ID).Updates(map[string]any{
				"projection_id": audit.ProjectionID, "expected_payload_hash": audit.ExpectedPayloadHash,
				"expected_payload_protocol": audit.ExpectedPayloadProtocol, "receipts": audit.Receipts,
				"distinct_receipts": audit.DistinctReceipts, "physical_rows": audit.PhysicalRows,
				"state": audit.State, "version": audit.Version, "last_detected_ms": audit.LastDetectedMs,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func FindOpenBillingLogSinkConflicts(
	ctx context.Context,
	afterID int64,
	limit int,
) ([]BillingLogSinkConflictAudit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil {
		return nil, errors.New("billing database is unavailable")
	}
	if afterID < 0 {
		return nil, ErrBillingLogSinkConflictInvalid
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var audits []BillingLogSinkConflictAudit
	err := DB.WithContext(ctx).Where("state = ? AND id > ?", BillingLogSinkConflictStateOpen, afterID).
		Order("id asc").Limit(limit).Find(&audits).Error
	return audits, err
}

func sameBillingLogSinkConflictResolution(
	resolution *BillingLogSinkConflictResolution,
	audit *BillingLogSinkConflictAudit,
	actorUserID int,
	reason string,
) bool {
	return resolution != nil && audit != nil &&
		resolution.ConflictAuditID == audit.ID && resolution.ConflictVersion == audit.Version &&
		resolution.OperationKey == audit.OperationKey && resolution.ActorUserID == actorUserID &&
		resolution.Reason == reason && resolution.VerifiedPayloadHash == audit.ExpectedPayloadHash &&
		resolution.VerifiedPayloadProtocol == audit.ExpectedPayloadProtocol
}

// ResolveAndRequeueBillingLogSinkConflict verifies that the raw sink now has
// exactly the frozen intended receipt, appends immutable operator evidence,
// and requeues the projection in one main-database transaction.
func ResolveAndRequeueBillingLogSinkConflict(
	ctx context.Context,
	auditID int64,
	expectedVersion int64,
	actorUserID int,
	reason string,
	now time.Time,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reason = strings.TrimSpace(reason)
	if DB == nil || LOG_DB == nil || auditID <= 0 || expectedVersion <= 0 || actorUserID <= 0 ||
		reason == "" || len(reason) > maxBillingLogSinkConflictReasonBytes || !utf8.ValidString(reason) {
		return ErrBillingLogSinkConflictInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}

	var snapshot BillingLogSinkConflictAudit
	if err := DB.WithContext(ctx).Where("id = ?", auditID).First(&snapshot).Error; err != nil {
		return err
	}
	if snapshot.State == BillingLogSinkConflictStateResolved && snapshot.Version == expectedVersion {
		var existing BillingLogSinkConflictResolution
		if err := DB.WithContext(ctx).Where(
			"conflict_audit_id = ? AND conflict_version = ?", snapshot.ID, snapshot.Version,
		).First(&existing).Error; err != nil {
			return err
		}
		if sameBillingLogSinkConflictResolution(&existing, &snapshot, actorUserID, reason) {
			return nil
		}
		return ErrBillingLogSinkConflictPrecondition
	}
	if snapshot.State != BillingLogSinkConflictStateOpen || snapshot.Version != expectedVersion ||
		snapshot.ProjectionID <= 0 || snapshot.ExpectedPayloadHash == "" ||
		snapshot.ExpectedPayloadProtocol != billingLogPayloadProtocol {
		return ErrBillingLogSinkConflictPrecondition
	}
	receipts, err := getBillingLogSinkReceipts(ctx, snapshot.OperationKey)
	if err != nil {
		return err
	}
	if err := validateBillingLogSinkReceipts(
		ctx, snapshot.OperationKey, receipts, snapshot.ExpectedPayloadHash, snapshot.ExpectedPayloadProtocol,
	); err != nil {
		return ErrBillingLogSinkConflictPrecondition
	}

	nowMs := now.UnixMilli()
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var audit BillingLogSinkConflictAudit
		if err := lockForUpdate(tx).Where("id = ?", auditID).First(&audit).Error; err != nil {
			return err
		}
		if audit.State == BillingLogSinkConflictStateResolved && audit.Version == expectedVersion {
			var existing BillingLogSinkConflictResolution
			if err := tx.Where(
				"conflict_audit_id = ? AND conflict_version = ?", audit.ID, audit.Version,
			).First(&existing).Error; err != nil {
				return err
			}
			if sameBillingLogSinkConflictResolution(&existing, &audit, actorUserID, reason) {
				return nil
			}
			return ErrBillingLogSinkConflictPrecondition
		}
		if audit.State != BillingLogSinkConflictStateOpen || audit.Version != expectedVersion ||
			audit.OperationKey != snapshot.OperationKey || audit.ProjectionID != snapshot.ProjectionID ||
			audit.ExpectedPayloadHash != snapshot.ExpectedPayloadHash ||
			audit.ExpectedPayloadProtocol != snapshot.ExpectedPayloadProtocol {
			return ErrBillingLogSinkConflictPrecondition
		}
		var projection BillingLogProjection
		if err := lockForUpdate(tx).Where("id = ? AND operation_key = ?", audit.ProjectionID, audit.OperationKey).
			First(&projection).Error; err != nil {
			return err
		}
		if projection.State != BillingLogProjectionStateFailed ||
			(projection.FailureCode != BillingLogProjectionFailureSinkReceiptConflict &&
				projection.FailureCode != BillingLogProjectionFailureSinkReceiptConflictLate) ||
			projection.Disposition != BillingLogProjectionDispositionPending || !projection.Required ||
			projection.PayloadHash != audit.ExpectedPayloadHash ||
			projection.PayloadProtocol != audit.ExpectedPayloadProtocol {
			return ErrBillingLogSinkConflictPrecondition
		}
		resolution := &BillingLogSinkConflictResolution{
			ConflictAuditID: audit.ID, ConflictVersion: audit.Version, OperationKey: audit.OperationKey,
			ActorUserID: actorUserID, Reason: reason, VerifiedPayloadHash: audit.ExpectedPayloadHash,
			VerifiedPayloadProtocol: audit.ExpectedPayloadProtocol, ResolvedTimeMs: nowMs,
		}
		if err := tx.Create(resolution).Error; err != nil {
			return err
		}
		resolved := tx.Model(&BillingLogSinkConflictAudit{}).Where("id = ? AND state = ? AND version = ?",
			audit.ID, BillingLogSinkConflictStateOpen, audit.Version).Updates(map[string]any{
			"state": BillingLogSinkConflictStateResolved, "last_resolved_ms": nowMs,
			"last_resolved_by": actorUserID, "last_resolution_reason": reason,
		})
		if resolved.Error != nil {
			return resolved.Error
		}
		if resolved.RowsAffected != 1 {
			return ErrBillingLogSinkConflictPrecondition
		}
		updated := tx.Model(&BillingLogProjection{}).Where(
			"id = ? AND state = ? AND failure_code IN ?", projection.ID, BillingLogProjectionStateFailed,
			[]string{BillingLogProjectionFailureSinkReceiptConflict, BillingLogProjectionFailureSinkReceiptConflictLate},
		).Updates(map[string]any{
			"state": BillingLogProjectionStatePending, "lease_owner": "", "lease_until_ms": 0,
			"attempts": 0, "next_retry_ms": 0, "last_error": "", "failure_code": "",
			"updated_time_ms": nowMs, "completed_time_ms": 0,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrBillingLogSinkConflictPrecondition
		}
		return nil
	})
}

func CountOpenBillingLogSinkConflicts(ctx context.Context) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if DB == nil {
		return 0, errors.New("billing database is unavailable")
	}
	var count int64
	err := DB.WithContext(ctx).Model(&BillingLogSinkConflictAudit{}).
		Where("state = ?", BillingLogSinkConflictStateOpen).Count(&count).Error
	return count, err
}
