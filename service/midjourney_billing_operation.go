package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

func FinalizeMidjourneyFailure(
	ctx context.Context,
	observed *model.Midjourney,
	fromStatus string,
) (*model.MidjourneyFailureFinalization, error) {
	return finalizeMidjourneyFailureAt(ctx, observed, fromStatus, time.Now())
}

func finalizeMidjourneyFailureAt(
	ctx context.Context,
	observed *model.Midjourney,
	fromStatus string,
	now time.Time,
) (*model.MidjourneyFailureFinalization, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	modelName := ""
	if observed != nil {
		modelName = CovertMjpActionToModelName(observed.Action)
	}
	return model.FinalizeMidjourneyFailureWithOperation(ctx, observed, fromStatus, modelName, now)
}

func FinalizeMidjourneySuccess(
	ctx context.Context,
	observed *model.Midjourney,
	fromStatus string,
) (*model.MidjourneyFailureFinalization, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return model.FinalizeMidjourneySuccessWithOperation(ctx, observed, fromStatus, time.Now())
}

func ProcessMidjourneyBillingOperation(
	ctx context.Context,
	operationID int64,
	owner string,
	leaseDuration time.Duration,
) (*model.MidjourneyBillingOperation, error) {
	return processMidjourneyBillingOperationAt(ctx, operationID, owner, time.Now(), leaseDuration)
}

func processMidjourneyBillingOperationAt(
	ctx context.Context,
	operationID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*model.MidjourneyBillingOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operation, claimed, err := model.ClaimMidjourneyBillingOperation(ctx, operationID, owner, now, leaseDuration)
	if err != nil {
		return nil, err
	}
	if operation.State == model.TaskBillingOperationStateCompleted {
		if err := model.RecordMidjourneyBillingOperationLog(ctx, operation.ID, owner, now, leaseDuration); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("Midjourney billing log materialization failed: operation=%s error=%s",
				operation.OperationKey, common.SanitizeErrorMessage(err.Error())))
			return operation, err
		}
		return model.GetMidjourneyBillingOperation(ctx, operation.ID)
	}
	if !claimed && !(operation.State == model.TaskBillingOperationStateRunning &&
		operation.LeaseOwner == owner && operation.LeaseUntilMs > now.UnixMilli()) {
		return operation, model.ErrMidjourneyBillingOperationNotClaimed
	}
	return processClaimedMidjourneyBillingOperation(ctx, operation, owner, now, leaseDuration)
}

func ProcessNextMidjourneyBillingOperation(
	ctx context.Context,
	owner string,
	leaseDuration time.Duration,
) (*model.MidjourneyBillingOperation, bool, error) {
	return processNextMidjourneyBillingOperationAt(ctx, owner, time.Now(), leaseDuration)
}

func processNextMidjourneyBillingOperationAt(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*model.MidjourneyBillingOperation, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operation, claimed, err := model.ClaimNextMidjourneyBillingOperation(ctx, owner, now, leaseDuration)
	if err != nil {
		return nil, false, err
	}
	if !claimed {
		logOperation, logClaimed, logErr := model.ClaimNextMidjourneyBillingOperationLog(ctx, owner, now, leaseDuration)
		if logErr != nil || !logClaimed {
			return logOperation, false, logErr
		}
		logErr = model.RecordClaimedMidjourneyBillingOperationLog(ctx, logOperation.ID, owner, now)
		refreshed, refreshErr := model.GetMidjourneyBillingOperation(ctx, logOperation.ID)
		return refreshed, true, errors.Join(logErr, refreshErr)
	}
	completed, err := processClaimedMidjourneyBillingOperation(ctx, operation, owner, now, leaseDuration)
	return completed, true, err
}

func processClaimedMidjourneyBillingOperation(
	ctx context.Context,
	operation *model.MidjourneyBillingOperation,
	owner string,
	now time.Time,
	logLeaseDuration time.Duration,
) (*model.MidjourneyBillingOperation, error) {
	if operation == nil {
		return nil, errors.New("Midjourney billing operation is nil")
	}
	completed, err := model.CompleteMidjourneyBillingOperation(ctx, operation.ID, owner, now)
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
		retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_, retryErr := model.RetryMidjourneyBillingOperation(
			retryCtx,
			operation.ID,
			owner,
			now,
			now.Add(retryDelay),
			err.Error(),
		)
		return operation, errors.Join(err, retryErr)
	}

	if err := model.RecordMidjourneyBillingOperationLog(ctx, completed.ID, owner, now, logLeaseDuration); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Midjourney billing log materialization failed: operation=%s error=%s",
			completed.OperationKey, common.SanitizeErrorMessage(err.Error())))
		return completed, err
	}
	refreshed, err := model.GetMidjourneyBillingOperation(ctx, completed.ID)
	if err != nil {
		return completed, err
	}
	return refreshed, nil
}
