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
	)
	return verifiedFingerprint != "" && setting.StripeConfigurationVerifiedFingerprint == verifiedFingerprint &&
		setting.StripeConfigurationVerifiedAt > 0 && strings.TrimSpace(setting.StripeApiSecret) != "" &&
		strings.TrimSpace(operation_setting.CustomCallbackAddress) != "" &&
		strings.TrimSpace(setting.StripeWebhookSecret) != "" &&
		strings.TrimSpace(setting.StripePriceId) != "" &&
		strings.TrimSpace(setting.StripeCredentialAccountId) != "" &&
		(setting.StripeCredentialLivemode == "test" || setting.StripeCredentialLivemode == "live") &&
		setting.StripeCredentialModeAllowed(setting.StripeCredentialLivemode) &&
		setting.StripeWebhookCredentialLivemode == setting.StripeCredentialLivemode
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
	if !isPaymentComplianceConfirmedLocked() {
		return false
	}
	products := strings.TrimSpace(setting.CreemProducts)
	return strings.TrimSpace(setting.CreemApiKey) != "" &&
		products != "" &&
		products != "[]"
}

func isCreemWebhookConfigured() bool {
	return readPaymentConfiguration(isCreemWebhookConfiguredLocked)
}

func isCreemWebhookConfiguredLocked() bool {
	return strings.TrimSpace(setting.CreemWebhookSecret) != ""
}

func isCreemWebhookEnabled() bool {
	return readPaymentConfiguration(func() bool {
		return isCreemTopUpEnabledLocked() && isCreemWebhookConfiguredLocked()
	})
}

func isWaffoTopUpEnabled() bool {
	return readPaymentConfiguration(isWaffoTopUpEnabledLocked)
}

func isWaffoTopUpEnabledLocked() bool {
	if !isPaymentComplianceConfirmedLocked() {
		return false
	}
	if !setting.WaffoEnabled {
		return false
	}

	return isWaffoWebhookConfiguredLocked()
}

func isWaffoWebhookConfigured() bool {
	return readPaymentConfiguration(isWaffoWebhookConfiguredLocked)
}

func isWaffoWebhookConfiguredLocked() bool {
	if setting.WaffoSandbox {
		return strings.TrimSpace(setting.WaffoSandboxApiKey) != "" &&
			strings.TrimSpace(setting.WaffoSandboxPrivateKey) != "" &&
			strings.TrimSpace(setting.WaffoSandboxPublicCert) != ""
	}

	return strings.TrimSpace(setting.WaffoApiKey) != "" &&
		strings.TrimSpace(setting.WaffoPrivateKey) != "" &&
		strings.TrimSpace(setting.WaffoPublicCert) != ""
}

func isWaffoWebhookEnabled() bool {
	return readPaymentConfiguration(isWaffoTopUpEnabledLocked)
}

func isWaffoPancakeTopUpEnabled() bool {
	return readPaymentConfiguration(isWaffoPancakeTopUpEnabledLocked)
}

func isWaffoPancakeTopUpEnabledLocked() bool {
	if !isPaymentComplianceConfirmedLocked() {
		return false
	}
	// Presence-of-credentials = enabled. Webhook public keys ship inside
	// the SDK; mode (test/prod) is read from each event.
	return strings.TrimSpace(setting.WaffoPancakeMerchantID) != "" &&
		strings.TrimSpace(setting.WaffoPancakePrivateKey) != "" &&
		strings.TrimSpace(setting.WaffoPancakeProductID) != ""
}

func isWaffoPancakeWebhookConfigured() bool {
	return readPaymentConfiguration(isWaffoPancakeTopUpEnabledLocked)
}

func isWaffoPancakeWebhookEnabled() bool {
	return readPaymentConfiguration(isWaffoPancakeTopUpEnabledLocked)
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
		strings.EqualFold(strings.TrimSpace(operation_setting.EpayCurrency), "CNY")
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
		len(setting.XorPayEnabledMethods) > 0
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
