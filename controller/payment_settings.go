package controller

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

var (
	paymentCurrencyPattern  = regexp.MustCompile(`^[A-Z]{3}$`)
	xorPaySettingAidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

type paymentSettingsUpdateRequest struct {
	Options                   map[string]interface{} `json:"options"`
	ClearSecrets              []string               `json:"clear_secrets,omitempty"`
	RevokePreviousCredentials []string               `json:"revoke_previous_credentials,omitempty"`
	Reason                    string                 `json:"reason,omitempty"`
	ExpectedVersion           int64                  `json:"expected_version"`
}

const (
	maxPaymentSettingsRequestBytes                  int64 = 1 << 20
	stripeWebhookSecretOverlap                            = setting.StripeWebhookSecretOverlap
	epayCredentialOverlap                                 = 24 * time.Hour
	xorPayCredentialOverlap                               = 24 * time.Hour
	paymentEpayPreviousCredentialActiveOptionKey          = "payment_setting.epay_previous_credential_active"
	paymentStripePreviousCredentialActiveOptionKey        = "payment_setting.stripe_previous_credential_active"
	paymentStripeTestModeEnabledOptionKey                 = "payment_setting.stripe_test_mode_enabled"
	paymentStripeTestModeBlockedOptionKey                 = "payment_setting.stripe_test_mode_blocked"
	paymentStripeTestModeIsolationRequiredOptionKey       = "payment_setting.stripe_test_mode_isolation_required"
	paymentXorPayPreviousCredentialActiveOptionKey        = "payment_setting.xorpay_previous_credential_active"
)

type paymentPreviousCredentialStatus struct {
	epay   bool
	stripe bool
	xorPay bool
}

func paymentPreviousCredentialStatusLocked() paymentPreviousCredentialStatus {
	return paymentPreviousCredentialStatus{
		epay:   operation_setting.EpayPreviousCredentialActive(),
		stripe: setting.StripePreviousWebhookSecretActive(),
		xorPay: setting.XorPayPreviousCredentialActive(),
	}
}

func (status paymentPreviousCredentialStatus) options() []*model.Option {
	return []*model.Option{
		{Key: paymentEpayPreviousCredentialActiveOptionKey, Value: strconv.FormatBool(status.epay)},
		{Key: paymentStripePreviousCredentialActiveOptionKey, Value: strconv.FormatBool(status.stripe)},
		{Key: paymentXorPayPreviousCredentialActiveOptionKey, Value: strconv.FormatBool(status.xorPay)},
	}
}

var paymentInternalCredentialOptionKeys = map[string]struct{}{
	paymentEpayPreviousCredentialActiveOptionKey:    {},
	paymentStripePreviousCredentialActiveOptionKey:  {},
	paymentStripeTestModeEnabledOptionKey:           {},
	paymentStripeTestModeBlockedOptionKey:           {},
	paymentStripeTestModeIsolationRequiredOptionKey: {},
	paymentXorPayPreviousCredentialActiveOptionKey:  {},
	"EpayCredentialGeneration":                      {}, "EpayIdPrevious": {}, "EpayKeyPrevious": {},
	"EpayPreviousCredentialGeneration": {}, "EpayPreviousValidBefore": {}, "EpayPreviousExpiresAt": {},
	"StripeWebhookSecretPrevious": {}, "StripeWebhookSecretPreviousExpiresAt": {}, "StripeWebhookCredentialLivemode": {},
	"StripeWebhookCredentialGeneration": {}, "StripeWebhookPreviousCredentialGeneration": {}, "StripeWebhookPreviousValidBefore": {},
	"StripeConfigurationVerifiedFingerprint": {}, "StripeConfigurationVerifiedAt": {},
	"XorPayCredentialGeneration": {}, "XorPayAidPrevious": {}, "XorPayAppSecretPrevious": {},
	"XorPayPreviousCredentialGeneration": {}, "XorPayPreviousValidBefore": {}, "XorPayPreviousExpiresAt": {},
}

var paymentSettingsAllowedKeys = map[string]struct{}{
	"ServerAddress": {}, "TopupGroupRatio": {},
	"PayAddress": {}, "EpayId": {}, "EpayKey": {}, "EpayCurrency": {}, "Price": {}, "MinTopUp": {},
	"CustomCallbackAddress": {}, "PayMethods": {}, "payment_setting.amount_options": {}, "payment_setting.amount_discount": {},
	"StripeApiSecret": {}, "StripeWebhookSecret": {}, "StripePriceId": {},
	"StripeUnitPrice": {}, "StripeMinTopUp": {}, "StripePromotionCodesEnabled": {}, "StripeCurrency": {}, "StripeAccountId": {},
	"XorPayAid": {}, "XorPayAppSecret": {}, "XorPayUnitPrice": {}, "XorPayMinTopUp": {}, "XorPayCurrency": {}, "XorPayEnabledMethods": {},
	// Compatibility-only settings are accepted atomically but are not otherwise
	// changed by the Epay/Stripe/XORPay hardening work.
	"CreemApiKey": {}, "CreemWebhookSecret": {}, "CreemTestMode": {}, "CreemProducts": {},
	"WaffoEnabled": {}, "WaffoApiKey": {}, "WaffoPrivateKey": {}, "WaffoPublicCert": {}, "WaffoSandboxPublicCert": {},
	"WaffoSandboxApiKey": {}, "WaffoSandboxPrivateKey": {}, "WaffoSandbox": {}, "WaffoMerchantId": {}, "WaffoCurrency": {},
	"WaffoUnitPrice": {}, "WaffoMinTopUp": {}, "WaffoNotifyUrl": {}, "WaffoReturnUrl": {}, "WaffoPayMethods": {},
}

// ServerAddress remains a shared site setting. Payment callbacks and return
// URLs never trust it when a gateway is enabled; they use the payment-owned
// CustomCallbackAddress instead. Pricing ratios and every other payment-owned
// key must use the atomic, step-up-protected endpoint.
func isPaymentAtomicOnlyOptionKey(key string) bool {
	if key == "StripeCredentialAccountId" || key == "StripeCredentialLivemode" || key == model.PaymentConfigurationVersionOptionKey {
		return true
	}
	if _, internal := paymentInternalCredentialOptionKeys[key]; internal {
		return true
	}
	if _, ok := paymentSettingsAllowedKeys[key]; !ok {
		return false
	}
	return key != "ServerAddress"
}

func UpdatePaymentSettings(c *gin.Context) {
	if c.GetBool("use_access_token") {
		common.ApiErrorMsg(c, "Payment settings require dashboard session authentication")
		return
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		common.ApiErrorMsg(c, "Failed to synchronize payment configuration")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPaymentSettingsRequestBytes)
	var request paymentSettingsUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil ||
		request.ExpectedVersion <= 0 ||
		(len(request.Options) == 0 && len(request.ClearSecrets) == 0 && len(request.RevokePreviousCredentials) == 0) ||
		len(request.Options) > len(paymentSettingsAllowedKeys) || len(request.ClearSecrets) > len(paymentSettingsAllowedKeys) ||
		len(request.RevokePreviousCredentials) > 3 {
		common.ApiErrorMsg(c, "Invalid payment settings request")
		return
	}
	values := make(map[string]string, len(request.Options)+len(request.ClearSecrets))
	for key, rawValue := range request.Options {
		if _, ok := paymentSettingsAllowedKeys[key]; !ok {
			common.ApiErrorMsg(c, "Unsupported payment setting: "+key)
			return
		}
		value, err := paymentOptionString(rawValue)
		if err != nil {
			common.ApiErrorMsg(c, fmt.Sprintf("Invalid value for %s", key))
			return
		}
		if isWriteOnlyPaymentSetting(key) && strings.TrimSpace(value) == "" && key != "StripeWebhookSecretPrevious" {
			continue
		}
		if err := validatePaymentSettingValue(key, value); err != nil {
			common.ApiErrorMsg(c, err.Error())
			return
		}
		values[key] = value
	}
	seenClears := make(map[string]struct{}, len(request.ClearSecrets))
	for _, rawKey := range request.ClearSecrets {
		key := strings.TrimSpace(rawKey)
		_, allowed := paymentSettingsAllowedKeys[key]
		if _, duplicate := seenClears[key]; duplicate || !allowed || !isWriteOnlyPaymentSetting(key) {
			common.ApiErrorMsg(c, "Invalid payment secret clear request")
			return
		}
		if _, alsoUpdated := request.Options[key]; alsoUpdated {
			common.ApiErrorMsg(c, "A payment secret cannot be updated and cleared in the same request")
			return
		}
		seenClears[key] = struct{}{}
		values[key] = ""
	}
	revokePrevious := make(map[string]bool, len(request.RevokePreviousCredentials))
	for _, rawProvider := range request.RevokePreviousCredentials {
		provider := strings.ToLower(strings.TrimSpace(rawProvider))
		if (provider != model.PaymentProviderEpay && provider != model.PaymentProviderStripe && provider != model.PaymentProviderXorPay) || revokePrevious[provider] {
			common.ApiErrorMsg(c, "Invalid previous payment credential revocation request")
			return
		}
		revokePrevious[provider] = true
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if len(request.Reason) > 512 || len(revokePrevious) > 0 && len(request.Reason) < 8 {
		common.ApiErrorMsg(c, "Payment credential revocation reason must contain 8 to 512 characters")
		return
	}
	if len(values) == 0 && len(revokePrevious) == 0 {
		unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
		readiness := paymentGatewayReadinessLocked()
		unlockPaymentConfiguration()
		common.ApiSuccess(c, gin.H{"readiness": readiness, "version": request.ExpectedVersion})
		return
	}
	for key, value := range values {
		if model.IsPaymentSecretOption(key) && value != "" && !model.PaymentSecretEncryptionReady() {
			common.ApiErrorMsg(c, "PAYMENT_SECRET_KEY must be configured before saving payment credentials")
			return
		}
	}
	stripeCheckoutSettingsTouched := false
	for key := range values {
		switch key {
		case "StripeApiSecret", "StripePriceId", "StripeCurrency", "StripeAccountId":
			stripeCheckoutSettingsTouched = true
		}
		if stripeCheckoutSettingsTouched {
			break
		}
	}
	resolvedStripeAccountID := ""
	resolvedStripeCredentialMode := ""
	_, stripeAPISecretChanged := values["StripeApiSecret"]
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	stripeSecret := setting.StripeApiSecret
	stripePriceID := setting.StripePriceId
	stripeCurrency := setting.StripeCurrency
	stripeConnectedAccountID := setting.StripeAccountId
	stripeCredentialAccountID := setting.StripeCredentialAccountId
	stripeCredentialMode := setting.StripeCredentialLivemode
	unlockPaymentConfiguration()
	if next, exists := values["StripeApiSecret"]; exists {
		stripeSecret = next
	}
	if next, exists := values["StripePriceId"]; exists {
		stripePriceID = next
	}
	if next, exists := values["StripeCurrency"]; exists {
		stripeCurrency = next
	}
	if next, exists := values["StripeAccountId"]; exists {
		stripeConnectedAccountID = next
	}
	stripeIdentityIncomplete := strings.TrimSpace(stripeCredentialAccountID) == "" || strings.TrimSpace(stripeCredentialMode) == ""
	if stripeAPISecretChanged || stripeCheckoutSettingsTouched && stripeIdentityIncomplete {
		if strings.TrimSpace(stripeSecret) != "" {
			credentialMode, modeErr := service.StripeCredentialMode(stripeSecret)
			if modeErr != nil {
				common.ApiErrorMsg(c, "Failed to determine the Stripe API credential mode")
				return
			}
			if !setting.StripeCredentialModeAllowed(credentialMode) {
				common.ApiErrorMsg(c, "Stripe test mode is disabled; enable it only in an isolated sandbox environment")
				return
			}
			accountID, resolveErr := service.ResolveStripeCredentialAccount(c.Request.Context(), stripeSecret)
			if resolveErr != nil {
				common.ApiErrorMsg(c, "Failed to verify the Stripe API credential and account binding")
				return
			}
			resolvedStripeAccountID = accountID
			resolvedStripeCredentialMode = credentialMode
			stripeCredentialAccountID = accountID
			stripeCredentialMode = credentialMode
		}
	}
	if err := validateStripeSandboxSettingsMutation(values, stripeCredentialMode); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	if stripeCheckoutSettingsTouched {
		if strings.TrimSpace(stripeSecret) == "" || strings.TrimSpace(stripePriceID) == "" ||
			strings.TrimSpace(stripeCurrency) == "" || strings.TrimSpace(stripeCredentialAccountID) == "" ||
			(stripeCredentialMode != "test" && stripeCredentialMode != "live") {
			values["StripeConfigurationVerifiedFingerprint"] = ""
			values["StripeConfigurationVerifiedAt"] = "0"
		} else {
			fingerprint, verifyErr := service.VerifyStripeCheckoutConfiguration(
				c.Request.Context(), stripeSecret, stripeCredentialAccountID, stripeConnectedAccountID,
				stripePriceID, stripeCurrency, stripeCredentialMode,
			)
			if verifyErr != nil {
				common.ApiErrorMsg(c, "Failed to verify Stripe Price, Checkout permissions, currency, and account binding")
				return
			}
			values["StripeConfigurationVerifiedFingerprint"] = fingerprint
			values["StripeConfigurationVerifiedAt"] = strconv.FormatInt(time.Now().Unix(), 10)
		}
	}
	unlockPaymentConfiguration = service.LockPaymentConfigurationForUpdate()
	defer unlockPaymentConfiguration()
	if resolvedStripeAccountID != "" {
		values["StripeCredentialAccountId"] = resolvedStripeAccountID
		values["StripeCredentialLivemode"] = resolvedStripeCredentialMode
	}
	revocations, err := preparePaymentCredentialRotations(values, revokePrevious, time.Now().Unix())
	if err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	if err := validateInFlightPaymentConfigurationChanges(values, revokePrevious); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	revokedProviders := make([]string, 0, len(revokePrevious))
	for provider := range revokePrevious {
		revokedProviders = append(revokedProviders, provider)
	}
	sort.Strings(revokedProviders)
	preconditions := paymentConfigurationPreconditions(values, revokePrevious)
	nextVersion, err := model.UpdatePaymentOptionsAndRevokeCredentialsAuditedWithVersionLockHeld(
		values, request.ExpectedVersion, revocations, preconditions,
		&model.PaymentConfigurationAuditInput{
			AdminID: c.GetInt("id"), ActorIP: c.ClientIP(), ChangedKeys: keys,
			RevokedProviders: revokedProviders, Reason: request.Reason,
		},
	)
	if errors.Is(err, model.ErrPaymentConfigurationVersionConflict) {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": "Payment settings changed in another session. Refresh before saving again.",
		})
		return
	}
	if errors.Is(err, model.ErrPaymentConfigurationPrecondition) {
		common.ApiErrorMsg(c, strings.TrimPrefix(err.Error(), model.ErrPaymentConfigurationPrecondition.Error()+": "))
		return
	}
	if err != nil {
		common.ApiErrorMsg(c, "Failed to save payment settings")
		return
	}
	recordManageAudit(c, "payment.settings.update", map[string]interface{}{
		"keys": keys, "revoked_previous_credentials": revokedProviders, "reason": request.Reason,
	})
	common.ApiSuccess(c, gin.H{"readiness": paymentGatewayReadinessLocked(), "version": nextVersion})
}

func validateStripeSandboxSettingsMutation(values map[string]string, credentialMode string) error {
	if credentialMode != "test" || setting.StripeTestModeEnabled() {
		return nil
	}
	for key, value := range values {
		if strings.HasPrefix(key, "Stripe") && strings.TrimSpace(value) != "" {
			return errors.New("Stripe test mode is disabled; test credentials can only be saved in an isolated sandbox environment")
		}
	}
	return nil
}

func paymentConfigurationPreconditions(values map[string]string, emergencyRevocations map[string]bool) *model.PaymentConfigurationPreconditions {
	preconditions := &model.PaymentConfigurationPreconditions{}
	if next, exists := values["PayAddress"]; exists && next != operation_setting.PayAddress &&
		!epayEmergencyMigrationReplacesCurrent(values, emergencyRevocations) {
		preconditions.RequireNoActiveEpayOrders = true
	}
	if next, exists := values["StripeAccountId"]; exists && next != setting.StripeAccountId {
		preconditions.RequireNoStripeHistory = true
	}
	if current := strings.TrimSpace(setting.StripeCredentialAccountId); current != "" {
		if next, exists := values["StripeCredentialAccountId"]; exists && next != current {
			preconditions.RequireNoStripeHistory = true
		}
	}
	if current := strings.TrimSpace(setting.StripeCredentialLivemode); current != "" {
		if next, exists := values["StripeCredentialLivemode"]; exists && next != current {
			preconditions.RequireNoStripeHistory = true
		}
	}
	if next, changingSecret := values["StripeApiSecret"]; changingSecret && next != setting.StripeApiSecret &&
		(strings.TrimSpace(setting.StripeCredentialAccountId) == "" || strings.TrimSpace(setting.StripeCredentialLivemode) == "") {
		preconditions.RequireNoStripeHistory = true
	}
	if next, exists := values["StripeWebhookSecret"]; exists && next != setting.StripeWebhookSecret && setting.StripeWebhookSecret != "" &&
		!emergencyRevocations[model.PaymentProviderStripe] {
		preconditions.RequireStripeWebhookOverlap = true
		if next == "" {
			preconditions.RequireNoStripeHistory = true
		}
	}
	return preconditions
}

func preparePaymentCredentialRotations(values map[string]string, revokePrevious map[string]bool, now int64) ([]model.PaymentCredentialRevocation, error) {
	if now <= 0 {
		return nil, errors.New("invalid payment credential rotation time")
	}
	revocations := make([]model.PaymentCredentialRevocation, 0, 5)
	stripeCredentialMode := setting.StripeCredentialLivemode
	if nextMode, exists := values["StripeCredentialLivemode"]; exists {
		stripeCredentialMode = nextMode
	}
	stripeWebhookGeneration := setting.StripeWebhookCredentialGeneration
	stripeModeChanging := stripeCredentialMode != setting.StripeCredentialLivemode
	stripeWebhookSecret, stripeWebhookSecretSupplied := values["StripeWebhookSecret"]
	if revokePrevious[model.PaymentProviderStripe] {
		if stripeWebhookSecretSupplied && stripeWebhookSecret != "" && stripeWebhookSecret == setting.StripeWebhookSecret {
			return nil, errors.New("Stripe emergency webhook credential rotation requires a new secret or disables the webhook")
		}
		if !stripeWebhookSecretSupplied {
			stripeWebhookSecret = ""
			stripeWebhookSecretSupplied = true
			values["StripeWebhookSecret"] = stripeWebhookSecret
		}
	}
	stripeWebhookSecretChanging := stripeWebhookSecretSupplied && stripeWebhookSecret != setting.StripeWebhookSecret
	if stripeWebhookSecretChanging && stripeWebhookSecret != "" && !stripeModeChanging &&
		!revokePrevious[model.PaymentProviderStripe] && setting.StripePreviousWebhookSecretActive() {
		return nil, errors.New("Stripe webhook secret cannot be rotated again while the previous signing secret overlap is still active")
	}
	if stripeWebhookSecretChanging || revokePrevious[model.PaymentProviderStripe] {
		currentGeneration := stripeWebhookGeneration
		if currentGeneration <= 0 || currentGeneration == math.MaxInt64 && stripeWebhookSecretChanging && setting.StripeWebhookSecret != "" {
			return nil, errors.New("Stripe webhook credential generation is invalid or exhausted")
		}
		if stripeWebhookSecretChanging {
			nextGeneration := currentGeneration
			if setting.StripeWebhookSecret != "" {
				nextGeneration++
			}
			values["StripeWebhookCredentialGeneration"] = strconv.FormatInt(nextGeneration, 10)
		}
		if stripeWebhookSecret != "" {
			if stripeCredentialMode != "test" && stripeCredentialMode != "live" {
				return nil, errors.New("Stripe API credential mode must be verified before binding a webhook secret")
			}
			values["StripeWebhookCredentialLivemode"] = stripeCredentialMode
		} else {
			values["StripeWebhookCredentialLivemode"] = ""
		}
		preservePrevious := !revokePrevious[model.PaymentProviderStripe] && stripeWebhookSecret != "" &&
			setting.StripeWebhookSecret != "" && !stripeModeChanging
		if preservePrevious {
			values["StripeWebhookSecretPrevious"] = setting.StripeWebhookSecret
			values["StripeWebhookPreviousCredentialGeneration"] = strconv.FormatInt(currentGeneration, 10)
			values["StripeWebhookPreviousValidBefore"] = strconv.FormatInt(now, 10)
			values["StripeWebhookSecretPreviousExpiresAt"] = strconv.FormatInt(now+int64(stripeWebhookSecretOverlap/time.Second), 10)
		} else {
			values["StripeWebhookSecretPrevious"] = ""
			values["StripeWebhookPreviousCredentialGeneration"] = "0"
			values["StripeWebhookPreviousValidBefore"] = "0"
			values["StripeWebhookSecretPreviousExpiresAt"] = "0"
		}
	}
	if revokePrevious[model.PaymentProviderStripe] {
		revokedGenerations := make([]int64, 0, 2)
		if setting.StripeWebhookSecret != "" && stripeWebhookGeneration > 0 {
			revokedGenerations = append(revokedGenerations, stripeWebhookGeneration)
		}
		if setting.StripeWebhookSecretPrevious != "" && setting.StripeWebhookSecretPreviousExpiresAt > now {
			previousGeneration := setting.StripeWebhookPreviousCredentialGeneration
			if previousGeneration <= 0 {
				previousGeneration = stripeWebhookGeneration - 1
			}
			if previousGeneration > 0 && (len(revokedGenerations) == 0 || revokedGenerations[0] != previousGeneration) {
				revokedGenerations = append(revokedGenerations, previousGeneration)
			}
		}
		if len(revokedGenerations) == 0 && stripeWebhookGeneration > 0 {
			revokedGenerations = append(revokedGenerations, stripeWebhookGeneration)
		}
		for index, generation := range revokedGenerations {
			revocations = append(revocations, model.PaymentCredentialRevocation{
				Provider: model.PaymentProviderStripe, Generation: generation, ValidBefore: now, AllActiveOrders: index == 0,
			})
		}
	}

	nextEpayID := operation_setting.EpayId
	_, epayIDSupplied := values["EpayId"]
	if value, exists := values["EpayId"]; exists {
		nextEpayID = value
	}
	nextEpayKey := operation_setting.EpayKey
	_, epayKeySupplied := values["EpayKey"]
	if value, exists := values["EpayKey"]; exists {
		nextEpayKey = value
	}
	if revokePrevious[model.PaymentProviderEpay] && (epayIDSupplied || epayKeySupplied) &&
		nextEpayID == operation_setting.EpayId && nextEpayKey == operation_setting.EpayKey {
		return nil, errors.New("Epay emergency replacement credentials must differ from the current credentials")
	}
	if nextEpayID != operation_setting.EpayId || nextEpayKey != operation_setting.EpayKey {
		if operation_setting.EpayCredentialGeneration <= 0 || operation_setting.EpayCredentialGeneration == math.MaxInt64 {
			return nil, errors.New("Epay credential generation is invalid or exhausted")
		}
		previousExplicitlyCleared := false
		if value, exists := values["EpayKeyPrevious"]; exists && value == "" {
			previousExplicitlyCleared = true
		}
		if operation_setting.EpayPreviousCredentialActive() && !previousExplicitlyCleared && !revokePrevious[model.PaymentProviderEpay] {
			return nil, errors.New("Epay credentials cannot be rotated again while the previous credential generation is still active")
		}
		if revokePrevious[model.PaymentProviderEpay] {
			if operation_setting.EpayPreviousCredentialGeneration > 0 && operation_setting.EpayKeyPrevious != "" {
				revocations = append(revocations, model.PaymentCredentialRevocation{
					Provider: model.PaymentProviderEpay, Generation: operation_setting.EpayPreviousCredentialGeneration, ValidBefore: now,
				})
			}
			if operation_setting.EpayId != "" && operation_setting.EpayKey != "" {
				revocations = append(revocations, model.PaymentCredentialRevocation{
					Provider: model.PaymentProviderEpay, Generation: operation_setting.EpayCredentialGeneration, ValidBefore: now,
				})
			}
			values["EpayIdPrevious"] = ""
			values["EpayKeyPrevious"] = ""
			values["EpayPreviousCredentialGeneration"] = "0"
			values["EpayPreviousValidBefore"] = "0"
			values["EpayPreviousExpiresAt"] = "0"
		} else if operation_setting.EpayId != "" && operation_setting.EpayKey != "" {
			if !model.PaymentSecretStorageReady() {
				return nil, errors.New("PAYMENT_SECRET_KEY must be configured before rotating Epay credentials")
			}
			values["EpayIdPrevious"] = operation_setting.EpayId
			values["EpayKeyPrevious"] = operation_setting.EpayKey
			values["EpayPreviousCredentialGeneration"] = strconv.FormatInt(operation_setting.EpayCredentialGeneration, 10)
			values["EpayPreviousValidBefore"] = strconv.FormatInt(now, 10)
			values["EpayPreviousExpiresAt"] = strconv.FormatInt(now+int64(epayCredentialOverlap/time.Second), 10)
		}
		values["EpayCredentialGeneration"] = strconv.FormatInt(operation_setting.EpayCredentialGeneration+1, 10)
	} else if revokePrevious[model.PaymentProviderEpay] {
		if operation_setting.EpayPreviousCredentialGeneration <= 0 || operation_setting.EpayKeyPrevious == "" {
			return nil, errors.New("no previous Epay credential generation is available to revoke")
		}
		revocations = append(revocations, model.PaymentCredentialRevocation{
			Provider: model.PaymentProviderEpay, Generation: operation_setting.EpayPreviousCredentialGeneration, ValidBefore: now,
		})
		values["EpayIdPrevious"] = ""
		values["EpayKeyPrevious"] = ""
		values["EpayPreviousCredentialGeneration"] = "0"
		values["EpayPreviousValidBefore"] = "0"
		values["EpayPreviousExpiresAt"] = "0"
	}

	nextXorPayAid := setting.XorPayAid
	_, xorPayAidSupplied := values["XorPayAid"]
	if value, exists := values["XorPayAid"]; exists {
		nextXorPayAid = value
	}
	nextXorPaySecret := setting.XorPayAppSecret
	_, xorPaySecretSupplied := values["XorPayAppSecret"]
	if value, exists := values["XorPayAppSecret"]; exists {
		nextXorPaySecret = value
	}
	if revokePrevious[model.PaymentProviderXorPay] && (xorPayAidSupplied || xorPaySecretSupplied) &&
		nextXorPayAid == setting.XorPayAid && nextXorPaySecret == setting.XorPayAppSecret {
		return nil, errors.New("XORPay emergency replacement credentials must differ from the current credentials")
	}
	xorPayAidChanging := nextXorPayAid != setting.XorPayAid
	xorPaySecretChanging := nextXorPaySecret != setting.XorPayAppSecret
	if xorPayAidChanging && strings.TrimSpace(setting.XorPayAid) != "" && strings.TrimSpace(nextXorPayAid) != "" &&
		(!xorPaySecretChanging || strings.TrimSpace(nextXorPaySecret) == "") {
		return nil, errors.New("XORPay AID changes require a different AppSecret in the same atomic update")
	}
	if nextXorPayAid != setting.XorPayAid || nextXorPaySecret != setting.XorPayAppSecret {
		if setting.XorPayCredentialGeneration <= 0 || setting.XorPayCredentialGeneration == math.MaxInt64 {
			return nil, errors.New("XORPay credential generation is invalid or exhausted")
		}
		previousExplicitlyCleared := false
		if value, exists := values["XorPayAppSecretPrevious"]; exists && value == "" {
			previousExplicitlyCleared = true
		}
		if setting.XorPayPreviousCredentialActive() && !previousExplicitlyCleared && !revokePrevious[model.PaymentProviderXorPay] {
			return nil, errors.New("XORPay credentials cannot be rotated again while the previous credential generation is still active")
		}
		if revokePrevious[model.PaymentProviderXorPay] {
			if setting.XorPayPreviousCredentialGeneration > 0 && setting.XorPayAppSecretPrevious != "" {
				revocations = append(revocations, model.PaymentCredentialRevocation{
					Provider: model.PaymentProviderXorPay, Generation: setting.XorPayPreviousCredentialGeneration, ValidBefore: now,
				})
			}
			if setting.XorPayAid != "" && setting.XorPayAppSecret != "" {
				revocations = append(revocations, model.PaymentCredentialRevocation{
					Provider: model.PaymentProviderXorPay, Generation: setting.XorPayCredentialGeneration, ValidBefore: now,
				})
			}
			values["XorPayAidPrevious"] = ""
			values["XorPayAppSecretPrevious"] = ""
			values["XorPayPreviousCredentialGeneration"] = "0"
			values["XorPayPreviousValidBefore"] = "0"
			values["XorPayPreviousExpiresAt"] = "0"
		} else if setting.XorPayAid != "" && setting.XorPayAppSecret != "" {
			if !model.PaymentSecretStorageReady() {
				return nil, errors.New("PAYMENT_SECRET_KEY must be configured before rotating XORPay credentials")
			}
			values["XorPayAidPrevious"] = setting.XorPayAid
			values["XorPayAppSecretPrevious"] = setting.XorPayAppSecret
			values["XorPayPreviousCredentialGeneration"] = strconv.FormatInt(setting.XorPayCredentialGeneration, 10)
			values["XorPayPreviousValidBefore"] = strconv.FormatInt(now, 10)
			values["XorPayPreviousExpiresAt"] = strconv.FormatInt(now+int64(xorPayCredentialOverlap/time.Second), 10)
		}
		values["XorPayCredentialGeneration"] = strconv.FormatInt(setting.XorPayCredentialGeneration+1, 10)
	} else if revokePrevious[model.PaymentProviderXorPay] {
		if setting.XorPayPreviousCredentialGeneration <= 0 || setting.XorPayAppSecretPrevious == "" {
			return nil, errors.New("no previous XORPay credential generation is available to revoke")
		}
		revocations = append(revocations, model.PaymentCredentialRevocation{
			Provider: model.PaymentProviderXorPay, Generation: setting.XorPayPreviousCredentialGeneration, ValidBefore: now,
		})
		values["XorPayAidPrevious"] = ""
		values["XorPayAppSecretPrevious"] = ""
		values["XorPayPreviousCredentialGeneration"] = "0"
		values["XorPayPreviousValidBefore"] = "0"
		values["XorPayPreviousExpiresAt"] = "0"
	}
	return revocations, nil
}

func validateInFlightPaymentConfigurationChanges(values map[string]string, emergencyRevocations map[string]bool) error {
	activeStatuses := []string{model.PaymentOrderStatusPending, model.PaymentOrderStatusProcessing, model.PaymentOrderStatusManualReview}
	if next, exists := values["StripeWebhookSecret"]; exists && next == "" && setting.StripeWebhookSecret != "" &&
		!emergencyRevocations[model.PaymentProviderStripe] {
		hasHistory, err := model.HasStripeAccountBoundData()
		if err != nil {
			return err
		}
		if hasHistory {
			return errors.New("StripeWebhookSecret cannot be cleared while durable Stripe data exists; use the emergency credential revocation flow with a reason")
		}
	}
	if nextSecret, changingSecret := values["StripeApiSecret"]; changingSecret && nextSecret != setting.StripeApiSecret &&
		(strings.TrimSpace(setting.StripeCredentialAccountId) == "" || strings.TrimSpace(setting.StripeCredentialLivemode) == "") {
		hasHistory, err := model.HasStripeAccountBoundData()
		if err != nil {
			return err
		}
		if hasHistory {
			return errors.New("StripeApiSecret cannot be changed while historical Stripe data exists until the current credential account and test/live mode are verified and bound")
		}
	}
	type guardedChange struct {
		provider string
		key      string
		current  string
		all      bool
	}
	changes := []guardedChange{
		{provider: model.PaymentProviderEpay, key: "PayAddress", current: operation_setting.PayAddress},
		{provider: model.PaymentProviderStripe, key: "StripeAccountId", current: setting.StripeAccountId, all: true},
	}
	if strings.TrimSpace(setting.StripeCredentialAccountId) != "" {
		changes = append(changes, guardedChange{
			provider: model.PaymentProviderStripe, key: "StripeCredentialAccountId",
			current: setting.StripeCredentialAccountId, all: true,
		})
	}
	if strings.TrimSpace(setting.StripeCredentialLivemode) != "" {
		changes = append(changes, guardedChange{
			provider: model.PaymentProviderStripe, key: "StripeCredentialLivemode",
			current: setting.StripeCredentialLivemode, all: true,
		})
	}
	if next, exists := values["StripeWebhookSecret"]; exists && next != setting.StripeWebhookSecret && setting.StripeWebhookSecret != "" &&
		!emergencyRevocations[model.PaymentProviderStripe] {
		count, err := model.CountPaymentOrdersForProvider(model.PaymentProviderStripe, activeStatuses)
		if err != nil {
			return err
		}
		if count > 0 {
			previous := values["StripeWebhookSecretPrevious"]
			expiresAt, _ := strconv.ParseInt(values["StripeWebhookSecretPreviousExpiresAt"], 10, 64)
			if next == "" || previous != setting.StripeWebhookSecret || expiresAt <= time.Now().Unix() {
				return errors.New("StripeWebhookSecret cannot be cleared or rotated without an active previous-secret overlap while Stripe payment orders are in flight")
			}
		}
	}
	for _, change := range changes {
		next, exists := values[change.key]
		if !exists || next == change.current {
			continue
		}
		if change.provider == model.PaymentProviderEpay && change.key == "PayAddress" &&
			epayEmergencyMigrationReplacesCurrent(values, emergencyRevocations) {
			continue
		}
		statuses := activeStatuses
		if change.all {
			if change.provider == model.PaymentProviderStripe {
				hasHistory, err := model.HasStripeAccountBoundData()
				if err != nil {
					return err
				}
				if hasHistory {
					return fmt.Errorf("%s cannot be changed while durable Stripe data still depends on the current account", change.key)
				}
				continue
			}
			statuses = nil
		}
		count, err := model.CountPaymentOrdersForProvider(change.provider, statuses)
		if err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("%s cannot be changed while %s payment orders still depend on the current configuration", change.key, change.provider)
		}
	}
	if nextMode, changingMode := values["StripeCredentialLivemode"]; changingMode && nextMode != setting.StripeCredentialLivemode {
		nextWebhookSecret, supplied := values["StripeWebhookSecret"]
		if !supplied || nextWebhookSecret == "" || nextWebhookSecret == setting.StripeWebhookSecret ||
			values["StripeWebhookCredentialLivemode"] != nextMode {
			return errors.New("Stripe test/live mode changes require a new webhook secret bound in the same payment settings update")
		}
	}
	return nil
}

func epayEmergencyMigrationReplacesCurrent(values map[string]string, emergencyRevocations map[string]bool) bool {
	if !emergencyRevocations[model.PaymentProviderEpay] || strings.TrimSpace(operation_setting.EpayId) == "" ||
		strings.TrimSpace(operation_setting.EpayKey) == "" || operation_setting.EpayCredentialGeneration <= 0 {
		return false
	}
	nextID := operation_setting.EpayId
	if value, exists := values["EpayId"]; exists {
		nextID = value
	}
	nextKey := operation_setting.EpayKey
	if value, exists := values["EpayKey"]; exists {
		nextKey = value
	}
	return strings.TrimSpace(nextID) != "" && strings.TrimSpace(nextKey) != "" &&
		(nextID != operation_setting.EpayId || nextKey != operation_setting.EpayKey)
}

func paymentOptionString(value interface{}) (string, error) {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), nil
	case bool:
		return strconv.FormatBool(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", errors.New("invalid number")
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	default:
		return "", errors.New("unsupported option value")
	}
}

func isWriteOnlyPaymentSetting(key string) bool {
	switch key {
	case "EpayKey", "StripeApiSecret", "StripeWebhookSecret", "StripeWebhookSecretPrevious", "XorPayAppSecret",
		"CreemApiKey", "CreemWebhookSecret", "WaffoApiKey", "WaffoPrivateKey", "WaffoSandboxApiKey", "WaffoSandboxPrivateKey":
		return true
	default:
		return false
	}
}

func validatePaymentSettingValue(key, value string) error {
	if len(value) > 64<<10 {
		return fmt.Errorf("%s is too large", key)
	}
	switch key {
	case "ServerAddress":
		return validatePaymentAdminURL(value, true)
	case "PayAddress":
		return validatePaymentAdminURL(value, false)
	case "CustomCallbackAddress":
		if value == "" {
			return nil
		}
		return service.ValidatePaymentCallbackOrigin(value, true)
	case "EpayId":
		if value == "" || len(value) > 128 {
			return errors.New("Epay merchant ID is invalid")
		}
	case "EpayKey":
		if len(value) < 32 || len(value) > 512 {
			return errors.New("Epay key is invalid")
		}
	case "EpayCurrency", "StripeCurrency", "XorPayCurrency", "WaffoCurrency":
		currency := strings.ToUpper(value)
		if !paymentCurrencyPattern.MatchString(currency) {
			return fmt.Errorf("%s must be a three-letter currency code", key)
		}
		if _, ok := common.PaymentCurrencyExponentOK(value); !ok {
			return fmt.Errorf("%s is not a recognized ISO 4217 currency", key)
		}
		if (key == "EpayCurrency" || key == "XorPayCurrency") && currency != "CNY" {
			return fmt.Errorf("%s must be CNY because the provider protocol has no currency field", key)
		}
	case "Price", "StripeUnitPrice", "XorPayUnitPrice", "WaffoUnitPrice":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed <= 0 || parsed > 1_000_000 {
			return fmt.Errorf("%s must be a positive finite number", key)
		}
	case "MinTopUp", "StripeMinTopUp", "XorPayMinTopUp", "WaffoMinTopUp":
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > int(service.MaxPaymentTopUpAmount) {
			return fmt.Errorf("%s is outside the supported range", key)
		}
	case "StripeApiSecret":
		if len(value) > 512 || (!strings.HasPrefix(value, "sk_live_") && !strings.HasPrefix(value, "sk_test_") && !strings.HasPrefix(value, "rk_live_") && !strings.HasPrefix(value, "rk_test_")) {
			return errors.New("Stripe API secret format is invalid")
		}
	case "StripeWebhookSecret", "StripeWebhookSecretPrevious":
		if key == "StripeWebhookSecretPrevious" && value == "" {
			return nil
		}
		if !strings.HasPrefix(value, "whsec_") || len(value) < 16 || len(value) > 512 {
			return fmt.Errorf("%s format is invalid", key)
		}
	case "StripeWebhookSecretPreviousExpiresAt":
		expiresAt, err := strconv.ParseInt(value, 10, 64)
		if err != nil || expiresAt < 0 || expiresAt > time.Now().Add(stripeWebhookSecretOverlap).Unix() {
			return errors.New("Stripe previous webhook secret expiry is invalid")
		}
	case "StripePriceId":
		if value != "" && (!strings.HasPrefix(value, "price_") || len(value) > 128) {
			return errors.New("Stripe price template ID is invalid")
		}
	case "StripeAccountId":
		if value != "" && (!strings.HasPrefix(value, "acct_") || len(value) > 128) {
			return errors.New("Stripe account ID is invalid")
		}
	case "StripePromotionCodesEnabled":
		if value != "false" {
			return errors.New("Stripe promotion codes are disabled for server-quoted payments")
		}
	case "CreemTestMode", "WaffoEnabled", "WaffoSandbox":
		if value != "true" && value != "false" {
			return fmt.Errorf("%s must be boolean", key)
		}
	case "XorPayAid":
		if !xorPaySettingAidPattern.MatchString(value) {
			return errors.New("XORPay AID is invalid")
		}
	case "XorPayAppSecret":
		if len(value) < 32 || len(value) > 512 {
			return errors.New("XORPay app secret is invalid")
		}
	case "XorPayEnabledMethods":
		var methods []string
		if err := common.UnmarshalJsonStr(value, &methods); err != nil || len(methods) > 2 {
			return errors.New("XORPay enabled methods are invalid")
		}
		seen := map[string]struct{}{}
		for _, method := range methods {
			if method != setting.XorPayMethodNative && method != setting.XorPayMethodAlipay {
				return errors.New("XORPay enabled methods are invalid")
			}
			if _, exists := seen[method]; exists {
				return errors.New("XORPay enabled methods contain duplicates")
			}
			seen[method] = struct{}{}
		}
	case "PayMethods":
		if _, err := operation_setting.ParsePayMethodsByJsonString(value); err != nil {
			return err
		}
	case "payment_setting.amount_options":
		var amounts []int
		if err := common.UnmarshalJsonStr(value, &amounts); err != nil || len(amounts) > 50 {
			return errors.New("Top-up amount options are invalid")
		}
		for _, amount := range amounts {
			if amount < 1 || amount > int(service.MaxPaymentTopUpAmount) {
				return errors.New("Top-up amount option is outside the supported range")
			}
		}
	case "payment_setting.amount_discount":
		var discounts map[int]float64
		if err := common.UnmarshalJsonStr(value, &discounts); err != nil || len(discounts) > 100 {
			return errors.New("Top-up discounts are invalid")
		}
		for amount, discount := range discounts {
			if amount < 1 || amount > int(service.MaxPaymentTopUpAmount) || math.IsNaN(discount) || math.IsInf(discount, 0) || discount <= 0 || discount > 1 {
				return errors.New("Top-up discount is outside the supported range")
			}
		}
	case "TopupGroupRatio":
		var ratios map[string]float64
		if err := common.UnmarshalJsonStr(value, &ratios); err != nil || len(ratios) == 0 || len(ratios) > 100 {
			return errors.New("Top-up group ratios are invalid")
		}
		for group, ratio := range ratios {
			if strings.TrimSpace(group) == "" || len(group) > 64 || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 || ratio > 1_000 {
				return errors.New("Top-up group ratio is outside the supported range")
			}
		}
	case "CreemProducts", "WaffoPayMethods":
		var decoded interface{}
		if err := common.UnmarshalJsonStr(value, &decoded); err != nil {
			return fmt.Errorf("%s must be valid JSON", key)
		}
	default:
		if len(value) > 8192 {
			return fmt.Errorf("%s is too large", key)
		}
	}
	return nil
}

func validatePaymentAdminURL(raw string, allowLocal bool) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(raw) > 2048 {
		return errors.New("Payment URL is invalid")
	}
	local := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	if parsed.Scheme != "https" && !(allowLocal && local && parsed.Scheme == "http") {
		return errors.New("Payment URL must use HTTPS")
	}
	return nil
}

func paymentGatewayReadinessLocked() map[string]interface{} {
	encryptionReady := model.PaymentSecretStorageReady()
	previousCredentials := paymentPreviousCredentialStatusLocked()
	return map[string]interface{}{
		"epay": map[string]interface{}{
			"ready": isEpayTopUpEnabledLocked(), "configured": isEpayWebhookConfiguredLocked(), "secrets_encrypted": encryptionReady,
			"previous_credential_active": previousCredentials.epay,
		},
		"stripe": map[string]interface{}{
			"ready": isStripeTopUpEnabledLocked(), "configured": isStripeWebhookConfiguredLocked(), "secrets_encrypted": encryptionReady,
			"promotion_codes_supported": false, "credential_account_id": setting.StripeCredentialAccountId,
			"credential_livemode":                setting.StripeCredentialLivemode,
			"webhook_credential_livemode":        setting.StripeWebhookCredentialLivemode,
			"test_mode_enabled":                  setting.StripeTestModeEnabled(),
			"test_mode_isolation_required":       true,
			"test_mode_blocked":                  setting.StripeCredentialLivemode == "test" && !setting.StripeTestModeEnabled(),
			"checkout_configuration_verified_at": setting.StripeConfigurationVerifiedAt,
			"previous_credential_active":         previousCredentials.stripe,
		},
		"xorpay": map[string]interface{}{
			"ready": isXorPayTopUpEnabledLocked(),
			"configured": strings.TrimSpace(setting.XorPayAid) != "" && strings.TrimSpace(setting.XorPayAppSecret) != "" ||
				setting.XorPayPreviousCredentialActive(),
			"secrets_encrypted":          encryptionReady,
			"previous_credential_active": previousCredentials.xorPay,
		},
	}
}
