package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingAuthorityPaymentProvider struct {
	name string
}

func (p *failingAuthorityPaymentProvider) Name() string { return p.name }

func (*failingAuthorityPaymentProvider) ValidateMethod(string) error { return nil }

func (*failingAuthorityPaymentProvider) Create(context.Context, *model.PaymentOrder) (*service.PaymentStart, error) {
	return nil, errors.New("not implemented")
}

func (*failingAuthorityPaymentProvider) VerifyWebhook(*http.Request) (*service.NormalizedPaymentEvent, error) {
	return nil, errors.New("not implemented")
}

func (*failingAuthorityPaymentProvider) Query(context.Context, *model.PaymentOrder) (*service.NormalizedPaymentEvent, error) {
	return nil, errors.New("not implemented")
}

func (*failingAuthorityPaymentProvider) ValidateVerifiedWebhook(context.Context, *service.NormalizedPaymentEvent) error {
	return errors.New("provider authority temporarily unavailable secret_private")
}

func TestPaymentMutationEndpointsRejectOversizedBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{name: "quote", handler: CreatePaymentQuote},
		{name: "start", handler: StartPayment},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"padding":"`+strings.Repeat("x", paymentRequestBodyLimit)+`"}`))
			context.Request.Header.Set("Content-Type", "application/json")

			test.handler(context)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"code":"payment_request_invalid"`)
			assert.NotContains(t, recorder.Body.String(), `"message"`)
		})
	}
}

func TestLegacyPaymentRequestIDUsesClientValueAndFallsBackOnlyWhenMissing(t *testing.T) {
	provided, err := legacyPaymentRequestID("  checkout_retry_123  ", "legacy_topup_")
	require.NoError(t, err)
	assert.Equal(t, "checkout_retry_123", provided)

	generated, err := legacyPaymentRequestID("", "legacy_topup_")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(generated, "legacy_topup_"))
	assert.Len(t, generated, len("legacy_topup_")+24)

	_, err = legacyPaymentRequestID("invalid request id", "legacy_topup_")
	assert.Error(t, err)
}

func TestSubscriptionBalanceRequestIDKeepsLegacyPayloadCompatible(t *testing.T) {
	generated, err := subscriptionBalanceRequestID("")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(generated, "legacy_balance_"))
	assert.Len(t, generated, len("legacy_balance_")+24)

	provided, err := subscriptionBalanceRequestID(" balance_purchase_123 ")
	require.NoError(t, err)
	assert.Equal(t, "balance_purchase_123", provided)

	_, err = subscriptionBalanceRequestID("invalid request id")
	assert.Error(t, err)
}

func TestLegacyEpayAmountPreviewRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"padding":"`+strings.Repeat("x", paymentRequestBodyLimit)+`"}`,
	))
	context.Request.Header.Set("Content-Type", "application/json")

	RequestAmount(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"payment_request_invalid"`)
	assert.NotContains(t, recorder.Body.String(), "参数错误")
}

func TestSubscriptionBalancePayRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	paymentSetting := operation_setting.GetPaymentSetting()
	originalConfirmed := paymentSetting.ComplianceConfirmed
	originalTermsVersion := paymentSetting.ComplianceTermsVersion
	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() {
		paymentSetting.ComplianceConfirmed = originalConfirmed
		paymentSetting.ComplianceTermsVersion = originalTermsVersion
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 1)
	context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"plan_id":1,"padding":"`+strings.Repeat("x", paymentRequestBodyLimit)+`"}`,
	))
	context.Request.Header.Set("Content-Type", "application/json")

	SubscriptionRequestBalancePay(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Contains(t, recorder.Body.String(), `"code":"payment_request_invalid"`)
	assert.NotContains(t, recorder.Body.String(), "参数错误")
}

func TestPrivilegedCompatibilityPaymentMutationsRejectOversizedBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name       string
		handler    gin.HandlerFunc
		statusCode int
	}{
		{name: "compliance", handler: ConfirmPaymentCompliance, statusCode: http.StatusBadRequest},
		{name: "manual completion", handler: AdminCompleteTopUp, statusCode: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
				`{"padding":"`+strings.Repeat("x", paymentRequestBodyLimit)+`"}`,
			))
			context.Request.Header.Set("Content-Type", "application/json")

			test.handler(context)

			assert.Equal(t, test.statusCode, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
		})
	}
}

func TestPaymentOrderPublicStatusCodesHideInternalState(t *testing.T) {
	tests := []struct {
		order    model.PaymentOrder
		expected string
	}{
		{order: model.PaymentOrder{Status: model.PaymentOrderStatusPending}, expected: "preparing"},
		{order: model.PaymentOrder{Status: model.PaymentOrderStatusPending, StartFlow: "qr", StartedAt: 1}, expected: "awaiting_payment"},
		{order: model.PaymentOrder{Status: model.PaymentOrderStatusProcessing}, expected: "preparing"},
		{order: model.PaymentOrder{Status: model.PaymentOrderStatusPaid}, expected: "confirming"},
		{order: model.PaymentOrder{Status: model.PaymentOrderStatusFulfilled}, expected: "succeeded"},
		{order: model.PaymentOrder{Status: model.PaymentOrderStatusExpired}, expected: "expired"},
		{order: model.PaymentOrder{Status: model.PaymentOrderStatusManualReview, StatusReason: "xorpay credential generation 7 rejected"}, expected: "temporarily_unavailable"},
	}
	for _, test := range tests {
		assert.Equal(t, test.expected, paymentOrderPublicStatusCode(&test.order))
	}
	assert.Equal(t, "temporarily_unavailable", paymentOrderPublicStatusCode(nil))
}

func TestPublicPaymentViewsDoNotSerializeGatewayIdentifiers(t *testing.T) {
	quotePayload, err := common.Marshal(publicPaymentQuoteView{
		QuoteID: "PQ_PUBLIC", RouteID: "alipay_primary", PublicMethod: "alipay", ChannelAlias: "qr",
	})
	require.NoError(t, err)
	serializedQuote := string(quotePayload)
	assert.NotContains(t, serializedQuote, `"provider"`)
	assert.NotContains(t, serializedQuote, `"payment_method"`)

	checkout, err := service.PublicPaymentCheckout(&model.PaymentOrder{TradeNo: "PO_PUBLIC", ExpiresAt: 123})
	require.NoError(t, err)
	orderPayload, err := common.Marshal(paymentOrderView{
		TradeNo: "PO_PUBLIC", RouteID: "alipay_primary", PublicMethod: "alipay",
		ChannelAlias: "qr", StatusCode: "awaiting_payment", Checkout: checkout,
	})
	require.NoError(t, err)
	serializedOrder := string(orderPayload)
	assert.NotContains(t, serializedOrder, `"provider"`)
	assert.NotContains(t, serializedOrder, `"payment_method"`)
	assert.NotContains(t, serializedOrder, `"status_reason"`)
	assert.Contains(t, serializedOrder, `"checkout":{"flow":"pending"`)

	startPayload, err := common.Marshal(publicPaymentStart(&service.PaymentStart{
		Flow: service.PaymentFlowPending, TradeNo: "PO_PUBLIC", ExpiresAt: 123,
		URL: "https://provider.invalid/private", QRContent: "private-qr",
	}))
	require.NoError(t, err)
	serializedStart := string(startPayload)
	assert.Contains(t, serializedStart, `"flow":"pending"`)
	assert.NotContains(t, serializedStart, "provider.invalid")
	assert.NotContains(t, serializedStart, "private-qr")
}

func TestPaymentServiceAPIErrorsUseStableCodesAndHideDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		err         error
		status      int
		code        string
		mustContain string
	}{
		{name: "single limit", err: fmt.Errorf("merchant xorpay aid_private: %w", model.ErrPaymentSingleLimitExceeded), status: http.StatusUnprocessableEntity, code: "payment_single_limit_exceeded"},
		{name: "daily limit", err: fmt.Errorf("gateway secret_private: %w", model.ErrPaymentDailyLimitExceeded), status: http.StatusTooManyRequests, code: "payment_daily_limit_exceeded"},
		{name: "expired quote", err: model.ErrPaymentQuoteExpired, status: http.StatusGone, code: "payment_quote_expired"},
		{name: "idempotency", err: model.ErrPaymentIdempotencyConflict, status: http.StatusConflict, code: "payment_request_conflict"},
		{name: "unknown", err: errors.New("xorpay upstream raw error secret_private"), status: http.StatusServiceUnavailable, code: "payment_temporarily_unavailable"},
		{name: "amount", err: fmt.Errorf("top-up amount must be between 1 and %d", service.MaxPaymentTopUpAmount), status: http.StatusBadRequest, code: "payment_amount_invalid", mustContain: `"params"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			paymentServiceAPIError(context, test.err)

			assert.Equal(t, test.status, recorder.Code)
			body := recorder.Body.String()
			assert.Contains(t, body, `"code":"`+test.code+`"`)
			assert.NotContains(t, body, "xorpay")
			assert.NotContains(t, body, "secret_private")
			if test.mustContain != "" {
				assert.Contains(t, body, test.mustContain)
			}
		})
	}
}

func TestCompatibilityPaymentErrorsKeepLegacyEnvelopesWithoutRawDiagnostics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := fmt.Errorf("xorpay secret_private upstream failure: %w", model.ErrPaymentDailyLimitExceeded)

	legacyRecorder := httptest.NewRecorder()
	legacyContext, _ := gin.CreateTestContext(legacyRecorder)
	legacyPaymentServiceAPIError(legacyContext, raw)
	assert.Equal(t, http.StatusOK, legacyRecorder.Code)
	legacyBody := legacyRecorder.Body.String()
	assert.Contains(t, legacyBody, `"message":"error"`)
	assert.Contains(t, legacyBody, `"data":"payment_daily_limit_exceeded"`)
	assert.Contains(t, legacyBody, `"code":"payment_daily_limit_exceeded"`)
	assert.NotContains(t, legacyBody, "xorpay")
	assert.NotContains(t, legacyBody, "secret_private")

	compatibilityRecorder := httptest.NewRecorder()
	compatibilityContext, _ := gin.CreateTestContext(compatibilityRecorder)
	compatibilityPaymentServiceAPIError(compatibilityContext, raw)
	assert.Equal(t, http.StatusOK, compatibilityRecorder.Code)
	compatibilityBody := compatibilityRecorder.Body.String()
	assert.Contains(t, compatibilityBody, `"success":false`)
	assert.Contains(t, compatibilityBody, `"message":"payment_daily_limit_exceeded"`)
	assert.Contains(t, compatibilityBody, `"code":"payment_daily_limit_exceeded"`)
	assert.NotContains(t, compatibilityBody, "xorpay")
	assert.NotContains(t, compatibilityBody, "secret_private")
}

func TestUnifiedPaymentEndpointsRejectUnsafeClusterBeforeCreatingWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "controller-cluster-gate-test-key-00000001")
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.SystemInstance{},
		&model.PaymentQuote{},
		&model.PaymentUserGuard{},
		&model.PaymentOrder{},
	))

	common.OptionMapRWMutex.Lock()
	optionMapWasNil := common.OptionMap == nil
	if optionMapWasNil {
		common.OptionMap = make(map[string]string)
	}
	originalVersion, versionExisted := common.OptionMap[model.PaymentConfigurationVersionOptionKey]
	common.OptionMap[model.PaymentConfigurationVersionOptionKey] = "1"
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		if optionMapWasNil {
			common.OptionMap = nil
		} else if versionExisted {
			common.OptionMap[model.PaymentConfigurationVersionOptionKey] = originalVersion
		} else {
			delete(common.OptionMap, model.PaymentConfigurationVersionOptionKey)
		}
		common.OptionMapRWMutex.Unlock()
	})

	now := common.GetTimestamp()
	require.NoError(t, model.UpsertSystemInstance("controller-cluster-gate-peer", service.SystemInstanceInfo{
		SchemaVersion: 1,
		Node:          common.NodeIdentity{Name: "controller-cluster-gate-peer"},
		Payment:       service.PaymentRuntimeInfo{SchemaVersion: 1},
	}, now, now))

	tests := []struct {
		name    string
		path    string
		body    string
		handler gin.HandlerFunc
	}{
		{name: "quote", path: "/api/user/payment/quote", body: `{}`, handler: CreatePaymentQuote},
		{name: "start", path: "/api/user/payment/start", body: `{"quote_id":"missing","request_id":"cluster_gate_start"}`, handler: StartPayment},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Set("id", 70001)
			context.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			context.Request.Header.Set("Content-Type", "application/json")

			test.handler(context)

			assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			body := recorder.Body.String()
			assert.Contains(t, body, `"code":"payment_temporarily_unavailable"`)
			assert.NotContains(t, body, "SQLite")
			assert.NotContains(t, body, "Redis")
			assert.NotContains(t, body, "configuration")
		})
	}
}

func TestUnifiedPaymentWebhookDefersUntilCurrentNodeIsReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "controller-webhook-readiness-key-0000001")
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.SystemInstance{}, &model.PaymentEvent{}))

	originalSessionSecret := common.SessionSecret
	originalCryptoSecret := common.CryptoSecret
	common.SessionSecret = "controller-webhook-session-secret"
	common.CryptoSecret = "controller-webhook-crypto-secret"
	t.Cleanup(func() {
		common.SessionSecret = originalSessionSecret
		common.CryptoSecret = originalCryptoSecret
	})

	request := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPost, "/api/user/epay/notify", strings.NewReader(""))
		context.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		processUnifiedPaymentWebhook(context, model.PaymentProviderEpay, "success")
		return recorder
	}

	deferred := request()
	assert.Equal(t, http.StatusServiceUnavailable, deferred.Code)
	assert.Equal(t, "fail", deferred.Body.String())
	var eventCount int64
	require.NoError(t, db.Model(&model.PaymentEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount)

	require.NoError(t, service.ReportCurrentSystemInstance())
	rejected := request()
	assert.Equal(t, http.StatusBadRequest, rejected.Code)
	assert.Equal(t, "fail", rejected.Body.String())
	require.NoError(t, db.Model(&model.PaymentEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
}

func TestRetainedPaymentWebhooksDeferUntilCurrentNodeIsReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "controller-retained-webhook-readiness-key")
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.SystemInstance{}))

	originalSessionSecret := common.SessionSecret
	originalCryptoSecret := common.CryptoSecret
	common.SessionSecret = "controller-retained-webhook-session-secret"
	common.CryptoSecret = "controller-retained-webhook-crypto-secret"
	t.Cleanup(func() {
		common.SessionSecret = originalSessionSecret
		common.CryptoSecret = originalCryptoSecret
	})

	tests := []struct {
		name        string
		path        string
		handler     gin.HandlerFunc
		failureBody string
	}{
		{name: "creem", path: "/api/creem/webhook", handler: CreemWebhook},
		{name: "waffo", path: "/api/waffo/webhook", handler: WaffoWebhook},
		{name: "waffo pancake", path: "/api/waffo-pancake/webhook/test", handler: WaffoPancakeWebhook, failureBody: "retry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := func() *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				context, _ := gin.CreateTestContext(recorder)
				context.Request = httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(""))
				test.handler(context)
				return recorder
			}

			deferred := request()
			assert.Equal(t, http.StatusServiceUnavailable, deferred.Code)
			assert.Equal(t, test.failureBody, deferred.Body.String())

			require.NoError(t, service.ReportCurrentSystemInstance())
			acceptedByClusterGate := request()
			assert.NotEqual(t, http.StatusServiceUnavailable, acceptedByClusterGate.Code)
			require.NoError(t, db.Exec("DELETE FROM system_instances").Error)
		})
	}
}

func TestVerifiedWebhookAuthorityFailureKeepsDurableRetryableInbox(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentEvent{}))

	const providerName = "authority_inbox_test"
	service.RegisterPaymentProvider(&failingAuthorityPaymentProvider{name: providerName})
	event := &service.NormalizedPaymentEvent{
		Provider:          providerName,
		EventKey:          "authority_inbox_event",
		EventType:         "provider.paid",
		NormalizedPayload: `{"event":"authority_inbox"}`,
	}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/payment/test/webhook", nil)

	assert.False(t, validatePersistedPaymentWebhook(context, providerName, "success", event))
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.Equal(t, "fail", recorder.Body.String())

	var stored model.PaymentEvent
	require.NoError(t, db.Where("provider = ? AND event_key = ?", providerName, event.EventKey).First(&stored).Error)
	assert.Equal(t, model.PaymentEventStatusFailed, stored.Status)
	assert.Equal(t, "provider_authority_validation_failed", stored.LastError)
	assert.Equal(t, 1, stored.Attempts)
	assert.NotContains(t, stored.LastError, "secret_private")
}
