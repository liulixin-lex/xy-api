package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetainedProviderHostedPaymentURLValidation(t *testing.T) {
	tests := []struct {
		name     string
		validate func(string) error
		valid    string
		invalid  []string
	}{
		{
			name:     "Creem",
			validate: ValidateCreemCheckoutURL,
			valid:    "https://checkout.creem.io/ch_1QyIQDw9cbFWdA1ry5Qc6I?theme=dark#provider-state",
			invalid: []string{
				"http://checkout.creem.io/ch_123",
				"https://checkout.creem.io:443/ch_123",
				"https://user@checkout.creem.io/ch_123",
				"https://sub.checkout.creem.io/ch_123",
				"https://creem.io/ch_123",
				"https://checkout.creem.io.evil.example/ch_123",
				"https://evilcreem.io/ch_123",
			},
		},
		{
			name: "Waffo",
			validate: func(raw string) error {
				return ValidateWaffoWebPaymentURL(raw, nil)
			},
			valid: "https://cashier.waffo.com/checkout/order_123",
			invalid: []string{
				"http://cashier.waffo.com/checkout/order_123",
				"https://cashier.waffo.com:443/checkout/order_123",
				"https://user@cashier.waffo.com/checkout/order_123",
				"https://cashier.waffo.com.evil.example/checkout/order_123",
				"https://evilwaffo.com/checkout/order_123",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, test.validate(test.valid))
			for _, candidate := range test.invalid {
				assert.Error(t, test.validate(candidate), candidate)
			}
			assert.Error(t, test.validate("https://"+strings.Repeat("a", maxRetainedPaymentURLBytes)+"."+strings.ToLower(test.name)+".example/path"))
		})
	}
}

func TestWaffoWebPaymentURLPreservesProviderParametersAndRequiresExactHostTrust(t *testing.T) {
	providerURL := "https://cashier.waffo.com/checkout/order_123?session=abc%2F123&return=https%3A%2F%2Fmerchant.example#continue=wallet"
	require.NoError(t, ValidateWaffoWebPaymentURL(providerURL, nil))

	hosts, err := ParseWaffoWebRedirectHosts("PAY.EXAMPLE.COM, checkout.partner.example\npay.example.com")
	require.NoError(t, err)
	assert.Equal(t, []string{"checkout.partner.example", "pay.example.com"}, hosts)
	require.NoError(t, ValidateWaffoWebPaymentURL(
		"https://pay.example.com/session?id=123#provider-state",
		hosts,
	))
	assert.Error(t, ValidateWaffoWebPaymentURL("https://sub.pay.example.com/session", hosts))
	assert.Error(t, ValidateWaffoWebPaymentURL("https://pay.example.com.evil.test/session", hosts))

	invalidAllowlists := []string{
		"https://pay.example.com",
		"pay.example.com/path",
		"*.example.com",
		"localhost",
		"127.0.0.1",
		"10.0.0.8",
		"pay_example.com",
	}
	for _, candidate := range invalidAllowlists {
		_, err := ParseWaffoWebRedirectHosts(candidate)
		assert.Error(t, err, candidate)
	}
}

func TestWaffoAppPaymentURLUsesIndependentFailClosedSchemeAllowlist(t *testing.T) {
	schemes, err := ParseWaffoAppRedirectSchemes("weixin, ALIPAYS\nwallet-pay")
	require.NoError(t, err)
	assert.Equal(t, []string{"alipays", "wallet-pay", "weixin"}, schemes)

	require.NoError(t, ValidateWaffoAppPaymentURL(
		"weixin://wap/pay?prepayid=wx123#provider-state",
		schemes,
	))
	assert.Error(t, ValidateWaffoAppPaymentURL("weixin://wap/pay?prepayid=wx123", nil))
	assert.Error(t, ValidateWaffoAppPaymentURL("other-wallet://pay/session", schemes))
	assert.Error(t, ValidateWaffoAppPaymentURL("https://cashier.waffo.com/session", schemes))

	for _, candidate := range []string{"https", "javascript", "data", "file", "bad_scheme"} {
		_, err := ParseWaffoAppRedirectSchemes(candidate)
		assert.Error(t, err, candidate)
	}
}

func TestWaffoPancakeCheckoutURLRequiresSingleSDKTokenFragment(t *testing.T) {
	require.NoError(t, ValidateWaffoPancakeCheckoutURL(
		"https://pancake.waffo.ai/checkout/session_123?locale=ja#token=aaa.bbb.ccc",
	))

	invalid := []string{
		"https://pancake.waffo.ai/checkout/session_123",
		"http://pancake.waffo.ai/checkout/session_123#token=a.b.c",
		"https://pancake.waffo.ai:443/checkout/session_123#token=a.b.c",
		"https://user@pancake.waffo.ai/checkout/session_123#token=a.b.c",
		"https://sub.pancake.waffo.ai/checkout/session_123#token=a.b.c",
		"https://waffo.ai/checkout/session_123#token=a.b.c",
		"https://pancake.waffo.ai.evil.example/checkout/session_123#token=a.b.c",
		"https://evilwaffo.ai/checkout/session_123#token=a.b.c",
		"https://pancake.waffo.ai/checkout/session_123#token=a.b.c&next=evil",
		"https://pancake.waffo.ai/checkout/session_123#token=a%2Eb%2Ec",
		"https://pancake.waffo.ai/checkout/session_123#token=a.b.c#extra",
		"https://pancake.waffo.ai/checkout/session_123#other=a.b.c",
	}
	for _, candidate := range invalid {
		assert.Error(t, ValidateWaffoPancakeCheckoutURL(candidate), candidate)
	}
}
