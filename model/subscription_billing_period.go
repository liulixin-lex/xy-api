package model

import (
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SubscriptionBillingPeriod preserves the accounting identity of one quota
// period. UserSubscription.AmountUsed remains the fast current-period
// aggregate; this row is the durable source used by asynchronous reservations
// that may settle after the subscription has reset or been deleted.
type SubscriptionBillingPeriod struct {
	ID             int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	SubscriptionID int    `json:"subscription_id" gorm:"not null;uniqueIndex:uidx_subscription_billing_period,priority:1;index"`
	PeriodSequence int64  `json:"period_sequence" gorm:"not null;uniqueIndex:uidx_subscription_billing_period,priority:2"`
	UserID         int    `json:"user_id" gorm:"not null;index"`
	PeriodStart    int64  `json:"period_start" gorm:"not null"`
	PeriodEnd      int64  `json:"period_end" gorm:"not null"`
	AmountTotal    int64  `json:"amount_total" gorm:"not null"`
	AmountUsed     int64  `json:"amount_used" gorm:"not null"`
	ClosedTime     int64  `json:"closed_time" gorm:"index"`
	CloseReason    string `json:"close_reason,omitempty" gorm:"type:varchar(32)"`
	CreatedTime    int64  `json:"created_time" gorm:"not null"`
	UpdatedTime    int64  `json:"updated_time" gorm:"not null"`
}

func subscriptionPeriodBounds(sub *UserSubscription) (int64, int64) {
	if sub == nil {
		return 0, 0
	}
	start := sub.LastResetTime
	if start <= 0 {
		start = sub.StartTime
	}
	end := sub.NextResetTime
	if end <= 0 {
		end = sub.EndTime
	}
	return start, end
}

// ensureSubscriptionBillingPeriodTx requires the subscription row to already
// be locked by the caller. ON CONFLICT DO NOTHING keeps the path valid on
// PostgreSQL; catching a unique violation and continuing in the transaction
// would leave PostgreSQL's transaction aborted.
func ensureSubscriptionBillingPeriodTx(tx *gorm.DB, sub *UserSubscription, now int64) (*SubscriptionBillingPeriod, error) {
	if tx == nil || sub == nil || sub.Id <= 0 || sub.UserId <= 0 || sub.BillingPeriodSequence < 0 {
		return nil, errors.New("invalid subscription billing period")
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	start, end := subscriptionPeriodBounds(sub)
	period := &SubscriptionBillingPeriod{
		SubscriptionID: sub.Id,
		PeriodSequence: sub.BillingPeriodSequence,
		UserID:         sub.UserId,
		PeriodStart:    start,
		PeriodEnd:      end,
		AmountTotal:    sub.AmountTotal,
		AmountUsed:     sub.AmountUsed,
		CreatedTime:    now,
		UpdatedTime:    now,
	}
	created := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "subscription_id"}, {Name: "period_sequence"}},
		DoNothing: true,
	}).Create(period)
	if created.Error != nil {
		return nil, created.Error
	}
	if created.RowsAffected == 0 {
		if err := lockForUpdate(tx).
			Where("subscription_id = ? AND period_sequence = ?", sub.Id, sub.BillingPeriodSequence).
			First(period).Error; err != nil {
			return nil, err
		}
	}
	if period.UserID != sub.UserId || period.AmountTotal != sub.AmountTotal {
		return nil, fmt.Errorf("subscription billing period identity mismatch")
	}
	return period, nil
}

func closeSubscriptionBillingPeriodTx(tx *gorm.DB, sub *UserSubscription, now int64, reason string) error {
	period, err := ensureSubscriptionBillingPeriodTx(tx, sub, now)
	if err != nil {
		return err
	}
	if period.AmountUsed != sub.AmountUsed {
		return fmt.Errorf("subscription billing period amount mismatch: period=%d subscription=%d", period.AmountUsed, sub.AmountUsed)
	}
	if period.ClosedTime > 0 {
		return nil
	}
	updated := tx.Model(&SubscriptionBillingPeriod{}).
		Where("id = ? AND closed_time = ?", period.ID, 0).
		Updates(map[string]any{
			"closed_time":  now,
			"close_reason": reason,
			"updated_time": now,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return errors.New("subscription billing period close lost")
	}
	return nil
}

// applySubscriptionBillingPeriodDeltaTx applies a delta to the immutable
// period first and mirrors it to UserSubscription only while that exact period
// is still current. A refund from an old period therefore cannot make the new
// period negative after a reset.
func applySubscriptionBillingPeriodDeltaTx(
	tx *gorm.DB,
	subscriptionID int,
	periodID int64,
	userID int,
	delta int64,
	now int64,
) error {
	if tx == nil || subscriptionID <= 0 || periodID <= 0 || userID <= 0 {
		return errors.New("invalid subscription billing period delta")
	}
	if delta == 0 {
		return nil
	}
	var sub UserSubscription
	subErr := lockForUpdate(tx).Where("id = ?", subscriptionID).First(&sub).Error
	if subErr != nil && !errors.Is(subErr, gorm.ErrRecordNotFound) {
		return subErr
	}

	var period SubscriptionBillingPeriod
	if err := lockForUpdate(tx).Where("id = ?", periodID).First(&period).Error; err != nil {
		return err
	}
	if period.SubscriptionID != subscriptionID || period.UserID != userID {
		return errors.New("subscription billing period ownership mismatch")
	}
	if period.AmountUsed < 0 || period.AmountTotal < 0 {
		return errors.New("subscription billing period amounts are invalid")
	}
	if delta > 0 && period.AmountUsed > math.MaxInt64-delta {
		return errors.New("subscription billing period used amount overflow")
	}
	if delta < 0 && delta < -period.AmountUsed {
		return fmt.Errorf("subscription period refund exceeds used amount, used=%d delta=%d", period.AmountUsed, delta)
	}
	newUsed := period.AmountUsed + delta
	if period.AmountTotal > 0 && newUsed > period.AmountTotal {
		return fmt.Errorf("subscription period used exceeds total, used=%d total=%d", newUsed, period.AmountTotal)
	}

	if subErr == nil && sub.UserId == userID && sub.BillingPeriodSequence == period.PeriodSequence {
		if sub.AmountUsed != period.AmountUsed {
			return fmt.Errorf("current subscription period amount mismatch: subscription=%d period=%d", sub.AmountUsed, period.AmountUsed)
		}
		updatedSub := tx.Model(&UserSubscription{}).Where(
			"id = ? AND billing_period_sequence = ? AND amount_used = ?",
			subscriptionID, period.PeriodSequence, sub.AmountUsed,
		).Updates(map[string]any{"amount_used": newUsed, "updated_at": now})
		if updatedSub.Error != nil {
			return updatedSub.Error
		}
		if updatedSub.RowsAffected != 1 {
			return errors.New("subscription period aggregate update lost")
		}
	}

	updatedPeriod := tx.Model(&SubscriptionBillingPeriod{}).
		Where("id = ? AND amount_used = ?", period.ID, period.AmountUsed).
		Updates(map[string]any{"amount_used": newUsed, "updated_time": now})
	if updatedPeriod.Error != nil {
		return updatedPeriod.Error
	}
	if updatedPeriod.RowsAffected != 1 {
		return errors.New("subscription billing period delta update lost")
	}
	return nil
}
