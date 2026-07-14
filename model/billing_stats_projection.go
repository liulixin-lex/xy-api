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
	BillingStatsProjectionProtocol = 1

	BillingStatsProjectionKindAccepted           = "accepted"
	BillingStatsProjectionKindTaskTerminal       = "task_terminal"
	BillingStatsProjectionKindMidjourneyTerminal = "midjourney_terminal"

	BillingStatsProjectionStatePending   = "pending"
	BillingStatsProjectionStateRunning   = "running"
	BillingStatsProjectionStateCompleted = "completed"
	BillingStatsProjectionStateFailed    = "failed"

	billingStatsProjectionMaxLease        = 10 * time.Minute
	billingStatsProjectionErrorMaxBytes   = 1024
	billingStatsProjectionOwnerMaxBytes   = 128
	billingStatsProjectionFailureMaxBytes = 64
	billingStatsProjectionMaxAttempts     = 100
	billingStatsProjectionMutationTimeout = 3 * time.Second
)

var (
	ErrBillingStatsProjectionInvalid      = errors.New("billing stats projection is invalid")
	ErrBillingStatsProjectionConflict     = errors.New("billing stats projection conflicts with the durable receipt")
	ErrBillingStatsProjectionNotClaimed   = errors.New("billing stats projection is not claimed by this worker")
	ErrBillingStatsProjectionLeaseExpired = errors.New("billing stats projection lease has expired")
)

// BillingStatsProjection is a durable intent for derived usage counters. The
// authoritative billing transaction creates the row; a leased worker applies
// the counters and completes this receipt in one main-database transaction.
type BillingStatsProjection struct {
	ID              int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	OperationKey    string `json:"operation_key" gorm:"type:varchar(64);not null;uniqueIndex:uidx_billing_stats_projection_operation"`
	ProtocolVersion int    `json:"protocol_version" gorm:"not null"`
	Kind            string `json:"kind" gorm:"type:varchar(24);not null;index;uniqueIndex:uidx_billing_stats_projection_source,priority:1"`
	ReferenceID     int64  `json:"reference_id" gorm:"not null;index;uniqueIndex:uidx_billing_stats_projection_source,priority:2"`

	UserID              int    `json:"user_id" gorm:"not null;index"`
	ChannelID           int    `json:"channel_id" gorm:"not null;index"`
	QuotaDelta          int    `json:"quota_delta" gorm:"not null"`
	RequestDelta        int    `json:"request_delta" gorm:"not null"`
	DataExportRequired  bool   `json:"data_export_required" gorm:"not null"`
	DataExportUsername  string `json:"data_export_username,omitempty" gorm:"type:varchar(191)"`
	DataExportModelName string `json:"data_export_model_name,omitempty" gorm:"type:varchar(191)"`
	DataExportCreatedAt int64  `json:"data_export_created_at"`
	DataExportTokenUsed int    `json:"data_export_token_used"`
	DataExportUseGroup  string `json:"data_export_use_group,omitempty" gorm:"type:varchar(64)"`
	DataExportTokenID   int    `json:"data_export_token_id"`
	DataExportNodeName  string `json:"data_export_node_name,omitempty" gorm:"type:varchar(128)"`

	State        string `json:"state" gorm:"type:varchar(16);not null;index:idx_billing_stats_projection_recovery,priority:1"`
	LeaseOwner   string `json:"lease_owner,omitempty" gorm:"type:varchar(128)"`
	LeaseUntilMs int64  `json:"lease_until_ms" gorm:"index:idx_billing_stats_projection_lease"`
	Attempts     int    `json:"attempts" gorm:"not null"`
	NextRetryMs  int64  `json:"next_retry_ms" gorm:"index:idx_billing_stats_projection_recovery,priority:2"`
	LastError    string `json:"last_error,omitempty" gorm:"type:varchar(1024)"`
	FailureCode  string `json:"failure_code,omitempty" gorm:"type:varchar(64)"`

	UserOutcome       string `json:"user_outcome,omitempty" gorm:"type:varchar(32)"`
	ChannelOutcome    string `json:"channel_outcome,omitempty" gorm:"type:varchar(32)"`
	DataExportOutcome string `json:"data_export_outcome,omitempty" gorm:"type:varchar(32)"`

	CreatedTimeMs   int64 `json:"created_time_ms" gorm:"not null"`
	UpdatedTimeMs   int64 `json:"updated_time_ms" gorm:"not null"`
	CompletedTimeMs int64 `json:"completed_time_ms"`
}

type BillingStatsProjectionSpec struct {
	OperationKey        string
	Kind                string
	ReferenceID         int64
	UserID              int
	ChannelID           int
	QuotaDelta          int
	RequestDelta        int
	DataExportRequired  bool
	DataExportUsername  string
	DataExportModelName string
	DataExportCreatedAt int64
	DataExportTokenUsed int
	DataExportUseGroup  string
	DataExportTokenID   int
	DataExportNodeName  string
}

func validateBillingStatsProjectionSpec(spec BillingStatsProjectionSpec) error {
	spec.OperationKey = strings.TrimSpace(spec.OperationKey)
	if spec.OperationKey == "" || len(spec.OperationKey) > maxBillingLogOperationKeyBytes ||
		!utf8.ValidString(spec.OperationKey) || spec.ReferenceID <= 0 || spec.UserID <= 0 || spec.ChannelID <= 0 ||
		spec.QuotaDelta < 0 || spec.QuotaDelta > common.MaxQuota {
		return ErrBillingStatsProjectionInvalid
	}
	switch spec.Kind {
	case BillingStatsProjectionKindAccepted:
		if spec.RequestDelta != 1 {
			return ErrBillingStatsProjectionInvalid
		}
	case BillingStatsProjectionKindTaskTerminal, BillingStatsProjectionKindMidjourneyTerminal:
		if spec.RequestDelta != 0 || spec.QuotaDelta <= 0 {
			return ErrBillingStatsProjectionInvalid
		}
	default:
		return ErrBillingStatsProjectionInvalid
	}
	if spec.DataExportRequired && (spec.DataExportCreatedAt <= 0 || spec.DataExportTokenUsed < 0 ||
		spec.DataExportTokenUsed > math.MaxInt32 || spec.DataExportTokenID < 0 ||
		!validBillingQuotaDataDimension(spec.DataExportUsername) ||
		!validBillingQuotaDataDimension(spec.DataExportModelName) ||
		!validBillingQuotaDataDimension(spec.DataExportUseGroup) ||
		!validBillingQuotaDataDimension(spec.DataExportNodeName)) {
		return ErrBillingStatsProjectionInvalid
	}
	return nil
}

func normalizeBillingStatsProjectionSpec(spec BillingStatsProjectionSpec) (BillingStatsProjectionSpec, string) {
	spec.OperationKey = strings.TrimSpace(spec.OperationKey)
	if !spec.DataExportRequired {
		spec.DataExportUsername = ""
		spec.DataExportModelName = ""
		spec.DataExportCreatedAt = 0
		spec.DataExportTokenUsed = 0
		spec.DataExportUseGroup = ""
		spec.DataExportTokenID = 0
		spec.DataExportNodeName = ""
		return spec, BillingQuotaDataOutcomeNotRequired
	}
	if spec.DataExportCreatedAt <= 0 || spec.DataExportTokenUsed < 0 || spec.DataExportTokenUsed > math.MaxInt32 ||
		spec.DataExportTokenID < 0 || !validBillingQuotaDataDimension(spec.DataExportUsername) ||
		!validBillingQuotaDataDimension(spec.DataExportModelName) ||
		!validBillingQuotaDataDimension(spec.DataExportUseGroup) ||
		!validBillingQuotaDataDimension(spec.DataExportNodeName) {
		spec.DataExportRequired = false
		spec.DataExportUsername = ""
		spec.DataExportModelName = ""
		spec.DataExportCreatedAt = 0
		spec.DataExportTokenUsed = 0
		spec.DataExportUseGroup = ""
		spec.DataExportTokenID = 0
		spec.DataExportNodeName = ""
		return spec, BillingQuotaDataOutcomeSkippedInvalid
	}
	return spec, ""
}

func sameBillingStatsProjectionSpec(projection *BillingStatsProjection, spec BillingStatsProjectionSpec) bool {
	return projection != nil && projection.ProtocolVersion == BillingStatsProjectionProtocol &&
		projection.OperationKey == strings.TrimSpace(spec.OperationKey) && projection.Kind == spec.Kind &&
		projection.ReferenceID == spec.ReferenceID && projection.UserID == spec.UserID &&
		projection.ChannelID == spec.ChannelID && projection.QuotaDelta == spec.QuotaDelta &&
		projection.RequestDelta == spec.RequestDelta && projection.DataExportRequired == spec.DataExportRequired &&
		projection.DataExportUsername == spec.DataExportUsername && projection.DataExportModelName == spec.DataExportModelName &&
		projection.DataExportCreatedAt == spec.DataExportCreatedAt && projection.DataExportTokenUsed == spec.DataExportTokenUsed &&
		projection.DataExportUseGroup == spec.DataExportUseGroup && projection.DataExportTokenID == spec.DataExportTokenID &&
		projection.DataExportNodeName == spec.DataExportNodeName
}

// CreateBillingStatsProjectionTx freezes an idempotent stats intent inside the
// caller's authoritative billing transaction. A conflicting replay fails closed.
func CreateBillingStatsProjectionTx(
	tx *gorm.DB,
	spec BillingStatsProjectionSpec,
	now time.Time,
) (*BillingStatsProjection, bool, error) {
	spec, initialDataExportOutcome := normalizeBillingStatsProjectionSpec(spec)
	if tx == nil || validateBillingStatsProjectionSpec(spec) != nil {
		return nil, false, ErrBillingStatsProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	spec.OperationKey = strings.TrimSpace(spec.OperationKey)
	projection := &BillingStatsProjection{
		OperationKey:        spec.OperationKey,
		ProtocolVersion:     BillingStatsProjectionProtocol,
		Kind:                spec.Kind,
		ReferenceID:         spec.ReferenceID,
		UserID:              spec.UserID,
		ChannelID:           spec.ChannelID,
		QuotaDelta:          spec.QuotaDelta,
		RequestDelta:        spec.RequestDelta,
		DataExportRequired:  spec.DataExportRequired,
		DataExportUsername:  spec.DataExportUsername,
		DataExportModelName: spec.DataExportModelName,
		DataExportCreatedAt: spec.DataExportCreatedAt,
		DataExportTokenUsed: spec.DataExportTokenUsed,
		DataExportUseGroup:  spec.DataExportUseGroup,
		DataExportTokenID:   spec.DataExportTokenID,
		DataExportNodeName:  spec.DataExportNodeName,
		DataExportOutcome:   initialDataExportOutcome,
		State:               BillingStatsProjectionStatePending,
		CreatedTimeMs:       now.UnixMilli(),
		UpdatedTimeMs:       now.UnixMilli(),
	}
	created := tx.Clauses(clause.OnConflict{
		DoNothing: true,
	}).Create(projection)
	if created.Error != nil {
		return nil, false, created.Error
	}
	insertedID := projection.ID
	var existing BillingStatsProjection
	if err := lockForUpdate(tx).Where("operation_key = ?", spec.OperationKey).First(&existing).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, err
		}
		if sourceErr := lockForUpdate(tx).Where("kind = ? AND reference_id = ?", spec.Kind, spec.ReferenceID).
			First(&existing).Error; sourceErr != nil {
			return nil, false, sourceErr
		}
		return &existing, false, ErrBillingStatsProjectionConflict
	}
	if !sameBillingStatsProjectionSpec(&existing, spec) {
		return &existing, false, ErrBillingStatsProjectionConflict
	}
	return &existing, created.RowsAffected == 1 && insertedID > 0 && insertedID == existing.ID, nil
}

func GetBillingStatsProjection(ctx context.Context, projectionID int64) (*BillingStatsProjection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if projectionID <= 0 {
		return nil, ErrBillingStatsProjectionInvalid
	}
	var projection BillingStatsProjection
	if err := DB.WithContext(ctx).Where("id = ?", projectionID).First(&projection).Error; err != nil {
		return nil, err
	}
	return &projection, nil
}

func HasRecoverableBillingStatsProjections(now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	var id int64
	err := DB.Model(&BillingStatsProjection{}).Select("id").
		Where("(state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?)",
			BillingStatsProjectionStatePending, now.UnixMilli(),
			BillingStatsProjectionStateRunning, now.UnixMilli()).
		Order("id asc").Limit(1).Pluck("id", &id).Error
	return err == nil && id > 0
}

func ClaimBillingStatsProjection(
	ctx context.Context,
	projectionID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*BillingStatsProjection, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if projectionID <= 0 || owner == "" || len(owner) > billingStatsProjectionOwnerMaxBytes ||
		!utf8.ValidString(owner) || leaseDuration <= 0 || leaseDuration > billingStatsProjectionMaxLease {
		return nil, false, ErrBillingStatsProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	leaseUntilMs := now.Add(leaseDuration).UnixMilli()
	var projection BillingStatsProjection
	claimed := false
	exhausted := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", projectionID).First(&projection).Error; err != nil {
			return err
		}
		eligible := (projection.State == BillingStatsProjectionStatePending && projection.NextRetryMs <= nowMs) ||
			(projection.State == BillingStatsProjectionStateRunning && projection.LeaseUntilMs <= nowMs)
		if !eligible {
			return nil
		}
		if projection.Attempts >= billingStatsProjectionMaxAttempts {
			updated := tx.Model(&BillingStatsProjection{}).
				Where("id = ? AND ((state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?))",
					projection.ID, BillingStatsProjectionStatePending, nowMs,
					BillingStatsProjectionStateRunning, nowMs).
				Updates(map[string]any{
					"state":             BillingStatsProjectionStateFailed,
					"lease_owner":       "",
					"lease_until_ms":    0,
					"next_retry_ms":     0,
					"last_error":        "",
					"failure_code":      "retry_exhausted",
					"updated_time_ms":   nowMs,
					"completed_time_ms": nowMs,
				})
			if updated.Error != nil {
				return updated.Error
			}
			exhausted = updated.RowsAffected == 1
			if exhausted {
				projection.State = BillingStatsProjectionStateFailed
				projection.FailureCode = "retry_exhausted"
				projection.CompletedTimeMs = nowMs
			}
			return nil
		}
		attempts := projection.Attempts
		if attempts < math.MaxInt32 {
			attempts++
		}
		updated := tx.Model(&BillingStatsProjection{}).
			Where("id = ? AND ((state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?))",
				projection.ID, BillingStatsProjectionStatePending, nowMs,
				BillingStatsProjectionStateRunning, nowMs).
			Updates(map[string]any{
				"state":           BillingStatsProjectionStateRunning,
				"lease_owner":     owner,
				"lease_until_ms":  leaseUntilMs,
				"attempts":        attempts,
				"updated_time_ms": nowMs,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return nil
		}
		claimed = true
		projection.State = BillingStatsProjectionStateRunning
		projection.LeaseOwner = owner
		projection.LeaseUntilMs = leaseUntilMs
		projection.Attempts = attempts
		projection.UpdatedTimeMs = nowMs
		return nil
	})
	if err == nil && exhausted {
		common.SysError(fmt.Sprintf("billing stats projection requires manual audit: id=%d code=retry_exhausted", projectionID))
	}
	return &projection, claimed, err
}

func ClaimNextBillingStatsProjection(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*BillingStatsProjection, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	var ids []int64
	err := DB.WithContext(ctx).Model(&BillingStatsProjection{}).Select("id").
		Where("(state = ? AND next_retry_ms <= ?) OR (state = ? AND lease_until_ms <= ?)",
			BillingStatsProjectionStatePending, now.UnixMilli(),
			BillingStatsProjectionStateRunning, now.UnixMilli()).
		Order("id asc").Limit(100).Pluck("id", &ids).Error
	if err != nil {
		return nil, false, err
	}
	for _, id := range ids {
		projection, claimed, claimErr := ClaimBillingStatsProjection(ctx, id, owner, now, leaseDuration)
		if claimErr != nil {
			return nil, false, claimErr
		}
		if claimed {
			return projection, true, nil
		}
	}
	return nil, false, nil
}

func CompleteClaimedBillingStatsProjection(
	ctx context.Context,
	projectionID int64,
	owner string,
	now time.Time,
) (*BillingStatsProjection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if projectionID <= 0 || owner == "" || len(owner) > billingStatsProjectionOwnerMaxBytes || !utf8.ValidString(owner) {
		return nil, ErrBillingStatsProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	var projection BillingStatsProjection
	var outcome billingUsageProjectionResult
	dataExportOutcome := projection.DataExportOutcome
	applied := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("id = ?", projectionID).First(&projection).Error; err != nil {
			return err
		}
		dataExportOutcome = projection.DataExportOutcome
		if projection.State == BillingStatsProjectionStateCompleted {
			return nil
		}
		if projection.State != BillingStatsProjectionStateRunning || projection.LeaseOwner != owner {
			return ErrBillingStatsProjectionNotClaimed
		}
		if projection.LeaseUntilMs <= now.UnixMilli() {
			return ErrBillingStatsProjectionLeaseExpired
		}
		spec := BillingStatsProjectionSpec{
			OperationKey:        projection.OperationKey,
			Kind:                projection.Kind,
			ReferenceID:         projection.ReferenceID,
			UserID:              projection.UserID,
			ChannelID:           projection.ChannelID,
			QuotaDelta:          projection.QuotaDelta,
			RequestDelta:        projection.RequestDelta,
			DataExportRequired:  projection.DataExportRequired,
			DataExportUsername:  projection.DataExportUsername,
			DataExportModelName: projection.DataExportModelName,
			DataExportCreatedAt: projection.DataExportCreatedAt,
			DataExportTokenUsed: projection.DataExportTokenUsed,
			DataExportUseGroup:  projection.DataExportUseGroup,
			DataExportTokenID:   projection.DataExportTokenID,
			DataExportNodeName:  projection.DataExportNodeName,
		}
		if projection.ProtocolVersion != BillingStatsProjectionProtocol || validateBillingStatsProjectionSpec(spec) != nil {
			return ErrBillingStatsProjectionInvalid
		}
		var err error
		outcome, err = applyBillingUsageProjectionTx(tx, billingUsageProjectionSpec{
			OperationKey: projection.OperationKey,
			UserID:       projection.UserID,
			ChannelID:    projection.ChannelID,
			QuotaDelta:   projection.QuotaDelta,
			RequestDelta: projection.RequestDelta,
		})
		if err != nil {
			return err
		}
		if dataExportOutcome == "" {
			dataExportOutcome, err = applyBillingQuotaDataProjectionTx(tx, billingQuotaDataProjectionSpec{
				Required:  projection.DataExportRequired,
				UserID:    projection.UserID,
				Username:  projection.DataExportUsername,
				ModelName: projection.DataExportModelName,
				CreatedAt: projection.DataExportCreatedAt,
				Quota:     projection.QuotaDelta,
				TokenUsed: projection.DataExportTokenUsed,
				UseGroup:  projection.DataExportUseGroup,
				TokenID:   projection.DataExportTokenID,
				ChannelID: projection.ChannelID,
				NodeName:  projection.DataExportNodeName,
			})
			if err != nil {
				return err
			}
		}
		updated := tx.Model(&BillingStatsProjection{}).
			Where("id = ? AND state = ? AND lease_owner = ?", projection.ID, BillingStatsProjectionStateRunning, owner).
			Updates(map[string]any{
				"state":               BillingStatsProjectionStateCompleted,
				"lease_owner":         "",
				"lease_until_ms":      0,
				"next_retry_ms":       0,
				"last_error":          "",
				"failure_code":        "",
				"user_outcome":        outcome.UserOutcome,
				"channel_outcome":     outcome.ChannelOutcome,
				"data_export_outcome": dataExportOutcome,
				"updated_time_ms":     now.UnixMilli(),
				"completed_time_ms":   now.UnixMilli(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrBillingStatsProjectionNotClaimed
		}
		projection.State = BillingStatsProjectionStateCompleted
		projection.LeaseOwner = ""
		projection.LeaseUntilMs = 0
		projection.NextRetryMs = 0
		projection.LastError = ""
		projection.FailureCode = ""
		projection.UserOutcome = outcome.UserOutcome
		projection.ChannelOutcome = outcome.ChannelOutcome
		projection.DataExportOutcome = dataExportOutcome
		projection.UpdatedTimeMs = now.UnixMilli()
		projection.CompletedTimeMs = now.UnixMilli()
		applied = true
		return nil
	})
	if err != nil {
		return &projection, err
	}
	if !applied {
		return &projection, nil
	}
	if warning := billingStatsProjectionWarning(projection.OperationKey, outcome, dataExportOutcome); warning != "" {
		common.SysError(warning)
	}
	return &projection, nil
}

func billingStatsProjectionWarning(operationKey string, usage billingUsageProjectionResult, dataExportOutcome string) string {
	usageWarning := billingUsageProjectionWarning(operationKey, usage)
	dataExportOK := dataExportOutcome == BillingQuotaDataOutcomeNotRequired ||
		dataExportOutcome == BillingQuotaDataOutcomeApplied || dataExportOutcome == BillingQuotaDataOutcomeAppliedSplit
	if usageWarning == "" && dataExportOK {
		return ""
	}
	return fmt.Sprintf("billing stats projection completed with audit outcome: operation=%s user=%s channel=%s data_export=%s",
		operationKey, usage.UserOutcome, usage.ChannelOutcome, dataExportOutcome)
}

func RetryClaimedBillingStatsProjection(
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
		len(owner) > billingStatsProjectionOwnerMaxBytes {
		return ErrBillingStatsProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	detachedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingStatsProjectionMutationTimeout)
	defer cancel()
	var projection BillingStatsProjection
	if err := DB.WithContext(detachedCtx).Select("id", "attempts", "lease_until_ms").
		Where("id = ? AND state = ? AND lease_owner = ?", projectionID, BillingStatsProjectionStateRunning, owner).
		First(&projection).Error; err != nil {
		return err
	}
	if projection.Attempts >= billingStatsProjectionMaxAttempts {
		return failClaimedBillingStatsProjection(detachedCtx, projectionID, owner, now, "retry_exhausted")
	}
	nextRetry := now.Add(billingProjectionRetryDelay(projection.Attempts))
	updated := DB.WithContext(detachedCtx).Model(&BillingStatsProjection{}).
		Where("id = ? AND state = ? AND lease_owner = ? AND lease_until_ms > ?",
			projectionID, BillingStatsProjectionStateRunning, owner, now.UnixMilli()).
		Updates(map[string]any{
			"state":           BillingStatsProjectionStatePending,
			"lease_owner":     "",
			"lease_until_ms":  0,
			"next_retry_ms":   nextRetry.UnixMilli(),
			"last_error":      boundedBillingStatsProjectionError(cause.Error()),
			"updated_time_ms": now.UnixMilli(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrBillingStatsProjectionNotClaimed
	}
	return nil
}

func FailClaimedBillingStatsProjection(
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
		!utf8.ValidString(failureCode) || len(owner) > billingStatsProjectionOwnerMaxBytes ||
		len(failureCode) > billingStatsProjectionFailureMaxBytes {
		return ErrBillingStatsProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	detachedCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingStatsProjectionMutationTimeout)
	defer cancel()
	return failClaimedBillingStatsProjection(detachedCtx, projectionID, owner, now, failureCode)
}

func failClaimedBillingStatsProjection(
	ctx context.Context,
	projectionID int64,
	owner string,
	now time.Time,
	failureCode string,
) error {
	updated := DB.WithContext(ctx).Model(&BillingStatsProjection{}).
		Where("id = ? AND state = ? AND lease_owner = ? AND lease_until_ms > ?",
			projectionID, BillingStatsProjectionStateRunning, owner, now.UnixMilli()).
		Updates(map[string]any{
			"state":             BillingStatsProjectionStateFailed,
			"lease_owner":       "",
			"lease_until_ms":    0,
			"next_retry_ms":     0,
			"last_error":        "",
			"failure_code":      failureCode,
			"updated_time_ms":   now.UnixMilli(),
			"completed_time_ms": now.UnixMilli(),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrBillingStatsProjectionNotClaimed
	}
	common.SysError(fmt.Sprintf("billing stats projection requires manual audit: id=%d code=%s", projectionID, failureCode))
	return nil
}

func boundedBillingStatsProjectionError(message string) string {
	message = common.SanitizeErrorMessage(message)
	if len(message) <= billingStatsProjectionErrorMaxBytes {
		return message
	}
	end := billingStatsProjectionErrorMaxBytes
	for end > 0 && !utf8.ValidString(message[:end]) {
		end--
	}
	return strings.TrimSpace(message[:end])
}
