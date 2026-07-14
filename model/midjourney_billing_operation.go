package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var (
	ErrMidjourneyBillingOperationInvariant    = errors.New("Midjourney billing operation invariant violation")
	ErrMidjourneyBillingOperationNotClaimed   = errors.New("Midjourney billing operation is not claimed by this worker")
	ErrMidjourneyBillingOperationLeaseExpired = errors.New("Midjourney billing operation lease has expired")
)

type MidjourneyBillingOperation struct {
	ID             int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	MidjourneyID   int    `json:"midjourney_id" gorm:"not null;uniqueIndex:uidx_midjourney_billing_operation_task"`
	ReservationID  int64  `json:"reservation_id" gorm:"index"`
	OperationKey   string `json:"operation_key" gorm:"type:varchar(191);not null;uniqueIndex:uidx_midjourney_billing_operation_key"`
	TerminalStatus string `json:"terminal_status" gorm:"type:varchar(20);not null"`
	Kind           string `json:"kind" gorm:"type:varchar(20);not null"`
	State          string `json:"state" gorm:"type:varchar(20);not null;index:idx_midjourney_billing_pending,priority:1;index:idx_midjourney_billing_lease,priority:1"`

	UserID                  int    `json:"user_id" gorm:"not null;index"`
	ChannelID               int    `json:"channel_id" gorm:"not null;index"`
	BillingSource           string `json:"billing_source" gorm:"type:varchar(20);not null"`
	SubscriptionID          int    `json:"subscription_id"`
	TokenID                 int    `json:"token_id"`
	RefundQuota             int    `json:"refund_quota" gorm:"not null"`
	ModelName               string `json:"model_name" gorm:"type:varchar(191)"`
	Group                   string `json:"group" gorm:"type:varchar(64)"`
	Reason                  string `json:"reason" gorm:"type:varchar(1024)"`
	TerminalPayloadProtocol int    `json:"terminal_payload_protocol" gorm:"not null"`
	TerminalPayloadHash     string `json:"terminal_payload_hash,omitempty" gorm:"type:varchar(64)"`
	TerminalPayload         []byte `json:"-"`

	LeaseOwner      string `json:"lease_owner,omitempty" gorm:"type:varchar(128)"`
	LeaseUntilMs    int64  `json:"lease_until_ms" gorm:"index:idx_midjourney_billing_lease,priority:2"`
	Attempts        int    `json:"attempts" gorm:"not null"`
	NextRetryTimeMs int64  `json:"next_retry_time_ms" gorm:"index:idx_midjourney_billing_pending,priority:2"`
	LastError       string `json:"last_error,omitempty" gorm:"type:varchar(1024)"`

	CreatedTimeMs   int64 `json:"created_time_ms" gorm:"not null"`
	UpdatedTimeMs   int64 `json:"updated_time_ms" gorm:"not null"`
	CompletedTimeMs int64 `json:"completed_time_ms"`

	LogState           string `json:"log_state" gorm:"type:varchar(20);not null;index:idx_midjourney_billing_log_pending,priority:1"`
	LogPayloadProtocol int    `json:"log_payload_protocol" gorm:"not null"`
	LogPayloadHash     string `json:"log_payload_hash,omitempty" gorm:"type:varchar(64)"`
	LogPayload         string `json:"log_payload,omitempty" gorm:"type:text"`
	LogAttempts        int    `json:"log_attempts" gorm:"not null"`
	LogLastError       string `json:"log_last_error,omitempty" gorm:"type:varchar(1024)"`
	LogUpdatedTimeMs   int64  `json:"log_updated_time_ms"`
	LogLeaseOwner      string `json:"log_lease_owner,omitempty" gorm:"type:varchar(128)"`
	LogLeaseUntilMs    int64  `json:"log_lease_until_ms" gorm:"index"`
	LogNextRetryMs     int64  `json:"log_next_retry_ms" gorm:"index:idx_midjourney_billing_log_pending,priority:2"`

	UsageUserOutcome    string `json:"usage_user_outcome,omitempty" gorm:"type:varchar(32)"`
	UsageChannelOutcome string `json:"usage_channel_outcome,omitempty" gorm:"type:varchar(32)"`
	UsageWarning        string `json:"usage_warning,omitempty" gorm:"type:varchar(1024)"`
}

type MidjourneyFailureFinalization struct {
	Task               Midjourney
	Operation          MidjourneyBillingOperation
	Transitioned       bool
	ObservationMatched bool
}

// createAcceptedMidjourneyTerminalOperationTx is used by the v2 accepted
// handoff when the provider returns an immediate terminal result. The operation
// is created in the same transaction as the real task and reservation binding.
func createAcceptedMidjourneyTerminalOperationTx(
	tx *gorm.DB,
	task *Midjourney,
	now time.Time,
) (*MidjourneyBillingOperation, error) {
	if tx == nil || task == nil || task.Id <= 0 || task.AsyncBillingReservationID <= 0 ||
		task.BillingProtocolVersion != TaskBillingProtocolVersion || task.Progress != "100%" ||
		(task.Status != "SUCCESS" && task.Status != "FAILURE") {
		return nil, ErrMidjourneyBillingOperationInvariant
	}
	var existing MidjourneyBillingOperation
	query := tx.Where("midjourney_id = ?", task.Id).Limit(1).Find(&existing)
	if query.Error != nil {
		return nil, query.Error
	}
	if query.RowsAffected > 0 {
		if existing.ReservationID != task.AsyncBillingReservationID || existing.UserID != task.UserId {
			return nil, ErrMidjourneyBillingOperationInvariant
		}
		if _, err := repairMidjourneyTerminalSnapshotTx(tx, &existing, task); err != nil {
			return nil, err
		}
		return &existing, nil
	}

	nowMs := now.UnixMilli()
	billingSource := strings.TrimSpace(task.BillingSource)
	if billingSource == "" {
		billingSource = TaskBillingSourceWallet
	}
	operation := &MidjourneyBillingOperation{
		MidjourneyID: task.Id, ReservationID: task.AsyncBillingReservationID,
		OperationKey:   fmt.Sprintf("midjourney:%d:terminal:v2", task.Id),
		TerminalStatus: task.Status, Kind: TaskBillingOperationKindNoop, State: TaskBillingOperationStateCompleted,
		UserID: task.UserId, ChannelID: task.ChannelId, BillingSource: billingSource,
		SubscriptionID: task.SubscriptionId, TokenID: task.TokenId,
		RefundQuota: 0, ModelName: strings.ToLower(strings.TrimSpace(task.Action)),
		Group: task.Group, Reason: boundedTaskBillingError(task.FailReason),
		CreatedTimeMs: nowMs, UpdatedTimeMs: nowMs, CompletedTimeMs: nowMs,
		LogState: TaskBillingOperationLogNotRequired, LogUpdatedTimeMs: nowMs,
	}
	billingQuota := task.EffectiveBillingQuota()
	if task.Status == "FAILURE" && billingQuota > 0 {
		operation.OperationKey = fmt.Sprintf("midjourney:%d:refund:v2", task.Id)
		operation.Kind = TaskBillingOperationKindRefund
		operation.State = TaskBillingOperationStatePending
		operation.RefundQuota = billingQuota
		operation.CompletedTimeMs = 0
		operation.LogState = TaskBillingOperationLogNotRequired
		operation.LogUpdatedTimeMs = nowMs
	}
	if err := freezeMidjourneyTerminalSnapshot(operation, task); err != nil {
		return nil, err
	}
	if err := tx.Create(operation).Error; err != nil {
		return nil, err
	}
	return operation, nil
}

func createMidjourneySuccessAuditOperationTx(
	tx *gorm.DB,
	task *Midjourney,
	now time.Time,
) (*MidjourneyBillingOperation, error) {
	if tx == nil || task == nil || task.Id <= 0 || task.Status != "SUCCESS" || task.Progress != "100%" {
		return nil, ErrMidjourneyBillingOperationInvariant
	}
	var existing MidjourneyBillingOperation
	query := tx.Where("midjourney_id = ?", task.Id).Limit(1).Find(&existing)
	if query.Error != nil {
		return nil, query.Error
	}
	if query.RowsAffected == 1 {
		if _, err := repairMidjourneyTerminalSnapshotTx(tx, &existing, task); err != nil {
			return nil, err
		}
		return &existing, nil
	}
	nowMs := now.UnixMilli()
	billingSource := strings.TrimSpace(task.BillingSource)
	if billingSource == "" {
		billingSource = TaskBillingSourceWallet
	}
	operation := &MidjourneyBillingOperation{
		MidjourneyID: task.Id, OperationKey: fmt.Sprintf("midjourney:%d:success:legacy:v1", task.Id),
		TerminalStatus: task.Status, Kind: TaskBillingOperationKindNoop, State: TaskBillingOperationStateCompleted,
		UserID: task.UserId, ChannelID: task.ChannelId, BillingSource: billingSource,
		SubscriptionID: task.SubscriptionId, TokenID: task.TokenId,
		ModelName: strings.ToLower(strings.TrimSpace(task.Action)), Group: task.Group,
		Reason:        "legacy success observed; no terminal balance adjustment required",
		CreatedTimeMs: nowMs, UpdatedTimeMs: nowMs, CompletedTimeMs: nowMs,
		LogState: TaskBillingOperationLogNotRequired, LogUpdatedTimeMs: nowMs,
	}
	if err := freezeMidjourneyTerminalSnapshot(operation, task); err != nil {
		return nil, err
	}
	if err := tx.Create(operation).Error; err != nil {
		return nil, err
	}
	return operation, nil
}

func GetMidjourneyBillingOperation(ctx context.Context, operationID int64) (*MidjourneyBillingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if operationID <= 0 {
		return nil, errors.New("Midjourney billing operation id must be positive")
	}
	var operation MidjourneyBillingOperation
	if err := DB.WithContext(ctx).Where("id = ?", operationID).First(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

func GetMidjourneyBillingOperationByTaskID(ctx context.Context, taskID int) (*MidjourneyBillingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if taskID <= 0 {
		return nil, errors.New("Midjourney task id must be positive")
	}
	var operation MidjourneyBillingOperation
	if err := DB.WithContext(ctx).Where("midjourney_id = ?", taskID).First(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

func HasPendingMidjourneyBillingOperations() bool {
	return hasPendingMidjourneyBillingOperationsAt(time.Now())
}

func hasPendingMidjourneyBillingOperationsAt(now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	var id int64
	err := DB.Model(&MidjourneyBillingOperation{}).
		Where("(state = ? AND next_retry_time_ms <= ?) OR (state = ? AND lease_until_ms <= ?) OR "+
			"(state = ? AND ((log_state IN ? AND log_next_retry_ms <= ?) OR (log_state = ? AND log_lease_until_ms <= ?)))",
			TaskBillingOperationStatePending, nowMs,
			TaskBillingOperationStateRunning, nowMs,
			TaskBillingOperationStateCompleted,
			[]string{"", TaskBillingOperationLogPending, TaskBillingOperationLogFailed}, nowMs,
			TaskBillingOperationLogWriting, nowMs).
		Limit(1).Pluck("id", &id).Error
	return err == nil && id != 0
}

// FinalizeMidjourneyFailureWithOperation atomically persists the terminal
// failure and its durable refund intent. Quota is intentionally retained on
// the task until the refund transaction completes, eliminating the historical
// crash window between quota=0 and the external refund side effect.
func FinalizeMidjourneyFailureWithOperation(
	ctx context.Context,
	observed *Midjourney,
	fromStatus string,
	modelName string,
	now time.Time,
) (*MidjourneyFailureFinalization, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if observed == nil || observed.Id <= 0 || observed.Status != "FAILURE" || observed.Progress != "100%" {
		return nil, ErrMidjourneyIdentityInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	safeFailureReason := boundedTaskBillingError(observed.FailReason)

	result := &MidjourneyFailureFinalization{}
	casLost := false
	legacyCreateAttempted := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Midjourney
		if err := lockForUpdate(tx).Where("id = ?", observed.Id).First(&current).Error; err != nil {
			return err
		}
		var existingOperation MidjourneyBillingOperation
		existing := tx.Where("midjourney_id = ?", current.Id).Limit(1).Find(&existingOperation)
		if existing.Error != nil {
			return existing.Error
		}
		if existing.RowsAffected == 1 {
			if existingOperation.UserID != current.UserId || existingOperation.ChannelID != current.ChannelId {
				return ErrMidjourneyBillingOperationInvariant
			}
			if current.BillingProtocolVersion == TaskBillingProtocolVersion {
				if current.AsyncBillingReservationID <= 0 ||
					existingOperation.ReservationID != current.AsyncBillingReservationID {
					return ErrMidjourneyBillingOperationInvariant
				}
				if current.Status == "FAILURE" && current.Progress == "100%" && current.EffectiveBillingQuota() == 0 {
					if err := CompleteAsyncBillingReservationTx(
						tx, current.AsyncBillingReservationID, current.UserId, 0, 0, now,
					); err != nil {
						return err
					}
				}
			}
			if _, err := repairMidjourneyTerminalSnapshotTx(tx, &existingOperation, &current); err != nil {
				return err
			}
			result.Task = current
			result.Operation = existingOperation
			result.ObservationMatched = existingOperation.Kind == TaskBillingOperationKindRefund || current.Status == "FAILURE"
			return nil
		}

		if current.Progress == "100%" {
			var operation MidjourneyBillingOperation
			if err := tx.Where("midjourney_id = ?", current.Id).First(&operation).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if current.BillingProtocolVersion == TaskBillingProtocolVersion {
					billingQuota := current.EffectiveBillingQuota()
					if current.Status == "SUCCESS" {
						if err := CompleteAsyncBillingReservationTx(
							tx, current.AsyncBillingReservationID, current.UserId, billingQuota, billingQuota, now,
						); err != nil {
							return err
						}
					} else if billingQuota == 0 {
						if err := CompleteAsyncBillingReservationTx(
							tx, current.AsyncBillingReservationID, current.UserId, 0, 0, now,
						); err != nil {
							return err
						}
					}
					repaired, repairErr := createAcceptedMidjourneyTerminalOperationTx(tx, &current, now)
					if repairErr != nil {
						return repairErr
					}
					operation = *repaired
					result.Task = current
					result.Operation = operation
					result.ObservationMatched = current.Status == "FAILURE"
					return nil
				}
				billingSource := strings.TrimSpace(current.BillingSource)
				if billingSource == "" {
					billingSource = TaskBillingSourceWallet
				}
				nowMs := now.UnixMilli()
				lastError := ""
				if current.BillingProtocolVersion == TaskBillingHistoricalProtocolVersion {
					lastError = "historical Midjourney terminal charge state is ambiguous; charge retained"
				}
				operation = MidjourneyBillingOperation{
					MidjourneyID:     current.Id,
					OperationKey:     fmt.Sprintf("midjourney:%d:refund:legacy:v1", current.Id),
					TerminalStatus:   current.Status,
					Kind:             TaskBillingOperationKindNoop,
					State:            TaskBillingOperationStateCompleted,
					UserID:           current.UserId,
					ChannelID:        current.ChannelId,
					BillingSource:    billingSource,
					SubscriptionID:   current.SubscriptionId,
					TokenID:          current.TokenId,
					ModelName:        modelName,
					Group:            current.Group,
					Reason:           boundedTaskBillingError(current.FailReason),
					CreatedTimeMs:    nowMs,
					UpdatedTimeMs:    nowMs,
					CompletedTimeMs:  nowMs,
					LogState:         TaskBillingOperationLogNotRequired,
					LogUpdatedTimeMs: nowMs,
					LastError:        lastError,
				}
				if err := freezeMidjourneyTerminalSnapshot(&operation, &current); err != nil {
					return err
				}
				legacyCreateAttempted = true
				if err := tx.Create(&operation).Error; err != nil {
					return err
				}
			}
			result.Task = current
			result.Operation = operation
			result.ObservationMatched = current.Status == "FAILURE"
			return nil
		}
		// The terminal observation is authoritative even if a concurrent poll
		// advanced one non-terminal status since the caller's read. Locking the
		// current row and guarding the UPDATE with its latest status prevents a
		// stale failure from being dropped without a durable settlement intent.
		expectedStatus := current.Status
		historicalChargeAmbiguous := current.BillingProtocolVersion == TaskBillingHistoricalProtocolVersion
		durableBilling := current.BillingProtocolVersion >= TaskBillingLegacyProtocolVersion &&
			current.BillingProtocolVersion <= TaskBillingProtocolVersion
		reservationBilling := current.BillingProtocolVersion == TaskBillingProtocolVersion
		billingQuota := current.EffectiveBillingQuota()
		billingSource := strings.TrimSpace(current.BillingSource)
		if billingSource == "" {
			billingSource = TaskBillingSourceWallet
		}
		if durableBilling && (billingQuota < 0 || billingQuota > common.MaxQuota || current.TokenId < 0) {
			return ErrMidjourneyBillingOperationInvariant
		}
		if reservationBilling && current.AsyncBillingReservationID <= 0 {
			return ErrMidjourneyBillingOperationInvariant
		}
		if durableBilling && billingQuota > 0 {
			switch billingSource {
			case TaskBillingSourceWallet:
			case TaskBillingSourceSubscription:
				if current.SubscriptionId <= 0 {
					return ErrMidjourneyBillingOperationInvariant
				}
			default:
				return ErrMidjourneyBillingOperationInvariant
			}
		}

		updates := map[string]any{
			"code":        observed.Code,
			"prompt_en":   observed.PromptEn,
			"description": observed.Description,
			"state":       observed.State,
			"submit_time": observed.SubmitTime,
			"start_time":  observed.StartTime,
			"finish_time": observed.FinishTime,
			"image_url":   observed.ImageUrl,
			"video_url":   observed.VideoUrl,
			"video_urls":  observed.VideoUrls,
			"status":      "FAILURE",
			"progress":    "100%",
			"fail_reason": safeFailureReason,
			"buttons":     observed.Buttons,
			"properties":  observed.Properties,
		}
		updated := tx.Model(&Midjourney{}).
			Where("id = ? AND status = ? AND progress != ?", current.Id, expectedStatus, "100%").
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			casLost = true
			return nil
		}
		if reservationBilling && billingQuota == 0 {
			if err := CompleteAsyncBillingReservationTx(
				tx, current.AsyncBillingReservationID, current.UserId, 0, 0, now,
			); err != nil {
				return err
			}
		}

		nowMs := now.UnixMilli()
		kind := TaskBillingOperationKindRefund
		state := TaskBillingOperationStatePending
		logState := TaskBillingOperationLogPending
		completedTime := int64(0)
		operationKey := fmt.Sprintf("midjourney:%d:refund:v1", current.Id)
		if reservationBilling {
			operationKey = fmt.Sprintf("midjourney:%d:refund:v2", current.Id)
		}
		lastError := ""
		if !durableBilling || billingQuota == 0 || historicalChargeAmbiguous {
			kind = TaskBillingOperationKindNoop
			state = TaskBillingOperationStateCompleted
			logState = TaskBillingOperationLogNotRequired
			completedTime = nowMs
			if historicalChargeAmbiguous {
				operationKey = fmt.Sprintf("midjourney:%d:terminal:historical:v0", current.Id)
				lastError = "historical Midjourney charge state is ambiguous; charge retained for manual audit"
			} else if !durableBilling {
				operationKey = fmt.Sprintf("midjourney:%d:refund:legacy:v2", current.Id)
			}
		}
		refundQuota := billingQuota
		if kind == TaskBillingOperationKindNoop {
			refundQuota = 0
		}
		operation := MidjourneyBillingOperation{
			MidjourneyID:    current.Id,
			ReservationID:   current.AsyncBillingReservationID,
			OperationKey:    operationKey,
			TerminalStatus:  "FAILURE",
			Kind:            kind,
			State:           state,
			UserID:          current.UserId,
			ChannelID:       current.ChannelId,
			BillingSource:   billingSource,
			SubscriptionID:  current.SubscriptionId,
			TokenID:         current.TokenId,
			RefundQuota:     refundQuota,
			ModelName:       modelName,
			Group:           current.Group,
			Reason:          safeFailureReason,
			CreatedTimeMs:   nowMs,
			UpdatedTimeMs:   nowMs,
			CompletedTimeMs: completedTime,
			LogState:        logState,
			LastError:       lastError,
		}
		if reservationBilling {
			operation.LogState = TaskBillingOperationLogNotRequired
			operation.LogUpdatedTimeMs = nowMs
		}
		if operation.LogState == TaskBillingOperationLogPending {
			username, tokenName, err := snapshotBillingLogNames(tx, operation.UserID, operation.TokenID)
			if err != nil {
				return err
			}
			otherJSON, err := common.Marshal(map[string]interface{}{
				"is_task":            true,
				"task_kind":          "midjourney",
				"billing_operation":  operation.OperationKey,
				"billing_source":     operation.BillingSource,
				"midjourney_task_id": operation.MidjourneyID,
				"reason":             operation.Reason,
			})
			if err != nil {
				return err
			}
			payload, payloadHash, protocol, err := freezeBillingLogPayload(operation.OperationKey, &Log{
				UserId:    operation.UserID,
				CreatedAt: now.Unix(),
				Type:      LogTypeRefund,
				Content:   "Midjourney terminal task refund",
				Username:  username,
				TokenName: tokenName,
				ModelName: operation.ModelName,
				Quota:     operation.RefundQuota,
				ChannelId: operation.ChannelID,
				TokenId:   operation.TokenID,
				Group:     operation.Group,
				Other:     string(otherJSON),
			})
			if err != nil {
				return err
			}
			operation.LogPayload = payload
			operation.LogPayloadHash = payloadHash
			operation.LogPayloadProtocol = protocol
		}
		terminalSnapshotTask := current
		terminalSnapshotTask.Code = observed.Code
		terminalSnapshotTask.PromptEn = observed.PromptEn
		terminalSnapshotTask.Description = observed.Description
		terminalSnapshotTask.State = observed.State
		terminalSnapshotTask.SubmitTime = observed.SubmitTime
		terminalSnapshotTask.StartTime = observed.StartTime
		terminalSnapshotTask.FinishTime = observed.FinishTime
		terminalSnapshotTask.ImageUrl = observed.ImageUrl
		terminalSnapshotTask.VideoUrl = observed.VideoUrl
		terminalSnapshotTask.VideoUrls = observed.VideoUrls
		terminalSnapshotTask.Status = "FAILURE"
		terminalSnapshotTask.Progress = "100%"
		terminalSnapshotTask.FailReason = safeFailureReason
		terminalSnapshotTask.Buttons = observed.Buttons
		terminalSnapshotTask.Properties = observed.Properties
		if err := freezeMidjourneyTerminalSnapshot(&operation, &terminalSnapshotTask); err != nil {
			return err
		}
		if err := tx.Create(&operation).Error; err != nil {
			return err
		}
		current.Code = observed.Code
		current.PromptEn = observed.PromptEn
		current.Description = observed.Description
		current.State = observed.State
		current.SubmitTime = observed.SubmitTime
		current.StartTime = observed.StartTime
		current.FinishTime = observed.FinishTime
		current.ImageUrl = observed.ImageUrl
		current.VideoUrl = observed.VideoUrl
		current.VideoUrls = observed.VideoUrls
		current.Status = "FAILURE"
		current.Progress = "100%"
		current.FailReason = safeFailureReason
		current.Buttons = observed.Buttons
		current.Properties = observed.Properties
		result.Task = current
		result.Operation = operation
		result.Transitioned = true
		result.ObservationMatched = true
		return nil
	})
	if err != nil {
		if legacyCreateAttempted {
			operation, operationErr := GetMidjourneyBillingOperationByTaskID(ctx, observed.Id)
			var task Midjourney
			taskErr := DB.WithContext(ctx).Where("id = ?", observed.Id).First(&task).Error
			if operationErr == nil && taskErr == nil {
				return &MidjourneyFailureFinalization{
					Task:               task,
					Operation:          *operation,
					ObservationMatched: task.Status == "FAILURE",
				}, nil
			}
		}
		return nil, err
	}
	if !casLost {
		return result, nil
	}
	var task Midjourney
	if err := DB.WithContext(ctx).Where("id = ?", observed.Id).First(&task).Error; err != nil {
		return nil, err
	}
	operation, operationErr := GetMidjourneyBillingOperationByTaskID(ctx, observed.Id)
	if operationErr != nil && !errors.Is(operationErr, gorm.ErrRecordNotFound) {
		return nil, operationErr
	}
	finalization := &MidjourneyFailureFinalization{
		Task:               task,
		ObservationMatched: task.Status == "FAILURE" && task.Progress == "100%",
	}
	if operation != nil {
		finalization.Operation = *operation
	}
	return finalization, nil
}

func FinalizeMidjourneySuccessWithOperation(
	ctx context.Context,
	observed *Midjourney,
	fromStatus string,
	now time.Time,
) (*MidjourneyFailureFinalization, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if observed == nil || observed.Id <= 0 || observed.Status != "SUCCESS" || observed.Progress != "100%" {
		return nil, ErrMidjourneyIdentityInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	result := &MidjourneyFailureFinalization{}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Midjourney
		if err := lockForUpdate(tx).Where("id = ?", observed.Id).First(&current).Error; err != nil {
			return err
		}
		var existingOperation MidjourneyBillingOperation
		existing := tx.Where("midjourney_id = ?", current.Id).Limit(1).Find(&existingOperation)
		if existing.Error != nil {
			return existing.Error
		}
		if existing.RowsAffected == 1 {
			if existingOperation.UserID != current.UserId || existingOperation.ChannelID != current.ChannelId {
				return ErrMidjourneyBillingOperationInvariant
			}
			if _, err := repairMidjourneyTerminalSnapshotTx(tx, &existingOperation, &current); err != nil {
				return err
			}
			result.Task = current
			result.Operation = existingOperation
			result.ObservationMatched = current.Status == "SUCCESS"
			return nil
		}
		if current.Progress == "100%" {
			result.Task = current
			result.ObservationMatched = current.Status == "SUCCESS"
			if current.BillingProtocolVersion == TaskBillingProtocolVersion {
				var operation MidjourneyBillingOperation
				if err := tx.Where("midjourney_id = ?", current.Id).First(&operation).Error; err != nil {
					if !errors.Is(err, gorm.ErrRecordNotFound) || current.Status != "SUCCESS" ||
						current.AsyncBillingReservationID <= 0 {
						return err
					}
					billingQuota := current.EffectiveBillingQuota()
					if completeErr := CompleteAsyncBillingReservationTx(
						tx, current.AsyncBillingReservationID, current.UserId, billingQuota, billingQuota, now,
					); completeErr != nil {
						return completeErr
					}
					repaired, repairErr := createAcceptedMidjourneyTerminalOperationTx(tx, &current, now)
					if repairErr != nil {
						return repairErr
					}
					operation = *repaired
				}
				result.Operation = operation
			} else if current.Status == "SUCCESS" {
				operation, err := createMidjourneySuccessAuditOperationTx(tx, &current, now)
				if err != nil {
					return err
				}
				result.Operation = *operation
			}
			return nil
		}
		expectedStatus := current.Status
		if strings.TrimSpace(fromStatus) != "" && expectedStatus != fromStatus {
			// The locked current row is authoritative; a newer non-terminal state may
			// still transition to the terminal observation.
			fromStatus = expectedStatus
		}
		updates := map[string]any{
			"code": observed.Code, "prompt_en": observed.PromptEn,
			"description": observed.Description, "state": observed.State,
			"submit_time": observed.SubmitTime, "start_time": observed.StartTime,
			"finish_time": observed.FinishTime, "image_url": observed.ImageUrl,
			"video_url": observed.VideoUrl, "video_urls": observed.VideoUrls,
			"status": "SUCCESS", "progress": "100%", "fail_reason": "",
			"buttons": observed.Buttons, "properties": observed.Properties,
		}
		updated := tx.Model(&Midjourney{}).Where("id = ? AND status = ?", current.Id, expectedStatus).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrMidjourneyBillingOperationInvariant
		}
		current.Code = observed.Code
		current.PromptEn = observed.PromptEn
		current.Description = observed.Description
		current.State = observed.State
		current.SubmitTime = observed.SubmitTime
		current.StartTime = observed.StartTime
		current.FinishTime = observed.FinishTime
		current.ImageUrl = observed.ImageUrl
		current.VideoUrl = observed.VideoUrl
		current.VideoUrls = observed.VideoUrls
		current.Status = "SUCCESS"
		current.Progress = "100%"
		current.FailReason = ""
		current.Buttons = observed.Buttons
		current.Properties = observed.Properties

		if current.BillingProtocolVersion == TaskBillingProtocolVersion {
			if current.AsyncBillingReservationID <= 0 {
				return ErrMidjourneyBillingOperationInvariant
			}
			billingQuota := current.EffectiveBillingQuota()
			if err := CompleteAsyncBillingReservationTx(
				tx, current.AsyncBillingReservationID, current.UserId, billingQuota, billingQuota, now,
			); err != nil {
				return err
			}
			operation, err := createAcceptedMidjourneyTerminalOperationTx(tx, &current, now)
			if err != nil {
				return err
			}
			result.Operation = *operation
		} else {
			operation, err := createMidjourneySuccessAuditOperationTx(tx, &current, now)
			if err != nil {
				return err
			}
			result.Operation = *operation
		}
		result.Task = current
		result.Transitioned = true
		result.ObservationMatched = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func ClaimMidjourneyBillingOperation(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*MidjourneyBillingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if operationID <= 0 || owner == "" || len(owner) > 128 ||
		leaseDuration <= 0 || leaseDuration > maxTaskBillingLeaseDuration {
		return nil, false, ErrMidjourneyBillingOperationNotClaimed
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	claimed := DB.WithContext(ctx).Model(&MidjourneyBillingOperation{}).
		Where("id = ?", operationID).
		Where("((state = ? AND next_retry_time_ms <= ?) OR (state = ? AND lease_until_ms <= ?))",
			TaskBillingOperationStatePending, nowMs, TaskBillingOperationStateRunning, nowMs).
		Updates(map[string]any{
			"state":           TaskBillingOperationStateRunning,
			"lease_owner":     owner,
			"lease_until_ms":  now.Add(leaseDuration).UnixMilli(),
			"attempts":        gorm.Expr("attempts + ?", 1),
			"updated_time_ms": nowMs,
		})
	if claimed.Error != nil {
		return nil, false, claimed.Error
	}
	operation, err := GetMidjourneyBillingOperation(ctx, operationID)
	if err != nil {
		return nil, false, err
	}
	return operation, claimed.RowsAffected == 1, nil
}

func ClaimNextMidjourneyBillingOperation(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*MidjourneyBillingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	var operationIDs []int64
	if err := DB.WithContext(ctx).Model(&MidjourneyBillingOperation{}).Select("id").
		Where("((state = ? AND next_retry_time_ms <= ?) OR (state = ? AND lease_until_ms <= ?))",
			TaskBillingOperationStatePending, nowMs, TaskBillingOperationStateRunning, nowMs).
		Order("id ASC").Limit(32).Pluck("id", &operationIDs).Error; err != nil {
		return nil, false, err
	}
	for _, operationID := range operationIDs {
		operation, claimed, err := ClaimMidjourneyBillingOperation(ctx, operationID, owner, now, leaseDuration)
		if err != nil {
			return nil, false, err
		}
		if claimed {
			return operation, true, nil
		}
	}
	return nil, false, nil
}

func enqueueMidjourneyTerminalBillingProjectionTx(
	tx *gorm.DB,
	operation *MidjourneyBillingOperation,
	task *Midjourney,
	now time.Time,
) error {
	if tx == nil || operation == nil || task == nil || operation.ID <= 0 || operation.ReservationID <= 0 ||
		operation.MidjourneyID != task.Id || operation.Kind != TaskBillingOperationKindRefund ||
		operation.RefundQuota <= 0 || operation.RefundQuota > common.MaxQuota {
		return ErrMidjourneyBillingOperationInvariant
	}
	audit := AsyncBillingAcceptedAuditSnapshot{
		RequestPath: "/mj/task", Action: task.Action, Content: "Midjourney asynchronous billing",
		OriginModelName: operation.ModelName, Group: operation.Group, NodeName: task.NodeName,
	}
	if strings.TrimSpace(task.BillingAuditPayload) != "" {
		var frozen AsyncBillingAcceptedAuditSnapshot
		if err := common.UnmarshalJsonStr(task.BillingAuditPayload, &frozen); err == nil &&
			validateAsyncBillingAuditSnapshot(frozen) == nil {
			audit = frozen
		}
	}
	if audit.Action == "" {
		audit.Action = "midjourney"
	}
	if audit.OriginModelName == "" {
		audit.OriginModelName = "midjourney"
	}
	other := asyncBillingAcceptedOther(audit)
	other["is_task"] = true
	other["task_kind"] = AsyncBillingKindMidjourney
	other["billing_operation"] = operation.OperationKey
	other["billing_source"] = operation.BillingSource
	other["midjourney_task_id"] = operation.MidjourneyID
	other["reason"] = operation.Reason
	otherJSON, err := common.Marshal(other)
	if err != nil {
		return err
	}
	username, tokenName, err := snapshotBillingLogNames(tx, operation.UserID, operation.TokenID)
	if err != nil {
		return err
	}
	entry := &Log{
		UserId: operation.UserID, CreatedAt: now.Unix(), Type: LogTypeRefund,
		Content: "Midjourney terminal task refund", Username: username, TokenName: tokenName,
		ModelName: operation.ModelName, Quota: operation.RefundQuota,
		ChannelId: operation.ChannelID, TokenId: operation.TokenID, Group: operation.Group,
		Ip: audit.ClientIP, RequestId: audit.RequestID,
		UpstreamRequestId: task.GetUpstreamTaskID(), Other: string(otherJSON),
	}
	if _, _, err := CreateBillingLogProjectionTx(tx, BillingLogProjectionSpec{
		OperationKey: operation.OperationKey, Kind: BillingLogProjectionKindMidjourneyTerminal,
		ReferenceID: operation.ID, Required: true, Entry: entry,
	}, now); err != nil {
		return err
	}
	return nil
}

func CompleteMidjourneyBillingOperation(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
) (*MidjourneyBillingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if operationID <= 0 || owner == "" {
		return nil, ErrMidjourneyBillingOperationNotClaimed
	}
	if now.IsZero() {
		now = time.Now()
	}
	var completed MidjourneyBillingOperation
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var operation MidjourneyBillingOperation
		if err := lockForUpdate(tx).Where("id = ?", operationID).First(&operation).Error; err != nil {
			return err
		}
		if operation.State == TaskBillingOperationStateCompleted {
			var task Midjourney
			if err := lockForUpdate(tx).Where("id = ?", operation.MidjourneyID).First(&task).Error; err != nil {
				return err
			}
			if _, err := repairMidjourneyTerminalSnapshotTx(tx, &operation, &task); err != nil {
				return err
			}
			completed = operation
			return nil
		}
		if operation.State != TaskBillingOperationStateRunning || operation.LeaseOwner != owner {
			return ErrMidjourneyBillingOperationNotClaimed
		}
		if operation.LeaseUntilMs <= now.UnixMilli() {
			return ErrMidjourneyBillingOperationLeaseExpired
		}
		if operation.Kind != TaskBillingOperationKindRefund || operation.RefundQuota <= 0 || operation.RefundQuota > common.MaxQuota {
			return ErrMidjourneyBillingOperationInvariant
		}
		var task Midjourney
		if err := lockForUpdate(tx).Where("id = ?", operation.MidjourneyID).First(&task).Error; err != nil {
			return err
		}
		if _, err := repairMidjourneyTerminalSnapshotTx(tx, &operation, &task); err != nil {
			return err
		}
		if task.UserId != operation.UserID || task.ChannelId != operation.ChannelID ||
			task.Status != "FAILURE" || task.Progress != "100%" || task.EffectiveBillingQuota() != operation.RefundQuota {
			return ErrMidjourneyBillingOperationInvariant
		}
		if operation.ReservationID > 0 && task.AsyncBillingReservationID != operation.ReservationID {
			return ErrMidjourneyBillingOperationInvariant
		}

		if operation.ReservationID > 0 {
			if err := CompleteAsyncBillingReservationTx(
				tx, operation.ReservationID, operation.UserID, operation.RefundQuota, 0, now,
			); err != nil {
				return err
			}
		} else {
			switch operation.BillingSource {
			case TaskBillingSourceWallet:
				var user User
				if err := lockForUpdate(tx.Unscoped()).Where("id = ?", operation.UserID).First(&user).Error; err != nil {
					return err
				}
				newQuota := int64(user.Quota) + int64(operation.RefundQuota)
				if newQuota > int64(common.MaxQuota) {
					return ErrMidjourneyBillingOperationInvariant
				}
				updated := tx.Unscoped().Model(&User{}).Where("id = ?", operation.UserID).
					Update("quota", gorm.Expr("quota + ?", operation.RefundQuota))
				if updated.Error != nil || updated.RowsAffected != 1 {
					if updated.Error != nil {
						return updated.Error
					}
					return ErrMidjourneyBillingOperationInvariant
				}
			case TaskBillingSourceSubscription:
				var subscription UserSubscription
				if err := lockForUpdate(tx).Where("id = ?", operation.SubscriptionID).First(&subscription).Error; err != nil {
					return err
				}
				if subscription.UserId != operation.UserID || subscription.AmountUsed < int64(operation.RefundQuota) {
					return ErrMidjourneyBillingOperationInvariant
				}
				updated := tx.Model(&UserSubscription{}).Where("id = ?", operation.SubscriptionID).Updates(map[string]any{
					"amount_used": gorm.Expr("amount_used - ?", operation.RefundQuota),
					"updated_at":  now.Unix(),
				})
				if updated.Error != nil || updated.RowsAffected != 1 {
					if updated.Error != nil {
						return updated.Error
					}
					return ErrMidjourneyBillingOperationInvariant
				}
			default:
				return ErrMidjourneyBillingOperationInvariant
			}

			if operation.TokenID > 0 {
				var token Token
				if err := lockForUpdate(tx.Unscoped()).Where("id = ?", operation.TokenID).First(&token).Error; err != nil {
					return err
				}
				if token.UserId != operation.UserID || token.UsedQuota < operation.RefundQuota ||
					int64(token.RemainQuota)+int64(operation.RefundQuota) > int64(common.MaxQuota) {
					return ErrMidjourneyBillingOperationInvariant
				}
				updated := tx.Unscoped().Model(&Token{}).Where("id = ?", operation.TokenID).Updates(map[string]any{
					"remain_quota":  gorm.Expr("remain_quota + ?", operation.RefundQuota),
					"used_quota":    gorm.Expr("used_quota - ?", operation.RefundQuota),
					"accessed_time": now.Unix(),
				})
				if updated.Error != nil || updated.RowsAffected != 1 {
					if updated.Error != nil {
						return updated.Error
					}
					return ErrMidjourneyBillingOperationInvariant
				}
			}
		}

		quotaColumn := "quota"
		if task.BillingProtocolVersion == TaskBillingProtocolVersion {
			quotaColumn = "durable_quota"
		}
		taskUpdate := tx.Model(&Midjourney{}).
			Where("id = ? AND status = ? AND progress = ? AND "+quotaColumn+" = ?", task.Id, "FAILURE", "100%", operation.RefundQuota).
			Update(quotaColumn, 0)
		if taskUpdate.Error != nil || taskUpdate.RowsAffected != 1 {
			if taskUpdate.Error != nil {
				return taskUpdate.Error
			}
			return ErrMidjourneyBillingOperationInvariant
		}
		if operation.ReservationID > 0 {
			if err := enqueueMidjourneyTerminalBillingProjectionTx(tx, &operation, &task, now); err != nil {
				return err
			}
		}
		nowMs := now.UnixMilli()
		operationUpdate := tx.Model(&MidjourneyBillingOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, TaskBillingOperationStateRunning, owner).
			Updates(map[string]any{
				"state":             TaskBillingOperationStateCompleted,
				"lease_owner":       "",
				"lease_until_ms":    0,
				"last_error":        "",
				"updated_time_ms":   nowMs,
				"completed_time_ms": nowMs,
			})
		if operationUpdate.Error != nil || operationUpdate.RowsAffected != 1 {
			if operationUpdate.Error != nil {
				return operationUpdate.Error
			}
			return ErrMidjourneyBillingOperationNotClaimed
		}
		operation.State = TaskBillingOperationStateCompleted
		operation.LeaseOwner = ""
		operation.LeaseUntilMs = 0
		operation.LastError = ""
		operation.UpdatedTimeMs = nowMs
		operation.CompletedTimeMs = nowMs
		completed = operation
		return nil
	})
	if err != nil {
		return nil, err
	}
	if completed.ReservationID > 0 {
		if cacheErr := SyncAsyncBillingReservationCaches(ctx, completed.ReservationID, now); cacheErr != nil {
			common.SysError(fmt.Sprintf("sync terminal Midjourney billing cache failed: reservation=%d error=%s",
				completed.ReservationID, boundedTaskBillingError(cacheErr.Error())))
		}
	}
	return &completed, nil
}

func RetryMidjourneyBillingOperation(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
	nextRetry time.Time,
	failure string,
) (*MidjourneyBillingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	if nextRetry.IsZero() || nextRetry.Before(now) {
		nextRetry = now
	}
	updated := DB.WithContext(ctx).Model(&MidjourneyBillingOperation{}).
		Where("id = ? AND state = ? AND lease_owner = ?", operationID, TaskBillingOperationStateRunning, owner).
		Updates(map[string]any{
			"state":              TaskBillingOperationStatePending,
			"lease_owner":        "",
			"lease_until_ms":     0,
			"next_retry_time_ms": nextRetry.UnixMilli(),
			"last_error":         boundedTaskBillingError(failure),
			"updated_time_ms":    now.UnixMilli(),
		})
	if updated.Error != nil {
		return nil, updated.Error
	}
	if updated.RowsAffected != 1 {
		return nil, ErrMidjourneyBillingOperationNotClaimed
	}
	return GetMidjourneyBillingOperation(ctx, operationID)
}

func ClaimMidjourneyBillingOperationLog(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*MidjourneyBillingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if operationID <= 0 || owner == "" || len(owner) > 128 ||
		leaseDuration <= 0 || leaseDuration > maxTaskBillingLeaseDuration {
		return nil, false, ErrMidjourneyBillingOperationNotClaimed
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	claimed := DB.WithContext(ctx).Model(&MidjourneyBillingOperation{}).
		Where("id = ? AND state = ?", operationID, TaskBillingOperationStateCompleted).
		Where("((log_state IN ? AND log_next_retry_ms <= ?) OR (log_state = ? AND log_lease_until_ms <= ?))",
			[]string{"", TaskBillingOperationLogPending, TaskBillingOperationLogFailed},
			nowMs, TaskBillingOperationLogWriting, nowMs).
		Updates(map[string]any{
			"log_state":           TaskBillingOperationLogWriting,
			"log_lease_owner":     owner,
			"log_lease_until_ms":  now.Add(leaseDuration).UnixMilli(),
			"log_attempts":        gorm.Expr("log_attempts + ?", 1),
			"log_updated_time_ms": nowMs,
		})
	if claimed.Error != nil {
		return nil, false, claimed.Error
	}
	operation, err := GetMidjourneyBillingOperation(ctx, operationID)
	if err != nil {
		return nil, false, err
	}
	return operation, claimed.RowsAffected == 1, nil
}

func ClaimNextMidjourneyBillingOperationLog(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*MidjourneyBillingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	var operationIDs []int64
	if err := DB.WithContext(ctx).Model(&MidjourneyBillingOperation{}).Select("id").
		Where("state = ?", TaskBillingOperationStateCompleted).
		Where("((log_state IN ? AND log_next_retry_ms <= ?) OR (log_state = ? AND log_lease_until_ms <= ?))",
			[]string{"", TaskBillingOperationLogPending, TaskBillingOperationLogFailed},
			nowMs, TaskBillingOperationLogWriting, nowMs).
		Order("id ASC").Limit(32).Pluck("id", &operationIDs).Error; err != nil {
		return nil, false, err
	}
	for _, operationID := range operationIDs {
		operation, claimed, err := ClaimMidjourneyBillingOperationLog(ctx, operationID, owner, now, leaseDuration)
		if err != nil {
			return nil, false, err
		}
		if claimed {
			return operation, true, nil
		}
	}
	return nil, false, nil
}

func RecordMidjourneyBillingOperationLog(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) error {
	operation, claimed, err := ClaimMidjourneyBillingOperationLog(ctx, operationID, owner, now, leaseDuration)
	if err != nil {
		return err
	}
	if !claimed {
		if operation.State == TaskBillingOperationStateCompleted &&
			(operation.LogState == TaskBillingOperationLogWritten || operation.LogState == TaskBillingOperationLogNotRequired) {
			return nil
		}
		return ErrMidjourneyBillingOperationNotClaimed
	}
	return recordClaimedMidjourneyBillingOperationLog(ctx, operation, owner, now)
}

func RecordClaimedMidjourneyBillingOperationLog(ctx context.Context, operationID int64, owner string, now time.Time) error {
	operation, err := GetMidjourneyBillingOperation(ctx, operationID)
	if err != nil {
		return err
	}
	return recordClaimedMidjourneyBillingOperationLog(ctx, operation, owner, now)
}

func recordClaimedMidjourneyBillingOperationLog(
	ctx context.Context,
	operation *MidjourneyBillingOperation,
	owner string,
	now time.Time,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if operation == nil || operation.State != TaskBillingOperationStateCompleted ||
		operation.LogState != TaskBillingOperationLogWriting || operation.LogLeaseOwner != owner {
		return ErrMidjourneyBillingOperationNotClaimed
	}
	if now.IsZero() {
		now = time.Now()
	}
	if operation.LogLeaseUntilMs <= now.UnixMilli() {
		return ErrMidjourneyBillingOperationLeaseExpired
	}
	if operation.RefundQuota > 0 {
		if cacheErr := errors.Join(
			InvalidateUserCache(operation.UserID),
			InvalidateTokenCacheByID(operation.TokenID, operation.UserID),
		); cacheErr != nil {
			markErr := finishMidjourneyBillingOperationLog(ctx, operation.ID, owner, TaskBillingOperationLogFailed, cacheErr.Error(), now)
			return errors.Join(cacheErr, markErr)
		}
	}
	if operation.Kind == TaskBillingOperationKindNoop || operation.RefundQuota == 0 {
		return finishMidjourneyBillingOperationLog(ctx, operation.ID, owner, TaskBillingOperationLogNotRequired, "", now)
	}
	if LOG_DB == nil {
		logErr := errors.New("Midjourney billing log database is unavailable")
		markErr := finishMidjourneyBillingOperationLog(ctx, operation.ID, owner, TaskBillingOperationLogFailed, logErr.Error(), now)
		return errors.Join(logErr, markErr)
	}
	if err := writeFrozenBillingLog(ctx, operation.OperationKey, operation.LogPayload,
		operation.LogPayloadHash, operation.LogPayloadProtocol); err != nil {
		markErr := finishMidjourneyBillingOperationLog(ctx, operation.ID, owner, TaskBillingOperationLogFailed, err.Error(), now)
		return errors.Join(err, markErr)
	}
	return finishMidjourneyBillingOperationLog(ctx, operation.ID, owner, TaskBillingOperationLogWritten, "", now)
}

func finishMidjourneyBillingOperationLog(
	ctx context.Context,
	operationID int64,
	owner string,
	state string,
	failure string,
	now time.Time,
) error {
	updates := map[string]any{
		"log_state":           state,
		"log_last_error":      boundedTaskBillingError(failure),
		"log_updated_time_ms": now.UnixMilli(),
		"log_lease_owner":     "",
		"log_lease_until_ms":  0,
		"log_next_retry_ms":   0,
	}
	if state == TaskBillingOperationLogFailed {
		updates["log_next_retry_ms"] = now.Add(taskBillingLogRetryDelay).UnixMilli()
	}
	updated := DB.WithContext(ctx).Model(&MidjourneyBillingOperation{}).
		Where("id = ? AND state = ? AND log_state = ? AND log_lease_owner = ?",
			operationID, TaskBillingOperationStateCompleted, TaskBillingOperationLogWriting, owner).
		Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrMidjourneyBillingOperationNotClaimed
	}
	return nil
}
