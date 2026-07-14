package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoutingBreakerResetScopeMember   = "member"
	RoutingBreakerResetScopeEndpoint = "endpoint"

	RoutingBreakerResetOutboxMaxPageSize = 500
	routingBreakerResetClaimMaxMs        = int64(5 * 60 * 1_000)
)

var (
	ErrRoutingBreakerResetInvalid    = errors.New("invalid channel routing breaker reset")
	ErrRoutingBreakerResetClaimLost  = errors.New("channel routing breaker reset outbox claim lost")
	ErrRoutingBreakerResetTargetGone = errors.New("channel routing breaker reset target no longer exists")
	ErrRoutingBreakerResetGeneration = errors.New("channel routing breaker reset generation is exhausted")
)

type RoutingBreakerResetTarget struct {
	Scope             string `json:"scope"`
	PoolID            int    `json:"pool_id,omitempty"`
	MemberID          int    `json:"member_id,omitempty"`
	ChannelID         int    `json:"channel_id,omitempty"`
	APIKeyIndex       int    `json:"api_key_index,omitempty"`
	ModelName         string `json:"model_name,omitempty"`
	GroupName         string `json:"group_name,omitempty"`
	EndpointHost      string `json:"endpoint_host,omitempty"`
	EndpointAuthority string `json:"endpoint_authority,omitempty"`
	Region            string `json:"region,omitempty"`
}

type RoutingBreakerResetCommand struct {
	ID                int64  `json:"id" gorm:"primaryKey"`
	OperationID       int64  `json:"operation_id" gorm:"uniqueIndex;not null"`
	TargetKey         string `json:"-" gorm:"type:varchar(64);index;not null"`
	LegacyBreakerID   int    `json:"-" gorm:"index"`
	LegacyGeneration  int64  `json:"-" gorm:"bigint;index"`
	Scope             string `json:"scope" gorm:"type:varchar(16);index;not null"`
	PoolID            int    `json:"pool_id" gorm:"index;not null"`
	MemberID          int    `json:"member_id" gorm:"index;not null"`
	ChannelID         int    `json:"channel_id" gorm:"index;not null"`
	APIKeyIndex       int    `json:"api_key_index" gorm:"not null"`
	ModelName         string `json:"model_name" gorm:"type:varchar(128);not null"`
	GroupName         string `json:"group_name" gorm:"type:varchar(64);not null"`
	EndpointHost      string `json:"endpoint_host" gorm:"type:varchar(255);not null"`
	EndpointAuthority string `json:"endpoint_authority" gorm:"type:varchar(320);not null"`
	Region            string `json:"region" gorm:"type:varchar(64);not null"`
	Generation        int64  `json:"generation" gorm:"bigint;not null"`
	TombstoneID       int64  `json:"tombstone_id" gorm:"bigint;index;not null"`
	OutboxID          int64  `json:"outbox_id" gorm:"bigint;index;not null"`
	CreatedTimeMs     int64  `json:"created_time_ms" gorm:"bigint;index;not null"`
	CompletedTimeMs   int64  `json:"completed_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingBreakerResetCommand) TableName() string {
	return "routing_breaker_reset_commands"
}

type RoutingBreakerResetTombstone struct {
	ID                int64  `json:"id" gorm:"primaryKey"`
	TargetKey         string `json:"-" gorm:"type:varchar(64);uniqueIndex;not null"`
	Scope             string `json:"scope" gorm:"type:varchar(16);index;not null"`
	PoolID            int    `json:"pool_id" gorm:"index;not null"`
	MemberID          int    `json:"member_id" gorm:"index;not null"`
	ChannelID         int    `json:"channel_id" gorm:"index;not null"`
	APIKeyIndex       int    `json:"api_key_index" gorm:"not null"`
	ModelName         string `json:"model_name" gorm:"type:varchar(128);not null"`
	GroupName         string `json:"group_name" gorm:"type:varchar(64);not null"`
	EndpointHost      string `json:"endpoint_host" gorm:"type:varchar(255);not null"`
	EndpointAuthority string `json:"endpoint_authority" gorm:"type:varchar(320);not null"`
	Region            string `json:"region" gorm:"type:varchar(64);not null"`
	Generation        int64  `json:"generation" gorm:"bigint;not null"`
	ResetAtMs         int64  `json:"reset_at_ms" gorm:"bigint;index;not null"`
	LastOperationID   int64  `json:"last_operation_id" gorm:"bigint;index;not null"`
	CreatedTimeMs     int64  `json:"created_time_ms" gorm:"bigint;not null"`
	UpdatedTimeMs     int64  `json:"updated_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingBreakerResetTombstone) TableName() string {
	return "routing_breaker_reset_tombstones"
}

type RoutingBreakerResetFence struct {
	TargetKey     string `json:"-" gorm:"type:varchar(64);primaryKey"`
	Generation    int64  `json:"generation" gorm:"bigint;not null"`
	CreatedTimeMs int64  `json:"created_time_ms" gorm:"bigint;not null"`
	UpdatedTimeMs int64  `json:"updated_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingBreakerResetFence) TableName() string {
	return "routing_breaker_reset_fences"
}

type RoutingBreakerResetEvent struct {
	SchemaVersion int                       `json:"schema_version"`
	OperationID   int64                     `json:"operation_id"`
	OutboxID      int64                     `json:"outbox_id"`
	Generation    int64                     `json:"generation"`
	ResetAtMs     int64                     `json:"reset_at_ms"`
	Target        RoutingBreakerResetTarget `json:"target"`
}

type RoutingBreakerResetOutbox struct {
	ID              int64  `json:"id" gorm:"primaryKey"`
	OperationID     int64  `json:"operation_id" gorm:"uniqueIndex;not null"`
	TargetKey       string `json:"-" gorm:"type:varchar(64);index;not null"`
	Generation      int64  `json:"generation" gorm:"bigint;index;not null"`
	PayloadJSON     string `json:"-" gorm:"type:text;not null"`
	PayloadHash     string `json:"payload_hash" gorm:"type:varchar(64);not null"`
	ClaimToken      string `json:"-" gorm:"type:varchar(32);index;not null"`
	ClaimUntilMs    int64  `json:"claim_until_ms" gorm:"bigint;index;not null"`
	Attempts        int    `json:"attempts" gorm:"not null"`
	NextAttemptMs   int64  `json:"next_attempt_ms" gorm:"bigint;index;not null"`
	LastError       string `json:"last_error" gorm:"type:text;not null"`
	CreatedTimeMs   int64  `json:"created_time_ms" gorm:"bigint;index;not null"`
	UpdatedTimeMs   int64  `json:"updated_time_ms" gorm:"bigint;not null"`
	PublishedTimeMs int64  `json:"published_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingBreakerResetOutbox) TableName() string {
	return "routing_breaker_reset_outbox"
}

type RoutingBreakerResetExecution struct {
	Operation RoutingOperation             `json:"operation"`
	Command   RoutingBreakerResetCommand   `json:"command"`
	Tombstone RoutingBreakerResetTombstone `json:"tombstone"`
	Outbox    RoutingBreakerResetOutbox    `json:"outbox"`
	Event     RoutingBreakerResetEvent     `json:"event"`
}

func RoutingBreakerResetSchemaReady() bool {
	return DB != nil && DB.Migrator().HasTable(&RoutingBreakerResetCommand{}) &&
		DB.Migrator().HasTable(&RoutingBreakerResetFence{}) &&
		DB.Migrator().HasTable(&RoutingBreakerResetTombstone{}) &&
		DB.Migrator().HasTable(&RoutingBreakerResetOutbox{}) &&
		DB.Migrator().HasColumn(&RoutingBreakerState{}, "reset_generation") &&
		DB.Migrator().HasColumn(&RoutingEndpointEvidence{}, "reset_generation") &&
		DB.Migrator().HasColumn(&RoutingEndpointSharedState{}, "reset_generation")
}

func CreateRoutingBreakerResetOperationContext(
	ctx context.Context,
	spec RoutingOperationSpec,
	target RoutingBreakerResetTarget,
) (RoutingOperation, bool, error) {
	return createRoutingBreakerResetOperationContext(ctx, spec, target, 0, 0)
}

func CreateLegacyRoutingBreakerResetOperationContext(
	ctx context.Context,
	spec RoutingOperationSpec,
	target RoutingBreakerResetTarget,
	legacyBreakerID int,
	legacyGeneration int64,
) (RoutingOperation, bool, error) {
	if legacyBreakerID <= 0 || legacyGeneration < 0 || target.Scope != RoutingBreakerResetScopeMember {
		return RoutingOperation{}, false, ErrRoutingBreakerResetInvalid
	}
	return createRoutingBreakerResetOperationContext(ctx, spec, target, legacyBreakerID, legacyGeneration)
}

func createRoutingBreakerResetOperationContext(
	ctx context.Context,
	spec RoutingOperationSpec,
	target RoutingBreakerResetTarget,
	legacyBreakerID int,
	legacyGeneration int64,
) (RoutingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalizedTarget, targetKey, err := normalizeRoutingBreakerResetTarget(target)
	if err != nil {
		return RoutingOperation{}, false, err
	}
	if spec.Type != RoutingOperationTypeBreakerReset ||
		(normalizedTarget.Scope == RoutingBreakerResetScopeMember &&
			(spec.SubjectType != RoutingOperationSubjectMemberBreaker || spec.SubjectID != int64(normalizedTarget.MemberID) || spec.PoolID != normalizedTarget.PoolID)) ||
		(normalizedTarget.Scope == RoutingBreakerResetScopeEndpoint &&
			(spec.SubjectType != RoutingOperationSubjectEndpointBreaker || spec.SubjectID != 0 || spec.PoolID != 0)) {
		return RoutingOperation{}, false, ErrRoutingBreakerResetInvalid
	}
	var operation RoutingOperation
	created := false
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if spec.RequestKeyHash != "" || spec.RequestPayloadHash != "" {
			existing, found, lookupErr := getRoutingOperationByRequestIdentityDB(ctx, tx, RoutingOperationRequestIdentity{
				KeyHash: spec.RequestKeyHash, PayloadHash: spec.RequestPayloadHash,
			})
			if lookupErr != nil {
				return lookupErr
			}
			if found {
				if existing.OperationType != RoutingOperationTypeBreakerReset {
					return ErrRoutingOperationIdempotencyConflict
				}
				operation = existing
				var stored RoutingBreakerResetCommand
				if err := tx.WithContext(ctx).Where("operation_id = ?", operation.ID).First(&stored).Error; err != nil {
					return err
				}
				expected := routingBreakerResetCommandFromTarget(
					operation.ID, targetKey, normalizedTarget, operation.CreatedTimeMs,
					legacyBreakerID, legacyGeneration,
				)
				if !routingBreakerResetCommandMatchesTarget(stored, expected) {
					return ErrRoutingOperationIdempotencyConflict
				}
				return nil
			}
		}
		var createErr error
		operation, created, createErr = createRoutingOperationDB(ctx, tx, spec)
		if createErr != nil {
			return createErr
		}
		command := routingBreakerResetCommandFromTarget(
			operation.ID, targetKey, normalizedTarget, operation.CreatedTimeMs,
			legacyBreakerID, legacyGeneration,
		)
		if created {
			return tx.WithContext(ctx).Create(&command).Error
		}
		var stored RoutingBreakerResetCommand
		if err := tx.WithContext(ctx).Where("operation_id = ?", operation.ID).First(&stored).Error; err != nil {
			return err
		}
		if !routingBreakerResetCommandMatchesTarget(stored, command) {
			return ErrRoutingOperationIdempotencyConflict
		}
		return nil
	})
	return operation, created, err
}

func GetLatestLegacyRoutingBreakerResetContext(
	ctx context.Context,
	legacyBreakerID int,
) (RoutingOperation, RoutingBreakerResetCommand, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if legacyBreakerID <= 0 {
		return RoutingOperation{}, RoutingBreakerResetCommand{}, ErrRoutingBreakerResetInvalid
	}
	var command RoutingBreakerResetCommand
	if err := DB.WithContext(ctx).Where("legacy_breaker_id = ?", legacyBreakerID).
		Order("id DESC").First(&command).Error; err != nil {
		return RoutingOperation{}, RoutingBreakerResetCommand{}, err
	}
	if !validRoutingBreakerResetCommand(command) || command.LegacyBreakerID != legacyBreakerID {
		return RoutingOperation{}, RoutingBreakerResetCommand{}, ErrRoutingBreakerResetInvalid
	}
	operation, err := GetRoutingOperationContext(ctx, command.OperationID)
	if err != nil {
		return RoutingOperation{}, RoutingBreakerResetCommand{}, err
	}
	if operation.OperationType != RoutingOperationTypeBreakerReset {
		return RoutingOperation{}, RoutingBreakerResetCommand{}, ErrRoutingBreakerResetInvalid
	}
	return operation, command, nil
}

func RoutingBreakerResetTargetKey(target RoutingBreakerResetTarget) (string, error) {
	_, targetKey, err := normalizeRoutingBreakerResetTarget(target)
	return targetKey, err
}

func GetRoutingBreakerResetCommandByOperationContext(ctx context.Context, operationID int64) (RoutingBreakerResetCommand, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if operationID <= 0 {
		return RoutingBreakerResetCommand{}, ErrRoutingBreakerResetInvalid
	}
	var command RoutingBreakerResetCommand
	if err := DB.WithContext(ctx).Where("operation_id = ?", operationID).First(&command).Error; err != nil {
		return RoutingBreakerResetCommand{}, err
	}
	if !validRoutingBreakerResetCommand(command) {
		return RoutingBreakerResetCommand{}, ErrRoutingBreakerResetInvalid
	}
	return command, nil
}

func ExecuteRoutingBreakerResetOperationContext(
	ctx context.Context,
	claimed RoutingOperation,
) (RoutingBreakerResetExecution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if claimed.OperationType != RoutingOperationTypeBreakerReset || claimed.Status != RoutingOperationStatusRunning ||
		!validRoutingPersistenceToken(claimed.ClaimToken) {
		return RoutingBreakerResetExecution{}, ErrRoutingBreakerResetInvalid
	}
	var execution RoutingBreakerResetExecution
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nowMs, err := routingErrorBudgetDatabaseNowMs(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		var operation RoutingOperation
		query := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", claimed.ID)
		if err := query.First(&operation).Error; err != nil {
			return err
		}
		if err := validateStoredRoutingOperation(operation); err != nil {
			return err
		}
		if operation.OperationType != RoutingOperationTypeBreakerReset || operation.Status != RoutingOperationStatusRunning ||
			operation.ClaimToken != claimed.ClaimToken || operation.ClaimUntilMs <= nowMs {
			return ErrRoutingOperationClaimLost
		}
		var command RoutingBreakerResetCommand
		if err := tx.WithContext(ctx).Where("operation_id = ?", operation.ID).First(&command).Error; err != nil {
			return err
		}
		if !validRoutingBreakerResetCommand(command) || command.CompletedTimeMs != 0 || command.Generation != 0 ||
			command.TombstoneID != 0 || command.OutboxID != 0 {
			return ErrRoutingBreakerResetInvalid
		}
		transitionTimeMs := max(
			nowMs,
			operation.CreatedTimeMs,
			operation.UpdatedTimeMs,
			command.CreatedTimeMs,
		)
		target := command.Target()
		if target.Scope == RoutingBreakerResetScopeMember {
			current, currentErr := routingBreakerResetMemberTargetCurrentTx(ctx, tx, operation, target)
			if currentErr != nil {
				return currentErr
			}
			if !current {
				updated := tx.WithContext(ctx).Model(&RoutingBreakerResetCommand{}).
					Where("id = ? AND operation_id = ? AND completed_time_ms = 0", command.ID, operation.ID).
					Update("completed_time_ms", transitionTimeMs)
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return ErrRoutingOperationClaimLost
				}
				command.CompletedTimeMs = transitionTimeMs
				if !validRoutingBreakerResetCommand(command) {
					return ErrRoutingBreakerResetInvalid
				}
				finished, finishErr := finishRoutingOperationTx(
					ctx, tx, operation, nowMs, transitionTimeMs, RoutingOperationStatusSuperseded,
					ErrRoutingBreakerResetTargetGone.Error(), RoutingOperationResult{},
				)
				if finishErr != nil {
					return finishErr
				}
				execution = RoutingBreakerResetExecution{Operation: finished, Command: command}
				return nil
			}
		}
		tombstone, err := incrementRoutingBreakerResetGenerationTx(
			ctx, tx, target, operation.ID, transitionTimeMs,
		)
		if err != nil {
			return err
		}
		transitionTimeMs = max(
			transitionTimeMs,
			tombstone.ResetAtMs,
			tombstone.CreatedTimeMs,
			tombstone.UpdatedTimeMs,
		)
		if target.Scope == RoutingBreakerResetScopeMember {
			result := tx.WithContext(ctx).Where(&RoutingBreakerState{
				ChannelID: target.ChannelID, APIKeyIndex: target.APIKeyIndex,
				ModelName: target.ModelName, Group: target.GroupName,
			}).Delete(&RoutingBreakerState{})
			if result.Error != nil {
				return result.Error
			}
		} else {
			authorityKey := routingEndpointHash(target.EndpointAuthority)
			regionKey := routingEndpointHash(target.Region)
			if err := tx.WithContext(ctx).Where(
				"endpoint_authority_key = ? AND region_key = ?", authorityKey, regionKey,
			).Delete(&RoutingEndpointEvidence{}).Error; err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Where(
				"endpoint_authority_key = ? AND region_key = ?", authorityKey, regionKey,
			).Delete(&RoutingEndpointSharedState{}).Error; err != nil {
				return err
			}
		}

		event := RoutingBreakerResetEvent{
			SchemaVersion: 1, OperationID: operation.ID, Generation: tombstone.Generation,
			ResetAtMs: tombstone.ResetAtMs, Target: target,
		}
		payload, err := common.Marshal(event)
		if err != nil {
			return err
		}
		outbox := RoutingBreakerResetOutbox{
			OperationID: operation.ID, TargetKey: command.TargetKey, Generation: tombstone.Generation,
			PayloadJSON: string(payload), PayloadHash: routingBreakerResetHash(payload),
			CreatedTimeMs: nowMs, UpdatedTimeMs: nowMs,
		}
		if err := tx.WithContext(ctx).Create(&outbox).Error; err != nil {
			return err
		}
		event.OutboxID = outbox.ID
		payload, err = common.Marshal(event)
		if err != nil {
			return err
		}
		outbox.PayloadJSON = string(payload)
		outbox.PayloadHash = routingBreakerResetHash(payload)
		if err := tx.WithContext(ctx).Model(&RoutingBreakerResetOutbox{}).Where("id = ?", outbox.ID).
			Updates(map[string]any{"payload_json": outbox.PayloadJSON, "payload_hash": outbox.PayloadHash}).Error; err != nil {
			return err
		}
		command.Generation = tombstone.Generation
		command.TombstoneID = tombstone.ID
		command.OutboxID = outbox.ID
		command.CompletedTimeMs = transitionTimeMs
		updated := tx.WithContext(ctx).Model(&RoutingBreakerResetCommand{}).
			Where("id = ? AND operation_id = ? AND completed_time_ms = 0", command.ID, operation.ID).
			Updates(map[string]any{
				"generation": command.Generation, "tombstone_id": command.TombstoneID,
				"outbox_id": command.OutboxID, "completed_time_ms": transitionTimeMs,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRoutingOperationClaimLost
		}
		if !validRoutingBreakerResetCommand(command) {
			return ErrRoutingBreakerResetInvalid
		}
		resultPayload := struct {
			Scope      string                    `json:"scope"`
			Generation int64                     `json:"generation"`
			OutboxID   int64                     `json:"outbox_id"`
			Target     RoutingBreakerResetTarget `json:"target"`
		}{Scope: target.Scope, Generation: tombstone.Generation, OutboxID: outbox.ID, Target: target}
		resultJSON, resultHash, err := normalizeRoutingOperationResultPayload(resultPayload)
		if err != nil {
			return err
		}
		finished, err := finishRoutingOperationTx(ctx, tx, operation, nowMs, transitionTimeMs, RoutingOperationStatusSucceeded, "", RoutingOperationResult{
			PayloadJSON: resultJSON, PayloadHash: resultHash,
		})
		if err != nil {
			return err
		}
		execution = RoutingBreakerResetExecution{
			Operation: finished, Command: command, Tombstone: tombstone, Outbox: outbox, Event: event,
		}
		return nil
	})
	return execution, err
}

func ClaimRoutingBreakerResetOutboxContext(
	ctx context.Context,
	nowMs int64,
	leaseMs int64,
) (*RoutingBreakerResetOutbox, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if nowMs <= 0 || leaseMs <= 0 || leaseMs > routingBreakerResetClaimMaxMs || nowMs > math.MaxInt64-leaseMs {
		return nil, ErrRoutingBreakerResetInvalid
	}
	claimToken, err := newRoutingPersistenceToken()
	if err != nil {
		return nil, err
	}
	var claimed RoutingBreakerResetOutbox
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		eligible := "published_time_ms = 0 AND next_attempt_ms <= ? AND (claim_token = '' OR claim_until_ms <= ?)"
		if err := lockForUpdate(tx.WithContext(ctx)).Where(eligible, nowMs, nowMs).Order("id ASC").First(&claimed).Error; err != nil {
			return err
		}
		if claimed.Attempts == int(^uint(0)>>1) {
			return ErrRoutingBreakerResetInvalid
		}
		updated := tx.WithContext(ctx).Model(&RoutingBreakerResetOutbox{}).
			Where("id = ? AND "+eligible, claimed.ID, nowMs, nowMs).
			Updates(map[string]any{
				"claim_token": claimToken, "claim_until_ms": nowMs + leaseMs,
				"attempts": claimed.Attempts + 1, "updated_time_ms": nowMs,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRoutingBreakerResetClaimLost
		}
		return tx.WithContext(ctx).Where("id = ? AND claim_token = ?", claimed.ID, claimToken).First(&claimed).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := claimed.DecodePayload(); err != nil {
		return nil, err
	}
	return &claimed, nil
}

func MarkRoutingBreakerResetOutboxPublishedContext(
	ctx context.Context,
	id int64,
	claimToken string,
	nowMs int64,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || !validRoutingPersistenceToken(claimToken) || nowMs <= 0 {
		return ErrRoutingBreakerResetInvalid
	}
	updated := DB.WithContext(ctx).Model(&RoutingBreakerResetOutbox{}).
		Where("id = ? AND published_time_ms = 0 AND claim_token = ? AND claim_until_ms > ?", id, claimToken, nowMs).
		Updates(map[string]any{
			"claim_token": "", "claim_until_ms": 0, "next_attempt_ms": 0, "last_error": "",
			"updated_time_ms": nowMs, "published_time_ms": nowMs,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrRoutingBreakerResetClaimLost
	}
	return nil
}

func ReleaseRoutingBreakerResetOutboxClaimContext(
	ctx context.Context,
	id int64,
	claimToken string,
	nowMs int64,
	nextAttemptMs int64,
	publishErr error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 || !validRoutingPersistenceToken(claimToken) || nowMs <= 0 || nextAttemptMs < nowMs || publishErr == nil {
		return ErrRoutingBreakerResetInvalid
	}
	updated := DB.WithContext(ctx).Model(&RoutingBreakerResetOutbox{}).
		Where("id = ? AND published_time_ms = 0 AND claim_token = ? AND claim_until_ms > ?", id, claimToken, nowMs).
		Updates(map[string]any{
			"claim_token": "", "claim_until_ms": 0, "next_attempt_ms": nextAttemptMs,
			"last_error": routingOperationErrorText(publishErr), "updated_time_ms": nowMs,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrRoutingBreakerResetClaimLost
	}
	return nil
}

func ListRoutingBreakerResetTombstonesPageContext(
	ctx context.Context,
	afterID int64,
	limit int,
) ([]RoutingBreakerResetTombstone, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if afterID < 0 || limit < 1 || limit > RoutingBreakerResetOutboxMaxPageSize {
		return nil, ErrRoutingBreakerResetInvalid
	}
	var rows []RoutingBreakerResetTombstone
	if err := DB.WithContext(ctx).Where("id > ?", afterID).Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	for index := range rows {
		if !validRoutingBreakerResetTombstone(rows[index]) {
			return nil, ErrRoutingBreakerResetInvalid
		}
	}
	return rows, nil
}

func ListRoutingBreakerResetOutboxAfterContext(
	ctx context.Context,
	afterID int64,
	limit int,
) ([]RoutingBreakerResetOutbox, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if afterID < 0 || limit < 1 || limit > RoutingBreakerResetOutboxMaxPageSize {
		return nil, ErrRoutingBreakerResetInvalid
	}
	var rows []RoutingBreakerResetOutbox
	if err := DB.WithContext(ctx).Where("id > ?", afterID).Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	for index := range rows {
		if _, err := rows[index].DecodePayload(); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func GetRoutingBreakerResetOutboxContext(ctx context.Context, id int64) (RoutingBreakerResetOutbox, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id <= 0 {
		return RoutingBreakerResetOutbox{}, ErrRoutingBreakerResetInvalid
	}
	var outbox RoutingBreakerResetOutbox
	if err := DB.WithContext(ctx).Where("id = ?", id).First(&outbox).Error; err != nil {
		return RoutingBreakerResetOutbox{}, err
	}
	if _, err := outbox.DecodePayload(); err != nil {
		return RoutingBreakerResetOutbox{}, err
	}
	return outbox, nil
}

func MaxRoutingBreakerResetOutboxIDContext(ctx context.Context) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var maximum int64
	err := DB.WithContext(ctx).Model(&RoutingBreakerResetOutbox{}).Select("COALESCE(MAX(id), 0)").Scan(&maximum).Error
	return maximum, err
}

func DeletePublishedRoutingBreakerResetOutboxBeforeContext(ctx context.Context, cutoffMs int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoffMs <= 0 {
		return 0, nil
	}
	result := DB.WithContext(ctx).Where("published_time_ms > 0 AND published_time_ms < ?", cutoffMs).
		Delete(&RoutingBreakerResetOutbox{})
	return result.RowsAffected, result.Error
}

func DeleteCompletedRoutingBreakerResetCommandsBeforeContext(
	ctx context.Context,
	cutoffMs int64,
	limit int,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cutoffMs <= 0 || limit < 1 || limit > 500 {
		return 0, ErrRoutingBreakerResetInvalid
	}
	var ids []int64
	pendingOutbox := DB.WithContext(ctx).Model(&RoutingBreakerResetOutbox{}).Select("operation_id")
	if err := DB.WithContext(ctx).Model(&RoutingBreakerResetCommand{}).Select("id").
		Where("completed_time_ms > 0 AND completed_time_ms < ?", cutoffMs).
		Where("operation_id NOT IN (?)", pendingOutbox).
		Order("completed_time_ms ASC").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := DB.WithContext(ctx).Where("id IN ? AND completed_time_ms > 0", ids).
		Delete(&RoutingBreakerResetCommand{})
	return result.RowsAffected, result.Error
}

func (command RoutingBreakerResetCommand) Target() RoutingBreakerResetTarget {
	return RoutingBreakerResetTarget{
		Scope: command.Scope, PoolID: command.PoolID, MemberID: command.MemberID,
		ChannelID: command.ChannelID, APIKeyIndex: command.APIKeyIndex,
		ModelName: command.ModelName, GroupName: command.GroupName,
		EndpointHost: command.EndpointHost, EndpointAuthority: command.EndpointAuthority, Region: command.Region,
	}
}

func (tombstone RoutingBreakerResetTombstone) Target() RoutingBreakerResetTarget {
	return RoutingBreakerResetTarget{
		Scope: tombstone.Scope, PoolID: tombstone.PoolID, MemberID: tombstone.MemberID,
		ChannelID: tombstone.ChannelID, APIKeyIndex: tombstone.APIKeyIndex,
		ModelName: tombstone.ModelName, GroupName: tombstone.GroupName,
		EndpointHost: tombstone.EndpointHost, EndpointAuthority: tombstone.EndpointAuthority, Region: tombstone.Region,
	}
}

func (outbox RoutingBreakerResetOutbox) DecodePayload() (RoutingBreakerResetEvent, error) {
	payload := []byte(outbox.PayloadJSON)
	if outbox.ID <= 0 || outbox.OperationID <= 0 || outbox.Generation <= 0 || len(payload) == 0 ||
		outbox.PayloadHash != routingBreakerResetHash(payload) {
		return RoutingBreakerResetEvent{}, ErrRoutingBreakerResetInvalid
	}
	var event RoutingBreakerResetEvent
	if err := common.Unmarshal(payload, &event); err != nil {
		return RoutingBreakerResetEvent{}, ErrRoutingBreakerResetInvalid
	}
	_, targetKey, err := normalizeRoutingBreakerResetTarget(event.Target)
	if err != nil || event.SchemaVersion != 1 || event.OperationID != outbox.OperationID || event.OutboxID != outbox.ID ||
		event.Generation != outbox.Generation || event.ResetAtMs <= 0 || targetKey != outbox.TargetKey {
		return RoutingBreakerResetEvent{}, ErrRoutingBreakerResetInvalid
	}
	return event, nil
}

func ValidateRoutingBreakerResetEvent(event RoutingBreakerResetEvent) (RoutingBreakerResetEvent, error) {
	normalized, _, err := normalizeRoutingBreakerResetTarget(event.Target)
	if err != nil || event.SchemaVersion != 1 || event.OperationID <= 0 || event.OutboxID <= 0 ||
		event.Generation <= 0 || event.ResetAtMs <= 0 {
		return RoutingBreakerResetEvent{}, ErrRoutingBreakerResetInvalid
	}
	event.Target = normalized
	return event, nil
}

func lockRoutingBreakerResetGenerationTx(
	ctx context.Context,
	tx *gorm.DB,
	target RoutingBreakerResetTarget,
	nowMs int64,
) (RoutingBreakerResetTombstone, error) {
	if tx == nil || nowMs <= 0 {
		return RoutingBreakerResetTombstone{}, ErrRoutingBreakerResetInvalid
	}
	_, targetKey, err := normalizeRoutingBreakerResetTarget(target)
	if err != nil {
		return RoutingBreakerResetTombstone{}, err
	}
	fence, err := lockRoutingBreakerResetFenceTx(ctx, tx, targetKey, nowMs)
	if err != nil {
		return RoutingBreakerResetTombstone{}, err
	}
	return RoutingBreakerResetTombstone{TargetKey: targetKey, Generation: fence.Generation}, nil
}

func routingBreakerResetMemberTargetCurrentTx(
	ctx context.Context,
	tx *gorm.DB,
	operation RoutingOperation,
	target RoutingBreakerResetTarget,
) (bool, error) {
	if tx == nil || operation.ExpectedRevision <= 0 || operation.ExpectedActivationID < 0 {
		return false, ErrRoutingBreakerResetInvalid
	}
	var head RoutingPolicyHead
	if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", routingPolicyHeadID).First(&head).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if head.CurrentRevision != operation.ExpectedRevision || head.CurrentActivationID != operation.ExpectedActivationID {
		return false, nil
	}
	var poolCount int64
	if err := tx.WithContext(ctx).Model(&RoutingPolicyPoolRevision{}).
		Where("revision = ? AND pool_id = ? AND group_name = ?", operation.ExpectedRevision, target.PoolID, target.GroupName).
		Count(&poolCount).Error; err != nil {
		return false, err
	}
	if poolCount != 1 {
		return false, nil
	}
	var memberCount int64
	if err := tx.WithContext(ctx).Model(&RoutingPolicyMemberRevision{}).
		Where("revision = ? AND pool_id = ? AND member_id = ? AND channel_id = ?",
			operation.ExpectedRevision, target.PoolID, target.MemberID, target.ChannelID,
		).
		Count(&memberCount).Error; err != nil {
		return false, err
	}
	return memberCount == 1, nil
}

func incrementRoutingBreakerResetGenerationTx(
	ctx context.Context,
	tx *gorm.DB,
	target RoutingBreakerResetTarget,
	operationID int64,
	nowMs int64,
) (RoutingBreakerResetTombstone, error) {
	normalized, targetKey, err := normalizeRoutingBreakerResetTarget(target)
	if err != nil {
		return RoutingBreakerResetTombstone{}, err
	}
	fence, err := lockRoutingBreakerResetFenceTx(ctx, tx, targetKey, nowMs)
	if err != nil {
		return RoutingBreakerResetTombstone{}, err
	}
	transitionTimeMs := max(nowMs, fence.CreatedTimeMs, fence.UpdatedTimeMs)
	var previous RoutingBreakerResetTombstone
	err = lockForUpdate(tx.WithContext(ctx)).Where("target_key = ?", targetKey).First(&previous).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return RoutingBreakerResetTombstone{}, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if fence.Generation != 0 {
			return RoutingBreakerResetTombstone{}, ErrRoutingBreakerResetInvalid
		}
	} else {
		if !validRoutingBreakerResetTombstone(previous) || previous.Generation != fence.Generation {
			return RoutingBreakerResetTombstone{}, ErrRoutingBreakerResetInvalid
		}
		transitionTimeMs = max(
			transitionTimeMs,
			previous.ResetAtMs,
			previous.CreatedTimeMs,
			previous.UpdatedTimeMs,
		)
	}
	if fence.Generation == math.MaxInt64 {
		return RoutingBreakerResetTombstone{}, ErrRoutingBreakerResetGeneration
	}
	updated := tx.WithContext(ctx).Model(&RoutingBreakerResetFence{}).
		Where("target_key = ? AND generation = ?", targetKey, fence.Generation).
		Updates(map[string]any{
			"generation": fence.Generation + 1, "updated_time_ms": transitionTimeMs,
		})
	if updated.Error != nil {
		return RoutingBreakerResetTombstone{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RoutingBreakerResetTombstone{}, ErrRoutingOperationClaimLost
	}
	tombstone := routingBreakerResetTombstoneFromTarget(targetKey, normalized, transitionTimeMs)
	tombstone.Generation = fence.Generation + 1
	tombstone.ResetAtMs = transitionTimeMs
	tombstone.LastOperationID = operationID
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "target_key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"scope", "pool_id", "member_id", "channel_id", "api_key_index", "model_name", "group_name",
			"endpoint_host", "endpoint_authority", "region", "generation", "reset_at_ms",
			"last_operation_id", "updated_time_ms",
		}),
	}).Create(&tombstone).Error; err != nil {
		return RoutingBreakerResetTombstone{}, err
	}
	if err := tx.WithContext(ctx).Where("target_key = ?", targetKey).First(&tombstone).Error; err != nil {
		return RoutingBreakerResetTombstone{}, err
	}
	if !validRoutingBreakerResetTombstone(tombstone) || tombstone.Generation != fence.Generation+1 ||
		tombstone.LastOperationID != operationID {
		return RoutingBreakerResetTombstone{}, ErrRoutingBreakerResetInvalid
	}
	return tombstone, nil
}

func lockRoutingBreakerResetFenceTx(
	ctx context.Context,
	tx *gorm.DB,
	targetKey string,
	nowMs int64,
) (RoutingBreakerResetFence, error) {
	if tx == nil || !validRoutingHash(targetKey) || nowMs <= 0 {
		return RoutingBreakerResetFence{}, ErrRoutingBreakerResetInvalid
	}
	candidate := RoutingBreakerResetFence{TargetKey: targetKey, CreatedTimeMs: nowMs, UpdatedTimeMs: nowMs}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "target_key"}}, DoNothing: true}).
		Create(&candidate).Error; err != nil {
		return RoutingBreakerResetFence{}, err
	}
	var stored RoutingBreakerResetFence
	if err := lockForUpdate(tx.WithContext(ctx)).Where("target_key = ?", targetKey).First(&stored).Error; err != nil {
		return RoutingBreakerResetFence{}, err
	}
	if stored.TargetKey != targetKey || stored.Generation < 0 || stored.CreatedTimeMs <= 0 ||
		stored.UpdatedTimeMs < stored.CreatedTimeMs {
		return RoutingBreakerResetFence{}, ErrRoutingBreakerResetInvalid
	}
	return stored, nil
}

func normalizeRoutingBreakerResetTarget(target RoutingBreakerResetTarget) (RoutingBreakerResetTarget, string, error) {
	target.Scope = strings.ToLower(strings.TrimSpace(target.Scope))
	target.ModelName = strings.TrimSpace(target.ModelName)
	target.GroupName = strings.TrimSpace(target.GroupName)
	target.EndpointHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(target.EndpointHost)), ".")
	target.EndpointAuthority = strings.ToLower(strings.TrimSpace(target.EndpointAuthority))
	target.Region = strings.ToLower(strings.TrimSpace(target.Region))
	validText := func(value string, maximum int, empty bool) bool {
		return utf8.ValidString(value) && (empty || value != "") && utf8.RuneCountInString(value) <= maximum
	}
	switch target.Scope {
	case RoutingBreakerResetScopeMember:
		if target.PoolID <= 0 || target.MemberID <= 0 || target.ChannelID <= 0 || target.APIKeyIndex != RoutingMetricSingleKeyIndex ||
			!validText(target.ModelName, 128, false) || !validText(target.GroupName, 64, false) ||
			target.EndpointHost != "" || target.EndpointAuthority != "" || target.Region != "" {
			return RoutingBreakerResetTarget{}, "", ErrRoutingBreakerResetInvalid
		}
	case RoutingBreakerResetScopeEndpoint:
		if target.PoolID != 0 || target.MemberID != 0 || target.ChannelID != 0 || target.APIKeyIndex != 0 ||
			target.ModelName != "" || target.GroupName != "" ||
			!validText(target.EndpointHost, 255, false) || !validText(target.EndpointAuthority, 320, false) ||
			!validText(target.Region, 64, false) {
			return RoutingBreakerResetTarget{}, "", ErrRoutingBreakerResetInvalid
		}
	default:
		return RoutingBreakerResetTarget{}, "", ErrRoutingBreakerResetInvalid
	}
	targetKey := ""
	var err error
	if target.Scope == RoutingBreakerResetScopeEndpoint {
		targetKey, err = routingBreakerResetEndpointTargetKey(target.EndpointAuthority, target.Region)
	} else {
		targetKey, err = routingBreakerResetMemberTargetKey(
			target.ChannelID, target.APIKeyIndex, target.ModelName, target.GroupName,
		)
	}
	if err != nil {
		return RoutingBreakerResetTarget{}, "", err
	}
	return target, targetKey, nil
}

func routingBreakerResetMemberTargetKey(channelID int, apiKeyIndex int, modelName string, groupName string) (string, error) {
	modelName = strings.TrimSpace(modelName)
	groupName = strings.TrimSpace(groupName)
	if channelID <= 0 || apiKeyIndex != RoutingMetricSingleKeyIndex || modelName == "" || groupName == "" ||
		utf8.RuneCountInString(modelName) > 128 || utf8.RuneCountInString(groupName) > 64 ||
		!utf8.ValidString(modelName) || !utf8.ValidString(groupName) {
		return "", ErrRoutingBreakerResetInvalid
	}
	canonical, err := common.Marshal(struct {
		Scope       string `json:"scope"`
		ChannelID   int    `json:"channel_id"`
		APIKeyIndex int    `json:"api_key_index"`
		ModelName   string `json:"model_name"`
		GroupName   string `json:"group_name"`
	}{
		Scope: RoutingBreakerResetScopeMember, ChannelID: channelID, APIKeyIndex: apiKeyIndex,
		ModelName: modelName, GroupName: groupName,
	})
	if err != nil {
		return "", err
	}
	return routingBreakerResetHash(canonical), nil
}

func routingBreakerResetEndpointTargetKey(endpointAuthority string, region string) (string, error) {
	endpointAuthority = strings.ToLower(strings.TrimSpace(endpointAuthority))
	region = strings.ToLower(strings.TrimSpace(region))
	if endpointAuthority == "" || region == "" || utf8.RuneCountInString(endpointAuthority) > 320 ||
		utf8.RuneCountInString(region) > 64 ||
		!utf8.ValidString(endpointAuthority) || !utf8.ValidString(region) {
		return "", ErrRoutingBreakerResetInvalid
	}
	canonical, err := common.Marshal(struct {
		Scope             string `json:"scope"`
		EndpointAuthority string `json:"endpoint_authority"`
		Region            string `json:"region"`
	}{Scope: RoutingBreakerResetScopeEndpoint, EndpointAuthority: endpointAuthority, Region: region})
	if err != nil {
		return "", err
	}
	return routingBreakerResetHash(canonical), nil
}

func routingBreakerResetCommandFromTarget(
	operationID int64,
	targetKey string,
	target RoutingBreakerResetTarget,
	nowMs int64,
	legacyBreakerID int,
	legacyGeneration int64,
) RoutingBreakerResetCommand {
	return RoutingBreakerResetCommand{
		OperationID: operationID, TargetKey: targetKey,
		LegacyBreakerID: legacyBreakerID, LegacyGeneration: legacyGeneration,
		Scope:  target.Scope,
		PoolID: target.PoolID, MemberID: target.MemberID, ChannelID: target.ChannelID,
		APIKeyIndex: target.APIKeyIndex, ModelName: target.ModelName, GroupName: target.GroupName,
		EndpointHost: target.EndpointHost, EndpointAuthority: target.EndpointAuthority, Region: target.Region,
		CreatedTimeMs: nowMs,
	}
}

func routingBreakerResetTombstoneFromTarget(
	targetKey string,
	target RoutingBreakerResetTarget,
	nowMs int64,
) RoutingBreakerResetTombstone {
	return RoutingBreakerResetTombstone{
		TargetKey: targetKey, Scope: target.Scope,
		PoolID: target.PoolID, MemberID: target.MemberID, ChannelID: target.ChannelID,
		APIKeyIndex: target.APIKeyIndex, ModelName: target.ModelName, GroupName: target.GroupName,
		EndpointHost: target.EndpointHost, EndpointAuthority: target.EndpointAuthority, Region: target.Region,
		CreatedTimeMs: nowMs, UpdatedTimeMs: nowMs,
	}
}

func routingBreakerResetCommandMatchesTarget(left RoutingBreakerResetCommand, right RoutingBreakerResetCommand) bool {
	return left.OperationID == right.OperationID && left.TargetKey == right.TargetKey &&
		left.LegacyBreakerID == right.LegacyBreakerID && left.LegacyGeneration == right.LegacyGeneration &&
		left.Target() == right.Target()
}

func validRoutingBreakerResetCommand(command RoutingBreakerResetCommand) bool {
	target, targetKey, err := normalizeRoutingBreakerResetTarget(command.Target())
	if err != nil || target != command.Target() || targetKey != command.TargetKey || command.ID <= 0 || command.OperationID <= 0 ||
		command.LegacyBreakerID < 0 || command.LegacyGeneration < 0 ||
		(command.LegacyBreakerID == 0 && command.LegacyGeneration != 0) ||
		(command.LegacyBreakerID > 0 && target.Scope != RoutingBreakerResetScopeMember) ||
		command.CreatedTimeMs <= 0 || command.CompletedTimeMs < 0 ||
		(command.CompletedTimeMs > 0 && command.CompletedTimeMs < command.CreatedTimeMs) ||
		command.Generation < 0 || command.TombstoneID < 0 || command.OutboxID < 0 {
		return false
	}
	if command.CompletedTimeMs == 0 {
		return command.Generation == 0 && command.TombstoneID == 0 && command.OutboxID == 0
	}
	fullyApplied := command.Generation > 0 && command.TombstoneID > 0 && command.OutboxID > 0
	superseded := command.Generation == 0 && command.TombstoneID == 0 && command.OutboxID == 0
	return fullyApplied || superseded
}

func validRoutingBreakerResetTombstone(tombstone RoutingBreakerResetTombstone) bool {
	target, targetKey, err := normalizeRoutingBreakerResetTarget(tombstone.Target())
	if err != nil || target != tombstone.Target() || targetKey != tombstone.TargetKey || tombstone.ID <= 0 ||
		tombstone.Generation <= 0 || tombstone.ResetAtMs <= 0 || tombstone.LastOperationID <= 0 ||
		tombstone.CreatedTimeMs <= 0 || tombstone.UpdatedTimeMs < tombstone.CreatedTimeMs {
		return false
	}
	return true
}

func routingBreakerResetHash(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
