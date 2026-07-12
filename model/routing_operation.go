package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingOperationTypeCanaryAutoRollback = "canary_auto_rollback"

	RoutingOperationStatusPending    RoutingOperationStatus = "pending"
	RoutingOperationStatusRunning    RoutingOperationStatus = "running"
	RoutingOperationStatusSucceeded  RoutingOperationStatus = "succeeded"
	RoutingOperationStatusFailed     RoutingOperationStatus = "failed"
	RoutingOperationStatusSuperseded RoutingOperationStatus = "superseded"

	routingOperationSchemaVersion  = 1
	routingOperationReasonMaxRunes = 512
	routingOperationErrorMaxRunes  = 4_096
	routingOperationMaxClaimMs     = int64(5 * 60 * 1_000)
)

var (
	ErrRoutingOperationInvalid   = errors.New("invalid routing operation")
	ErrRoutingOperationClaimLost = errors.New("routing operation claim lost")
)

type RoutingOperationStatus string

type RoutingOperationSpec struct {
	Type                 string `json:"type"`
	EvaluationHash       string `json:"evaluation_hash"`
	PoolID               int    `json:"pool_id"`
	ExpectedRevision     int64  `json:"expected_revision"`
	ExpectedActivationID int64  `json:"expected_activation_id"`
	ActorID              int    `json:"actor_id"`
	Reason               string `json:"reason"`
}

type RoutingOperationResult struct {
	Revision     int64 `json:"revision"`
	ActivationID int64 `json:"activation_id"`
	OutboxID     int64 `json:"outbox_id"`
}

type RoutingOperation struct {
	ID                   int64                  `json:"id" gorm:"primaryKey"`
	OperationType        string                 `json:"type" gorm:"column:operation_type;type:varchar(64);index;not null"`
	IdempotencyHash      string                 `json:"idempotency_hash" gorm:"type:char(64);uniqueIndex;not null"`
	CreateToken          string                 `json:"-" gorm:"type:char(32);not null"`
	EvaluationHash       string                 `json:"evaluation_hash" gorm:"type:char(64);index;not null"`
	PoolID               int                    `json:"pool_id" gorm:"index;not null"`
	ExpectedRevision     int64                  `json:"expected_revision" gorm:"bigint;index;not null"`
	ExpectedActivationID int64                  `json:"expected_activation_id" gorm:"bigint;index;not null"`
	ActorID              int                    `json:"actor_id" gorm:"index;not null"`
	Reason               string                 `json:"reason" gorm:"type:varchar(512);not null"`
	Status               RoutingOperationStatus `json:"status" gorm:"type:varchar(24);index;not null"`
	ClaimToken           string                 `json:"-" gorm:"type:char(32);index;not null"`
	ClaimUntilMs         int64                  `json:"claim_until_ms" gorm:"bigint;index;not null"`
	Attempts             int                    `json:"attempts" gorm:"not null"`
	NextRetryMs          int64                  `json:"next_retry_ms" gorm:"bigint;index;not null"`
	LastError            string                 `json:"last_error" gorm:"type:text;not null"`
	ResultRevision       int64                  `json:"result_revision" gorm:"bigint;index;not null"`
	ResultActivationID   int64                  `json:"result_activation_id" gorm:"bigint;index;not null"`
	ResultOutboxID       int64                  `json:"result_outbox_id" gorm:"bigint;index;not null"`
	CreatedTimeMs        int64                  `json:"created_time_ms" gorm:"bigint;index;not null"`
	UpdatedTimeMs        int64                  `json:"updated_time_ms" gorm:"bigint;index;not null"`
	CompletedTimeMs      int64                  `json:"completed_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingOperation) TableName() string {
	return "routing_operations"
}

func CreateRoutingOperationContext(
	ctx context.Context,
	spec RoutingOperationSpec,
) (RoutingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, idempotencyHash, err := normalizeRoutingOperationSpec(spec)
	if err != nil {
		return RoutingOperation{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return RoutingOperation{}, false, err
	}
	createToken, err := newRoutingPersistenceToken()
	if err != nil {
		return RoutingOperation{}, false, err
	}
	nowMs := time.Now().UnixMilli()
	operation := RoutingOperation{
		OperationType:        normalized.Type,
		IdempotencyHash:      idempotencyHash,
		CreateToken:          createToken,
		EvaluationHash:       normalized.EvaluationHash,
		PoolID:               normalized.PoolID,
		ExpectedRevision:     normalized.ExpectedRevision,
		ExpectedActivationID: normalized.ExpectedActivationID,
		ActorID:              normalized.ActorID,
		Reason:               normalized.Reason,
		Status:               RoutingOperationStatusPending,
		CreatedTimeMs:        nowMs,
		UpdatedTimeMs:        nowMs,
	}
	created := DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "idempotency_hash"}},
		DoNothing: true,
	}).Create(&operation)
	if created.Error != nil {
		return RoutingOperation{}, false, created.Error
	}
	var stored RoutingOperation
	if err := DB.WithContext(ctx).Where("idempotency_hash = ?", idempotencyHash).First(&stored).Error; err != nil {
		return RoutingOperation{}, false, err
	}
	if stored.OperationType != normalized.Type || stored.EvaluationHash != normalized.EvaluationHash ||
		stored.PoolID != normalized.PoolID || stored.ExpectedRevision != normalized.ExpectedRevision ||
		stored.ExpectedActivationID != normalized.ExpectedActivationID || stored.ActorID != normalized.ActorID ||
		stored.Reason != normalized.Reason || !validRoutingPersistenceToken(stored.CreateToken) {
		return RoutingOperation{}, false, ErrRoutingOperationInvalid
	}
	return stored, stored.CreateToken == createToken, nil
}

func ClaimRoutingOperationContext(
	ctx context.Context,
	operationType string,
	nowMs int64,
	leaseMs int64,
) (*RoutingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingOperationType(operationType) || nowMs <= 0 || leaseMs <= 0 ||
		leaseMs > routingOperationMaxClaimMs || nowMs > math.MaxInt64-leaseMs {
		return nil, ErrRoutingOperationInvalid
	}
	claimToken, err := newRoutingPersistenceToken()
	if err != nil {
		return nil, err
	}
	var claimed RoutingOperation
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		eligible := "((status = ? AND next_retry_ms <= ?) OR (status = ? AND claim_until_ms <= ?))"
		query := lockForUpdate(tx.WithContext(ctx)).
			Where("operation_type = ?", operationType).
			Where(eligible, RoutingOperationStatusPending, nowMs, RoutingOperationStatusRunning, nowMs).
			Order("id asc")
		if err := query.First(&claimed).Error; err != nil {
			return err
		}
		if claimed.Attempts == int(^uint(0)>>1) {
			return ErrRoutingOperationInvalid
		}
		updated := tx.WithContext(ctx).Model(&RoutingOperation{}).
			Where("id = ? AND operation_type = ?", claimed.ID, operationType).
			Where(eligible, RoutingOperationStatusPending, nowMs, RoutingOperationStatusRunning, nowMs).
			Updates(map[string]any{
				"status":          RoutingOperationStatusRunning,
				"claim_token":     claimToken,
				"claim_until_ms":  nowMs + leaseMs,
				"attempts":        claimed.Attempts + 1,
				"updated_time_ms": nowMs,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRoutingOperationClaimLost
		}
		return tx.WithContext(ctx).Where("id = ? AND claim_token = ?", claimed.ID, claimToken).First(&claimed).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &claimed, nil
}

func HasRunnableRoutingOperationContext(
	ctx context.Context,
	operationType string,
	nowMs int64,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingOperationType(operationType) || nowMs <= 0 {
		return false, ErrRoutingOperationInvalid
	}
	eligible := "((status = ? AND next_retry_ms <= ?) OR (status = ? AND claim_until_ms <= ?))"
	var operation RoutingOperation
	err := DB.WithContext(ctx).
		Select("id").
		Where("operation_type = ?", operationType).
		Where(eligible, RoutingOperationStatusPending, nowMs, RoutingOperationStatusRunning, nowMs).
		Order("id asc").
		First(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

func RetryRoutingOperationContext(
	ctx context.Context,
	id int64,
	claimToken string,
	nowMs int64,
	nextRetryMs int64,
	operationErr error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || len(claimToken) != 32 || nowMs <= 0 || nextRetryMs < nowMs || operationErr == nil {
		return ErrRoutingOperationInvalid
	}
	lastError := routingOperationErrorText(operationErr)
	if lastError == "" {
		return ErrRoutingOperationInvalid
	}
	updated := DB.WithContext(ctx).Model(&RoutingOperation{}).
		Where("id = ? AND status = ? AND claim_token = ? AND claim_until_ms > ?",
			id, RoutingOperationStatusRunning, claimToken, nowMs,
		).
		Updates(map[string]any{
			"status":               RoutingOperationStatusPending,
			"claim_token":          "",
			"claim_until_ms":       0,
			"next_retry_ms":        nextRetryMs,
			"last_error":           lastError,
			"result_revision":      0,
			"result_activation_id": 0,
			"result_outbox_id":     0,
			"completed_time_ms":    0,
			"updated_time_ms":      nowMs,
		})
	return routingOperationCASResult(updated)
}

func SucceedRoutingOperationContext(
	ctx context.Context,
	id int64,
	claimToken string,
	nowMs int64,
	result RoutingOperationResult,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if result.Revision <= 0 || result.ActivationID <= 0 || result.OutboxID <= 0 {
		return ErrRoutingOperationInvalid
	}
	return finishRoutingOperationContext(
		ctx, id, claimToken, nowMs, RoutingOperationStatusSucceeded, "", result,
	)
}

func FailRoutingOperationContext(
	ctx context.Context,
	id int64,
	claimToken string,
	nowMs int64,
	operationErr error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if operationErr == nil {
		return ErrRoutingOperationInvalid
	}
	return finishRoutingOperationContext(
		ctx, id, claimToken, nowMs, RoutingOperationStatusFailed,
		routingOperationErrorText(operationErr), RoutingOperationResult{},
	)
}

func SupersedeRoutingOperationContext(
	ctx context.Context,
	id int64,
	claimToken string,
	nowMs int64,
	reason string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reason = routingOperationText(reason, routingOperationErrorMaxRunes)
	if reason == "" {
		return ErrRoutingOperationInvalid
	}
	return finishRoutingOperationContext(
		ctx, id, claimToken, nowMs, RoutingOperationStatusSuperseded, reason, RoutingOperationResult{},
	)
}

func finishRoutingOperationContext(
	ctx context.Context,
	id int64,
	claimToken string,
	nowMs int64,
	status RoutingOperationStatus,
	lastError string,
	result RoutingOperationResult,
) error {
	if id <= 0 || len(claimToken) != 32 || nowMs <= 0 ||
		!validRoutingOperationTerminalState(status, lastError, result) {
		return ErrRoutingOperationInvalid
	}
	updated := DB.WithContext(ctx).Model(&RoutingOperation{}).
		Where("id = ? AND status = ? AND claim_token = ? AND claim_until_ms > ?",
			id, RoutingOperationStatusRunning, claimToken, nowMs,
		).
		Updates(routingOperationTerminalUpdates(status, lastError, result, nowMs))
	return routingOperationCASResult(updated)
}

func finishRoutingOperationTx(
	ctx context.Context,
	tx *gorm.DB,
	operation RoutingOperation,
	nowMs int64,
	status RoutingOperationStatus,
	lastError string,
	result RoutingOperationResult,
) (RoutingOperation, error) {
	if operation.ID <= 0 || len(operation.ClaimToken) != 32 || nowMs <= 0 ||
		!validRoutingOperationTerminalState(status, lastError, result) {
		return RoutingOperation{}, ErrRoutingOperationInvalid
	}
	updated := tx.WithContext(ctx).Model(&RoutingOperation{}).
		Where("id = ? AND status = ? AND claim_token = ? AND claim_until_ms > ?",
			operation.ID, RoutingOperationStatusRunning, operation.ClaimToken, nowMs,
		).
		Updates(routingOperationTerminalUpdates(status, lastError, result, nowMs))
	if err := routingOperationCASResult(updated); err != nil {
		return RoutingOperation{}, err
	}
	var stored RoutingOperation
	if err := tx.WithContext(ctx).Where("id = ?", operation.ID).First(&stored).Error; err != nil {
		return RoutingOperation{}, err
	}
	return stored, nil
}

func validRoutingOperationTerminalState(
	status RoutingOperationStatus,
	lastError string,
	result RoutingOperationResult,
) bool {
	switch status {
	case RoutingOperationStatusSucceeded:
		return lastError == "" && result.Revision > 0 && result.ActivationID > 0 && result.OutboxID > 0
	case RoutingOperationStatusFailed, RoutingOperationStatusSuperseded:
		return lastError != "" && result == (RoutingOperationResult{})
	default:
		return false
	}
}

func routingOperationTerminalUpdates(
	status RoutingOperationStatus,
	lastError string,
	result RoutingOperationResult,
	nowMs int64,
) map[string]any {
	return map[string]any{
		"status":               status,
		"claim_token":          "",
		"claim_until_ms":       0,
		"next_retry_ms":        0,
		"last_error":           lastError,
		"result_revision":      result.Revision,
		"result_activation_id": result.ActivationID,
		"result_outbox_id":     result.OutboxID,
		"updated_time_ms":      nowMs,
		"completed_time_ms":    nowMs,
	}
}

func routingOperationCASResult(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRoutingOperationClaimLost
	}
	return nil
}

func normalizeRoutingOperationSpec(spec RoutingOperationSpec) (RoutingOperationSpec, string, error) {
	spec.Type = strings.TrimSpace(spec.Type)
	spec.EvaluationHash = strings.ToLower(strings.TrimSpace(spec.EvaluationHash))
	spec.Reason = strings.TrimSpace(spec.Reason)
	if !validRoutingOperationType(spec.Type) || !validRoutingHash(spec.EvaluationHash) ||
		spec.PoolID <= 0 || spec.ExpectedRevision <= 0 || spec.ExpectedActivationID <= 0 || spec.ActorID < 0 ||
		!utf8.ValidString(spec.Reason) || utf8.RuneCountInString(spec.Reason) > routingOperationReasonMaxRunes {
		return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
	}
	canonical, err := common.Marshal(struct {
		SchemaVersion        int    `json:"schema_version"`
		Type                 string `json:"type"`
		EvaluationHash       string `json:"evaluation_hash"`
		PoolID               int    `json:"pool_id"`
		ExpectedRevision     int64  `json:"expected_revision"`
		ExpectedActivationID int64  `json:"expected_activation_id"`
	}{
		SchemaVersion:        routingOperationSchemaVersion,
		Type:                 spec.Type,
		EvaluationHash:       spec.EvaluationHash,
		PoolID:               spec.PoolID,
		ExpectedRevision:     spec.ExpectedRevision,
		ExpectedActivationID: spec.ExpectedActivationID,
	})
	if err != nil {
		return RoutingOperationSpec{}, "", err
	}
	return spec, routingPolicyHash(canonical), nil
}

func validRoutingOperationType(operationType string) bool {
	return operationType == RoutingOperationTypeCanaryAutoRollback
}

func validRoutingHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func newRoutingPersistenceToken() (string, error) {
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(tokenBytes[:]), nil
}

func validRoutingPersistenceToken(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func routingOperationErrorText(operationErr error) string {
	if operationErr == nil {
		return ""
	}
	return routingOperationText(operationErr.Error(), routingOperationErrorMaxRunes)
}

func routingOperationText(value string, maxRunes int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "?"))
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	return value
}
