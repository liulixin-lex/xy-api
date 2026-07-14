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

func ProcessNextBillingLogProjection(
	ctx context.Context,
	owner string,
	leaseDuration time.Duration,
) (*model.BillingLogProjection, bool, error) {
	return processNextBillingLogProjectionAt(ctx, owner, time.Now(), leaseDuration)
}

func processNextBillingLogProjectionAt(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*model.BillingLogProjection, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	projection, claimed, err := model.ClaimNextBillingLogProjection(ctx, owner, now, leaseDuration)
	if err != nil || !claimed {
		return projection, false, err
	}
	delivered, err := model.DeliverClaimedBillingLogProjection(ctx, projection.ID, owner)
	if err == nil {
		return delivered, true, nil
	}

	failureCode := ""
	switch {
	case errors.Is(err, model.ErrBillingLogPayloadConflict):
		failureCode = model.BillingLogProjectionFailureSinkReceiptConflict
	case errors.Is(err, model.ErrBillingLogPayloadInvalid), errors.Is(err, model.ErrBillingLogProjectionInvalid):
		failureCode = model.BillingLogProjectionFailureInvalidPayload
	}
	if failureCode != "" {
		failErr := model.FailClaimedBillingLogProjection(
			ctx, projection.ID, owner, time.Now(), failureCode,
		)
		if failErr == nil {
			logger.LogWarn(ctx, fmt.Sprintf(
				"billing log projection moved to manual audit: operation=%s code=%s",
				projection.OperationKey, failureCode,
			))
			failed, readErr := model.GetBillingLogProjection(ctx, projection.ID)
			return failed, true, readErr
		}
		return projection, true, errors.Join(err, failErr)
	}
	if errors.Is(err, model.ErrBillingLogProjectionLeaseExpired) ||
		errors.Is(err, model.ErrBillingLogProjectionNotClaimed) {
		return projection, true, err
	}

	retryErr := model.RetryClaimedBillingLogProjection(ctx, projection.ID, owner, time.Now(), err)
	logger.LogWarn(ctx, fmt.Sprintf(
		"billing log projection failed and will retry: operation=%s error=%s",
		projection.OperationKey, common.SanitizeErrorMessage(err.Error()),
	))
	return projection, true, errors.Join(err, retryErr)
}
