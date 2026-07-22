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
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v86"
)

func TestPaymentSettingsMutationScopeRejectsMixedModules(t *testing.T) {
	assert.NoError(t, validatePaymentSettingsMutationScope(map[string]interface{}{
		"StripeCurrency": "USD",
		"StripeMinTopUp": 1,
	}, nil, nil))
	assert.NoError(t, validatePaymentSettingsMutationScope(map[string]interface{}{
		"StripeWebhookSecret": "whsec_replacement",
	}, nil, []string{model.PaymentProviderStripe}))
	assert.ErrorIs(t, validatePaymentSettingsMutationScope(map[string]interface{}{
		"EpayId":         "merchant",
		"StripeCurrency": "USD",
	}, nil, nil), errPaymentSettingsScopeConflict)
	assert.ErrorIs(t, validatePaymentSettingsMutationScope(nil, nil, []string{
		model.PaymentProviderEpay, model.PaymentProviderXorPay,
	}), errPaymentSettingsScopeConflict)
	assert.NoError(t, validatePaymentSettingsMutationScope(map[string]interface{}{
		"WaffoPancakeMerchantID": "MER_AbCdEfGhIjKlMnOpQrStUv",
		"WaffoPancakePrivateKey": "private",
		"WaffoPancakeReturnURL":  "https://payments.example.com/return",
		"WaffoPancakeStoreID":    "STO_AbCdEfGhIjKlMnOpQrStUv",
		"WaffoPancakeProductID":  "PROD_AbCdEfGhIjKlMnOpQrStUv",
	}, nil, nil))
	assert.ErrorIs(t, validatePaymentSettingsMutationScope(map[string]interface{}{
		"WaffoMerchantId":        "waffo-merchant",
		"WaffoPancakeMerchantID": "MER_AbCdEfGhIjKlMnOpQrStUv",
	}, nil, nil), errPaymentSettingsScopeConflict)
}

func TestUpdatePaymentSettingsReturnsStableScopeErrorWithoutRawMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload, err := common.Marshal(paymentSettingsUpdateRequest{
		Options: map[string]interface{}{
			"EpayId":         "merchant",
			"StripeCurrency": "USD",
		},
		ExpectedVersion: 1,
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/option/payment", bytes.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	UpdatePaymentSettings(context)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.JSONEq(t, `{"success":false,"code":"payment_settings_scope_conflict"}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "multiple configuration modules")
}

func TestUpdatePaymentSettingsReturnsStableStripeCheckoutHostError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload, err := common.Marshal(paymentSettingsUpdateRequest{
		Options: map[string]interface{}{
			"StripeCheckoutAllowedHosts": "*.example.com",
		},
		ExpectedVersion: 1,
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/option/payment", bytes.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	UpdatePaymentSettings(context)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.JSONEq(t, `{"success":false,"code":"payment_settings_stripe_checkout_hosts_invalid"}`, recorder.Body.String())
	assert.NotContains(t, recorder.Body.String(), "*.example.com")
}

func TestValidatePaymentSettingRejectsUnsafeProviderBindingsAndDiscounts(t *testing.T) {
	err := validatePaymentSettingValue("PayMethods", `[{"name":"Fake Stripe","type":"alipay","provider":"stripe"}]`)
	assert.Error(t, err)

	err = validatePaymentSettingValue("payment_setting.amount_discount", `{"100":1.5}`)
	assert.Error(t, err)

	err = validatePaymentSettingValue("StripePromotionCodesEnabled", "true")
	assert.Error(t, err)

	err = validatePaymentSettingValue("XorPayEnabledMethods", `["native","native"]`)
	assert.Error(t, err)

	err = validatePaymentSettingValue("XorPayCurrency", "USD")
	assert.Error(t, err)
	require.NoError(t, validatePaymentSettingValue("XorPayCurrency", "CNY"))
	require.NoError(t, validatePaymentSettingValue("WaffoWebRedirectHosts", "checkout.partner.example\npay.partner.example"))
	assert.Error(t, validatePaymentSettingValue("WaffoWebRedirectHosts", "https://checkout.partner.example/path"))
	require.NoError(t, validatePaymentSettingValue("WaffoAppRedirectSchemes", "weixin\nalipays"))
	assert.Error(t, validatePaymentSettingValue("WaffoAppRedirectSchemes", "javascript"))
	require.NoError(t, validatePaymentSettingValue("StripeCheckoutAllowedHosts", "checkout.example.com\npay.example.com"))
	assert.Error(t, validatePaymentSettingValue("StripeCheckoutAllowedHosts", "*.example.com"))
	assert.Error(t, validatePaymentSettingValue("StripeCheckoutAllowedHosts", "https://checkout.example.com"))
	assert.Error(t, validatePaymentSettingValue("EpayCurrency", "USD"))
	require.NoError(t, validatePaymentSettingValue("EpayCurrency", "CNY"))
	require.NoError(t, validatePaymentSettingValue("ServerAddress", "http://localhost:3000"))
	require.NoError(t, validatePaymentSettingValue("CustomCallbackAddress", "https://payments.example.com/"))
	require.NoError(t, validatePaymentSettingValue("CustomCallbackAddress", "http://localhost:3000"))
	assert.Error(t, validatePaymentSettingValue("CustomCallbackAddress", "https://payments.example.com/base"))
	assert.Error(t, validatePaymentSettingValue("CustomCallbackAddress", "https://payments.example.com/%2F"))
	assert.Error(t, validatePaymentSettingValue("CustomCallbackAddress", "https://payments.example.com/?tenant=1"))
	assert.Error(t, validatePaymentSettingValue("TopupGroupRatio", `{"default":0}`))
	assert.Error(t, validatePaymentSettingValue("EpayKey", "short-secret"))
	assert.Error(t, validatePaymentSettingValue("EpayKey", "0123456789abcdef"))
	require.NoError(t, validatePaymentSettingValue("EpayKey", "0123456789abcdef0123456789abcdef"))
	assert.Error(t, validatePaymentSettingValue("XorPayAppSecret", "short-secret"))
	assert.Error(t, validatePaymentSettingValue("XorPayAppSecret", "0123456789abcdef"))
	require.NoError(t, validatePaymentSettingValue("XorPayAppSecret", "0123456789abcdef0123456789abcdef"))
}

func TestValidatePaymentSettingRejectsUnsafeCreemProducts(t *testing.T) {
	validProduct := `[{"productId":"prod_safe","name":"Safe","price":9.99,"currency":"USD","quota":1000}]`
	require.NoError(t, validatePaymentSettingValue("CreemProducts", validProduct))

	tests := map[string]string{
		"not an array":           `{}`,
		"null":                   `null`,
		"empty public name":      `[{"productId":"prod_empty_name","name":" ","price":1,"currency":"USD","quota":1}]`,
		"control in public name": `[{"productId":"prod_control_name","name":"Unsafe\nName","price":1,"currency":"USD","quota":1}]`,
		"oversized public name":  `[{"productId":"prod_long_name","name":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","price":1,"currency":"USD","quota":1}]`,
		"empty product id":       `[{"productId":" ","name":"Safe","price":1,"currency":"USD","quota":1}]`,
		"duplicate product id":   `[{"productId":"prod_duplicate","name":"First","price":1,"currency":"USD","quota":1},{"productId":"prod_duplicate","name":"Second","price":2,"currency":"EUR","quota":2}]`,
		"unsupported currency":   `[{"productId":"prod_jpy","name":"Safe","price":1,"currency":"JPY","quota":1}]`,
		"zero price":             `[{"productId":"prod_zero_price","name":"Safe","price":0,"currency":"USD","quota":1}]`,
		"sub-minor price":        `[{"productId":"prod_fraction","name":"Safe","price":0.001,"currency":"USD","quota":1}]`,
		"negative quota":         `[{"productId":"prod_negative","name":"Safe","price":1,"currency":"USD","quota":-1}]`,
		"oversized quota":        `[{"productId":"prod_oversized","name":"Safe","price":1,"currency":"USD","quota":2147483648}]`,
	}
	for name, products := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validatePaymentSettingValue("CreemProducts", products))
		})
	}
}

func TestCompatibilityGatewaySecretsRemainExplicitlyClearable(t *testing.T) {
	for _, key := range []string{
		"EpayKey", "StripeApiSecret", "StripeWebhookSecret", "XorPayAppSecret",
		"CreemApiKey", "CreemWebhookSecret", "WaffoApiKey", "WaffoPrivateKey",
		"WaffoSandboxApiKey", "WaffoSandboxPrivateKey", "WaffoPancakePrivateKey",
	} {
		_, allowed := paymentSettingsAllowedKeys[key]
		assert.True(t, allowed, key)
		assert.True(t, isWriteOnlyPaymentSetting(key), key)
	}
	assert.False(t, isWriteOnlyPaymentSetting("WaffoPublicCert"))
}

func TestUpdatePaymentSettingsPersistsWaffoPancakeBindingAtomicallyAndEncrypted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("PAYMENT_SECRET_KEY", "waffo-pancake-settings-test-key-00000001")
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
	originalStoreID := setting.WaffoPancakeStoreID
	originalProductID := setting.WaffoPancakeProductID
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

	payload, err := common.Marshal(paymentSettingsUpdateRequest{
		Options: map[string]interface{}{
			"WaffoPancakeMerchantID": "MER_AbCdEfGhIjKlMnOpQrStUv",
			"WaffoPancakePrivateKey": privateKeyPEM,
			"WaffoPancakeReturnURL":  "https://payments.example.com/return",
			"WaffoPancakeStoreID":    "STO_AbCdEfGhIjKlMnOpQrStUv",
			"WaffoPancakeProductID":  "PROD_AbCdEfGhIjKlMnOpQrStUv",
		},
		ExpectedVersion: 1,
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("id", 984031)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/option/payment", bytes.NewReader(payload))
	context.Request.Header.Set("Content-Type", "application/json")
	context.Request.RemoteAddr = "192.0.2.31:1234"

	UpdatePaymentSettings(context)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"success":true`)
	assert.Contains(t, recorder.Body.String(), `"version":2`)
	var stored model.Option
	require.NoError(t, db.Where("key = ?", "WaffoPancakePrivateKey").First(&stored).Error)
	assert.Contains(t, stored.Value, "enc:v2:")
	assert.NotContains(t, stored.Value, "BEGIN RSA PRIVATE KEY")
	assert.Equal(t, "MER_AbCdEfGhIjKlMnOpQrStUv", setting.WaffoPancakeMerchantID)
	assert.Equal(t, "STO_AbCdEfGhIjKlMnOpQrStUv", setting.WaffoPancakeStoreID)
	assert.Equal(t, "PROD_AbCdEfGhIjKlMnOpQrStUv", setting.WaffoPancakeProductID)
}

func TestXorPayAidRotationRequiresDifferentAppSecret(t *testing.T) {
	originalAid := setting.XorPayAid
	originalSecret := setting.XorPayAppSecret
	originalGeneration := setting.XorPayCredentialGeneration
	originalPreviousAid := setting.XorPayAidPrevious
	originalPreviousSecret := setting.XorPayAppSecretPrevious
	originalPreviousGeneration := setting.XorPayPreviousCredentialGeneration
	originalPreviousValidBefore := setting.XorPayPreviousValidBefore
	originalPreviousExpiresAt := setting.XorPayPreviousExpiresAt
	t.Cleanup(func() {
		setting.XorPayAid = originalAid
		setting.XorPayAppSecret = originalSecret
		setting.XorPayCredentialGeneration = originalGeneration
		setting.XorPayAidPrevious = originalPreviousAid
		setting.XorPayAppSecretPrevious = originalPreviousSecret
		setting.XorPayPreviousCredentialGeneration = originalPreviousGeneration
		setting.XorPayPreviousValidBefore = originalPreviousValidBefore
		setting.XorPayPreviousExpiresAt = originalPreviousExpiresAt
	})
	setting.XorPayAid = "aid_current"
	setting.XorPayAppSecret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	setting.XorPayCredentialGeneration = 4
	setting.XorPayAidPrevious = ""
	setting.XorPayAppSecretPrevious = ""
	setting.XorPayPreviousCredentialGeneration = 0
	setting.XorPayPreviousValidBefore = 0
	setting.XorPayPreviousExpiresAt = 0
	_, err := preparePaymentCredentialRotations(map[string]string{
		"XorPayAid": setting.XorPayAid, "XorPayAppSecret": setting.XorPayAppSecret,
	}, map[string]bool{model.PaymentProviderXorPay: true}, 1_800_000_000)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must differ")

	_, err = preparePaymentCredentialRotations(map[string]string{
		"XorPayAid": "aid_replacement",
	}, nil, 1_800_000_000)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "different AppSecret")

	values := map[string]string{
		"XorPayAid":       "aid_replacement",
		"XorPayAppSecret": "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy",
	}
	_, err = preparePaymentCredentialRotations(values, map[string]bool{model.PaymentProviderXorPay: true}, 1_800_000_000)
	require.NoError(t, err)
	assert.Equal(t, "0", values["XorPayPreviousCredentialGeneration"])
	assert.Equal(t, "5", values["XorPayCredentialGeneration"])
}

func TestEpayCredentialRotationPreservesThePreviousGeneration(t *testing.T) {
	setupMidjourneyControllerBillingDB(t)
	t.Setenv("PAYMENT_SECRET_KEY", "test-payment-secret-key-at-least-32-bytes")
	originalID := operation_setting.EpayId
	originalKey := operation_setting.EpayKey
	originalGeneration := operation_setting.EpayCredentialGeneration
	originalPreviousID := operation_setting.EpayIdPrevious
	originalPreviousKey := operation_setting.EpayKeyPrevious
	originalPreviousGeneration := operation_setting.EpayPreviousCredentialGeneration
	originalPreviousValidBefore := operation_setting.EpayPreviousValidBefore
	originalPreviousExpiresAt := operation_setting.EpayPreviousExpiresAt
	t.Cleanup(func() {
		operation_setting.EpayId = originalID
		operation_setting.EpayKey = originalKey
		operation_setting.EpayCredentialGeneration = originalGeneration
		operation_setting.EpayIdPrevious = originalPreviousID
		operation_setting.EpayKeyPrevious = originalPreviousKey
		operation_setting.EpayPreviousCredentialGeneration = originalPreviousGeneration
		operation_setting.EpayPreviousValidBefore = originalPreviousValidBefore
		operation_setting.EpayPreviousExpiresAt = originalPreviousExpiresAt
	})
	operation_setting.EpayId = "merchant-current"
	operation_setting.EpayKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	operation_setting.EpayCredentialGeneration = 7
	operation_setting.EpayIdPrevious = ""
	operation_setting.EpayKeyPrevious = ""
	operation_setting.EpayPreviousCredentialGeneration = 0
	operation_setting.EpayPreviousValidBefore = 0
	operation_setting.EpayPreviousExpiresAt = 0

	values := map[string]string{"EpayKey": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	_, err := preparePaymentCredentialRotations(values, nil, 1_800_000_000)
	require.NoError(t, err)
	assert.Equal(t, "merchant-current", values["EpayIdPrevious"])
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", values["EpayKeyPrevious"])
	assert.Equal(t, "7", values["EpayPreviousCredentialGeneration"])
	assert.Equal(t, "8", values["EpayCredentialGeneration"])
	assert.Equal(t, "1800000000", values["EpayPreviousValidBefore"])
	assert.Equal(t, strconv.FormatInt(1_800_000_000+int64(epayCredentialOverlap/time.Second), 10), values["EpayPreviousExpiresAt"])
	assert.Greater(t, epayCredentialOverlap, model.PaymentCallbackRecoveryWindow+2*time.Hour)
}

func TestXorPayCredentialRotationPreservesProviderRetryWindow(t *testing.T) {
	setupMidjourneyControllerBillingDB(t)
	t.Setenv("PAYMENT_SECRET_KEY", "test-payment-secret-key-at-least-32-bytes")
	originalAid := setting.XorPayAid
	originalSecret := setting.XorPayAppSecret
	originalGeneration := setting.XorPayCredentialGeneration
	originalPreviousAid := setting.XorPayAidPrevious
	originalPreviousSecret := setting.XorPayAppSecretPrevious
	originalPreviousGeneration := setting.XorPayPreviousCredentialGeneration
	originalPreviousValidBefore := setting.XorPayPreviousValidBefore
	originalPreviousExpiresAt := setting.XorPayPreviousExpiresAt
	t.Cleanup(func() {
		setting.XorPayAid = originalAid
		setting.XorPayAppSecret = originalSecret
		setting.XorPayCredentialGeneration = originalGeneration
		setting.XorPayAidPrevious = originalPreviousAid
		setting.XorPayAppSecretPrevious = originalPreviousSecret
		setting.XorPayPreviousCredentialGeneration = originalPreviousGeneration
		setting.XorPayPreviousValidBefore = originalPreviousValidBefore
		setting.XorPayPreviousExpiresAt = originalPreviousExpiresAt
	})
	setting.XorPayAid = "aid_current_retry_window"
	setting.XorPayAppSecret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	setting.XorPayCredentialGeneration = 4
	setting.XorPayAidPrevious = ""
	setting.XorPayAppSecretPrevious = ""
	setting.XorPayPreviousCredentialGeneration = 0
	setting.XorPayPreviousValidBefore = 0
	setting.XorPayPreviousExpiresAt = 0

	const now = int64(1_800_000_000)
	values := map[string]string{
		"XorPayAid":       "aid_replacement_retry_window",
		"XorPayAppSecret": "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy",
	}
	_, err := preparePaymentCredentialRotations(values, nil, now)
	require.NoError(t, err)
	assert.Equal(t, setting.XorPayAid, values["XorPayAidPrevious"])
	assert.Equal(t, setting.XorPayAppSecret, values["XorPayAppSecretPrevious"])
	assert.Equal(t, "4", values["XorPayPreviousCredentialGeneration"])
	assert.Equal(t, strconv.FormatInt(now, 10), values["XorPayPreviousValidBefore"])
	assert.Equal(t, strconv.FormatInt(now+int64(xorPayCredentialOverlap/time.Second), 10), values["XorPayPreviousExpiresAt"])
	assert.Greater(t, xorPayCredentialOverlap, model.PaymentCallbackRecoveryWindow+24*time.Hour)
}

func TestEpayEmergencyMigrationCanAtomicallyReplaceEndpointAndCurrentCredential(t *testing.T) {
	setupMidjourneyControllerBillingDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.PaymentOrder{}))
	originalAddress := operation_setting.PayAddress
	originalID := operation_setting.EpayId
	originalKey := operation_setting.EpayKey
	originalGeneration := operation_setting.EpayCredentialGeneration
	originalPreviousID := operation_setting.EpayIdPrevious
	originalPreviousKey := operation_setting.EpayKeyPrevious
	originalPreviousGeneration := operation_setting.EpayPreviousCredentialGeneration
	originalPreviousValidBefore := operation_setting.EpayPreviousValidBefore
	originalPreviousExpiresAt := operation_setting.EpayPreviousExpiresAt
	t.Cleanup(func() {
		operation_setting.PayAddress = originalAddress
		operation_setting.EpayId = originalID
		operation_setting.EpayKey = originalKey
		operation_setting.EpayCredentialGeneration = originalGeneration
		operation_setting.EpayIdPrevious = originalPreviousID
		operation_setting.EpayKeyPrevious = originalPreviousKey
		operation_setting.EpayPreviousCredentialGeneration = originalPreviousGeneration
		operation_setting.EpayPreviousValidBefore = originalPreviousValidBefore
		operation_setting.EpayPreviousExpiresAt = originalPreviousExpiresAt
	})
	operation_setting.PayAddress = "https://compromised-gateway.example"
	operation_setting.EpayId = "merchant-current"
	operation_setting.EpayKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	operation_setting.EpayCredentialGeneration = 7
	operation_setting.EpayIdPrevious = ""
	operation_setting.EpayKeyPrevious = ""
	operation_setting.EpayPreviousCredentialGeneration = 0
	operation_setting.EpayPreviousValidBefore = 0
	operation_setting.EpayPreviousExpiresAt = 0
	_, err := preparePaymentCredentialRotations(map[string]string{
		"EpayId": operation_setting.EpayId, "EpayKey": operation_setting.EpayKey,
	}, map[string]bool{model.PaymentProviderEpay: true}, time.Now().Unix())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must differ")

	now := time.Now().Unix()
	order := &model.PaymentOrder{
		TradeNo: "epay-emergency-endpoint-migration", UserID: 996701,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderEpay, PaymentMethod: "alipay",
		RequestID: "epay-emergency-endpoint-migration", ProviderCredentialGeneration: 7,
		ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusPending, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, model.DB.Create(order).Error)
	t.Cleanup(func() { _ = model.DB.Delete(&model.PaymentOrder{}, order.ID).Error })

	values := map[string]string{
		"PayAddress": "https://replacement-gateway.example",
		"EpayId":     "merchant-replacement",
		"EpayKey":    "cccccccccccccccccccccccccccccccccccccccc",
	}
	emergency := map[string]bool{model.PaymentProviderEpay: true}
	revocations, err := preparePaymentCredentialRotations(values, emergency, now)
	require.NoError(t, err)
	require.Len(t, revocations, 1)
	assert.Equal(t, model.PaymentProviderEpay, revocations[0].Provider)
	assert.Equal(t, int64(7), revocations[0].Generation)
	assert.True(t, epayEmergencyMigrationReplacesCurrent(values, emergency))
	assert.False(t, paymentConfigurationPreconditions(values, emergency).RequireNoActiveEpayOrders)
	require.NoError(t, validateInFlightPaymentConfigurationChanges(values, emergency))

	ordinary := map[string]string{"PayAddress": "https://replacement-gateway.example"}
	assert.True(t, paymentConfigurationPreconditions(ordinary, nil).RequireNoActiveEpayOrders)
	assert.Error(t, validateInFlightPaymentConfigurationChanges(ordinary, nil))
}

func TestGetOptionsNeverReturnsRotatedPaymentSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMidjourneyControllerBillingDB(t)
	t.Setenv(setting.StripeTestModeEnabledEnv, "false")
	originalEpayPreviousID := operation_setting.EpayIdPrevious
	originalEpayPreviousKey := operation_setting.EpayKeyPrevious
	originalEpayPreviousGeneration := operation_setting.EpayPreviousCredentialGeneration
	originalEpayPreviousExpiresAt := operation_setting.EpayPreviousExpiresAt
	originalStripePreviousSecret := setting.StripeWebhookSecretPrevious
	originalStripePreviousExpiresAt := setting.StripeWebhookSecretPreviousExpiresAt
	originalStripeCredentialMode := setting.StripeCredentialLivemode
	originalXorPayPreviousAid := setting.XorPayAidPrevious
	originalXorPayPreviousSecret := setting.XorPayAppSecretPrevious
	originalXorPayPreviousGeneration := setting.XorPayPreviousCredentialGeneration
	originalXorPayPreviousExpiresAt := setting.XorPayPreviousExpiresAt
	common.OptionMapRWMutex.Lock()
	original := common.OptionMap
	common.OptionMap = map[string]string{
		"StripeWebhookSecretPrevious": "whsec_must_not_leak",
		"StripePriceId":               "price_public_template",
	}
	common.OptionMapRWMutex.Unlock()
	operation_setting.EpayIdPrevious = "epay_previous_id"
	operation_setting.EpayKeyPrevious = "epay_previous_secret_must_not_leak"
	operation_setting.EpayPreviousCredentialGeneration = 1
	operation_setting.EpayPreviousExpiresAt = time.Now().Add(time.Hour).Unix()
	setting.StripeWebhookSecretPrevious = "whsec_previous_secret_must_not_leak"
	setting.StripeWebhookSecretPreviousExpiresAt = time.Now().Add(time.Hour).Unix()
	setting.StripeCredentialLivemode = "test"
	setting.XorPayAidPrevious = "xorpay_previous_aid"
	setting.XorPayAppSecretPrevious = "xorpay_previous_secret_must_not_leak"
	setting.XorPayPreviousCredentialGeneration = 1
	setting.XorPayPreviousExpiresAt = time.Now().Add(time.Hour).Unix()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = original
		common.OptionMapRWMutex.Unlock()
		operation_setting.EpayIdPrevious = originalEpayPreviousID
		operation_setting.EpayKeyPrevious = originalEpayPreviousKey
		operation_setting.EpayPreviousCredentialGeneration = originalEpayPreviousGeneration
		operation_setting.EpayPreviousExpiresAt = originalEpayPreviousExpiresAt
		setting.StripeWebhookSecretPrevious = originalStripePreviousSecret
		setting.StripeWebhookSecretPreviousExpiresAt = originalStripePreviousExpiresAt
		setting.StripeCredentialLivemode = originalStripeCredentialMode
		setting.XorPayAidPrevious = originalXorPayPreviousAid
		setting.XorPayAppSecretPrevious = originalXorPayPreviousSecret
		setting.XorPayPreviousCredentialGeneration = originalXorPayPreviousGeneration
		setting.XorPayPreviousExpiresAt = originalXorPayPreviousExpiresAt
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	GetOptions(context)
	require.Equal(t, 200, recorder.Code)
	assert.NotContains(t, recorder.Body.String(), "whsec_must_not_leak")
	assert.NotContains(t, recorder.Body.String(), "epay_previous_secret_must_not_leak")
	assert.NotContains(t, recorder.Body.String(), "whsec_previous_secret_must_not_leak")
	assert.NotContains(t, recorder.Body.String(), "xorpay_previous_secret_must_not_leak")
	assert.Contains(t, recorder.Body.String(), "price_public_template")
	var response struct {
		Data []model.Option `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	optionValues := make(map[string]string, len(response.Data))
	for _, option := range response.Data {
		optionValues[option.Key] = option.Value
	}
	assert.Equal(t, "true", optionValues[paymentEpayPreviousCredentialActiveOptionKey])
	assert.Equal(t, "true", optionValues[paymentStripePreviousCredentialActiveOptionKey])
	assert.Equal(t, "false", optionValues[paymentStripeTestModeEnabledOptionKey])
	assert.Equal(t, "true", optionValues[paymentStripeTestModeBlockedOptionKey])
	assert.Equal(t, "true", optionValues[paymentStripeTestModeIsolationRequiredOptionKey])
	assert.Equal(t, stripe.APIVersion, optionValues[paymentStripeWebhookAPIVersionOptionKey])
	assert.Equal(t, "24", optionValues[paymentStripeWebhookSecretOverlapHoursOptionKey])
	assert.Equal(t, "true", optionValues[paymentXorPayPreviousCredentialActiveOptionKey])
}

func TestPaymentOrderPublicStatusHidesInternalSettlementStates(t *testing.T) {
	assert.Equal(t, common.TopUpStatusSuccess, paymentOrderPublicStatus(model.PaymentOrderStatusPaid))
	assert.Equal(t, common.TopUpStatusSuccess, paymentOrderPublicStatus(model.PaymentOrderStatusFulfilled))
	assert.Equal(t, model.PaymentOrderStatusRefundPending, paymentOrderPublicStatus(model.PaymentOrderStatusRefundPending))
}

func TestStripePreviousWebhookSecretExpiryCannotExceedRotationWindow(t *testing.T) {
	withinWindow := strconv.FormatInt(time.Now().Add(23*time.Hour).Unix(), 10)
	require.NoError(t, validatePaymentSettingValue("StripeWebhookSecretPreviousExpiresAt", withinWindow))

	beyondWindow := strconv.FormatInt(time.Now().Add(25*time.Hour).Unix(), 10)
	assert.Error(t, validatePaymentSettingValue("StripeWebhookSecretPreviousExpiresAt", beyondWindow))
}

func TestStripeWebhookSecretRotationPreservesPreviousFor24HoursAndBindsMode(t *testing.T) {
	originalCurrent := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalPreviousExpiry := setting.StripeWebhookSecretPreviousExpiresAt
	originalCredentialMode := setting.StripeCredentialLivemode
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	originalGeneration := setting.StripeWebhookCredentialGeneration
	originalPreviousGeneration := setting.StripeWebhookPreviousCredentialGeneration
	originalPreviousValidBefore := setting.StripeWebhookPreviousValidBefore
	t.Cleanup(func() {
		setting.StripeWebhookSecret = originalCurrent
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeWebhookSecretPreviousExpiresAt = originalPreviousExpiry
		setting.StripeCredentialLivemode = originalCredentialMode
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
		setting.StripeWebhookCredentialGeneration = originalGeneration
		setting.StripeWebhookPreviousCredentialGeneration = originalPreviousGeneration
		setting.StripeWebhookPreviousValidBefore = originalPreviousValidBefore
	})
	setting.StripeWebhookSecret = "whsec_current_rotation_secret"
	setting.StripeWebhookSecretPrevious = ""
	setting.StripeWebhookSecretPreviousExpiresAt = 0
	setting.StripeCredentialLivemode = "test"
	setting.StripeWebhookCredentialLivemode = "test"
	setting.StripeWebhookCredentialGeneration = 7
	setting.StripeWebhookPreviousCredentialGeneration = 0
	setting.StripeWebhookPreviousValidBefore = 0

	const now = int64(1_800_000_000)
	values := map[string]string{"StripeWebhookSecret": "whsec_new_rotation_secret"}
	revocations, err := preparePaymentCredentialRotations(values, nil, now)
	require.NoError(t, err)
	assert.Empty(t, revocations)
	assert.Equal(t, "whsec_current_rotation_secret", values["StripeWebhookSecretPrevious"])
	assert.Equal(t, strconv.FormatInt(now+int64(stripeWebhookSecretOverlap/time.Second), 10), values["StripeWebhookSecretPreviousExpiresAt"])
	assert.Equal(t, "test", values["StripeWebhookCredentialLivemode"])
	assert.Equal(t, "8", values["StripeWebhookCredentialGeneration"])
	assert.Equal(t, "7", values["StripeWebhookPreviousCredentialGeneration"])
	assert.Equal(t, strconv.FormatInt(now, 10), values["StripeWebhookPreviousValidBefore"])
}

func TestStripeWebhookSecretCannotRotateAgainDuringNormalOverlap(t *testing.T) {
	originalCurrent := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalPreviousExpiry := setting.StripeWebhookSecretPreviousExpiresAt
	originalCredentialMode := setting.StripeCredentialLivemode
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	originalGeneration := setting.StripeWebhookCredentialGeneration
	originalPreviousGeneration := setting.StripeWebhookPreviousCredentialGeneration
	originalPreviousValidBefore := setting.StripeWebhookPreviousValidBefore
	t.Cleanup(func() {
		setting.StripeWebhookSecret = originalCurrent
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeWebhookSecretPreviousExpiresAt = originalPreviousExpiry
		setting.StripeCredentialLivemode = originalCredentialMode
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
		setting.StripeWebhookCredentialGeneration = originalGeneration
		setting.StripeWebhookPreviousCredentialGeneration = originalPreviousGeneration
		setting.StripeWebhookPreviousValidBefore = originalPreviousValidBefore
	})
	setting.StripeWebhookSecret = "whsec_current_during_overlap"
	setting.StripeWebhookSecretPrevious = "whsec_previous_during_overlap"
	setting.StripeWebhookSecretPreviousExpiresAt = time.Now().Add(time.Hour).Unix()
	setting.StripeCredentialLivemode = "test"
	setting.StripeWebhookCredentialLivemode = "test"
	setting.StripeWebhookCredentialGeneration = 3
	setting.StripeWebhookPreviousCredentialGeneration = 2
	setting.StripeWebhookPreviousValidBefore = time.Now().Add(-time.Hour).Unix()

	_, err := preparePaymentCredentialRotations(
		map[string]string{"StripeWebhookSecret": "whsec_second_normal_rotation"}, nil, time.Now().Unix(),
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "overlap is still active")
}

func TestStripeWebhookEmergencyRotationRevokesOverlapAndBindsNewSecret(t *testing.T) {
	originalCurrent := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalPreviousExpiry := setting.StripeWebhookSecretPreviousExpiresAt
	originalCredentialMode := setting.StripeCredentialLivemode
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	originalGeneration := setting.StripeWebhookCredentialGeneration
	originalPreviousGeneration := setting.StripeWebhookPreviousCredentialGeneration
	originalPreviousValidBefore := setting.StripeWebhookPreviousValidBefore
	t.Cleanup(func() {
		setting.StripeWebhookSecret = originalCurrent
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeWebhookSecretPreviousExpiresAt = originalPreviousExpiry
		setting.StripeCredentialLivemode = originalCredentialMode
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
		setting.StripeWebhookCredentialGeneration = originalGeneration
		setting.StripeWebhookPreviousCredentialGeneration = originalPreviousGeneration
		setting.StripeWebhookPreviousValidBefore = originalPreviousValidBefore
	})
	setting.StripeWebhookSecret = "whsec_compromised_current"
	setting.StripeWebhookSecretPrevious = "whsec_compromised_previous"
	setting.StripeWebhookSecretPreviousExpiresAt = 1_800_003_600
	setting.StripeCredentialLivemode = "live"
	setting.StripeWebhookCredentialLivemode = "live"
	setting.StripeWebhookCredentialGeneration = 9
	setting.StripeWebhookPreviousCredentialGeneration = 8
	setting.StripeWebhookPreviousValidBefore = 1_799_999_000

	const now = int64(1_800_000_000)
	values := map[string]string{"StripeWebhookSecret": "whsec_emergency_replacement"}
	revocations, err := preparePaymentCredentialRotations(values, map[string]bool{model.PaymentProviderStripe: true}, now)
	require.NoError(t, err)
	require.Len(t, revocations, 2)
	assert.Equal(t, model.PaymentProviderStripe, revocations[0].Provider)
	assert.Equal(t, int64(9), revocations[0].Generation)
	assert.True(t, revocations[0].AllActiveOrders)
	assert.Equal(t, now, revocations[0].ValidBefore)
	assert.Equal(t, int64(8), revocations[1].Generation)
	assert.False(t, revocations[1].AllActiveOrders)
	assert.Equal(t, "", values["StripeWebhookSecretPrevious"])
	assert.Equal(t, "0", values["StripeWebhookSecretPreviousExpiresAt"])
	assert.Equal(t, "live", values["StripeWebhookCredentialLivemode"])
	assert.Equal(t, "10", values["StripeWebhookCredentialGeneration"])
	assert.Equal(t, "0", values["StripeWebhookPreviousCredentialGeneration"])
	assert.Equal(t, "0", values["StripeWebhookPreviousValidBefore"])
}

func TestStripeWebhookEmergencyRevocationWithoutReplacementDisablesWebhook(t *testing.T) {
	originalCurrent := setting.StripeWebhookSecret
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalPreviousExpiry := setting.StripeWebhookSecretPreviousExpiresAt
	originalCredentialMode := setting.StripeCredentialLivemode
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	originalGeneration := setting.StripeWebhookCredentialGeneration
	originalPreviousGeneration := setting.StripeWebhookPreviousCredentialGeneration
	originalPreviousValidBefore := setting.StripeWebhookPreviousValidBefore
	t.Cleanup(func() {
		setting.StripeWebhookSecret = originalCurrent
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeWebhookSecretPreviousExpiresAt = originalPreviousExpiry
		setting.StripeCredentialLivemode = originalCredentialMode
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
		setting.StripeWebhookCredentialGeneration = originalGeneration
		setting.StripeWebhookPreviousCredentialGeneration = originalPreviousGeneration
		setting.StripeWebhookPreviousValidBefore = originalPreviousValidBefore
	})
	setting.StripeWebhookSecret = "whsec_compromised_current"
	setting.StripeWebhookSecretPrevious = "whsec_compromised_previous"
	setting.StripeWebhookSecretPreviousExpiresAt = 1_800_003_600
	setting.StripeCredentialLivemode = "test"
	setting.StripeWebhookCredentialLivemode = "test"
	setting.StripeWebhookCredentialGeneration = 3
	setting.StripeWebhookPreviousCredentialGeneration = 2
	setting.StripeWebhookPreviousValidBefore = 1_799_999_000

	values := map[string]string{}
	revocations, err := preparePaymentCredentialRotations(values, map[string]bool{model.PaymentProviderStripe: true}, 1_800_000_000)
	require.NoError(t, err)
	require.Len(t, revocations, 2)
	assert.Equal(t, int64(3), revocations[0].Generation)
	assert.Equal(t, int64(2), revocations[1].Generation)
	assert.Equal(t, "", values["StripeWebhookSecret"])
	assert.Equal(t, "", values["StripeWebhookSecretPrevious"])
	assert.Equal(t, "0", values["StripeWebhookSecretPreviousExpiresAt"])
	assert.Equal(t, "", values["StripeWebhookCredentialLivemode"])
	assert.Equal(t, "4", values["StripeWebhookCredentialGeneration"])
	assert.Equal(t, "0", values["StripeWebhookPreviousCredentialGeneration"])
	assert.Equal(t, "0", values["StripeWebhookPreviousValidBefore"])
}

func TestStripeModeChangeRequiresNewWebhookSecretInSameUpdate(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalMode := setting.StripeCredentialLivemode
	originalWebhookSecret := setting.StripeWebhookSecret
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	t.Cleanup(func() {
		setting.StripeCredentialLivemode = originalMode
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
	})
	setting.StripeCredentialLivemode = "test"
	setting.StripeWebhookSecret = "whsec_test_domain"
	setting.StripeWebhookCredentialLivemode = "test"

	err := validateInFlightPaymentConfigurationChanges(map[string]string{
		"StripeCredentialLivemode": "live",
	}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "require a new webhook secret")

	values := map[string]string{
		"StripeCredentialLivemode": "live",
		"StripeWebhookSecret":      "whsec_live_domain_replacement",
	}
	_, err = preparePaymentCredentialRotations(values, nil, 1_800_000_000)
	require.NoError(t, err)
	require.NoError(t, validateInFlightPaymentConfigurationChanges(values, nil))
	assert.Equal(t, "live", values["StripeWebhookCredentialLivemode"])
	assert.Empty(t, values["StripeWebhookSecretPrevious"])
}

func TestStripeWebhookSecretCannotBeClearedNormallyWithHistoricalData(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalWebhookSecret := setting.StripeWebhookSecret
	setting.StripeWebhookSecret = "whsec_current_with_history"
	t.Cleanup(func() { setting.StripeWebhookSecret = originalWebhookSecret })

	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.PaymentOrder{
		TradeNo: "payment-settings-stripe-webhook-history", UserID: 990008,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-stripe-webhook-history",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusFulfilled, CreatedAt: now, UpdatedAt: now, Version: 1,
	}).Error)

	values := map[string]string{"StripeWebhookSecret": ""}
	err := validateInFlightPaymentConfigurationChanges(values, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "emergency credential revocation")
	preconditions := paymentConfigurationPreconditions(values, nil)
	assert.True(t, preconditions.RequireNoStripeHistory)

	require.NoError(t, validateInFlightPaymentConfigurationChanges(values, map[string]bool{model.PaymentProviderStripe: true}))
}

func TestStripeWebhookSecretCanBeClearedNormallyWithoutHistoricalData(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalWebhookSecret := setting.StripeWebhookSecret
	setting.StripeWebhookSecret = "whsec_current_without_history"
	t.Cleanup(func() { setting.StripeWebhookSecret = originalWebhookSecret })

	values := map[string]string{"StripeWebhookSecret": ""}
	require.NoError(t, validateInFlightPaymentConfigurationChanges(values, nil))
	assert.True(t, paymentConfigurationPreconditions(values, nil).RequireNoStripeHistory)
}

func TestPaymentAtomicOnlyOptionKeysPreserveSharedSettingPaths(t *testing.T) {
	assert.False(t, isPaymentAtomicOnlyOptionKey("ServerAddress"))
	assert.True(t, isPaymentAtomicOnlyOptionKey("TopupGroupRatio"))
	assert.True(t, isPaymentAtomicOnlyOptionKey("EpayKey"))
	assert.True(t, isPaymentAtomicOnlyOptionKey("StripeApiSecret"))
	assert.True(t, isPaymentAtomicOnlyOptionKey("StripeCheckoutAllowedHosts"))
	assert.True(t, isPaymentAtomicOnlyOptionKey("XorPayAppSecret"))
	assert.True(t, isPaymentAtomicOnlyOptionKey("WaffoPancakePrivateKey"))
	assert.True(t, isPaymentAtomicOnlyOptionKey("WaffoPancakeStoreID"))
	assert.True(t, isPaymentAtomicOnlyOptionKey("StripeCredentialAccountId"))
	assert.True(t, isPaymentAtomicOnlyOptionKey("StripeCredentialLivemode"))
	assert.True(t, isPaymentAtomicOnlyOptionKey("StripeWebhookCredentialLivemode"))
	assert.True(t, isPaymentAtomicOnlyOptionKey(paymentEpayPreviousCredentialActiveOptionKey))
	assert.True(t, isPaymentAtomicOnlyOptionKey(paymentStripePreviousCredentialActiveOptionKey))
	assert.True(t, isPaymentAtomicOnlyOptionKey(paymentStripeTestModeEnabledOptionKey))
	assert.True(t, isPaymentAtomicOnlyOptionKey(paymentStripeTestModeBlockedOptionKey))
	assert.True(t, isPaymentAtomicOnlyOptionKey(paymentStripeTestModeIsolationRequiredOptionKey))
	assert.True(t, isPaymentAtomicOnlyOptionKey(paymentXorPayPreviousCredentialActiveOptionKey))
	_, previousWebhookSecretClientWritable := paymentSettingsAllowedKeys["StripeWebhookSecretPrevious"]
	assert.False(t, previousWebhookSecretClientWritable)
	assert.True(t, isPaymentAtomicOnlyOptionKey(model.PaymentConfigurationVersionOptionKey))
	assert.False(t, isPaymentAtomicOnlyOptionKey("SystemName"))
}

func TestStripeTestSettingsMutationRequiresExplicitSandboxEnablement(t *testing.T) {
	t.Setenv(setting.StripeTestModeEnabledEnv, "false")
	err := validateStripeSandboxSettingsMutation(map[string]string{
		"StripeWebhookSecret": "whsec_test_replacement",
	}, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "isolated sandbox")
	require.NoError(t, validateStripeSandboxSettingsMutation(map[string]string{
		"StripeApiSecret": "",
		"StripePriceId":   "",
	}, "test"))
	require.NoError(t, validateStripeSandboxSettingsMutation(map[string]string{
		"StripeWebhookSecret": "whsec_live_replacement",
	}, "live"))

	t.Setenv(setting.StripeTestModeEnabledEnv, "true")
	require.NoError(t, validateStripeSandboxSettingsMutation(map[string]string{
		"StripeWebhookSecret": "whsec_test_replacement",
	}, "test"))
}

func TestStripeCredentialAccountCannotChangeAfterPaymentHistoryExists(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalAccountID := setting.StripeCredentialAccountId
	setting.StripeCredentialAccountId = "acct_original123"
	t.Cleanup(func() { setting.StripeCredentialAccountId = originalAccountID })

	order := &model.PaymentOrder{
		TradeNo: "payment-settings-stripe-account-history", UserID: 990002,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-stripe-account-history",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusFulfilled, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), Version: 1,
	}
	require.NoError(t, db.Create(order).Error)
	t.Cleanup(func() { db.Delete(&model.PaymentOrder{}, order.ID) })

	err := validateInFlightPaymentConfigurationChanges(map[string]string{
		"StripeCredentialAccountId": "acct_different456",
	}, nil)
	assert.Error(t, err)
}

func TestStripeCredentialCannotChangeBeforeHistoricalAccountIsBound(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalSecret := setting.StripeApiSecret
	originalAccountID := setting.StripeCredentialAccountId
	setting.StripeApiSecret = "sk_test_existing_credential"
	setting.StripeCredentialAccountId = ""
	t.Cleanup(func() {
		setting.StripeApiSecret = originalSecret
		setting.StripeCredentialAccountId = originalAccountID
	})

	order := &model.PaymentOrder{
		TradeNo: "payment-settings-stripe-unbound-history", UserID: 990003,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-stripe-unbound-history",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusFulfilled, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), Version: 1,
	}
	require.NoError(t, db.Create(order).Error)

	err := validateInFlightPaymentConfigurationChanges(map[string]string{
		"StripeApiSecret": "sk_test_different_credential",
	}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "credential account and test/live mode are verified and bound")
}

func TestStripeAPIKeyCanRotateWithinTheBoundAccountWithActiveOrders(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalSecret := setting.StripeApiSecret
	originalAccountID := setting.StripeCredentialAccountId
	originalMode := setting.StripeCredentialLivemode
	setting.StripeApiSecret = "sk_test_existing_bound_credential"
	setting.StripeCredentialAccountId = "acct_bound_same_account"
	setting.StripeCredentialLivemode = "test"
	t.Cleanup(func() {
		setting.StripeApiSecret = originalSecret
		setting.StripeCredentialAccountId = originalAccountID
		setting.StripeCredentialLivemode = originalMode
	})
	require.NoError(t, db.Create(&model.PaymentOrder{
		TradeNo: "payment-settings-stripe-active-key-rotation", UserID: 990005,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-stripe-active-key-rotation",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusProcessing, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), Version: 1,
	}).Error)

	err := validateInFlightPaymentConfigurationChanges(map[string]string{
		"StripeApiSecret":           "sk_test_rotated_same_account",
		"StripeCredentialAccountId": "acct_bound_same_account",
		"StripeCredentialLivemode":  "test",
	}, nil)
	require.NoError(t, err)
}

func TestStripeAPISecretCannotBeClearedWithDurableHistory(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalSecret := setting.StripeApiSecret
	setting.StripeApiSecret = "sk_test_durable_checkout_history"
	t.Cleanup(func() { setting.StripeApiSecret = originalSecret })
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.PaymentOrder{
		TradeNo: "payment-settings-stripe-expired-checkout-history", UserID: 990009,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-stripe-expired-checkout-history",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusExpired, CreatedAt: now - 3600, UpdatedAt: now, Version: 1,
	}).Error)

	values := map[string]string{"StripeApiSecret": ""}
	err := validateInFlightPaymentConfigurationChanges(values, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "durable Stripe data")
	assert.True(t, paymentConfigurationPreconditions(values, nil).RequireNoStripeHistory)
}

func TestStripeAPISecretCanBeClearedWithoutHistory(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalSecret := setting.StripeApiSecret
	setting.StripeApiSecret = "sk_test_no_checkout_history"
	t.Cleanup(func() { setting.StripeApiSecret = originalSecret })

	values := map[string]string{"StripeApiSecret": ""}
	require.NoError(t, validateInFlightPaymentConfigurationChanges(values, nil))
	assert.True(t, paymentConfigurationPreconditions(values, nil).RequireNoStripeHistory)
}

func TestStripeAPISecretEmergencyClearCanQuarantineHistory(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalSecret := setting.StripeApiSecret
	setting.StripeApiSecret = "sk_test_emergency_checkout_history"
	t.Cleanup(func() { setting.StripeApiSecret = originalSecret })
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.PaymentOrder{
		TradeNo: "payment-settings-stripe-emergency-checkout-history", UserID: 990011,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-stripe-emergency-checkout-history",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusProcessing, CreatedAt: now, UpdatedAt: now, Version: 1,
	}).Error)

	values := map[string]string{"StripeApiSecret": ""}
	emergency := map[string]bool{model.PaymentProviderStripe: true}
	require.NoError(t, validateInFlightPaymentConfigurationChanges(values, emergency))
	assert.False(t, paymentConfigurationPreconditions(values, emergency).RequireNoStripeHistory)
}

func TestStripeEmergencyRevocationCannotReplaceUnboundHistoricalAccount(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalSecret := setting.StripeApiSecret
	originalAccountID := setting.StripeCredentialAccountId
	originalMode := setting.StripeCredentialLivemode
	setting.StripeApiSecret = "sk_test_unbound_historical_account"
	setting.StripeCredentialAccountId = ""
	setting.StripeCredentialLivemode = ""
	t.Cleanup(func() {
		setting.StripeApiSecret = originalSecret
		setting.StripeCredentialAccountId = originalAccountID
		setting.StripeCredentialLivemode = originalMode
	})
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.PaymentOrder{
		TradeNo: "payment-settings-stripe-unbound-emergency-replacement", UserID: 990016,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-stripe-unbound-emergency-replacement",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusExpired, CreatedAt: now - 3600, UpdatedAt: now, Version: 1,
	}).Error)

	values := map[string]string{"StripeApiSecret": "test-stripe-potentially-different-account"}
	emergency := map[string]bool{model.PaymentProviderStripe: true}
	err := validateInFlightPaymentConfigurationChanges(values, emergency)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "historical Stripe data")
	assert.True(t, paymentConfigurationPreconditions(values, emergency).RequireNoStripeHistory)
}

func TestStripePriceCatalogCanBeDisabledWithActiveOrders(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalPriceID := setting.StripePriceId
	setting.StripePriceId = "price_active_checkout_catalog"
	t.Cleanup(func() { setting.StripePriceId = originalPriceID })
	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.PaymentOrder{
		TradeNo: "payment-settings-stripe-active-checkout-catalog", UserID: 990012,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-stripe-active-checkout-catalog",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusManualReview, CreatedAt: now, UpdatedAt: now, Version: 1,
	}).Error)

	values := map[string]string{"StripePriceId": ""}
	require.NoError(t, validateInFlightPaymentConfigurationChanges(values, nil))
	preconditions := paymentConfigurationPreconditions(values, nil)
	assert.False(t, preconditions.RequireNoStripeHistory)
}

func TestPaymentCallbackOriginCannotChangeWithActiveGatewayOrders(t *testing.T) {
	for _, test := range []struct {
		provider string
		method   string
		currency string
	}{
		{provider: model.PaymentProviderEpay, method: "alipay", currency: "CNY"},
		{provider: model.PaymentProviderStripe, method: model.PaymentMethodStripe, currency: "USD"},
		{provider: model.PaymentProviderXorPay, method: model.PaymentMethodXorPayNative, currency: "CNY"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			db := setupMidjourneyControllerBillingDB(t)
			require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
			originalCallback := operation_setting.CustomCallbackAddress
			operation_setting.CustomCallbackAddress = "https://payments-old.example.com"
			t.Cleanup(func() { operation_setting.CustomCallbackAddress = originalCallback })

			now := time.Now().Unix()
			require.NoError(t, db.Create(&model.PaymentOrder{
				TradeNo: "payment-settings-callback-" + test.provider, UserID: 990010,
				OrderKind: model.PaymentOrderKindTopUp, Provider: test.provider,
				PaymentMethod: test.method, RequestID: "payment-settings-callback-" + test.provider,
				ExpectedAmountMinor: 100, Currency: test.currency, RequestedAmount: 1, CreditQuota: 1,
				Status: model.PaymentOrderStatusProcessing, CreatedAt: now, UpdatedAt: now, Version: 1,
			}).Error)

			values := map[string]string{"CustomCallbackAddress": "https://payments-new.example.com"}
			err := validateInFlightPaymentConfigurationChanges(values, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.provider)
			assert.True(t, paymentConfigurationPreconditions(values, nil).RequireNoCallbackDependentOrders)
		})
	}
}

func TestPaymentCallbackOriginCannotChangeDuringLateSettlementRecovery(t *testing.T) {
	for _, test := range []struct {
		provider string
		method   string
		status   string
	}{
		{provider: model.PaymentProviderEpay, method: "alipay", status: model.PaymentOrderStatusFailed},
		{provider: model.PaymentProviderXorPay, method: model.PaymentMethodXorPayNative, status: model.PaymentOrderStatusExpired},
	} {
		t.Run(test.provider, func(t *testing.T) {
			db := setupMidjourneyControllerBillingDB(t)
			require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}, &model.TopUp{}, &model.SubscriptionOrder{}))
			originalCallback := operation_setting.CustomCallbackAddress
			operation_setting.CustomCallbackAddress = "https://payments-old.example.com"
			t.Cleanup(func() { operation_setting.CustomCallbackAddress = originalCallback })

			now := time.Now().Unix()
			require.NoError(t, db.Create(&model.PaymentOrder{
				TradeNo: "payment-settings-recoverable-callback-" + test.provider, UserID: 990013,
				OrderKind: model.PaymentOrderKindTopUp, Provider: test.provider,
				PaymentMethod: test.method, RequestID: "payment-settings-recoverable-callback-" + test.provider,
				ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 1,
				Status: test.status, StartedAt: now - 3600, ExpiresAt: now - 1,
				CreatedAt: now - 3600, UpdatedAt: now, Version: 1,
			}).Error)

			values := map[string]string{"CustomCallbackAddress": "https://payments-new.example.com"}
			err := validateInFlightPaymentConfigurationChanges(values, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.provider)
		})
	}
}

func TestPaymentCallbackOriginRecoveryWindowIsFinite(t *testing.T) {
	for _, provider := range []string{model.PaymentProviderEpay, model.PaymentProviderXorPay} {
		t.Run(provider, func(t *testing.T) {
			db := setupMidjourneyControllerBillingDB(t)
			require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}, &model.TopUp{}, &model.SubscriptionOrder{}))
			originalCallback := operation_setting.CustomCallbackAddress
			operation_setting.CustomCallbackAddress = "https://payments-old.example.com"
			t.Cleanup(func() { operation_setting.CustomCallbackAddress = originalCallback })

			now := time.Now().Unix()
			outsideWindow := now - int64(model.PaymentCallbackRecoveryWindow/time.Second) - 1
			require.NoError(t, db.Create(&model.PaymentOrder{
				TradeNo: "payment-settings-old-callback-" + provider, UserID: 990014,
				OrderKind: model.PaymentOrderKindTopUp, Provider: provider,
				PaymentMethod: "alipay", RequestID: "payment-settings-old-callback-" + provider,
				ExpectedAmountMinor: 100, Currency: "CNY", RequestedAmount: 1, CreditQuota: 1,
				Status: model.PaymentOrderStatusExpired, ExpiresAt: outsideWindow,
				CreatedAt: outsideWindow, UpdatedAt: outsideWindow, Version: 1,
			}).Error)

			values := map[string]string{"CustomCallbackAddress": "https://payments-new.example.com"}
			require.NoError(t, validateInFlightPaymentConfigurationChanges(values, nil))
		})
	}
}

func TestStripeExpiredOrdersDoNotPinCallbackOrigin(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}, &model.TopUp{}, &model.SubscriptionOrder{}))
	originalCallback := operation_setting.CustomCallbackAddress
	operation_setting.CustomCallbackAddress = "https://payments-old.example.com"
	t.Cleanup(func() { operation_setting.CustomCallbackAddress = originalCallback })

	now := time.Now().Unix()
	require.NoError(t, db.Create(&model.PaymentOrder{
		TradeNo: "payment-settings-expired-stripe-return", UserID: 990015,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-expired-stripe-return",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusExpired, ExpiresAt: now - 1, CreatedAt: now - 3600, UpdatedAt: now, Version: 1,
	}).Error)

	values := map[string]string{"CustomCallbackAddress": "https://payments-new.example.com"}
	require.NoError(t, validateInFlightPaymentConfigurationChanges(values, nil))
}

func TestStripeCheckoutAllowedHostsCannotBeRemovedWhileOrdersAreActive(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalAllowedHosts := setting.StripeCheckoutAllowedHosts
	setting.StripeCheckoutAllowedHosts = "checkout.example.net,pay.example.com"
	t.Cleanup(func() { setting.StripeCheckoutAllowedHosts = originalAllowedHosts })

	now := time.Now().Unix()
	order := &model.PaymentOrder{
		TradeNo: "payment-settings-stripe-host-policy", UserID: 990016,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-stripe-host-policy",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusPending, ExpiresAt: now + 600, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	require.NoError(t, db.Create(order).Error)

	require.NoError(t, validateInFlightPaymentConfigurationChanges(map[string]string{
		"StripeCheckoutAllowedHosts": "checkout.example.net,new.example.com,pay.example.com",
	}, nil))
	assert.False(t, paymentConfigurationPreconditions(map[string]string{
		"StripeCheckoutAllowedHosts": "checkout.example.net,new.example.com,pay.example.com",
	}, nil).RequireNoActiveStripeOrdersForHostRemoval)
	err := validateInFlightPaymentConfigurationChanges(map[string]string{
		"StripeCheckoutAllowedHosts": "checkout.example.net",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be removed")
	assert.True(t, paymentConfigurationPreconditions(map[string]string{
		"StripeCheckoutAllowedHosts": "checkout.example.net",
	}, nil).RequireNoActiveStripeOrdersForHostRemoval)

	require.NoError(t, db.Model(order).Update("status", model.PaymentOrderStatusExpired).Error)
	require.NoError(t, validateInFlightPaymentConfigurationChanges(map[string]string{
		"StripeCheckoutAllowedHosts": "checkout.example.net",
	}, nil))
}

func TestPaymentCallbackOriginCanChangeWithoutActiveGatewayOrders(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalCallback := operation_setting.CustomCallbackAddress
	operation_setting.CustomCallbackAddress = "https://payments-old.example.com"
	t.Cleanup(func() { operation_setting.CustomCallbackAddress = originalCallback })

	values := map[string]string{"CustomCallbackAddress": "https://payments-new.example.com"}
	require.NoError(t, validateInFlightPaymentConfigurationChanges(values, nil))
	assert.True(t, paymentConfigurationPreconditions(values, nil).RequireNoCallbackDependentOrders)
}

func TestStripeCredentialCannotCrossTestAndLiveModesWithHistoricalData(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	require.NoError(t, db.AutoMigrate(&model.PaymentOrder{}))
	originalAccountID := setting.StripeCredentialAccountId
	originalMode := setting.StripeCredentialLivemode
	setting.StripeCredentialAccountId = "acct_bound_mode"
	setting.StripeCredentialLivemode = "test"
	t.Cleanup(func() {
		setting.StripeCredentialAccountId = originalAccountID
		setting.StripeCredentialLivemode = originalMode
	})
	require.NoError(t, db.Create(&model.PaymentOrder{
		TradeNo: "payment-settings-stripe-mode-history", UserID: 990006,
		OrderKind: model.PaymentOrderKindTopUp, Provider: model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe, RequestID: "payment-settings-stripe-mode-history",
		ExpectedAmountMinor: 100, Currency: "USD", RequestedAmount: 1, CreditQuota: 1,
		Status: model.PaymentOrderStatusFulfilled, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(), Version: 1,
	}).Error)

	err := validateInFlightPaymentConfigurationChanges(map[string]string{
		"StripeApiSecret":           "sk_live_different_mode",
		"StripeCredentialAccountId": "acct_bound_mode",
		"StripeCredentialLivemode":  "live",
	}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "durable Stripe data")
}

func TestStripeAccountCannotChangeWhenOnlyLegacyCustomerBindingExists(t *testing.T) {
	db := setupMidjourneyControllerBillingDB(t)
	originalAccountID := setting.StripeAccountId
	setting.StripeAccountId = "acct_connected_original"
	t.Cleanup(func() { setting.StripeAccountId = originalAccountID })
	require.NoError(t, db.Create(&model.User{
		Id: 990004, Username: "stripe_legacy_account_binding", StripeCustomer: "cus_legacy_account_binding",
		Status: common.UserStatusEnabled, Group: "default",
	}).Error)

	err := validateInFlightPaymentConfigurationChanges(map[string]string{
		"StripeAccountId": "acct_connected_different",
	}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "durable Stripe data")
}
