package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateStripeCheckoutURLUsesStripeHostsAndExactCustomAllowlist(t *testing.T) {
	require.NoError(t, ValidateStripeCheckoutURL("https://checkout.stripe.com/c/pay/session", ""))
	require.NoError(t, ValidateStripeCheckoutURL("https://custom.stripe.com/pay/session", ""))
	require.NoError(t, ValidateStripeCheckoutURL("https://PAY.EXAMPLE.COM/checkout", "pay.example.com"))

	for _, candidate := range []string{
		"https://pay.example.com.evil.test/checkout",
		"https://sub.pay.example.com/checkout",
		"https://other.example.com/checkout",
		"http://pay.example.com/checkout",
		"https://user@pay.example.com/checkout",
		"https://pay.example.com:443/checkout",
	} {
		assert.Error(t, ValidateStripeCheckoutURL(candidate, "pay.example.com"), candidate)
	}
	assert.Error(t, ValidateStripeCheckoutURL("https://pay.example.com/checkout", "*.example.com"))
}

func TestStripeCheckoutConfigurationFingerprintIncludesCanonicalHostPolicy(t *testing.T) {
	secret := "sk_" + "test_" + "fixture"
	account := "acct_" + "fixture1234"
	price := "price_" + "fixture"
	base := StripeCheckoutConfigurationFingerprint(
		secret, account, "", price, "USD", "test", "",
	)
	withHosts := StripeCheckoutConfigurationFingerprint(
		secret, account, "", price, "USD", "test",
		"pay.example.com,checkout.example.net",
	)
	reordered := StripeCheckoutConfigurationFingerprint(
		secret, account, "", price, "USD", "test",
		" CHECKOUT.EXAMPLE.NET\npay.example.com ",
	)
	assert.NotEmpty(t, base)
	assert.NotEqual(t, base, withHosts)
	assert.Equal(t, withHosts, reordered)
	assert.Empty(t, StripeCheckoutConfigurationFingerprint(
		secret, account, "", price, "USD", "test", "*.example.com",
	))
}
