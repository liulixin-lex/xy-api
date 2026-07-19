package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountPaymentOrdersDependingOnCallbackOriginValidatesScope(t *testing.T) {
	_, err := CountPaymentOrdersDependingOnCallbackOrigin("unknown", common.GetTimestamp())
	require.Error(t, err)
	_, err = CountPaymentOrdersDependingOnCallbackOrigin(PaymentProviderEpay, 0)
	require.Error(t, err)
}

func TestCountPaymentOrdersDependingOnCallbackOriginCanonicalRecoveryBoundaries(t *testing.T) {
	now := common.GetTimestamp()
	cutoff := now - int64(PaymentCallbackRecoveryWindow/time.Second)
	tests := []struct {
		name  string
		order PaymentOrder
		want  int64
	}{
		{
			name: "ancient active order remains dependent",
			order: PaymentOrder{Status: PaymentOrderStatusPending, CreatedAt: cutoff - 1,
				UpdatedAt: cutoff - 1},
			want: 1,
		},
		{
			name: "failed order never started upstream is not dependent",
			order: PaymentOrder{Status: PaymentOrderStatusFailed, CreatedAt: now,
				UpdatedAt: now},
		},
		{
			name: "updated boundary is inclusive",
			order: PaymentOrder{Status: PaymentOrderStatusFailed, StartedAt: cutoff - 100,
				CreatedAt: cutoff - 1, UpdatedAt: cutoff},
			want: 1,
		},
		{
			name: "expiry boundary is inclusive",
			order: PaymentOrder{Status: PaymentOrderStatusExpired, StartedAt: cutoff - 100,
				CreatedAt: cutoff - 1, UpdatedAt: cutoff - 1, ExpiresAt: cutoff},
			want: 1,
		},
		{
			name: "creation boundary is a compatibility fallback",
			order: PaymentOrder{Status: PaymentOrderStatusExpired, StartedAt: cutoff - 100,
				CreatedAt: cutoff, UpdatedAt: cutoff - 1, ExpiresAt: cutoff - 1},
			want: 1,
		},
		{
			name: "one second outside recovery is not dependent",
			order: PaymentOrder{Status: PaymentOrderStatusExpired, StartedAt: cutoff - 100,
				CreatedAt: cutoff - 1, UpdatedAt: cutoff - 1, ExpiresAt: cutoff - 1},
		},
		{
			name: "recent fulfilled order is terminal",
			order: PaymentOrder{Status: PaymentOrderStatusFulfilled, StartedAt: now - 100,
				CreatedAt: now - 100, UpdatedAt: now},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			test.order.TradeNo = "callback-canonical-" + test.name
			test.order.UserID = 995000 + index
			test.order.OrderKind = PaymentOrderKindTopUp
			test.order.Provider = PaymentProviderEpay
			test.order.PaymentMethod = "alipay"
			test.order.RequestID = test.order.TradeNo
			test.order.ExpectedAmountMinor = 100
			test.order.Currency = "CNY"
			test.order.RequestedAmount = 1
			test.order.CreditQuota = 1
			test.order.Version = 1
			require.NoError(t, DB.Create(&test.order).Error)

			count, err := CountPaymentOrdersDependingOnCallbackOrigin(PaymentProviderEpay, now)
			require.NoError(t, err)
			assert.Equal(t, test.want, count)
		})
	}
}

func TestCountPaymentOrdersDependingOnCallbackOriginUsesProviderSpecificLegacyIdentity(t *testing.T) {
	truncateTables(t)
	now := common.GetTimestamp()
	zeroOrderID := int64(0)
	rows := []*TopUp{
		{
			UserId: 995100, Amount: 1, Money: 1, TradeNo: "callback-legacy-epay-null",
			PaymentMethod: "alipay", PaymentProvider: "", CreateTime: now,
			Status: common.TopUpStatusPending,
		},
		{
			PaymentOrderId: &zeroOrderID, UserId: 995101, Amount: 1, Money: 1,
			TradeNo: "callback-legacy-epay-zero", PaymentMethod: "wxpay",
			PaymentProvider: "", CreateTime: now, Status: common.TopUpStatusPending,
		},
		{
			UserId: 995102, Amount: 1, Money: 1, TradeNo: "callback-legacy-stripe-method",
			PaymentMethod: PaymentMethodStripe, PaymentProvider: "", CreateTime: now,
			Status: common.TopUpStatusPending,
		},
		{
			UserId: 995103, Amount: 1, Money: 1, TradeNo: "callback-legacy-balance-method",
			PaymentMethod: PaymentMethodBalance, PaymentProvider: "", CreateTime: now,
			Status: common.TopUpStatusPending,
		},
	}
	for _, row := range rows {
		require.NoError(t, DB.Create(row).Error)
	}

	epayCount, err := CountPaymentOrdersDependingOnCallbackOrigin(PaymentProviderEpay, now)
	require.NoError(t, err)
	assert.EqualValues(t, 2, epayCount)
	stripeCount, err := CountPaymentOrdersDependingOnCallbackOrigin(PaymentProviderStripe, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, stripeCount)
	xorPayCount, err := CountPaymentOrdersDependingOnCallbackOrigin(PaymentProviderXorPay, now)
	require.NoError(t, err)
	assert.Zero(t, xorPayCount)
}

func TestCountPaymentOrdersDependingOnCallbackOriginIgnoresLinkedLegacyProjection(t *testing.T) {
	truncateTables(t)
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: "callback-linked-canonical", UserID: 995200, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "callback-linked-canonical",
		ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 1,
		Status: PaymentOrderStatusFulfilled, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	projection := &TopUp{
		PaymentOrderId: &order.ID, UserId: order.UserID, Amount: 1, Money: 1,
		TradeNo: order.TradeNo, PaymentMethod: "alipay", PaymentProvider: PaymentProviderEpay,
		CreateTime: now, Status: common.TopUpStatusPending,
	}
	require.NoError(t, DB.Create(projection).Error)

	count, err := CountPaymentOrdersDependingOnCallbackOrigin(PaymentProviderEpay, now)
	require.NoError(t, err)
	assert.Zero(t, count)
}
