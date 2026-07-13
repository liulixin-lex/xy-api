package model

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

const (
	RoutingErrorBudgetEvaluatorCursor = "enterprise_evaluator"
	RoutingErrorBudgetPublisherCursor = "event_publisher"

	routingErrorBudgetUnpublishedHistoryMax = 100_000
)

var (
	ErrRoutingErrorBudgetCursorInvalid   = errors.New("invalid channel routing error budget cursor")
	ErrRoutingErrorBudgetPublisherBehind = errors.New("channel routing error budget publisher backlog is full")
)

// RoutingErrorBudgetCursor is a lease-fenced cluster cursor. The evaluator uses
// PolicyRevision plus PositionID (last pool ID); the publisher uses PositionID
// as the last durably published history ID.
type RoutingErrorBudgetCursor struct {
	CursorName        string `json:"cursor_name" gorm:"type:varchar(32);primaryKey"`
	PolicyRevision    int64  `json:"policy_revision" gorm:"bigint;index;not null"`
	PositionID        int64  `json:"position_id" gorm:"bigint;index;not null"`
	LeaseName         string `json:"lease_name" gorm:"type:varchar(64);not null"`
	LeaseFencingToken int64  `json:"lease_fencing_token" gorm:"bigint;not null"`
	UpdatedTimeMs     int64  `json:"updated_time_ms" gorm:"bigint;index;not null"`
}

func (RoutingErrorBudgetCursor) TableName() string {
	return "routing_error_budget_cursors"
}

func GetRoutingErrorBudgetCursorContext(ctx context.Context, cursorName string) (RoutingErrorBudgetCursor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingErrorBudgetCursorName(cursorName) {
		return RoutingErrorBudgetCursor{}, ErrRoutingErrorBudgetCursorInvalid
	}
	var cursor RoutingErrorBudgetCursor
	err := DB.WithContext(ctx).Where("cursor_name = ?", cursorName).First(&cursor).Error
	return cursor, err
}

func SetRoutingErrorBudgetCursorContext(
	ctx context.Context,
	lease RoutingControlLease,
	cursor RoutingErrorBudgetCursor,
) (RoutingErrorBudgetCursor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validRoutingErrorBudgetCursor(lease, cursor) {
		return RoutingErrorBudgetCursor{}, ErrRoutingErrorBudgetCursorInvalid
	}
	var stored RoutingErrorBudgetCursor
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentLease RoutingControlLease
		if err := lockForUpdate(tx.WithContext(ctx)).Where("lease_name = ?", lease.LeaseName).First(&currentLease).Error; err != nil {
			return err
		}
		nowMs, err := routingDatabaseNowMs(tx.WithContext(ctx))
		if err != nil {
			return err
		}
		if currentLease.HolderID != lease.HolderID || currentLease.LeaseToken != lease.LeaseToken ||
			currentLease.FencingToken != lease.FencingToken || currentLease.LeaseUntilMs <= nowMs {
			return ErrRoutingControlLeaseLost
		}

		var existing RoutingErrorBudgetCursor
		loadErr := lockForUpdate(tx.WithContext(ctx)).Where("cursor_name = ?", cursor.CursorName).First(&existing).Error
		switch {
		case loadErr == nil:
			if !routingErrorBudgetCursorCanAdvance(existing, cursor) {
				return ErrRoutingErrorBudgetCursorInvalid
			}
			cursor.UpdatedTimeMs = nowMs
			if err := tx.WithContext(ctx).Model(&RoutingErrorBudgetCursor{}).
				Where("cursor_name = ?", cursor.CursorName).
				Updates(map[string]any{
					"policy_revision":     cursor.PolicyRevision,
					"position_id":         cursor.PositionID,
					"lease_name":          cursor.LeaseName,
					"lease_fencing_token": cursor.LeaseFencingToken,
					"updated_time_ms":     cursor.UpdatedTimeMs,
				}).Error; err != nil {
				return err
			}
		case errors.Is(loadErr, gorm.ErrRecordNotFound):
			cursor.UpdatedTimeMs = nowMs
			if err := tx.WithContext(ctx).Create(&cursor).Error; err != nil {
				return err
			}
		default:
			return loadErr
		}
		stored = cursor
		return nil
	})
	return stored, err
}

func routingErrorBudgetPublishedThroughTx(ctx context.Context, tx *gorm.DB) (int64, error) {
	var cursor RoutingErrorBudgetCursor
	result := tx.WithContext(ctx).Where("cursor_name = ?", RoutingErrorBudgetPublisherCursor).Limit(1).Find(&cursor)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, nil
	}
	if !validRoutingErrorBudgetCursorStored(cursor) {
		return 0, ErrRoutingErrorBudgetCursorInvalid
	}
	return cursor.PositionID, nil
}

func ensureRoutingErrorBudgetPublisherBacklogTx(ctx context.Context, tx *gorm.DB) error {
	publishedThrough, err := routingErrorBudgetPublishedThroughTx(ctx, tx)
	if err != nil {
		return err
	}
	var count int64
	if err := tx.WithContext(ctx).Model(&RoutingErrorBudgetHistory{}).
		Where("id > ?", publishedThrough).Count(&count).Error; err != nil {
		return err
	}
	if count >= routingErrorBudgetUnpublishedHistoryMax {
		return ErrRoutingErrorBudgetPublisherBehind
	}
	return nil
}

func validRoutingErrorBudgetCursorName(cursorName string) bool {
	return cursorName == RoutingErrorBudgetEvaluatorCursor || cursorName == RoutingErrorBudgetPublisherCursor
}

func validRoutingErrorBudgetCursor(lease RoutingControlLease, cursor RoutingErrorBudgetCursor) bool {
	if !validRoutingErrorBudgetCursorName(cursor.CursorName) ||
		!validRoutingControlLeaseText(lease.LeaseName, 64) || !validRoutingControlLeaseText(lease.HolderID, 128) ||
		len(lease.LeaseToken) != 32 || lease.FencingToken <= 0 || cursor.LeaseName != lease.LeaseName ||
		cursor.LeaseFencingToken != lease.FencingToken || cursor.PositionID < 0 {
		return false
	}
	if cursor.CursorName == RoutingErrorBudgetPublisherCursor {
		return cursor.PolicyRevision == 0
	}
	return (cursor.PolicyRevision == 0 && cursor.PositionID == 0) || cursor.PolicyRevision > 0
}

func validRoutingErrorBudgetCursorStored(cursor RoutingErrorBudgetCursor) bool {
	if !validRoutingErrorBudgetCursorName(cursor.CursorName) || !validRoutingControlLeaseText(cursor.LeaseName, 64) ||
		cursor.LeaseFencingToken <= 0 || cursor.PositionID < 0 || cursor.UpdatedTimeMs <= 0 {
		return false
	}
	if cursor.CursorName == RoutingErrorBudgetPublisherCursor {
		return cursor.PolicyRevision == 0
	}
	return (cursor.PolicyRevision == 0 && cursor.PositionID == 0) || cursor.PolicyRevision > 0
}

func routingErrorBudgetCursorCanAdvance(existing RoutingErrorBudgetCursor, incoming RoutingErrorBudgetCursor) bool {
	if !validRoutingErrorBudgetCursorStored(existing) || existing.CursorName != incoming.CursorName {
		return false
	}
	if incoming.CursorName == RoutingErrorBudgetPublisherCursor {
		return incoming.PositionID >= existing.PositionID
	}
	if incoming.PolicyRevision == 0 {
		return incoming.PositionID == 0
	}
	if existing.PolicyRevision == 0 {
		return true
	}
	if incoming.PolicyRevision < existing.PolicyRevision {
		return false
	}
	return incoming.PolicyRevision > existing.PolicyRevision || incoming.PositionID >= existing.PositionID
}
