package setting

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeStripeCheckoutAllowedHostsCanonicalizesExactDNSHosts(t *testing.T) {
	canonical, err := NormalizeStripeCheckoutAllowedHosts(" Pay.Example.com\ncheckout.example.net, pay.example.com ")
	require.NoError(t, err)
	assert.Equal(t, "checkout.example.net,pay.example.com", canonical)

	canonical, err = NormalizeStripeCheckoutAllowedHosts("  ")
	require.NoError(t, err)
	assert.Empty(t, canonical)
}

func TestNormalizeStripeCheckoutAllowedHostsRejectsUnsafeEntries(t *testing.T) {
	tests := []string{
		"*.example.com",
		"https://pay.example.com",
		"pay.example.com:443",
		"user@pay.example.com",
		"127.0.0.1",
		"[::1]",
		"localhost",
		"pay.localhost",
		"internal",
		"pay_example.com",
		"pay.example.com.",
		"支付.example.com",
		"pay.example.123",
		strings.Repeat("a", 64) + ".example.com",
		strings.Repeat("pay.example.com,", 33),
		strings.Repeat("a", maxStripeCheckoutAllowedHostsBytes+1),
	}
	for _, raw := range tests {
		t.Run(raw[:min(len(raw), 48)], func(t *testing.T) {
			_, err := NormalizeStripeCheckoutAllowedHosts(raw)
			assert.ErrorIs(t, err, ErrStripeCheckoutAllowedHostsInvalid)
		})
	}
}
