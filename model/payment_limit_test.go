package model

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPaymentLimitPolicyAcceptsOnlyCanonicalProviderMethods(t *testing.T) {
	tests := []struct {
		name    string
		policy  PaymentLimitPolicy
		wantErr bool
	}{
		{
			name: "epay custom method",
			policy: PaymentLimitPolicy{Provider: PaymentProviderEpay, PaymentMethod: "merchant_alipay",
				Currency: "CNY", Timezone: "UTC", Enabled: true},
		},
		{
			name: "stripe canonical method",
			policy: PaymentLimitPolicy{Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe,
				Currency: "USD", Timezone: "UTC", Enabled: true},
		},
		{
			name: "stripe mismatched method",
			policy: PaymentLimitPolicy{Provider: PaymentProviderStripe, PaymentMethod: "alipay",
				Currency: "USD", Timezone: "UTC", Enabled: true},
			wantErr: true,
		},
		{
			name: "xorpay unsupported method",
			policy: PaymentLimitPolicy{Provider: PaymentProviderXorPay, PaymentMethod: "wechat_h5",
				Currency: "CNY", Timezone: "UTC", Enabled: true},
			wantErr: true,
		},
		{
			name: "creem canonical method",
			policy: PaymentLimitPolicy{Provider: PaymentProviderCreem, PaymentMethod: PaymentMethodCreem,
				Currency: "USD", Timezone: "UTC", Enabled: true},
		},
		{
			name: "waffo canonical method",
			policy: PaymentLimitPolicy{Provider: PaymentProviderWaffo, PaymentMethod: PaymentMethodWaffo,
				Currency: "USD", Timezone: "UTC", Enabled: true},
		},
		{
			name: "waffo pancake canonical method",
			policy: PaymentLimitPolicy{Provider: PaymentProviderWaffoPancake, PaymentMethod: PaymentMethodWaffoPancake,
				Currency: "USD", Timezone: "UTC", Enabled: true},
		},
		{
			name: "creem mismatched method",
			policy: PaymentLimitPolicy{Provider: PaymentProviderCreem, PaymentMethod: PaymentMethodWaffo,
				Currency: "USD", Timezone: "UTC", Enabled: true},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := normalizePaymentLimitPolicy(&test.policy)
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestPaymentLimitArithmeticFailsClosedWithoutOverflow(t *testing.T) {
	t.Parallel()

	assert.True(t, paymentLimitWouldExceed(math.MaxInt64, math.MaxInt64-5, 4, 2))
	assert.False(t, paymentLimitWouldExceed(math.MaxInt64, math.MaxInt64-5, 4, 1))
	assert.True(t, paymentLimitWouldExceed(100, -1, 0, 1))

	value, err := addPaymentLimitMinor(math.MaxInt64-1, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(math.MaxInt64), value)
	_, err = addPaymentLimitMinor(math.MaxInt64, 1)
	assert.Error(t, err)
}

func TestPaymentLimitReservationBlocksDailyOvercommitAndReleasesOnExpiry(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 979101, 0)
	require.NoError(t, UpsertPaymentLimitPolicy(&PaymentLimitPolicy{
		Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayAlipay,
		Currency: "CNY", SingleLimitMinor: 800, DailyLimitMinor: 1000, Timezone: "Asia/Shanghai", Enabled: true,
	}))
	now := common.GetTimestamp()
	firstQuote := &PaymentQuote{
		QuoteID: "Q_LIMIT_FIRST", UserID: 979101, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayAlipay,
		RequestedAmount: 1, CreditQuota: 500, ExpectedAmountMinor: 600, Currency: "CNY",
		PricingSnapshot: `{}`, ExpiresAt: now + 60,
	}
	secondQuote := *firstQuote
	secondQuote.ID = 0
	secondQuote.QuoteID = "Q_LIMIT_SECOND"
	require.NoError(t, CreatePaymentQuote(firstQuote))
	require.NoError(t, CreatePaymentQuote(&secondQuote))
	firstOrder, err := CreatePaymentOrderFromQuote(firstQuote.UserID, firstQuote.QuoteID, "limit-first")
	require.NoError(t, err)
	_, err = CreatePaymentOrderFromQuote(secondQuote.UserID, secondQuote.QuoteID, "limit-second")
	assert.ErrorIs(t, err, ErrPaymentDailyLimitExceeded)

	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", firstOrder.ID).Update("expires_at", now-1).Error)
	_, err = ExpirePaymentOrderIfDue(firstOrder.UserID, firstOrder.TradeNo)
	require.NoError(t, err)
	secondOrder, err := CreatePaymentOrderFromQuote(secondQuote.UserID, secondQuote.QuoteID, "limit-second")
	require.NoError(t, err)
	assert.NotZero(t, secondOrder.ID)
}

func TestPaymentLimitSettlementMovesReservationToPaidBucket(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 979102, 0)
	require.NoError(t, UpsertPaymentLimitPolicy(&PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "alipay",
		Currency: "CNY", SingleLimitMinor: 2000, DailyLimitMinor: 5000, Timezone: "UTC", Enabled: true,
	}))
	now := time.Now().Unix()
	quote := &PaymentQuote{
		QuoteID: "Q_LIMIT_SETTLE", UserID: 979102, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestedAmount: 1,
		CreditQuota: 500, ExpectedAmountMinor: 1200, Currency: "CNY", PricingSnapshot: `{}`,
		ExpiresAt: now + 600,
	}
	require.NoError(t, CreatePaymentQuote(quote))
	order, err := CreatePaymentOrderFromQuote(quote.UserID, quote.QuoteID, "limit-settle")
	require.NoError(t, err)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return settlePaymentLimitReservationTx(tx, order, now)
	}))

	var reservation PaymentLimitReservation
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&reservation).Error)
	assert.Equal(t, PaymentLimitReservationPaid, reservation.Status)
	usage, err := CurrentPaymentLimitUsage(PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY", Timezone: "UTC", Enabled: true,
	}, now)
	require.NoError(t, err)
	assert.Zero(t, usage.ReservedMinor)
	assert.Equal(t, int64(1200), usage.PaidMinor)
}

func TestStripeDelayedCallbackUsesTrustedProviderPaymentDay(t *testing.T) {
	truncateTables(t)
	require.NoError(t, UpsertPaymentLimitPolicy(&PaymentLimitPolicy{
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe,
		Currency: "USD", SingleLimitMinor: 5000, DailyLimitMinor: 10000, Timezone: "UTC", Enabled: true,
	}))
	orderCreatedAt := time.Date(2026, time.July, 21, 22, 55, 0, 0, time.UTC).Unix()
	providerPaidAt := time.Date(2026, time.July, 21, 23, 20, 0, 0, time.UTC).Unix()
	callbackReceivedAt := time.Date(2026, time.July, 22, 0, 10, 0, 0, time.UTC).Unix()
	order := &PaymentOrder{
		TradeNo: "PO_STRIPE_DELAYED_LIMIT_DAY", UserID: 979110, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, RequestID: "stripe-delayed-limit-day",
		ExpectedAmountMinor: 1200, Currency: "USD", Status: PaymentOrderStatusPending,
		ExpiresAt: orderCreatedAt + PaymentStripeOrderTTLSeconds, CreatedAt: orderCreatedAt, UpdatedAt: orderCreatedAt, Version: 1,
	}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(order).Error; err != nil {
			return err
		}
		if err := reservePaymentLimitTxAt(tx, order, orderCreatedAt); err != nil {
			return err
		}
		if err := releasePaymentLimitReservationTx(tx, order, order.ExpiresAt+1); err != nil {
			return err
		}
		event := &PaymentEvent{
			Provider: PaymentProviderStripe, ProviderCreatedAt: providerPaidAt, CreatedAt: callbackReceivedAt,
		}
		return settlePaymentLimitReservationAtTx(
			tx, order, paymentLimitPaidAtForEvent(event, order, callbackReceivedAt), callbackReceivedAt,
		)
	}))

	var reservation PaymentLimitReservation
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&reservation).Error)
	assert.Equal(t, PaymentLimitReservationPaid, reservation.Status)
	assert.Equal(t, "2026-07-21", reservation.PaidDayKey)
	assert.Equal(t, providerPaidAt, reservation.PaidAt)

	previousDay, err := CurrentPaymentLimitUsage(PaymentLimitPolicy{
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, Currency: "USD", Timezone: "UTC", Enabled: true,
	}, providerPaidAt)
	require.NoError(t, err)
	assert.Equal(t, int64(1200), previousDay.PaidMinor)
	nextDay, err := CurrentPaymentLimitUsage(PaymentLimitPolicy{
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, Currency: "USD", Timezone: "UTC", Enabled: true,
	}, callbackReceivedAt)
	require.NoError(t, err)
	assert.Zero(t, nextDay.PaidMinor)
}

func TestPaymentLimitPaidAtRejectsUntrustedProviderTimestamps(t *testing.T) {
	receivedAt := time.Date(2026, time.July, 22, 0, 10, 0, 0, time.UTC).Unix()
	order := &PaymentOrder{CreatedAt: receivedAt - 3600}

	assert.Equal(t, receivedAt, paymentLimitPaidAtForEvent(&PaymentEvent{
		Provider: PaymentProviderStripe, ProviderCreatedAt: order.CreatedAt - paymentLimitProviderClockSkew - 1,
		CreatedAt: receivedAt,
	}, order, receivedAt))
	assert.Equal(t, receivedAt, paymentLimitPaidAtForEvent(&PaymentEvent{
		Provider: PaymentProviderStripe, ProviderCreatedAt: receivedAt + paymentLimitProviderClockSkew + 1,
		CreatedAt: receivedAt,
	}, order, receivedAt))
	assert.Equal(t, receivedAt, paymentLimitPaidAtForEvent(&PaymentEvent{
		Provider: PaymentProviderEpay, ProviderCreatedAt: receivedAt - 1800, CreatedAt: receivedAt,
	}, order, receivedAt))
}

func TestPaymentLimitsDoNotMixCurrencyMinorUnits(t *testing.T) {
	truncateTables(t)
	for _, currency := range []string{"CNY", "USD"} {
		require.NoError(t, UpsertPaymentLimitPolicy(&PaymentLimitPolicy{
			Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, Currency: currency,
			SingleLimitMinor: 1000, DailyLimitMinor: 1000, Timezone: "UTC", Enabled: true,
		}))
	}
	now := time.Now().Unix()
	dayKey, err := paymentLimitDayKey(now, "UTC")
	require.NoError(t, err)
	require.NoError(t, DB.Create(&PaymentLimitBucket{
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, Currency: "CNY", DayKey: dayKey,
		PaidMinor: 900, CreatedAt: now, UpdatedAt: now, Version: 1,
	}).Error)

	assert.ErrorIs(t, CheckPaymentLimitForQuote(PaymentProviderStripe, PaymentMethodStripe, "CNY", 200, now), ErrPaymentDailyLimitExceeded)
	assert.NoError(t, CheckPaymentLimitForQuote(PaymentProviderStripe, PaymentMethodStripe, "USD", 200, now))
}

func TestPaymentLimitReconciliationRepairsFulfilledOrderFromOlderNode(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 979103, 0)
	require.NoError(t, UpsertPaymentLimitPolicy(&PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY",
		SingleLimitMinor: 5000, DailyLimitMinor: 10000, Timezone: "UTC", Enabled: true,
	}))
	now := time.Now().Unix()
	quote := &PaymentQuote{
		QuoteID: "Q_LIMIT_REPAIR_FULFILLED", UserID: 979103, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestedAmount: 1,
		CreditQuota: 500, ExpectedAmountMinor: 1200, Currency: "CNY", PricingSnapshot: `{}`,
		ExpiresAt: now + 600,
	}
	require.NoError(t, CreatePaymentQuote(quote))
	order, err := CreatePaymentOrderFromQuote(quote.UserID, quote.QuoteID, "limit-repair-fulfilled")
	require.NoError(t, err)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
		Update("status", PaymentOrderStatusFulfilled).Error)

	result, err := ReconcilePaymentLimitReservations(context.Background(), now, 10)
	require.NoError(t, err)
	assert.Equal(t, PaymentLimitReconciliationResult{Scanned: 1, Settled: 1}, result)
	usage, err := CurrentPaymentLimitUsage(PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY", Timezone: "UTC", Enabled: true,
	}, now)
	require.NoError(t, err)
	assert.Zero(t, usage.ReservedMinor)
	assert.Equal(t, int64(1200), usage.PaidMinor)

	result, err = ReconcilePaymentLimitReservations(context.Background(), now, 10)
	require.NoError(t, err)
	assert.Zero(t, result.Scanned)
	usage, err = CurrentPaymentLimitUsage(PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY", Timezone: "UTC", Enabled: true,
	}, now)
	require.NoError(t, err)
	assert.Equal(t, int64(1200), usage.PaidMinor)
}

func TestPaymentLimitReconciliationSettlesLatePaymentAfterReservationRelease(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 979104, 0)
	require.NoError(t, UpsertPaymentLimitPolicy(&PaymentLimitPolicy{
		Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayAlipay, Currency: "CNY",
		SingleLimitMinor: 5000, DailyLimitMinor: 10000, Timezone: "UTC", Enabled: true,
	}))
	now := time.Now().Unix()
	quote := &PaymentQuote{
		QuoteID: "Q_LIMIT_REPAIR_RELEASED", UserID: 979104, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayAlipay, RequestedAmount: 1,
		CreditQuota: 500, ExpectedAmountMinor: 900, Currency: "CNY", PricingSnapshot: `{}`,
		ExpiresAt: now + 600,
	}
	require.NoError(t, CreatePaymentQuote(quote))
	order, err := CreatePaymentOrderFromQuote(quote.UserID, quote.QuoteID, "limit-repair-released")
	require.NoError(t, err)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return releasePaymentLimitReservationTx(tx, order, now)
	}))
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
		Update("status", PaymentOrderStatusFulfilled).Error)

	result, err := ReconcilePaymentLimitReservations(context.Background(), now, 10)
	require.NoError(t, err)
	assert.Equal(t, PaymentLimitReconciliationResult{Scanned: 1, Settled: 1}, result)
	usage, err := CurrentPaymentLimitUsage(PaymentLimitPolicy{
		Provider: PaymentProviderXorPay, PaymentMethod: PaymentMethodXorPayAlipay,
		Currency: "CNY", Timezone: "UTC", Enabled: true,
	}, now)
	require.NoError(t, err)
	assert.Zero(t, usage.ReservedMinor)
	assert.Equal(t, int64(900), usage.PaidMinor)
}

func TestPaymentLimitReconciliationReleasesExpiredManualReviewReservation(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 979105, 0)
	require.NoError(t, UpsertPaymentLimitPolicy(&PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "wechat", Currency: "CNY",
		SingleLimitMinor: 5000, DailyLimitMinor: 10000, Timezone: "UTC", Enabled: true,
	}))
	now := time.Now().Unix()
	quote := &PaymentQuote{
		QuoteID: "Q_LIMIT_REPAIR_EXPIRED", UserID: 979105, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderEpay, PaymentMethod: "wechat", RequestedAmount: 1,
		CreditQuota: 500, ExpectedAmountMinor: 700, Currency: "CNY", PricingSnapshot: `{}`,
		ExpiresAt: now + 600,
	}
	require.NoError(t, CreatePaymentQuote(quote))
	order, err := CreatePaymentOrderFromQuote(quote.UserID, quote.QuoteID, "limit-repair-expired")
	require.NoError(t, err)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
		Update("status", PaymentOrderStatusManualReview).Error)
	require.NoError(t, DB.Model(&PaymentLimitReservation{}).Where("payment_order_id = ?", order.ID).
		Update("expires_at", now-1).Error)

	result, err := ReconcilePaymentLimitReservations(context.Background(), now, 10)
	require.NoError(t, err)
	assert.Equal(t, PaymentLimitReconciliationResult{Scanned: 1, Released: 1}, result)
	usage, err := CurrentPaymentLimitUsage(PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "wechat", Currency: "CNY", Timezone: "UTC", Enabled: true,
	}, now)
	require.NoError(t, err)
	assert.Zero(t, usage.ReservedMinor)
	assert.Zero(t, usage.PaidMinor)
}

func TestStripeOrderGetsProviderCompatibleServerOwnedExpiry(t *testing.T) {
	truncateTables(t)
	insertUserForPaymentGuardTest(t, 979106, 0)
	now := time.Now().Unix()
	livemode := false
	quote := &PaymentQuote{
		QuoteID: "Q_STRIPE_SERVER_EXPIRY", UserID: 979106, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, ProviderLivemode: &livemode,
		RequestedAmount: 1, CreditQuota: 500, ExpectedAmountMinor: 800, Currency: "USD",
		PricingSnapshot: `{}`, ExpiresAt: now + 600, CreatedAt: now,
	}
	require.NoError(t, CreatePaymentQuote(quote))
	order, err := CreatePaymentOrderFromQuote(quote.UserID, quote.QuoteID, "stripe-server-expiry")
	require.NoError(t, err)
	assert.Equal(t, order.CreatedAt+PaymentStripeOrderTTLSeconds, order.ExpiresAt)
	assert.Greater(t, order.ExpiresAt, quote.ExpiresAt)
}

func TestPaymentProviderCannotExtendServerOwnedExpiry(t *testing.T) {
	t.Setenv("PAYMENT_SECRET_KEY", "provider-expiry-authority-test-key-000001")
	truncateTables(t)
	now := time.Now().Unix()
	order := &PaymentOrder{
		TradeNo: "PO_EPAY_SERVER_EXPIRY", UserID: 979107, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "epay-server-expiry",
		ExpectedAmountMinor: 800, Currency: "CNY", Status: PaymentOrderStatusPending,
		ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)

	require.NoError(t, SavePaymentOrderStartWithProviderIdentity(
		order.TradeNo, "form", `{"action":"https://payments.example.test"}`, now+2*60*60, "", ""))

	stored, err := GetPaymentOrderByID(order.ID)
	require.NoError(t, err)
	assert.Equal(t, order.ExpiresAt, stored.ExpiresAt)
}

func TestPaymentLimitTimezoneCannotChangeAfterUsageExists(t *testing.T) {
	truncateTables(t)
	policy := &PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY",
		SingleLimitMinor: 5000, DailyLimitMinor: 10000, Timezone: "UTC", Enabled: true,
	}
	require.NoError(t, UpsertPaymentLimitPolicy(policy))
	now := time.Now().Unix()
	require.NoError(t, DB.Create(&PaymentLimitBucket{
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY", DayKey: "2026-07-21",
		PaidMinor: 100, CreatedAt: now, UpdatedAt: now, Version: 1,
	}).Error)

	policy.Timezone = "Asia/Shanghai"
	assert.ErrorIs(t, UpsertPaymentLimitPolicy(policy), ErrPaymentLimitTimezoneLocked)

	stored, err := GetPaymentLimitPolicy(PaymentProviderEpay, "alipay", "CNY")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "UTC", stored.Timezone)
}

func TestPaymentLimitTimezoneCanChangeBeforeAnyUsage(t *testing.T) {
	truncateTables(t)
	policy := &PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY",
		SingleLimitMinor: 5000, DailyLimitMinor: 10000, Timezone: "UTC", Enabled: true,
	}
	require.NoError(t, UpsertPaymentLimitPolicy(policy))
	policy.Timezone = "Asia/Shanghai"
	require.NoError(t, UpsertPaymentLimitPolicy(policy))

	stored, err := GetPaymentLimitPolicy(PaymentProviderEpay, "alipay", "CNY")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "Asia/Shanghai", stored.Timezone)
}

func TestPaymentLimitTimezoneCannotChangeWhileReservationExists(t *testing.T) {
	truncateTables(t)
	policy := &PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "wechat", Currency: "CNY",
		SingleLimitMinor: 5000, DailyLimitMinor: 10000, Timezone: "UTC", Enabled: true,
	}
	require.NoError(t, UpsertPaymentLimitPolicy(policy))
	now := time.Now().Unix()
	require.NoError(t, DB.Create(&PaymentLimitReservation{
		PaymentOrderID: 979108, Provider: PaymentProviderEpay, PaymentMethod: "wechat", Currency: "CNY",
		DayKey: "2026-07-21", AmountMinor: 100, Status: PaymentLimitReservationReleased,
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	policy.Timezone = "Asia/Shanghai"
	assert.ErrorIs(t, UpsertPaymentLimitPolicy(policy), ErrPaymentLimitTimezoneLocked)
}

func TestPaymentLimitExpiryStopsReservationAtMerchantMidnight(t *testing.T) {
	truncateTables(t)
	require.NoError(t, UpsertPaymentLimitPolicy(&PaymentLimitPolicy{
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY",
		SingleLimitMinor: 5000, DailyLimitMinor: 10000, Timezone: "UTC", Enabled: true,
	}))
	now := time.Date(2026, time.July, 21, 23, 55, 0, 0, time.UTC).Unix()
	merchantMidnight := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC).Unix()

	quoteExpiry, err := BoundPaymentQuoteExpiryForLimit(
		PaymentProviderEpay, "alipay", "CNY", now, now+10*60,
	)
	require.NoError(t, err)
	assert.Equal(t, merchantMidnight, quoteExpiry)

	quote := &PaymentQuote{
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY",
	}
	var order PaymentOrder
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		orderExpiry, err := boundPaymentOrderExpiryForLimitTx(tx, quote, now, now+10*60)
		if err != nil {
			return err
		}
		order = PaymentOrder{
			TradeNo: "PO_LIMIT_MIDNIGHT", UserID: 979109, OrderKind: PaymentOrderKindTopUp,
			Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "limit-midnight",
			ExpectedAmountMinor: 500, Currency: "CNY", Status: PaymentOrderStatusPending,
			ExpiresAt: orderExpiry, CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		return reservePaymentLimitTxAt(tx, &order, now)
	}))
	assert.Equal(t, merchantMidnight, order.ExpiresAt)

	var reservation PaymentLimitReservation
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&reservation).Error)
	assert.Equal(t, "2026-07-21", reservation.DayKey)
	assert.Equal(t, merchantMidnight, reservation.ExpiresAt)
	assert.Less(t, reservation.ExpiresAt, time.Date(2026, time.July, 22, 0, 5, 0, 0, time.UTC).Unix())
}

func TestStripeDailyLimitRequiresFullOrderWindowBeforeMerchantMidnight(t *testing.T) {
	truncateTables(t)
	require.NoError(t, UpsertPaymentLimitPolicy(&PaymentLimitPolicy{
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, Currency: "USD",
		SingleLimitMinor: 5000, DailyLimitMinor: 10000, Timezone: "UTC", Enabled: true,
	}))
	merchantMidnight := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC).Unix()
	latestQuoteExpiry := merchantMidnight - PaymentStripeOrderTTLSeconds

	now := time.Date(2026, time.July, 21, 23, 15, 0, 0, time.UTC).Unix()
	bounded, err := BoundPaymentQuoteExpiryForLimit(
		PaymentProviderStripe, PaymentMethodStripe, "USD", now, now+10*60,
	)
	require.NoError(t, err)
	assert.Equal(t, latestQuoteExpiry, bounded)

	tooLate := time.Date(2026, time.July, 21, 23, 20, 0, 0, time.UTC).Unix()
	_, err = BoundPaymentQuoteExpiryForLimit(
		PaymentProviderStripe, PaymentMethodStripe, "USD", tooLate, tooLate+10*60,
	)
	assert.ErrorIs(t, err, ErrPaymentLimitDayBoundary)

	quote := &PaymentQuote{Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, Currency: "USD"}
	err = DB.Transaction(func(tx *gorm.DB) error {
		_, err := boundPaymentOrderExpiryForLimitTx(
			tx, quote, tooLate+1, tooLate+1+PaymentStripeOrderTTLSeconds,
		)
		return err
	})
	assert.ErrorIs(t, err, ErrPaymentLimitDayBoundary)
}
