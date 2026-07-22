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
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

var (
	paymentCurrencyPattern          = regexp.MustCompile(`^[A-Z]{3}$`)
	xorPaySettingAidPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	errPaymentSettingsScopeConflict = errors.New("payment settings request spans multiple configuration modules")
)

type paymentSettingsUpdateRequest struct {
	Options                   map[string]interface{} `json:"options"`
	ClearSecrets              []string               `json:"clear_secrets,omitempty"`
	RevokePreviousCredentials []string               `json:"revoke_previous_credentials,omitempty"`
	DisableCurrentCredentials []string               `json:"disable_current_credentials,omitempty"`
	Reason                    string                 `json:"reason,omitempty"`
	ExpectedVersion           int64                  `json:"expected_version"`
}

const (
	maxPaymentSettingsRequestBytes                  int64 = 1 << 20
	stripeWebhookSecretOverlap                            = setting.StripeWebhookSecretOverlap
	epayCredentialOverlap                                 = model.PaymentCallbackRecoveryWindow + 3*time.Hour
	xorPayCredentialOverlap                               = model.PaymentCallbackRecoveryWindow + 25*time.Hour
	paymentEpayPreviousCredentialActiveOptionKey          = "payment_setting.epay_previous_credential_active"
	paymentStripePreviousCredentialActiveOptionKey        = "payment_setting.stripe_previous_credential_active"
	paymentStripeTestModeEnabledOptionKey                 = "payment_setting.stripe_test_mode_enabled"
	paymentStripeTestModeBlockedOptionKey                 = "payment_setting.stripe_test_mode_blocked"
	paymentStripeTestModeIsolationRequiredOptionKey       = "payment_setting.stripe_test_mode_isolation_required"
	paymentStripeWebhookAPIVersionOptionKey               = "payment_setting.stripe_webhook_api_version"
	paymentStripeWebhookSecretOverlapHoursOptionKey       = "payment_setting.stripe_webhook_secret_overlap_hours"
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
	model.PaymentRouteCatalogVersionOptionKey:       {},
	paymentEpayPreviousCredentialActiveOptionKey:    {},
	paymentStripePreviousCredentialActiveOptionKey:  {},
	paymentStripeTestModeEnabledOptionKey:           {},
	paymentStripeTestModeBlockedOptionKey:           {},
	paymentStripeTestModeIsolationRequiredOptionKey: {},
	paymentStripeWebhookAPIVersionOptionKey:         {},
	paymentStripeWebhookSecretOverlapHoursOptionKey: {},
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
	"StripeCheckoutAllowedHosts": {},
	"XorPayAid":                  {}, "XorPayAppSecret": {}, "XorPayUnitPrice": {}, "XorPayMinTopUp": {}, "XorPayCurrency": {}, "XorPayEnabledMethods": {},
	// Compatibility-only settings are accepted atomically but are not otherwise
	// changed by the Epay/Stripe/XORPay hardening work.
	"CreemApiKey": {}, "CreemWebhookSecret": {}, "CreemTestMode": {}, "CreemProducts": {},
	"WaffoEnabled": {}, "WaffoApiKey": {}, "WaffoPrivateKey": {}, "WaffoPublicCert": {}, "WaffoSandboxPublicCert": {},
	"WaffoSandboxApiKey": {}, "WaffoSandboxPrivateKey": {}, "WaffoSandbox": {}, "WaffoMerchantId": {}, "WaffoCurrency": {},
	"WaffoUnitPrice": {}, "WaffoMinTopUp": {}, "WaffoNotifyUrl": {}, "WaffoReturnUrl": {}, "WaffoPayMethods": {},
	"WaffoWebRedirectHosts": {}, "WaffoAppRedirectSchemes": {},
	"WaffoPancakeMerchantID": {}, "WaffoPancakePrivateKey": {}, "WaffoPancakeReturnURL": {},
	"WaffoPancakeTestMode": {}, "WaffoPancakeUnitPrice": {}, "WaffoPancakeMinTopUp": {}, "WaffoPancakeStoreID": {}, "WaffoPancakeProductID": {},
}

func paymentSettingMutationScope(key string) string {
	switch key {
	case "ServerAddress", "TopupGroupRatio", "Price", "MinTopUp", "CustomCallbackAddress", "PayMethods",
		"payment_setting.amount_options", "payment_setting.amount_discount":
		return "general"
	case "PayAddress", "EpayId", "EpayKey", "EpayCurrency":
		return model.PaymentProviderEpay
	}
	if strings.HasPrefix(key, "WaffoPancake") {
		return model.PaymentProviderWaffoPancake
	}
	for _, prefix := range []string{"Stripe", "XorPay", "Creem", "Waffo"} {
		if strings.HasPrefix(key, prefix) {
			return strings.ToLower(prefix)
		}
	}
	return ""
}

// validatePaymentSettingsMutationScope enforces one independently saved
// configuration module per request. Emergency replacement may include new
// credentials for the same provider, but it can never be combined with a
// different provider, general settings, or a second revocation.
func validatePaymentSettingsMutationScope(options map[string]interface{}, clearSecrets, revokeProviders []string) error {
	scopes := make(map[string]struct{}, 2)
	addKey := func(key string) {
		if scope := paymentSettingMutationScope(strings.TrimSpace(key)); scope != "" {
			scopes[scope] = struct{}{}
		}
	}
	for key := range options {
		addKey(key)
	}
	for _, key := range clearSecrets {
		addKey(key)
	}
	if len(revokeProviders) > 1 {
		return errPaymentSettingsScopeConflict
	}
	for _, provider := range revokeProviders {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == model.PaymentProviderEpay || provider == model.PaymentProviderStripe || provider == model.PaymentProviderXorPay ||
			provider == model.PaymentProviderCreem || provider == model.PaymentProviderWaffo || provider == model.PaymentProviderWaffoPancake {
			scopes[provider] = struct{}{}
		}
	}
	if len(scopes) > 1 {
		return errPaymentSettingsScopeConflict
	}
	return nil
}

func paymentSettingsAPIError(c *gin.Context, status int, code string, diagnostic error) {
	paymentSettingsAPIErrorWithParams(c, status, code, nil, diagnostic)
}

func paymentSettingsAPIErrorWithParams(c *gin.Context, status int, code string, params gin.H, diagnostic error) {
	if diagnostic != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"payment settings request rejected admin_id=%d code=%s error=%q",
			c.GetInt("id"), code, diagnostic.Error(),
		))
	}
	paymentAPIErrorWithCode(c, status, code, "", params)
}

func paymentSettingsFieldAPIError(c *gin.Context, field string, diagnostic error) {
	paymentSettingsAPIErrorWithParams(c, http.StatusBadRequest, "payment_settings_field_invalid", gin.H{
		"field": field,
	}, diagnostic)
}

// ServerAddress remains a shared site setting. Payment callbacks and return
// URLs never trust it when a gateway is enabled; they use the payment-owned
// CustomCallbackAddress instead. Pricing ratios and every other payment-owned
// key must use the atomic, step-up-protected endpoint.
func isPaymentAtomicOnlyOptionKey(key string) bool {
	if key == "StripeCredentialAccountId" || key == "StripeCredentialLivemode" ||
		key == model.PaymentConfigurationVersionOptionKey || key == model.PaymentRouteCatalogVersionOptionKey {
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
		paymentSettingsAPIError(c, http.StatusForbidden, "payment_settings_auth_required", nil)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPaymentSettingsRequestBytes)
	var request paymentSettingsUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil ||
		request.ExpectedVersion <= 0 ||
		(len(request.Options) == 0 && len(request.ClearSecrets) == 0 && len(request.RevokePreviousCredentials) == 0 && len(request.DisableCurrentCredentials) == 0) ||
		len(request.Options) > len(paymentSettingsAllowedKeys) || len(request.ClearSecrets) > len(paymentSettingsAllowedKeys) ||
		len(request.RevokePreviousCredentials) > 3 || len(request.DisableCurrentCredentials) > 1 ||
		len(request.RevokePreviousCredentials) > 0 && len(request.DisableCurrentCredentials) > 0 {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", errors.New("invalid request shape"))
		return
	}
	emergencyProviders := append(append([]string(nil), request.RevokePreviousCredentials...), request.DisableCurrentCredentials...)
	if err := validatePaymentSettingsMutationScope(request.Options, request.ClearSecrets, emergencyProviders); err != nil {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_scope_conflict", err)
		return
	}
	if len(request.DisableCurrentCredentials) > 0 && (len(request.Options) > 0 || len(request.ClearSecrets) > 0) {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_scope_conflict", errors.New("current-only credential disable must be a dedicated operation"))
		return
	}
	revokePrevious := make(map[string]bool, len(request.RevokePreviousCredentials)+len(request.DisableCurrentCredentials))
	for _, rawProvider := range request.RevokePreviousCredentials {
		provider := strings.ToLower(strings.TrimSpace(rawProvider))
		if (provider != model.PaymentProviderEpay && provider != model.PaymentProviderStripe && provider != model.PaymentProviderXorPay) || revokePrevious[provider] {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", errors.New("invalid previous payment credential revocation request"))
			return
		}
		revokePrevious[provider] = true
	}
	disableCurrent := make(map[string]bool, len(request.DisableCurrentCredentials))
	for _, rawProvider := range request.DisableCurrentCredentials {
		provider := strings.ToLower(strings.TrimSpace(rawProvider))
		if (provider != model.PaymentProviderCreem && provider != model.PaymentProviderWaffo && provider != model.PaymentProviderWaffoPancake) || disableCurrent[provider] {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", errors.New("invalid current-only payment credential disable request"))
			return
		}
		disableCurrent[provider] = true
		revokePrevious[provider] = true
	}
	if rawValue, exists := request.Options["StripeCheckoutAllowedHosts"]; exists {
		value, valueErr := paymentOptionString(rawValue)
		if valueErr != nil {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_stripe_checkout_hosts_invalid", valueErr)
			return
		}
		if _, normalizeErr := setting.NormalizeStripeCheckoutAllowedHosts(value); normalizeErr != nil {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_stripe_checkout_hosts_invalid", normalizeErr)
			return
		}
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		paymentSettingsAPIError(c, http.StatusServiceUnavailable, "payment_settings_sync_failed", err)
		return
	}
	values := make(map[string]string, len(request.Options)+len(request.ClearSecrets))
	for key, rawValue := range request.Options {
		if _, ok := paymentSettingsAllowedKeys[key]; !ok {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", fmt.Errorf("unsupported payment setting %q", key))
			return
		}
		value, err := paymentOptionString(rawValue)
		if err != nil {
			paymentSettingsFieldAPIError(c, key, fmt.Errorf("invalid value for %s: %w", key, err))
			return
		}
		if key == "StripeCheckoutAllowedHosts" {
			value, err = setting.NormalizeStripeCheckoutAllowedHosts(value)
			if err != nil {
				paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_stripe_checkout_hosts_invalid", err)
				return
			}
		}
		if isWriteOnlyPaymentSetting(key) && strings.TrimSpace(value) == "" && key != "StripeWebhookSecretPrevious" {
			continue
		}
		if err := validatePaymentSettingValue(key, value); err != nil {
			paymentSettingsFieldAPIError(c, key, err)
			return
		}
		values[key] = value
	}
	seenClears := make(map[string]struct{}, len(request.ClearSecrets))
	for _, rawKey := range request.ClearSecrets {
		key := strings.TrimSpace(rawKey)
		_, allowed := paymentSettingsAllowedKeys[key]
		if _, duplicate := seenClears[key]; duplicate || !allowed || !isWriteOnlyPaymentSetting(key) {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", errors.New("invalid payment secret clear request"))
			return
		}
		if _, alsoUpdated := request.Options[key]; alsoUpdated {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", errors.New("payment secret update and clear conflict"))
			return
		}
		seenClears[key] = struct{}{}
		values[key] = ""
	}
	if disableCurrent[model.PaymentProviderCreem] {
		values["CreemApiKey"] = ""
		values["CreemWebhookSecret"] = ""
	}
	if disableCurrent[model.PaymentProviderWaffo] {
		values["WaffoEnabled"] = "false"
		unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
		waffoSandbox := setting.WaffoSandbox
		unlockPaymentConfiguration()
		if waffoSandbox {
			values["WaffoSandboxApiKey"] = ""
			values["WaffoSandboxPrivateKey"] = ""
			values["WaffoSandboxPublicCert"] = ""
		} else {
			values["WaffoApiKey"] = ""
			values["WaffoPrivateKey"] = ""
			values["WaffoPublicCert"] = ""
		}
	}
	if disableCurrent[model.PaymentProviderWaffoPancake] {
		values["WaffoPancakePrivateKey"] = ""
		values["WaffoPancakeStoreID"] = ""
	}
	if !disableCurrent[model.PaymentProviderWaffoPancake] {
		if err := validateWaffoPancakeSettingsMutation(values); err != nil {
			paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", err)
			return
		}
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if len(request.Reason) > 512 || len(revokePrevious) > 0 && len(request.Reason) < 8 {
		paymentSettingsAPIError(c, http.StatusBadRequest, "payment_settings_invalid", errors.New("payment credential revocation reason length is invalid"))
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
			paymentSettingsAPIError(c, http.StatusServiceUnavailable, "payment_settings_secret_storage_unavailable", errors.New("payment secret encryption is unavailable"))
			return
		}
	}
	stripeCheckoutSettingsTouched := false
	for key := range values {
		switch key {
		case "StripeApiSecret", "StripePriceId", "StripeCurrency", "StripeAccountId", "StripeCheckoutAllowedHosts":
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
	stripeCheckoutAllowedHosts := setting.StripeCheckoutAllowedHosts
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
	if next, exists := values["StripeCheckoutAllowedHosts"]; exists {
		stripeCheckoutAllowedHosts = next
	}
	stripeIdentityIncomplete := strings.TrimSpace(stripeCredentialAccountID) == "" || strings.TrimSpace(stripeCredentialMode) == ""
	if stripeAPISecretChanged || stripeCheckoutSettingsTouched && stripeIdentityIncomplete {
		if strings.TrimSpace(stripeSecret) != "" {
			credentialMode, modeErr := service.StripeCredentialMode(stripeSecret)
			if modeErr != nil {
				paymentSettingsAPIError(c, http.StatusUnprocessableEntity, "payment_settings_stripe_verification_failed", modeErr)
				return
			}
			if !setting.StripeCredentialModeAllowed(credentialMode) {
				paymentSettingsAPIError(c, http.StatusConflict, "payment_settings_stripe_test_mode_disabled", errors.New("Stripe test mode is disabled"))
				return
			}
			accountID, resolveErr := service.ResolveStripeCredentialAccount(c.Request.Context(), stripeSecret)
			if resolveErr != nil {
				paymentSettingsAPIError(c, http.StatusUnprocessableEntity, "payment_settings_stripe_verification_failed", resolveErr)
				return
			}
			resolvedStripeAccountID = accountID
			resolvedStripeCredentialMode = credentialMode
			stripeCredentialAccountID = accountID
			stripeCredentialMode = credentialMode
		}
	}
	if err := validateStripeSandboxSettingsMutation(values, stripeCredentialMode); err != nil {
		paymentSettingsAPIError(c, http.StatusConflict, "payment_settings_stripe_test_mode_disabled", err)
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
				stripePriceID, stripeCurrency, stripeCredentialMode, stripeCheckoutAllowedHosts,
			)
			if verifyErr != nil {
				paymentSettingsAPIError(c, http.StatusUnprocessableEntity, "payment_settings_stripe_verification_failed", verifyErr)
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
		paymentSettingsAPIError(c, http.StatusConflict, "payment_settings_rotation_blocked", err)
		return
	}
	if err := validateInFlightPaymentConfigurationChanges(values, revokePrevious); err != nil {
		paymentSettingsAPIError(c, http.StatusConflict, "payment_settings_change_blocked", err)
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
		paymentSettingsAPIError(c, http.StatusConflict, "payment_settings_version_conflict", err)
		return
	}
	if errors.Is(err, model.ErrPaymentConfigurationPrecondition) {
		paymentSettingsAPIError(c, http.StatusConflict, "payment_settings_change_blocked", err)
		return
	}
	if err != nil {
		paymentSettingsAPIError(c, http.StatusInternalServerError, "payment_settings_save_failed", err)
		return
	}
	recordManageAudit(c, "payment.settings.update", map[string]interface{}{
		"keys": keys, "revoked_previous_credentials": request.RevokePreviousCredentials,
		"disabled_current_credentials": request.DisableCurrentCredentials, "reason": request.Reason,
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

const (
	retainedConfigurationDependencyActive   = "active"
	retainedConfigurationDependencyRecovery = "recovery"
)

type retainedPaymentConfigurationGuard struct {
	provider   string
	key        string
	dependency string
}

func retainedPaymentConfigurationGuards(values map[string]string, emergencyRevocations map[string]bool) []retainedPaymentConfigurationGuard {
	guards := make([]retainedPaymentConfigurationGuard, 0, 12)
	appendIfChanged := func(provider, key, current, dependency string) {
		if emergencyRevocations[provider] {
			return
		}
		if next, exists := values[key]; exists && next != current {
			guards = append(guards, retainedPaymentConfigurationGuard{
				provider: provider, key: key, dependency: dependency,
			})
		}
	}

	appendIfChanged(model.PaymentProviderCreem, "CreemApiKey", setting.CreemApiKey, retainedConfigurationDependencyActive)
	appendIfChanged(model.PaymentProviderCreem, "CreemWebhookSecret", setting.CreemWebhookSecret, retainedConfigurationDependencyRecovery)
	appendIfChanged(model.PaymentProviderCreem, "CreemTestMode", strconv.FormatBool(setting.CreemTestMode), retainedConfigurationDependencyRecovery)

	appendIfChanged(model.PaymentProviderWaffo, "WaffoMerchantId", setting.WaffoMerchantId, retainedConfigurationDependencyActive)
	appendIfChanged(model.PaymentProviderWaffo, "WaffoSandbox", strconv.FormatBool(setting.WaffoSandbox), retainedConfigurationDependencyRecovery)
	appendIfChanged(model.PaymentProviderWaffo, "WaffoNotifyUrl", setting.WaffoNotifyUrl, retainedConfigurationDependencyRecovery)
	appendIfChanged(model.PaymentProviderWaffo, "WaffoReturnUrl", setting.WaffoReturnUrl, retainedConfigurationDependencyActive)
	if setting.WaffoSandbox {
		appendIfChanged(model.PaymentProviderWaffo, "WaffoSandboxApiKey", setting.WaffoSandboxApiKey, retainedConfigurationDependencyRecovery)
		appendIfChanged(model.PaymentProviderWaffo, "WaffoSandboxPrivateKey", setting.WaffoSandboxPrivateKey, retainedConfigurationDependencyRecovery)
		appendIfChanged(model.PaymentProviderWaffo, "WaffoSandboxPublicCert", setting.WaffoSandboxPublicCert, retainedConfigurationDependencyRecovery)
	} else {
		appendIfChanged(model.PaymentProviderWaffo, "WaffoApiKey", setting.WaffoApiKey, retainedConfigurationDependencyRecovery)
		appendIfChanged(model.PaymentProviderWaffo, "WaffoPrivateKey", setting.WaffoPrivateKey, retainedConfigurationDependencyRecovery)
		appendIfChanged(model.PaymentProviderWaffo, "WaffoPublicCert", setting.WaffoPublicCert, retainedConfigurationDependencyRecovery)
	}

	appendIfChanged(model.PaymentProviderWaffoPancake, "WaffoPancakeMerchantID", setting.WaffoPancakeMerchantID, retainedConfigurationDependencyActive)
	appendIfChanged(model.PaymentProviderWaffoPancake, "WaffoPancakePrivateKey", setting.WaffoPancakePrivateKey, retainedConfigurationDependencyActive)
	appendIfChanged(model.PaymentProviderWaffoPancake, "WaffoPancakeReturnURL", setting.WaffoPancakeReturnURL, retainedConfigurationDependencyActive)
	appendIfChanged(model.PaymentProviderWaffoPancake, "WaffoPancakeTestMode", strconv.FormatBool(setting.WaffoPancakeTestMode), retainedConfigurationDependencyRecovery)
	appendIfChanged(model.PaymentProviderWaffoPancake, "WaffoPancakeStoreID", setting.WaffoPancakeStoreID, retainedConfigurationDependencyRecovery)
	return guards
}

func paymentConfigurationPreconditions(values map[string]string, emergencyRevocations map[string]bool) *model.PaymentConfigurationPreconditions {
	preconditions := &model.PaymentConfigurationPreconditions{}
	nextStripeAPISecret, stripeAPISecretSupplied := values["StripeApiSecret"]
	stripeEmergencyAPIClear := emergencyRevocations[model.PaymentProviderStripe] && stripeAPISecretSupplied &&
		strings.TrimSpace(nextStripeAPISecret) == "" && strings.TrimSpace(setting.StripeApiSecret) != ""
	if next, exists := values["CustomCallbackAddress"]; exists && next != operation_setting.CustomCallbackAddress {
		preconditions.RequireNoCallbackDependentOrders = true
		preconditions.RequireNoCallbackDependentProviderOrders = append(
			preconditions.RequireNoCallbackDependentProviderOrders,
			model.PaymentProviderCreem, model.PaymentProviderWaffo, model.PaymentProviderWaffoPancake,
		)
	}
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
	if next, exists := values["StripeCheckoutAllowedHosts"]; exists && next != setting.StripeCheckoutAllowedHosts {
		removesTrustedHost, err := stripeCheckoutAllowedHostsRemoveCurrent(setting.StripeCheckoutAllowedHosts, next)
		preconditions.RequireNoActiveStripeOrdersForHostRemoval = err != nil || removesTrustedHost
	}
	if current := strings.TrimSpace(setting.StripeCredentialLivemode); current != "" {
		if next, exists := values["StripeCredentialLivemode"]; exists && next != current {
			preconditions.RequireNoStripeHistory = true
		}
	}
	if next, changingSecret := values["StripeApiSecret"]; changingSecret && next != setting.StripeApiSecret &&
		!stripeEmergencyAPIClear &&
		(strings.TrimSpace(setting.StripeCredentialAccountId) == "" || strings.TrimSpace(setting.StripeCredentialLivemode) == "") {
		preconditions.RequireNoStripeHistory = true
	}
	if next, exists := values["StripeApiSecret"]; exists && strings.TrimSpace(next) == "" &&
		strings.TrimSpace(setting.StripeApiSecret) != "" && !stripeEmergencyAPIClear {
		preconditions.RequireNoStripeHistory = true
	}
	if next, exists := values["StripeWebhookSecret"]; exists && next != setting.StripeWebhookSecret && setting.StripeWebhookSecret != "" &&
		!emergencyRevocations[model.PaymentProviderStripe] {
		preconditions.RequireStripeWebhookOverlap = true
		if next == "" {
			preconditions.RequireNoStripeHistory = true
		}
	}
	for _, guard := range retainedPaymentConfigurationGuards(values, emergencyRevocations) {
		if guard.dependency == retainedConfigurationDependencyRecovery {
			preconditions.RequireNoCallbackDependentProviderOrders = append(
				preconditions.RequireNoCallbackDependentProviderOrders, guard.provider,
			)
		} else {
			preconditions.RequireNoActiveProviderOrders = append(preconditions.RequireNoActiveProviderOrders, guard.provider)
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

	if revokePrevious[model.PaymentProviderCreem] {
		if strings.TrimSpace(setting.CreemApiKey) == "" && strings.TrimSpace(setting.CreemWebhookSecret) == "" {
			return nil, errors.New("no current Creem credential is available to disable")
		}
		values["CreemApiKey"] = ""
		values["CreemWebhookSecret"] = ""
		revocations = append(revocations, model.PaymentCredentialRevocation{
			Provider: model.PaymentProviderCreem, Generation: 0, ValidBefore: now, AllActiveOrders: true,
		})
	}
	if revokePrevious[model.PaymentProviderWaffo] {
		if setting.WaffoSandbox {
			if strings.TrimSpace(setting.WaffoSandboxApiKey) == "" && strings.TrimSpace(setting.WaffoSandboxPrivateKey) == "" &&
				strings.TrimSpace(setting.WaffoSandboxPublicCert) == "" {
				return nil, errors.New("no current Waffo sandbox credential is available to disable")
			}
			values["WaffoSandboxApiKey"] = ""
			values["WaffoSandboxPrivateKey"] = ""
			values["WaffoSandboxPublicCert"] = ""
		} else {
			if strings.TrimSpace(setting.WaffoApiKey) == "" && strings.TrimSpace(setting.WaffoPrivateKey) == "" &&
				strings.TrimSpace(setting.WaffoPublicCert) == "" {
				return nil, errors.New("no current Waffo production credential is available to disable")
			}
			values["WaffoApiKey"] = ""
			values["WaffoPrivateKey"] = ""
			values["WaffoPublicCert"] = ""
		}
		values["WaffoEnabled"] = "false"
		revocations = append(revocations, model.PaymentCredentialRevocation{
			Provider: model.PaymentProviderWaffo, Generation: 0, ValidBefore: now, AllActiveOrders: true,
		})
	}
	if revokePrevious[model.PaymentProviderWaffoPancake] {
		if strings.TrimSpace(setting.WaffoPancakePrivateKey) == "" && strings.TrimSpace(setting.WaffoPancakeStoreID) == "" {
			return nil, errors.New("no current Waffo Pancake credential is available to disable")
		}
		values["WaffoPancakePrivateKey"] = ""
		values["WaffoPancakeStoreID"] = ""
		revocations = append(revocations, model.PaymentCredentialRevocation{
			Provider: model.PaymentProviderWaffoPancake, Generation: 0, ValidBefore: now, AllActiveOrders: true,
		})
	}
	return revocations, nil
}

func validateInFlightPaymentConfigurationChanges(values map[string]string, emergencyRevocations map[string]bool) error {
	activeStatuses := []string{model.PaymentOrderStatusPending, model.PaymentOrderStatusProcessing, model.PaymentOrderStatusManualReview}
	nextStripeAPISecret, stripeAPISecretSupplied := values["StripeApiSecret"]
	stripeEmergencyAPIClear := emergencyRevocations[model.PaymentProviderStripe] && stripeAPISecretSupplied &&
		strings.TrimSpace(nextStripeAPISecret) == "" && strings.TrimSpace(setting.StripeApiSecret) != ""
	if next, exists := values["CustomCallbackAddress"]; exists && next != operation_setting.CustomCallbackAddress {
		for _, provider := range []string{
			model.PaymentProviderEpay, model.PaymentProviderStripe, model.PaymentProviderXorPay,
			model.PaymentProviderCreem, model.PaymentProviderWaffo, model.PaymentProviderWaffoPancake,
		} {
			count, err := model.CountPaymentOrdersDependingOnCallbackOrigin(provider, time.Now().Unix())
			if err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("CustomCallbackAddress cannot be changed while %s payment orders still depend on the current callback origin", provider)
			}
		}
	}
	for _, guard := range retainedPaymentConfigurationGuards(values, emergencyRevocations) {
		var count int64
		var err error
		if guard.dependency == retainedConfigurationDependencyRecovery {
			count, err = model.CountPaymentOrdersDependingOnCallbackOrigin(guard.provider, time.Now().Unix())
		} else {
			count, err = model.CountActivePaymentOrdersForProvider(guard.provider)
		}
		if err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("%s cannot be changed while %s payment orders still depend on the current configuration", guard.key, guard.provider)
		}
	}
	if next, exists := values["StripeCheckoutAllowedHosts"]; exists && next != setting.StripeCheckoutAllowedHosts {
		removesTrustedHost, err := stripeCheckoutAllowedHostsRemoveCurrent(setting.StripeCheckoutAllowedHosts, next)
		if err != nil {
			return err
		}
		if removesTrustedHost {
			count, countErr := model.CountPaymentOrdersForProvider(model.PaymentProviderStripe, activeStatuses)
			if countErr != nil {
				return countErr
			}
			if count > 0 {
				return errors.New("Stripe custom Checkout hosts cannot be removed while unfinished Stripe payment orders may still depend on them")
			}
		}
	}
	if next, exists := values["StripeApiSecret"]; exists && strings.TrimSpace(next) == "" &&
		strings.TrimSpace(setting.StripeApiSecret) != "" && !stripeEmergencyAPIClear {
		hasHistory, err := model.HasStripeAccountBoundData()
		if err != nil {
			return err
		}
		if hasHistory {
			return errors.New("StripeApiSecret cannot be cleared while durable Stripe data exists; rotate to a verified credential for the same account or use the emergency credential revocation flow with a reason")
		}
	}
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
		!stripeEmergencyAPIClear &&
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

func stripeCheckoutAllowedHostsRemoveCurrent(current, next string) (bool, error) {
	currentHosts, err := setting.StripeCheckoutAllowedHostSet(current)
	if err != nil {
		return false, err
	}
	nextHosts, err := setting.StripeCheckoutAllowedHostSet(next)
	if err != nil {
		return false, err
	}
	for host := range currentHosts {
		if _, retained := nextHosts[host]; !retained {
			return true, nil
		}
	}
	return false, nil
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
		"CreemApiKey", "CreemWebhookSecret", "WaffoApiKey", "WaffoPrivateKey", "WaffoSandboxApiKey", "WaffoSandboxPrivateKey",
		"WaffoPancakePrivateKey":
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
	case "WaffoPancakeReturnURL":
		if value == "" {
			return nil
		}
		return validatePaymentAdminURL(value, false)
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
	case "Price", "StripeUnitPrice", "XorPayUnitPrice", "WaffoUnitPrice", "WaffoPancakeUnitPrice":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed <= 0 || parsed > 1_000_000 {
			return fmt.Errorf("%s must be a positive finite number", key)
		}
	case "MinTopUp", "StripeMinTopUp", "XorPayMinTopUp", "WaffoMinTopUp", "WaffoPancakeMinTopUp":
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
	case "StripeCheckoutAllowedHosts":
		_, err := setting.NormalizeStripeCheckoutAllowedHosts(value)
		return err
	case "StripePromotionCodesEnabled":
		if value != "false" {
			return errors.New("Stripe promotion codes are disabled for server-quoted payments")
		}
	case "CreemTestMode", "WaffoEnabled", "WaffoSandbox", "WaffoPancakeTestMode":
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
		if err := common.UnmarshalJsonStr(value, &methods); err != nil || len(methods) > 3 {
			return errors.New("XORPay enabled methods are invalid")
		}
		seen := map[string]struct{}{}
		for _, method := range methods {
			if method != setting.XorPayMethodNative && method != setting.XorPayMethodAlipay && method != setting.XorPayMethodJSAPI {
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
	case "CreemProducts":
		if _, err := parseValidatedCreemProducts(value); err != nil {
			return err
		}
	case "WaffoPayMethods":
		var decoded interface{}
		if err := common.UnmarshalJsonStr(value, &decoded); err != nil {
			return fmt.Errorf("%s must be valid JSON", key)
		}
	case "WaffoWebRedirectHosts":
		if _, err := service.ParseWaffoWebRedirectHosts(value); err != nil {
			return err
		}
	case "WaffoAppRedirectSchemes":
		if _, err := service.ParseWaffoAppRedirectSchemes(value); err != nil {
			return err
		}
	default:
		if len(value) > 8192 {
			return fmt.Errorf("%s is too large", key)
		}
	}
	return nil
}

func validateWaffoPancakeSettingsMutation(values map[string]string) error {
	touched := false
	for key := range values {
		if strings.HasPrefix(key, "WaffoPancake") {
			touched = true
			break
		}
	}
	if !touched {
		return nil
	}
	unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
	merchantID := setting.WaffoPancakeMerchantID
	privateKey := setting.WaffoPancakePrivateKey
	returnURL := setting.WaffoPancakeReturnURL
	storeID := setting.WaffoPancakeStoreID
	productID := setting.WaffoPancakeProductID
	unlockPaymentConfiguration()
	if next, ok := values["WaffoPancakeMerchantID"]; ok {
		merchantID = next
	}
	if next, ok := values["WaffoPancakePrivateKey"]; ok {
		privateKey = next
	}
	if next, ok := values["WaffoPancakeReturnURL"]; ok {
		returnURL = next
	}
	if next, ok := values["WaffoPancakeStoreID"]; ok {
		storeID = next
	}
	if next, ok := values["WaffoPancakeProductID"]; ok {
		productID = next
	}
	if strings.TrimSpace(privateKey) == "" {
		return nil
	}
	return service.ValidateWaffoPancakeConfig(merchantID, privateKey, returnURL, storeID, productID)
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
	creemEnvironment := "prod"
	if setting.CreemTestMode {
		creemEnvironment = "test"
	}
	waffoEnvironment := "prod"
	waffoCredentialConfigured := strings.TrimSpace(setting.WaffoApiKey) != "" && isWaffoWebhookConfiguredLocked()
	if setting.WaffoSandbox {
		waffoEnvironment = "sandbox"
		waffoCredentialConfigured = strings.TrimSpace(setting.WaffoSandboxApiKey) != "" && isWaffoWebhookConfiguredLocked()
	}
	pancakeEnvironment := "prod"
	if setting.WaffoPancakeTestMode {
		pancakeEnvironment = "test"
	}
	pancakeConfigured := strings.TrimSpace(setting.WaffoPancakeMerchantID) != "" &&
		strings.TrimSpace(setting.WaffoPancakePrivateKey) != "" &&
		strings.TrimSpace(setting.WaffoPancakeStoreID) != "" &&
		strings.TrimSpace(setting.WaffoPancakeProductID) != ""
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
		"creem": map[string]interface{}{
			"ready":                       isCreemTopUpEnabledLocked(),
			"configured":                  strings.TrimSpace(setting.CreemApiKey) != "" && strings.TrimSpace(setting.CreemWebhookSecret) != "",
			"webhook_configured":          isCreemWebhookConfiguredLocked(),
			"environment":                 creemEnvironment,
			"credential_model":            "current_only",
			"emergency_disable_supported": true,
			"secrets_encrypted":           encryptionReady,
		},
		"waffo": map[string]interface{}{
			"ready":                       isWaffoTopUpEnabledLocked(),
			"configured":                  strings.TrimSpace(setting.WaffoMerchantId) != "" && waffoCredentialConfigured,
			"webhook_configured":          isWaffoWebhookConfiguredLocked(),
			"enabled":                     setting.WaffoEnabled,
			"environment":                 waffoEnvironment,
			"credential_model":            "current_only",
			"emergency_disable_supported": true,
			"secrets_encrypted":           encryptionReady,
		},
		"waffo_pancake": map[string]interface{}{
			"ready":                       isWaffoPancakeTopUpEnabledLocked(),
			"configured":                  pancakeConfigured,
			"webhook_configured":          isWaffoPancakeWebhookConfiguredLocked(),
			"test_mode":                   setting.WaffoPancakeTestMode,
			"environment":                 pancakeEnvironment,
			"unit_price":                  setting.WaffoPancakeUnitPrice,
			"min_top_up":                  setting.WaffoPancakeMinTopUp,
			"credential_model":            "current_only",
			"emergency_disable_supported": true,
			"secrets_encrypted":           encryptionReady,
		},
	}
}
