package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			assert.Contains(t, recorder.Body.String(), "Invalid payment request")
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
	assert.Contains(t, recorder.Body.String(), "参数错误")
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
	assert.Contains(t, recorder.Body.String(), "参数错误")
}

func TestPrivilegedCompatibilityPaymentMutationsRejectOversizedBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{name: "compliance", handler: ConfirmPaymentCompliance},
		{name: "manual completion", handler: AdminCompleteTopUp},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
				`{"padding":"`+strings.Repeat("x", paymentRequestBodyLimit)+`"}`,
			))
			context.Request.Header.Set("Content-Type", "application/json")

			test.handler(context)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
		})
	}
}

func TestPaymentOrderPublicReasonNeverLeaksAdministratorEvidence(t *testing.T) {
	internalEvidence := "Stripe dashboard case cs_sensitive account acct_internal risk ticket 12345"
	for _, status := range []string{
		model.PaymentOrderStatusManualReview,
		model.PaymentOrderStatusFailed,
		model.PaymentOrderStatusExpired,
		model.PaymentOrderStatusRefundPending,
		model.PaymentOrderStatusRefunded,
		model.PaymentOrderStatusDisputed,
		model.PaymentOrderStatusDebt,
	} {
		publicReason := paymentOrderPublicReason(status)
		assert.NotEmpty(t, publicReason)
		assert.NotEqual(t, internalEvidence, publicReason)
		assert.NotContains(t, publicReason, "acct_internal")
	}
	assert.Empty(t, paymentOrderPublicReason(model.PaymentOrderStatusFulfilled))
}
