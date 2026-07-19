package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v86"
	stripewebhook "github.com/stripe/stripe-go/v86/webhook"
)

type paymentRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn paymentRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestFillSubscriptionQuoteStripeDoesNotApplyTopUpUnitPrice(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.SubscriptionPlan{}))
	require.NoError(t, model.DB.Exec("DELETE FROM subscription_plans").Error)
	t.Cleanup(func() { _ = model.DB.Exec("DELETE FROM subscription_plans").Error })

	originalUnitPrice := setting.StripeUnitPrice
	originalCurrency := setting.StripeCurrency
	t.Cleanup(func() {
		setting.StripeUnitPrice = originalUnitPrice
		setting.StripeCurrency = originalCurrency
	})
	setting.StripeUnitPrice = 8
	setting.StripeCurrency = "USD"
	allowWallet := true
	plan := &model.SubscriptionPlan{
		Id:                  980101,
		Title:               "Fixed USD plan",
		PriceAmount:         9.99,
		Currency:            "USD",
		DurationUnit:        model.SubscriptionDurationMonth,
		DurationValue:       1,
		Enabled:             true,
		AllowWalletOverflow: &allowWallet,
		QuotaResetPeriod:    model.SubscriptionResetNever,
	}
	require.NoError(t, model.DB.Create(plan).Error)

	quote := &model.PaymentQuote{Provider: model.PaymentProviderStripe}
	payable, err := fillSubscriptionQuote(quote, plan.Id)
	require.NoError(t, err)
	assert.True(t, payable.Equal(decimal.RequireFromString("9.99")))
	assert.Equal(t, "USD", quote.Currency)
	assert.EqualValues(t, plan.Id, quote.RequestedAmount)

	var snapshot model.SubscriptionPlanSnapshot
	require.NoError(t, common.UnmarshalJsonStr(quote.ProductSnapshot, &snapshot))
	assert.Equal(t, plan.Id, snapshot.PlanId)
}

func TestEffectivePaymentMethodMinimumCannotBeBypassedByTheClient(t *testing.T) {
	originalMethods := operation_setting.PayMethods
	originalMinimum := operation_setting.MinTopUp
	t.Cleanup(func() {
		operation_setting.PayMethods = originalMethods
		operation_setting.MinTopUp = originalMinimum
	})
	operation_setting.MinTopUp = 10
	operation_setting.PayMethods = []map[string]string{
		{"name": "High minimum", "type": "custom_high", "provider": model.PaymentProviderEpay, "min_topup": "50"},
		{"name": "Low minimum", "type": "custom_low", "provider": model.PaymentProviderEpay, "min_topup": "1"},
	}

	minimum, err := EffectivePaymentMethodMinimum(model.PaymentProviderEpay, "custom_high")
	require.NoError(t, err)
	assert.EqualValues(t, 50, minimum)
	minimum, err = EffectivePaymentMethodMinimum(model.PaymentProviderEpay, "custom_low")
	require.NoError(t, err)
	assert.EqualValues(t, 10, minimum)
}

func TestNormalizePaymentMethodPreservesCaseSensitiveEpayTypes(t *testing.T) {
	assert.Equal(t, "CustomQR", NormalizePaymentMethod(model.PaymentProviderEpay, " CustomQR "))
	assert.Equal(t, model.PaymentMethodStripe, NormalizePaymentMethod(model.PaymentProviderStripe, " STRIPE "))
	assert.Equal(t, model.PaymentMethodXorPayNative, NormalizePaymentMethod(model.PaymentProviderXorPay, " XORPAY_NATIVE "))
}

func TestEpayCreateAndCallbackPreserveConfiguredMethodCase(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOrder{}, &model.TopUp{}, &model.SubscriptionOrder{}))
	originalAddress := operation_setting.PayAddress
	originalCallback := operation_setting.CustomCallbackAddress
	originalCurrency := operation_setting.EpayCurrency
	originalMethods := operation_setting.PayMethods
	originalID, originalKey := operation_setting.EpayId, operation_setting.EpayKey
	originalGeneration := operation_setting.EpayCredentialGeneration
	t.Cleanup(func() {
		operation_setting.PayAddress = originalAddress
		operation_setting.CustomCallbackAddress = originalCallback
		operation_setting.EpayCurrency = originalCurrency
		operation_setting.PayMethods = originalMethods
		operation_setting.EpayId, operation_setting.EpayKey = originalID, originalKey
		operation_setting.EpayCredentialGeneration = originalGeneration
		model.DB.Where("trade_no = ?", "PO_EPAY_CASE_SENSITIVE_METHOD").Delete(&model.PaymentOrder{})
	})
	operation_setting.PayAddress = "https://pay.example.com"
	operation_setting.CustomCallbackAddress = "https://api.example.com"
	operation_setting.EpayCurrency = "CNY"
	operation_setting.PayMethods = []map[string]string{{
		"name": "Case-sensitive custom method", "type": "CustomQR", "provider": model.PaymentProviderEpay,
	}}
	operation_setting.EpayId = "case_sensitive_merchant"
	operation_setting.EpayKey = "case_sensitive_secret"
	operation_setting.EpayCredentialGeneration = 1

	order := &model.PaymentOrder{
		TradeNo: "PO_EPAY_CASE_SENSITIVE_METHOD", UserID: 998815, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderEpay, PaymentMethod: "CustomQR", RequestID: "epay_case_sensitive_method",
		ProviderCredentialGeneration: 1, ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1,
		CreditQuota: 1, Status: model.PaymentOrderStatusPending, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)

	provider := &epayPaymentProvider{}
	start, err := provider.Create(t.Context(), order)
	require.NoError(t, err)
	assert.Equal(t, "CustomQR", start.Fields["type"])

	params := map[string]string{
		"pid": operation_setting.EpayId, "trade_no": "gateway_case_sensitive", "out_trade_no": order.TradeNo,
		"type": "CustomQR", "name": "AI API top-up", "money": "1.00", "trade_status": epay.StatusTradeSuccess,
	}
	epay.GenerateParams(params, operation_setting.EpayKey)
	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/payment/epay/notify", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	event, err := provider.VerifyWebhook(request)
	require.NoError(t, err)
	assert.Equal(t, "CustomQR", event.PaymentMethod)
}

func TestPaymentCreateRequiresDedicatedCredentialEncryption(t *testing.T) {
	paymentSetting := operation_setting.GetPaymentSetting()
	originalConfirmed := paymentSetting.ComplianceConfirmed
	originalTermsVersion := paymentSetting.ComplianceTermsVersion
	originalAddress := operation_setting.PayAddress
	originalID := operation_setting.EpayId
	originalKey := operation_setting.EpayKey
	originalMethods := operation_setting.PayMethods
	t.Cleanup(func() {
		paymentSetting.ComplianceConfirmed = originalConfirmed
		paymentSetting.ComplianceTermsVersion = originalTermsVersion
		operation_setting.PayAddress = originalAddress
		operation_setting.EpayId = originalID
		operation_setting.EpayKey = originalKey
		operation_setting.PayMethods = originalMethods
	})
	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	operation_setting.PayAddress = "https://pay.example.com"
	operation_setting.EpayId = "merchant"
	operation_setting.EpayKey = "aaaaaaaaaaaaaaaa"
	operation_setting.PayMethods = []map[string]string{{"name": "Alipay", "type": "alipay", "provider": model.PaymentProviderEpay}}
	t.Setenv("PAYMENT_SECRET_KEY", "")

	err := ValidatePaymentProviderForCreate(model.PaymentProviderEpay, "alipay")
	assert.ErrorContains(t, err, "PAYMENT_SECRET_KEY")
}

func TestStripeLegacyReturnURLsAreSnapshottedAndRevalidated(t *testing.T) {
	originalDomains := constant.TrustedRedirectDomains
	constant.TrustedRedirectDomains = []string{"merchant.example"}
	t.Cleanup(func() { constant.TrustedRedirectDomains = originalDomains })

	quote := &model.PaymentQuote{
		Provider:        model.PaymentProviderStripe,
		PricingSnapshot: `{"currency":"USD"}`,
	}
	require.NoError(t, applyStripeReturnURLSnapshot(
		quote,
		"https://merchant.example/payment/success?source=legacy",
		"https://merchant.example/payment/cancel",
	))

	order := &model.PaymentOrder{TradeNo: "PO_RETURN_URL", PricingSnapshot: quote.PricingSnapshot}
	successURL, cancelURL, err := stripeCheckoutReturnURLs(order)
	require.NoError(t, err)
	assert.Equal(t, "https://merchant.example/payment/success?source=legacy", successURL)
	assert.Equal(t, "https://merchant.example/payment/cancel", cancelURL)

	order.PricingSnapshot = `{"stripe_success_url":"https://attacker.example/steal","stripe_cancel_url":"https://merchant.example/cancel"}`
	_, _, err = stripeCheckoutReturnURLs(order)
	assert.Error(t, err)
}

func TestResolveStripeCredentialAccountUsesAuthenticatedBoundedEndpoint(t *testing.T) {
	t.Setenv(setting.StripeTestModeEnabledEnv, "true")
	const secret = "sk_test_identity_secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, secret, username)
		assert.Empty(t, password)
		assert.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"acct_verified123"}`))
	}))
	defer server.Close()

	originalEndpoint := stripeAccountIdentityEndpoint
	originalClient := stripeConfigurationVerificationHTTPClient
	stripeAccountIdentityEndpoint = server.URL
	stripeConfigurationVerificationHTTPClient = server.Client()
	t.Cleanup(func() {
		stripeAccountIdentityEndpoint = originalEndpoint
		stripeConfigurationVerificationHTTPClient = originalClient
	})

	accountID, err := ResolveStripeCredentialAccount(context.Background(), secret)
	require.NoError(t, err)
	assert.Equal(t, "acct_verified123", accountID)
}

func TestStripeTestConfigurationVerificationFailsClosedByDefault(t *testing.T) {
	t.Setenv(setting.StripeTestModeEnabledEnv, "")
	_, err := ResolveStripeCredentialAccount(t.Context(), "sk_test_disabled")
	require.Error(t, err)
	assert.Contains(t, err.Error(), setting.StripeTestModeEnabledEnv)

	_, err = VerifyStripeCheckoutConfiguration(
		t.Context(), "sk_test_disabled", "acct_platformdisabled", "",
		"price_disabled", "USD", "test",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), setting.StripeTestModeEnabledEnv)
}

func TestStripeTestRuntimeOperationsFailClosedWhenSandboxIsDisabled(t *testing.T) {
	t.Setenv(setting.StripeTestModeEnabledEnv, "false")
	originalSecret := setting.StripeApiSecret
	originalCredentialMode := setting.StripeCredentialLivemode
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	t.Cleanup(func() {
		setting.StripeApiSecret = originalSecret
		setting.StripeCredentialLivemode = originalCredentialMode
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
	})
	setting.StripeApiSecret = "sk_test_runtime_disabled"
	setting.StripeCredentialLivemode = "test"
	setting.StripeWebhookCredentialLivemode = "test"
	provider := &stripePaymentProvider{}
	providerOrderKey := "stripe:cs_test_runtime_disabled"
	order := &model.PaymentOrder{
		TradeNo: "PO_STRIPE_TEST_RUNTIME_DISABLED", Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, ProviderOrderKey: &providerOrderKey,
	}

	_, err := provider.Create(t.Context(), order)
	require.Error(t, err)
	assert.Contains(t, err.Error(), setting.StripeTestModeEnabledEnv)
	_, err = provider.Query(t.Context(), order)
	require.Error(t, err)
	assert.Contains(t, err.Error(), setting.StripeTestModeEnabledEnv)
	_, err = provider.RecoverStart(t.Context(), order)
	require.Error(t, err)
	assert.Contains(t, err.Error(), setting.StripeTestModeEnabledEnv)
}

func TestPaymentMinorConversionSupportsISOExponentsAndBounds(t *testing.T) {
	minor, err := paymentAmountMinor(decimal.RequireFromString("123"), 0)
	require.NoError(t, err)
	assert.EqualValues(t, 123, minor)

	minor, err = paymentAmountMinor(decimal.RequireFromString("1.234"), 3)
	require.NoError(t, err)
	assert.EqualValues(t, 1234, minor)

	minor, err = parsePaymentMinor("123", 0)
	require.NoError(t, err)
	assert.EqualValues(t, 123, minor)

	minor, err = parsePaymentMinor("1.234", 3)
	require.NoError(t, err)
	assert.EqualValues(t, 1234, minor)

	_, err = parsePaymentMinor("999999999999999999999999999999999999", 2)
	assert.Error(t, err)

	stripeISKExponent, ok := common.PaymentProviderCurrencyExponentOK(model.PaymentProviderStripe, "ISK")
	require.True(t, ok)
	assert.Equal(t, int32(2), stripeISKExponent)
	minor, err = paymentAmountMinorForProvider(decimal.RequireFromString("5"), model.PaymentProviderStripe, "ISK")
	require.NoError(t, err)
	assert.EqualValues(t, 500, minor)
	_, err = paymentAmountMinorForProvider(decimal.RequireFromString("5.25"), model.PaymentProviderStripe, "ISK")
	assert.Error(t, err)
}

func TestEpayWebhookVerificationBindsSignedMerchantAmountAndMethod(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOrder{}))
	originalID := operation_setting.EpayId
	originalKey := operation_setting.EpayKey
	originalCurrency := operation_setting.EpayCurrency
	t.Cleanup(func() {
		operation_setting.EpayId = originalID
		operation_setting.EpayKey = originalKey
		operation_setting.EpayCurrency = originalCurrency
	})
	operation_setting.EpayId = "merchant_123"
	operation_setting.EpayKey = "epay_test_secret"
	operation_setting.EpayCurrency = "CNY"

	params := map[string]string{
		"pid":          operation_setting.EpayId,
		"trade_no":     "gateway_1001",
		"out_trade_no": "PO_TEST_1001",
		"type":         "alipay",
		"name":         "AI API top-up",
		"money":        "73.00",
		"trade_status": epay.StatusTradeSuccess,
	}
	epay.GenerateParams(params, operation_setting.EpayKey)
	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/payment/epay/notify", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	provider, err := GetPaymentProvider(model.PaymentProviderEpay)
	require.NoError(t, err)
	event, err := provider.VerifyWebhook(request)
	require.NoError(t, err)
	assert.True(t, event.Paid)
	assert.Equal(t, "PO_TEST_1001", event.TradeNo)
	assert.Equal(t, "epay:g1:gateway_1001", event.ProviderOrderKey)
	assert.EqualValues(t, 7300, event.PaidAmountMinor)
	assert.Equal(t, "CNY", event.Currency)
	assert.Equal(t, "alipay", event.PaymentMethod)

	params["money"] = "1.00"
	badForm := url.Values{}
	for key, value := range params {
		badForm.Set(key, value)
	}
	badRequest := httptest.NewRequest(http.MethodPost, "/api/payment/epay/notify", strings.NewReader(badForm.Encode()))
	badRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = provider.VerifyWebhook(badRequest)
	assert.Error(t, err)

	missingMethod := make(map[string]string, len(params))
	for key, value := range params {
		if key != "type" && key != "sign" && key != "sign_type" {
			missingMethod[key] = value
		}
	}
	epay.GenerateParams(missingMethod, operation_setting.EpayKey)
	missingMethodForm := url.Values{}
	for key, value := range missingMethod {
		missingMethodForm.Set(key, value)
	}
	missingMethodRequest := httptest.NewRequest(http.MethodPost, "/api/payment/epay/notify", strings.NewReader(missingMethodForm.Encode()))
	missingMethodRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = provider.VerifyWebhook(missingMethodRequest)
	assert.ErrorContains(t, err, "missing required fields")
}

func TestEpayPreviousCredentialOnlyVerifiesItsBoundOrderGeneration(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOrder{}, &model.TopUp{}, &model.SubscriptionOrder{}))
	originalID, originalKey := operation_setting.EpayId, operation_setting.EpayKey
	originalGeneration := operation_setting.EpayCredentialGeneration
	originalPreviousID, originalPreviousKey := operation_setting.EpayIdPrevious, operation_setting.EpayKeyPrevious
	originalPreviousGeneration := operation_setting.EpayPreviousCredentialGeneration
	originalValidBefore, originalExpiresAt := operation_setting.EpayPreviousValidBefore, operation_setting.EpayPreviousExpiresAt
	t.Cleanup(func() {
		operation_setting.EpayId, operation_setting.EpayKey = originalID, originalKey
		operation_setting.EpayCredentialGeneration = originalGeneration
		operation_setting.EpayIdPrevious, operation_setting.EpayKeyPrevious = originalPreviousID, originalPreviousKey
		operation_setting.EpayPreviousCredentialGeneration = originalPreviousGeneration
		operation_setting.EpayPreviousValidBefore, operation_setting.EpayPreviousExpiresAt = originalValidBefore, originalExpiresAt
	})
	operation_setting.EpayId, operation_setting.EpayKey = "merchant_new", "epay_new_secret"
	operation_setting.EpayCredentialGeneration = 2
	operation_setting.EpayIdPrevious, operation_setting.EpayKeyPrevious = "merchant_old", "epay_old_secret"
	operation_setting.EpayPreviousCredentialGeneration = 1
	operation_setting.EpayPreviousValidBefore = time.Now().Unix()
	operation_setting.EpayPreviousExpiresAt = time.Now().Add(time.Hour).Unix()

	oldOrder := &model.PaymentOrder{
		TradeNo: "PO_EPAY_PREVIOUS_GENERATION", UserID: 998811, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "epay_previous_generation",
		ProviderCredentialGeneration: 1, ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1,
		CreditQuota: 1, Status: model.PaymentOrderStatusPending, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), Version: 1,
	}
	newOrder := *oldOrder
	newOrder.ID = 0
	newOrder.TradeNo = "PO_EPAY_CURRENT_GENERATION"
	newOrder.RequestID = "epay_current_generation"
	newOrder.ProviderCredentialGeneration = 2
	legacyOrder := *oldOrder
	legacyOrder.ID = 0
	legacyOrder.TradeNo = "PO_EPAY_LEGACY_PREVIOUS_GENERATION"
	legacyOrder.RequestID = "epay_legacy_previous_generation"
	legacyOrder.ProviderCredentialGeneration = 0
	legacyOrder.CreatedAt = operation_setting.EpayPreviousValidBefore - 1
	legacyOrder.UpdatedAt = legacyOrder.CreatedAt
	require.NoError(t, model.DB.Create(oldOrder).Error)
	require.NoError(t, model.DB.Create(&newOrder).Error)
	require.NoError(t, model.DB.Create(&legacyOrder).Error)
	t.Cleanup(func() {
		model.DB.Where("trade_no IN ?", []string{oldOrder.TradeNo, newOrder.TradeNo, legacyOrder.TradeNo}).Delete(&model.PaymentOrder{})
	})

	provider, err := GetPaymentProvider(model.PaymentProviderEpay)
	require.NoError(t, err)
	makeRequest := func(tradeNo string) *http.Request {
		params := map[string]string{
			"pid": operation_setting.EpayIdPrevious, "trade_no": "gateway_previous", "out_trade_no": tradeNo,
			"type": "alipay", "name": "AI API top-up", "money": "1.00", "trade_status": epay.StatusTradeSuccess,
		}
		epay.GenerateParams(params, operation_setting.EpayKeyPrevious)
		form := url.Values{}
		for key, value := range params {
			form.Set(key, value)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/payment/epay/notify", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return request
	}
	_, err = provider.VerifyWebhook(makeRequest(oldOrder.TradeNo))
	require.NoError(t, err)
	legacyEvent, err := provider.VerifyWebhook(makeRequest(legacyOrder.TradeNo))
	require.NoError(t, err)
	assert.Equal(t, operation_setting.EpayPreviousCredentialGeneration, legacyEvent.ProviderCredentialGeneration)
	_, err = provider.VerifyWebhook(makeRequest(newOrder.TradeNo))
	assert.Error(t, err)
}

func TestEpayStartRetryUsesTheOrderBoundPreviousCredential(t *testing.T) {
	originalAddress := operation_setting.PayAddress
	originalCallback := operation_setting.CustomCallbackAddress
	originalCurrency := operation_setting.EpayCurrency
	originalMethods := operation_setting.PayMethods
	originalID, originalKey := operation_setting.EpayId, operation_setting.EpayKey
	originalGeneration := operation_setting.EpayCredentialGeneration
	originalPreviousID, originalPreviousKey := operation_setting.EpayIdPrevious, operation_setting.EpayKeyPrevious
	originalPreviousGeneration := operation_setting.EpayPreviousCredentialGeneration
	originalValidBefore, originalExpiresAt := operation_setting.EpayPreviousValidBefore, operation_setting.EpayPreviousExpiresAt
	t.Cleanup(func() {
		operation_setting.PayAddress = originalAddress
		operation_setting.CustomCallbackAddress = originalCallback
		operation_setting.EpayCurrency = originalCurrency
		operation_setting.PayMethods = originalMethods
		operation_setting.EpayId, operation_setting.EpayKey = originalID, originalKey
		operation_setting.EpayCredentialGeneration = originalGeneration
		operation_setting.EpayIdPrevious, operation_setting.EpayKeyPrevious = originalPreviousID, originalPreviousKey
		operation_setting.EpayPreviousCredentialGeneration = originalPreviousGeneration
		operation_setting.EpayPreviousValidBefore, operation_setting.EpayPreviousExpiresAt = originalValidBefore, originalExpiresAt
	})
	operation_setting.PayAddress = "https://pay.example.com"
	operation_setting.CustomCallbackAddress = "https://api.example.com"
	operation_setting.EpayCurrency = "CNY"
	operation_setting.PayMethods = []map[string]string{{"name": "Alipay", "type": "alipay", "provider": model.PaymentProviderEpay}}
	operation_setting.EpayId, operation_setting.EpayKey = "merchant_new", "epay_new_secret"
	operation_setting.EpayCredentialGeneration = 2
	operation_setting.EpayIdPrevious, operation_setting.EpayKeyPrevious = "merchant_old", "epay_old_secret"
	operation_setting.EpayPreviousCredentialGeneration = 1
	operation_setting.EpayPreviousValidBefore = time.Now().Unix()
	operation_setting.EpayPreviousExpiresAt = time.Now().Add(time.Hour).Unix()

	order := &model.PaymentOrder{
		TradeNo: "PO_EPAY_RETRY_PREVIOUS", OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY",
		ExpectedAmountMinor: 100, ProviderCredentialGeneration: 1,
		CreatedAt: operation_setting.EpayPreviousValidBefore - 1,
	}
	start, err := (&epayPaymentProvider{}).Create(t.Context(), order)
	require.NoError(t, err)
	assert.Equal(t, "merchant_old", start.Fields["pid"])
	previousSignedFields := make(map[string]string, len(start.Fields))
	currentSignedFields := make(map[string]string, len(start.Fields))
	for key, value := range start.Fields {
		previousSignedFields[key] = value
		currentSignedFields[key] = value
	}
	assert.Equal(t, start.Fields["sign"], epay.GenerateParams(previousSignedFields, operation_setting.EpayKeyPrevious)["sign"])
	assert.NotEqual(t, start.Fields["sign"], epay.GenerateParams(currentSignedFields, operation_setting.EpayKey)["sign"])
}

func TestEpayProviderOrderIdentityIsNamespacedByCredentialGeneration(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOrder{}, &model.TopUp{}, &model.SubscriptionOrder{}))
	originalID, originalKey := operation_setting.EpayId, operation_setting.EpayKey
	originalGeneration := operation_setting.EpayCredentialGeneration
	originalPreviousID, originalPreviousKey := operation_setting.EpayIdPrevious, operation_setting.EpayKeyPrevious
	originalPreviousGeneration := operation_setting.EpayPreviousCredentialGeneration
	originalValidBefore, originalExpiresAt := operation_setting.EpayPreviousValidBefore, operation_setting.EpayPreviousExpiresAt
	t.Cleanup(func() {
		operation_setting.EpayId, operation_setting.EpayKey = originalID, originalKey
		operation_setting.EpayCredentialGeneration = originalGeneration
		operation_setting.EpayIdPrevious, operation_setting.EpayKeyPrevious = originalPreviousID, originalPreviousKey
		operation_setting.EpayPreviousCredentialGeneration = originalPreviousGeneration
		operation_setting.EpayPreviousValidBefore, operation_setting.EpayPreviousExpiresAt = originalValidBefore, originalExpiresAt
	})
	operation_setting.EpayId, operation_setting.EpayKey = "merchant_new", "epay_new_secret"
	operation_setting.EpayCredentialGeneration = 2
	operation_setting.EpayIdPrevious, operation_setting.EpayKeyPrevious = "merchant_old", "epay_old_secret"
	operation_setting.EpayPreviousCredentialGeneration = 1
	operation_setting.EpayPreviousValidBefore = time.Now().Unix()
	operation_setting.EpayPreviousExpiresAt = time.Now().Add(time.Hour).Unix()

	now := time.Now().Unix()
	orders := []*model.PaymentOrder{
		{TradeNo: "PO_EPAY_NAMESPACE_OLD", UserID: 998813, OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderEpay,
			PaymentMethod: "alipay", RequestID: "epay_namespace_old", ProviderCredentialGeneration: 1,
			ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 1,
			Status: model.PaymentOrderStatusPending, CreatedAt: now - 1, UpdatedAt: now - 1, Version: 1},
		{TradeNo: "PO_EPAY_NAMESPACE_CURRENT", UserID: 998813, OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderEpay,
			PaymentMethod: "alipay", RequestID: "epay_namespace_current", ProviderCredentialGeneration: 2,
			ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 1,
			Status: model.PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1},
	}
	require.NoError(t, model.DB.Create(orders).Error)
	t.Cleanup(func() {
		model.DB.Where("trade_no IN ?", []string{orders[0].TradeNo, orders[1].TradeNo}).Delete(&model.PaymentOrder{})
	})

	provider, err := GetPaymentProvider(model.PaymentProviderEpay)
	require.NoError(t, err)
	makeRequest := func(order *model.PaymentOrder, merchantID, secret string) *http.Request {
		params := map[string]string{
			"pid": merchantID, "trade_no": "shared_gateway_trade", "out_trade_no": order.TradeNo,
			"type": "alipay", "name": "AI API top-up", "money": "1.00", "trade_status": epay.StatusTradeSuccess,
		}
		epay.GenerateParams(params, secret)
		form := url.Values{}
		for key, value := range params {
			form.Set(key, value)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/payment/epay/notify", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return request
	}
	oldEvent, err := provider.VerifyWebhook(makeRequest(orders[0], operation_setting.EpayIdPrevious, operation_setting.EpayKeyPrevious))
	require.NoError(t, err)
	currentEvent, err := provider.VerifyWebhook(makeRequest(orders[1], operation_setting.EpayId, operation_setting.EpayKey))
	require.NoError(t, err)
	assert.Equal(t, "epay:g1:shared_gateway_trade", oldEvent.ProviderOrderKey)
	assert.Equal(t, "epay:g2:shared_gateway_trade", currentEvent.ProviderOrderKey)
	assert.NotEqual(t, oldEvent.ProviderOrderKey, currentEvent.ProviderOrderKey)
}

func TestEpayNonSuccessCallbackDoesNotPrematurelyFailOrder(t *testing.T) {
	originalID := operation_setting.EpayId
	originalKey := operation_setting.EpayKey
	originalCurrency := operation_setting.EpayCurrency
	t.Cleanup(func() {
		operation_setting.EpayId = originalID
		operation_setting.EpayKey = originalKey
		operation_setting.EpayCurrency = originalCurrency
	})
	operation_setting.EpayId = "merchant_123"
	operation_setting.EpayKey = "epay_test_secret"
	operation_setting.EpayCurrency = "CNY"
	params := map[string]string{
		"pid": operation_setting.EpayId, "trade_no": "gateway_pending", "out_trade_no": "PO_PENDING",
		"type": "alipay", "money": "73.00", "trade_status": "WAIT_BUYER_PAY",
	}
	epay.GenerateParams(params, operation_setting.EpayKey)
	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/payment/epay/notify", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	provider, err := GetPaymentProvider(model.PaymentProviderEpay)
	require.NoError(t, err)
	event, err := provider.VerifyWebhook(request)
	require.NoError(t, err)
	assert.False(t, event.Paid)
	assert.False(t, event.Failed)
	assert.False(t, event.PermanentFailure)
}

func TestProviderCreateErrorClassificationPreservesAmbiguousState(t *testing.T) {
	assert.ErrorIs(t, xorPayCreateError("fee_error"), ErrPaymentStateUnknown)
	assert.NotErrorIs(t, xorPayCreateError("sign_error"), ErrPaymentStateUnknown)

	transient := normalizeStripeCreateError(errors.New("connection reset"))
	assert.ErrorIs(t, transient, ErrPaymentStateUnknown)
	deterministic := normalizeStripeCreateError(&stripe.Error{
		Type: stripe.ErrorTypeInvalidRequest, HTTPStatusCode: http.StatusBadRequest,
	})
	assert.NotErrorIs(t, deterministic, ErrPaymentStateUnknown)
	assert.EqualError(t, deterministic, "stripe rejected the checkout configuration")
}

func TestStartPaymentNeverRecreatesAnExpiredAmbiguousOrder(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}, &model.PaymentUserGuard{}, &model.PaymentOrder{}, &model.TopUp{}))
	now := time.Now().Unix()
	order := &model.PaymentOrder{
		TradeNo: "PO_EXPIRED_AMBIGUOUS_START", UserID: 998801,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "expired_ambiguous_start",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusProcessing, ExpiresAt: now - 1,
		CreatedAt: now - 600, UpdatedAt: now - 600, Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)
	require.NoError(t, model.DB.Create(&model.TopUp{
		PaymentOrderId: &order.ID, UserId: order.UserID, Amount: 1, Money: 1,
		TradeNo: order.TradeNo, PaymentMethod: order.PaymentMethod, PaymentProvider: order.Provider,
		CreateTime: now - 600, Status: common.TopUpStatusPending,
	}).Error)
	t.Cleanup(func() {
		model.DB.Where("payment_order_id = ?", order.ID).Delete(&model.TopUp{})
		model.DB.Delete(&model.PaymentOrder{}, order.ID)
	})

	_, err := StartPayment(t.Context(), order.UserID, PaymentStartRequest{
		QuoteID: "quote-no-longer-needed", RequestID: order.RequestID,
	})
	require.EqualError(t, err, "payment order has expired")

	stored, err := model.GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	assert.Equal(t, model.PaymentOrderStatusExpired, stored.Status)
}

func TestXorPayWebhookUsesOnlySignedFields(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOrder{}))
	originalSecret := setting.XorPayAppSecret
	originalAid := setting.XorPayAid
	originalCurrency := setting.XorPayCurrency
	t.Cleanup(func() {
		setting.XorPayAppSecret = originalSecret
		setting.XorPayAid = originalAid
		setting.XorPayCurrency = originalCurrency
	})
	setting.XorPayAid = "xorpay_test_aid"
	setting.XorPayAppSecret = "xorpay_test_secret"
	setting.XorPayCurrency = "CNY"

	aoid := "AOID_123"
	tradeNo := "PO_XORPAY_123"
	price := "36.50"
	payTime := "2026-07-18 12:00:00"
	form := url.Values{
		"aoid":      {aoid},
		"order_id":  {tradeNo},
		"pay_price": {price},
		"pay_time":  {payTime},
		"more":      {`{"order_uid":"attacker-controlled"}`},
		"sign":      {xorPayMD5(aoid + tradeNo + price + payTime + setting.XorPayAppSecret)},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/xorpay/notify", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	provider, err := GetPaymentProvider(model.PaymentProviderXorPay)
	require.NoError(t, err)
	event, err := provider.VerifyWebhook(request)
	require.NoError(t, err)
	assert.True(t, event.Paid)
	assert.Equal(t, tradeNo, event.TradeNo)
	assert.Equal(t, "xorpay:"+aoid, event.ProviderOrderKey)
	assert.EqualValues(t, 3650, event.PaidAmountMinor)
	assert.NotContains(t, event.NormalizedPayload, "attacker-controlled")

	invalidAOID := "invalid/aoid"
	form.Set("aoid", invalidAOID)
	form.Set("sign", xorPayMD5(invalidAOID+tradeNo+price+payTime+setting.XorPayAppSecret))
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/xorpay/notify", strings.NewReader(form.Encode()))
	invalidRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = provider.VerifyWebhook(invalidRequest)
	require.EqualError(t, err, "xorpay callback contains invalid identifiers")
}

func TestXorPayPreviousCredentialOnlyVerifiesItsBoundOrderGeneration(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOrder{}, &model.TopUp{}, &model.SubscriptionOrder{}))
	originalAid, originalSecret := setting.XorPayAid, setting.XorPayAppSecret
	originalGeneration := setting.XorPayCredentialGeneration
	originalPreviousAid, originalPreviousSecret := setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious
	originalPreviousGeneration := setting.XorPayPreviousCredentialGeneration
	originalValidBefore, originalExpiresAt := setting.XorPayPreviousValidBefore, setting.XorPayPreviousExpiresAt
	t.Cleanup(func() {
		setting.XorPayAid, setting.XorPayAppSecret = originalAid, originalSecret
		setting.XorPayCredentialGeneration = originalGeneration
		setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious = originalPreviousAid, originalPreviousSecret
		setting.XorPayPreviousCredentialGeneration = originalPreviousGeneration
		setting.XorPayPreviousValidBefore, setting.XorPayPreviousExpiresAt = originalValidBefore, originalExpiresAt
	})
	setting.XorPayAid, setting.XorPayAppSecret = "aid_new", "xorpay_new_secret"
	setting.XorPayCredentialGeneration = 2
	setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious = "aid_old", "xorpay_old_secret"
	setting.XorPayPreviousCredentialGeneration = 1
	setting.XorPayPreviousValidBefore = time.Now().Unix()
	setting.XorPayPreviousExpiresAt = time.Now().Add(time.Hour).Unix()

	oldOrder := &model.PaymentOrder{
		TradeNo: "PO_XORPAY_PREVIOUS_GENERATION", UserID: 998812, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderXorPay, PaymentMethod: model.PaymentMethodXorPayNative,
		RequestID: "xorpay_previous_generation", ProviderCredentialGeneration: 1,
		ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusPending, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), Version: 1,
	}
	newOrder := *oldOrder
	newOrder.ID = 0
	newOrder.TradeNo = "PO_XORPAY_CURRENT_GENERATION"
	newOrder.RequestID = "xorpay_current_generation"
	newOrder.ProviderCredentialGeneration = 2
	legacyOrder := *oldOrder
	legacyOrder.ID = 0
	legacyOrder.TradeNo = "PO_XORPAY_LEGACY_PREVIOUS_GENERATION"
	legacyOrder.RequestID = "xorpay_legacy_previous_generation"
	legacyOrder.ProviderCredentialGeneration = 0
	legacyOrder.CreatedAt = setting.XorPayPreviousValidBefore - 1
	legacyOrder.UpdatedAt = legacyOrder.CreatedAt
	require.NoError(t, model.DB.Create(oldOrder).Error)
	require.NoError(t, model.DB.Create(&newOrder).Error)
	require.NoError(t, model.DB.Create(&legacyOrder).Error)
	t.Cleanup(func() {
		model.DB.Where("trade_no IN ?", []string{oldOrder.TradeNo, newOrder.TradeNo, legacyOrder.TradeNo}).Delete(&model.PaymentOrder{})
	})

	provider, err := GetPaymentProvider(model.PaymentProviderXorPay)
	require.NoError(t, err)
	makeRequest := func(tradeNo string) *http.Request {
		aoid, price, payTime := "AOID_PREVIOUS", "1.00", "2026-07-18 12:00:00"
		form := url.Values{
			"aoid": {aoid}, "order_id": {tradeNo}, "pay_price": {price}, "pay_time": {payTime},
			"sign": {xorPayMD5(aoid + tradeNo + price + payTime + setting.XorPayAppSecretPrevious)},
		}
		request := httptest.NewRequest(http.MethodPost, "/api/xorpay/notify", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return request
	}
	_, err = provider.VerifyWebhook(makeRequest(oldOrder.TradeNo))
	require.NoError(t, err)
	legacyEvent, err := provider.VerifyWebhook(makeRequest(legacyOrder.TradeNo))
	require.NoError(t, err)
	assert.Equal(t, setting.XorPayPreviousCredentialGeneration, legacyEvent.ProviderCredentialGeneration)
	_, err = provider.VerifyWebhook(makeRequest(newOrder.TradeNo))
	assert.Error(t, err)
}

func TestXorPayStartRetryUsesTheOrderBoundPreviousCredential(t *testing.T) {
	originalAid, originalSecret := setting.XorPayAid, setting.XorPayAppSecret
	originalGeneration := setting.XorPayCredentialGeneration
	originalPreviousAid, originalPreviousSecret := setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious
	originalPreviousGeneration := setting.XorPayPreviousCredentialGeneration
	originalValidBefore, originalExpiresAt := setting.XorPayPreviousValidBefore, setting.XorPayPreviousExpiresAt
	originalCurrency := setting.XorPayCurrency
	originalMethods := setting.XorPayEnabledMethods
	originalCallback := operation_setting.CustomCallbackAddress
	t.Cleanup(func() {
		setting.XorPayAid, setting.XorPayAppSecret = originalAid, originalSecret
		setting.XorPayCredentialGeneration = originalGeneration
		setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious = originalPreviousAid, originalPreviousSecret
		setting.XorPayPreviousCredentialGeneration = originalPreviousGeneration
		setting.XorPayPreviousValidBefore, setting.XorPayPreviousExpiresAt = originalValidBefore, originalExpiresAt
		setting.XorPayCurrency = originalCurrency
		setting.XorPayEnabledMethods = originalMethods
		operation_setting.CustomCallbackAddress = originalCallback
	})
	setting.XorPayAid, setting.XorPayAppSecret = "aid_new", "xorpay_new_secret"
	setting.XorPayCredentialGeneration = 2
	setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious = "aid_old", "xorpay_old_secret"
	setting.XorPayPreviousCredentialGeneration = 1
	setting.XorPayPreviousValidBefore = time.Now().Unix()
	setting.XorPayPreviousExpiresAt = time.Now().Add(time.Hour).Unix()
	setting.XorPayCurrency = "CNY"
	setting.XorPayEnabledMethods = []string{setting.XorPayMethodNative}
	operation_setting.CustomCallbackAddress = "https://api.example.com"

	provider := &xorPayProvider{client: &http.Client{Transport: paymentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "https://xorpay.com/api/pay/aid_old", request.URL.String())
		require.NoError(t, request.ParseForm())
		name := "AI API top-up"
		expectedSign := xorPayMD5(name + setting.XorPayMethodNative + "1.00" + "PO_XORPAY_RETRY_PREVIOUS" +
			"https://api.example.com/api/xorpay/notify" + setting.XorPayAppSecretPrevious)
		assert.Equal(t, expectedSign, request.Form.Get("sign"))
		assert.NotEqual(t, xorPayMD5(name+setting.XorPayMethodNative+"1.00"+"PO_XORPAY_RETRY_PREVIOUS"+
			"https://api.example.com/api/xorpay/notify"+setting.XorPayAppSecret), request.Form.Get("sign"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"status":"ok","aoid":"AOID_RETRY_PREVIOUS","expires_in":600,"info":{"qr":"weixin://wxpay/example"}}`)),
			Request:    request,
		}, nil
	})}}
	start, err := provider.Create(t.Context(), &model.PaymentOrder{
		TradeNo: "PO_XORPAY_RETRY_PREVIOUS", OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderXorPay, PaymentMethod: model.PaymentMethodXorPayNative,
		Currency: "CNY", ExpectedAmountMinor: 100, ProviderCredentialGeneration: 1,
		CreatedAt: setting.XorPayPreviousValidBefore - 1,
	})
	require.NoError(t, err)
	assert.Equal(t, "xorpay:AOID_RETRY_PREVIOUS", start.ProviderOrderKey)
}

func TestXorPayCreateRejectsInvalidProviderOrderIdentity(t *testing.T) {
	originalAid, originalSecret := setting.XorPayAid, setting.XorPayAppSecret
	originalGeneration := setting.XorPayCredentialGeneration
	originalCurrency := setting.XorPayCurrency
	originalMethods := setting.XorPayEnabledMethods
	originalCallback := operation_setting.CustomCallbackAddress
	t.Cleanup(func() {
		setting.XorPayAid, setting.XorPayAppSecret = originalAid, originalSecret
		setting.XorPayCredentialGeneration = originalGeneration
		setting.XorPayCurrency = originalCurrency
		setting.XorPayEnabledMethods = originalMethods
		operation_setting.CustomCallbackAddress = originalCallback
	})
	setting.XorPayAid, setting.XorPayAppSecret = "aid_current", "xorpay_current_secret"
	setting.XorPayCredentialGeneration = 8
	setting.XorPayCurrency = "CNY"
	setting.XorPayEnabledMethods = []string{setting.XorPayMethodNative}
	operation_setting.CustomCallbackAddress = "https://api.example.com"

	provider := &xorPayProvider{client: &http.Client{Transport: paymentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"status":"ok","aoid":"../../unexpected","expires_in":600,"info":{"qr":"weixin://wxpay/example"}}`)),
			Request:    request,
		}, nil
	})}}
	_, err := provider.Create(t.Context(), &model.PaymentOrder{
		TradeNo: "PO_XORPAY_INVALID_AOID", OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderXorPay, PaymentMethod: model.PaymentMethodXorPayNative,
		Currency: "CNY", ExpectedAmountMinor: 100, ProviderCredentialGeneration: 8,
	})
	require.ErrorIs(t, err, ErrPaymentStateUnknown)
	assert.Contains(t, err.Error(), "invalid aoid")
}

func TestXorPayQueryUsesAOIDWithoutRetainingRotatedCredentials(t *testing.T) {
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	keys := []string{
		model.PaymentConfigurationVersionOptionKey,
		"XorPayCredentialGeneration", "XorPayPreviousCredentialGeneration",
		"XorPayPreviousValidBefore", "XorPayPreviousExpiresAt",
	}
	require.NoError(t, model.DB.Where("`key` IN ?", keys).Delete(&model.Option{}).Error)
	require.NoError(t, model.DB.Create([]model.Option{
		{Key: model.PaymentConfigurationVersionOptionKey, Value: "1"},
		{Key: "XorPayCredentialGeneration", Value: "23"},
		{Key: "XorPayPreviousCredentialGeneration", Value: "0"},
		{Key: "XorPayPreviousValidBefore", Value: "0"},
		{Key: "XorPayPreviousExpiresAt", Value: "0"},
	}).Error)
	t.Cleanup(func() { _ = model.DB.Where("`key` IN ?", keys).Delete(&model.Option{}).Error })
	originalGeneration := setting.XorPayCredentialGeneration
	originalCurrency := setting.XorPayCurrency
	setting.XorPayCredentialGeneration = 23
	setting.XorPayCurrency = "CNY"
	t.Cleanup(func() {
		setting.XorPayCredentialGeneration = originalGeneration
		setting.XorPayCurrency = originalCurrency
	})

	provider := &xorPayProvider{client: &http.Client{Transport: paymentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "https://xorpay.com/api/query/AOID_ROTATED", request.URL.String())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"status":"payed"}`)),
			Request:    request,
		}, nil
	})}}
	providerOrderKey := "xorpay:AOID_ROTATED"
	event, err := provider.Query(t.Context(), &model.PaymentOrder{
		TradeNo: "PO_XORPAY_ROTATED_QUERY", Provider: model.PaymentProviderXorPay,
		ProviderOrderKey: &providerOrderKey, ExpectedAmountMinor: 500, Currency: "CNY",
		PaymentMethod: model.PaymentMethodXorPayNative, ProviderCredentialGeneration: 23,
		CreatedAt: time.Now().Unix(),
	})
	require.NoError(t, err)
	assert.True(t, event.Paid)
	assert.Equal(t, providerOrderKey, event.ProviderOrderKey)
}

func TestXorPayLegacyQueryBindsTheCredentialThatFoundTheOrder(t *testing.T) {
	originalAid, originalSecret := setting.XorPayAid, setting.XorPayAppSecret
	originalGeneration := setting.XorPayCredentialGeneration
	originalPreviousAid, originalPreviousSecret := setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious
	originalPreviousGeneration := setting.XorPayPreviousCredentialGeneration
	originalValidBefore, originalExpiresAt := setting.XorPayPreviousValidBefore, setting.XorPayPreviousExpiresAt
	originalCurrency := setting.XorPayCurrency
	t.Cleanup(func() {
		setting.XorPayAid, setting.XorPayAppSecret = originalAid, originalSecret
		setting.XorPayCredentialGeneration = originalGeneration
		setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious = originalPreviousAid, originalPreviousSecret
		setting.XorPayPreviousCredentialGeneration = originalPreviousGeneration
		setting.XorPayPreviousValidBefore, setting.XorPayPreviousExpiresAt = originalValidBefore, originalExpiresAt
		setting.XorPayCurrency = originalCurrency
	})

	now := time.Now().Unix()
	setting.XorPayAid, setting.XorPayAppSecret = "aid_current", "xorpay_current_secret"
	setting.XorPayCredentialGeneration = 8
	setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious = "aid_previous", "xorpay_previous_secret"
	setting.XorPayPreviousCredentialGeneration = 7
	setting.XorPayPreviousValidBefore = now
	setting.XorPayPreviousExpiresAt = now + int64(time.Hour/time.Second)
	setting.XorPayCurrency = "CNY"

	requests := 0
	provider := &xorPayProvider{client: &http.Client{Transport: paymentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		status := `{"status":"not_exist"}`
		if strings.Contains(request.URL.Path, "/aid_previous") {
			status = `{"status":"payed"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(status)),
			Request:    request,
		}, nil
	})}}
	event, err := provider.Query(t.Context(), &model.PaymentOrder{
		TradeNo: "PO_XORPAY_LEGACY_QUERY", Provider: model.PaymentProviderXorPay,
		ExpectedAmountMinor: 500, Currency: "CNY", PaymentMethod: model.PaymentMethodXorPayNative,
		ProviderCredentialGeneration: 0, CreatedAt: now - 1,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, requests)
	assert.True(t, event.Paid)
	assert.Equal(t, setting.XorPayPreviousCredentialGeneration, event.ProviderCredentialGeneration)
}

func TestXorPayLegacyQueryDoesNotCrossCredentialGenerationsAfterAmbiguousFailure(t *testing.T) {
	originalAid, originalSecret := setting.XorPayAid, setting.XorPayAppSecret
	originalGeneration := setting.XorPayCredentialGeneration
	originalPreviousAid, originalPreviousSecret := setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious
	originalPreviousGeneration := setting.XorPayPreviousCredentialGeneration
	originalValidBefore, originalExpiresAt := setting.XorPayPreviousValidBefore, setting.XorPayPreviousExpiresAt
	originalCurrency := setting.XorPayCurrency
	t.Cleanup(func() {
		setting.XorPayAid, setting.XorPayAppSecret = originalAid, originalSecret
		setting.XorPayCredentialGeneration = originalGeneration
		setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious = originalPreviousAid, originalPreviousSecret
		setting.XorPayPreviousCredentialGeneration = originalPreviousGeneration
		setting.XorPayPreviousValidBefore, setting.XorPayPreviousExpiresAt = originalValidBefore, originalExpiresAt
		setting.XorPayCurrency = originalCurrency
	})

	now := time.Now().Unix()
	setting.XorPayAid, setting.XorPayAppSecret = "aid_current", "xorpay_current_secret"
	setting.XorPayCredentialGeneration = 8
	setting.XorPayAidPrevious, setting.XorPayAppSecretPrevious = "aid_previous", "xorpay_previous_secret"
	setting.XorPayPreviousCredentialGeneration = 7
	setting.XorPayPreviousValidBefore = now
	setting.XorPayPreviousExpiresAt = now + int64(time.Hour/time.Second)
	setting.XorPayCurrency = "CNY"

	requests := 0
	provider := &xorPayProvider{client: &http.Client{Transport: paymentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if strings.Contains(request.URL.Path, "/aid_previous") {
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"status":"payed"}`)), Request: request,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusBadGateway, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"status":"temporary_failure"}`)), Request: request,
		}, nil
	})}}
	_, err := provider.Query(t.Context(), &model.PaymentOrder{
		TradeNo: "PO_XORPAY_AMBIGUOUS_QUERY", Provider: model.PaymentProviderXorPay,
		ExpectedAmountMinor: 500, Currency: "CNY", PaymentMethod: model.PaymentMethodXorPayNative,
		ProviderCredentialGeneration: 0, CreatedAt: now - 1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 502")
	assert.Equal(t, 1, requests)
}

func TestXorPayRejectsInvalidProviderConfigurationBeforeNetworkAccess(t *testing.T) {
	originalSecret := setting.XorPayAppSecret
	originalAid := setting.XorPayAid
	originalCurrency := setting.XorPayCurrency
	t.Cleanup(func() {
		setting.XorPayAppSecret = originalSecret
		setting.XorPayAid = originalAid
		setting.XorPayCurrency = originalCurrency
	})
	setting.XorPayAid = "invalid/aid"
	setting.XorPayAppSecret = "xorpay_test_secret"
	setting.XorPayCurrency = "CNY"

	provider := &xorPayProvider{client: &http.Client{}}
	_, err := provider.Query(t.Context(), &model.PaymentOrder{TradeNo: "PO_XORPAY_CONFIG", Currency: "CNY"})
	require.EqualError(t, err, "xorpay is not configured")

	request := httptest.NewRequest(http.MethodPost, "/api/xorpay/notify", nil)
	_, err = provider.VerifyWebhook(request)
	require.EqualError(t, err, "xorpay webhook is not configured")
}

func TestXorPayQRValidationUsesAProviderSpecificAllowlist(t *testing.T) {
	require.NoError(t, validateXorPayQR(setting.XorPayMethodNative, "weixin://wxpay/example"))
	require.NoError(t, validateXorPayQR(setting.XorPayMethodAlipay, "https://qr.alipay.com/example"))

	for _, value := range []string{
		"weixin://example",
		"https://xorpay.com/qr/example",
		"https://qr.alipay.com.evil.example/",
		"https://user:pass@qr.alipay.com/example",
		"https://qr.alipay.com:8443/example",
	} {
		assert.Error(t, validateXorPayQR(setting.XorPayMethodAlipay, value), value)
	}
}

func TestStripeWebhookVerificationSupportsPreviousSecretAndNormalizesCheckout(t *testing.T) {
	originalAPISecret := setting.StripeApiSecret
	originalCurrent := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalPreviousExpiry := setting.StripeWebhookSecretPreviousExpiresAt
	originalAccount := setting.StripeAccountId
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	originalGeneration := setting.StripeWebhookCredentialGeneration
	originalPreviousGeneration := setting.StripeWebhookPreviousCredentialGeneration
	originalPreviousValidBefore := setting.StripeWebhookPreviousValidBefore
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		setting.StripeWebhookSecret = originalCurrent
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeWebhookSecretPreviousExpiresAt = originalPreviousExpiry
		setting.StripeAccountId = originalAccount
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
		setting.StripeWebhookCredentialGeneration = originalGeneration
		setting.StripeWebhookPreviousCredentialGeneration = originalPreviousGeneration
		setting.StripeWebhookPreviousValidBefore = originalPreviousValidBefore
	})
	setting.StripeApiSecret = "sk_test_123"
	setting.StripeWebhookSecret = "whsec_current_secret"
	setting.StripeWebhookSecretPrevious = "whsec_previous_secret"
	setting.StripeWebhookSecretPreviousExpiresAt = time.Now().Add(time.Hour).Unix()
	setting.StripeAccountId = ""
	setting.StripeWebhookCredentialLivemode = "test"
	setting.StripeWebhookCredentialGeneration = 2
	setting.StripeWebhookPreviousCredentialGeneration = 1
	setting.StripeWebhookPreviousValidBefore = time.Now().Unix()

	payload := []byte(fmt.Sprintf(`{
		"id":"evt_checkout_123",
		"object":"event",
		"api_version":%q,
		"created":1721304000,
		"livemode":false,
		"pending_webhooks":1,
		"type":"checkout.session.completed",
		"data":{"object":{
			"id":"cs_test_123",
			"object":"checkout.session",
			"client_reference_id":"PO_STRIPE_123",
			"amount_total":999,
			"currency":"usd",
			"payment_status":"paid",
			"status":"complete",
			"payment_intent":"pi_123",
			"customer":"cus_123",
			"metadata":{"trade_no":"PO_STRIPE_123","order_kind":"topup"}
		}}
	}`, stripe.APIVersion))
	signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{
		Payload: payload,
		Secret:  setting.StripeWebhookSecretPrevious,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
	request.Header.Set("Stripe-Signature", signed.Header)

	provider, err := GetPaymentProvider(model.PaymentProviderStripe)
	require.NoError(t, err)
	event, err := provider.VerifyWebhook(request)
	require.NoError(t, err)
	assert.True(t, event.Paid)
	assert.Equal(t, "PO_STRIPE_123", event.TradeNo)
	assert.Equal(t, "stripe:cs_test_123", event.ProviderOrderKey)
	assert.Equal(t, "stripe:pi_123", event.ProviderPaymentKey)
	assert.Equal(t, "cus_123", event.CustomerID)
	assert.EqualValues(t, 999, event.PaidAmountMinor)
	assert.Equal(t, "USD", event.Currency)
	assert.Equal(t, int64(1), event.ProviderCredentialGeneration)
	require.NotNil(t, event.ProviderLivemode)
	assert.False(t, *event.ProviderLivemode)
}

func TestStripeCheckoutConfigurationProbeBindsPriceCurrencyPermissionsAndAccount(t *testing.T) {
	t.Setenv(setting.StripeTestModeEnabledEnv, "true")
	denyPrice := false
	denyCheckout := false
	var checkoutProbeCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "acct_connectedverified", request.Header.Get("Stripe-Account"))
		switch {
		case request.URL.Path == "/v1/prices/price_verified":
			assert.Equal(t, http.MethodGet, request.Method)
			if denyPrice {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"permission denied"}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"price_verified","object":"price","active":true,"currency":"usd","product":"prod_verified"}`))
		case strings.HasPrefix(request.URL.Path, "/v1/checkout/sessions/cs_test_permission_probe_") && strings.HasSuffix(request.URL.Path, "/expire"):
			assert.Equal(t, http.MethodPost, request.Method)
			username, password, ok := request.BasicAuth()
			assert.True(t, ok)
			assert.NotEmpty(t, username)
			assert.Empty(t, password)
			assert.Empty(t, request.Header.Get("Idempotency-Key"))
			checkoutProbeCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if denyCheckout {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"permission denied"}}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing","param":"id","message":"No such checkout.session"}}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	originalBackend := stripe.GetBackend(stripe.APIBackend)
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
		URL: stripe.String(server.URL), HTTPClient: server.Client(), MaxNetworkRetries: stripe.Int64(0),
	}))
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, originalBackend) })
	originalCapabilityEndpoint := stripeCheckoutCapabilityEndpoint
	originalVerificationClient := stripeConfigurationVerificationHTTPClient
	stripeCheckoutCapabilityEndpoint = server.URL + "/v1/checkout/sessions"
	stripeConfigurationVerificationHTTPClient = server.Client()
	t.Cleanup(func() {
		stripeCheckoutCapabilityEndpoint = originalCapabilityEndpoint
		stripeConfigurationVerificationHTTPClient = originalVerificationClient
	})

	fingerprint, err := VerifyStripeCheckoutConfiguration(
		t.Context(), "sk_test_verified", "acct_platformverified", "acct_connectedverified",
		"price_verified", "USD", "test",
	)
	require.NoError(t, err)
	assert.Len(t, fingerprint, 64)
	assert.Equal(t, fingerprint, StripeCheckoutConfigurationFingerprint(
		"sk_test_verified", "acct_platformverified", "acct_connectedverified", "price_verified", "USD", "test",
	))
	assert.EqualValues(t, 1, checkoutProbeCount.Load())

	secondFingerprint, err := VerifyStripeCheckoutConfiguration(
		t.Context(), "sk_test_verified", "acct_platformverified", "acct_connectedverified",
		"price_verified", "USD", "test",
	)
	require.NoError(t, err)
	assert.Equal(t, fingerprint, secondFingerprint)
	assert.EqualValues(t, 2, checkoutProbeCount.Load())

	_, err = VerifyStripeCheckoutConfiguration(
		t.Context(), "sk_test_verified", "acct_platformverified", "acct_connectedverified",
		"price_verified", "EUR", "test",
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "currency mismatch")

	denyCheckout = true
	_, err = VerifyStripeCheckoutConfiguration(
		t.Context(), "rk_test_restricted", "acct_platformverified", "acct_connectedverified",
		"price_verified", "USD", "test",
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot create Checkout Sessions")

	denyCheckout = false
	denyPrice = true
	_, err = VerifyStripeCheckoutConfiguration(
		t.Context(), "rk_test_price_denied", "acct_platformverified", "acct_connectedverified",
		"price_verified", "USD", "test",
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot access")
}

func TestStripeWebhookEmergencyRevocationRejectsOldAndEmptySigningSecrets(t *testing.T) {
	originalCurrent := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalPreviousExpiry := setting.StripeWebhookSecretPreviousExpiresAt
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	t.Cleanup(func() {
		setting.StripeWebhookSecret = originalCurrent
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeWebhookSecretPreviousExpiresAt = originalPreviousExpiry
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
	})
	setting.StripeWebhookSecret = "whsec_emergency_replacement"
	setting.StripeWebhookSecretPrevious = ""
	setting.StripeWebhookSecretPreviousExpiresAt = 0
	setting.StripeWebhookCredentialLivemode = "test"

	payload := []byte(`{"id":"evt_revoked_secret","object":"event","livemode":false,"type":"unknown.event","data":{"object":{}}}`)
	provider, err := GetPaymentProvider(model.PaymentProviderStripe)
	require.NoError(t, err)
	verifyWithSecret := func(secret string) error {
		signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{Payload: payload, Secret: secret})
		request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
		request.Header.Set("Stripe-Signature", signed.Header)
		_, verifyErr := provider.VerifyWebhook(request)
		return verifyErr
	}

	assert.Error(t, verifyWithSecret("whsec_revoked_old_secret"))
	setting.StripeWebhookCredentialLivemode = "live"
	assert.Error(t, verifyWithSecret(setting.StripeWebhookSecret))
	setting.StripeWebhookCredentialLivemode = "test"

	setting.StripeWebhookSecret = ""
	setting.StripeWebhookCredentialLivemode = ""
	assert.Error(t, verifyWithSecret(""))
}

func TestStripeWebhookLegacyUnboundModeUsesAPIKeyDomainForUpgradeCompatibility(t *testing.T) {
	originalAPISecret := setting.StripeApiSecret
	originalCredentialMode := setting.StripeCredentialLivemode
	originalCurrent := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalPreviousExpiry := setting.StripeWebhookSecretPreviousExpiresAt
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	originalPriceID := setting.StripePriceId
	originalAccountID := setting.StripeCredentialAccountId
	originalGeneration := setting.StripeWebhookCredentialGeneration
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		setting.StripeCredentialLivemode = originalCredentialMode
		setting.StripeWebhookSecret = originalCurrent
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeWebhookSecretPreviousExpiresAt = originalPreviousExpiry
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
		setting.StripePriceId = originalPriceID
		setting.StripeCredentialAccountId = originalAccountID
		setting.StripeWebhookCredentialGeneration = originalGeneration
	})
	setting.StripeApiSecret = "sk_test_legacy_mode_inference"
	setting.StripeCredentialLivemode = ""
	setting.StripeWebhookSecret = "whsec_legacy_unbound_mode"
	setting.StripeWebhookSecretPrevious = ""
	setting.StripeWebhookSecretPreviousExpiresAt = 0
	setting.StripeWebhookCredentialLivemode = ""
	setting.StripePriceId = "price_legacy_mode"
	setting.StripeCredentialAccountId = "acct_legacy_mode"
	setting.StripeWebhookCredentialGeneration = 2

	payload := []byte(`{"id":"evt_legacy_mode","object":"event","livemode":false,"type":"unknown.event","data":{"object":{}}}`)
	signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{Payload: payload, Secret: setting.StripeWebhookSecret})
	request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
	request.Header.Set("Stripe-Signature", signed.Header)
	provider, err := GetPaymentProvider(model.PaymentProviderStripe)
	require.NoError(t, err)
	event, err := provider.VerifyWebhook(request)
	require.NoError(t, err)
	assert.Equal(t, int64(2), event.ProviderCredentialGeneration)

}

func TestSetStripeAccountAppliesOnlyConfiguredConnectedAccount(t *testing.T) {
	priceParams := &stripe.PriceParams{}
	setStripeAccount(priceParams, " acct_connected ")
	require.NotNil(t, priceParams.StripeAccount)
	assert.Equal(t, "acct_connected", *priceParams.StripeAccount)

	sessionParams := &stripe.CheckoutSessionParams{}
	setStripeAccount(sessionParams, "")
	assert.Nil(t, sessionParams.StripeAccount)
}

func TestStripeWebhookEnforcesPlatformAndConnectedAccountBoundary(t *testing.T) {
	originalAPISecret := setting.StripeApiSecret
	originalWebhookSecret := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalAccount := setting.StripeAccountId
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeAccountId = originalAccount
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
	})
	setting.StripeApiSecret = "sk_test_123"
	setting.StripeWebhookSecret = "whsec_account_boundary"
	setting.StripeWebhookSecretPrevious = ""
	setting.StripeWebhookCredentialLivemode = "test"

	tests := []struct {
		name              string
		configuredAccount string
		eventAccount      string
		wantError         bool
	}{
		{name: "platform accepts platform event"},
		{name: "platform rejects connected event", eventAccount: "acct_connected", wantError: true},
		{name: "connected accepts exact account", configuredAccount: "acct_expected", eventAccount: "acct_expected"},
		{name: "connected rejects platform event", configuredAccount: "acct_expected", wantError: true},
		{name: "connected rejects another account", configuredAccount: "acct_expected", eventAccount: "acct_other", wantError: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setting.StripeAccountId = test.configuredAccount
			accountField := ""
			if test.eventAccount != "" {
				accountField = fmt.Sprintf(`,"account":%q`, test.eventAccount)
			}
			payload := []byte(fmt.Sprintf(`{
				"id":"evt_account_%d","object":"event","api_version":%q,
				"created":1721304000,"livemode":false,"pending_webhooks":1%s,
				"type":"checkout.session.completed","data":{"object":{
					"id":"cs_test_account_%d","object":"checkout.session","client_reference_id":"PO_ACCOUNT_%d",
					"amount_total":999,"currency":"usd","payment_status":"paid","status":"complete",
					"payment_intent":"pi_account_%d","metadata":{"trade_no":"PO_ACCOUNT_%d"}
				}}
			}`, index, stripe.APIVersion, accountField, index, index, index, index))
			signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{
				Payload: payload,
				Secret:  setting.StripeWebhookSecret,
			})
			request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
			request.Header.Set("Stripe-Signature", signed.Header)
			provider, err := GetPaymentProvider(model.PaymentProviderStripe)
			require.NoError(t, err)
			_, err = provider.VerifyWebhook(request)
			if test.wantError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestStripeWebhookRetainsLegacyChargeIdentityAndResolvesWarningClosed(t *testing.T) {
	originalAPISecret := setting.StripeApiSecret
	originalWebhookSecret := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalAccount := setting.StripeAccountId
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeAccountId = originalAccount
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
	})
	setting.StripeApiSecret = "sk_test_123"
	setting.StripeWebhookSecret = "whsec_legacy_charge"
	setting.StripeWebhookSecretPrevious = ""
	setting.StripeAccountId = ""
	setting.StripeWebhookCredentialLivemode = "test"

	provider, err := GetPaymentProvider(model.PaymentProviderStripe)
	require.NoError(t, err)
	verify := func(t *testing.T, payload []byte) *NormalizedPaymentEvent {
		signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{Payload: payload, Secret: setting.StripeWebhookSecret})
		request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
		request.Header.Set("Stripe-Signature", signed.Header)
		event, err := provider.VerifyWebhook(request)
		require.NoError(t, err)
		return event
	}

	refundPayload := []byte(fmt.Sprintf(`{
		"id":"evt_refund_without_pi","object":"event","api_version":%q,"created":1721304001,
		"livemode":false,"type":"charge.refunded","data":{"object":{
			"id":"ch_legacy_123","object":"charge","amount_refunded":500,"currency":"usd",
			"payment_intent":null,"metadata":{"trade_no":"PO_LEGACY_CHARGE"}
		}}
	}`, "2025-02-24.acacia"))
	refund := verify(t, refundPayload)
	assert.True(t, refund.Refunded)
	assert.False(t, refund.ManualReview)
	assert.Equal(t, "stripe:ch_legacy_123", refund.ProviderPaymentKey)
	assert.Equal(t, "PO_LEGACY_CHARGE", refund.TradeNo)

	disputePayload := []byte(fmt.Sprintf(`{
		"id":"evt_warning_closed","object":"event","api_version":%q,"created":1721304002,
		"livemode":false,"type":"charge.dispute.closed","data":{"object":{
			"id":"dp_warning_123","object":"dispute","amount":999,"currency":"usd","status":"warning_closed",
			"charge":"ch_legacy_123","payment_intent":"pi_legacy_123","metadata":{}
		}}
	}`, stripe.APIVersion))
	dispute := verify(t, disputePayload)
	assert.True(t, dispute.DisputeResolved)
	assert.True(t, dispute.DisputeWon)
	assert.Equal(t, "stripe:dp_warning_123", dispute.ProviderResourceKey)
	assert.Equal(t, "warning_closed", dispute.ProviderState)
	assert.EqualValues(t, 1721304002, dispute.ProviderCreatedAt)
}

func TestStripeFinancialWebhookIncompatibleAPIVersionRequiresManualReview(t *testing.T) {
	originalWebhookSecret := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalAccount := setting.StripeAccountId
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	originalGeneration := setting.StripeWebhookCredentialGeneration
	t.Cleanup(func() {
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeAccountId = originalAccount
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
		setting.StripeWebhookCredentialGeneration = originalGeneration
	})
	setting.StripeWebhookSecret = "whsec_api_version_gate"
	setting.StripeWebhookSecretPrevious = ""
	setting.StripeAccountId = ""
	setting.StripeWebhookCredentialLivemode = "test"
	setting.StripeWebhookCredentialGeneration = 1

	provider, err := GetPaymentProvider(model.PaymentProviderStripe)
	require.NoError(t, err)
	verify := func(t *testing.T, payload []byte) *NormalizedPaymentEvent {
		t.Helper()
		signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{
			Payload: payload,
			Secret:  setting.StripeWebhookSecret,
		})
		request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
		request.Header.Set("Stripe-Signature", signed.Header)
		event, verifyErr := provider.VerifyWebhook(request)
		require.NoError(t, verifyErr)
		return event
	}

	refundPayload := []byte(`{
		"id":"evt_refund_basil","object":"event","api_version":"2025-06-30.basil","created":1721304003,
		"livemode":false,"type":"charge.refunded","data":{"object":{
			"id":"ch_basil_123","object":"charge","amount_refunded":500,"currency":"usd",
			"payment_intent":"pi_basil_123","metadata":{"trade_no":"PO_BASIL_REFUND"}
		}}
	}`)
	refund := verify(t, refundPayload)
	assert.True(t, refund.ManualReview)
	assert.False(t, refund.Refunded)
	assert.Zero(t, refund.RefundedAmountMinor)
	assert.Equal(t, "stripe:pi_basil_123", refund.ProviderPaymentKey)
	assert.Equal(t, "PO_BASIL_REFUND", refund.TradeNo)
	assert.Equal(t, "api_version_manual_review", refund.ProviderState)
	assert.Contains(t, refund.NormalizedPayload, "blocked_incompatible_api_version")

	disputePayload := []byte(`{
		"id":"evt_dispute_clover","object":"event","api_version":"2025-09-30.clover","created":1721304004,
		"livemode":false,"type":"charge.dispute.closed","data":{"object":{
			"id":"dp_clover_123","object":"dispute","amount":999,"currency":"usd","status":"lost",
			"charge":"ch_clover_123","payment_intent":"pi_clover_123","metadata":{"trade_no":"PO_CLOVER_DISPUTE"}
		}}
	}`)
	dispute := verify(t, disputePayload)
	assert.True(t, dispute.ManualReview)
	assert.False(t, dispute.Disputed)
	assert.False(t, dispute.DisputeResolved)
	assert.False(t, dispute.DisputeWon)
	assert.Zero(t, dispute.DisputedAmountMinor)
	assert.Equal(t, "stripe:pi_clover_123", dispute.ProviderPaymentKey)
	assert.Equal(t, "stripe:dp_clover_123", dispute.ProviderResourceKey)
	assert.Equal(t, "PO_CLOVER_DISPUTE", dispute.TradeNo)
	assert.Equal(t, "api_version_manual_review", dispute.ProviderState)
	assert.Contains(t, dispute.NormalizedPayload, "blocked_incompatible_api_version")
}

func TestStripeFinancialWebhookAPIVersionCompatibilityIsExplicit(t *testing.T) {
	assert.True(t, stripeFinancialEventAPIVersionCompatible("2025-02-24.acacia"))
	assert.True(t, stripeFinancialEventAPIVersionCompatible(stripe.APIVersion))
	for _, version := range []string{
		"", "2025-02-24", "not-a-date.acacia", "2025-06-30.basil",
		"2025-09-30.clover", "2026-06-24.preview",
	} {
		assert.False(t, stripeFinancialEventAPIVersionCompatible(version), version)
	}
}

func TestStripeWebhookRejectsExpiredSignatureTimestamp(t *testing.T) {
	originalWebhookSecret := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	t.Cleanup(func() {
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
	})
	setting.StripeWebhookSecret = "whsec_timestamp"
	setting.StripeWebhookSecretPrevious = ""
	setting.StripeWebhookCredentialLivemode = "test"
	payload := []byte(`{"id":"evt_old","object":"event","livemode":false,"type":"unknown.event","data":{"object":{}}}`)
	signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{
		Payload: payload, Secret: setting.StripeWebhookSecret, Timestamp: time.Now().Add(-10 * time.Minute),
	})
	request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", strings.NewReader(string(payload)))
	request.Header.Set("Stripe-Signature", signed.Header)
	provider, err := GetPaymentProvider(model.PaymentProviderStripe)
	require.NoError(t, err)
	_, err = provider.VerifyWebhook(request)
	assert.Error(t, err)
}
