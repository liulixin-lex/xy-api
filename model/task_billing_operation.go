package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	TaskBillingOperationKindSettle = "settle"
	TaskBillingOperationKindRefund = "refund"
	TaskBillingOperationKindNoop   = "noop"

	TaskBillingOperationStatePending      = "pending"
	TaskBillingOperationStateRunning      = "running"
	TaskBillingOperationStateCompleted    = "completed"
	TaskBillingOperationStateManualReview = "manual_review"

	TaskBillingOperationLogPending     = "pending"
	TaskBillingOperationLogWriting     = "writing"
	TaskBillingOperationLogWritten     = "written"
	TaskBillingOperationLogFailed      = "failed"
	TaskBillingOperationLogNotRequired = "not_required"

	TaskBillingSourceWallet       = "wallet"
	TaskBillingSourceSubscription = "subscription"

	maxTaskBillingLeaseDuration  = 15 * time.Minute
	maxTaskBillingLastErrorBytes = 1024
	taskBillingLogRetryDelay     = 15 * time.Second
)

var (
	ErrTaskBillingOperationMissing           = errors.New("task billing operation is missing")
	ErrTaskBillingOperationInvariant         = errors.New("task billing operation invariant violation")
	ErrTaskBillingOperationNotClaimed        = errors.New("task billing operation is not claimed by this worker")
	ErrTaskBillingOperationLeaseExpired      = errors.New("task billing operation lease has expired")
	ErrTaskBillingOperationInsufficientQuota = errors.New("task billing operation has insufficient quota")
)

// TaskBillingOperation is the durable, exactly-once accounting intent created
// with a task's terminal transition. TaskID is the Task primary key, not the
// public/upstream task identifier.
type TaskBillingOperation struct {
	ID             int64      `json:"id" gorm:"primaryKey;autoIncrement"`
	TaskID         int64      `json:"task_id" gorm:"not null;uniqueIndex:uidx_task_billing_operation_task"`
	ReservationID  int64      `json:"reservation_id" gorm:"index"`
	OperationKey   string     `json:"operation_key" gorm:"type:varchar(191);not null;uniqueIndex:uidx_task_billing_operation_key"`
	TerminalStatus TaskStatus `json:"terminal_status" gorm:"type:varchar(20);not null"`
	Kind           string     `json:"kind" gorm:"type:varchar(20);not null"`
	State          string     `json:"state" gorm:"type:varchar(20);not null;index:idx_task_billing_pending,priority:1;index:idx_task_billing_lease,priority:1"`

	UserID         int    `json:"user_id" gorm:"not null;index"`
	ChannelID      int    `json:"channel_id" gorm:"not null;index"`
	BillingSource  string `json:"billing_source" gorm:"type:varchar(20);not null"`
	SubscriptionID int    `json:"subscription_id"`
	TokenID        int    `json:"token_id"`

	PreConsumedQuota int `json:"pre_consumed_quota" gorm:"not null"`
	TargetQuota      int `json:"target_quota" gorm:"not null"`
	QuotaDelta       int `json:"quota_delta" gorm:"not null"`

	QuotaClampOp            string `json:"quota_clamp_op,omitempty" gorm:"type:varchar(32)"`
	QuotaClampKind          string `json:"quota_clamp_kind,omitempty" gorm:"type:varchar(32)"`
	QuotaClampOriginal      string `json:"quota_clamp_original,omitempty" gorm:"type:varchar(64)"`
	QuotaClampValue         int    `json:"quota_clamp_value,omitempty"`
	TerminalPayloadProtocol int    `json:"terminal_payload_protocol" gorm:"not null"`
	TerminalPayloadHash     string `json:"terminal_payload_hash,omitempty" gorm:"type:varchar(64)"`
	TerminalPayload         []byte `json:"-"`

	LeaseOwner      string `json:"lease_owner,omitempty" gorm:"type:varchar(128)"`
	LeaseUntilMs    int64  `json:"lease_until_ms" gorm:"index:idx_task_billing_lease,priority:2"`
	Attempts        int    `json:"attempts" gorm:"not null"`
	NextRetryTimeMs int64  `json:"next_retry_time_ms" gorm:"index:idx_task_billing_pending,priority:2"`
	LastError       string `json:"last_error,omitempty" gorm:"type:varchar(1024)"`

	CreatedTimeMs   int64 `json:"created_time_ms" gorm:"not null"`
	UpdatedTimeMs   int64 `json:"updated_time_ms" gorm:"not null"`
	CompletedTimeMs int64 `json:"completed_time_ms"`

	LogState           string `json:"log_state" gorm:"type:varchar(20);not null;index:idx_task_billing_log_pending,priority:1"`
	LogPayloadProtocol int    `json:"log_payload_protocol" gorm:"not null"`
	LogPayloadHash     string `json:"log_payload_hash,omitempty" gorm:"type:varchar(64)"`
	LogPayload         string `json:"log_payload,omitempty" gorm:"type:text"`
	LogAttempts        int    `json:"log_attempts" gorm:"not null"`
	LogLastError       string `json:"log_last_error,omitempty" gorm:"type:varchar(1024)"`
	LogUpdatedTimeMs   int64  `json:"log_updated_time_ms"`
	LogLeaseOwner      string `json:"log_lease_owner,omitempty" gorm:"type:varchar(128)"`
	LogLeaseUntilMs    int64  `json:"log_lease_until_ms" gorm:"index"`
	LogNextRetryMs     int64  `json:"log_next_retry_ms" gorm:"index:idx_task_billing_log_pending,priority:2"`

	UsageUserOutcome    string `json:"usage_user_outcome,omitempty" gorm:"type:varchar(32)"`
	UsageChannelOutcome string `json:"usage_channel_outcome,omitempty" gorm:"type:varchar(32)"`
	UsageWarning        string `json:"usage_warning,omitempty" gorm:"type:varchar(1024)"`
}

type TaskTerminalUpdate struct {
	TaskID            int64
	TerminalStatus    TaskStatus
	Progress          string
	SubmitTime        int64
	StartTime         int64
	FinishTime        int64
	FailReason        string
	UpstreamResultURL string
	Data              json.RawMessage
}

type TaskBillingOperationPlan struct {
	TargetQuota          int
	QuotaClamp           *common.QuotaClamp
	PreserveChargeReason string
}

type TaskBillingPlanBuilder func(task *Task) (TaskBillingOperationPlan, error)

type TaskBillingFinalization struct {
	Task               Task
	Operation          TaskBillingOperation
	Transitioned       bool
	ObservationMatched bool
}

func GetTaskBillingOperation(ctx context.Context, operationID int64) (*TaskBillingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if operationID <= 0 {
		return nil, errors.New("task billing operation id must be positive")
	}
	var operation TaskBillingOperation
	if err := DB.WithContext(ctx).Where("id = ?", operationID).First(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

func GetTaskBillingOperationByTaskID(ctx context.Context, taskID int64) (*TaskBillingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if taskID <= 0 {
		return nil, errors.New("task id must be positive")
	}
	var operation TaskBillingOperation
	if err := DB.WithContext(ctx).Where("task_id = ?", taskID).First(&operation).Error; err != nil {
		return nil, err
	}
	return &operation, nil
}

// HasRecoverableTaskBillingOperations keeps the background worker enabled while
// financial work or post-commit log materialization can still be recovered.
func HasRecoverableTaskBillingOperations() bool {
	return hasRecoverableTaskBillingOperationsAt(time.Now())
}

func hasRecoverableTaskBillingOperationsAt(now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	var id int64
	err := DB.Model(&TaskBillingOperation{}).Select("id").
		Where("(state = ? AND next_retry_time_ms <= ?) OR (state = ? AND lease_until_ms <= ?) OR "+
			"(state = ? AND ((log_state IN ? AND log_next_retry_ms <= ?) OR (log_state = ? AND log_lease_until_ms <= ?)))",
			TaskBillingOperationStatePending, nowMs,
			TaskBillingOperationStateRunning, nowMs,
			TaskBillingOperationStateCompleted,
			[]string{"", TaskBillingOperationLogPending, TaskBillingOperationLogFailed}, nowMs,
			TaskBillingOperationLogWriting, nowMs).
		Limit(1).Pluck("id", &id).Error
	return err == nil && id > 0
}

// FinalizeTaskWithBillingOperation atomically moves a task to a terminal state
// and creates its single durable accounting operation. The plan is calculated
// from the task row read inside this transaction, so billing never depends on
// a stale pre-read or on mutable completion-time pricing settings.
func FinalizeTaskWithBillingOperation(
	ctx context.Context,
	update TaskTerminalUpdate,
	now time.Time,
	buildPlan TaskBillingPlanBuilder,
) (*TaskBillingFinalization, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if update.TaskID <= 0 {
		return nil, errors.New("task id must be positive")
	}
	if update.TerminalStatus != TaskStatusSuccess && update.TerminalStatus != TaskStatusFailure {
		return nil, errors.New("task billing terminal status must be SUCCESS or FAILURE")
	}
	if buildPlan == nil {
		return nil, errors.New("task billing plan builder is nil")
	}
	if now.IsZero() {
		now = time.Now()
	}

	result := &TaskBillingFinalization{}
	casLost := false
	legacyCreateAttempted := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task Task
		if err := lockForUpdate(tx).Where("id = ?", update.TaskID).First(&task).Error; err != nil {
			return err
		}
		var existingOperation TaskBillingOperation
		existing := tx.Where("task_id = ?", task.ID).Limit(1).Find(&existingOperation)
		if existing.Error != nil {
			return existing.Error
		}
		if existing.RowsAffected == 1 {
			if existingOperation.UserID != task.UserId || existingOperation.ChannelID != task.ChannelId {
				return ErrTaskBillingOperationInvariant
			}
			if _, err := repairTaskTerminalSnapshotTx(tx, &existingOperation, &task, now); err != nil {
				return err
			}
			result.Task = task
			result.Operation = existingOperation
			result.ObservationMatched = existingOperation.TerminalStatus == update.TerminalStatus
			return nil
		}

		if task.Status == TaskStatusSuccess || task.Status == TaskStatusFailure {
			var operation TaskBillingOperation
			if err := tx.Where("task_id = ?", task.ID).First(&operation).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if task.PrivateData.BillingProtocolVersion == TaskBillingProtocolVersion {
					if task.PrivateData.AsyncBillingReservationID <= 0 {
						return ErrTaskBillingOperationInvariant
					}
					preConsumedQuota := task.EffectiveBillingQuota()
					if preConsumedQuota < 0 || preConsumedQuota > common.MaxQuota {
						return ErrTaskBillingOperationInvariant
					}
					plan, planErr := buildPlan(&task)
					if planErr != nil {
						return planErr
					}
					if task.Status == TaskStatusFailure {
						plan.TargetQuota = 0
						plan.QuotaClamp = nil
					}
					if plan.TargetQuota < 0 || plan.TargetQuota > common.MaxQuota {
						return ErrTaskBillingOperationInvariant
					}
					delta64 := int64(plan.TargetQuota) - int64(preConsumedQuota)
					if delta64 < int64(common.MinQuota) || delta64 > int64(common.MaxQuota) {
						return ErrTaskBillingOperationInvariant
					}
					delta := int(delta64)
					kind := TaskBillingOperationKindSettle
					if delta == 0 {
						kind = TaskBillingOperationKindNoop
					} else if task.Status == TaskStatusFailure {
						kind = TaskBillingOperationKindRefund
					}
					nowMs := now.UnixMilli()
					operation = TaskBillingOperation{
						TaskID: task.ID, ReservationID: task.PrivateData.AsyncBillingReservationID,
						OperationKey:   fmt.Sprintf("task:%d:terminal:v2", task.ID),
						TerminalStatus: task.Status, Kind: kind, State: TaskBillingOperationStatePending,
						UserID: task.UserId, ChannelID: task.ChannelId,
						BillingSource:  strings.TrimSpace(task.PrivateData.BillingSource),
						SubscriptionID: task.PrivateData.SubscriptionId, TokenID: task.PrivateData.TokenId,
						PreConsumedQuota: preConsumedQuota, TargetQuota: plan.TargetQuota, QuotaDelta: delta,
						CreatedTimeMs: nowMs, UpdatedTimeMs: nowMs,
						LogState: TaskBillingOperationLogNotRequired, LogUpdatedTimeMs: nowMs,
					}
					if operation.BillingSource == "" {
						operation.BillingSource = TaskBillingSourceWallet
					}
					if plan.QuotaClamp != nil {
						operation.QuotaClampOp = plan.QuotaClamp.Op
						operation.QuotaClampKind = plan.QuotaClamp.Kind
						operation.QuotaClampOriginal = strconv.FormatFloat(plan.QuotaClamp.Original, 'g', -1, 64)
						operation.QuotaClampValue = plan.QuotaClamp.Clamped
					}
					if err := freezeTaskTerminalSnapshot(&operation, &task); err != nil {
						return err
					}
					if err := tx.Create(&operation).Error; err != nil {
						return err
					}
					result.Task = task
					result.Operation = operation
					result.ObservationMatched = task.Status == update.TerminalStatus
					return nil
				}

				// Historical terminal rows predate the durable-operation protocol.
				// Record a completed/noop marker so they remain observable without
				// replaying a charge or refund that may already have happened.
				billingSource := strings.TrimSpace(task.PrivateData.BillingSource)
				if billingSource == "" {
					billingSource = TaskBillingSourceWallet
				}
				nowMs := now.UnixMilli()
				operation = TaskBillingOperation{
					TaskID:           task.ID,
					OperationKey:     fmt.Sprintf("task:%d:terminal:legacy:v1", task.ID),
					TerminalStatus:   task.Status,
					Kind:             TaskBillingOperationKindNoop,
					State:            TaskBillingOperationStateCompleted,
					UserID:           task.UserId,
					ChannelID:        task.ChannelId,
					BillingSource:    billingSource,
					SubscriptionID:   task.PrivateData.SubscriptionId,
					TokenID:          task.PrivateData.TokenId,
					PreConsumedQuota: task.Quota,
					TargetQuota:      task.Quota,
					CompletedTimeMs:  nowMs,
					CreatedTimeMs:    nowMs,
					UpdatedTimeMs:    nowMs,
					LogState:         TaskBillingOperationLogNotRequired,
					LogUpdatedTimeMs: nowMs,
				}
				if err := freezeTaskTerminalSnapshot(&operation, &task); err != nil {
					return err
				}
				legacyCreateAttempted = true
				if err := tx.Create(&operation).Error; err != nil {
					return err
				}
			}
			if operation.TerminalStatus != task.Status || operation.UserID != task.UserId {
				return ErrTaskBillingOperationInvariant
			}
			result.Task = task
			result.Operation = operation
			result.ObservationMatched = task.Status == update.TerminalStatus
			return nil
		}

		durableBilling := SupportsDurableTaskBillingProtocol(task.PrivateData.BillingProtocolVersion)
		reservationBilling := task.PrivateData.BillingProtocolVersion == TaskBillingProtocolVersion
		billingSource := strings.TrimSpace(task.PrivateData.BillingSource)
		if billingSource == "" {
			billingSource = TaskBillingSourceWallet
		}
		preConsumedQuota := task.EffectiveBillingQuota()
		plan := TaskBillingOperationPlan{TargetQuota: preConsumedQuota}
		delta := 0
		kind := TaskBillingOperationKindNoop
		state := TaskBillingOperationStateCompleted
		logState := TaskBillingOperationLogNotRequired
		completedTime := now.UnixMilli()
		operationKey := fmt.Sprintf("task:%d:terminal:legacy:v2", task.ID)
		if durableBilling {
			if reservationBilling && task.PrivateData.AsyncBillingReservationID <= 0 {
				return fmt.Errorf("%w: v2 task has no async billing reservation", ErrTaskBillingOperationInvariant)
			}
			if preConsumedQuota < 0 || preConsumedQuota > common.MaxQuota {
				return fmt.Errorf("%w: invalid pre-consumed quota %d", ErrTaskBillingOperationInvariant, preConsumedQuota)
			}
			if task.PrivateData.TokenId < 0 {
				return fmt.Errorf("%w: invalid token id %d", ErrTaskBillingOperationInvariant, task.PrivateData.TokenId)
			}
			switch billingSource {
			case TaskBillingSourceWallet:
			case TaskBillingSourceSubscription:
				if task.PrivateData.SubscriptionId <= 0 {
					return fmt.Errorf("%w: subscription billing source has no subscription id", ErrTaskBillingOperationInvariant)
				}
			default:
				return fmt.Errorf("%w: unsupported billing source %q", ErrTaskBillingOperationInvariant, billingSource)
			}

			var err error
			plan, err = buildPlan(&task)
			if err != nil {
				return err
			}
			preserveChargeReason := strings.TrimSpace(plan.PreserveChargeReason)
			if preserveChargeReason != "" {
				if task.PrivateData.BillingProtocolVersion != TaskBillingHistoricalProtocolVersion ||
					update.TerminalStatus != TaskStatusFailure {
					return fmt.Errorf("%w: charge preservation is only valid for a historical failed task", ErrTaskBillingOperationInvariant)
				}
				plan.TargetQuota = preConsumedQuota
				plan.QuotaClamp = nil
			} else if update.TerminalStatus == TaskStatusFailure {
				plan.TargetQuota = 0
				plan.QuotaClamp = nil
			}
			if plan.TargetQuota < 0 || plan.TargetQuota > common.MaxQuota {
				return fmt.Errorf("%w: invalid target quota %d", ErrTaskBillingOperationInvariant, plan.TargetQuota)
			}
			delta64 := int64(plan.TargetQuota) - int64(preConsumedQuota)
			if delta64 < int64(common.MinQuota) || delta64 > int64(common.MaxQuota) {
				return fmt.Errorf("%w: invalid quota delta %d", ErrTaskBillingOperationInvariant, delta64)
			}
			delta = int(delta64)
			kind = TaskBillingOperationKindSettle
			if delta == 0 {
				kind = TaskBillingOperationKindNoop
			} else if update.TerminalStatus == TaskStatusFailure {
				kind = TaskBillingOperationKindRefund
			}
			state = TaskBillingOperationStatePending
			logState = TaskBillingOperationLogPending
			completedTime = 0
			operationKey = fmt.Sprintf("task:%d:terminal:v1", task.ID)
			if reservationBilling {
				operationKey = fmt.Sprintf("task:%d:terminal:v2", task.ID)
			}
			if preserveChargeReason != "" {
				kind = TaskBillingOperationKindNoop
				state = TaskBillingOperationStateCompleted
				logState = TaskBillingOperationLogNotRequired
				completedTime = now.UnixMilli()
				operationKey = fmt.Sprintf("task:%d:terminal:historical:v0", task.ID)
			}
		}

		progress := update.Progress
		if progress == "" {
			progress = "100%"
		}
		finishTime := update.FinishTime
		if finishTime <= 0 {
			finishTime = now.Unix()
		}
		privateData := task.PrivateData
		privateData.UpstreamResultURL = strings.TrimSpace(update.UpstreamResultURL)
		task.PrivateData = privateData
		updates := map[string]any{
			"status":       update.TerminalStatus,
			"progress":     progress,
			"finish_time":  finishTime,
			"fail_reason":  boundedTaskBillingError(update.FailReason),
			"private_data": privateData,
			"updated_at":   now.Unix(),
		}
		if reservationBilling {
			if err := task.freezeV2PrivateData(); err != nil {
				return err
			}
			updates["durable_private_data"] = task.DurablePrivateDataPayload
			updates["durable_private_data_hash"] = task.DurablePrivateDataHash
		}
		if update.SubmitTime > 0 {
			updates["submit_time"] = update.SubmitTime
		}
		if update.StartTime > 0 {
			updates["start_time"] = update.StartTime
		}
		if update.Data != nil {
			updates["data"] = json.RawMessage(append([]byte(nil), update.Data...))
		}
		updated := tx.Model(&Task{}).
			Where("id = ? AND status NOT IN ?", task.ID, []TaskStatus{TaskStatusSuccess, TaskStatusFailure}).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			casLost = true
			return nil
		}

		nowMs := now.UnixMilli()
		operation := TaskBillingOperation{
			TaskID:           task.ID,
			ReservationID:    task.PrivateData.AsyncBillingReservationID,
			OperationKey:     operationKey,
			TerminalStatus:   update.TerminalStatus,
			Kind:             kind,
			State:            state,
			UserID:           task.UserId,
			ChannelID:        task.ChannelId,
			BillingSource:    billingSource,
			SubscriptionID:   task.PrivateData.SubscriptionId,
			TokenID:          task.PrivateData.TokenId,
			PreConsumedQuota: preConsumedQuota,
			TargetQuota:      plan.TargetQuota,
			QuotaDelta:       delta,
			CreatedTimeMs:    nowMs,
			UpdatedTimeMs:    nowMs,
			CompletedTimeMs:  completedTime,
			LogState:         logState,
			LogUpdatedTimeMs: completedTime,
		}
		if strings.TrimSpace(plan.PreserveChargeReason) != "" {
			operation.LastError = boundedTaskBillingError(plan.PreserveChargeReason)
		}
		if durableBilling && (delta == 0 || (delta > 0 && !common.LogConsumeEnabled)) {
			operation.LogState = TaskBillingOperationLogNotRequired
		}
		if reservationBilling {
			// V2 freezes terminal stats/log projections only after the authoritative
			// balance mutation commits. The legacy embedded log worker stays disabled.
			operation.LogState = TaskBillingOperationLogNotRequired
			operation.LogUpdatedTimeMs = nowMs
		}
		if plan.QuotaClamp != nil {
			operation.QuotaClampOp = plan.QuotaClamp.Op
			operation.QuotaClampKind = plan.QuotaClamp.Kind
			operation.QuotaClampOriginal = strconv.FormatFloat(plan.QuotaClamp.Original, 'g', -1, 64)
			operation.QuotaClampValue = plan.QuotaClamp.Clamped
		}
		if operation.LogState == TaskBillingOperationLogPending {
			username, tokenName, err := snapshotBillingLogNames(tx, operation.UserID, operation.TokenID)
			if err != nil {
				return err
			}
			modelName := task.Properties.OriginModelName
			if task.PrivateData.BillingContext != nil && task.PrivateData.BillingContext.OriginModelName != "" {
				modelName = task.PrivateData.BillingContext.OriginModelName
			}
			logType := LogTypeConsume
			content := "terminal task billing settlement"
			logQuota := operation.QuotaDelta
			if logQuota < 0 {
				logType = LogTypeRefund
				logQuota = -logQuota
				content = "terminal task billing refund"
			}
			other := map[string]interface{}{
				"is_task":            true,
				"task_id":            task.TaskID,
				"billing_operation":  operation.OperationKey,
				"billing_source":     operation.BillingSource,
				"terminal_status":    operation.TerminalStatus,
				"pre_consumed_quota": operation.PreConsumedQuota,
				"actual_quota":       operation.TargetQuota,
			}
			if operation.QuotaClampKind != "" {
				other["admin_info"] = map[string]interface{}{
					"quota_saturation": map[string]interface{}{
						"op":       operation.QuotaClampOp,
						"kind":     operation.QuotaClampKind,
						"original": operation.QuotaClampOriginal,
						"clamped":  operation.QuotaClampValue,
					},
				}
			}
			otherJSON, err := common.Marshal(other)
			if err != nil {
				return err
			}
			payload, payloadHash, protocol, err := freezeBillingLogPayload(operation.OperationKey, &Log{
				UserId:    operation.UserID,
				CreatedAt: now.Unix(),
				Type:      logType,
				Content:   content,
				Username:  username,
				TokenName: tokenName,
				ModelName: modelName,
				Quota:     logQuota,
				ChannelId: operation.ChannelID,
				TokenId:   operation.TokenID,
				Group:     task.Group,
				Other:     string(otherJSON),
			})
			if err != nil {
				return err
			}
			operation.LogPayload = payload
			operation.LogPayloadHash = payloadHash
			operation.LogPayloadProtocol = protocol
		}
		terminalSnapshotTask := task
		terminalSnapshotTask.Status = update.TerminalStatus
		terminalSnapshotTask.Progress = progress
		terminalSnapshotTask.SubmitTime = task.SubmitTime
		if update.SubmitTime > 0 {
			terminalSnapshotTask.SubmitTime = update.SubmitTime
		}
		terminalSnapshotTask.StartTime = task.StartTime
		if update.StartTime > 0 {
			terminalSnapshotTask.StartTime = update.StartTime
		}
		terminalSnapshotTask.FinishTime = finishTime
		terminalSnapshotTask.FailReason = boundedTaskBillingError(update.FailReason)
		terminalSnapshotTask.PrivateData = privateData
		if update.Data != nil {
			terminalSnapshotTask.Data = json.RawMessage(append([]byte(nil), update.Data...))
		}
		if err := freezeTaskTerminalSnapshot(&operation, &terminalSnapshotTask); err != nil {
			return err
		}
		if err := tx.Create(&operation).Error; err != nil {
			return err
		}

		task.Status = update.TerminalStatus
		task.Progress = progress
		if update.SubmitTime > 0 {
			task.SubmitTime = update.SubmitTime
		}
		if update.StartTime > 0 {
			task.StartTime = update.StartTime
		}
		task.FinishTime = finishTime
		task.FailReason = boundedTaskBillingError(update.FailReason)
		task.PrivateData = privateData
		task.UpdatedAt = now.Unix()
		if update.Data != nil {
			task.Data = json.RawMessage(append([]byte(nil), update.Data...))
		}
		result.Task = task
		result.Operation = operation
		result.Transitioned = true
		result.ObservationMatched = true
		return nil
	})
	if err != nil {
		if legacyCreateAttempted {
			var task Task
			operation, operationErr := GetTaskBillingOperationByTaskID(ctx, update.TaskID)
			taskErr := DB.WithContext(ctx).Where("id = ?", update.TaskID).First(&task).Error
			if operationErr == nil && taskErr == nil &&
				operation.TerminalStatus == task.Status && operation.UserID == task.UserId {
				return &TaskBillingFinalization{
					Task:               task,
					Operation:          *operation,
					ObservationMatched: task.Status == update.TerminalStatus,
				}, nil
			}
		}
		return nil, err
	}
	if !casLost {
		return result, nil
	}

	// A database without row-level locking (notably SQLite) can reach this
	// branch. Reload outside the transaction so a competing winner is visible.
	var task Task
	if err := DB.WithContext(ctx).Where("id = ?", update.TaskID).First(&task).Error; err != nil {
		return nil, err
	}
	operation, err := GetTaskBillingOperationByTaskID(ctx, update.TaskID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTaskBillingOperationMissing
		}
		return nil, err
	}
	if operation.TerminalStatus != task.Status || operation.UserID != task.UserId {
		return nil, ErrTaskBillingOperationInvariant
	}
	return &TaskBillingFinalization{
		Task:               task,
		Operation:          *operation,
		ObservationMatched: task.Status == update.TerminalStatus,
	}, nil
}

func ClaimTaskBillingOperation(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*TaskBillingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if operationID <= 0 {
		return nil, false, errors.New("task billing operation id must be positive")
	}
	if owner == "" || len(owner) > 128 {
		return nil, false, errors.New("task billing lease owner is invalid")
	}
	if leaseDuration <= 0 || leaseDuration > maxTaskBillingLeaseDuration {
		return nil, false, errors.New("task billing lease duration is invalid")
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	claimed := DB.WithContext(ctx).Model(&TaskBillingOperation{}).
		Where("id = ?", operationID).
		Where("((state = ? AND next_retry_time_ms <= ?) OR (state = ? AND lease_until_ms <= ?))",
			TaskBillingOperationStatePending, nowMs,
			TaskBillingOperationStateRunning, nowMs).
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
	operation, err := GetTaskBillingOperation(ctx, operationID)
	if err != nil {
		return nil, false, err
	}
	return operation, claimed.RowsAffected == 1, nil
}

func ClaimNextTaskBillingOperation(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*TaskBillingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	var operationIDs []int64
	if err := DB.WithContext(ctx).Model(&TaskBillingOperation{}).
		Select("id").
		Where("((state = ? AND next_retry_time_ms <= ?) OR (state = ? AND lease_until_ms <= ?))",
			TaskBillingOperationStatePending, nowMs,
			TaskBillingOperationStateRunning, nowMs).
		Order("id ASC").Limit(32).Pluck("id", &operationIDs).Error; err != nil {
		return nil, false, err
	}
	for _, operationID := range operationIDs {
		operation, claimed, err := ClaimTaskBillingOperation(ctx, operationID, owner, now, leaseDuration)
		if err != nil {
			return nil, false, err
		}
		if claimed {
			return operation, true, nil
		}
	}
	return nil, false, nil
}

func enqueueTaskTerminalBillingProjectionsTx(
	tx *gorm.DB,
	operation *TaskBillingOperation,
	task *Task,
	delta int,
	now time.Time,
) error {
	if tx == nil || operation == nil || task == nil || operation.ID <= 0 || operation.ReservationID <= 0 ||
		operation.TaskID != task.ID || delta != operation.QuotaDelta || delta == 0 {
		return ErrTaskBillingOperationInvariant
	}
	audit := task.PrivateData.BillingAudit
	if audit == nil || validateAsyncBillingAuditSnapshot(*audit) != nil {
		modelName := strings.TrimSpace(task.Properties.OriginModelName)
		modelPrice := float64(0)
		modelRatio := float64(0)
		groupRatio := float64(0)
		var otherRatios map[string]float64
		if task.PrivateData.BillingContext != nil {
			billingContext := task.PrivateData.BillingContext
			if billingContext.OriginModelName != "" {
				modelName = billingContext.OriginModelName
			}
			modelPrice = billingContext.ModelPrice
			modelRatio = billingContext.ModelRatio
			groupRatio = billingContext.GroupRatio
			otherRatios = billingContext.OtherRatios
		}
		if modelName == "" {
			modelName = "unknown"
		}
		action := strings.TrimSpace(task.Action)
		if action == "" {
			action = "task"
		}
		fallback := AsyncBillingAcceptedAuditSnapshot{
			RequestPath: "/async-task", Action: action, Content: "asynchronous task billing",
			OriginModelName: modelName, UpstreamModelName: task.Properties.UpstreamModelName,
			IsModelMapped: task.Properties.UpstreamModelName != "" && task.Properties.UpstreamModelName != modelName,
			Group:         task.Group, NodeName: task.PrivateData.NodeName, ModelPrice: modelPrice,
			ModelRatio: modelRatio, GroupRatio: groupRatio, OtherRatios: otherRatios,
		}
		audit = &fallback
	}
	operationKey := strings.TrimSpace(operation.OperationKey)
	if delta > 0 {
		username, _, err := snapshotBillingLogNames(tx, operation.UserID, operation.TokenID)
		if err != nil {
			return err
		}
		if _, _, err := CreateBillingStatsProjectionTx(tx, BillingStatsProjectionSpec{
			OperationKey: operationKey, Kind: BillingStatsProjectionKindTaskTerminal,
			ReferenceID: operation.ID, UserID: operation.UserID, ChannelID: operation.ChannelID,
			QuotaDelta: delta, RequestDelta: 0,
			DataExportRequired: common.LogConsumeEnabled && audit.DataExportEnabled,
			DataExportUsername: username, DataExportModelName: audit.OriginModelName,
			DataExportCreatedAt: now.Unix(), DataExportTokenUsed: 0,
			DataExportUseGroup: audit.Group, DataExportTokenID: operation.TokenID,
			DataExportNodeName: audit.NodeName,
		}, now); err != nil {
			return err
		}
	}
	logType := LogTypeConsume
	logQuota := delta
	content := "terminal task billing settlement"
	required := common.LogConsumeEnabled
	if delta < 0 {
		logType = LogTypeRefund
		logQuota = -delta
		content = "terminal task billing refund"
		required = true
	}
	other := asyncBillingAcceptedOther(*audit)
	other["is_task"] = true
	other["task_id"] = task.TaskID
	other["billing_operation"] = operation.OperationKey
	other["billing_source"] = operation.BillingSource
	other["terminal_status"] = operation.TerminalStatus
	other["pre_consumed_quota"] = operation.PreConsumedQuota
	other["actual_quota"] = operation.TargetQuota
	otherJSON, err := common.Marshal(other)
	if err != nil {
		return err
	}
	username, tokenName, err := snapshotBillingLogNames(tx, operation.UserID, operation.TokenID)
	if err != nil {
		return err
	}
	entry := &Log{
		UserId: operation.UserID, CreatedAt: now.Unix(), Type: logType,
		Content: content, Username: username, TokenName: tokenName,
		ModelName: audit.OriginModelName, Quota: logQuota,
		ChannelId: operation.ChannelID, TokenId: operation.TokenID, Group: audit.Group,
		Ip: audit.ClientIP, RequestId: audit.RequestID,
		UpstreamRequestId: task.GetUpstreamTaskID(), Other: string(otherJSON),
	}
	if _, _, err := CreateBillingLogProjectionTx(tx, BillingLogProjectionSpec{
		OperationKey: operationKey, Kind: BillingLogProjectionKindTaskTerminal,
		ReferenceID: operation.ID, Required: required, Entry: entry,
	}, now); err != nil {
		return err
	}
	return nil
}

// CompleteTaskBillingOperation applies the durable accounting delta and marks
// the operation completed in one main-database transaction. A completed
// operation is a successful idempotent replay and never changes balances.
func CompleteTaskBillingOperation(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
) (*TaskBillingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if operationID <= 0 || owner == "" {
		return nil, ErrTaskBillingOperationNotClaimed
	}
	if now.IsZero() {
		now = time.Now()
	}

	var completed TaskBillingOperation
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var operation TaskBillingOperation
		if err := lockForUpdate(tx).Where("id = ?", operationID).First(&operation).Error; err != nil {
			return err
		}
		if operation.State == TaskBillingOperationStateCompleted {
			var task Task
			if err := lockForUpdate(tx).Where("id = ?", operation.TaskID).First(&task).Error; err != nil {
				return err
			}
			if _, err := repairTaskTerminalSnapshotTx(tx, &operation, &task, now); err != nil {
				return err
			}
			completed = operation
			return nil
		}
		if operation.State != TaskBillingOperationStateRunning || operation.LeaseOwner != owner {
			return ErrTaskBillingOperationNotClaimed
		}
		if operation.LeaseUntilMs <= now.UnixMilli() {
			return ErrTaskBillingOperationLeaseExpired
		}
		if operation.PreConsumedQuota < 0 || operation.TargetQuota < 0 ||
			operation.PreConsumedQuota > common.MaxQuota || operation.TargetQuota > common.MaxQuota {
			return ErrTaskBillingOperationInvariant
		}
		delta64 := int64(operation.TargetQuota) - int64(operation.PreConsumedQuota)
		if delta64 != int64(operation.QuotaDelta) {
			return ErrTaskBillingOperationInvariant
		}
		if operation.TerminalStatus != TaskStatusSuccess && operation.TerminalStatus != TaskStatusFailure {
			return ErrTaskBillingOperationInvariant
		}

		var task Task
		if err := lockForUpdate(tx).Where("id = ?", operation.TaskID).First(&task).Error; err != nil {
			return err
		}
		if _, err := repairTaskTerminalSnapshotTx(tx, &operation, &task, now); err != nil {
			return err
		}
		if task.UserId != operation.UserID || task.ChannelId != operation.ChannelID ||
			task.Status != operation.TerminalStatus || task.EffectiveBillingQuota() != operation.PreConsumedQuota {
			return ErrTaskBillingOperationInvariant
		}
		if operation.ReservationID > 0 && task.PrivateData.AsyncBillingReservationID != operation.ReservationID {
			return ErrTaskBillingOperationInvariant
		}

		delta := operation.QuotaDelta
		usageResult := billingUsageProjectionResult{
			UserOutcome: BillingUsageOutcomeNotRequired, ChannelOutcome: BillingUsageOutcomeNotRequired,
		}
		if operation.ReservationID > 0 {
			if err := CompleteAsyncBillingReservationTx(
				tx, operation.ReservationID, operation.UserID,
				operation.PreConsumedQuota, operation.TargetQuota, now,
			); err != nil {
				if !errors.Is(err, ErrAsyncBillingTerminalOverage) {
					return err
				}
				reason := boundedTaskBillingError(err.Error())
				updated := tx.Model(&TaskBillingOperation{}).Where(
					"id = ? AND state = ? AND lease_owner = ?", operation.ID, TaskBillingOperationStateRunning, owner,
				).Updates(map[string]any{
					"state":       TaskBillingOperationStateManualReview,
					"lease_owner": "", "lease_until_ms": 0,
					"last_error": reason, "updated_time_ms": now.UnixMilli(),
				})
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return ErrTaskBillingOperationInvariant
				}
				operation.State = TaskBillingOperationStateManualReview
				operation.LeaseOwner = ""
				operation.LeaseUntilMs = 0
				operation.LastError = reason
				operation.UpdatedTimeMs = now.UnixMilli()
				completed = operation
				return nil
			}
		}
		if delta != 0 {
			if operation.ReservationID == 0 {
				switch operation.BillingSource {
				case TaskBillingSourceWallet:
					var user User
					if err := lockForUpdate(tx.Unscoped()).Where("id = ?", operation.UserID).First(&user).Error; err != nil {
						return err
					}
					newQuota := int64(user.Quota) - int64(delta)
					if newQuota < 0 {
						return fmt.Errorf("%w: wallet needs %d and has %d", ErrTaskBillingOperationInsufficientQuota, delta, user.Quota)
					}
					if newQuota > int64(common.MaxQuota) {
						return fmt.Errorf("%w: wallet refund exceeds quota range", ErrTaskBillingOperationInvariant)
					}
					var walletUpdate *gorm.DB
					if delta > 0 {
						walletUpdate = tx.Unscoped().Model(&User{}).Where("id = ?", operation.UserID).
							Update("quota", gorm.Expr("quota - ?", delta))
					} else {
						walletUpdate = tx.Unscoped().Model(&User{}).Where("id = ?", operation.UserID).
							Update("quota", gorm.Expr("quota + ?", -delta))
					}
					if walletUpdate.Error != nil {
						return walletUpdate.Error
					}
					if walletUpdate.RowsAffected != 1 {
						return ErrTaskBillingOperationInvariant
					}
				case TaskBillingSourceSubscription:
					if operation.SubscriptionID <= 0 {
						return ErrTaskBillingOperationInvariant
					}
					var subscription UserSubscription
					if err := lockForUpdate(tx).Where("id = ?", operation.SubscriptionID).First(&subscription).Error; err != nil {
						return err
					}
					if subscription.UserId != operation.UserID {
						return ErrTaskBillingOperationInvariant
					}
					if subscription.AmountUsed < 0 || subscription.AmountTotal < 0 ||
						(int64(delta) > 0 && subscription.AmountUsed > math.MaxInt64-int64(delta)) ||
						(int64(delta) < 0 && int64(delta) < -subscription.AmountUsed) {
						return fmt.Errorf("%w: subscription amount is outside the supported range", ErrTaskBillingOperationInvariant)
					}
					newUsed := subscription.AmountUsed + int64(delta)
					if subscription.AmountTotal > 0 && newUsed > subscription.AmountTotal {
						return fmt.Errorf("%w: subscription needs %d and has %d remaining",
							ErrTaskBillingOperationInsufficientQuota, delta, subscription.AmountTotal-subscription.AmountUsed)
					}
					updated := tx.Model(&UserSubscription{}).Where("id = ?", operation.SubscriptionID).
						Updates(map[string]any{"amount_used": newUsed, "updated_at": now.Unix()})
					if updated.Error != nil {
						return updated.Error
					}
					if updated.RowsAffected != 1 {
						return ErrTaskBillingOperationInvariant
					}
				default:
					return ErrTaskBillingOperationInvariant
				}

				if operation.TokenID > 0 {
					var token Token
					if err := lockForUpdate(tx.Unscoped()).Where("id = ?", operation.TokenID).First(&token).Error; err != nil {
						return err
					}
					if token.UserId != operation.UserID {
						return ErrTaskBillingOperationInvariant
					}
					newRemain := int64(token.RemainQuota) - int64(delta)
					newUsed := int64(token.UsedQuota) + int64(delta)
					if !token.UnlimitedQuota && newRemain < 0 {
						return fmt.Errorf("%w: token needs %d and has %d", ErrTaskBillingOperationInsufficientQuota, delta, token.RemainQuota)
					}
					if newRemain < int64(common.MinQuota) || newRemain > int64(common.MaxQuota) ||
						newUsed < 0 || newUsed > int64(common.MaxQuota) {
						return fmt.Errorf("%w: token quota is outside the supported range", ErrTaskBillingOperationInvariant)
					}
					var tokenUpdate *gorm.DB
					if delta > 0 {
						tokenUpdate = tx.Unscoped().Model(&Token{}).Where("id = ?", operation.TokenID).Updates(map[string]any{
							"remain_quota":  gorm.Expr("remain_quota - ?", delta),
							"used_quota":    gorm.Expr("used_quota + ?", delta),
							"accessed_time": now.Unix(),
						})
					} else {
						refund := -delta
						tokenUpdate = tx.Unscoped().Model(&Token{}).Where("id = ?", operation.TokenID).Updates(map[string]any{
							"remain_quota":  gorm.Expr("remain_quota + ?", refund),
							"used_quota":    gorm.Expr("used_quota - ?", refund),
							"accessed_time": now.Unix(),
						})
					}
					if tokenUpdate.Error != nil {
						return tokenUpdate.Error
					}
					if tokenUpdate.RowsAffected != 1 {
						return ErrTaskBillingOperationInvariant
					}
				}
			}

			// Preserve the established task-settlement statistics contract: only
			// an additional positive charge increments usage and request counters.
			// Refunds remain represented by the authoritative operation/log and do
			// not rewrite historical consumption counters.
			if delta > 0 && operation.ReservationID == 0 {
				projected, projectionErr := applyBillingUsageProjectionTx(tx, billingUsageProjectionSpec{
					OperationKey: operation.OperationKey,
					UserID:       operation.UserID,
					ChannelID:    operation.ChannelID,
					QuotaDelta:   delta,
				})
				if projectionErr != nil {
					return projectionErr
				}
				usageResult = projected
			}

			quotaColumn := "quota"
			if task.PrivateData.BillingProtocolVersion == TaskBillingProtocolVersion {
				quotaColumn = "durable_quota"
			}
			taskUpdate := tx.Model(&Task{}).
				Where("id = ? AND status = ? AND "+quotaColumn+" = ?", task.ID, operation.TerminalStatus, operation.PreConsumedQuota).
				Updates(map[string]any{quotaColumn: operation.TargetQuota, "updated_at": now.Unix()})
			if taskUpdate.Error != nil {
				return taskUpdate.Error
			}
			if taskUpdate.RowsAffected != 1 {
				return ErrTaskBillingOperationInvariant
			}
			if operation.ReservationID > 0 {
				if err := enqueueTaskTerminalBillingProjectionsTx(tx, &operation, &task, delta, now); err != nil {
					return err
				}
			}
		}

		nowMs := now.UnixMilli()
		operationUpdate := tx.Model(&TaskBillingOperation{}).
			Where("id = ? AND state = ? AND lease_owner = ?", operation.ID, TaskBillingOperationStateRunning, owner).
			Updates(map[string]any{
				"state":                 TaskBillingOperationStateCompleted,
				"lease_owner":           "",
				"lease_until_ms":        0,
				"last_error":            "",
				"updated_time_ms":       nowMs,
				"completed_time_ms":     nowMs,
				"usage_user_outcome":    usageResult.UserOutcome,
				"usage_channel_outcome": usageResult.ChannelOutcome,
				"usage_warning": boundedTaskBillingError(
					billingUsageProjectionWarning(operation.OperationKey, usageResult),
				),
			})
		if operationUpdate.Error != nil {
			return operationUpdate.Error
		}
		if operationUpdate.RowsAffected != 1 {
			return ErrTaskBillingOperationNotClaimed
		}
		operation.State = TaskBillingOperationStateCompleted
		operation.LeaseOwner = ""
		operation.LeaseUntilMs = 0
		operation.LastError = ""
		operation.UpdatedTimeMs = nowMs
		operation.CompletedTimeMs = nowMs
		operation.UsageUserOutcome = usageResult.UserOutcome
		operation.UsageChannelOutcome = usageResult.ChannelOutcome
		operation.UsageWarning = boundedTaskBillingError(billingUsageProjectionWarning(operation.OperationKey, usageResult))
		completed = operation
		return nil
	})
	if err != nil {
		return nil, err
	}
	if completed.ReservationID > 0 {
		if cacheErr := SyncAsyncBillingReservationCaches(ctx, completed.ReservationID, now); cacheErr != nil {
			common.SysError(fmt.Sprintf("sync terminal task billing cache failed: reservation=%d error=%s",
				completed.ReservationID, boundedTaskBillingError(cacheErr.Error())))
		}
	}
	return &completed, nil
}

func RetryTaskBillingOperation(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
	nextRetry time.Time,
	failure string,
) (*TaskBillingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if operationID <= 0 || owner == "" {
		return nil, ErrTaskBillingOperationNotClaimed
	}
	if now.IsZero() {
		now = time.Now()
	}
	if nextRetry.IsZero() || nextRetry.Before(now) {
		nextRetry = now
	}
	lastError := boundedTaskBillingError(failure)
	updated := DB.WithContext(ctx).Model(&TaskBillingOperation{}).
		Where("id = ? AND state = ? AND lease_owner = ?", operationID, TaskBillingOperationStateRunning, owner).
		Updates(map[string]any{
			"state":              TaskBillingOperationStatePending,
			"lease_owner":        "",
			"lease_until_ms":     0,
			"next_retry_time_ms": nextRetry.UnixMilli(),
			"last_error":         lastError,
			"updated_time_ms":    now.UnixMilli(),
		})
	if updated.Error != nil {
		return nil, updated.Error
	}
	if updated.RowsAffected != 1 {
		return nil, ErrTaskBillingOperationNotClaimed
	}
	return GetTaskBillingOperation(ctx, operationID)
}

func ClaimTaskBillingOperationLog(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*TaskBillingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner = strings.TrimSpace(owner)
	if operationID <= 0 {
		return nil, false, errors.New("task billing operation id must be positive")
	}
	if owner == "" || len(owner) > 128 {
		return nil, false, errors.New("task billing log lease owner is invalid")
	}
	if leaseDuration <= 0 || leaseDuration > maxTaskBillingLeaseDuration {
		return nil, false, errors.New("task billing log lease duration is invalid")
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	claimed := DB.WithContext(ctx).Model(&TaskBillingOperation{}).
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
	operation, err := GetTaskBillingOperation(ctx, operationID)
	if err != nil {
		return nil, false, err
	}
	return operation, claimed.RowsAffected == 1, nil
}

func ClaimNextTaskBillingOperationLog(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*TaskBillingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowMs := now.UnixMilli()
	var operationIDs []int64
	if err := DB.WithContext(ctx).Model(&TaskBillingOperation{}).Select("id").
		Where("state = ?", TaskBillingOperationStateCompleted).
		Where("((log_state IN ? AND log_next_retry_ms <= ?) OR (log_state = ? AND log_lease_until_ms <= ?))",
			[]string{"", TaskBillingOperationLogPending, TaskBillingOperationLogFailed},
			nowMs, TaskBillingOperationLogWriting, nowMs).
		Order("id ASC").Limit(32).Pluck("id", &operationIDs).Error; err != nil {
		return nil, false, err
	}
	for _, operationID := range operationIDs {
		operation, claimed, err := ClaimTaskBillingOperationLog(ctx, operationID, owner, now, leaseDuration)
		if err != nil {
			return nil, false, err
		}
		if claimed {
			return operation, true, nil
		}
	}
	return nil, false, nil
}

// RecordTaskBillingOperationLog materializes the authoritative main-database
// operation into the independently configured log database. This is
// intentionally post-commit and recoverable. The sink's nullable unique
// billing_operation_key is the SQL receipt; ClickHouse exposes identical
// append retries once through its visible-log view. Sink failures never return
// the operation to the financial pending state.
func RecordTaskBillingOperationLog(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) error {
	operation, claimed, err := ClaimTaskBillingOperationLog(ctx, operationID, owner, now, leaseDuration)
	if err != nil {
		return err
	}
	if !claimed {
		if operation.State == TaskBillingOperationStateCompleted &&
			(operation.LogState == TaskBillingOperationLogWritten || operation.LogState == TaskBillingOperationLogNotRequired) {
			return nil
		}
		return ErrTaskBillingOperationNotClaimed
	}
	return recordClaimedTaskBillingOperationLog(ctx, operation, owner, now)
}

func RecordClaimedTaskBillingOperationLog(ctx context.Context, operationID int64, owner string, now time.Time) error {
	operation, err := GetTaskBillingOperation(ctx, operationID)
	if err != nil {
		return err
	}
	return recordClaimedTaskBillingOperationLog(ctx, operation, owner, now)
}

func recordClaimedTaskBillingOperationLog(ctx context.Context, operation *TaskBillingOperation, owner string, now time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if operation == nil || operation.State != TaskBillingOperationStateCompleted ||
		operation.LogState != TaskBillingOperationLogWriting || operation.LogLeaseOwner != owner {
		return ErrTaskBillingOperationNotClaimed
	}
	if now.IsZero() {
		now = time.Now()
	}
	if operation.LogLeaseUntilMs <= now.UnixMilli() {
		return ErrTaskBillingOperationLeaseExpired
	}
	if operation.QuotaDelta != 0 {
		if cacheErr := errors.Join(
			InvalidateUserCache(operation.UserID),
			InvalidateTokenCacheByID(operation.TokenID, operation.UserID),
		); cacheErr != nil {
			markErr := finishTaskBillingOperationLog(ctx, operation.ID, owner, TaskBillingOperationLogFailed, cacheErr.Error(), now)
			return errors.Join(cacheErr, markErr)
		}
	}
	if operation.QuotaDelta == 0 {
		return finishTaskBillingOperationLog(ctx, operation.ID, owner, TaskBillingOperationLogNotRequired, "", now)
	}
	if LOG_DB == nil {
		logErr := errors.New("task billing log database is unavailable")
		markErr := finishTaskBillingOperationLog(ctx, operation.ID, owner, TaskBillingOperationLogFailed, logErr.Error(), now)
		return errors.Join(logErr, markErr)
	}
	if err := writeFrozenBillingLog(ctx, operation.OperationKey, operation.LogPayload,
		operation.LogPayloadHash, operation.LogPayloadProtocol); err != nil {
		markErr := finishTaskBillingOperationLog(ctx, operation.ID, owner, TaskBillingOperationLogFailed, err.Error(), now)
		return errors.Join(err, markErr)
	}
	return finishTaskBillingOperationLog(ctx, operation.ID, owner, TaskBillingOperationLogWritten, "", now)
}

func finishTaskBillingOperationLog(
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
	updated := DB.WithContext(ctx).Model(&TaskBillingOperation{}).
		Where("id = ? AND state = ? AND log_state = ? AND log_lease_owner = ?",
			operationID, TaskBillingOperationStateCompleted, TaskBillingOperationLogWriting, owner).
		Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrTaskBillingOperationInvariant
	}
	return nil
}

func boundedTaskBillingError(failure string) string {
	bounded := strings.Join(strings.Fields(common.SanitizeErrorMessage(failure)), " ")
	if len(bounded) <= maxTaskBillingLastErrorBytes {
		return bounded
	}
	bounded = bounded[:maxTaskBillingLastErrorBytes]
	for !utf8.ValidString(bounded) {
		bounded = bounded[:len(bounded)-1]
	}
	return bounded
}
