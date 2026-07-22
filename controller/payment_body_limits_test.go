package controller

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureRetainedTopUpHandlerTest(t *testing.T) {
	t.Helper()
	db := setupMidjourneyControllerBillingDB(t)
	t.Setenv("PAYMENT_SECRET_KEY", "retained-handler-test-payment-key-0123456789abcdef")
	require.NoError(t, db.AutoMigrate(&model.SystemInstance{}))
	require.NoError(t, db.Exec("DELETE FROM system_instances").Error)
	confirmPaymentComplianceForTest(t)
	originalWaffoEnabled := setting.WaffoEnabled
	originalWaffoSandbox := setting.WaffoSandbox
	originalWaffoAPIKey := setting.WaffoApiKey
	originalWaffoPrivateKey := setting.WaffoPrivateKey
	originalWaffoPublicCert := setting.WaffoPublicCert
	originalWaffoMerchantID := setting.WaffoMerchantId
	originalCallbackAddress := operation_setting.CustomCallbackAddress
	originalPayMethods := operation_setting.PayMethods
	originalMerchantID := setting.WaffoPancakeMerchantID
	originalPrivateKey := setting.WaffoPancakePrivateKey
	originalProductID := setting.WaffoPancakeProductID
	originalStoreID := setting.WaffoPancakeStoreID
	setting.WaffoEnabled = true
	setting.WaffoSandbox = false
	setting.WaffoApiKey = "waffo-test-api-key"
	setting.WaffoPrivateKey = "waffo-test-private-key"
	setting.WaffoPublicCert = "waffo-test-public-cert"
	setting.WaffoMerchantId = "waffo-test-merchant"
	operation_setting.CustomCallbackAddress = "https://payments.example.com"
	setting.WaffoPancakeMerchantID = "MER_test"
	setting.WaffoPancakePrivateKey = "test-private-key"
	setting.WaffoPancakeProductID = "PROD_test"
	setting.WaffoPancakeStoreID = "STO_test"
	operation_setting.PayMethods = []map[string]string{
		{"name": "Waffo", "type": model.PaymentMethodWaffo, "provider": model.PaymentProviderWaffo},
		{"name": "Pancake", "type": model.PaymentMethodWaffoPancake, "provider": model.PaymentProviderWaffoPancake},
		{"name": "Creem", "type": model.PaymentMethodCreem, "provider": model.PaymentProviderCreem},
	}
	require.NoError(t, service.ReportCurrentSystemInstance())
	t.Cleanup(func() {
		setting.WaffoEnabled = originalWaffoEnabled
		setting.WaffoSandbox = originalWaffoSandbox
		setting.WaffoApiKey = originalWaffoAPIKey
		setting.WaffoPrivateKey = originalWaffoPrivateKey
		setting.WaffoPublicCert = originalWaffoPublicCert
		setting.WaffoMerchantId = originalWaffoMerchantID
		operation_setting.CustomCallbackAddress = originalCallbackAddress
		setting.WaffoPancakeMerchantID = originalMerchantID
		setting.WaffoPancakePrivateKey = originalPrivateKey
		setting.WaffoPancakeProductID = originalProductID
		setting.WaffoPancakeStoreID = originalStoreID
		operation_setting.PayMethods = originalPayMethods
	})
}

func TestRetainedTopUpHandlersRejectOversizedRequestBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configureRetainedTopUpHandlerTest(t)
	body := fmt.Sprintf(
		`{"amount":1,"plan_id":1,"product_id":"product","payment_method":"creem","padding":"%s"}`,
		strings.Repeat("x", paymentRequestBodyLimit),
	)
	tests := []struct {
		name    string
		path    string
		handler gin.HandlerFunc
	}{
		{name: "creem pay", path: "/api/user/creem/pay", handler: RequestCreemPay},
		{name: "waffo amount", path: "/api/user/waffo/amount", handler: RequestWaffoAmount},
		{name: "waffo pay", path: "/api/user/waffo/pay", handler: RequestWaffoPay},
		{name: "waffo pancake amount", path: "/api/user/waffo-pancake/amount", handler: RequestWaffoPancakeAmount},
		{name: "waffo pancake pay", path: "/api/user/waffo-pancake/pay", handler: RequestWaffoPancakePay},
		{name: "waffo pancake subscription pay", path: "/api/subscription/waffo-pancake/pay", handler: SubscriptionRequestWaffoPancakePay},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(body))

			test.handler(context)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"payment_request_invalid"`)
		})
	}
}

func TestRetainedVariableTopUpHandlersRejectOutOfRangeAmounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configureRetainedTopUpHandlerTest(t)
	tests := []struct {
		name    string
		path    string
		handler gin.HandlerFunc
	}{
		{name: "waffo amount", path: "/api/user/waffo/amount", handler: RequestWaffoAmount},
		{name: "waffo pay", path: "/api/user/waffo/pay", handler: RequestWaffoPay},
		{name: "waffo pancake amount", path: "/api/user/waffo-pancake/amount", handler: RequestWaffoPancakeAmount},
		{name: "waffo pancake pay", path: "/api/user/waffo-pancake/pay", handler: RequestWaffoPancakePay},
	}
	for _, amount := range []int64{0, service.MaxPaymentTopUpAmount + 1, math.MaxInt64} {
		for _, test := range tests {
			t.Run(fmt.Sprintf("%s/%d", test.name, amount), func(t *testing.T) {
				recorder := httptest.NewRecorder()
				context, _ := gin.CreateTestContext(recorder)
				context.Request = httptest.NewRequest(
					http.MethodPost,
					test.path,
					strings.NewReader(fmt.Sprintf(`{"amount":%d}`, amount)),
				)

				test.handler(context)

				assert.Equal(t, http.StatusOK, recorder.Code)
				assert.Contains(t, recorder.Body.String(), `"code":"payment_amount_invalid"`)
			})
		}
	}
}

func TestNormalizeRetainedTopUpCreditRejectsUnrepresentableQuota(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	originalDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	t.Cleanup(func() {
		common.QuotaPerUnit = originalQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalDisplayType
	})

	tests := []struct {
		name             string
		displayType      string
		quotaPerUnit     float64
		amount           int64
		expectedAmount   int64
		expectedCredit   int
		expectedValidity bool
	}{
		{name: "currency display", displayType: operation_setting.QuotaDisplayTypeUSD, quotaPerUnit: 2, amount: 3, expectedAmount: 3, expectedCredit: 6, expectedValidity: true},
		{name: "token display exact normalization", displayType: operation_setting.QuotaDisplayTypeTokens, quotaPerUnit: 2, amount: 6, expectedAmount: 3, expectedCredit: 6, expectedValidity: true},
		{name: "token display rejects fractional normalization", displayType: operation_setting.QuotaDisplayTypeTokens, quotaPerUnit: 2, amount: 5, expectedValidity: false},
		{name: "token display rejects minimum over-credit", displayType: operation_setting.QuotaDisplayTypeTokens, quotaPerUnit: 500_000, amount: 1, expectedValidity: false},
		{name: "currency display quota overflow", displayType: operation_setting.QuotaDisplayTypeUSD, quotaPerUnit: float64(common.MaxQuota), amount: 2, expectedValidity: false},
		{name: "token display normalized amount overflow", displayType: operation_setting.QuotaDisplayTypeTokens, quotaPerUnit: 1e-20, amount: 10_000, expectedValidity: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			common.QuotaPerUnit = test.quotaPerUnit
			operation_setting.GetGeneralSetting().QuotaDisplayType = test.displayType

			normalizedAmount, creditQuota, valid := normalizeRetainedTopUpCredit(test.amount)

			assert.Equal(t, test.expectedValidity, valid)
			assert.Equal(t, test.expectedAmount, normalizedAmount)
			assert.Equal(t, test.expectedCredit, creditQuota)
		})
	}
}

func TestRetainedTopUpHandlersRejectQuotaOverflowBeforeOrderCreation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configureRetainedTopUpHandlerTest(t)
	originalQuotaPerUnit := common.QuotaPerUnit
	originalDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	common.QuotaPerUnit = float64(common.MaxQuota)
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeUSD
	t.Cleanup(func() {
		common.QuotaPerUnit = originalQuotaPerUnit
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalDisplayType
	})

	tests := []struct {
		name    string
		path    string
		handler gin.HandlerFunc
		body    string
	}{
		{name: "waffo amount", path: "/api/user/waffo/amount", handler: RequestWaffoAmount, body: `{"amount":2}`},
		{name: "waffo pay", path: "/api/user/waffo/pay", handler: RequestWaffoPay, body: `{"amount":2,"request_id":"overflow-waffo-pay"}`},
		{name: "waffo pancake amount", path: "/api/user/waffo-pancake/amount", handler: RequestWaffoPancakeAmount, body: `{"amount":2}`},
		{name: "waffo pancake pay", path: "/api/user/waffo-pancake/pay", handler: RequestWaffoPancakePay, body: `{"amount":2,"request_id":"overflow-pancake-pay"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))

			test.handler(context)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"payment_amount_invalid"`)
		})
	}
}

func TestRetainedTopUpAmountEndpointsRequireCompleteEnabledGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)
	confirmPaymentComplianceForTest(t)
	originalWaffoEnabled := setting.WaffoEnabled
	originalWaffoSandbox := setting.WaffoSandbox
	originalWaffoAPIKey := setting.WaffoApiKey
	originalWaffoPrivateKey := setting.WaffoPrivateKey
	originalWaffoPublicCert := setting.WaffoPublicCert
	originalMerchantID := setting.WaffoPancakeMerchantID
	originalPrivateKey := setting.WaffoPancakePrivateKey
	originalProductID := setting.WaffoPancakeProductID
	setting.WaffoEnabled = true
	setting.WaffoSandbox = false
	setting.WaffoApiKey = ""
	setting.WaffoPrivateKey = ""
	setting.WaffoPublicCert = ""
	setting.WaffoPancakeMerchantID = "MER_test"
	setting.WaffoPancakePrivateKey = ""
	setting.WaffoPancakeProductID = "PROD_test"
	t.Cleanup(func() {
		setting.WaffoEnabled = originalWaffoEnabled
		setting.WaffoSandbox = originalWaffoSandbox
		setting.WaffoApiKey = originalWaffoAPIKey
		setting.WaffoPrivateKey = originalWaffoPrivateKey
		setting.WaffoPublicCert = originalWaffoPublicCert
		setting.WaffoPancakeMerchantID = originalMerchantID
		setting.WaffoPancakePrivateKey = originalPrivateKey
		setting.WaffoPancakeProductID = originalProductID
	})
	tests := []struct {
		name    string
		path    string
		handler gin.HandlerFunc
	}{
		{name: "waffo amount", path: "/api/user/waffo/amount", handler: RequestWaffoAmount},
		{name: "waffo pay", path: "/api/user/waffo/pay", handler: RequestWaffoPay},
		{name: "waffo pancake amount", path: "/api/user/waffo-pancake/amount", handler: RequestWaffoPancakeAmount},
		{name: "waffo pancake pay", path: "/api/user/waffo-pancake/pay", handler: RequestWaffoPancakePay},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{"amount":10}`))

			test.handler(context)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"payment_method_unavailable"`)
		})
	}
}

func TestCreemPayRejectsMissingUserWithoutCreatingOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "creem-missing-user-payment-key-0123456789abcdef")
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.TopUp{}, &model.SystemInstance{}, &model.Option{}, &model.PaymentQuote{},
		&model.PaymentUserGuard{}, &model.PaymentOrder{}, &model.PaymentTask{},
		&model.PaymentLimitPolicy{}, &model.PaymentLimitBucket{}, &model.PaymentLimitReservation{},
	))
	require.NoError(t, db.Exec("DELETE FROM system_instances").Error)
	confirmPaymentComplianceForTest(t)
	originalAPIKey := setting.CreemApiKey
	originalWebhookSecret := setting.CreemWebhookSecret
	originalProducts := setting.CreemProducts
	originalCallbackAddress := operation_setting.CustomCallbackAddress
	originalPayMethods := operation_setting.PayMethods
	setting.CreemApiKey = "creem-test-api-key"
	setting.CreemWebhookSecret = "creem-test-webhook-secret"
	setting.CreemProducts = `[{"productId":"product_missing_user","name":"Test","price":1,"currency":"USD","quota":100}]`
	operation_setting.CustomCallbackAddress = "https://payments.example.com"
	operation_setting.PayMethods = []map[string]string{{
		"name": "Creem", "type": model.PaymentMethodCreem, "provider": model.PaymentProviderCreem,
	}}
	require.NoError(t, service.ReportCurrentSystemInstance())
	t.Cleanup(func() {
		setting.CreemApiKey = originalAPIKey
		setting.CreemWebhookSecret = originalWebhookSecret
		setting.CreemProducts = originalProducts
		operation_setting.CustomCallbackAddress = originalCallbackAddress
		operation_setting.PayMethods = originalPayMethods
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 987654321)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/user/creem/pay",
		strings.NewReader(`{"product_id":"product_missing_user","payment_method":"creem","request_id":"creem-missing-user"}`),
	)

	RequestCreemPay(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"payment_account_unavailable"`)
	var count int64
	require.NoError(t, db.Model(&model.TopUp{}).Where("user_id = ?", 987654321).Count(&count).Error)
	assert.Zero(t, count)
}

func TestCreemPayRejectsUnsafeConfiguredQuotaWithoutCreatingOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "creem-quota-validation-key-0123456789abcdef")
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.TopUp{}))
	confirmPaymentComplianceForTest(t)
	originalAPIKey := setting.CreemApiKey
	originalWebhookSecret := setting.CreemWebhookSecret
	originalProducts := setting.CreemProducts
	setting.CreemApiKey = "creem-test-api-key"
	setting.CreemWebhookSecret = "creem-test-webhook-secret"
	t.Cleanup(func() {
		setting.CreemApiKey = originalAPIKey
		setting.CreemWebhookSecret = originalWebhookSecret
		setting.CreemProducts = originalProducts
	})

	for _, quota := range []int64{-1, int64(common.MaxQuota) + 1} {
		t.Run(fmt.Sprintf("quota_%d", quota), func(t *testing.T) {
			setting.CreemProducts = fmt.Sprintf(
				`[{"productId":"product_unsafe_quota","name":"Test","price":1,"currency":"USD","quota":%d}]`,
				quota,
			)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Set("id", 987654322)
			context.Request = httptest.NewRequest(
				http.MethodPost,
				"/api/user/creem/pay",
				strings.NewReader(`{"product_id":"product_unsafe_quota","payment_method":"creem"}`),
			)

			RequestCreemPay(context)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"payment_temporarily_unavailable"`)
			var count int64
			require.NoError(t, db.Model(&model.TopUp{}).Where("user_id = ?", 987654322).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}

func TestCreemPayRejectsUnsafePublicProductNameBeforeLegacyIDResolution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "creem-name-validation-key-0123456789abcdef")
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.TopUp{}))
	confirmPaymentComplianceForTest(t)
	originalAPIKey := setting.CreemApiKey
	originalWebhookSecret := setting.CreemWebhookSecret
	originalProducts := setting.CreemProducts
	setting.CreemApiKey = "creem-test-api-key"
	setting.CreemWebhookSecret = "creem-test-webhook-secret"
	setting.CreemProducts = `[{"productId":"product_unsafe_name","name":"Unsafe\nName","price":1,"currency":"USD","quota":100}]`
	t.Cleanup(func() {
		setting.CreemApiKey = originalAPIKey
		setting.CreemWebhookSecret = originalWebhookSecret
		setting.CreemProducts = originalProducts
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 987654323)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/user/creem/pay",
		strings.NewReader(`{"product_id":"product_unsafe_name","payment_method":"creem"}`),
	)

	RequestCreemPay(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"payment_temporarily_unavailable"`)
	var count int64
	require.NoError(t, db.Model(&model.TopUp{}).Where("user_id = ?", 987654323).Count(&count).Error)
	assert.Zero(t, count)
}

func TestCreemWebhookRejectsOversizedBodyBeforeSignatureVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "creem-body-limit-test-payment-key-00001")
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.SystemInstance{}))
	originalSessionSecret := common.SessionSecret
	originalCryptoSecret := common.CryptoSecret
	originalSecret := setting.CreemWebhookSecret
	common.SessionSecret = "creem-body-limit-test-session-secret"
	common.CryptoSecret = "creem-body-limit-test-crypto-secret"
	setting.CreemWebhookSecret = "creem_test_webhook_secret"
	t.Cleanup(func() {
		common.SessionSecret = originalSessionSecret
		common.CryptoSecret = originalCryptoSecret
		setting.CreemWebhookSecret = originalSecret
	})
	require.NoError(t, service.ReportCurrentSystemInstance())

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/creem/webhook",
		strings.NewReader(strings.Repeat("x", retainedPaymentWebhookBodyLimit+1)),
	)
	context.Request.Header.Set(CreemSignatureHeader, "invalid")

	CreemWebhook(context)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestCreemCompletedWebhookUsesAmountPaidAndAuditsConflictingDuplicate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.TopUp{},
		&model.PaymentEvent{},
		&model.SubscriptionPlan{},
		&model.SubscriptionOrder{},
		&model.UserSubscription{},
	))
	const userID = 984021
	const tradeNo = "creem-controller-amount-paid"
	require.NoError(t, db.Create(&model.User{
		Id: userID, Username: tradeNo, AffCode: tradeNo, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, (&model.TopUp{
		UserId: userID, Amount: 2, Money: 10, TradeNo: tradeNo,
		PaymentMethod: model.PaymentMethodCreem, PaymentProvider: model.PaymentProviderCreem,
		Currency: "USD", ExpectedAmountMinor: 1000, Status: common.TopUpStatusPending,
		CreateTime: time.Now().Unix(),
	}).Insert())

	newContext := func() (*gin.Context, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPost, "/api/creem/webhook", nil)
		return context, recorder
	}
	event := &CreemWebhookEvent{}
	event.Id = "evt_creem_amount_paid"
	event.EventType = "checkout.completed"
	event.Object.RequestId = tradeNo
	event.Object.Mode = "test"
	event.Object.Order.Id = "creem_order_amount_paid"
	event.Object.Order.Status = "paid"
	event.Object.Order.Type = "onetime"
	event.Object.Order.Currency = "USD"
	event.Object.Order.Amount = 1000
	event.Object.Order.AmountPaid = 1000
	event.Object.Order.Mode = "test"

	normalizedEvent, err := normalizedCreemWebhookEvent(event)
	require.NoError(t, err)
	require.NoError(t, service.RecordVerifiedRetainedPaymentWebhookReceived(normalizedEvent))
	context, recorder := newContext()
	handleCheckoutCompleted(context, event, normalizedEvent)
	require.Equal(t, http.StatusOK, recorder.Code)
	var user model.User
	require.NoError(t, db.First(&user, userID).Error)
	quotaAfterCompletion := user.Quota
	require.Positive(t, quotaAfterCompletion)

	event.Object.Order.AmountPaid = 999
	conflictingEvent, err := normalizedCreemWebhookEvent(event)
	require.NoError(t, err)
	require.ErrorIs(t, service.RecordVerifiedRetainedPaymentWebhookReceived(conflictingEvent), model.ErrPaymentEventConflict)

	topUp := model.GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusSuccess, topUp.Status)
	assert.Empty(t, topUp.ReviewReason)
	require.NoError(t, db.First(&user, userID).Error)
	assert.Equal(t, quotaAfterCompletion, user.Quota)
	var inboxEvent model.PaymentEvent
	require.NoError(t, db.Where("provider = ? AND event_key = ?", model.PaymentProviderCreem, event.Id).First(&inboxEvent).Error)
	assert.Equal(t, model.PaymentEventStatusManualReview, inboxEvent.Status)
	assert.Equal(t, model.PaymentReviewCodeEventKeyPayloadConflict, inboxEvent.ReviewCode)
}
