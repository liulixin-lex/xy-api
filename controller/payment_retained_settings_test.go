package controller

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareCurrentOnlyCredentialDisableClearsAuthoritativeTrustMaterial(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider string
		prepare  func()
		assert   func(*testing.T, map[string]string)
	}{
		{
			name: "creem", provider: model.PaymentProviderCreem,
			prepare: func() {
				setting.CreemApiKey = "creem-api-current"
				setting.CreemWebhookSecret = "creem-webhook-current"
			},
			assert: func(t *testing.T, values map[string]string) {
				assert.Equal(t, "", values["CreemApiKey"])
				assert.Equal(t, "", values["CreemWebhookSecret"])
			},
		},
		{
			name: "waffo production", provider: model.PaymentProviderWaffo,
			prepare: func() {
				setting.WaffoSandbox = false
				setting.WaffoEnabled = true
				setting.WaffoApiKey = "waffo-api-current"
				setting.WaffoPrivateKey = "waffo-private-current"
				setting.WaffoPublicCert = "waffo-cert-current"
			},
			assert: func(t *testing.T, values map[string]string) {
				assert.Equal(t, "false", values["WaffoEnabled"])
				assert.Equal(t, "", values["WaffoApiKey"])
				assert.Equal(t, "", values["WaffoPrivateKey"])
				assert.Equal(t, "", values["WaffoPublicCert"])
			},
		},
		{
			name: "waffo sandbox", provider: model.PaymentProviderWaffo,
			prepare: func() {
				setting.WaffoSandbox = true
				setting.WaffoEnabled = true
				setting.WaffoSandboxApiKey = "waffo-sandbox-api-current"
				setting.WaffoSandboxPrivateKey = "waffo-sandbox-private-current"
				setting.WaffoSandboxPublicCert = "waffo-sandbox-cert-current"
			},
			assert: func(t *testing.T, values map[string]string) {
				assert.Equal(t, "false", values["WaffoEnabled"])
				assert.Equal(t, "", values["WaffoSandboxApiKey"])
				assert.Equal(t, "", values["WaffoSandboxPrivateKey"])
				assert.Equal(t, "", values["WaffoSandboxPublicCert"])
			},
		},
		{
			name: "waffo pancake", provider: model.PaymentProviderWaffoPancake,
			prepare: func() {
				setting.WaffoPancakePrivateKey = "pancake-private-current"
				setting.WaffoPancakeStoreID = "pancake-store-current"
			},
			assert: func(t *testing.T, values map[string]string) {
				assert.Equal(t, "", values["WaffoPancakePrivateKey"])
				assert.Equal(t, "", values["WaffoPancakeStoreID"])
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			originalCreemAPIKey, originalCreemWebhookSecret := setting.CreemApiKey, setting.CreemWebhookSecret
			originalWaffoSandbox, originalWaffoEnabled := setting.WaffoSandbox, setting.WaffoEnabled
			originalWaffoAPIKey, originalWaffoPrivateKey, originalWaffoPublicCert := setting.WaffoApiKey, setting.WaffoPrivateKey, setting.WaffoPublicCert
			originalWaffoSandboxAPIKey, originalWaffoSandboxPrivateKey, originalWaffoSandboxPublicCert := setting.WaffoSandboxApiKey, setting.WaffoSandboxPrivateKey, setting.WaffoSandboxPublicCert
			originalPancakePrivateKey, originalPancakeStoreID := setting.WaffoPancakePrivateKey, setting.WaffoPancakeStoreID
			t.Cleanup(func() {
				setting.CreemApiKey, setting.CreemWebhookSecret = originalCreemAPIKey, originalCreemWebhookSecret
				setting.WaffoSandbox, setting.WaffoEnabled = originalWaffoSandbox, originalWaffoEnabled
				setting.WaffoApiKey, setting.WaffoPrivateKey, setting.WaffoPublicCert = originalWaffoAPIKey, originalWaffoPrivateKey, originalWaffoPublicCert
				setting.WaffoSandboxApiKey, setting.WaffoSandboxPrivateKey, setting.WaffoSandboxPublicCert = originalWaffoSandboxAPIKey, originalWaffoSandboxPrivateKey, originalWaffoSandboxPublicCert
				setting.WaffoPancakePrivateKey, setting.WaffoPancakeStoreID = originalPancakePrivateKey, originalPancakeStoreID
			})
			test.prepare()
			values := map[string]string{}
			revocations, err := preparePaymentCredentialRotations(values, map[string]bool{test.provider: true}, time.Now().Unix())
			require.NoError(t, err)
			require.Len(t, revocations, 1)
			assert.Equal(t, test.provider, revocations[0].Provider)
			assert.Zero(t, revocations[0].Generation)
			assert.True(t, revocations[0].AllActiveOrders)
			test.assert(t, values)
		})
	}
}

func TestValidateRetainedConfigurationChangesUsesActiveAndRecoveryScopes(t *testing.T) {
	for index, test := range []struct {
		name        string
		provider    string
		status      string
		started     bool
		values      map[string]string
		configure   func()
		wantBlocked bool
	}{
		{name: "creem api key active", provider: model.PaymentProviderCreem, status: model.PaymentOrderStatusPending,
			values: map[string]string{"CreemApiKey": "creem-api-next"}, configure: func() { setting.CreemApiKey = "creem-api-current" }, wantBlocked: true},
		{name: "creem webhook recovery", provider: model.PaymentProviderCreem, status: model.PaymentOrderStatusFailed, started: true,
			values: map[string]string{"CreemWebhookSecret": "creem-webhook-next"}, configure: func() { setting.CreemWebhookSecret = "creem-webhook-current" }, wantBlocked: true},
		{name: "creem mode recovery", provider: model.PaymentProviderCreem, status: model.PaymentOrderStatusExpired, started: true,
			values: map[string]string{"CreemTestMode": "true"}, configure: func() { setting.CreemTestMode = false }, wantBlocked: true},
		{name: "waffo active credential recovery", provider: model.PaymentProviderWaffo, status: model.PaymentOrderStatusFailed, started: true,
			values: map[string]string{"WaffoApiKey": "waffo-api-next"}, configure: func() { setting.WaffoSandbox = false; setting.WaffoApiKey = "waffo-api-current" }, wantBlocked: true},
		{name: "waffo inactive credential is future configuration", provider: model.PaymentProviderWaffo, status: model.PaymentOrderStatusPending,
			values: map[string]string{"WaffoSandboxApiKey": "waffo-sandbox-next"}, configure: func() { setting.WaffoSandbox = false; setting.WaffoSandboxApiKey = "waffo-sandbox-current" }},
		{name: "pancake merchant active", provider: model.PaymentProviderWaffoPancake, status: model.PaymentOrderStatusPending,
			values: map[string]string{"WaffoPancakeMerchantID": "merchant-next"}, configure: func() { setting.WaffoPancakeMerchantID = "merchant-current" }, wantBlocked: true},
		{name: "pancake store recovery", provider: model.PaymentProviderWaffoPancake, status: model.PaymentOrderStatusFailed, started: true,
			values: map[string]string{"WaffoPancakeStoreID": "store-next"}, configure: func() { setting.WaffoPancakeStoreID = "store-current" }, wantBlocked: true},
		{name: "pancake mode recovery", provider: model.PaymentProviderWaffoPancake, status: model.PaymentOrderStatusExpired, started: true,
			values: map[string]string{"WaffoPancakeTestMode": "true"}, configure: func() { setting.WaffoPancakeTestMode = false }, wantBlocked: true},
		{name: "pancake product is snapshot configuration", provider: model.PaymentProviderWaffoPancake, status: model.PaymentOrderStatusPending,
			values: map[string]string{"WaffoPancakeProductID": "product-next"}, configure: func() { setting.WaffoPancakeProductID = "product-current" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := setupMidjourneyControllerBillingDB(t)
			require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}, &model.TopUp{}, &model.SubscriptionOrder{}))
			original := struct {
				creemAPI, creemWebhook                        string
				creemTest                                     bool
				waffoSandbox                                  bool
				waffoAPI, waffoSandboxAPI                     string
				pancakeTest                                   bool
				pancakeMerchant, pancakeStore, pancakeProduct string
			}{
				setting.CreemApiKey, setting.CreemWebhookSecret, setting.CreemTestMode,
				setting.WaffoSandbox, setting.WaffoApiKey, setting.WaffoSandboxApiKey,
				setting.WaffoPancakeTestMode,
				setting.WaffoPancakeMerchantID, setting.WaffoPancakeStoreID, setting.WaffoPancakeProductID,
			}
			t.Cleanup(func() {
				setting.CreemApiKey, setting.CreemWebhookSecret, setting.CreemTestMode = original.creemAPI, original.creemWebhook, original.creemTest
				setting.WaffoSandbox, setting.WaffoApiKey, setting.WaffoSandboxApiKey = original.waffoSandbox, original.waffoAPI, original.waffoSandboxAPI
				setting.WaffoPancakeTestMode = original.pancakeTest
				setting.WaffoPancakeMerchantID, setting.WaffoPancakeStoreID, setting.WaffoPancakeProductID = original.pancakeMerchant, original.pancakeStore, original.pancakeProduct
			})
			test.configure()
			now := time.Now().Unix()
			order := &model.PaymentOrder{
				TradeNo: "PO_RETAINED_GUARD_" + test.provider + "_" + string(rune('a'+index)), UserID: 990600 + index,
				OrderKind: model.PaymentOrderKindTopUp, Provider: test.provider, PaymentMethod: test.provider,
				RequestID:           "request_retained_guard_" + test.provider + "_" + string(rune('a'+index)),
				ExpectedAmountMinor: 100, Currency: "USD", Status: test.status, CreatedAt: now - 60, UpdatedAt: now, Version: 1,
			}
			if test.started {
				order.StartedAt = now - 50
			}
			require.NoError(t, db.Create(order).Error)

			err := validateInFlightPaymentConfigurationChanges(test.values, nil)
			if test.wantBlocked {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.provider)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestUpdatePaymentSettingsUsesDedicatedCurrentCredentialDisableField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name    string
		request paymentSettingsUpdateRequest
		code    string
	}{
		{
			name:    "retained provider cannot use previous-generation field",
			request: paymentSettingsUpdateRequest{RevokePreviousCredentials: []string{model.PaymentProviderCreem}, Reason: "disable leaked credential", ExpectedVersion: 1},
			code:    "payment_settings_invalid",
		},
		{
			name:    "disable is a dedicated operation",
			request: paymentSettingsUpdateRequest{Options: map[string]interface{}{"CreemProducts": "[]"}, DisableCurrentCredentials: []string{model.PaymentProviderCreem}, Reason: "disable leaked credential", ExpectedVersion: 1},
			code:    "payment_settings_scope_conflict",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := common.Marshal(test.request)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPut, "/api/option/payment", bytes.NewReader(payload))
			context.Request.Header.Set("Content-Type", "application/json")
			UpdatePaymentSettings(context)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.JSONEq(t, `{"success":false,"code":"`+test.code+`"}`, recorder.Body.String())
		})
	}
}

func TestSaveWaffoPancakeBlocksActiveOrderBindingChanges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.PaymentOrder{}, &model.TopUp{}, &model.SubscriptionOrder{}, &model.PaymentConfigurationAudit{},
	))

	originalMerchantID := setting.WaffoPancakeMerchantID
	originalPrivateKey := setting.WaffoPancakePrivateKey
	originalReturnURL := setting.WaffoPancakeReturnURL
	originalStoreID := setting.WaffoPancakeStoreID
	originalProductID := setting.WaffoPancakeProductID
	setting.WaffoPancakeMerchantID = "MER_AbCdEfGhIjKlMnOpQrStUv"
	setting.WaffoPancakePrivateKey = "current-private-key"
	setting.WaffoPancakeReturnURL = "https://payments.example.com/return"
	setting.WaffoPancakeStoreID = "STO_AbCdEfGhIjKlMnOpQrStUv"
	setting.WaffoPancakeProductID = "PROD_AbCdEfGhIjKlMnOpQrStUv"
	t.Cleanup(func() {
		setting.WaffoPancakeMerchantID = originalMerchantID
		setting.WaffoPancakePrivateKey = originalPrivateKey
		setting.WaffoPancakeReturnURL = originalReturnURL
		setting.WaffoPancakeStoreID = originalStoreID
		setting.WaffoPancakeProductID = originalProductID
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
		TradeNo: "PO_PANCAKE_SAVE_GUARD", UserID: 990701, OrderKind: model.PaymentOrderKindTopUp,
		Provider: model.PaymentProviderWaffoPancake, PaymentMethod: model.PaymentProviderWaffoPancake,
		RequestID: "request_pancake_save_guard", ExpectedAmountMinor: 100, Currency: "USD",
		Status: model.PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
	}).Error)

	payload, err := common.Marshal(saveWaffoPancakeRequest{
		MerchantID: "MER_ZzYyXxWwVvUuTtSsRrQqPp",
		ReturnURL:  setting.WaffoPancakeReturnURL,
		StoreID:    setting.WaffoPancakeStoreID,
		ProductID:  setting.WaffoPancakeProductID,
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 990702)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/waffo-pancake/settings", bytes.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	SaveWaffoPancake(context)

	assert.Equal(t, http.StatusConflict, recorder.Code)
	assert.JSONEq(t, `{"success":false,"code":"payment_settings_change_blocked"}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), model.PaymentProviderWaffoPancake)
}

func TestSaveWaffoPancakePersistsOptionalPricingAndPreservesItForLegacyClients(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.PaymentConfigurationAudit{}, &model.PaymentOrder{}, &model.TopUp{}, &model.SubscriptionOrder{},
	))

	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}))

	originalMerchantID := setting.WaffoPancakeMerchantID
	originalPrivateKey := setting.WaffoPancakePrivateKey
	originalReturnURL := setting.WaffoPancakeReturnURL
	originalTestMode := setting.WaffoPancakeTestMode
	originalUnitPrice := setting.WaffoPancakeUnitPrice
	originalMinTopUp := setting.WaffoPancakeMinTopUp
	originalStoreID := setting.WaffoPancakeStoreID
	originalProductID := setting.WaffoPancakeProductID
	setting.WaffoPancakeMerchantID = "MER_AbCdEfGhIjKlMnOpQrStUv"
	setting.WaffoPancakePrivateKey = privateKeyPEM
	setting.WaffoPancakeReturnURL = "https://payments.example.com/return"
	setting.WaffoPancakeTestMode = false
	setting.WaffoPancakeUnitPrice = 1
	setting.WaffoPancakeMinTopUp = 1
	setting.WaffoPancakeStoreID = "STO_AbCdEfGhIjKlMnOpQrStUv"
	setting.WaffoPancakeProductID = "PROD_AbCdEfGhIjKlMnOpQrStUv"
	t.Cleanup(func() {
		setting.WaffoPancakeMerchantID = originalMerchantID
		setting.WaffoPancakePrivateKey = originalPrivateKey
		setting.WaffoPancakeReturnURL = originalReturnURL
		setting.WaffoPancakeTestMode = originalTestMode
		setting.WaffoPancakeUnitPrice = originalUnitPrice
		setting.WaffoPancakeMinTopUp = originalMinTopUp
		setting.WaffoPancakeStoreID = originalStoreID
		setting.WaffoPancakeProductID = originalProductID
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

	unitPrice := 2.5
	minTopUp := 7
	testMode := true
	request := saveWaffoPancakeRequest{
		MerchantID: setting.WaffoPancakeMerchantID, ReturnURL: setting.WaffoPancakeReturnURL,
		StoreID: setting.WaffoPancakeStoreID, ProductID: setting.WaffoPancakeProductID,
		TestMode: &testMode, UnitPrice: &unitPrice, MinTopUp: &minTopUp, ExpectedVersion: 1,
	}
	payload, err := common.Marshal(request)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 990702)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/waffo-pancake/settings", bytes.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	SaveWaffoPancake(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Equal(t, 2.5, setting.WaffoPancakeUnitPrice)
	assert.Equal(t, 7, setting.WaffoPancakeMinTopUp)
	assert.True(t, setting.WaffoPancakeTestMode)
	assert.Contains(t, recorder.Body.String(), `"test_mode":true`)
	assert.Contains(t, recorder.Body.String(), `"unit_price":2.5`)
	assert.Contains(t, recorder.Body.String(), `"min_top_up":7`)

	var storedUnitPrice, storedMinTopUp, storedTestMode model.Option
	require.NoError(t, db.Where("key = ?", "WaffoPancakeUnitPrice").First(&storedUnitPrice).Error)
	require.NoError(t, db.Where("key = ?", "WaffoPancakeMinTopUp").First(&storedMinTopUp).Error)
	require.NoError(t, db.Where("key = ?", "WaffoPancakeTestMode").First(&storedTestMode).Error)
	assert.Equal(t, "2.5", storedUnitPrice.Value)
	assert.Equal(t, "7", storedMinTopUp.Value)
	assert.Equal(t, "true", storedTestMode.Value)

	legacyPayload, err := common.Marshal(saveWaffoPancakeRequest{
		MerchantID: setting.WaffoPancakeMerchantID, ReturnURL: setting.WaffoPancakeReturnURL,
		StoreID: setting.WaffoPancakeStoreID, ProductID: setting.WaffoPancakeProductID,
		ExpectedVersion: 2,
	})
	require.NoError(t, err)
	legacyRecorder := httptest.NewRecorder()
	legacyContext, _ := gin.CreateTestContext(legacyRecorder)
	legacyContext.Set("id", 990702)
	legacyContext.Request = httptest.NewRequest(http.MethodPost, "/api/waffo-pancake/settings", bytes.NewReader(legacyPayload))
	legacyContext.Request.Header.Set("Content-Type", "application/json")
	SaveWaffoPancake(legacyContext)
	require.Equal(t, http.StatusOK, legacyRecorder.Code, legacyRecorder.Body.String())
	assert.Equal(t, 2.5, setting.WaffoPancakeUnitPrice)
	assert.Equal(t, 7, setting.WaffoPancakeMinTopUp)
	assert.True(t, setting.WaffoPancakeTestMode)
}

func TestValidateWaffoPancakePricingBounds(t *testing.T) {
	require.NoError(t, validatePaymentSettingValue("WaffoPancakeUnitPrice", "2.5"))
	require.NoError(t, validatePaymentSettingValue("WaffoPancakeMinTopUp", "7"))
	for _, value := range []string{"0", "-1", "NaN", "Inf", "1000001"} {
		assert.Error(t, validatePaymentSettingValue("WaffoPancakeUnitPrice", value), value)
	}
	for _, value := range []string{
		"0", "-1", strconv.FormatInt(service.MaxPaymentTopUpAmount+1, 10),
	} {
		assert.Error(t, validatePaymentSettingValue("WaffoPancakeMinTopUp", value), value)
	}
}

func TestValidateWaffoPancakeModeRequiresBoolean(t *testing.T) {
	require.NoError(t, validatePaymentSettingValue("WaffoPancakeTestMode", "true"))
	require.NoError(t, validatePaymentSettingValue("WaffoPancakeTestMode", "false"))
	assert.Error(t, validatePaymentSettingValue("WaffoPancakeTestMode", "prod"))
}
