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

func ProcessNextBillingStatsProjection(
	ctx context.Context,
	owner string,
	leaseDuration time.Duration,
) (*model.BillingStatsProjection, bool, error) {
	return processNextBillingStatsProjectionAt(ctx, owner, time.Now(), leaseDuration)
}

func processNextBillingStatsProjectionAt(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (*model.BillingStatsProjection, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	projection, claimed, err := model.ClaimNextBillingStatsProjection(ctx, owner, now, leaseDuration)
	if err != nil || !claimed {
		return projection, false, err
	}
	completed, err := model.CompleteClaimedBillingStatsProjection(ctx, projection.ID, owner, now)
	if err == nil {
		return completed, true, nil
	}

	if errors.Is(err, model.ErrBillingStatsProjectionInvalid) {
		failErr := model.FailClaimedBillingStatsProjection(
			ctx, projection.ID, owner, now, "invalid_frozen_spec",
		)
		if failErr == nil {
			logger.LogWarn(ctx, fmt.Sprintf(
				"billing stats projection moved to manual audit: operation=%s code=invalid_frozen_spec",
				projection.OperationKey,
			))
			failed, readErr := model.GetBillingStatsProjection(ctx, projection.ID)
			return failed, true, readErr
		}
		return projection, true, errors.Join(err, failErr)
	}
	if errors.Is(err, model.ErrBillingStatsProjectionLeaseExpired) ||
		errors.Is(err, model.ErrBillingStatsProjectionNotClaimed) {
		return projection, true, err
	}

	retryErr := model.RetryClaimedBillingStatsProjection(ctx, projection.ID, owner, now, err)
	logger.LogWarn(ctx, fmt.Sprintf(
		"billing stats projection failed and will retry: operation=%s error=%s",
		projection.OperationKey, common.SanitizeErrorMessage(err.Error()),
	))
	return projection, true, errors.Join(err, retryErr)
}
