package model

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentEventKeyIsBoundedAndDeterministic(t *testing.T) {
	providerOrderKey := strings.Repeat("provider-order-", 64)
	normalized := strings.Repeat("payload", 1024)

	first := PaymentEventKey(PaymentProviderEpay, strings.Repeat("status", 64), providerOrderKey, "trade-1", normalized)
	second := PaymentEventKey(PaymentProviderEpay, strings.Repeat("status", 64), providerOrderKey, "trade-1", normalized)

	require.Equal(t, first, second)
	assert.Len(t, first, len("event:")+sha256.Size*2)
	assert.LessOrEqual(t, len(first), 255)
}

func TestPaymentEventKeyBindsEveryIdentityComponent(t *testing.T) {
	base := PaymentEventKey(PaymentProviderEpay, "paid", "provider-order", "trade-1", "payload")
	tests := []string{
		PaymentEventKey(PaymentProviderXorPay, "paid", "provider-order", "trade-1", "payload"),
		PaymentEventKey(PaymentProviderEpay, "refunded", "provider-order", "trade-1", "payload"),
		PaymentEventKey(PaymentProviderEpay, "paid", "other-order", "trade-1", "payload"),
		PaymentEventKey(PaymentProviderEpay, "paid", "provider-order", "trade-2", "payload"),
		PaymentEventKey(PaymentProviderEpay, "paid", "provider-order", "trade-1", "other-payload"),
	}

	for _, candidate := range tests {
		assert.NotEqual(t, base, candidate)
	}
}
