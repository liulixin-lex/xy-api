package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetainedPaymentEventInboxDoesNotRequireCanonicalOrder(t *testing.T) {
	truncateTables(t)
	input := PaymentEventInput{
		Provider:          PaymentProviderWaffo,
		EventKey:          "waffo:payment:order-123:pay-success",
		EventType:         "PAYMENT_NOTIFICATION",
		TradeNo:           "WAFFO-123",
		ProviderOrderKey:  "acquiring-order-123",
		ProviderState:     "PAY_SUCCESS",
		PaidAmountMinor:   100,
		Currency:          "USD",
		PaymentMethod:     PaymentMethodWaffo,
		Paid:              true,
		NormalizedPayload: `{"trade_no":"WAFFO-123","provider_order_key":"acquiring-order-123","state":"PAY_SUCCESS","amount_minor":100,"currency":"USD"}`,
	}

	require.NoError(t, RecordRetainedPaymentEventReceived(input))
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", input.Provider, input.EventKey).First(&event).Error)
	assert.Equal(t, PaymentEventStatusReceived, event.Status)
	assert.Zero(t, event.PaymentOrderID)
	assert.True(t, event.Paid)

	require.NoError(t, MarkRetainedPaymentEventProcessed(input))
	require.ErrorIs(t, RecordRetainedPaymentEventReceived(input), ErrPaymentEventDuplicate)
	require.NoError(t, DB.First(&event, event.ID).Error)
	assert.Equal(t, PaymentEventStatusProcessed, event.Status)
	assert.Equal(t, 2, event.Attempts)
	assert.Zero(t, event.PaymentOrderID)
}

func TestRetainedPaymentEventInboxQuarantinesStableKeyPayloadConflict(t *testing.T) {
	truncateTables(t)
	first := PaymentEventInput{
		Provider:          PaymentProviderCreem,
		EventKey:          "evt_creem_stable",
		EventType:         "checkout.completed",
		TradeNo:           "creem-trade-123",
		ProviderOrderKey:  "creem-order-123",
		ProviderState:     "paid",
		PaidAmountMinor:   1000,
		Currency:          "USD",
		PaymentMethod:     PaymentMethodCreem,
		Paid:              true,
		NormalizedPayload: `{"amount_minor":1000,"currency":"USD"}`,
	}
	require.NoError(t, RecordRetainedPaymentEventReceived(first))

	conflicting := first
	conflicting.PaidAmountMinor = 999
	conflicting.NormalizedPayload = `{"amount_minor":999,"currency":"USD"}`
	require.ErrorIs(t, RecordRetainedPaymentEventReceived(conflicting), ErrPaymentEventConflict)

	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", first.Provider, first.EventKey).First(&event).Error)
	assert.Equal(t, PaymentEventStatusManualReview, event.Status)
	assert.Equal(t, PaymentReviewCodeEventKeyPayloadConflict, event.ReviewCode)
	assert.Equal(t, PaymentPayloadDigest(first.NormalizedPayload), event.PayloadDigest)
	assert.True(t, errors.Is(MarkRetainedPaymentEventProcessed(first), ErrPaymentEventConflict))
}
