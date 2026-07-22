package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaffoPancakeEnvironmentMismatchCannotSettleProductionOrder(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	production := true
	order := &PaymentOrder{
		TradeNo: "PO_PANCAKE_PRODUCTION_MODE", UserID: 990800, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderWaffoPancake, PaymentMethod: PaymentMethodWaffoPancake,
		ProviderLivemode: &production, RequestID: "request_pancake_production_mode",
		ExpectedAmountMinor: 1000, Currency: "USD", RequestedAmount: 1, CreditQuota: 100,
		Status: PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)

	testEnvironment := false
	result, err := ProcessPaymentEvent(PaymentEventInput{
		Provider: PaymentProviderWaffoPancake, ProviderLivemode: &testEnvironment,
		EventKey: "pancake_test_callback_for_production", EventType: "order.completed",
		TradeNo: order.TradeNo, ProviderOrderKey: PaymentProviderWaffoPancake + ":ORD_test_mode",
		PaidAmountMinor: 1000, Currency: "USD", PaymentMethod: PaymentMethodWaffoPancake,
		Paid: true, NormalizedPayload: `{"mode":"test"}`,
	})
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	require.NoError(t, DB.First(order, order.ID).Error)
	assert.Equal(t, PaymentOrderStatusManualReview, order.Status)
	assert.Equal(t, "waffo_pancake_order_event_environment_mismatch", order.StatusReason)
	assert.Zero(t, order.PaidAmountMinor)
	assert.Zero(t, order.SettledAt)

	var ledgerCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ?", order.ID).Count(&ledgerCount).Error)
	assert.Zero(t, ledgerCount)
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderWaffoPancake, "pancake_test_callback_for_production").First(&event).Error)
	assert.Equal(t, PaymentEventStatusManualReview, event.Status)
}

func TestWaffoPancakePaymentQuoteRequiresEnvironmentBinding(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	quote := &PaymentQuote{
		QuoteID: "Q_PANCAKE_ENVIRONMENT", UserID: 990801, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderWaffoPancake, PaymentMethod: PaymentMethodWaffoPancake,
		ExpectedAmountMinor: 100, Currency: "USD", ExpiresAt: now + 60,
	}
	assert.Error(t, CreatePaymentQuote(quote))

	production := true
	quote.ProviderLivemode = &production
	require.NoError(t, CreatePaymentQuote(quote))
	var stored PaymentQuote
	require.NoError(t, DB.Where("quote_id = ?", quote.QuoteID).First(&stored).Error)
	require.NotNil(t, stored.ProviderLivemode)
	assert.True(t, *stored.ProviderLivemode)
}
