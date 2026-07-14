package model

import (
	"context"
	"time"
	"unicode/utf8"
)

const (
	billingStatsProjectionCompletedRetention = 90 * 24 * time.Hour
	billingStatsProjectionFailedRetention    = 365 * 24 * time.Hour
	billingStatsProjectionCleanupMaxBatch    = 500
)

func FindFailedBillingStatsProjections(
	ctx context.Context,
	afterID int64,
	limit int,
) ([]BillingStatsProjection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if afterID < 0 {
		return nil, ErrBillingStatsProjectionInvalid
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := DB.WithContext(ctx).Where("state = ?", BillingStatsProjectionStateFailed)
	if afterID > 0 {
		query = query.Where("id > ?", afterID)
	}
	var projections []BillingStatsProjection
	err := query.Order("id asc").Limit(limit).Find(&projections).Error
	return projections, err
}

func CountFailedBillingStatsProjections(ctx context.Context) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var count int64
	err := DB.WithContext(ctx).Model(&BillingStatsProjection{}).
		Where("state = ?", BillingStatsProjectionStateFailed).Count(&count).Error
	return count, err
}

func RequeueFailedBillingStatsProjection(
	ctx context.Context,
	projectionID int64,
	expectedFailureCode string,
	now time.Time,
) error {
	return requeueFailedBillingStatsProjection(ctx, projectionID, expectedFailureCode, 0, now)
}

func RequeueFailedBillingStatsProjectionAtVersion(
	ctx context.Context,
	projectionID int64,
	expectedFailureCode string,
	expectedUpdatedTimeMs int64,
	now time.Time,
) error {
	if expectedUpdatedTimeMs <= 0 {
		return ErrBillingStatsProjectionInvalid
	}
	return requeueFailedBillingStatsProjection(
		ctx, projectionID, expectedFailureCode, expectedUpdatedTimeMs, now,
	)
}

func requeueFailedBillingStatsProjection(
	ctx context.Context,
	projectionID int64,
	expectedFailureCode string,
	expectedUpdatedTimeMs int64,
	now time.Time,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if projectionID <= 0 || expectedFailureCode == "" || !utf8.ValidString(expectedFailureCode) ||
		len(expectedFailureCode) > billingStatsProjectionFailureMaxBytes {
		return ErrBillingStatsProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	query := DB.WithContext(ctx).Model(&BillingStatsProjection{}).
		Where("id = ? AND state = ? AND failure_code = ?",
			projectionID, BillingStatsProjectionStateFailed, expectedFailureCode)
	if expectedUpdatedTimeMs > 0 {
		query = query.Where("updated_time_ms = ?", expectedUpdatedTimeMs)
	}
	updated := query.
		Updates(map[string]any{
			"state":             BillingStatsProjectionStatePending,
			"lease_owner":       "",
			"lease_until_ms":    0,
			"attempts":          0,
			"next_retry_ms":     0,
			"last_error":        "",
			"failure_code":      "",
			"updated_time_ms":   now.UnixMilli(),
			"completed_time_ms": 0,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrBillingStatsProjectionConflict
	}
	return nil
}

// CleanupExpiredBillingStatsProjections only removes terminal receipts after
// their retention window, after the authoritative source proves the same
// immutable terminal identity, and after the external billing log receipt has
// disappeared. Missing schemas fail closed so startup or log-database outages
// cannot erase audit evidence.
func CleanupExpiredBillingStatsProjections(ctx context.Context, now time.Time, limit int) (int64, error) {
	deleted, _, _, err := CleanupExpiredBillingStatsProjectionsPage(ctx, now, 0, limit)
	return deleted, err
}

func CleanupExpiredBillingStatsProjectionsPage(
	ctx context.Context,
	now time.Time,
	afterID int64,
	limit int,
) (int64, int64, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if afterID < 0 {
		return 0, 0, false, ErrBillingStatsProjectionInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	if limit <= 0 || limit > billingStatsProjectionCleanupMaxBatch {
		limit = 100
	}
	completedBefore := now.Add(-billingStatsProjectionCompletedRetention).UnixMilli()
	failedBefore := now.Add(-billingStatsProjectionFailedRetention).UnixMilli()
	var candidates []BillingStatsProjection
	if err := DB.WithContext(ctx).
		Where("((state = ? AND completed_time_ms > 0 AND completed_time_ms <= ?) OR "+
			"(state = ? AND completed_time_ms > 0 AND completed_time_ms <= ?))",
			BillingStatsProjectionStateCompleted, completedBefore,
			BillingStatsProjectionStateFailed, failedBefore).
		Where("id > ?", afterID).
		Order("id asc").Limit(limit + 1).Find(&candidates).Error; err != nil {
		return 0, afterID, false, err
	}
	hasMore := len(candidates) > limit
	if hasMore {
		candidates = candidates[:limit]
	}

	references := make([]billingProjectionReference, 0, len(candidates))
	for i := range candidates {
		references = append(references, billingProjectionReference{
			ProjectionID: candidates[i].ID, Kind: candidates[i].Kind,
			ReferenceID: candidates[i].ReferenceID, OperationKey: candidates[i].OperationKey,
		})
	}
	protected, err := billingProjectionReferencesProtected(ctx, references)
	if err != nil {
		return 0, afterID, false, err
	}
	deletableIDs := make([]int64, 0, len(candidates))
	for i := range candidates {
		if !protected[candidates[i].ID] {
			deletableIDs = append(deletableIDs, candidates[i].ID)
		}
	}
	var deleted int64
	nextID := int64(0)
	if hasMore {
		nextID = candidates[len(candidates)-1].ID
	}
	if len(deletableIDs) > 0 {
		result := DB.WithContext(ctx).Where("id IN ?", deletableIDs).
			Where("((state = ? AND completed_time_ms > 0 AND completed_time_ms <= ?) OR "+
				"(state = ? AND completed_time_ms > 0 AND completed_time_ms <= ?))",
				BillingStatsProjectionStateCompleted, completedBefore,
				BillingStatsProjectionStateFailed, failedBefore).
			Delete(&BillingStatsProjection{})
		if result.Error != nil {
			return deleted, nextID, hasMore, result.Error
		}
		deleted += result.RowsAffected
	}
	return deleted, nextID, hasMore, nil
}
