package controller

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetTopUpInfoExposesOnlyEnabledPublicPaymentAliases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMidjourneyControllerBillingDB(t)
	t.Setenv("PAYMENT_SECRET_KEY", "test-payment-secret-key-at-least-32-bytes")
	originalAid := setting.XorPayAid
	originalSecret := setting.XorPayAppSecret
	originalCurrency := setting.XorPayCurrency
	originalEnabledMethods := setting.XorPayEnabledMethods
	originalPayMethods := operation_setting.PayMethods
	originalCallbackAddress := operation_setting.CustomCallbackAddress
	paymentSetting := operation_setting.GetPaymentSetting()
	originalComplianceConfirmed := paymentSetting.ComplianceConfirmed
	originalComplianceVersion := paymentSetting.ComplianceTermsVersion
	t.Cleanup(func() {
		setting.XorPayAid = originalAid
		setting.XorPayAppSecret = originalSecret
		setting.XorPayCurrency = originalCurrency
		setting.XorPayEnabledMethods = originalEnabledMethods
		operation_setting.PayMethods = originalPayMethods
		operation_setting.CustomCallbackAddress = originalCallbackAddress
		paymentSetting.ComplianceConfirmed = originalComplianceConfirmed
		paymentSetting.ComplianceTermsVersion = originalComplianceVersion
	})

	setting.XorPayAid = "xorpay_test_aid"
	setting.XorPayAppSecret = "xorpay_test_secret"
	setting.XorPayCurrency = "CNY"
	setting.XorPayEnabledMethods = []string{setting.XorPayMethodNative}
	operation_setting.CustomCallbackAddress = "https://api.example.com"
	operation_setting.PayMethods = []map[string]string{
		{"name": "XORPay WeChat", "type": model.PaymentMethodXorPayNative, "provider": model.PaymentProviderXorPay},
		{"name": "XORPay Alipay", "type": model.PaymentMethodXorPayAlipay, "provider": model.PaymentProviderXorPay},
	}
	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest("GET", "/api/user/topup", nil)
	GetTopUpInfo(context)

	require.Equal(t, 200, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"payment_routes"`)
	assert.Contains(t, body, `"public_method":"wechat_pay"`)
	assert.Contains(t, body, `"channel_alias":"qr"`)
	assert.Contains(t, body, `"checkout_mode":"quote"`)
	assert.NotContains(t, body, `"public_method":"alipay"`)
	assert.NotContains(t, body, `"provider"`)
	assert.NotContains(t, body, model.PaymentMethodXorPayNative)
	assert.NotContains(t, body, model.PaymentMethodXorPayAlipay)
	assert.NotContains(t, body, "XORPay")
	assert.NotContains(t, body, "xorpay")
	assert.NotContains(t, body, `"pay_methods"`)
	assert.NotContains(t, body, `"enable_stripe_topup"`)
	assert.NotContains(t, body, `"enable_creem_topup"`)
	assert.NotContains(t, body, `"creem_products"`)
	assert.NotContains(t, body, `"waffo_pay_methods"`)
}

func TestGetTopUpInfoUsesSeparateOpaqueRetainedSelectors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "topup-info-selector-key-0123456789abcdef")
	setupMidjourneyControllerBillingDB(t)
	confirmPaymentComplianceForTest(t)
	originalCreemAPIKey := setting.CreemApiKey
	originalCreemWebhookSecret := setting.CreemWebhookSecret
	originalCreemProducts := setting.CreemProducts
	originalWaffoEnabled := setting.WaffoEnabled
	originalWaffoSandbox := setting.WaffoSandbox
	originalWaffoAPIKey := setting.WaffoApiKey
	originalWaffoPrivateKey := setting.WaffoPrivateKey
	originalWaffoPublicCert := setting.WaffoPublicCert
	t.Cleanup(func() {
		setting.CreemApiKey = originalCreemAPIKey
		setting.CreemWebhookSecret = originalCreemWebhookSecret
		setting.CreemProducts = originalCreemProducts
		setting.WaffoEnabled = originalWaffoEnabled
		setting.WaffoSandbox = originalWaffoSandbox
		setting.WaffoApiKey = originalWaffoAPIKey
		setting.WaffoPrivateKey = originalWaffoPrivateKey
		setting.WaffoPublicCert = originalWaffoPublicCert
	})

	setting.CreemApiKey = "creem-test-api-key"
	setting.CreemWebhookSecret = "creem-test-webhook-secret"
	setting.CreemProducts = `[{"productId":"prod_private_catalog_id","name":"Creem Starter","price":9.99,"currency":"USD","quota":1000}]`
	setting.WaffoEnabled = true
	setting.WaffoSandbox = false
	setting.WaffoApiKey = "test-api-key"
	setting.WaffoPrivateKey = "test-private-key"
	setting.WaffoPublicCert = "test-public-cert"

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest("GET", "/api/user/topup", nil)
	GetTopUpInfo(context)

	require.Equal(t, 200, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"checkout_mode":"product"`)
	assert.Contains(t, body, `"product_id":"product_`)
	assert.Contains(t, body, `"name":"Online payment"`)
	assert.Contains(t, body, `"checkout_mode":"option"`)
	assert.Contains(t, body, `"option_id":"option_`)
	assert.Contains(t, body, `"public_label":"Card"`)
	assert.NotContains(t, body, "prod_private_catalog_id")
	assert.NotContains(t, body, "payMethodType")
	assert.NotContains(t, body, "payMethodName")
	assert.NotContains(t, body, "pay_method_index")
	assert.NotContains(t, strings.ToLower(body), "creem")
	assert.NotContains(t, strings.ToLower(body), "waffo")
}

func TestGetTopUpInfoHonorsConfiguredWaffoPancakePublicRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "topup-info-pancake-route-key-0123456789")
	setupMidjourneyControllerBillingDB(t)
	confirmPaymentComplianceForTest(t)

	originalMethods := operation_setting.PayMethods
	originalMerchantID := setting.WaffoPancakeMerchantID
	originalPrivateKey := setting.WaffoPancakePrivateKey
	originalProductID := setting.WaffoPancakeProductID
	originalMinimum := setting.WaffoPancakeMinTopUp
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		setting.WaffoPancakeMerchantID = originalMerchantID
		setting.WaffoPancakePrivateKey = originalPrivateKey
		setting.WaffoPancakeProductID = originalProductID
		setting.WaffoPancakeMinTopUp = originalMinimum
	})

	operation_setting.PayMethods = []map[string]string{
		{
			"name": "Hosted payment", "type": model.PaymentMethodWaffoPancake,
			"provider": model.PaymentProviderWaffoPancake, "route_id": "premium_checkout",
			"public_method": "online_payment", "channel_alias": "alternative_checkout",
			"min_topup": "17",
		},
	}
	setting.WaffoPancakeMerchantID = "pancake-merchant"
	setting.WaffoPancakePrivateKey = "pancake-private-key"
	setting.WaffoPancakeProductID = "pancake-product"
	setting.WaffoPancakeMinTopUp = 5

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest("GET", "/api/user/topup", nil)
	GetTopUpInfo(context)

	require.Equal(t, 200, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			PaymentRoutes []publicTopUpRouteView `json:"payment_routes"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Len(t, response.Data.PaymentRoutes, 1)
	route := response.Data.PaymentRoutes[0]
	assert.Equal(t, "premium_checkout", route.RouteID)
	assert.Equal(t, "online_payment", route.PublicMethod)
	assert.Equal(t, "alternative_checkout", route.ChannelAlias)
	assert.Equal(t, publicCheckoutModeDirect, route.CheckoutMode)
	assert.Equal(t, "USD", route.Currency)
	assert.EqualValues(t, 17, route.MinimumTopUp)
	assert.NotContains(t, strings.ToLower(recorder.Body.String()), "waffo")
}

func TestGetTopUpInfoHidesEveryPaymentSelectionUntilComplianceConfirmed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "topup-info-compliance-key-0123456789abcdef")
	setupMidjourneyControllerBillingDB(t)

	paymentSetting := operation_setting.GetPaymentSetting()
	originalComplianceConfirmed := paymentSetting.ComplianceConfirmed
	originalComplianceVersion := paymentSetting.ComplianceTermsVersion
	originalPayAddress := operation_setting.PayAddress
	originalEpayID := operation_setting.EpayId
	originalEpayKey := operation_setting.EpayKey
	originalPayMethods := operation_setting.PayMethods
	originalCreemAPIKey := setting.CreemApiKey
	originalCreemWebhookSecret := setting.CreemWebhookSecret
	originalCreemProducts := setting.CreemProducts
	originalWaffoEnabled := setting.WaffoEnabled
	originalWaffoAPIKey := setting.WaffoApiKey
	originalWaffoPrivateKey := setting.WaffoPrivateKey
	originalWaffoPublicCert := setting.WaffoPublicCert
	originalWaffoPancakeMerchantID := setting.WaffoPancakeMerchantID
	originalWaffoPancakePrivateKey := setting.WaffoPancakePrivateKey
	originalWaffoPancakeProductID := setting.WaffoPancakeProductID
	t.Cleanup(func() {
		paymentSetting.ComplianceConfirmed = originalComplianceConfirmed
		paymentSetting.ComplianceTermsVersion = originalComplianceVersion
		operation_setting.PayAddress = originalPayAddress
		operation_setting.EpayId = originalEpayID
		operation_setting.EpayKey = originalEpayKey
		operation_setting.PayMethods = originalPayMethods
		setting.CreemApiKey = originalCreemAPIKey
		setting.CreemWebhookSecret = originalCreemWebhookSecret
		setting.CreemProducts = originalCreemProducts
		setting.WaffoEnabled = originalWaffoEnabled
		setting.WaffoApiKey = originalWaffoAPIKey
		setting.WaffoPrivateKey = originalWaffoPrivateKey
		setting.WaffoPublicCert = originalWaffoPublicCert
		setting.WaffoPancakeMerchantID = originalWaffoPancakeMerchantID
		setting.WaffoPancakePrivateKey = originalWaffoPancakePrivateKey
		setting.WaffoPancakeProductID = originalWaffoPancakeProductID
	})

	paymentSetting.ComplianceConfirmed = false
	paymentSetting.ComplianceTermsVersion = ""
	operation_setting.PayAddress = "https://pay.example.com"
	operation_setting.EpayId = "merchant"
	operation_setting.EpayKey = "secret"
	operation_setting.PayMethods = []map[string]string{{
		"name": "Alipay", "type": "alipay", "provider": model.PaymentProviderEpay,
	}}
	setting.CreemApiKey = "creem-test-api-key"
	setting.CreemWebhookSecret = "creem-test-webhook-secret"
	setting.CreemProducts = `[{"productId":"prod_private","name":"Starter","price":9.99,"currency":"USD","quota":1000}]`
	setting.WaffoEnabled = true
	setting.WaffoApiKey = "waffo-test-api-key"
	setting.WaffoPrivateKey = "waffo-test-private-key"
	setting.WaffoPublicCert = "waffo-test-public-cert"
	setting.WaffoPancakeMerchantID = "pancake-merchant"
	setting.WaffoPancakePrivateKey = "pancake-private-key"
	setting.WaffoPancakeProductID = "pancake-product"

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest("GET", "/api/user/topup", nil)
	GetTopUpInfo(context)

	require.Equal(t, 200, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			OnlinePaymentAvailable bool                         `json:"online_payment_available"`
			PaymentRoutes          []publicTopUpRouteView       `json:"payment_routes"`
			PaymentProducts        []publicTopUpProductView     `json:"payment_products"`
			PaymentRouteOptions    []publicTopUpRouteOptionView `json:"payment_route_options"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.False(t, response.Data.OnlinePaymentAvailable)
	assert.Empty(t, response.Data.PaymentRoutes)
	assert.Empty(t, response.Data.PaymentProducts)
	assert.Empty(t, response.Data.PaymentRouteOptions)
}

func TestPublicTopUpHistoryOmitsProviderAndInternalStatusEvidence(t *testing.T) {
	payload, err := common.Marshal(publicTopUpHistoryView{
		ID: 1, TradeNo: "PO_HISTORY", RouteID: "alipay_primary",
		PublicMethod: "alipay", ChannelAlias: "qr", StatusCode: "succeeded",
	})
	require.NoError(t, err)
	serialized := string(payload)
	assert.Contains(t, serialized, `"route_id":"alipay_primary"`)
	assert.NotContains(t, serialized, `"provider"`)
	assert.NotContains(t, serialized, `"payment_method"`)
	assert.NotContains(t, serialized, `"status_reason"`)
	assert.NotContains(t, serialized, `"payment_order_id"`)
	assert.NotContains(t, serialized, `"review_reason"`)
	assert.NotContains(t, serialized, `"provider_order_id"`)
}

func TestAdminTopUpHistoryIncludesLegacyReviewEvidence(t *testing.T) {
	payload, err := common.Marshal(adminTopUpHistory(&model.TopUp{
		Id: 2, TradeNo: "PO_ADMIN_REVIEW", PaymentProvider: model.PaymentProviderCreem,
		ProviderOrderId: "creem_order_admin_review", ReviewReason: "completed_callback_amount_mismatch",
	}))
	require.NoError(t, err)
	serialized := string(payload)
	assert.Contains(t, serialized, `"payment_provider":"creem"`)
	assert.Contains(t, serialized, `"provider_order_id":"creem_order_admin_review"`)
	assert.Contains(t, serialized, `"review_reason":"completed_callback_amount_mismatch"`)
}
