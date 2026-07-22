package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentContinuationLocaleSupportsAllCheckoutLanguages(t *testing.T) {
	tests := []struct {
		header string
		lang   string
	}{
		{header: "en-US,en;q=0.9", lang: "en"},
		{header: "zh-CN,zh;q=0.9", lang: "zh-Hans"},
		{header: "zh-TW,zh;q=0.9", lang: "zh-Hant"},
		{header: "fr-FR,fr;q=0.9", lang: "fr"},
		{header: "ja-JP,ja;q=0.9", lang: "ja"},
		{header: "ru-RU,ru;q=0.9", lang: "ru"},
		{header: "vi-VN,vi;q=0.9", lang: "vi"},
	}
	for _, test := range tests {
		t.Run(test.lang, func(t *testing.T) {
			copy := paymentContinuationLocale(test.header)
			assert.Equal(t, test.lang, copy.lang)
			assert.NotEmpty(t, copy.title)
			assert.NotEmpty(t, copy.message)
			assert.NotEmpty(t, copy.button)
		})
	}
}

func TestLegacyPaymentPageRedirectRequiresOrderOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	now := common.GetTimestamp()
	order := &model.PaymentOrder{
		TradeNo: "PO_LEGACY_ASYNC_BRIDGE", UserID: 88101, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "legacy-async-bridge",
		ExpectedAmountMinor: 100, Currency: "CNY", Status: model.PaymentOrderStatusPending,
		ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, db.Create(order).Error)

	for _, test := range []struct {
		name     string
		userID   int
		status   int
		location string
	}{
		{name: "owner", userID: order.UserID, status: http.StatusSeeOther, location: "/payment/PO_LEGACY_ASYNC_BRIDGE"},
		{name: "different user", userID: order.UserID + 1, status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Set("id", test.userID)
			context.Params = gin.Params{{Key: "trade_no", Value: order.TradeNo}}
			context.Request = httptest.NewRequest(http.MethodPost,
				"/api/user/payment/orders/"+order.TradeNo+"/legacy-continue", nil)

			LegacyPaymentPageRedirect(context)
			context.Writer.WriteHeaderNow()

			assert.Equal(t, test.status, recorder.Code)
			assert.Equal(t, test.location, recorder.Header().Get("Location"))
			if test.status == http.StatusSeeOther {
				assert.Contains(t, recorder.Header().Get("Cache-Control"), "no-store")
			}
		})
	}
}

func TestContinuePaymentWaffoAppRedirectRevalidatesProviderAndScheme(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "waffo-continue-key-0123456789abcdef")
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalSchemes := setting.WaffoAppRedirectSchemes
	t.Cleanup(func() { setting.WaffoAppRedirectSchemes = originalSchemes })
	setting.WaffoAppRedirectSchemes = "weixin"

	const tradeNo = "PO_WAFFO_APP_CONTINUE"
	const deeplink = "weixin://wap/pay?prepayid=wx123#provider-state"
	now := common.GetTimestamp()
	payload, err := common.Marshal(service.PaymentStart{
		Flow: service.PaymentFlowAppRedirect, TradeNo: tradeNo, URL: deeplink, ExpiresAt: now + 600,
	})
	require.NoError(t, err)
	encrypted, err := model.EncryptPaymentOrderStartPayload(tradeNo, string(payload))
	require.NoError(t, err)
	order := &model.PaymentOrder{
		TradeNo: tradeNo, UserID: 88102, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderWaffo, PaymentMethod: model.PaymentMethodWaffo,
		RequestID: "waffo-app-continue", ExpectedAmountMinor: 100, Currency: "USD",
		Status: model.PaymentOrderStatusPending, StartFlow: service.PaymentFlowAppRedirect,
		StartPayload: encrypted, ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, db.Create(order).Error)

	request := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Set("id", order.UserID)
		context.Params = gin.Params{{Key: "trade_no", Value: tradeNo}}
		context.Request = httptest.NewRequest(http.MethodGet,
			"/api/user/payment/orders/"+tradeNo+"/continue", nil)
		ContinuePayment(context)
		context.Writer.WriteHeaderNow()
		return recorder
	}

	allowed := request()
	assert.Equal(t, http.StatusSeeOther, allowed.Code)
	assert.Equal(t, deeplink, allowed.Header().Get("Location"))
	assert.Contains(t, allowed.Header().Get("Cache-Control"), "no-store")

	setting.WaffoAppRedirectSchemes = ""
	blocked := request()
	assert.Equal(t, http.StatusConflict, blocked.Code)
	assert.Empty(t, blocked.Header().Get("Location"))

	setting.WaffoAppRedirectSchemes = "weixin"
	require.NoError(t, db.Model(order).Update("provider", model.PaymentProviderCreem).Error)
	wrongProvider := request()
	assert.Equal(t, http.StatusConflict, wrongProvider.Code)
	assert.Empty(t, wrongProvider.Header().Get("Location"))
}

func TestContinuePaymentStripeRevalidatesExactCheckoutHost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "stripe-continue-key-0123456789abcdef")
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.PaymentOrder{}))
	originalAllowedHosts := setting.StripeCheckoutAllowedHosts
	common.OptionMapRWMutex.Lock()
	optionMapWasNil := common.OptionMap == nil
	if optionMapWasNil {
		common.OptionMap = make(map[string]string)
	}
	originalVersion, versionExisted := common.OptionMap[model.PaymentConfigurationVersionOptionKey]
	setting.StripeCheckoutAllowedHosts = "pay.example.com"
	common.OptionMap[model.PaymentConfigurationVersionOptionKey] = "1"
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		setting.StripeCheckoutAllowedHosts = originalAllowedHosts
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if optionMapWasNil {
			common.OptionMap = nil
		} else if versionExisted {
			common.OptionMap[model.PaymentConfigurationVersionOptionKey] = originalVersion
		} else {
			delete(common.OptionMap, model.PaymentConfigurationVersionOptionKey)
		}
	})

	const tradeNo = "PO_STRIPE_CUSTOM_CONTINUE"
	const checkoutURL = "https://pay.example.com/checkout/session"
	now := common.GetTimestamp()
	payload, err := common.Marshal(service.PaymentStart{
		Flow: service.PaymentFlowHostedRedirect, TradeNo: tradeNo, URL: checkoutURL, ExpiresAt: now + 600,
	})
	require.NoError(t, err)
	encrypted, err := model.EncryptPaymentOrderStartPayload(tradeNo, string(payload))
	require.NoError(t, err)
	order := &model.PaymentOrder{
		TradeNo: tradeNo, UserID: 88103, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderStripe, PaymentMethod: model.PaymentMethodStripe,
		RequestID: "stripe-custom-continue", ExpectedAmountMinor: 100, Currency: "USD",
		Status: model.PaymentOrderStatusPending, StartFlow: service.PaymentFlowHostedRedirect,
		StartPayload: encrypted, ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, db.Create(order).Error)

	request := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Set("id", order.UserID)
		context.Params = gin.Params{{Key: "trade_no", Value: tradeNo}}
		context.Request = httptest.NewRequest(http.MethodGet,
			"/api/user/payment/orders/"+tradeNo+"/continue", nil)
		ContinuePayment(context)
		context.Writer.WriteHeaderNow()
		return recorder
	}

	allowed := request()
	assert.Equal(t, http.StatusSeeOther, allowed.Code)
	assert.Equal(t, checkoutURL, allowed.Header().Get("Location"))

	setting.StripeCheckoutAllowedHosts = ""
	blocked := request()
	assert.Equal(t, http.StatusConflict, blocked.Code)
	assert.Empty(t, blocked.Header().Get("Location"))

	setting.StripeCheckoutAllowedHosts = "example.com"
	blockedSubdomain := request()
	assert.Equal(t, http.StatusConflict, blockedSubdomain.Code)
}
