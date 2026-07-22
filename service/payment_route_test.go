package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicPaymentRoutesAllowSameBrandAcrossGateways(t *testing.T) {
	originalMethods := operation_setting.PayMethods
	originalEnabledMethods := setting.XorPayEnabledMethods
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		setting.XorPayEnabledMethods = originalEnabledMethods
	})
	setting.XorPayEnabledMethods = nil
	operation_setting.PayMethods = []map[string]string{
		{
			"type": "Alipay", "provider": model.PaymentProviderEpay,
			"route_id": "alipay_backup", "public_method": "alipay", "channel_alias": "redirect",
		},
		{
			"type": model.PaymentMethodXorPayAlipay, "provider": model.PaymentProviderXorPay,
			"route_id": "alipay_primary", "public_method": "alipay", "channel_alias": "qr",
		},
	}

	routes := publicPaymentRoutesLocked()
	require.GreaterOrEqual(t, len(routes), 2)
	assert.Equal(t, "Alipay", routes[0].PaymentMethod, "Epay method identifiers are case-sensitive")
	assert.Equal(t, "alipay", routes[0].PublicMethod)
	assert.Equal(t, "alipay", routes[1].PublicMethod)
	assert.NotEqual(t, routes[0].RouteID, routes[1].RouteID)
}

func TestPublicPaymentRoutesKeepConfiguredCustomAndHostedChannels(t *testing.T) {
	originalMethods := operation_setting.PayMethods
	originalEnabledMethods := setting.XorPayEnabledMethods
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		setting.XorPayEnabledMethods = originalEnabledMethods
	})
	setting.XorPayEnabledMethods = nil
	operation_setting.PayMethods = []map[string]string{
		{"type": "regional_card", "provider": model.PaymentProviderEpay, "public_method": "regional_card", "channel_alias": "redirect"},
	}

	routes := publicPaymentRoutesLocked()
	customFound := false
	stripeFound := false
	for _, route := range routes {
		switch {
		case route.Provider == model.PaymentProviderEpay && route.PaymentMethod == "regional_card":
			customFound = true
			assert.Equal(t, "regional_card", route.PublicMethod)
		case route.Provider == model.PaymentProviderStripe:
			stripeFound = true
			assert.Equal(t, "card", route.PublicMethod)
			assert.Equal(t, "checkout", route.ChannelAlias)
		}
	}
	assert.True(t, customFound)
	assert.True(t, stripeFound)
}

func TestPublicPaymentRoutesHonorConfiguredWaffoPancakeRoute(t *testing.T) {
	originalMethods := operation_setting.PayMethods
	originalEnabledMethods := setting.XorPayEnabledMethods
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		setting.XorPayEnabledMethods = originalEnabledMethods
	})
	setting.XorPayEnabledMethods = nil
	operation_setting.PayMethods = []map[string]string{
		{
			"name": "Hosted payment", "type": model.PaymentMethodWaffoPancake,
			"provider": model.PaymentProviderWaffoPancake, "route_id": "premium_checkout",
			"public_method": "online_payment", "channel_alias": "alternative_checkout",
			"min_topup": "17",
		},
	}

	routes := publicPaymentRoutesLocked()
	pancakeRoutes := make([]PublicPaymentRoute, 0, 1)
	for _, route := range routes {
		if route.Provider == model.PaymentProviderWaffoPancake {
			pancakeRoutes = append(pancakeRoutes, route)
		}
	}
	require.Len(t, pancakeRoutes, 1)
	assert.Equal(t, "premium_checkout", pancakeRoutes[0].RouteID)
	assert.Equal(t, "online_payment", pancakeRoutes[0].PublicMethod)
	assert.Equal(t, "alternative_checkout", pancakeRoutes[0].ChannelAlias)
	assert.Equal(t, "17", pancakeRoutes[0].MinimumTopUp)
}

func TestPublicPaymentRouteJSONNeverContainsInternalGatewayIdentifiers(t *testing.T) {
	route := publicPaymentRouteFromValues(model.PaymentProviderXorPay, model.PaymentMethodXorPayAlipay, map[string]string{
		"route_id": "alipay_primary", "public_method": "alipay", "channel_alias": "qr",
	})
	payload, err := common.Marshal(route)
	require.NoError(t, err)

	serialized := string(payload)
	assert.Contains(t, serialized, `"route_id":"alipay_primary"`)
	assert.Contains(t, serialized, `"public_method":"alipay"`)
	assert.NotContains(t, serialized, `"provider"`)
	assert.NotContains(t, serialized, model.PaymentProviderXorPay)
	assert.NotContains(t, serialized, model.PaymentMethodXorPayAlipay)
}

func TestPublicPaymentRouteForOrderFallsBackWithoutLeakingProvider(t *testing.T) {
	originalMethods := operation_setting.PayMethods
	originalEnabledMethods := setting.XorPayEnabledMethods
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		setting.XorPayEnabledMethods = originalEnabledMethods
	})
	operation_setting.PayMethods = nil
	setting.XorPayEnabledMethods = nil

	route := PublicPaymentRouteForOrder(model.PaymentProviderStripe, model.PaymentMethodStripe)
	assert.Equal(t, "card", route.PublicMethod)
	assert.Equal(t, "checkout", route.ChannelAlias)
	assert.NotContains(t, route.RouteID, model.PaymentProviderStripe)
}

func TestPublicPaymentRoutesRejectConfiguredAliasShadowingImplicitRoute(t *testing.T) {
	stripeRouteID := operation_setting.PublicPaymentRouteID(
		model.PaymentProviderStripe,
		model.PaymentMethodStripe,
	)
	routes := []PublicPaymentRoute{
		publicPaymentRouteFromValues(model.PaymentProviderEpay, "alipay", map[string]string{
			"route_id": stripeRouteID, "public_method": "alipay", "channel_alias": "redirect",
		}),
		publicPaymentRouteFromValues(model.PaymentProviderStripe, model.PaymentMethodStripe, nil),
	}

	err := validatePublicPaymentRoutes(routes)
	assert.ErrorIs(t, err, ErrPublicPaymentRouteConflict)
}

func TestPublicPaymentLabelHidesSeparatedProviderIdentifiers(t *testing.T) {
	assert.False(t, safePublicPaymentAlias("xor_pay"))
	assert.False(t, safePublicPaymentAlias("stripe_com"))
	for _, value := range []string{"XOR Pay", "Stripe.com checkout", "WAF-FO", "Cre em", "E Pay"} {
		assert.Equal(t, "Online payment", PublicPaymentLabel(value, "Online payment"), value)
	}
	assert.Equal(t, "Prepayment package", PublicPaymentLabel("Prepayment package", "Online payment"))
	assert.Equal(t, "Online payment", PublicPaymentLabel("XOR Pay", "Stripe.com"))
}
