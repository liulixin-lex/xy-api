package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func isPaymentComplianceConfirmed() bool {
	return readPaymentConfiguration(isPaymentComplianceConfirmedLocked)
}

func isStripeTopUpEnabled() bool {
	return readPaymentConfiguration(isStripeTopUpEnabledLocked)
}

func isStripeTopUpEnabledLocked() bool {
	if !isPaymentComplianceConfirmedLocked() || !model.PaymentSecretStorageReady() {
		return false
	}
	if service.ValidatePaymentCallbackOrigin(operation_setting.CustomCallbackAddress, true) != nil {
		return false
	}
	verifiedFingerprint := service.StripeCheckoutConfigurationFingerprint(
		setting.StripeApiSecret, setting.StripeCredentialAccountId, setting.StripeAccountId,
		setting.StripePriceId, setting.StripeCurrency, setting.StripeCredentialLivemode,
		setting.StripeCheckoutAllowedHosts,
	)
	return verifiedFingerprint != "" && setting.StripeConfigurationVerifiedFingerprint == verifiedFingerprint &&
		setting.StripeConfigurationVerifiedAt > 0 && strings.TrimSpace(setting.StripeApiSecret) != "" &&
		strings.TrimSpace(operation_setting.CustomCallbackAddress) != "" &&
		strings.TrimSpace(setting.StripeWebhookSecret) != "" &&
		strings.TrimSpace(setting.StripePriceId) != "" &&
		strings.TrimSpace(setting.StripeCredentialAccountId) != "" &&
		(setting.StripeCredentialLivemode == "test" || setting.StripeCredentialLivemode == "live") &&
		setting.StripeCredentialModeAllowed(setting.StripeCredentialLivemode) &&
		setting.StripeWebhookCredentialLivemode == setting.StripeCredentialLivemode &&
		service.ValidatePaymentPricingForOrderKind(model.PaymentProviderStripe, model.PaymentOrderKindTopUp) == nil
}

func isStripeWebhookConfigured() bool {
	return readPaymentConfiguration(isStripeWebhookConfiguredLocked)
}

func isStripeWebhookConfiguredLocked() bool {
	return (setting.StripeWebhookCredentialLivemode == "test" || setting.StripeWebhookCredentialLivemode == "live") &&
		(strings.TrimSpace(setting.StripeWebhookSecret) != "" || setting.StripePreviousWebhookSecretActive())
}

func isStripeWebhookEnabled() bool {
	return readPaymentConfiguration(isStripeWebhookConfiguredLocked)
}

func isCreemTopUpEnabled() bool {
	return readPaymentConfiguration(isCreemTopUpEnabledLocked)
}

func isCreemTopUpEnabledLocked() bool {
	if !isCreemCheckoutEnabledLocked() {
		return false
	}
	products := strings.TrimSpace(setting.CreemProducts)
	if strings.TrimSpace(setting.CreemApiKey) == "" || products == "" || products == "[]" {
		return false
	}
	parsed, err := parseValidatedCreemProducts(products)
	return err == nil && len(parsed) > 0
}

func isCreemCheckoutEnabledLocked() bool {
	return isPaymentComplianceConfirmedLocked() && model.PaymentSecretStorageReady() &&
		service.ValidatePaymentCallbackOrigin(operation_setting.CustomCallbackAddress, true) == nil &&
		strings.TrimSpace(setting.CreemApiKey) != "" &&
		strings.TrimSpace(setting.CreemWebhookSecret) != ""
}

func isCreemWebhookConfigured() bool {
	return readPaymentConfiguration(isCreemWebhookConfiguredLocked)
}

func isCreemWebhookConfiguredLocked() bool {
	return strings.TrimSpace(setting.CreemWebhookSecret) != ""
}

func isCreemWebhookEnabled() bool {
	// Keep accepting delayed callbacks even when new checkout creation has been
	// disabled by clearing the API key or product catalog.
	return readPaymentConfiguration(isCreemWebhookConfiguredLocked)
}

func isWaffoTopUpEnabled() bool {
	return readPaymentConfiguration(isWaffoTopUpEnabledLocked)
}

func isWaffoTopUpEnabledLocked() bool {
	if !isPaymentComplianceConfirmedLocked() || !model.PaymentSecretStorageReady() {
		return false
	}
	if service.ValidatePaymentCallbackOrigin(operation_setting.CustomCallbackAddress, true) != nil ||
		service.ValidatePaymentPricingForOrderKind(model.PaymentProviderWaffo, model.PaymentOrderKindTopUp) != nil {
		return false
	}
	if !setting.WaffoEnabled {
		return false
	}
	if setting.WaffoSandbox {
		return strings.TrimSpace(setting.WaffoSandboxApiKey) != "" && isWaffoWebhookConfiguredLocked()
	}
	return strings.TrimSpace(setting.WaffoApiKey) != "" && isWaffoWebhookConfiguredLocked()
}

func isWaffoWebhookConfigured() bool {
	return readPaymentConfiguration(isWaffoWebhookConfiguredLocked)
}

func isWaffoWebhookConfiguredLocked() bool {
	if setting.WaffoSandbox {
		return strings.TrimSpace(setting.WaffoSandboxPrivateKey) != "" &&
			strings.TrimSpace(setting.WaffoSandboxPublicCert) != ""
	}

	return strings.TrimSpace(setting.WaffoPrivateKey) != "" &&
		strings.TrimSpace(setting.WaffoPublicCert) != ""
}

func isWaffoWebhookEnabled() bool {
	// Delayed callbacks remain verifiable after operators disable new Waffo
	// checkout creation or clear the API key. The public certificate verifies
	// Waffo and the merchant private key signs the required acknowledgement.
	return readPaymentConfiguration(isWaffoWebhookConfiguredLocked)
}

func isWaffoPancakeTopUpEnabled() bool {
	return readPaymentConfiguration(isWaffoPancakeTopUpEnabledLocked)
}

func isWaffoPancakeTopUpEnabledLocked() bool {
	if !isPaymentComplianceConfirmedLocked() || !model.PaymentSecretStorageReady() {
		return false
	}
	if service.ValidatePaymentCallbackOrigin(operation_setting.CustomCallbackAddress, true) != nil ||
		service.ValidatePaymentPricingForOrderKind(model.PaymentProviderWaffoPancake, model.PaymentOrderKindTopUp) != nil {
		return false
	}
	// Presence-of-credentials = enabled. Webhook public keys ship inside
	// the SDK; mode (test/prod) is read from each event.
	return strings.TrimSpace(setting.WaffoPancakeMerchantID) != "" &&
		strings.TrimSpace(setting.WaffoPancakePrivateKey) != "" &&
		strings.TrimSpace(setting.WaffoPancakeProductID) != "" &&
		strings.TrimSpace(setting.WaffoPancakeStoreID) != ""
}

func isWaffoPancakeWebhookConfigured() bool {
	return readPaymentConfiguration(isWaffoPancakeWebhookConfiguredLocked)
}

func isWaffoPancakeWebhookConfiguredLocked() bool {
	// Pancake webhook verification keys ship with the official SDK. StoreID is
	// the local merchant-authority boundary checked before settlement.
	return strings.TrimSpace(setting.WaffoPancakeStoreID) != ""
}

func isWaffoPancakeWebhookEnabled() bool {
	return readPaymentConfiguration(isWaffoPancakeWebhookConfiguredLocked)
}

func isEpayTopUpEnabled() bool {
	return readPaymentConfiguration(isEpayTopUpEnabledLocked)
}

func isEpayTopUpEnabledLocked() bool {
	if !isPaymentComplianceConfirmedLocked() || !model.PaymentSecretStorageReady() {
		return false
	}
	if service.ValidatePaymentCallbackOrigin(operation_setting.CustomCallbackAddress, true) != nil {
		return false
	}
	return strings.TrimSpace(operation_setting.EpayId) != "" && strings.TrimSpace(operation_setting.EpayKey) != "" &&
		strings.TrimSpace(operation_setting.CustomCallbackAddress) != "" &&
		strings.TrimSpace(operation_setting.PayAddress) != "" && len(operation_setting.PayMethods) > 0 &&
		strings.EqualFold(strings.TrimSpace(operation_setting.EpayCurrency), "CNY") &&
		service.ValidatePaymentPricingForOrderKind(model.PaymentProviderEpay, model.PaymentOrderKindTopUp) == nil
}

func isEpayWebhookConfigured() bool {
	return readPaymentConfiguration(isEpayWebhookConfiguredLocked)
}

func isEpayWebhookConfiguredLocked() bool {
	return strings.TrimSpace(operation_setting.EpayId) != "" && strings.TrimSpace(operation_setting.EpayKey) != "" ||
		operation_setting.EpayPreviousCredentialActive()
}

func isEpayWebhookEnabled() bool {
	return readPaymentConfiguration(isEpayWebhookConfiguredLocked)
}

func isXorPayTopUpEnabled() bool {
	return readPaymentConfiguration(isXorPayTopUpEnabledLocked)
}

func isXorPayTopUpEnabledLocked() bool {
	if !isPaymentComplianceConfirmedLocked() || !model.PaymentSecretStorageReady() {
		return false
	}
	if service.ValidatePaymentCallbackOrigin(operation_setting.CustomCallbackAddress, true) != nil {
		return false
	}
	return strings.TrimSpace(setting.XorPayAid) != "" &&
		strings.TrimSpace(setting.XorPayAppSecret) != "" &&
		strings.TrimSpace(operation_setting.CustomCallbackAddress) != "" &&
		strings.EqualFold(strings.TrimSpace(setting.XorPayCurrency), "CNY") &&
		len(setting.XorPayEnabledMethods) > 0 &&
		service.ValidatePaymentPricingForOrderKind(model.PaymentProviderXorPay, model.PaymentOrderKindTopUp) == nil
}

func isXorPayWebhookEnabled() bool {
	return readPaymentConfiguration(func() bool {
		return strings.TrimSpace(setting.XorPayAid) != "" && strings.TrimSpace(setting.XorPayAppSecret) != "" ||
			setting.XorPayPreviousCredentialActive()
	})
}

func isPaymentComplianceConfirmedLocked() bool {
	return operation_setting.IsPaymentComplianceConfirmed()
}

func readPaymentConfiguration(read func() bool) bool {
	unlock := setting.LockPaymentConfigurationForRead()
	defer unlock()
	return read()
}
