package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

type TaskFinalizationObservation struct {
	TaskID               int64
	TerminalStatus       model.TaskStatus
	Progress             string
	SubmitTime           int64
	StartTime            int64
	FinishTime           int64
	FailReason           string
	UpstreamResultURL    string
	Data                 json.RawMessage
	ActualQuota          *int
	TotalTokens          int
	PreserveChargeReason string
}

type TaskFinalizationResult struct {
	Task               *model.Task
	Operation          *model.TaskBillingOperation
	ManualReview       bool
	Transitioned       bool
	ObservationMatched bool
}

func taskTerminalUsageReviewReason(task *model.Task, observation TaskFinalizationObservation) string {
	if task == nil || observation.TerminalStatus != model.TaskStatusSuccess ||
		task.PrivateData.BillingProtocolVersion != model.TaskBillingProtocolVersion ||
		task.PrivateData.AsyncBillingReservationID <= 0 {
		return ""
	}
	billingContext := task.EffectiveBillingContext()
	if billingContext != nil && billingContext.PerCallBilling {
		return ""
	}
	if observation.ActualQuota != nil {
		return ""
	}
	if billingContext == nil {
		return "terminal usage verification failed: billing context is missing"
	}
	if observation.TotalTokens <= 0 {
		return "terminal usage verification failed: positive usage is missing"
	}
	if billingContext.ModelRatio <= 0 || billingContext.GroupRatio <= 0 ||
		math.IsNaN(billingContext.ModelRatio) || math.IsInf(billingContext.ModelRatio, 0) ||
		math.IsNaN(billingContext.GroupRatio) || math.IsInf(billingContext.GroupRatio, 0) {
		return "terminal usage verification failed: billing ratios are invalid"
	}
	for _, ratio := range billingContext.OtherRatios {
		if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return "terminal usage verification failed: billing multiplier is invalid"
		}
	}
	return ""
}

// FinalizeTaskObservation records one terminal upstream observation and its
// durable billing intent. It deliberately does not execute accounting inline:
// the operation remains recoverable if the process exits immediately after
// the terminal transaction commits.
func FinalizeTaskObservation(ctx context.Context, observation TaskFinalizationObservation) (*TaskFinalizationResult, error) {
	return finalizeTaskObservationAt(ctx, observation, time.Now())
}

func finalizeTaskObservationAt(ctx context.Context, observation TaskFinalizationObservation, now time.Time) (*TaskFinalizationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if observation.TaskID <= 0 {
		return nil, errors.New("task finalization requires a positive task id")
	}
	if observation.TerminalStatus != model.TaskStatusSuccess && observation.TerminalStatus != model.TaskStatusFailure {
		return nil, errors.New("task finalization status must be SUCCESS or FAILURE")
	}
	if observation.TotalTokens < 0 {
		return nil, errors.New("task finalization total tokens cannot be negative")
	}
	if observation.ActualQuota != nil && (*observation.ActualQuota < 0 || *observation.ActualQuota > common.MaxQuota) {
		return nil, fmt.Errorf("task finalization actual quota %d is outside the supported range", *observation.ActualQuota)
	}

	sanitizedData := observation.Data
	if observation.Data != nil {
		sanitizedData = SanitizeTaskData(observation.Data)
	}
	if observation.TerminalStatus == model.TaskStatusSuccess {
		var current model.Task
		if err := model.DB.WithContext(ctx).Where("id = ?", observation.TaskID).First(&current).Error; err != nil {
			return nil, err
		}
		if reviewReason := taskTerminalUsageReviewReason(&current, observation); reviewReason != "" {
			task, _, err := model.FinalizeTaskTerminalUsageManualReview(
				ctx,
				current.PrivateData.AsyncBillingReservationID,
				model.TaskTerminalUpdate{
					TaskID: observation.TaskID, TerminalStatus: observation.TerminalStatus,
					Progress: observation.Progress, SubmitTime: observation.SubmitTime,
					StartTime: observation.StartTime, FinishTime: observation.FinishTime,
					FailReason:        common.SanitizeErrorMessage(observation.FailReason),
					UpstreamResultURL: observation.UpstreamResultURL, Data: sanitizedData,
				},
				reviewReason,
				now,
			)
			if err != nil {
				return nil, err
			}
			return &TaskFinalizationResult{
				Task: task, ManualReview: true, Transitioned: true, ObservationMatched: true,
			}, nil
		}
	}
	finalization, err := model.FinalizeTaskWithBillingOperation(ctx, model.TaskTerminalUpdate{
		TaskID:            observation.TaskID,
		TerminalStatus:    observation.TerminalStatus,
		Progress:          observation.Progress,
		SubmitTime:        observation.SubmitTime,
		StartTime:         observation.StartTime,
		FinishTime:        observation.FinishTime,
		FailReason:        common.SanitizeErrorMessage(observation.FailReason),
		UpstreamResultURL: observation.UpstreamResultURL,
		Data:              sanitizedData,
	}, now, func(task *model.Task) (model.TaskBillingOperationPlan, error) {
		plan := model.TaskBillingOperationPlan{
			TargetQuota:          task.EffectiveBillingQuota(),
			PreserveChargeReason: strings.TrimSpace(observation.PreserveChargeReason),
		}
		if observation.TerminalStatus == model.TaskStatusFailure {
			if plan.PreserveChargeReason == "" &&
				task.PrivateData.BillingProtocolVersion == model.TaskBillingHistoricalProtocolVersion &&
				task.Platform == constant.TaskPlatformSuno {
				plan.PreserveChargeReason = "historical Suno refund state is ambiguous; charge retained for manual audit"
			}
			if plan.PreserveChargeReason != "" {
				return plan, nil
			}
			plan.TargetQuota = 0
			return plan, nil
		}

		billingContext := task.EffectiveBillingContext()
		if billingContext != nil && billingContext.PerCallBilling {
			return plan, nil
		}
		if observation.ActualQuota != nil {
			plan.TargetQuota = *observation.ActualQuota
			return plan, nil
		}
		if observation.TotalTokens == 0 || billingContext == nil {
			return plan, nil
		}

		// A missing/non-positive ratio denotes a fixed-price or historical task
		// without a usable token snapshot. Preserve the already charged amount;
		// never consult mutable completion-time settings as a fallback.
		if billingContext.ModelRatio <= 0 || billingContext.GroupRatio <= 0 {
			return plan, nil
		}
		if math.IsNaN(billingContext.ModelRatio) || math.IsInf(billingContext.ModelRatio, 0) ||
			math.IsNaN(billingContext.GroupRatio) || math.IsInf(billingContext.GroupRatio, 0) {
			return plan, errors.New("task billing snapshot contains a non-finite base ratio")
		}

		otherMultiplier := 1.0
		keys := make([]string, 0, len(billingContext.OtherRatios))
		for key := range billingContext.OtherRatios {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			ratio := billingContext.OtherRatios[key]
			if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
				return plan, fmt.Errorf("task billing snapshot ratio %q is invalid", key)
			}
			otherMultiplier *= ratio
		}

		targetQuota, clamp := common.QuotaFromFloatChecked(
			float64(observation.TotalTokens) * billingContext.ModelRatio * billingContext.GroupRatio * otherMultiplier,
		)
		if targetQuota < 0 {
			return plan, errors.New("task billing snapshot produced a negative target quota")
		}
		plan.TargetQuota = targetQuota
		plan.QuotaClamp = clamp
		return plan, nil
	})
	if err != nil {
		return nil, err
	}
	return &TaskFinalizationResult{
		Task:               &finalization.Task,
		Operation:          &finalization.Operation,
		Transitioned:       finalization.Transitioned,
		ObservationMatched: finalization.ObservationMatched,
	}, nil
}

// ProcessTaskBillingOperation claims and executes one operation. The lease is
// recoverable: another worker may reclaim it after expiry if this process exits
// before the accounting transaction completes.
func ProcessTaskBillingOperation(
	ctx context.Context,
	operationID int64,
	owner string,
	leaseDuration time.Duration,
) (*model.TaskBillingOperation, error) {
	return processTaskBillingOperationAt(ctx, operationID, owner, time.Now(), leaseDuration)
}

func processTaskBillingOperationAt(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*model.TaskBillingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operation, claimed, err := model.ClaimTaskBillingOperation(ctx, operationID, owner, now, leaseDuration)
	if err != nil {
		return nil, err
	}
	if operation.State == model.TaskBillingOperationStateCompleted {
		if err := model.RecordTaskBillingOperationLog(ctx, operation.ID, owner, now, leaseDuration); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("task billing log materialization failed: operation=%s error=%s",
				operation.OperationKey, common.SanitizeErrorMessage(err.Error())))
			return operation, err
		}
		return model.GetTaskBillingOperation(ctx, operation.ID)
	}
	if !claimed && !(operation.State == model.TaskBillingOperationStateRunning &&
		operation.LeaseOwner == owner && operation.LeaseUntilMs > now.UnixMilli()) {
		return operation, model.ErrTaskBillingOperationNotClaimed
	}
	return processClaimedTaskBillingOperation(ctx, operation, owner, now, leaseDuration)
}

func ProcessNextTaskBillingOperation(
	ctx context.Context,
	owner string,
	leaseDuration time.Duration,
) (*model.TaskBillingOperation, bool, error) {
	return processNextTaskBillingOperationAt(ctx, owner, time.Now(), leaseDuration)
}

func processNextTaskBillingOperationAt(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*model.TaskBillingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operation, claimed, err := model.ClaimNextTaskBillingOperation(ctx, owner, now, leaseDuration)
	if err != nil {
		return nil, false, err
	}
	if !claimed {
		logOperation, logClaimed, logErr := model.ClaimNextTaskBillingOperationLog(ctx, owner, now, leaseDuration)
		if logErr != nil || !logClaimed {
			return logOperation, false, logErr
		}
		logErr = model.RecordClaimedTaskBillingOperationLog(ctx, logOperation.ID, owner, now)
		refreshed, refreshErr := model.GetTaskBillingOperation(ctx, logOperation.ID)
		return refreshed, true, errors.Join(logErr, refreshErr)
	}
	completed, err := processClaimedTaskBillingOperation(ctx, operation, owner, now, leaseDuration)
	return completed, true, err
}

func processClaimedTaskBillingOperation(
	ctx context.Context,
	operation *model.TaskBillingOperation,
	owner string,
	now time.Time,
	logLeaseDuration time.Duration,
) (*model.TaskBillingOperation, error) {
	if operation == nil {
		return nil, errors.New("task billing operation is nil")
	}
	completed, err := model.CompleteTaskBillingOperation(ctx, operation.ID, owner, now)
	if err != nil {
		attempt := operation.Attempts
		if attempt < 1 {
			attempt = 1
		}
		shift := attempt - 1
		if shift > 6 {
			shift = 6
		}
		retryDelay := 5 * time.Second * time.Duration(1<<shift)
		if retryDelay > 5*time.Minute {
			retryDelay = 5 * time.Minute
		}

		// Releasing a lease is cleanup for a durable operation. Give it a short
		// independent context even when the caller was canceled; owner fencing
		// still prevents a stale worker from releasing a reclaimed operation.
		retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_, retryErr := model.RetryTaskBillingOperation(
			retryCtx,
			operation.ID,
			owner,
			now,
			now.Add(retryDelay),
			err.Error(),
		)
		return operation, errors.Join(err, retryErr)
	}
	if completed.State == model.TaskBillingOperationStateManualReview {
		logger.LogWarn(ctx, fmt.Sprintf(
			"task billing operation requires manual review: operation=%s reason=%s",
			completed.OperationKey, common.SanitizeErrorMessage(completed.LastError),
		))
		return completed, nil
	}

	if completed.QuotaClampKind != "" {
		logger.LogWarn(ctx, fmt.Sprintf("task billing quota saturation committed: operation=%s kind=%s original=%s clamped=%d",
			completed.OperationKey, completed.QuotaClampKind, completed.QuotaClampOriginal, completed.QuotaClampValue))
	}
	if err := model.RecordTaskBillingOperationLog(ctx, completed.ID, owner, now, logLeaseDuration); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("task billing log materialization failed: operation=%s error=%s",
			completed.OperationKey, common.SanitizeErrorMessage(err.Error())))
		return completed, err
	}
	refreshed, err := model.GetTaskBillingOperation(ctx, completed.ID)
	if err != nil {
		return completed, err
	}
	return refreshed, nil
}
