package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreemEnvironmentMismatchCannotChangeOrderLifecycleOrEconomics(t *testing.T) {
	for index, test := range []struct {
		name   string
		mutate func(*PaymentEventInput)
	}{
		{name: "paid", mutate: func(input *PaymentEventInput) { input.Paid = true }},
		{name: "failed", mutate: func(input *PaymentEventInput) { input.Failed = true }},
		{name: "expired", mutate: func(input *PaymentEventInput) { input.Expired = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			now := time.Now().Unix()
			livemode := true
			tradeNo := fmt.Sprintf("PO_CREEM_MODE_%d", index)
			order := &PaymentOrder{
				TradeNo: tradeNo, UserID: 990300 + index, OrderKind: PaymentOrderKindTopUp,
				Provider: PaymentProviderCreem, PaymentMethod: PaymentMethodCreem,
				ProviderLivemode: &livemode, RequestID: "request_" + tradeNo,
				ExpectedAmountMinor: 1000, Currency: "USD", RequestedAmount: 1, CreditQuota: 100,
				Status: PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
			}
			require.NoError(t, DB.Create(order).Error)
			testMode := false
			input := PaymentEventInput{
				Provider: PaymentProviderCreem, ProviderLivemode: &testMode,
				EventKey: "creem_mode_" + test.name, EventType: test.name,
				TradeNo: tradeNo, ProviderOrderKey: PaymentProviderCreem + ":checkout_" + test.name,
				PaidAmountMinor: 1000, Currency: "USD", PaymentMethod: PaymentMethodCreem,
				NormalizedPayload: `{"mode":"test"}`,
			}
			test.mutate(&input)

			result, err := ProcessPaymentEvent(input)
			require.ErrorIs(t, err, ErrPaymentManualReview)
			require.NotNil(t, result)
			assert.True(t, result.ManualReview)
			require.NoError(t, DB.First(order, order.ID).Error)
			assert.Equal(t, PaymentOrderStatusManualReview, order.Status)
			assert.Zero(t, order.PaidAmountMinor)
			assert.Zero(t, order.SettledAt)
			var ledgerCount int64
			require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ?", order.ID).Count(&ledgerCount).Error)
			assert.Zero(t, ledgerCount)
			var event PaymentEvent
			require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderCreem, input.EventKey).First(&event).Error)
			assert.Equal(t, PaymentEventStatusManualReview, event.Status)
		})
	}
}
