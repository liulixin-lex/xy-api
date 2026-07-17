package model

import (
	"math"

	"gorm.io/gorm"
)

const (
	routingOperationInvariantMigrationBatch = 200
	routingCostSyncRetiredReason            = "routing cost connector retired; use channel configurations"
)

func migrateRoutingOperationStateInvariants(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingSchemaNotReady
	}
	if !db.Migrator().HasTable(&RoutingOperation{}) {
		return nil
	}
	if err := migrateRoutingOperationV2Metadata(db); err != nil {
		return err
	}
	operationTypes := []string{
		RoutingOperationTypeCanaryAutoRollback,
		RoutingOperationTypePolicySimulation,
		RoutingOperationTypeHistoricalSimulation,
		RoutingOperationTypePolicyPublish,
		RoutingOperationTypePolicyRollback,
		RoutingOperationTypeCostSync,
		RoutingOperationTypeActiveProbe,
		RoutingOperationTypeAuditExport,
		RoutingOperationTypeBreakerReset,
	}
	terminalStatuses := []RoutingOperationStatus{
		RoutingOperationStatusSucceeded,
		RoutingOperationStatusPartial,
		RoutingOperationStatusFailed,
		RoutingOperationStatusCancelled,
		RoutingOperationStatusSuperseded,
	}
	lastID := int64(0)
	for {
		processed := 0
		err := db.Transaction(func(tx *gorm.DB) error {
			var operations []RoutingOperation
			query := lockForUpdate(tx).
				Where("id > ?", lastID).
				Where(
					"(operation_type IN ? AND (updated_time_ms < created_time_ms OR (completed_time_ms > 0 AND completed_time_ms < created_time_ms))) OR "+
						"(operation_type IN ? AND status IN ? AND attempts = 0)",
					operationTypes,
					operationTypes,
					terminalStatuses,
				).
				Order("id asc").
				Limit(routingOperationInvariantMigrationBatch)
			if err := query.Find(&operations).Error; err != nil {
				return err
			}
			for index := range operations {
				operation := operations[index]
				lastID = operation.ID
				repaired := operation
				transitionTimeMs := max(repaired.CreatedTimeMs, repaired.UpdatedTimeMs)
				switch repaired.Status {
				case RoutingOperationStatusPending:
					repaired.UpdatedTimeMs = transitionTimeMs
				case RoutingOperationStatusRetryWait:
					repaired.UpdatedTimeMs = transitionTimeMs
					repaired.NextRetryMs = max(repaired.NextRetryMs, transitionTimeMs)
				case RoutingOperationStatusRunning:
					repaired.UpdatedTimeMs = transitionTimeMs
					if repaired.ClaimUntilMs <= transitionTimeMs {
						if transitionTimeMs == math.MaxInt64 {
							return ErrRoutingOperationCorrupt
						}
						repaired.ClaimUntilMs = transitionTimeMs + 1
					}
				case RoutingOperationStatusSucceeded,
					RoutingOperationStatusPartial,
					RoutingOperationStatusFailed,
					RoutingOperationStatusCancelled,
					RoutingOperationStatusSuperseded:
					transitionTimeMs = max(transitionTimeMs, repaired.CompletedTimeMs)
					repaired.UpdatedTimeMs = transitionTimeMs
					repaired.CompletedTimeMs = transitionTimeMs
					if repaired.Attempts == 0 {
						repaired.Attempts = 1
					}
				default:
					return ErrRoutingOperationCorrupt
				}
				if err := validateStoredRoutingOperation(repaired); err != nil {
					return err
				}
				updated := tx.Model(&RoutingOperation{}).
					Where(
						"id = ? AND status = ? AND attempts = ? AND created_time_ms = ? AND updated_time_ms = ? "+
							"AND completed_time_ms = ? AND next_retry_ms = ? AND claim_until_ms = ?",
						operation.ID,
						operation.Status,
						operation.Attempts,
						operation.CreatedTimeMs,
						operation.UpdatedTimeMs,
						operation.CompletedTimeMs,
						operation.NextRetryMs,
						operation.ClaimUntilMs,
					).
					Updates(map[string]any{
						"attempts":          repaired.Attempts,
						"claim_until_ms":    repaired.ClaimUntilMs,
						"next_retry_ms":     repaired.NextRetryMs,
						"updated_time_ms":   repaired.UpdatedTimeMs,
						"completed_time_ms": repaired.CompletedTimeMs,
					})
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					var current RoutingOperation
					if err := tx.Where("id = ?", operation.ID).First(&current).Error; err != nil {
						return err
					}
					if err := validateStoredRoutingOperation(current); err != nil {
						return err
					}
				}
				var stored RoutingOperation
				if err := tx.Where("id = ?", operation.ID).First(&stored).Error; err != nil {
					return err
				}
				if err := validateStoredRoutingOperation(stored); err != nil {
					return err
				}
			}
			processed = len(operations)
			return nil
		})
		if err != nil {
			return err
		}
		if processed < routingOperationInvariantMigrationBatch {
			break
		}
	}

	if !db.Migrator().HasTable(&RoutingBreakerResetCommand{}) {
		return nil
	}
	lastID = 0
	for {
		processed := 0
		err := db.Transaction(func(tx *gorm.DB) error {
			var commands []RoutingBreakerResetCommand
			if err := lockForUpdate(tx).
				Where("id > ? AND completed_time_ms > 0 AND completed_time_ms < created_time_ms", lastID).
				Order("id asc").
				Limit(routingOperationInvariantMigrationBatch).
				Find(&commands).Error; err != nil {
				return err
			}
			for index := range commands {
				command := commands[index]
				lastID = command.ID
				repaired := command
				repaired.CompletedTimeMs = repaired.CreatedTimeMs
				if !validRoutingBreakerResetCommand(repaired) {
					return ErrRoutingBreakerResetInvalid
				}
				updated := tx.Model(&RoutingBreakerResetCommand{}).
					Where(
						"id = ? AND created_time_ms = ? AND completed_time_ms = ?",
						command.ID,
						command.CreatedTimeMs,
						command.CompletedTimeMs,
					).
					Update("completed_time_ms", repaired.CompletedTimeMs)
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					var current RoutingBreakerResetCommand
					if err := tx.Where("id = ?", command.ID).First(&current).Error; err != nil {
						return err
					}
					if !validRoutingBreakerResetCommand(current) {
						return ErrRoutingBreakerResetInvalid
					}
				}
				var stored RoutingBreakerResetCommand
				if err := tx.Where("id = ?", command.ID).First(&stored).Error; err != nil {
					return err
				}
				if !validRoutingBreakerResetCommand(stored) {
					return ErrRoutingBreakerResetInvalid
				}
			}
			processed = len(commands)
			return nil
		})
		if err != nil {
			return err
		}
		if processed < routingOperationInvariantMigrationBatch {
			break
		}
	}
	return nil
}

func migrateRoutingOperationV2Metadata(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || !db.Migrator().HasTable(&RoutingOperation{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&RoutingOperation{}, "schema_version") {
		return ErrRoutingSchemaNotReady
	}
	lastID := int64(0)
	for {
		processed := 0
		err := db.Transaction(func(tx *gorm.DB) error {
			var operations []RoutingOperation
			if err := lockForUpdate(tx).
				Where("id > ? AND schema_version < ?", lastID, routingOperationRecordSchemaVersion).
				Order("id asc").Limit(routingOperationInvariantMigrationBatch).
				Find(&operations).Error; err != nil {
				return err
			}
			for index := range operations {
				operation := operations[index]
				lastID = operation.ID
				normalizeRoutingOperationStorage(&operation)
				if operation.Status == RoutingOperationStatusRetryWait {
					operation.NextRetryMs = max(operation.NextRetryMs, operation.UpdatedTimeMs)
				}
				updates := map[string]any{
					"schema_version":        operation.SchemaVersion,
					"source":                operation.Source,
					"correlation_id":        operation.CorrelationID,
					"parent_operation_id":   operation.ParentOperationID,
					"retry_of_operation_id": operation.RetryOfOperationID,
					"retry_sequence":        operation.RetrySequence,
					"retryable":             operation.Retryable,
					"cancellable":           operation.Cancellable,
					"summary":               operation.Summary,
					"needs_attention":       operation.NeedsAttention,
					"retention_category":    operation.RetentionCategory,
					"terminal_actor_id":     operation.TerminalActorID,
					"status":                operation.Status,
					"next_retry_ms":         operation.NextRetryMs,
				}
				updated := tx.Model(&RoutingOperation{}).
					Where("id = ? AND schema_version < ?", operation.ID, routingOperationRecordSchemaVersion).
					Updates(updates)
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return ErrRoutingOperationCorrupt
				}
			}
			processed = len(operations)
			return nil
		})
		if err != nil {
			return err
		}
		if processed < routingOperationInvariantMigrationBatch {
			return nil
		}
	}
}

func retireRoutingCostSyncWork(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingSchemaNotReady
	}
	nowMs, err := routingDatabaseNowMs(db)
	if err != nil {
		return err
	}
	now := nowMs / 1_000
	if now <= 0 {
		now = 1
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasTable(&RoutingOperation{}) {
			for {
				var operations []RoutingOperation
				if err := lockForUpdate(tx).
					Where("operation_type = ? AND status IN ?", RoutingOperationTypeCostSync, []RoutingOperationStatus{
						RoutingOperationStatusPending,
						RoutingOperationStatusRunning,
						RoutingOperationStatusRetryWait,
					}).
					Order("id asc").
					Limit(routingOperationInvariantMigrationBatch).
					Find(&operations).Error; err != nil {
					return err
				}
				for index := range operations {
					operation := operations[index]
					transitionTimeMs := max(nowMs, operation.CreatedTimeMs, operation.UpdatedTimeMs)
					updates := routingOperationTerminalUpdates(
						RoutingOperationStatusSuperseded,
						routingCostSyncRetiredReason,
						RoutingOperationResult{},
						transitionTimeMs,
					)
					updates["attempts"] = gorm.Expr("CASE WHEN attempts < ? THEN ? ELSE attempts END", 1, 1)
					updated := tx.Model(&RoutingOperation{}).
						Where("id = ? AND status IN ?", operation.ID, []RoutingOperationStatus{
							RoutingOperationStatusPending,
							RoutingOperationStatusRunning,
							RoutingOperationStatusRetryWait,
						}).
						Updates(updates)
					if updated.Error != nil {
						return updated.Error
					}
					if updated.RowsAffected != 1 {
						return ErrRoutingOperationClaimLost
					}
					var stored RoutingOperation
					if err := tx.Where("id = ?", operation.ID).First(&stored).Error; err != nil {
						return err
					}
					if err := validateStoredRoutingOperation(stored); err != nil {
						return err
					}
				}
				if len(operations) < routingOperationInvariantMigrationBatch {
					break
				}
			}
		}

		if tx.Migrator().HasTable(&SystemTask{}) {
			for {
				var tasks []SystemTask
				if err := lockForUpdate(tx).
					Where("type = ? AND status IN ?", SystemTaskTypeRoutingCostSync, []SystemTaskStatus{
						SystemTaskStatusPending,
						SystemTaskStatusRunning,
					}).
					Order("id asc").
					Limit(routingOperationInvariantMigrationBatch).
					Find(&tasks).Error; err != nil {
					return err
				}
				for index := range tasks {
					task := tasks[index]
					transitionTime := max(now, task.CreatedAt, task.UpdatedAt)
					updated := tx.Model(&SystemTask{}).
						Where("id = ? AND type = ? AND status IN ?", task.ID, SystemTaskTypeRoutingCostSync, []SystemTaskStatus{
							SystemTaskStatusPending,
							SystemTaskStatusRunning,
						}).
						Updates(map[string]any{
							"status":     SystemTaskStatusFailed,
							"active_key": nil,
							"error":      routingCostSyncRetiredReason,
							"locked_by":  "",
							"updated_at": transitionTime,
						})
					if updated.Error != nil {
						return updated.Error
					}
					if updated.RowsAffected != 1 {
						return ErrSystemTaskLockLost
					}
				}
				if len(tasks) < routingOperationInvariantMigrationBatch {
					break
				}
			}
		}
		if tx.Migrator().HasTable(&SystemTaskLock{}) {
			if err := tx.Where("type = ?", SystemTaskTypeRoutingCostSync).
				Delete(&SystemTaskLock{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
