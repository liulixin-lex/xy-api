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
	RoutingOperationTypeCanaryAutoRollback   = "canary_auto_rollback"
	RoutingOperationTypePolicySimulation     = "policy_simulation"
	RoutingOperationTypeHistoricalSimulation = "historical_simulation"
	RoutingOperationTypePolicyPublish        = "policy_publish"
	RoutingOperationTypePolicyRollback       = "policy_manual_rollback"
	RoutingOperationTypeCostSync             = "cost_sync"
	RoutingOperationTypeActiveProbe          = "active_probe"
	RoutingOperationTypeAuditExport          = "audit_export"
	RoutingOperationTypeBreakerReset         = "breaker_reset"

	RoutingOperationSubjectPolicyDraft     = "policy_draft"
	RoutingOperationSubjectPolicyRevision  = "policy_revision"
	RoutingOperationSubjectRoutingCosts    = "routing_costs"
	RoutingOperationSubjectRoutingProbes   = "routing_probes"
	RoutingOperationSubjectDecisionAudit   = "decision_audit"
	RoutingOperationSubjectRoutingPool     = "routing_pool"
	RoutingOperationSubjectMemberBreaker   = "member_breaker"
	RoutingOperationSubjectEndpointBreaker = "endpoint_breaker"

	RoutingOperationStatusPending    RoutingOperationStatus = "pending"
	RoutingOperationStatusRunning    RoutingOperationStatus = "running"
	RoutingOperationStatusSucceeded  RoutingOperationStatus = "succeeded"
	RoutingOperationStatusFailed     RoutingOperationStatus = "failed"
	RoutingOperationStatusSuperseded RoutingOperationStatus = "superseded"

	routingOperationSchemaVersion         = 1
	routingOperationPolicySchemaVersion   = 2
	routingOperationControlSchemaVersion  = 3
	routingOperationReasonMaxRunes        = 512
	routingOperationErrorMaxRunes         = 4_096
	routingOperationResultMaxBytes        = 60 << 10
	routingOperationMaxClaimMs            = int64(5 * 60 * 1_000)
	RoutingOperationMaxPageSize           = 100
	routingOperationRequestKeyUniqueIndex = "idx_routing_operation_request_key_unique"
)

var (
	ErrRoutingOperationInvalid             = errors.New("invalid routing operation")
	ErrRoutingOperationClaimLost           = errors.New("routing operation claim lost")
	ErrRoutingOperationCorrupt             = errors.New("channel routing operation is corrupt")
	ErrRoutingOperationIdempotencyConflict = errors.New("channel routing idempotency key conflicts with another request")
)

type RoutingOperationStatus string

type RoutingOperationSpec struct {
	Type                 string `json:"type"`
	EvaluationHash       string `json:"evaluation_hash"`
	SubjectType          string `json:"subject_type"`
	SubjectID            int64  `json:"subject_id"`
	PoolID               int    `json:"pool_id"`
	ExpectedRevision     int64  `json:"expected_revision"`
	ExpectedActivationID int64  `json:"expected_activation_id"`
	ActorID              int    `json:"actor_id"`
	Reason               string `json:"reason"`
	RequestKeyHash       string `json:"-"`
	RequestPayloadHash   string `json:"-"`
}

type RoutingOperationRequestIdentity struct {
	KeyHash     string
	PayloadHash string
}

type RoutingOperationResult struct {
	Revision     int64  `json:"revision"`
	ActivationID int64  `json:"activation_id"`
	OutboxID     int64  `json:"outbox_id"`
	PayloadJSON  string `json:"-"`
	PayloadHash  string `json:"payload_hash,omitempty"`
}

type RoutingOperation struct {
	ID                   int64                  `json:"id" gorm:"primaryKey"`
	OperationType        string                 `json:"type" gorm:"column:operation_type;type:varchar(64);index;not null"`
	IdempotencyHash      string                 `json:"idempotency_hash" gorm:"type:varchar(64);uniqueIndex;not null"`
	RequestKeyHash       *string                `json:"-" gorm:"type:varchar(64)"`
	RequestPayloadHash   string                 `json:"-" gorm:"type:varchar(64)"`
	SystemTaskID         string                 `json:"system_task_id,omitempty" gorm:"type:varchar(64);index"`
	CreateToken          string                 `json:"-" gorm:"type:varchar(32);not null"`
	EvaluationHash       string                 `json:"evaluation_hash" gorm:"type:varchar(64);index;not null"`
	SubjectType          string                 `json:"subject_type" gorm:"type:varchar(32);index"`
	SubjectID            int64                  `json:"subject_id" gorm:"bigint;index"`
	PoolID               int                    `json:"pool_id" gorm:"index;not null"`
	ExpectedRevision     int64                  `json:"expected_revision" gorm:"bigint;index;not null"`
	ExpectedActivationID int64                  `json:"expected_activation_id" gorm:"bigint;index;not null"`
	ActorID              int                    `json:"actor_id" gorm:"index;not null"`
	Reason               string                 `json:"reason" gorm:"type:varchar(512);not null"`
	Status               RoutingOperationStatus `json:"status" gorm:"type:varchar(24);index;not null"`
	ClaimToken           string                 `json:"-" gorm:"type:varchar(32);index;not null"`
	ClaimUntilMs         int64                  `json:"claim_until_ms" gorm:"bigint;index;not null"`
	Attempts             int                    `json:"attempts" gorm:"not null"`
	NextRetryMs          int64                  `json:"next_retry_ms" gorm:"bigint;index;not null"`
	LastError            string                 `json:"last_error" gorm:"type:text;not null"`
	ResultRevision       int64                  `json:"result_revision" gorm:"bigint;index;not null"`
	ResultActivationID   int64                  `json:"result_activation_id" gorm:"bigint;index;not null"`
	ResultOutboxID       int64                  `json:"result_outbox_id" gorm:"bigint;index;not null"`
	ResultPayloadJSON    string                 `json:"-" gorm:"type:text"`
	ResultPayloadHash    string                 `json:"result_payload_hash" gorm:"type:varchar(64);index"`
	CreatedTimeMs        int64                  `json:"created_time_ms" gorm:"bigint;index;not null"`
	UpdatedTimeMs        int64                  `json:"updated_time_ms" gorm:"bigint;index;not null"`
	CompletedTimeMs      int64                  `json:"completed_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingOperation) TableName() string {
	return "routing_operations"
}

func (operation *RoutingOperation) AfterFind(*gorm.DB) error {
	normalizeRoutingOperationStorage(operation)
	return nil
}

func ensureRoutingOperationRequestKeyUniqueIndex(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasColumn(&RoutingOperation{}, "request_key_hash") ||
		db.Migrator().HasIndex(&RoutingOperation{}, routingOperationRequestKeyUniqueIndex) {
		return nil
	}
	return db.Exec(
		"CREATE UNIQUE INDEX " + routingOperationRequestKeyUniqueIndex + " ON routing_operations (request_key_hash)",
	).Error
}

func CreateRoutingOperationContext(
	ctx context.Context,
	spec RoutingOperationSpec,
) (RoutingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return createRoutingOperationDB(ctx, DB, spec)
}

func createRoutingOperationDB(
	ctx context.Context,
	db *gorm.DB,
	spec RoutingOperationSpec,
) (RoutingOperation, bool, error) {
	if db == nil {
		return RoutingOperation{}, false, ErrRoutingOperationInvalid
	}
	normalized, idempotencyHash, err := normalizeRoutingOperationSpec(spec)
	if err != nil {
		return RoutingOperation{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return RoutingOperation{}, false, err
	}
	nowMs, err := routingDatabaseNowMs(db.WithContext(ctx))
	if err != nil {
		return RoutingOperation{}, false, err
	}
	createToken, err := newRoutingPersistenceToken()
	if err != nil {
		return RoutingOperation{}, false, err
	}
	operation := RoutingOperation{
		OperationType:        normalized.Type,
		IdempotencyHash:      idempotencyHash,
		CreateToken:          createToken,
		EvaluationHash:       normalized.EvaluationHash,
		SubjectType:          normalized.SubjectType,
		SubjectID:            normalized.SubjectID,
		PoolID:               normalized.PoolID,
		ExpectedRevision:     normalized.ExpectedRevision,
		ExpectedActivationID: normalized.ExpectedActivationID,
		ActorID:              normalized.ActorID,
		Reason:               normalized.Reason,
		RequestKeyHash:       routingOperationRequestKeyPointer(normalized.RequestKeyHash),
		RequestPayloadHash:   normalized.RequestPayloadHash,
		Status:               RoutingOperationStatusPending,
		CreatedTimeMs:        nowMs,
		UpdatedTimeMs:        nowMs,
	}
	created := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&operation)
	if created.Error != nil {
		return RoutingOperation{}, false, created.Error
	}
	var stored RoutingOperation
	if err := db.WithContext(ctx).Where("idempotency_hash = ?", idempotencyHash).First(&stored).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) && normalized.RequestKeyHash != "" {
			if keyErr := db.WithContext(ctx).Where("request_key_hash = ?", normalized.RequestKeyHash).First(&stored).Error; keyErr == nil {
				return RoutingOperation{}, false, ErrRoutingOperationIdempotencyConflict
			}
		}
		return RoutingOperation{}, false, err
	}
	if stored.OperationType != normalized.Type || stored.EvaluationHash != normalized.EvaluationHash ||
		stored.SubjectType != normalized.SubjectType || stored.SubjectID != normalized.SubjectID ||
		stored.PoolID != normalized.PoolID || stored.ExpectedRevision != normalized.ExpectedRevision ||
		stored.ExpectedActivationID != normalized.ExpectedActivationID || stored.ActorID != normalized.ActorID ||
		stored.Reason != normalized.Reason || !routingOperationRequestIdentityMatches(stored, normalized) ||
		!validRoutingPersistenceToken(stored.CreateToken) {
		return RoutingOperation{}, false, ErrRoutingOperationInvalid
	}
	return stored, stored.CreateToken == createToken, nil
}

func CreateSucceededRoutingOperationContext(
	ctx context.Context,
	spec RoutingOperationSpec,
	result RoutingOperationResult,
	payload any,
) (RoutingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var stored RoutingOperation
	var created bool
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		stored, created, err = createSucceededRoutingOperationTx(ctx, tx, spec, result, payload, time.Now().UnixMilli())
		return err
	})
	return stored, created, err
}

func CreateFailedRoutingOperationContext(
	ctx context.Context,
	spec RoutingOperationSpec,
	operationErr error,
) (RoutingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if operationErr == nil {
		return RoutingOperation{}, false, ErrRoutingOperationInvalid
	}
	lastError := routingOperationErrorText(operationErr)
	if lastError == "" {
		return RoutingOperation{}, false, ErrRoutingOperationInvalid
	}
	var stored RoutingOperation
	var created bool
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		normalized, idempotencyHash, normalizeErr := normalizeRoutingOperationSpec(spec)
		if normalizeErr != nil {
			return normalizeErr
		}
		nowMs := time.Now().UnixMilli()
		createToken, tokenErr := newRoutingPersistenceToken()
		if tokenErr != nil {
			return tokenErr
		}
		operation := RoutingOperation{
			OperationType: normalized.Type, IdempotencyHash: idempotencyHash, CreateToken: createToken,
			EvaluationHash: normalized.EvaluationHash, SubjectType: normalized.SubjectType, SubjectID: normalized.SubjectID,
			PoolID: normalized.PoolID, ExpectedRevision: normalized.ExpectedRevision,
			ExpectedActivationID: normalized.ExpectedActivationID, ActorID: normalized.ActorID, Reason: normalized.Reason,
			RequestKeyHash: routingOperationRequestKeyPointer(normalized.RequestKeyHash), RequestPayloadHash: normalized.RequestPayloadHash,
			Status: RoutingOperationStatusFailed, Attempts: 1, LastError: lastError,
			CreatedTimeMs: nowMs, UpdatedTimeMs: nowMs, CompletedTimeMs: nowMs,
		}
		result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&operation)
		if result.Error != nil {
			return result.Error
		}
		if err := tx.WithContext(ctx).Where("idempotency_hash = ?", idempotencyHash).First(&stored).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) && normalized.RequestKeyHash != "" {
				if keyErr := tx.WithContext(ctx).Where("request_key_hash = ?", normalized.RequestKeyHash).First(&stored).Error; keyErr == nil {
					return ErrRoutingOperationIdempotencyConflict
				}
			}
			return err
		}
		if err := validateStoredRoutingOperation(stored); err != nil {
			return err
		}
		if stored.OperationType != normalized.Type || stored.EvaluationHash != normalized.EvaluationHash ||
			stored.SubjectType != normalized.SubjectType || stored.SubjectID != normalized.SubjectID ||
			stored.PoolID != normalized.PoolID || stored.ExpectedRevision != normalized.ExpectedRevision ||
			stored.ExpectedActivationID != normalized.ExpectedActivationID || stored.ActorID != normalized.ActorID ||
			stored.Reason != normalized.Reason || !routingOperationRequestIdentityMatches(stored, normalized) ||
			stored.Status != RoutingOperationStatusFailed || stored.LastError != lastError {
			return ErrRoutingOperationInvalid
		}
		created = stored.CreateToken == createToken
		return nil
	})
	return stored, created, err
}

func createSucceededRoutingOperationTx(
	ctx context.Context,
	tx *gorm.DB,
	spec RoutingOperationSpec,
	result RoutingOperationResult,
	payload any,
	nowMs int64,
) (RoutingOperation, bool, error) {
	if tx == nil || nowMs <= 0 {
		return RoutingOperation{}, false, ErrRoutingOperationInvalid
	}
	normalized, idempotencyHash, err := normalizeRoutingOperationSpec(spec)
	if err != nil {
		return RoutingOperation{}, false, err
	}
	result.PayloadJSON, result.PayloadHash, err = normalizeRoutingOperationResultPayload(payload)
	if err != nil || !validRoutingOperationTerminalState(RoutingOperationStatusSucceeded, "", result) {
		return RoutingOperation{}, false, ErrRoutingOperationInvalid
	}
	createToken, err := newRoutingPersistenceToken()
	if err != nil {
		return RoutingOperation{}, false, err
	}
	operation := RoutingOperation{
		OperationType: normalized.Type, IdempotencyHash: idempotencyHash, CreateToken: createToken,
		EvaluationHash: normalized.EvaluationHash, SubjectType: normalized.SubjectType, SubjectID: normalized.SubjectID,
		PoolID: normalized.PoolID, ExpectedRevision: normalized.ExpectedRevision,
		ExpectedActivationID: normalized.ExpectedActivationID, ActorID: normalized.ActorID, Reason: normalized.Reason,
		RequestKeyHash:     routingOperationRequestKeyPointer(normalized.RequestKeyHash),
		RequestPayloadHash: normalized.RequestPayloadHash,
		Status:             RoutingOperationStatusSucceeded, Attempts: 1,
		ResultRevision: result.Revision, ResultActivationID: result.ActivationID, ResultOutboxID: result.OutboxID,
		ResultPayloadJSON: result.PayloadJSON, ResultPayloadHash: result.PayloadHash,
		CreatedTimeMs: nowMs, UpdatedTimeMs: nowMs, CompletedTimeMs: nowMs,
	}
	create := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&operation)
	if create.Error != nil {
		return RoutingOperation{}, false, create.Error
	}
	var stored RoutingOperation
	if err := tx.WithContext(ctx).Where("idempotency_hash = ?", idempotencyHash).First(&stored).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) && normalized.RequestKeyHash != "" {
			if keyErr := tx.WithContext(ctx).Where("request_key_hash = ?", normalized.RequestKeyHash).First(&stored).Error; keyErr == nil {
				return RoutingOperation{}, false, ErrRoutingOperationIdempotencyConflict
			}
		}
		return RoutingOperation{}, false, err
	}
	if err := validateStoredRoutingOperation(stored); err != nil {
		return RoutingOperation{}, false, err
	}
	if stored.OperationType != normalized.Type || stored.EvaluationHash != normalized.EvaluationHash ||
		stored.SubjectType != normalized.SubjectType || stored.SubjectID != normalized.SubjectID ||
		stored.PoolID != normalized.PoolID || stored.ExpectedRevision != normalized.ExpectedRevision ||
		stored.ExpectedActivationID != normalized.ExpectedActivationID || stored.ActorID != normalized.ActorID ||
		stored.Reason != normalized.Reason || !routingOperationRequestIdentityMatches(stored, normalized) ||
		stored.Status != RoutingOperationStatusSucceeded ||
		stored.ResultRevision != result.Revision || stored.ResultActivationID != result.ActivationID ||
		stored.ResultOutboxID != result.OutboxID || stored.ResultPayloadHash != result.PayloadHash ||
		stored.ResultPayloadJSON != result.PayloadJSON {
		return RoutingOperation{}, false, ErrRoutingOperationInvalid
	}
	return stored, stored.CreateToken == createToken, nil
}

type RoutingOperationFilter struct {
	OperationType string
	Status        RoutingOperationStatus
	BeforeID      int64
	Limit         int
}

func GetRoutingOperationContext(ctx context.Context, id int64) (RoutingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 {
		return RoutingOperation{}, ErrRoutingOperationInvalid
	}
	var operation RoutingOperation
	if err := DB.WithContext(ctx).Where("id = ?", id).First(&operation).Error; err != nil {
		return RoutingOperation{}, err
	}
	if err := validateStoredRoutingOperation(operation); err != nil {
		return RoutingOperation{}, err
	}
	return operation, nil
}

func GetRoutingOperationByRequestIdentityContext(
	ctx context.Context,
	identity RoutingOperationRequestIdentity,
) (RoutingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingOperationRequestIdentity(identity) {
		return RoutingOperation{}, ErrRoutingOperationInvalid
	}
	operation, found, err := getRoutingOperationByRequestIdentityDB(ctx, DB, identity)
	if err != nil {
		return RoutingOperation{}, err
	}
	if !found {
		return RoutingOperation{}, gorm.ErrRecordNotFound
	}
	return operation, nil
}

func getRoutingOperationByRequestIdentityDB(
	ctx context.Context,
	db *gorm.DB,
	identity RoutingOperationRequestIdentity,
) (RoutingOperation, bool, error) {
	if db == nil || !validRoutingOperationRequestIdentity(identity) {
		return RoutingOperation{}, false, ErrRoutingOperationInvalid
	}
	identity.KeyHash = strings.ToLower(strings.TrimSpace(identity.KeyHash))
	identity.PayloadHash = strings.ToLower(strings.TrimSpace(identity.PayloadHash))
	var operation RoutingOperation
	err := db.WithContext(ctx).Where("request_key_hash = ?", identity.KeyHash).First(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RoutingOperation{}, false, nil
	}
	if err != nil {
		return RoutingOperation{}, false, err
	}
	if operation.RequestPayloadHash != identity.PayloadHash {
		return RoutingOperation{}, true, ErrRoutingOperationIdempotencyConflict
	}
	if err := validateStoredRoutingOperation(operation); err != nil {
		return RoutingOperation{}, true, err
	}
	return operation, true, nil
}

func ListRoutingOperationsContext(
	ctx context.Context,
	filter RoutingOperationFilter,
) ([]RoutingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if filter.BeforeID < 0 || filter.Limit < 1 || filter.Limit > RoutingOperationMaxPageSize ||
		(filter.OperationType != "" && !validRoutingOperationType(filter.OperationType)) ||
		(filter.Status != "" && !validRoutingOperationStatus(filter.Status)) {
		return nil, false, ErrRoutingOperationInvalid
	}
	query := DB.WithContext(ctx).Model(&RoutingOperation{}).
		Omit("result_payload_json").
		Order("id desc").Limit(filter.Limit + 1)
	if filter.OperationType != "" {
		query = query.Where("operation_type = ?", filter.OperationType)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.BeforeID > 0 {
		query = query.Where("id < ?", filter.BeforeID)
	}
	var operations []RoutingOperation
	if err := query.Find(&operations).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(operations) > filter.Limit
	if hasMore {
		operations = operations[:filter.Limit]
	}
	for index := range operations {
		if err := validateStoredRoutingOperationSummary(operations[index]); err != nil {
			return nil, false, err
		}
	}
	return operations, hasMore, nil
}

func DeleteCompletedRoutingOperationsBeforeContext(
	ctx context.Context,
	cutoffMs int64,
	nowMs int64,
	limit int,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoffMs <= 0 || nowMs <= 0 || limit < 1 || limit > 500 {
		return 0, ErrRoutingOperationInvalid
	}
	var operations []RoutingOperation
	if err := DB.WithContext(ctx).Select("id").
		Where("status IN ? AND completed_time_ms > 0 AND completed_time_ms < ?", []RoutingOperationStatus{
			RoutingOperationStatusSucceeded, RoutingOperationStatusFailed, RoutingOperationStatusSuperseded,
		}, cutoffMs).
		Where("NOT EXISTS (SELECT 1 FROM routing_audit_exports WHERE routing_audit_exports.operation_id = routing_operations.id AND routing_audit_exports.expires_time_ms > ?)", nowMs).
		Order("completed_time_ms asc").Limit(limit).Find(&operations).Error; err != nil {
		return 0, err
	}
	if len(operations) == 0 {
		return 0, nil
	}
	ids := make([]int64, len(operations))
	for index := range operations {
		ids[index] = operations[index].ID
	}
	deleted := DB.WithContext(ctx).Where("id IN ? AND status IN ?", ids, []RoutingOperationStatus{
		RoutingOperationStatusSucceeded, RoutingOperationStatusFailed, RoutingOperationStatusSuperseded,
	}).Delete(&RoutingOperation{})
	return deleted.RowsAffected, deleted.Error
}

func (operation RoutingOperation) ResultPayload() ([]byte, error) {
	normalizeRoutingOperationStorage(&operation)
	if !validRoutingOperationResultPayload(operation.ResultPayloadJSON, operation.ResultPayloadHash) {
		return nil, ErrRoutingOperationCorrupt
	}
	return append([]byte(nil), operation.ResultPayloadJSON...), nil
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

func RenewRoutingOperationClaimContext(
	ctx context.Context,
	id int64,
	claimToken string,
	nowMs int64,
	leaseMs int64,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || !validRoutingPersistenceToken(claimToken) || nowMs <= 0 || leaseMs <= 0 ||
		leaseMs > routingOperationMaxClaimMs || nowMs > math.MaxInt64-leaseMs {
		return ErrRoutingOperationInvalid
	}
	updated := DB.WithContext(ctx).Model(&RoutingOperation{}).
		Where("id = ? AND status = ? AND claim_token = ? AND claim_until_ms > ?",
			id, RoutingOperationStatusRunning, claimToken, nowMs,
		).
		Updates(map[string]any{
			"claim_until_ms":  nowMs + leaseMs,
			"updated_time_ms": nowMs,
		})
	return routingOperationCASResult(updated)
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
			"result_payload_json":  "",
			"result_payload_hash":  "",
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

func SucceedRoutingOperationWithPayloadContext(
	ctx context.Context,
	id int64,
	claimToken string,
	nowMs int64,
	payload any,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	payloadJSON, payloadHash, err := normalizeRoutingOperationResultPayload(payload)
	if err != nil || payloadJSON == "" {
		return ErrRoutingOperationInvalid
	}
	return finishRoutingOperationContext(
		ctx,
		id,
		claimToken,
		nowMs,
		RoutingOperationStatusSucceeded,
		"",
		RoutingOperationResult{PayloadJSON: payloadJSON, PayloadHash: payloadHash},
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
		if lastError != "" || !validRoutingOperationResultPayload(result.PayloadJSON, result.PayloadHash) {
			return false
		}
		hasRevisionResult := result.Revision > 0 || result.ActivationID > 0 || result.OutboxID > 0
		if hasRevisionResult && (result.Revision <= 0 || result.ActivationID <= 0 || result.OutboxID <= 0) {
			return false
		}
		return hasRevisionResult || result.PayloadJSON != ""
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
		"result_payload_json":  result.PayloadJSON,
		"result_payload_hash":  result.PayloadHash,
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
	spec.SubjectType = strings.TrimSpace(spec.SubjectType)
	spec.Reason = strings.TrimSpace(spec.Reason)
	spec.RequestKeyHash = strings.ToLower(strings.TrimSpace(spec.RequestKeyHash))
	spec.RequestPayloadHash = strings.ToLower(strings.TrimSpace(spec.RequestPayloadHash))
	if !validRoutingOperationType(spec.Type) || !validRoutingHash(spec.EvaluationHash) || spec.ActorID < 0 ||
		!utf8.ValidString(spec.Reason) || utf8.RuneCountInString(spec.Reason) > routingOperationReasonMaxRunes ||
		((spec.RequestKeyHash == "") != (spec.RequestPayloadHash == "")) ||
		(spec.RequestKeyHash != "" && (!validRoutingHash(spec.RequestKeyHash) || !validRoutingHash(spec.RequestPayloadHash))) {
		return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
	}
	switch spec.Type {
	case RoutingOperationTypeCanaryAutoRollback:
		if spec.SubjectType != "" || spec.SubjectID != 0 || spec.PoolID <= 0 ||
			spec.ExpectedRevision <= 0 || spec.ExpectedActivationID <= 0 {
			return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
		}
	case RoutingOperationTypePolicySimulation:
		if spec.SubjectType != RoutingOperationSubjectPolicyDraft || spec.SubjectID <= 0 || spec.PoolID <= 0 ||
			spec.ExpectedRevision < 0 || spec.ExpectedActivationID < 0 {
			return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
		}
	case RoutingOperationTypeHistoricalSimulation:
		if spec.SubjectType != RoutingOperationSubjectRoutingPool || spec.SubjectID <= 0 ||
			spec.PoolID <= 0 || spec.SubjectID != int64(spec.PoolID) ||
			spec.ExpectedRevision < 0 || spec.ExpectedActivationID < 0 {
			return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
		}
	case RoutingOperationTypePolicyPublish:
		if spec.SubjectType != RoutingOperationSubjectPolicyDraft || spec.SubjectID <= 0 || spec.PoolID != 0 ||
			spec.ExpectedRevision < 0 || spec.ExpectedActivationID < 0 {
			return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
		}
	case RoutingOperationTypePolicyRollback:
		if spec.SubjectType != RoutingOperationSubjectPolicyRevision || spec.SubjectID <= 0 || spec.PoolID != 0 ||
			spec.ExpectedRevision <= 0 || spec.SubjectID >= spec.ExpectedRevision || spec.ExpectedActivationID < 0 {
			return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
		}
	case RoutingOperationTypeCostSync:
		if spec.SubjectType != RoutingOperationSubjectRoutingCosts || spec.SubjectID != 0 || spec.PoolID != 0 ||
			spec.ExpectedRevision < 0 || spec.ExpectedActivationID < 0 {
			return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
		}
	case RoutingOperationTypeActiveProbe:
		if spec.SubjectType != RoutingOperationSubjectRoutingProbes || spec.SubjectID != 0 || spec.PoolID != 0 ||
			spec.ExpectedRevision < 0 || spec.ExpectedActivationID < 0 {
			return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
		}
	case RoutingOperationTypeAuditExport:
		if spec.SubjectType != RoutingOperationSubjectDecisionAudit || spec.SubjectID != 0 || spec.PoolID != 0 ||
			spec.ExpectedRevision < 0 || spec.ExpectedActivationID < 0 {
			return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
		}
	case RoutingOperationTypeBreakerReset:
		memberTarget := spec.SubjectType == RoutingOperationSubjectMemberBreaker && spec.SubjectID > 0 && spec.PoolID > 0
		endpointTarget := spec.SubjectType == RoutingOperationSubjectEndpointBreaker && spec.SubjectID == 0 && spec.PoolID == 0
		if (!memberTarget && !endpointTarget) || spec.ExpectedRevision < 0 || spec.ExpectedActivationID < 0 ||
			(memberTarget && spec.ExpectedRevision == 0) {
			return RoutingOperationSpec{}, "", ErrRoutingOperationInvalid
		}
	}
	var canonical []byte
	var err error
	if spec.Type == RoutingOperationTypeCanaryAutoRollback {
		canonical, err = common.Marshal(struct {
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
	} else {
		schemaVersion := routingOperationPolicySchemaVersion
		if spec.Type == RoutingOperationTypeCostSync || spec.Type == RoutingOperationTypeActiveProbe ||
			spec.Type == RoutingOperationTypeAuditExport || spec.Type == RoutingOperationTypeBreakerReset {
			schemaVersion = routingOperationControlSchemaVersion
		}
		canonical, err = common.Marshal(struct {
			SchemaVersion        int    `json:"schema_version"`
			Type                 string `json:"type"`
			EvaluationHash       string `json:"evaluation_hash"`
			RequestKeyHash       string `json:"request_key_hash"`
			SubjectType          string `json:"subject_type"`
			SubjectID            int64  `json:"subject_id"`
			PoolID               int    `json:"pool_id"`
			ExpectedRevision     int64  `json:"expected_revision"`
			ExpectedActivationID int64  `json:"expected_activation_id"`
		}{
			SchemaVersion:        schemaVersion,
			Type:                 spec.Type,
			EvaluationHash:       spec.EvaluationHash,
			RequestKeyHash:       spec.RequestKeyHash,
			SubjectType:          spec.SubjectType,
			SubjectID:            spec.SubjectID,
			PoolID:               spec.PoolID,
			ExpectedRevision:     spec.ExpectedRevision,
			ExpectedActivationID: spec.ExpectedActivationID,
		})
	}
	if err != nil {
		return RoutingOperationSpec{}, "", err
	}
	return spec, routingPolicyHash(canonical), nil
}

func validRoutingOperationType(operationType string) bool {
	switch operationType {
	case RoutingOperationTypeCanaryAutoRollback,
		RoutingOperationTypePolicySimulation,
		RoutingOperationTypeHistoricalSimulation,
		RoutingOperationTypePolicyPublish,
		RoutingOperationTypePolicyRollback,
		RoutingOperationTypeCostSync,
		RoutingOperationTypeActiveProbe,
		RoutingOperationTypeAuditExport,
		RoutingOperationTypeBreakerReset:
		return true
	default:
		return false
	}
}

func validRoutingOperationStatus(status RoutingOperationStatus) bool {
	switch status {
	case RoutingOperationStatusPending, RoutingOperationStatusRunning, RoutingOperationStatusSucceeded,
		RoutingOperationStatusFailed, RoutingOperationStatusSuperseded:
		return true
	default:
		return false
	}
}

func validateStoredRoutingOperationBase(operation *RoutingOperation) error {
	if operation == nil {
		return ErrRoutingOperationCorrupt
	}
	normalizeRoutingOperationStorage(operation)
	spec := RoutingOperationSpec{
		Type: operation.OperationType, EvaluationHash: operation.EvaluationHash,
		SubjectType: operation.SubjectType, SubjectID: operation.SubjectID, PoolID: operation.PoolID,
		ExpectedRevision: operation.ExpectedRevision, ExpectedActivationID: operation.ExpectedActivationID,
		ActorID: operation.ActorID, Reason: operation.Reason,
		RequestPayloadHash: operation.RequestPayloadHash,
	}
	if operation.RequestKeyHash != nil {
		spec.RequestKeyHash = *operation.RequestKeyHash
	}
	_, idempotencyHash, err := normalizeRoutingOperationSpec(spec)
	if err != nil || operation.ID <= 0 || operation.IdempotencyHash != idempotencyHash ||
		!routingOperationRequestIdentityMatches(*operation, spec) ||
		!validRoutingPersistenceToken(operation.CreateToken) || !validRoutingOperationStatus(operation.Status) ||
		(operation.SystemTaskID != "" &&
			(operation.OperationType != RoutingOperationTypeCostSync || !validSystemTaskID(operation.SystemTaskID))) ||
		operation.Attempts < 0 || operation.NextRetryMs < 0 || operation.CreatedTimeMs <= 0 ||
		operation.UpdatedTimeMs < operation.CreatedTimeMs || operation.CompletedTimeMs < 0 ||
		!utf8.ValidString(operation.LastError) || utf8.RuneCountInString(operation.LastError) > routingOperationErrorMaxRunes {
		return ErrRoutingOperationCorrupt
	}
	return nil
}

func validateStoredRoutingOperation(operation RoutingOperation) error {
	if err := validateStoredRoutingOperationBase(&operation); err != nil {
		return err
	}
	result := RoutingOperationResult{
		Revision: operation.ResultRevision, ActivationID: operation.ResultActivationID, OutboxID: operation.ResultOutboxID,
		PayloadJSON: operation.ResultPayloadJSON, PayloadHash: operation.ResultPayloadHash,
	}
	switch operation.Status {
	case RoutingOperationStatusPending:
		if operation.ClaimToken != "" || operation.ClaimUntilMs != 0 || operation.CompletedTimeMs != 0 ||
			result != (RoutingOperationResult{}) ||
			(operation.Attempts == 0 && (operation.LastError != "" || operation.NextRetryMs != 0)) ||
			(operation.Attempts > 0 && (operation.LastError == "" || operation.NextRetryMs < operation.UpdatedTimeMs)) {
			return ErrRoutingOperationCorrupt
		}
	case RoutingOperationStatusRunning:
		if !validRoutingPersistenceToken(operation.ClaimToken) || operation.ClaimUntilMs <= operation.UpdatedTimeMs ||
			operation.Attempts <= 0 || operation.CompletedTimeMs != 0 || result != (RoutingOperationResult{}) {
			return ErrRoutingOperationCorrupt
		}
	case RoutingOperationStatusSucceeded:
		if operation.ClaimToken != "" || operation.ClaimUntilMs != 0 || operation.NextRetryMs != 0 ||
			operation.Attempts <= 0 || operation.CompletedTimeMs < operation.CreatedTimeMs ||
			!validRoutingOperationTerminalState(operation.Status, operation.LastError, result) {
			return ErrRoutingOperationCorrupt
		}
	case RoutingOperationStatusFailed, RoutingOperationStatusSuperseded:
		if operation.ClaimToken != "" || operation.ClaimUntilMs != 0 || operation.NextRetryMs != 0 ||
			operation.Attempts <= 0 || operation.CompletedTimeMs < operation.CreatedTimeMs ||
			!validRoutingOperationTerminalState(operation.Status, operation.LastError, result) {
			return ErrRoutingOperationCorrupt
		}
	}
	return nil
}

func validateStoredRoutingOperationSummary(operation RoutingOperation) error {
	if err := validateStoredRoutingOperationBase(&operation); err != nil {
		return err
	}
	hasRevisionResult := operation.ResultRevision > 0 || operation.ResultActivationID > 0 || operation.ResultOutboxID > 0
	if hasRevisionResult &&
		(operation.ResultRevision <= 0 || operation.ResultActivationID <= 0 || operation.ResultOutboxID <= 0) {
		return ErrRoutingOperationCorrupt
	}
	hasPayloadResult := operation.ResultPayloadHash != ""
	if hasPayloadResult && !validRoutingHash(operation.ResultPayloadHash) {
		return ErrRoutingOperationCorrupt
	}
	switch operation.Status {
	case RoutingOperationStatusPending:
		if operation.ClaimToken != "" || operation.ClaimUntilMs != 0 || operation.CompletedTimeMs != 0 ||
			hasRevisionResult || hasPayloadResult ||
			(operation.Attempts == 0 && (operation.LastError != "" || operation.NextRetryMs != 0)) ||
			(operation.Attempts > 0 && (operation.LastError == "" || operation.NextRetryMs < operation.UpdatedTimeMs)) {
			return ErrRoutingOperationCorrupt
		}
	case RoutingOperationStatusRunning:
		if !validRoutingPersistenceToken(operation.ClaimToken) || operation.ClaimUntilMs <= operation.UpdatedTimeMs ||
			operation.Attempts <= 0 || operation.CompletedTimeMs != 0 || hasRevisionResult || hasPayloadResult {
			return ErrRoutingOperationCorrupt
		}
	case RoutingOperationStatusSucceeded:
		if operation.ClaimToken != "" || operation.ClaimUntilMs != 0 || operation.NextRetryMs != 0 ||
			operation.Attempts <= 0 || operation.CompletedTimeMs < operation.CreatedTimeMs ||
			operation.LastError != "" || (!hasRevisionResult && !hasPayloadResult) {
			return ErrRoutingOperationCorrupt
		}
	case RoutingOperationStatusFailed, RoutingOperationStatusSuperseded:
		if operation.ClaimToken != "" || operation.ClaimUntilMs != 0 || operation.NextRetryMs != 0 ||
			operation.Attempts <= 0 || operation.CompletedTimeMs < operation.CreatedTimeMs ||
			operation.LastError == "" || hasRevisionResult || hasPayloadResult {
			return ErrRoutingOperationCorrupt
		}
	}
	return nil
}

func validRoutingOperationRequestIdentity(identity RoutingOperationRequestIdentity) bool {
	return validRoutingHash(strings.ToLower(strings.TrimSpace(identity.KeyHash))) &&
		validRoutingHash(strings.ToLower(strings.TrimSpace(identity.PayloadHash)))
}

func routingOperationRequestKeyPointer(value string) *string {
	if value == "" {
		return nil
	}
	copyValue := value
	return &copyValue
}

func routingOperationRequestIdentityMatches(operation RoutingOperation, spec RoutingOperationSpec) bool {
	if spec.RequestKeyHash == "" {
		return operation.RequestKeyHash == nil && operation.RequestPayloadHash == ""
	}
	return operation.RequestKeyHash != nil && *operation.RequestKeyHash == spec.RequestKeyHash &&
		operation.RequestPayloadHash == spec.RequestPayloadHash
}

func normalizeRoutingOperationResultPayload(payload any) (string, string, error) {
	if payload == nil {
		return "", "", nil
	}
	encoded, err := common.Marshal(payload)
	if err != nil || len(encoded) == 0 || len(encoded) > routingOperationResultMaxBytes || string(encoded) == "null" {
		return "", "", ErrRoutingOperationInvalid
	}
	return string(encoded), routingPolicyHash(encoded), nil
}

func validRoutingOperationResultPayload(payloadJSON string, payloadHash string) bool {
	payloadHash = strings.TrimSpace(payloadHash)
	if payloadJSON == "" || payloadHash == "" {
		return payloadJSON == "" && payloadHash == ""
	}
	if len(payloadJSON) > routingOperationResultMaxBytes || !validRoutingHash(payloadHash) ||
		routingPolicyHash([]byte(payloadJSON)) != payloadHash {
		return false
	}
	var payload any
	return common.UnmarshalJsonStr(payloadJSON, &payload) == nil && payload != nil
}

func validRoutingHash(value string) bool {
	value = strings.TrimSpace(value)
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
	value = strings.TrimSpace(value)
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func normalizeRoutingOperationStorage(operation *RoutingOperation) {
	if operation == nil {
		return
	}
	operation.IdempotencyHash = strings.TrimSpace(operation.IdempotencyHash)
	operation.RequestPayloadHash = strings.TrimSpace(operation.RequestPayloadHash)
	operation.CreateToken = strings.TrimSpace(operation.CreateToken)
	operation.EvaluationHash = strings.TrimSpace(operation.EvaluationHash)
	operation.ClaimToken = strings.TrimSpace(operation.ClaimToken)
	operation.ResultPayloadHash = strings.TrimSpace(operation.ResultPayloadHash)
	if operation.RequestKeyHash != nil {
		trimmed := strings.TrimSpace(*operation.RequestKeyHash)
		if trimmed == "" {
			operation.RequestKeyHash = nil
		} else {
			operation.RequestKeyHash = &trimmed
		}
	}
}

func routingOperationErrorText(operationErr error) string {
	if operationErr == nil {
		return ""
	}
	return routingOperationText(common.SanitizeErrorMessage(operationErr.Error()), routingOperationErrorMaxRunes)
}

func routingOperationText(value string, maxRunes int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "?"))
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	return value
}
