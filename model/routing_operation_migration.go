package model

import (
	"math"

	"gorm.io/gorm"
)

const routingOperationInvariantMigrationBatch = 200

func migrateRoutingOperationStateInvariants(db *gorm.DB) error {
	if db == nil || db.Dialector == nil {
		return ErrRoutingV2SchemaNotReady
	}
	if !db.Migrator().HasTable(&RoutingOperation{}) {
		return nil
	}
	operationTypes := []string{
		RoutingOperationTypeCanaryAutoRollback,
		RoutingOperationTypeCostSync,
		RoutingOperationTypeActiveProbe,
		RoutingOperationTypeBreakerReset,
	}
	terminalStatuses := []RoutingOperationStatus{
		RoutingOperationStatusSucceeded,
		RoutingOperationStatusFailed,
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
						"(operation_type = ? AND status IN ? AND attempts = 0)",
					operationTypes,
					RoutingOperationTypeCanaryAutoRollback,
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
					if repaired.Attempts > 0 {
						repaired.NextRetryMs = max(repaired.NextRetryMs, transitionTimeMs)
					}
				case RoutingOperationStatusRunning:
					repaired.UpdatedTimeMs = transitionTimeMs
					if repaired.ClaimUntilMs <= transitionTimeMs {
						if transitionTimeMs == math.MaxInt64 {
							return ErrRoutingOperationCorrupt
						}
						repaired.ClaimUntilMs = transitionTimeMs + 1
					}
				case RoutingOperationStatusSucceeded,
					RoutingOperationStatusFailed,
					RoutingOperationStatusSuperseded:
					transitionTimeMs = max(transitionTimeMs, repaired.CompletedTimeMs)
					repaired.UpdatedTimeMs = transitionTimeMs
					repaired.CompletedTimeMs = transitionTimeMs
					if repaired.OperationType == RoutingOperationTypeCanaryAutoRollback && repaired.Attempts == 0 {
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
