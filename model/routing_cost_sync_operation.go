package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type RoutingCostSyncTaskPayload struct {
	RoutingOperationID int64 `json:"routing_operation_id,omitempty"`
}

type RoutingCostSyncOperationResult struct {
	SystemTaskID   string           `json:"system_task_id"`
	SystemTaskType string           `json:"system_task_type"`
	TaskStatus     SystemTaskStatus `json:"task_status"`
	ExecutionState string           `json:"execution_state"`
	Summary        any              `json:"summary,omitempty"`
}

const (
	RoutingCostSyncExecutionStateCompleted = "completed"
	RoutingCostSyncExecutionStatePartial   = "partial"
	RoutingCostSyncExecutionStateFailed    = "failed"
)

func AttachRoutingCostSyncOperationContext(
	ctx context.Context,
	operationID int64,
) (*SystemTask, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if operationID <= 0 {
		return nil, false, ErrRoutingOperationInvalid
	}

	var attachedTask SystemTask
	createdTask := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var operation RoutingOperation
		if err := lockForUpdate(tx.WithContext(ctx)).Where("id = ?", operationID).First(&operation).Error; err != nil {
			return err
		}
		if err := validateStoredRoutingOperation(operation); err != nil {
			return err
		}
		if operation.OperationType != RoutingOperationTypeCostSync {
			return ErrRoutingOperationInvalid
		}
		if operation.SystemTaskID != "" {
			if err := lockForUpdate(tx.WithContext(ctx)).
				Where("task_id = ? AND type = ?", operation.SystemTaskID, SystemTaskTypeRoutingCostSync).
				First(&attachedTask).Error; err != nil {
				return err
			}
			return reconcileAttachedRoutingCostSyncOperationTx(ctx, tx, operation, attachedTask, time.Now().UnixMilli())
		}
		if operation.Status != RoutingOperationStatusPending {
			return ErrRoutingOperationInvalid
		}

		query := lockForUpdate(tx.WithContext(ctx)).
			Where("type = ? AND status IN ?", SystemTaskTypeRoutingCostSync, activeSystemTaskStatuses()).
			Order("id desc")
		err := query.First(&attachedTask).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			taskID, generateErr := GenerateSystemTaskID()
			if generateErr != nil {
				return generateErr
			}
			payload, marshalErr := marshalSystemTaskJSON(RoutingCostSyncTaskPayload{RoutingOperationID: operation.ID})
			if marshalErr != nil {
				return marshalErr
			}
			activeKey := SystemTaskTypeRoutingCostSync
			candidate := SystemTask{
				TaskID: taskID, Type: SystemTaskTypeRoutingCostSync, Status: SystemTaskStatusPending,
				ActiveKey: &activeKey, Payload: payload,
			}
			create := tx.WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "active_key"}},
				DoNothing: true,
			}).Create(&candidate)
			if create.Error != nil {
				return create.Error
			}
			if create.RowsAffected == 1 {
				attachedTask = candidate
				createdTask = true
			} else if err := query.First(&attachedTask).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		nowMs := time.Now().UnixMilli()
		transitionTimeMs := max(nowMs, operation.CreatedTimeMs, operation.UpdatedTimeMs)
		updates := map[string]any{
			"system_task_id":  attachedTask.TaskID,
			"updated_time_ms": transitionTimeMs,
		}
		if attachedTask.Status == SystemTaskStatusRunning {
			lockValid, err := routingCostSyncTaskLockValidTx(ctx, tx, attachedTask, nowMs)
			if err != nil {
				return err
			}
			if lockValid {
				if operation.Attempts == int(^uint(0)>>1) {
					return ErrRoutingOperationInvalid
				}
				leaseMs := int64(time.Minute / time.Millisecond)
				if transitionTimeMs > math.MaxInt64-leaseMs {
					return ErrRoutingOperationInvalid
				}
				updates["status"] = RoutingOperationStatusRunning
				updates["claim_token"] = routingCostSyncClaimToken(attachedTask.TaskID, attachedTask.LockedBy)
				updates["claim_until_ms"] = transitionTimeMs + leaseMs
				updates["attempts"] = gorm.Expr("attempts + ?", 1)
			}
		}
		updated := tx.WithContext(ctx).Model(&RoutingOperation{}).
			Where("id = ? AND operation_type = ? AND status = ?", operation.ID, RoutingOperationTypeCostSync, RoutingOperationStatusPending).
			Where("system_task_id = '' OR system_task_id IS NULL").
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRoutingOperationClaimLost
		}
		var stored RoutingOperation
		if err := tx.WithContext(ctx).Where("id = ?", operation.ID).First(&stored).Error; err != nil {
			return err
		}
		return validateStoredRoutingOperation(stored)
	})
	if err != nil {
		return nil, false, err
	}
	return &attachedTask, createdTask, nil
}

func ClaimRoutingCostSyncOperationsContext(
	ctx context.Context,
	taskID string,
	runnerID string,
	nowMs int64,
	leaseMs int64,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, err := ownedRoutingCostSyncTaskTx(ctx, tx, taskID, runnerID, nowMs)
		if err != nil {
			return err
		}
		_, err = syncRoutingCostSyncOperationClaimsTx(ctx, tx, task, nowMs, leaseMs)
		return err
	})
}

func RenewRoutingCostSyncOperationsContext(
	ctx context.Context,
	taskID string,
	runnerID string,
	nowMs int64,
	leaseMs int64,
) error {
	return ClaimRoutingCostSyncOperationsContext(ctx, taskID, runnerID, nowMs, leaseMs)
}

func FinishRoutingCostSyncTaskContext(
	ctx context.Context,
	taskID string,
	runnerID string,
	status SystemTaskStatus,
	resultPayload any,
	errorMessage string,
	nowMs int64,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if status != SystemTaskStatusSucceeded && status != SystemTaskStatusFailed {
		return 0, ErrRoutingOperationInvalid
	}
	errorMessage = routingOperationText(errorMessage, routingOperationErrorMaxRunes)
	if (status == SystemTaskStatusSucceeded) != (errorMessage == "") {
		return 0, ErrRoutingOperationInvalid
	}

	var operationCount int64
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, err := ownedRoutingCostSyncTaskTx(ctx, tx, taskID, runnerID, nowMs)
		if err != nil {
			return err
		}
		leaseMs := int64(time.Minute / time.Millisecond)
		transitionTimeMs, err := syncRoutingCostSyncOperationClaimsTx(ctx, tx, task, nowMs, leaseMs)
		if err != nil {
			return err
		}

		if err := tx.WithContext(ctx).Model(&RoutingOperation{}).
			Where("system_task_id = ? AND status IN ?", task.TaskID, []RoutingOperationStatus{
				RoutingOperationStatusPending, RoutingOperationStatusRunning,
			}).Count(&operationCount).Error; err != nil {
			return err
		}
		claimToken := routingCostSyncClaimToken(task.TaskID, runnerID)
		operationStatus := RoutingOperationStatusSucceeded
		operationResult := RoutingOperationResult{}
		lastError := ""
		if status == SystemTaskStatusSucceeded {
			operationPayload := RoutingCostSyncOperationResult{
				SystemTaskID: task.TaskID, SystemTaskType: task.Type, TaskStatus: status,
				ExecutionState: routingCostSyncResultExecutionState(resultPayload), Summary: resultPayload,
			}
			operationResult.PayloadJSON, operationResult.PayloadHash, err = normalizeRoutingOperationResultPayload(operationPayload)
			if err != nil {
				return err
			}
		} else {
			operationStatus = RoutingOperationStatusFailed
			lastError = errorMessage
		}
		finishedOperations := tx.WithContext(ctx).Model(&RoutingOperation{}).
			Where("system_task_id = ? AND status = ? AND claim_token = ? AND claim_until_ms > ?",
				task.TaskID, RoutingOperationStatusRunning, claimToken, nowMs).
			Updates(routingOperationTerminalUpdates(operationStatus, lastError, operationResult, transitionTimeMs))
		if finishedOperations.Error != nil {
			return finishedOperations.Error
		}
		if finishedOperations.RowsAffected != operationCount {
			return ErrRoutingOperationClaimLost
		}
		var storedOperations []RoutingOperation
		if err := tx.WithContext(ctx).Where("system_task_id = ?", task.TaskID).Find(&storedOperations).Error; err != nil {
			return err
		}
		for index := range storedOperations {
			if err := validateStoredRoutingOperation(storedOperations[index]); err != nil {
				return err
			}
		}

		resultText, err := marshalSystemTaskJSON(resultPayload)
		if err != nil {
			return err
		}
		now := nowMs / 1_000
		finishedTask := tx.WithContext(ctx).Model(&SystemTask{}).
			Where("task_id = ? AND type = ? AND status = ? AND locked_by = ?",
				task.TaskID, SystemTaskTypeRoutingCostSync, SystemTaskStatusRunning, runnerID).
			Where("EXISTS (SELECT 1 FROM system_task_locks WHERE system_task_locks.task_id = system_tasks.task_id AND system_task_locks.locked_by = ? AND system_task_locks.locked_until >= ?)", runnerID, now).
			Updates(map[string]any{
				"status": status, "active_key": nil, "result": resultText,
				"error": errorMessage, "updated_at": now,
			})
		if finishedTask.Error != nil {
			return finishedTask.Error
		}
		if finishedTask.RowsAffected != 1 {
			return ErrSystemTaskLockLost
		}
		deleted := tx.WithContext(ctx).
			Where("task_id = ? AND locked_by = ?", task.TaskID, runnerID).
			Delete(&SystemTaskLock{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected != 1 {
			return ErrSystemTaskLockLost
		}
		return nil
	})
	return operationCount, err
}

func FailUnattachedRoutingOperationContext(
	ctx context.Context,
	operationID int64,
	operationErr error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	lastError := routingOperationErrorText(operationErr)
	if operationID <= 0 || lastError == "" {
		return ErrRoutingOperationInvalid
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var operation RoutingOperation
		err := lockForUpdate(tx.WithContext(ctx)).
			Where("id = ? AND operation_type = ? AND status = ?", operationID, RoutingOperationTypeCostSync, RoutingOperationStatusPending).
			Where("system_task_id = '' OR system_task_id IS NULL").
			First(&operation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRoutingOperationClaimLost
		}
		if err != nil {
			return err
		}
		if err := validateStoredRoutingOperation(operation); err != nil {
			return err
		}
		if operation.Attempts == int(^uint(0)>>1) {
			return ErrRoutingOperationInvalid
		}
		nowMs := max(time.Now().UnixMilli(), operation.CreatedTimeMs, operation.UpdatedTimeMs)
		updates := routingOperationTerminalUpdates(
			RoutingOperationStatusFailed, lastError, RoutingOperationResult{}, nowMs,
		)
		updates["attempts"] = gorm.Expr("attempts + ?", 1)
		updated := tx.WithContext(ctx).Model(&RoutingOperation{}).
			Where("id = ? AND operation_type = ? AND status = ?", operationID, RoutingOperationTypeCostSync, RoutingOperationStatusPending).
			Where("system_task_id = '' OR system_task_id IS NULL").
			Updates(updates)
		if err := routingOperationCASResult(updated); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Where("id = ?", operationID).First(&operation).Error; err != nil {
			return err
		}
		return validateStoredRoutingOperation(operation)
	})
}

func ownedRoutingCostSyncTaskTx(
	ctx context.Context,
	tx *gorm.DB,
	taskID string,
	runnerID string,
	nowMs int64,
) (SystemTask, error) {
	if tx == nil || !validSystemTaskID(taskID) || runnerID == "" || nowMs <= 0 {
		return SystemTask{}, ErrRoutingOperationInvalid
	}
	var task SystemTask
	if err := lockForUpdate(tx.WithContext(ctx)).
		Where("task_id = ? AND type = ? AND status = ? AND locked_by = ?",
			taskID, SystemTaskTypeRoutingCostSync, SystemTaskStatusRunning, runnerID).
		First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SystemTask{}, ErrSystemTaskLockLost
		}
		return SystemTask{}, err
	}
	valid, err := routingCostSyncTaskLockValidTx(ctx, tx, task, nowMs)
	if err != nil {
		return SystemTask{}, err
	}
	if !valid {
		return SystemTask{}, ErrSystemTaskLockLost
	}
	return task, nil
}

func routingCostSyncTaskLockValidTx(
	ctx context.Context,
	tx *gorm.DB,
	task SystemTask,
	nowMs int64,
) (bool, error) {
	if task.Status != SystemTaskStatusRunning || task.LockedBy == "" || nowMs <= 0 {
		return false, nil
	}
	var count int64
	err := tx.WithContext(ctx).Model(&SystemTaskLock{}).
		Where("type = ? AND task_id = ? AND locked_by = ? AND locked_until >= ?",
			task.Type, task.TaskID, task.LockedBy, nowMs/1_000).
		Count(&count).Error
	return count == 1, err
}

func syncRoutingCostSyncOperationClaimsTx(
	ctx context.Context,
	tx *gorm.DB,
	task SystemTask,
	nowMs int64,
	leaseMs int64,
) (int64, error) {
	if tx == nil || leaseMs <= 0 || leaseMs > routingOperationMaxClaimMs || nowMs <= 0 || nowMs > math.MaxInt64-leaseMs {
		return 0, ErrRoutingOperationInvalid
	}
	var activeOperations []RoutingOperation
	if err := lockForUpdate(tx.WithContext(ctx)).
		Where("system_task_id = ? AND status IN ?", task.TaskID, []RoutingOperationStatus{
			RoutingOperationStatusPending, RoutingOperationStatusRunning,
		}).
		Find(&activeOperations).Error; err != nil {
		return 0, err
	}
	transitionTimeMs := nowMs
	claimToken := routingCostSyncClaimToken(task.TaskID, task.LockedBy)
	for index := range activeOperations {
		operation := activeOperations[index]
		if err := validateStoredRoutingOperation(operation); err != nil {
			return 0, err
		}
		if operation.Attempts == int(^uint(0)>>1) &&
			(operation.Status == RoutingOperationStatusPending || operation.ClaimUntilMs <= nowMs) {
			return 0, ErrRoutingOperationInvalid
		}
		transitionTimeMs = max(transitionTimeMs, operation.CreatedTimeMs, operation.UpdatedTimeMs)
	}
	if transitionTimeMs > math.MaxInt64-leaseMs {
		return 0, ErrRoutingOperationInvalid
	}
	claimUntilMs := transitionTimeMs + leaseMs
	for index := range activeOperations {
		operation := activeOperations[index]
		if operation.Status == RoutingOperationStatusRunning && operation.ClaimToken == claimToken {
			claimUntilMs = max(claimUntilMs, operation.ClaimUntilMs)
		}
	}
	claimable := tx.WithContext(ctx).Model(&RoutingOperation{}).
		Where("system_task_id = ?", task.TaskID).
		Where("status = ? OR (status = ? AND claim_until_ms <= ?)",
			RoutingOperationStatusPending, RoutingOperationStatusRunning, nowMs).
		Updates(map[string]any{
			"status": RoutingOperationStatusRunning, "claim_token": claimToken,
			"claim_until_ms": claimUntilMs, "attempts": gorm.Expr("attempts + ?", 1),
			"updated_time_ms": transitionTimeMs,
		})
	if claimable.Error != nil {
		return 0, claimable.Error
	}
	renewed := tx.WithContext(ctx).Model(&RoutingOperation{}).
		Where("system_task_id = ? AND status = ? AND claim_token = ?",
			task.TaskID, RoutingOperationStatusRunning, claimToken).
		Updates(map[string]any{
			"claim_until_ms":  claimUntilMs,
			"updated_time_ms": transitionTimeMs,
		})
	if renewed.Error != nil {
		return 0, renewed.Error
	}
	var ownedOperations []RoutingOperation
	if err := tx.WithContext(ctx).
		Where("system_task_id = ? AND status IN ?", task.TaskID, []RoutingOperationStatus{
			RoutingOperationStatusPending, RoutingOperationStatusRunning,
		}).
		Find(&ownedOperations).Error; err != nil {
		return 0, err
	}
	if len(activeOperations) != len(ownedOperations) {
		return 0, ErrRoutingOperationClaimLost
	}
	for index := range ownedOperations {
		operation := ownedOperations[index]
		if err := validateStoredRoutingOperation(operation); err != nil {
			return 0, err
		}
		if operation.Status != RoutingOperationStatusRunning || operation.ClaimToken != claimToken ||
			operation.ClaimUntilMs <= nowMs {
			return 0, ErrRoutingOperationClaimLost
		}
	}
	return transitionTimeMs, nil
}

func routingCostSyncClaimToken(taskID string, runnerID string) string {
	hash := sha256.Sum256([]byte(taskID + "\x00" + runnerID))
	return hex.EncodeToString(hash[:16])
}

func reconcileAttachedRoutingCostSyncOperationTx(
	ctx context.Context,
	tx *gorm.DB,
	operation RoutingOperation,
	task SystemTask,
	nowMs int64,
) error {
	if operation.Status != RoutingOperationStatusPending && operation.Status != RoutingOperationStatusRunning {
		return nil
	}
	if task.Status == SystemTaskStatusPending {
		return nil
	}
	if task.Status == SystemTaskStatusRunning {
		valid, err := routingCostSyncTaskLockValidTx(ctx, tx, task, nowMs)
		if err != nil || !valid {
			return err
		}
		transitionTimeMs := max(nowMs, operation.CreatedTimeMs, operation.UpdatedTimeMs)
		leaseMs := int64(time.Minute / time.Millisecond)
		if operation.Attempts == int(^uint(0)>>1) || transitionTimeMs > math.MaxInt64-leaseMs {
			return ErrRoutingOperationInvalid
		}
		claimToken := routingCostSyncClaimToken(task.TaskID, task.LockedBy)
		updated := tx.WithContext(ctx).Model(&RoutingOperation{}).
			Where("id = ? AND system_task_id = ? AND status = ?",
				operation.ID, task.TaskID, RoutingOperationStatusPending).
			Updates(map[string]any{
				"status": RoutingOperationStatusRunning, "claim_token": claimToken,
				"claim_until_ms": transitionTimeMs + leaseMs,
				"attempts":       gorm.Expr("attempts + ?", 1), "updated_time_ms": transitionTimeMs,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if err := tx.WithContext(ctx).Where("id = ?", operation.ID).First(&operation).Error; err != nil {
			return err
		}
		return validateStoredRoutingOperation(operation)
	}

	operationStatus := RoutingOperationStatusFailed
	lastError := routingOperationText(task.Error, routingOperationErrorMaxRunes)
	operationResult := RoutingOperationResult{}
	if task.Status == SystemTaskStatusSucceeded {
		operationStatus = RoutingOperationStatusSucceeded
		lastError = ""
		summary := decodeSystemTaskJSONValue(task.Result)
		payload := RoutingCostSyncOperationResult{
			SystemTaskID: task.TaskID, SystemTaskType: task.Type, TaskStatus: task.Status,
			ExecutionState: routingCostSyncResultExecutionState(summary), Summary: summary,
		}
		var err error
		operationResult.PayloadJSON, operationResult.PayloadHash, err = normalizeRoutingOperationResultPayload(payload)
		if err != nil {
			return err
		}
	} else if task.Status != SystemTaskStatusFailed {
		return ErrRoutingOperationInvalid
	} else if lastError == "" {
		lastError = "cost sync task failed"
	}
	transitionTimeMs := max(nowMs, operation.CreatedTimeMs, operation.UpdatedTimeMs)
	updates := routingOperationTerminalUpdates(operationStatus, lastError, operationResult, transitionTimeMs)
	updates["attempts"] = gorm.Expr("CASE WHEN attempts < ? THEN ? ELSE attempts END", 1, 1)
	updated := tx.WithContext(ctx).Model(&RoutingOperation{}).
		Where("id = ? AND system_task_id = ? AND status IN ?", operation.ID, task.TaskID, []RoutingOperationStatus{
			RoutingOperationStatusPending, RoutingOperationStatusRunning,
		}).Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if err := tx.WithContext(ctx).Where("id = ?", operation.ID).First(&operation).Error; err != nil {
		return err
	}
	return validateStoredRoutingOperation(operation)
}

func routingCostSyncResultExecutionState(resultPayload any) string {
	encoded, err := common.Marshal(resultPayload)
	if err != nil {
		return RoutingCostSyncExecutionStateCompleted
	}
	var result struct {
		ExecutionState string `json:"execution_state"`
	}
	if err := common.Unmarshal(encoded, &result); err != nil || result.ExecutionState != RoutingCostSyncExecutionStatePartial {
		return RoutingCostSyncExecutionStateCompleted
	}
	return RoutingCostSyncExecutionStatePartial
}

func markSystemTaskLeaseExpiredContext(ctx context.Context, taskID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validSystemTaskID(taskID) {
		return ErrRoutingOperationInvalid
	}
	now := common.GetTimestamp()
	nowMs := time.Now().UnixMilli()
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task SystemTask
		if err := lockForUpdate(tx.WithContext(ctx)).Where("task_id = ?", taskID).First(&task).Error; err != nil {
			return err
		}
		if task.Status != SystemTaskStatusRunning {
			return nil
		}
		lastError := "task lease expired"
		if tx.Migrator().HasTable(&RoutingOperation{}) {
			var activeOperations []RoutingOperation
			if err := lockForUpdate(tx.WithContext(ctx)).
				Where("system_task_id = ? AND status IN ?", task.TaskID, []RoutingOperationStatus{
					RoutingOperationStatusPending, RoutingOperationStatusRunning,
				}).
				Find(&activeOperations).Error; err != nil {
				return err
			}
			for index := range activeOperations {
				if err := validateStoredRoutingOperation(activeOperations[index]); err != nil {
					return err
				}
			}
			transitionTimeMs := nowMs
			for index := range activeOperations {
				transitionTimeMs = max(
					transitionTimeMs,
					activeOperations[index].CreatedTimeMs,
					activeOperations[index].UpdatedTimeMs,
				)
			}
			updates := routingOperationTerminalUpdates(
				RoutingOperationStatusFailed, lastError, RoutingOperationResult{}, transitionTimeMs,
			)
			updates["attempts"] = gorm.Expr("CASE WHEN attempts < ? THEN ? ELSE attempts END", 1, 1)
			if err := tx.WithContext(ctx).Model(&RoutingOperation{}).
				Where("system_task_id = ? AND status IN ?", task.TaskID, []RoutingOperationStatus{
					RoutingOperationStatusPending, RoutingOperationStatusRunning,
				}).Updates(updates).Error; err != nil {
				return err
			}
			if len(activeOperations) > 0 {
				ids := make([]int64, len(activeOperations))
				for index := range activeOperations {
					ids[index] = activeOperations[index].ID
				}
				var storedOperations []RoutingOperation
				if err := tx.WithContext(ctx).Where("id IN ?", ids).Find(&storedOperations).Error; err != nil {
					return err
				}
				if len(storedOperations) != len(activeOperations) {
					return ErrRoutingOperationClaimLost
				}
				for index := range storedOperations {
					if err := validateStoredRoutingOperation(storedOperations[index]); err != nil {
						return err
					}
				}
			}
		}
		return tx.WithContext(ctx).Model(&SystemTask{}).
			Where("task_id = ? AND status = ?", task.TaskID, SystemTaskStatusRunning).
			Updates(map[string]any{
				"status": SystemTaskStatusFailed, "active_key": nil,
				"error": lastError, "updated_at": now,
			}).Error
	})
}
