package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripeAccountHistoryIgnoresUnsupportedTelemetryButKeepsFinancialEvents(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&PaymentEvent{
		Provider: PaymentProviderStripe, EventKey: "stripe-unsupported-telemetry",
		EventType: "customer.created", Status: PaymentEventStatusProcessed,
		PayloadDigest:     PaymentPayloadDigest(`{"type":"customer.created"}`),
		NormalizedPayload: `{"type":"customer.created"}`,
		CreatedAt:         common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)

	hasHistory, err := HasStripeAccountBoundData()
	require.NoError(t, err)
	assert.False(t, hasHistory)

	require.NoError(t, DB.Create(&PaymentEvent{
		Provider: PaymentProviderStripe, EventKey: "stripe-unmatched-financial-event",
		EventType: "charge.refunded", ProviderPaymentKey: "stripe:pi_financial",
		Refunded: true, RefundedAmountMinor: 100, Currency: "USD",
		Status:            PaymentEventStatusManualReview,
		PayloadDigest:     PaymentPayloadDigest(`{"amount_refunded":100}`),
		NormalizedPayload: `{"amount_refunded":100}`,
		CreatedAt:         common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}).Error)

	hasHistory, err = HasStripeAccountBoundData()
	require.NoError(t, err)
	assert.True(t, hasHistory)
}
