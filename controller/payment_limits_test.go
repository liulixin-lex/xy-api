package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdatePaymentLimitPolicyReturnsStableTimezoneLockCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.PaymentLimitPolicy{}, &model.PaymentLimitBucket{}, &model.PaymentLimitReservation{},
	))
	policy := &model.PaymentLimitPolicy{
		Provider: model.PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY",
		SingleLimitMinor: 5000, DailyLimitMinor: 10000, Timezone: "UTC", Enabled: true,
	}
	require.NoError(t, model.UpsertPaymentLimitPolicy(policy))
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.PaymentLimitBucket{
		Provider: model.PaymentProviderEpay, PaymentMethod: "alipay", Currency: "CNY", DayKey: "2026-07-21",
		PaidMinor: 100, CreatedAt: now, UpdatedAt: now, Version: 1,
	}).Error)
	payload, err := common.Marshal(paymentLimitPolicyRequest{
		Provider: "epay", PaymentMethod: "alipay", Currency: "CNY",
		SingleLimitMinor: "5000", DailyLimitMinor: "10000", Timezone: "Asia/Shanghai", Enabled: true,
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/payment/limits", bytes.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	UpdatePaymentLimitPolicy(context)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	assert.JSONEq(t, `{"success":false,"code":"payment_limit_timezone_locked"}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), model.ErrPaymentLimitTimezoneLocked.Error())
}

func TestUpdatePaymentLimitPolicyReturnsStableValidationCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload, err := common.Marshal(paymentLimitPolicyRequest{
		Provider: "epay", PaymentMethod: "alipay", Currency: "CNY",
		SingleLimitMinor: "not-a-number", DailyLimitMinor: "10000", Timezone: "UTC", Enabled: true,
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/option/payment/limits", bytes.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	UpdatePaymentLimitPolicy(context)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.JSONEq(t, `{"success":false,"code":"payment_limit_invalid"}`, recorder.Body.String())
}

func TestPaymentCredentialRevocationPreviewUsesServerResolvedGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.PaymentOrder{}, &model.PaymentEvent{}, &model.TopUp{}, &model.SubscriptionOrder{},
	))

	originalID := operation_setting.EpayIdPrevious
	originalKey := operation_setting.EpayKeyPrevious
	originalGeneration := operation_setting.EpayPreviousCredentialGeneration
	originalValidBefore := operation_setting.EpayPreviousValidBefore
	originalExpiresAt := operation_setting.EpayPreviousExpiresAt
	operation_setting.EpayIdPrevious = "preview-merchant"
	operation_setting.EpayKeyPrevious = "preview-secret-must-not-leak"
	operation_setting.EpayPreviousCredentialGeneration = 7
	operation_setting.EpayPreviousValidBefore = time.Now().Add(-time.Minute).Unix()
	operation_setting.EpayPreviousExpiresAt = time.Now().Add(time.Hour).Unix()
	t.Cleanup(func() {
		operation_setting.EpayIdPrevious = originalID
		operation_setting.EpayKeyPrevious = originalKey
		operation_setting.EpayPreviousCredentialGeneration = originalGeneration
		operation_setting.EpayPreviousValidBefore = originalValidBefore
		operation_setting.EpayPreviousExpiresAt = originalExpiresAt
	})

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

	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.PaymentOrder{
		TradeNo: "PO_REVOCATION_PREVIEW_CONTROLLER", UserID: 979221, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "preview-controller",
		ProviderCredentialGeneration: 7, ExpectedAmountMinor: 100, Currency: "CNY",
		Status: model.PaymentOrderStatusPending, CreatedAt: now - 10, UpdatedAt: now, Version: 1,
	}).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodGet, "/api/option/payment/credential-revocation-preview?provider=epay&mode=previous&generation=999", nil,
	)
	GetPaymentCredentialRevocationPreview(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.Contains(t, body, `"canonical_affected_orders":1`)
	assert.Contains(t, body, `"canonical_unfinished_orders":1`)
	assert.Contains(t, body, `"configuration_version":1`)
	assert.NotContains(t, body, "preview-secret-must-not-leak")
	assert.NotContains(t, body, "PO_REVOCATION_PREVIEW_CONTROLLER")
	assert.NotContains(t, body, `"generation"`)
	assert.False(t, strings.Contains(strings.ToLower(body), "credential"))
}

func TestCurrentOnlyCredentialDisablePreviewSupportsRetainedProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.PaymentOrder{}, &model.PaymentEvent{}, &model.TopUp{}, &model.SubscriptionOrder{},
	))

	originalCreemAPIKey, originalCreemWebhookSecret := setting.CreemApiKey, setting.CreemWebhookSecret
	originalWaffoSandbox := setting.WaffoSandbox
	originalWaffoAPIKey, originalWaffoPrivateKey, originalWaffoPublicCert := setting.WaffoApiKey, setting.WaffoPrivateKey, setting.WaffoPublicCert
	originalPancakePrivateKey, originalPancakeStoreID := setting.WaffoPancakePrivateKey, setting.WaffoPancakeStoreID
	setting.CreemApiKey = "creem-preview-api"
	setting.CreemWebhookSecret = "creem-preview-webhook"
	setting.WaffoSandbox = false
	setting.WaffoApiKey = "waffo-preview-api"
	setting.WaffoPrivateKey = "waffo-preview-private"
	setting.WaffoPublicCert = "waffo-preview-cert"
	setting.WaffoPancakePrivateKey = "pancake-preview-private"
	setting.WaffoPancakeStoreID = "STO_AbCdEfGhIjKlMnOpQrStUv"
	t.Cleanup(func() {
		setting.CreemApiKey, setting.CreemWebhookSecret = originalCreemAPIKey, originalCreemWebhookSecret
		setting.WaffoSandbox = originalWaffoSandbox
		setting.WaffoApiKey, setting.WaffoPrivateKey, setting.WaffoPublicCert = originalWaffoAPIKey, originalWaffoPrivateKey, originalWaffoPublicCert
		setting.WaffoPancakePrivateKey, setting.WaffoPancakeStoreID = originalPancakePrivateKey, originalPancakeStoreID
	})

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

	now := time.Now().Unix()
	providers := []string{model.PaymentProviderCreem, model.PaymentProviderWaffo, model.PaymentProviderWaffoPancake}
	for index, provider := range providers {
		baseID := 979300 + index*10
		for offset, status := range []string{model.PaymentOrderStatusPending, model.PaymentOrderStatusFailed} {
			startedAt := int64(0)
			if status == model.PaymentOrderStatusFailed {
				startedAt = now - 120
			}
			require.NoError(t, db.Create(&model.PaymentOrder{
				TradeNo: "PO_CURRENT_ONLY_PREVIEW_" + provider + "_" + strconv.Itoa(offset), UserID: baseID + offset,
				OrderKind: model.PaymentOrderKindTopUp, Provider: provider, PaymentMethod: provider,
				RequestID:           "request_current_only_preview_" + provider + "_" + strconv.Itoa(offset),
				ExpectedAmountMinor: 100, Currency: "USD", Status: status, StartedAt: startedAt,
				CreatedAt: now - 180, UpdatedAt: now - 30, Version: 1,
			}).Error)
		}
		require.NoError(t, db.Create(&model.TopUp{
			UserId: baseID + 3, Amount: 1, Money: 1, TradeNo: "LEGACY_CURRENT_ONLY_PREVIEW_" + provider,
			PaymentProvider: provider, Status: common.TopUpStatusPending, CreateTime: now - 60,
		}).Error)
	}

	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(
				http.MethodGet,
				"/api/option/payment/credential-revocation-preview?provider="+provider+"&mode=all_active",
				nil,
			)
			GetPaymentCredentialRevocationPreview(context)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			body := recorder.Body.String()
			assert.Contains(t, body, `"canonical_affected_orders":2`)
			assert.Contains(t, body, `"canonical_unfinished_orders":2`)
			assert.Contains(t, body, `"legacy_pending_topups":1`)
			assert.Contains(t, body, `"total_affected_orders":3`)
			assert.Contains(t, body, `"configuration_version":1`)
			assert.NotContains(t, body, "preview-private")
			assert.NotContains(t, body, "CURRENT_ONLY_PREVIEW")
		})
	}
}
