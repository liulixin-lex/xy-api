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
	originalPayAddress, originalEpayID, originalEpayKey := operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey
	originalXorPayAid, originalXorPaySecret := setting.XorPayAid, setting.XorPayAppSecret
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		setting.XorPayEnabledMethods = originalEnabledMethods
		operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = originalPayAddress, originalEpayID, originalEpayKey
		setting.XorPayAid, setting.XorPayAppSecret = originalXorPayAid, originalXorPaySecret
	})
	operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = "https://payments.example.com", "merchant", "secret"
	setting.XorPayAid, setting.XorPayAppSecret = "aid", "secret"
	setting.XorPayEnabledMethods = []string{setting.XorPayMethodAlipay}
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

func TestPublicPaymentRoutesUseOnlyConfiguredCatalogEntries(t *testing.T) {
	originalMethods := operation_setting.PayMethods
	originalEnabledMethods := setting.XorPayEnabledMethods
	originalPayAddress, originalEpayID, originalEpayKey := operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		setting.XorPayEnabledMethods = originalEnabledMethods
		operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = originalPayAddress, originalEpayID, originalEpayKey
	})
	setting.XorPayEnabledMethods = nil
	operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = "https://payments.example.com", "merchant", "secret"
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
		}
	}
	assert.True(t, customFound)
	assert.False(t, stripeFound, "a configured provider must not auto-publish a second catalog entry")
}

func TestPublicPaymentRoutesHonorConfiguredWaffoPancakeRoute(t *testing.T) {
	originalMethods := operation_setting.PayMethods
	originalEnabledMethods := setting.XorPayEnabledMethods
	originalMerchantID, originalPrivateKey := setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey
	originalStoreID, originalProductID := setting.WaffoPancakeStoreID, setting.WaffoPancakeProductID
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		setting.XorPayEnabledMethods = originalEnabledMethods
		setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey = originalMerchantID, originalPrivateKey
		setting.WaffoPancakeStoreID, setting.WaffoPancakeProductID = originalStoreID, originalProductID
	})
	setting.XorPayEnabledMethods = nil
	setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey = "merchant", "private-key"
	setting.WaffoPancakeStoreID, setting.WaffoPancakeProductID = "store", ""
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
	assert.Empty(t, setting.WaffoPancakeProductID, "subscription-only routes must not require the global top-up product")
	assert.Equal(t, "premium_checkout", pancakeRoutes[0].RouteID)
	assert.Equal(t, "online_payment", pancakeRoutes[0].PublicMethod)
	assert.Equal(t, "alternative_checkout", pancakeRoutes[0].ChannelAlias)
	assert.Equal(t, "17", pancakeRoutes[0].MinimumTopUp)
}

func TestPublicPaymentRoutesRequireCatalogAndEnabledProviderCapability(t *testing.T) {
	originalMethods := operation_setting.PayMethods
	originalEnabledMethods := setting.XorPayEnabledMethods
	originalAid, originalSecret := setting.XorPayAid, setting.XorPayAppSecret
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		setting.XorPayEnabledMethods = originalEnabledMethods
		setting.XorPayAid, setting.XorPayAppSecret = originalAid, originalSecret
	})

	setting.XorPayAid, setting.XorPayAppSecret = "aid", "secret"
	operation_setting.PayMethods = []map[string]string{{
		"type": model.PaymentMethodXorPayNative, "provider": model.PaymentProviderXorPay,
		"route_id": "wechat_primary", "public_method": "wechat_pay", "channel_alias": "qr",
	}}
	setting.XorPayEnabledMethods = nil
	assert.Empty(t, publicPaymentRoutesLocked(), "a disabled provider method must not remain user-selectable")

	setting.XorPayEnabledMethods = []string{setting.XorPayMethodNative}
	routes := publicPaymentRoutesLocked()
	require.Len(t, routes, 1)
	assert.Equal(t, "wechat_primary", routes[0].RouteID)

	operation_setting.PayMethods = nil
	assert.Empty(t, publicPaymentRoutesLocked(), "provider settings must not auto-generate a public route")
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

func TestSubscriptionQuoteTermsForPlanEnforceTheRouteSpecificContract(t *testing.T) {
	originalUnitPrice := setting.WaffoPancakeUnitPrice
	t.Cleanup(func() {
		setting.WaffoPancakeUnitPrice = originalUnitPrice
	})
	setting.WaffoPancakeUnitPrice = 1

	validPlan := model.SubscriptionPlan{
		Id: 31, Title: "Hosted access", PriceAmount: 10, Currency: "USD",
		DurationUnit: model.SubscriptionDurationMonth, DurationValue: 1,
		TotalAmount: 1000, WaffoPancakeProductId: "private_product", Enabled: true,
	}
	validPlan.NormalizeDefaults()
	terms, err := subscriptionQuoteTermsForPlan(model.PaymentProviderWaffoPancake, &validPlan)
	require.NoError(t, err)
	assert.Equal(t, "10", terms.payable.String())
	assert.Equal(t, float64(1), terms.unitPrice)
	assert.Equal(t, "USD", terms.currency)
	assert.Equal(t, "private_product", terms.planSnapshot.WaffoPancakeProductId)

	missingProduct := validPlan
	missingProduct.WaffoPancakeProductId = ""
	_, err = subscriptionQuoteTermsForPlan(model.PaymentProviderWaffoPancake, &missingProduct)
	assert.EqualError(t, err, "Waffo Pancake product is not configured for this plan")

	freePlan := validPlan
	freePlan.PriceAmount = 0
	_, err = subscriptionQuoteTermsForPlan(model.PaymentProviderWaffoPancake, &freePlan)
	assert.EqualError(t, err, "套餐金额过低")

	nonUSDPlan := validPlan
	nonUSDPlan.Currency = "EUR"
	_, err = subscriptionQuoteTermsForPlan(model.PaymentProviderWaffoPancake, &nonUSDPlan)
	assert.EqualError(t, err, "external payment subscription plans must use USD as the base currency")

	setting.WaffoPancakeUnitPrice = 0.0001
	_, err = subscriptionQuoteTermsForPlan(model.PaymentProviderWaffoPancake, &validPlan)
	assert.EqualError(t, err, "payment amount is too low")

	setting.WaffoPancakeUnitPrice = 0
	_, err = subscriptionQuoteTermsForPlan(model.PaymentProviderWaffoPancake, &validPlan)
	assert.EqualError(t, err, "invalid waffo_pancake subscription pricing configuration")
}
