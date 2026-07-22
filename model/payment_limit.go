package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	PaymentLimitReservationActive   = "active"
	PaymentLimitReservationPaid     = "paid"
	PaymentLimitReservationReleased = "released"
	paymentLimitProviderClockSkew   = int64(5 * 60)
)

var (
	ErrPaymentSingleLimitExceeded = errors.New("payment amount exceeds the channel single-payment limit")
	ErrPaymentDailyLimitExceeded  = errors.New("payment channel daily limit is unavailable")
	ErrPaymentLimitDayBoundary    = errors.New("payment order would cross the configured daily limit boundary")
	ErrPaymentLimitTimezoneLocked = errors.New("payment limit timezone cannot change after usage exists")
)

// PaymentLimitPolicy is merchant configuration, not a provider-wide constant.
// A missing or disabled row means that this image does not enforce an
// additional channel limit. Amounts are always stored in minor currency units.
type PaymentLimitPolicy struct {
	ID               int64  `json:"id" gorm:"primaryKey"`
	Provider         string `json:"provider" gorm:"type:varchar(32);uniqueIndex:idx_payment_limit_policy_channel_v2,priority:1"`
	PaymentMethod    string `json:"payment_method" gorm:"type:varchar(64);uniqueIndex:idx_payment_limit_policy_channel_v2,priority:2"`
	Currency         string `json:"currency" gorm:"type:varchar(8);uniqueIndex:idx_payment_limit_policy_channel_v2,priority:3"`
	SingleLimitMinor int64  `json:"single_limit_minor"`
	DailyLimitMinor  int64  `json:"daily_limit_minor"`
	Timezone         string `json:"timezone" gorm:"type:varchar(64)"`
	Enabled          bool   `json:"enabled" gorm:"index"`
	CreatedAt        int64  `json:"created_at" gorm:"index"`
	UpdatedAt        int64  `json:"updated_at" gorm:"index"`
	Version          int64  `json:"version"`
}

type PaymentLimitBucket struct {
	ID            int64  `json:"id" gorm:"primaryKey"`
	Provider      string `json:"provider" gorm:"type:varchar(32);uniqueIndex:idx_payment_limit_bucket_v2,priority:1"`
	PaymentMethod string `json:"payment_method" gorm:"type:varchar(64);uniqueIndex:idx_payment_limit_bucket_v2,priority:2"`
	Currency      string `json:"currency" gorm:"type:varchar(8);uniqueIndex:idx_payment_limit_bucket_v2,priority:3"`
	DayKey        string `json:"day_key" gorm:"type:varchar(16);uniqueIndex:idx_payment_limit_bucket_v2,priority:4"`
	ReservedMinor int64  `json:"reserved_minor"`
	PaidMinor     int64  `json:"paid_minor"`
	CreatedAt     int64  `json:"created_at" gorm:"index"`
	UpdatedAt     int64  `json:"updated_at" gorm:"index"`
	Version       int64  `json:"version"`
}

type PaymentLimitReservation struct {
	ID             int64  `json:"id" gorm:"primaryKey"`
	PaymentOrderID int64  `json:"payment_order_id" gorm:"uniqueIndex"`
	Provider       string `json:"provider" gorm:"type:varchar(32);index"`
	PaymentMethod  string `json:"payment_method" gorm:"type:varchar(64);index"`
	Currency       string `json:"currency" gorm:"type:varchar(8);index"`
	DayKey         string `json:"day_key" gorm:"type:varchar(16);index"`
	PaidDayKey     string `json:"paid_day_key,omitempty" gorm:"type:varchar(16);index"`
	PaidAt         int64  `json:"paid_at,omitempty" gorm:"index"`
	AmountMinor    int64  `json:"amount_minor"`
	Status         string `json:"status" gorm:"type:varchar(24);index"`
	ExpiresAt      int64  `json:"expires_at" gorm:"index"`
	OverLimit      bool   `json:"over_limit" gorm:"index"`
	CreatedAt      int64  `json:"created_at" gorm:"index"`
	UpdatedAt      int64  `json:"updated_at" gorm:"index"`
}

func normalizePaymentLimitPolicy(policy *PaymentLimitPolicy) error {
	if policy == nil {
		return errors.New("payment limit policy is required")
	}
	policy.Provider = strings.ToLower(strings.TrimSpace(policy.Provider))
	policy.PaymentMethod = NormalizePaymentMethodForStorage(policy.Provider, policy.PaymentMethod)
	policy.Currency = strings.ToUpper(strings.TrimSpace(policy.Currency))
	policy.Timezone = strings.TrimSpace(policy.Timezone)
	if policy.Provider == "" || policy.PaymentMethod == "" || !validPaymentLimitCurrency(policy.Provider, policy.Currency) ||
		policy.SingleLimitMinor < 0 || policy.DailyLimitMinor < 0 {
		return errors.New("invalid payment limit policy")
	}
	switch policy.Provider {
	case PaymentProviderEpay:
		// Epay installations may use merchant-defined method identifiers.
	case PaymentProviderStripe:
		if policy.PaymentMethod != PaymentMethodStripe {
			return errors.New("invalid Stripe payment limit method")
		}
	case PaymentProviderXorPay:
		if policy.PaymentMethod != PaymentMethodXorPayNative &&
			policy.PaymentMethod != PaymentMethodXorPayAlipay &&
			policy.PaymentMethod != PaymentMethodXorPayJSAPI {
			return errors.New("invalid XORPay payment limit method")
		}
	case PaymentProviderCreem:
		if policy.PaymentMethod != PaymentMethodCreem {
			return errors.New("invalid Creem payment limit method")
		}
	case PaymentProviderWaffo:
		if policy.PaymentMethod != PaymentMethodWaffo {
			return errors.New("invalid Waffo payment limit method")
		}
	case PaymentProviderWaffoPancake:
		if policy.PaymentMethod != PaymentMethodWaffoPancake {
			return errors.New("invalid Waffo Pancake payment limit method")
		}
	default:
		return errors.New("payment limits are not supported for this provider")
	}
	if policy.Timezone == "" {
		policy.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(policy.Timezone); err != nil {
		return fmt.Errorf("invalid payment limit timezone: %w", err)
	}
	if policy.SingleLimitMinor > 0 && policy.DailyLimitMinor > 0 && policy.SingleLimitMinor > policy.DailyLimitMinor {
		return errors.New("single-payment limit cannot exceed the daily limit")
	}
	return nil
}

type PaymentLimitReconciliationResult struct {
	Scanned  int `json:"scanned"`
	Settled  int `json:"settled"`
	Released int `json:"released"`
	Skipped  int `json:"skipped"`
}

// ReconcilePaymentLimitReservations repairs the accounting projection when an
// older application node changes a payment order without knowing about the
// v0.2.1 reservation tables. Released reservations are intentionally eligible
// for later settlement because a delayed callback may prove that an expired
// order was paid after its capacity reservation was released.
func ReconcilePaymentLimitReservations(ctx context.Context, now int64, limit int) (PaymentLimitReconciliationResult, error) {
	result := PaymentLimitReconciliationResult{}
	if ctx == nil {
		ctx = context.Background()
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	if limit <= 0 {
		return result, nil
	}
	paidStatuses := []string{
		PaymentOrderStatusPaid,
		PaymentOrderStatusFulfilled,
		PaymentOrderStatusRefundPending,
		PaymentOrderStatusRefunded,
		PaymentOrderStatusDisputed,
		PaymentOrderStatusDebt,
	}
	var reservationIDs []int64
	err := DB.WithContext(ctx).Model(&PaymentLimitReservation{}).
		Joins("LEFT JOIN payment_orders ON payment_orders.id = payment_limit_reservations.payment_order_id").
		Where("payment_limit_reservations.status = ? OR (payment_limit_reservations.status = ? AND payment_orders.status IN ?)",
			PaymentLimitReservationActive, PaymentLimitReservationReleased, paidStatuses).
		Order("payment_limit_reservations.id ASC").Limit(limit).
		Pluck("payment_limit_reservations.id", &reservationIDs).Error
	if err != nil {
		return result, err
	}
	for _, reservationID := range reservationIDs {
		action := "skipped"
		err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var reservation PaymentLimitReservation
			if err := lockForUpdate(tx).Where("id = ?", reservationID).First(&reservation).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if reservation.Status == PaymentLimitReservationPaid {
				return nil
			}
			var order PaymentOrder
			if err := lockForUpdate(tx).Where("id = ?", reservation.PaymentOrderID).First(&order).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) && reservation.Status == PaymentLimitReservationActive {
					if err := releasePaymentLimitReservationTx(tx, &PaymentOrder{ID: reservation.PaymentOrderID}, now); err != nil {
						return err
					}
					action = "released"
					return nil
				}
				return err
			}
			switch order.Status {
			case PaymentOrderStatusPaid, PaymentOrderStatusFulfilled, PaymentOrderStatusRefundPending,
				PaymentOrderStatusRefunded, PaymentOrderStatusDisputed, PaymentOrderStatusDebt:
				if err := settlePaymentLimitReservationTx(tx, &order, now); err != nil {
					return err
				}
				action = "settled"
			case PaymentOrderStatusFailed, PaymentOrderStatusExpired:
				if reservation.Status == PaymentLimitReservationActive {
					if err := releasePaymentLimitReservationTx(tx, &order, now); err != nil {
						return err
					}
					action = "released"
				}
			default:
				if reservation.Status == PaymentLimitReservationActive && reservation.ExpiresAt > 0 && reservation.ExpiresAt <= now {
					if err := releasePaymentLimitReservationTx(tx, &order, now); err != nil {
						return err
					}
					action = "released"
				}
			}
			return nil
		})
		if err != nil {
			return result, err
		}
		result.Scanned++
		switch action {
		case "settled":
			result.Settled++
		case "released":
			result.Released++
		default:
			result.Skipped++
		}
	}
	return result, nil
}

func validPaymentLimitCurrency(provider, currency string) bool {
	_, ok := common.PaymentProviderCurrencyExponentOK(provider, currency)
	return ok
}

// NormalizePaymentMethodForStorage preserves Epay custom identifiers while
// keeping protocol-defined provider methods canonical. It lives in model to
// avoid a model-to-service dependency.
func NormalizePaymentMethodForStorage(provider, method string) string {
	method = strings.TrimSpace(method)
	if strings.EqualFold(strings.TrimSpace(provider), PaymentProviderEpay) {
		return method
	}
	return strings.ToLower(method)
}

func UpsertPaymentLimitPolicy(policy *PaymentLimitPolicy) error {
	if err := normalizePaymentLimitPolicy(policy); err != nil {
		return err
	}
	now := common.GetTimestamp()
	return DB.Transaction(func(tx *gorm.DB) error {
		var existing PaymentLimitPolicy
		err := lockForUpdate(tx).Where("provider = ? AND payment_method = ? AND currency = ?", policy.Provider, policy.PaymentMethod, policy.Currency).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			policy.CreatedAt = now
			policy.UpdatedAt = now
			policy.Version = 1
			return tx.Create(policy).Error
		}
		if err != nil {
			return err
		}
		existingTimezone := strings.TrimSpace(existing.Timezone)
		if existingTimezone == "" {
			existingTimezone = "UTC"
		}
		if existingTimezone != policy.Timezone {
			var bucketCount int64
			if err := tx.Model(&PaymentLimitBucket{}).
				Where("provider = ? AND payment_method = ? AND currency = ?", policy.Provider, policy.PaymentMethod, policy.Currency).
				Count(&bucketCount).Error; err != nil {
				return err
			}
			var reservationCount int64
			if err := tx.Model(&PaymentLimitReservation{}).
				Where("provider = ? AND payment_method = ? AND currency = ?", policy.Provider, policy.PaymentMethod, policy.Currency).
				Count(&reservationCount).Error; err != nil {
				return err
			}
			if bucketCount > 0 || reservationCount > 0 {
				return ErrPaymentLimitTimezoneLocked
			}
		}
		updates := map[string]interface{}{
			"single_limit_minor": policy.SingleLimitMinor,
			"daily_limit_minor":  policy.DailyLimitMinor,
			"timezone":           policy.Timezone,
			"enabled":            policy.Enabled,
			"updated_at":         now,
			"version":            gorm.Expr("version + ?", 1),
		}
		return tx.Model(&PaymentLimitPolicy{}).Where("id = ?", existing.ID).Updates(updates).Error
	})
}

func ListPaymentLimitPolicies() ([]PaymentLimitPolicy, error) {
	var policies []PaymentLimitPolicy
	err := DB.Order("provider ASC, payment_method ASC, currency ASC").Find(&policies).Error
	return policies, err
}

func GetPaymentLimitPolicy(provider, paymentMethod, currency string) (*PaymentLimitPolicy, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	paymentMethod = NormalizePaymentMethodForStorage(provider, paymentMethod)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	var policy PaymentLimitPolicy
	err := DB.Where("provider = ? AND payment_method = ? AND currency = ?", provider, paymentMethod, currency).First(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &policy, err
}

func CheckPaymentLimitForQuote(provider, paymentMethod, currency string, amountMinor, now int64) error {
	if amountMinor <= 0 {
		return errors.New("invalid payment amount")
	}
	policy, err := GetPaymentLimitPolicy(provider, paymentMethod, currency)
	if err != nil || policy == nil || !policy.Enabled {
		return err
	}
	if err := normalizePaymentLimitPolicy(policy); err != nil {
		return err
	}
	if policy.SingleLimitMinor > 0 && amountMinor > policy.SingleLimitMinor {
		return ErrPaymentSingleLimitExceeded
	}
	if policy.DailyLimitMinor == 0 {
		return nil
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	dayKey, err := paymentLimitDayKey(now, policy.Timezone)
	if err != nil {
		return err
	}
	var bucket PaymentLimitBucket
	err = DB.Where("provider = ? AND payment_method = ? AND currency = ? AND day_key = ?", policy.Provider, policy.PaymentMethod, policy.Currency, dayKey).
		First(&bucket).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if paymentLimitWouldExceed(policy.DailyLimitMinor, bucket.PaidMinor, bucket.ReservedMinor, amountMinor) {
		return ErrPaymentDailyLimitExceeded
	}
	return nil
}

// paymentLimitWouldExceed compares capacity without adding attacker- or
// operator-controlled int64 values. Negative persisted counters fail closed.
func paymentLimitWouldExceed(limitMinor, paidMinor, reservedMinor, amountMinor int64) bool {
	if limitMinor <= 0 {
		return false
	}
	if paidMinor < 0 || reservedMinor < 0 || amountMinor < 0 || paidMinor > limitMinor {
		return true
	}
	remaining := limitMinor - paidMinor
	if reservedMinor > remaining {
		return true
	}
	return amountMinor > remaining-reservedMinor
}

func addPaymentLimitMinor(current, delta int64) (int64, error) {
	if current < 0 || delta < 0 || current > math.MaxInt64-delta {
		return 0, errors.New("payment limit counter overflow")
	}
	return current + delta, nil
}

func paymentLimitDayKey(timestamp int64, timezone string) (string, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return "", err
	}
	return time.Unix(timestamp, 0).In(location).Format("2006-01-02"), nil
}

func paymentLimitNextDayBoundary(query *gorm.DB, provider, paymentMethod, currency string, now int64) (int64, bool, error) {
	if query == nil {
		return 0, false, errors.New("payment limit database is unavailable")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	paymentMethod = NormalizePaymentMethodForStorage(provider, paymentMethod)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if now <= 0 {
		now = common.GetTimestamp()
	}
	var policy PaymentLimitPolicy
	err := query.Where("provider = ? AND payment_method = ? AND currency = ?", provider, paymentMethod, currency).First(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !policy.Enabled || policy.DailyLimitMinor <= 0 {
		return 0, false, nil
	}
	if err := normalizePaymentLimitPolicy(&policy); err != nil {
		return 0, false, err
	}
	location, err := time.LoadLocation(policy.Timezone)
	if err != nil {
		return 0, false, err
	}
	localNow := time.Unix(now, 0).In(location)
	boundary := time.Date(localNow.Year(), localNow.Month(), localNow.Day()+1, 0, 0, 0, 0, location).Unix()
	if boundary <= now {
		return 0, false, errors.New("invalid payment limit day boundary")
	}
	return boundary, true, nil
}

// BoundPaymentQuoteExpiryForLimit prevents a quote from authorizing an order
// whose active capacity reservation would span two merchant accounting days.
// Stripe needs a fixed provider-compatible order window, so its quote must
// expire early enough to leave the whole window before the next day boundary.
func BoundPaymentQuoteExpiryForLimit(provider, paymentMethod, currency string, now, expiresAt int64) (int64, error) {
	if now <= 0 {
		now = common.GetTimestamp()
	}
	if expiresAt <= now {
		return 0, ErrPaymentLimitDayBoundary
	}
	boundary, enforced, err := paymentLimitNextDayBoundary(DB, provider, paymentMethod, currency, now)
	if err != nil || !enforced {
		return expiresAt, err
	}
	latestExpiry := boundary
	if strings.EqualFold(strings.TrimSpace(provider), PaymentProviderStripe) {
		latestExpiry -= PaymentStripeOrderTTLSeconds
	}
	if latestExpiry <= now {
		return 0, ErrPaymentLimitDayBoundary
	}
	if expiresAt > latestExpiry {
		expiresAt = latestExpiry
	}
	return expiresAt, nil
}

func boundPaymentOrderExpiryForLimitTx(tx *gorm.DB, quote *PaymentQuote, now, proposedExpiresAt int64) (int64, error) {
	if tx == nil || quote == nil || now <= 0 || proposedExpiresAt <= now {
		return 0, ErrPaymentLimitDayBoundary
	}
	boundary, enforced, err := paymentLimitNextDayBoundary(
		lockForUpdate(tx), quote.Provider, quote.PaymentMethod, quote.Currency, now,
	)
	if err != nil || !enforced {
		return proposedExpiresAt, err
	}
	if quote.Provider == PaymentProviderStripe && proposedExpiresAt > boundary {
		// Shortening a Stripe order below its server-owned provider window could
		// make the upstream Checkout Session invalid. Require a fresh quote on
		// the next merchant day instead.
		return 0, ErrPaymentLimitDayBoundary
	}
	if proposedExpiresAt > boundary {
		proposedExpiresAt = boundary
	}
	return proposedExpiresAt, nil
}

func ensurePaymentLimitBucketTx(tx *gorm.DB, provider, paymentMethod, currency, dayKey string, now int64) (*PaymentLimitBucket, error) {
	bucket := &PaymentLimitBucket{
		Provider: provider, PaymentMethod: paymentMethod, Currency: currency, DayKey: dayKey,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "provider"}, {Name: "payment_method"}, {Name: "currency"}, {Name: "day_key"}},
		DoNothing: true,
	}).Create(bucket).Error; err != nil {
		return nil, err
	}
	// A real write serializes the capacity check on SQLite as well as on
	// databases where SELECT FOR UPDATE is available.
	if err := tx.Model(&PaymentLimitBucket{}).
		Where("provider = ? AND payment_method = ? AND currency = ? AND day_key = ?", provider, paymentMethod, currency, dayKey).
		Updates(map[string]interface{}{"updated_at": now, "version": gorm.Expr("version + ?", 1)}).Error; err != nil {
		return nil, err
	}
	var stored PaymentLimitBucket
	if err := lockForUpdate(tx).Where("provider = ? AND payment_method = ? AND currency = ? AND day_key = ?", provider, paymentMethod, currency, dayKey).
		First(&stored).Error; err != nil {
		return nil, err
	}
	return &stored, nil
}

func releaseExpiredPaymentLimitReservationsTx(tx *gorm.DB, bucket *PaymentLimitBucket, now int64) error {
	if tx == nil || bucket == nil {
		return errors.New("invalid payment limit bucket")
	}
	if bucket.ReservedMinor < 0 {
		return errors.New("payment limit reserved counter is invalid")
	}
	var reservations []PaymentLimitReservation
	if err := lockForUpdate(tx).
		Where("provider = ? AND payment_method = ? AND currency = ? AND day_key = ? AND status = ? AND expires_at > 0 AND expires_at <= ?",
			bucket.Provider, bucket.PaymentMethod, bucket.Currency, bucket.DayKey, PaymentLimitReservationActive, now).
		Find(&reservations).Error; err != nil {
		return err
	}
	if len(reservations) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(reservations))
	released := int64(0)
	for i := range reservations {
		ids = append(ids, reservations[i].ID)
		if reservations[i].AmountMinor <= 0 {
			continue
		}
		if released >= bucket.ReservedMinor || reservations[i].AmountMinor >= bucket.ReservedMinor-released {
			released = bucket.ReservedMinor
			continue
		}
		released += reservations[i].AmountMinor
	}
	if err := tx.Model(&PaymentLimitReservation{}).Where("id IN ? AND status = ?", ids, PaymentLimitReservationActive).
		Updates(map[string]interface{}{"status": PaymentLimitReservationReleased, "updated_at": now}).Error; err != nil {
		return err
	}
	if released > bucket.ReservedMinor {
		released = bucket.ReservedMinor
	}
	bucket.ReservedMinor -= released
	return tx.Model(&PaymentLimitBucket{}).Where("id = ?", bucket.ID).
		Updates(map[string]interface{}{
			"reserved_minor": bucket.ReservedMinor,
			"updated_at":     now,
			"version":        gorm.Expr("version + ?", 1),
		}).Error
}

func reservePaymentLimitTx(tx *gorm.DB, order *PaymentOrder) error {
	return reservePaymentLimitTxAt(tx, order, common.GetTimestamp())
}

func reservePaymentLimitTxAt(tx *gorm.DB, order *PaymentOrder, now int64) error {
	if tx == nil || order == nil || order.ID <= 0 || order.ExpectedAmountMinor <= 0 {
		return errors.New("invalid payment limit reservation")
	}
	if now <= 0 {
		return errors.New("invalid payment limit reservation time")
	}
	var policy PaymentLimitPolicy
	err := lockForUpdate(tx).
		Where("provider = ? AND payment_method = ? AND currency = ?", order.Provider, order.PaymentMethod, strings.ToUpper(order.Currency)).
		First(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && !policy.Enabled {
		return nil
	}
	if err != nil {
		return err
	}
	if err := normalizePaymentLimitPolicy(&policy); err != nil {
		return err
	}
	if policy.SingleLimitMinor > 0 && order.ExpectedAmountMinor > policy.SingleLimitMinor {
		return ErrPaymentSingleLimitExceeded
	}
	dayKey, err := paymentLimitDayKey(now, policy.Timezone)
	if err != nil {
		return err
	}
	bucket, err := ensurePaymentLimitBucketTx(tx, order.Provider, order.PaymentMethod, policy.Currency, dayKey, now)
	if err != nil {
		return err
	}
	if err := releaseExpiredPaymentLimitReservationsTx(tx, bucket, now); err != nil {
		return err
	}
	if paymentLimitWouldExceed(policy.DailyLimitMinor, bucket.PaidMinor, bucket.ReservedMinor, order.ExpectedAmountMinor) {
		return ErrPaymentDailyLimitExceeded
	}
	reservation := &PaymentLimitReservation{
		PaymentOrderID: order.ID,
		Provider:       order.Provider,
		PaymentMethod:  order.PaymentMethod,
		Currency:       policy.Currency,
		DayKey:         dayKey,
		AmountMinor:    order.ExpectedAmountMinor,
		Status:         PaymentLimitReservationActive,
		ExpiresAt:      order.ExpiresAt,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := tx.Create(reservation).Error; err != nil {
		return err
	}
	bucket.ReservedMinor, err = addPaymentLimitMinor(bucket.ReservedMinor, order.ExpectedAmountMinor)
	if err != nil {
		return err
	}
	return tx.Model(&PaymentLimitBucket{}).Where("id = ?", bucket.ID).
		Updates(map[string]interface{}{
			"reserved_minor": bucket.ReservedMinor,
			"updated_at":     now,
			"version":        gorm.Expr("version + ?", 1),
		}).Error
}

func updatePaymentLimitReservationExpiryTx(tx *gorm.DB, paymentOrderID, expiresAt int64) error {
	if tx == nil || paymentOrderID <= 0 || expiresAt <= 0 {
		return nil
	}
	return tx.Model(&PaymentLimitReservation{}).
		Where("payment_order_id = ? AND status = ?", paymentOrderID, PaymentLimitReservationActive).
		Updates(map[string]interface{}{"expires_at": expiresAt, "updated_at": common.GetTimestamp()}).Error
}

func releasePaymentLimitReservationTx(tx *gorm.DB, order *PaymentOrder, now int64) error {
	if tx == nil || order == nil || order.ID <= 0 {
		return nil
	}
	var reservation PaymentLimitReservation
	err := lockForUpdate(tx).Where("payment_order_id = ?", order.ID).First(&reservation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && reservation.Status != PaymentLimitReservationActive {
		return nil
	}
	if err != nil {
		return err
	}
	if reservation.AmountMinor <= 0 {
		return errors.New("payment limit reservation amount is invalid")
	}
	bucket, err := ensurePaymentLimitBucketTx(tx, reservation.Provider, reservation.PaymentMethod, reservation.Currency, reservation.DayKey, now)
	if err != nil {
		return err
	}
	if bucket.ReservedMinor < 0 {
		return errors.New("payment limit reserved counter is invalid")
	}
	released := reservation.AmountMinor
	if released > bucket.ReservedMinor {
		released = bucket.ReservedMinor
	}
	if err := tx.Model(&PaymentLimitReservation{}).Where("id = ? AND status = ?", reservation.ID, PaymentLimitReservationActive).
		Updates(map[string]interface{}{"status": PaymentLimitReservationReleased, "updated_at": now}).Error; err != nil {
		return err
	}
	return tx.Model(&PaymentLimitBucket{}).Where("id = ?", bucket.ID).
		Updates(map[string]interface{}{
			"reserved_minor": bucket.ReservedMinor - released,
			"updated_at":     now,
			"version":        gorm.Expr("version + ?", 1),
		}).Error
}

func settlePaymentLimitReservationTx(tx *gorm.DB, order *PaymentOrder, now int64) error {
	paidAt := now
	if order != nil && order.SettledAt > 0 {
		paidAt = order.SettledAt
	}
	if tx != nil && order != nil && order.ID > 0 {
		var event PaymentEvent
		if err := tx.Where("payment_order_id = ? AND paid = ?", order.ID, true).
			Order("created_at ASC, id ASC").First(&event).Error; err == nil {
			paidAt = paymentLimitPaidAtForEvent(&event, order, now)
		}
	}
	return settlePaymentLimitReservationAtTx(tx, order, paidAt, now)
}

func paymentLimitPaidAtForEvent(event *PaymentEvent, order *PaymentOrder, receivedAt int64) int64 {
	if receivedAt <= 0 {
		receivedAt = common.GetTimestamp()
	}
	if event == nil {
		return receivedAt
	}
	if event.CreatedAt > 0 && event.CreatedAt <= receivedAt+paymentLimitProviderClockSkew {
		receivedAt = event.CreatedAt
	}
	if event.Provider != PaymentProviderStripe || event.ProviderCreatedAt <= 0 {
		return receivedAt
	}
	lowerBound := int64(1)
	if order != nil && order.CreatedAt > paymentLimitProviderClockSkew {
		lowerBound = order.CreatedAt - paymentLimitProviderClockSkew
	}
	if event.ProviderCreatedAt < lowerBound || event.ProviderCreatedAt > receivedAt+paymentLimitProviderClockSkew {
		return receivedAt
	}
	return event.ProviderCreatedAt
}

func settlePaymentLimitReservationAtTx(tx *gorm.DB, order *PaymentOrder, paidAt, now int64) error {
	if tx == nil || order == nil || order.ID <= 0 {
		return nil
	}
	if paidAt <= 0 {
		paidAt = now
	}
	var reservation PaymentLimitReservation
	err := lockForUpdate(tx).Where("payment_order_id = ?", order.ID).First(&reservation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && reservation.Status == PaymentLimitReservationPaid {
		return nil
	}
	if err != nil {
		return err
	}
	if reservation.AmountMinor <= 0 {
		return errors.New("payment limit reservation amount is invalid")
	}
	var policy PaymentLimitPolicy
	policyErr := tx.Where("provider = ? AND payment_method = ? AND currency = ?", reservation.Provider, reservation.PaymentMethod, reservation.Currency).First(&policy).Error
	if policyErr != nil && !errors.Is(policyErr, gorm.ErrRecordNotFound) {
		return policyErr
	}
	timezone := "UTC"
	if policyErr == nil && strings.TrimSpace(policy.Timezone) != "" {
		timezone = policy.Timezone
	}
	paidDayKey, err := paymentLimitDayKey(paidAt, timezone)
	if err != nil {
		return err
	}
	if reservation.Status == PaymentLimitReservationActive {
		reservedBucket, err := ensurePaymentLimitBucketTx(tx, reservation.Provider, reservation.PaymentMethod, reservation.Currency, reservation.DayKey, now)
		if err != nil {
			return err
		}
		if reservedBucket.ReservedMinor < 0 {
			return errors.New("payment limit reserved counter is invalid")
		}
		released := reservation.AmountMinor
		if released > reservedBucket.ReservedMinor {
			released = reservedBucket.ReservedMinor
		}
		if err := tx.Model(&PaymentLimitBucket{}).Where("id = ?", reservedBucket.ID).
			Updates(map[string]interface{}{
				"reserved_minor": reservedBucket.ReservedMinor - released,
				"updated_at":     now,
				"version":        gorm.Expr("version + ?", 1),
			}).Error; err != nil {
			return err
		}
	}
	paidBucket, err := ensurePaymentLimitBucketTx(tx, reservation.Provider, reservation.PaymentMethod, reservation.Currency, paidDayKey, now)
	if err != nil {
		return err
	}
	overLimit := policyErr == nil && paymentLimitWouldExceed(policy.DailyLimitMinor, paidBucket.PaidMinor, 0, reservation.AmountMinor)
	paidMinor, err := addPaymentLimitMinor(paidBucket.PaidMinor, reservation.AmountMinor)
	if err != nil {
		return err
	}
	if err := tx.Model(&PaymentLimitBucket{}).Where("id = ?", paidBucket.ID).
		Updates(map[string]interface{}{
			"paid_minor": paidMinor,
			"updated_at": now,
			"version":    gorm.Expr("version + ?", 1),
		}).Error; err != nil {
		return err
	}
	return tx.Model(&PaymentLimitReservation{}).Where("id = ?", reservation.ID).
		Updates(map[string]interface{}{
			"status":       PaymentLimitReservationPaid,
			"paid_day_key": paidDayKey,
			"paid_at":      paidAt,
			"over_limit":   overLimit,
			"updated_at":   now,
		}).Error
}

type PaymentLimitUsage struct {
	Policy           PaymentLimitPolicy `json:"policy"`
	DayKey           string             `json:"day_key"`
	CurrencyExponent int32              `json:"currency_exponent"`
	ReservedMinor    int64              `json:"reserved_minor"`
	PaidMinor        int64              `json:"paid_minor"`
}

func CurrentPaymentLimitUsage(policy PaymentLimitPolicy, now int64) (*PaymentLimitUsage, error) {
	if err := normalizePaymentLimitPolicy(&policy); err != nil {
		return nil, err
	}
	if now <= 0 {
		now = common.GetTimestamp()
	}
	dayKey, err := paymentLimitDayKey(now, policy.Timezone)
	if err != nil {
		return nil, err
	}
	usage := &PaymentLimitUsage{
		Policy: policy, DayKey: dayKey,
		CurrencyExponent: common.PaymentProviderCurrencyExponent(policy.Provider, policy.Currency),
	}
	var bucket PaymentLimitBucket
	err = DB.Where("provider = ? AND payment_method = ? AND currency = ? AND day_key = ?", policy.Provider, policy.PaymentMethod, policy.Currency, dayKey).
		First(&bucket).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return usage, nil
	}
	if err != nil {
		return nil, err
	}
	usage.ReservedMinor = bucket.ReservedMinor
	usage.PaidMinor = bucket.PaidMinor
	return usage, nil
}
