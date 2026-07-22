package operation_setting

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeLegacyImplicitPaymentRoutesPreservesExplicitCatalogPriority(t *testing.T) {
	existing := []map[string]string{
		{
			"name": "Primary card", "type": "stripe", "provider": "stripe",
			"route_id": "primary_card", "public_method": "card", "channel_alias": "checkout",
		},
		{
			"name": "Alipay", "type": "alipay", "provider": "epay",
			"route_id": "primary_alipay", "public_method": "alipay", "channel_alias": "redirect",
		},
	}

	migrated, added, err := MergeLegacyImplicitPaymentRoutes(existing, LegacyImplicitPaymentRoutes{
		Stripe: true,
		XorPay: []string{"xorpay_native", "xorpay_alipay"},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, added)
	require.Len(t, migrated, 4)
	assert.Equal(t, "primary_card", migrated[0]["route_id"])
	assert.Equal(t, "primary_alipay", migrated[1]["route_id"])
	assert.Equal(t, "xorpay_native", migrated[2]["type"])
	assert.Equal(t, "xorpay_alipay", migrated[3]["type"])

	second, addedAgain, err := MergeLegacyImplicitPaymentRoutes(migrated, LegacyImplicitPaymentRoutes{
		Stripe: true,
		XorPay: []string{"xorpay_native", "xorpay_alipay"},
	})
	require.NoError(t, err)
	assert.Zero(t, addedAgain)
	assert.Equal(t, migrated, second)
}

func TestMergeLegacyImplicitPaymentRoutesRejectsUnknownXorPayMethod(t *testing.T) {
	_, _, err := MergeLegacyImplicitPaymentRoutes(DefaultPayMethods(), LegacyImplicitPaymentRoutes{
		XorPay: []string{"xorpay_unknown"},
	})
	require.Error(t, err)
}

func TestMergeLegacyImplicitPaymentRoutesFitsMaximumLegacyEffectiveCatalog(t *testing.T) {
	existing := make([]map[string]string, 0, 20)
	for index := 0; index < 20; index++ {
		existing = append(existing, map[string]string{
			"name": fmt.Sprintf("Method %d", index), "type": fmt.Sprintf("method_%d", index), "provider": "epay",
		})
	}
	migrated, added, err := MergeLegacyImplicitPaymentRoutes(existing, LegacyImplicitPaymentRoutes{
		Stripe: true, Creem: true, Waffo: true, WaffoPancake: true,
		XorPay: []string{"xorpay_native", "xorpay_alipay", "xorpay_jsapi"},
	})
	require.NoError(t, err)
	assert.Equal(t, 7, added)
	assert.Len(t, migrated, maxConfiguredPaymentMethods)
}
