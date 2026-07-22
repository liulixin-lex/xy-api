package controller

import (
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

func TestWaffoPancakeStoreMismatchIsAcknowledgedAndQuarantined(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentEvent{}))
	originalStoreID := setting.WaffoPancakeStoreID
	t.Cleanup(func() { setting.WaffoPancakeStoreID = originalStoreID })
	setting.WaffoPancakeStoreID = "STO_TrustedMerchantStore"

	event := &service.WaffoPancakeWebhookEvent{
		ID:        "evt_pancake_wrong_store",
		EventID:   "business_event_wrong_store",
		EventType: "order.completed",
		StoreID:   "STO_DifferentMerchantStore",
		Mode:      "prod",
		Data: service.WaffoPancakeWebhookData{
			OrderID:                 "ORD_wrong_store",
			OrderMerchantExternalID: "WAFFO_PANCAKE-123",
			Currency:                "USD",
			Amount:                  "10.00",
		},
	}
	normalizedEvent, err := normalizedWaffoPancakeWebhookEvent(event)
	require.NoError(t, err)
	require.NoError(t, service.RecordVerifiedRetainedPaymentWebhookReceived(normalizedEvent))

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/waffo-pancake/webhook/prod", nil)
	assert.False(t, ensureTrustedWaffoPancakeWebhookStore(context, event, normalizedEvent))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "OK", recorder.Body.String())

	var inboxEvent model.PaymentEvent
	require.NoError(t, db.Where("provider = ? AND event_key = ?", model.PaymentProviderWaffoPancake, normalizedEvent.EventKey).First(&inboxEvent).Error)
	assert.Equal(t, model.PaymentEventStatusManualReview, inboxEvent.Status)
	assert.Equal(t, "waffo_pancake_store_mismatch", inboxEvent.LastError)
}

func TestNormalizedWaffoPancakeWebhookEventBindsEnvironment(t *testing.T) {
	for _, test := range []struct {
		mode     string
		wantLive bool
		wantErr  bool
	}{
		{mode: "prod", wantLive: true},
		{mode: "test"},
		{mode: "sandbox", wantErr: true},
		{mode: "", wantErr: true},
	} {
		t.Run(test.mode, func(t *testing.T) {
			event := &service.WaffoPancakeWebhookEvent{
				ID: "evt_pancake_mode_" + test.mode, EventID: "payment_pancake_mode_" + test.mode,
				EventType: "order.completed", Mode: test.mode,
				Data: service.WaffoPancakeWebhookData{
					OrderID: "ORD_pancake_mode_" + test.mode, OrderMerchantExternalID: "PO_PANCAKE_MODE_" + test.mode,
					Currency: "USD", Amount: "10.00",
				},
			}
			normalized, err := normalizedWaffoPancakeWebhookEvent(event)
			require.NotNil(t, normalized)
			if test.wantErr {
				require.Error(t, err)
				assert.Nil(t, normalized.ProviderLivemode)
				assert.True(t, normalized.ManualReview)
				assert.False(t, normalized.Paid)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, normalized.ProviderLivemode)
			assert.Equal(t, test.wantLive, *normalized.ProviderLivemode)
			assert.True(t, normalized.Paid)
		})
	}
}

func TestWaffoPancakeWebhookEnvironmentRequiresPathAndConfigurationAgreement(t *testing.T) {
	production := true
	testMode := false
	assert.Empty(t, waffoPancakeWebhookEnvironmentMismatchReason(true, true, &production))
	assert.Equal(t, "waffo_pancake_webhook_path_environment_mismatch",
		waffoPancakeWebhookEnvironmentMismatchReason(false, true, &production))
	assert.Equal(t, "waffo_pancake_webhook_configuration_environment_mismatch",
		waffoPancakeWebhookEnvironmentMismatchReason(false, true, &testMode))
	assert.Equal(t, "waffo_pancake_webhook_environment_invalid",
		waffoPancakeWebhookEnvironmentMismatchReason(true, true, nil))
}

func TestWaffoPancakeTestCallbackCannotSettleProductionCanonicalOrder(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.PaymentOrder{}, &model.PaymentEvent{}, &model.PaymentLedgerEntry{},
		&model.TopUp{}, &model.SubscriptionOrder{},
	))
	production := true
	now := time.Now().Unix()
	order := &model.PaymentOrder{
		TradeNo: "PO_PANCAKE_PRODUCTION_CALLBACK", UserID: 990810, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderWaffoPancake, PaymentMethod: model.PaymentMethodWaffoPancake,
		ProviderLivemode: &production, RequestID: "request_pancake_production_callback",
		ExpectedAmountMinor: 1000, Currency: "USD", RequestedAmount: 1, CreditQuota: 100,
		Status: model.PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, db.Create(order).Error)
	normalized, err := normalizedWaffoPancakeWebhookEvent(&service.WaffoPancakeWebhookEvent{
		ID: "evt_pancake_test_for_prod", EventID: "payment_pancake_test_for_prod",
		EventType: "order.completed", StoreID: "STO_AbCdEfGhIjKlMnOpQrStUv", Mode: "test",
		Data: service.WaffoPancakeWebhookData{
			OrderID: "ORD_pancake_test_for_prod", OrderMerchantExternalID: order.TradeNo,
			Currency: "USD", Amount: "10.00",
		},
	})
	require.NoError(t, err)
	handled, err := processCanonicalRetainedPaymentEvent(normalized)
	require.True(t, handled)
	require.NoError(t, err)

	require.NoError(t, db.First(order, order.ID).Error)
	assert.Equal(t, model.PaymentOrderStatusManualReview, order.Status)
	assert.Equal(t, "waffo_pancake_order_event_environment_mismatch", order.StatusReason)
	assert.Zero(t, order.PaidAmountMinor)
	assert.Zero(t, order.SettledAt)
	var ledgerCount int64
	require.NoError(t, db.Model(&model.PaymentLedgerEntry{}).Where("payment_order_id = ?", order.ID).Count(&ledgerCount).Error)
	assert.Zero(t, ledgerCount)
}

func TestFormatWaffoPancakeAmount_UsesDisplayPriceString(t *testing.T) {
	testCases := []struct {
		name     string
		amount   float64
		expected string
	}{
		{name: "whole amount", amount: 29, expected: "29.00"},
		{name: "decimal amount", amount: 29.9, expected: "29.90"},
		{name: "round half up to cents", amount: 29.999, expected: "30.00"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, formatWaffoPancakeAmount(tc.amount))
		})
	}
}

func TestGetWaffoPancakePayMoney(t *testing.T) {
	originalUnitPrice := setting.WaffoPancakeUnitPrice
	originalQuotaDisplayType := operation_setting.GetGeneralSetting().QuotaDisplayType
	originalDiscounts := make(map[int]float64, len(operation_setting.GetPaymentSetting().AmountDiscount))
	for k, v := range operation_setting.GetPaymentSetting().AmountDiscount {
		originalDiscounts[k] = v
	}
	originalTopupGroupRatio := common.TopupGroupRatio2JSONString()

	t.Cleanup(func() {
		setting.WaffoPancakeUnitPrice = originalUnitPrice
		operation_setting.GetGeneralSetting().QuotaDisplayType = originalQuotaDisplayType
		operation_setting.GetPaymentSetting().AmountDiscount = originalDiscounts
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(originalTopupGroupRatio))
	})

	setting.WaffoPancakeUnitPrice = 2.5
	operation_setting.GetPaymentSetting().AmountDiscount = map[int]float64{
		10:                           0.8,
		int(common.QuotaPerUnit * 3): 0.5,
		20:                           0,
	}
	require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"default":1,"vip":1.2}`))

	testCases := []struct {
		name             string
		amount           int64
		group            string
		quotaDisplayType string
		expected         float64
	}{
		{
			name:             "currency display applies unit price group ratio and discount",
			amount:           10,
			group:            "vip",
			quotaDisplayType: operation_setting.QuotaDisplayTypeUSD,
			expected:         24,
		},
		{
			name:             "tokens display converts quota to display units before pricing",
			amount:           int64(common.QuotaPerUnit * 3),
			group:            "vip",
			quotaDisplayType: operation_setting.QuotaDisplayTypeTokens,
			expected:         4.5,
		},
		{
			name:             "non-positive discount falls back to no discount",
			amount:           20,
			group:            "default",
			quotaDisplayType: operation_setting.QuotaDisplayTypeUSD,
			expected:         50,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.GetGeneralSetting().QuotaDisplayType = tc.quotaDisplayType
			normalizedAmount, _, valid := normalizeRetainedTopUpCredit(tc.amount)
			require.True(t, valid)
			actual := getWaffoPancakePayMoney(tc.amount, normalizedAmount, tc.group)
			require.InDelta(t, tc.expected, actual, 0.000001)
		})
	}
}

func TestListWaffoPancakeCatalogReturnsStableConfigurationError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalMerchantID := setting.WaffoPancakeMerchantID
	originalPrivateKey := setting.WaffoPancakePrivateKey
	t.Cleanup(func() {
		setting.WaffoPancakeMerchantID = originalMerchantID
		setting.WaffoPancakePrivateKey = originalPrivateKey
	})
	setting.WaffoPancakeMerchantID = ""
	setting.WaffoPancakePrivateKey = ""

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/waffo-pancake/catalog", nil)

	ListWaffoPancakeCatalog(context)

	require.Equal(t, http.StatusConflict, recorder.Code)
	require.JSONEq(t, `{"success":false,"code":"waffo_pancake_configuration_incomplete"}`, recorder.Body.String())
}

func TestListWaffoPancakeCatalogDoesNotExposeCredentialValidationDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/waffo-pancake/catalog",
		strings.NewReader(`{"merchant_id":"invalid-merchant","private_key":"sensitive-invalid-key"}`),
	)
	context.Request.Header.Set("Content-Type", "application/json")

	ListWaffoPancakeCatalog(context)

	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
	require.JSONEq(t, `{"success":false,"code":"waffo_pancake_credentials_invalid"}`, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "sensitive-invalid-key")
	require.NotContains(t, recorder.Body.String(), "merchant")
}
