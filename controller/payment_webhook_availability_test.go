package controller

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

func confirmPaymentComplianceForTest(t *testing.T) {
	t.Helper()
	t.Setenv("PAYMENT_SECRET_KEY", "test-payment-secret-key-at-least-32-bytes")
	paymentSetting := operation_setting.GetPaymentSetting()
	originalConfirmed := paymentSetting.ComplianceConfirmed
	originalTermsVersion := paymentSetting.ComplianceTermsVersion
	t.Cleanup(func() {
		paymentSetting.ComplianceConfirmed = originalConfirmed
		paymentSetting.ComplianceTermsVersion = originalTermsVersion
	})
	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
}

func TestStripeWebhookRemainsEnabledWhenNewCheckoutCreationIsDisabled(t *testing.T) {
	setupMidjourneyControllerBillingDB(t)
	confirmPaymentComplianceForTest(t)
	t.Setenv(setting.StripeTestModeEnabledEnv, "")
	originalAPISecret := setting.StripeApiSecret
	originalWebhookSecret := setting.StripeWebhookSecret
	originalPriceID := setting.StripePriceId
	originalPrevious := setting.StripeWebhookSecretPrevious
	originalPreviousExpiry := setting.StripeWebhookSecretPreviousExpiresAt
	originalWebhookMode := setting.StripeWebhookCredentialLivemode
	originalCredentialMode := setting.StripeCredentialLivemode
	originalCredentialAccountID := setting.StripeCredentialAccountId
	originalConnectedAccountID := setting.StripeAccountId
	originalCurrency := setting.StripeCurrency
	originalFingerprint := setting.StripeConfigurationVerifiedFingerprint
	originalVerifiedAt := setting.StripeConfigurationVerifiedAt
	originalCallbackAddress := operation_setting.CustomCallbackAddress
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripePriceId = originalPriceID
		setting.StripeWebhookSecretPrevious = originalPrevious
		setting.StripeWebhookSecretPreviousExpiresAt = originalPreviousExpiry
		setting.StripeWebhookCredentialLivemode = originalWebhookMode
		setting.StripeCredentialLivemode = originalCredentialMode
		setting.StripeCredentialAccountId = originalCredentialAccountID
		setting.StripeAccountId = originalConnectedAccountID
		setting.StripeCurrency = originalCurrency
		setting.StripeConfigurationVerifiedFingerprint = originalFingerprint
		setting.StripeConfigurationVerifiedAt = originalVerifiedAt
		operation_setting.CustomCallbackAddress = originalCallbackAddress
	})

	setting.StripeWebhookSecret = ""
	setting.StripeApiSecret = "sk_test_123"
	setting.StripePriceId = "price_123"
	setting.StripeCredentialLivemode = "test"
	setting.StripeCredentialAccountId = "acct_test123"
	setting.StripeAccountId = ""
	setting.StripeCurrency = "USD"
	operation_setting.CustomCallbackAddress = "https://payments.example.com"
	setting.StripeConfigurationVerifiedFingerprint = service.StripeCheckoutConfigurationFingerprint(
		setting.StripeApiSecret, setting.StripeCredentialAccountId, setting.StripeAccountId,
		setting.StripePriceId, setting.StripeCurrency, setting.StripeCredentialLivemode,
	)
	setting.StripeConfigurationVerifiedAt = time.Now().Unix()
	setting.StripeWebhookCredentialLivemode = ""
	require.False(t, isStripeWebhookEnabled())

	setting.StripeWebhookSecret = "whsec_test"
	require.False(t, isStripeWebhookEnabled())
	require.False(t, isStripeTopUpEnabled())
	setting.StripeWebhookCredentialLivemode = "test"
	require.True(t, isStripeWebhookEnabled())
	require.False(t, isStripeTopUpEnabled())
	stripeReadiness := paymentGatewayReadinessLocked()["stripe"].(map[string]interface{})
	require.Equal(t, false, stripeReadiness["test_mode_enabled"])
	require.Equal(t, true, stripeReadiness["test_mode_blocked"])

	t.Setenv(setting.StripeTestModeEnabledEnv, "true")
	require.True(t, isStripeTopUpEnabled())
	stripeReadiness = paymentGatewayReadinessLocked()["stripe"].(map[string]interface{})
	require.Equal(t, true, stripeReadiness["test_mode_enabled"])
	require.Equal(t, false, stripeReadiness["test_mode_blocked"])

	operation_setting.CustomCallbackAddress = "https://payments.example.com/base"
	require.True(t, isStripeWebhookEnabled())
	require.False(t, isStripeTopUpEnabled())
	operation_setting.CustomCallbackAddress = "https://payments.example.com"

	setting.StripePriceId = ""
	require.True(t, isStripeWebhookEnabled())
	require.False(t, isStripeTopUpEnabled())

	setting.StripeWebhookSecret = ""
	setting.StripeWebhookSecretPrevious = "whsec_previous"
	setting.StripeWebhookSecretPreviousExpiresAt = time.Now().Add(time.Hour).Unix()
	require.True(t, isStripeWebhookEnabled())
	setting.StripeWebhookSecretPreviousExpiresAt = time.Now().Add(-time.Hour).Unix()
	require.False(t, isStripeWebhookEnabled())
}

func TestCreemWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalAPIKey := setting.CreemApiKey
	originalProducts := setting.CreemProducts
	originalWebhookSecret := setting.CreemWebhookSecret
	t.Cleanup(func() {
		setting.CreemApiKey = originalAPIKey
		setting.CreemProducts = originalProducts
		setting.CreemWebhookSecret = originalWebhookSecret
	})

	setting.CreemWebhookSecret = ""
	setting.CreemApiKey = "creem_api_key"
	setting.CreemProducts = `[{"productId":"prod_123"}]`
	require.False(t, isCreemWebhookEnabled())

	setting.CreemWebhookSecret = "creem_secret"
	require.True(t, isCreemWebhookEnabled())

	setting.CreemProducts = "[]"
	require.False(t, isCreemWebhookEnabled())
}

func TestWaffoWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalEnabled := setting.WaffoEnabled
	originalSandbox := setting.WaffoSandbox
	originalAPIKey := setting.WaffoApiKey
	originalPrivateKey := setting.WaffoPrivateKey
	originalPublicCert := setting.WaffoPublicCert
	originalSandboxAPIKey := setting.WaffoSandboxApiKey
	originalSandboxPrivateKey := setting.WaffoSandboxPrivateKey
	originalSandboxPublicCert := setting.WaffoSandboxPublicCert
	t.Cleanup(func() {
		setting.WaffoEnabled = originalEnabled
		setting.WaffoSandbox = originalSandbox
		setting.WaffoApiKey = originalAPIKey
		setting.WaffoPrivateKey = originalPrivateKey
		setting.WaffoPublicCert = originalPublicCert
		setting.WaffoSandboxApiKey = originalSandboxAPIKey
		setting.WaffoSandboxPrivateKey = originalSandboxPrivateKey
		setting.WaffoSandboxPublicCert = originalSandboxPublicCert
	})

	setting.WaffoEnabled = true
	setting.WaffoSandbox = false
	setting.WaffoApiKey = ""
	setting.WaffoPrivateKey = "private"
	setting.WaffoPublicCert = "public"
	require.False(t, isWaffoWebhookEnabled())

	setting.WaffoApiKey = "api"
	require.True(t, isWaffoWebhookEnabled())

	setting.WaffoEnabled = false
	require.False(t, isWaffoWebhookEnabled())

	setting.WaffoEnabled = true
	setting.WaffoSandbox = true
	setting.WaffoSandboxApiKey = ""
	setting.WaffoSandboxPrivateKey = "sandbox_private"
	setting.WaffoSandboxPublicCert = "sandbox_public"
	require.False(t, isWaffoWebhookEnabled())

	setting.WaffoSandboxApiKey = "sandbox_api"
	require.True(t, isWaffoWebhookEnabled())
}

func TestWaffoPancakeWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalMerchantID := setting.WaffoPancakeMerchantID
	originalPrivateKey := setting.WaffoPancakePrivateKey
	originalProductID := setting.WaffoPancakeProductID
	t.Cleanup(func() {
		setting.WaffoPancakeMerchantID = originalMerchantID
		setting.WaffoPancakePrivateKey = originalPrivateKey
		setting.WaffoPancakeProductID = originalProductID
	})

	// Presence of all three credentials enables the gateway. Webhook public
	// keys are bundled in the SDK and there is no separate Enabled toggle —
	// clear any of the three fields to disable.
	setting.WaffoPancakeMerchantID = ""
	setting.WaffoPancakePrivateKey = "private"
	setting.WaffoPancakeProductID = "product"
	require.False(t, isWaffoPancakeWebhookEnabled())

	setting.WaffoPancakeMerchantID = "merchant"
	require.True(t, isWaffoPancakeWebhookEnabled())

	setting.WaffoPancakeProductID = ""
	require.False(t, isWaffoPancakeWebhookEnabled())

	setting.WaffoPancakeProductID = "product"
	setting.WaffoPancakePrivateKey = ""
	require.False(t, isWaffoPancakeWebhookEnabled())
}

func TestEpayWebhookRemainsEnabledWhenNewCheckoutCreationIsDisabled(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalPayAddress := operation_setting.PayAddress
	originalEpayID := operation_setting.EpayId
	originalEpayKey := operation_setting.EpayKey
	originalPayMethods := operation_setting.PayMethods
	t.Cleanup(func() {
		operation_setting.PayAddress = originalPayAddress
		operation_setting.EpayId = originalEpayID
		operation_setting.EpayKey = originalEpayKey
		operation_setting.PayMethods = originalPayMethods
	})

	operation_setting.PayAddress = "https://pay.example.com"
	operation_setting.EpayId = "epay_id"
	operation_setting.EpayKey = ""
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}
	require.False(t, isEpayWebhookEnabled())

	operation_setting.EpayKey = "epay_key"
	require.True(t, isEpayWebhookEnabled())

	operation_setting.PayMethods = nil
	require.True(t, isEpayWebhookEnabled())
	require.False(t, isEpayTopUpEnabled())

	operation_setting.PayAddress = ""
	require.True(t, isEpayWebhookEnabled())
}

func TestXorPayWebhookRemainsEnabledWhenNewCheckoutCreationIsDisabled(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalAid := setting.XorPayAid
	originalSecret := setting.XorPayAppSecret
	originalMethods := setting.XorPayEnabledMethods
	t.Cleanup(func() {
		setting.XorPayAid = originalAid
		setting.XorPayAppSecret = originalSecret
		setting.XorPayEnabledMethods = originalMethods
	})
	setting.XorPayAid = "aid_test"
	setting.XorPayAppSecret = "xorpay_secret"
	setting.XorPayEnabledMethods = nil
	require.True(t, isXorPayWebhookEnabled())
	require.False(t, isXorPayTopUpEnabled())
}
