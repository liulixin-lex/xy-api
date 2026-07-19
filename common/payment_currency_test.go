package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPaymentCurrencyExponentUsesISO4217Metadata(t *testing.T) {
	tests := []struct {
		currency string
		exponent int32
		valid    bool
	}{
		{currency: "USD", exponent: 2, valid: true},
		{currency: "JPY", exponent: 0, valid: true},
		{currency: "KWD", exponent: 3, valid: true},
		{currency: "ZZZ", valid: false},
		{currency: "XXX", valid: false},
	}
	for _, test := range tests {
		exponent, valid := PaymentCurrencyExponentOK(test.currency)
		assert.Equal(t, test.valid, valid, test.currency)
		if test.valid {
			assert.Equal(t, test.exponent, exponent, test.currency)
		}
	}
}

func TestStripeCurrencyExponentPreservesProviderChargeSemantics(t *testing.T) {
	for _, currency := range []string{"ISK", "UGX"} {
		isoExponent, valid := PaymentCurrencyExponentOK(currency)
		assert.True(t, valid)
		assert.Equal(t, int32(0), isoExponent)

		stripeExponent, valid := PaymentProviderCurrencyExponentOK("stripe", currency)
		assert.True(t, valid)
		assert.Equal(t, int32(2), stripeExponent)
		assert.EqualValues(t, 100, PaymentProviderCurrencyMinorUnit("stripe", currency))
	}

	exponent, valid := PaymentProviderCurrencyExponentOK("stripe", "JPY")
	assert.True(t, valid)
	assert.Equal(t, int32(0), exponent)
	exponent, valid = PaymentProviderCurrencyExponentOK("epay", "ISK")
	assert.True(t, valid)
	assert.Equal(t, int32(0), exponent)
}
