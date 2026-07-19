package service

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stripe/stripe-go/v86"
	stripeclient "github.com/stripe/stripe-go/v86/client"
	"github.com/stripe/stripe-go/v86/webhook"
)

const maxStripeWebhookBytes = 1 << 20

const maxStripeVerificationResponseBytes = 64 << 10

var (
	stripeAccountIdentityEndpoint             = "https://api.stripe.com/v1/account"
	stripeCheckoutCapabilityEndpoint          = "https://api.stripe.com/v1/checkout/sessions"
	stripeAccountIDPattern                    = regexp.MustCompile(`^acct_[A-Za-z0-9]{4,123}$`)
	stripeConfigurationVerificationHTTPClient = newStripeConfigurationVerificationHTTPClient()
	stripeLegacyWebhookModeWarn               sync.Once
	errStripeCheckoutConfirmation             = errors.New("Stripe Checkout Session could not be confirmed against the configured account")
)

type stripePaymentProvider struct{}

type stripeAccountParams interface {
	SetStripeAccount(string)
}

func setStripeAccount(params stripeAccountParams, accountID string) {
	if accountID = strings.TrimSpace(accountID); accountID != "" {
		params.SetStripeAccount(accountID)
	}
}

func init() {
	RegisterPaymentProvider(&stripePaymentProvider{})
}

func (*stripePaymentProvider) Name() string { return model.PaymentProviderStripe }

func (*stripePaymentProvider) CredentialGeneration() int64 {
	return setting.StripeWebhookCredentialGeneration
}

// ResolveStripeCredentialAccount verifies an API credential against Stripe and
// returns the authenticating platform/direct account. The endpoint is fixed in
// production, redirects are forbidden, response bodies are bounded, and the
// credential is never copied into logs or persisted outside encrypted options.
func ResolveStripeCredentialAccount(ctx context.Context, secret string) (string, error) {
	secret = strings.TrimSpace(secret)
	if !validStripeSecret(secret) {
		return "", errors.New("invalid Stripe API secret")
	}
	if _, err := stripeCredentialModeForUse(secret); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, stripeAccountIdentityEndpoint, nil)
	if err != nil {
		return "", err
	}
	request.SetBasicAuth(secret, "")
	request.Header.Set("Accept", "application/json")
	response, err := stripeConfigurationVerificationHTTPClient.Do(request)
	if err != nil {
		return "", errors.New("failed to verify Stripe account identity")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Stripe account verification returned HTTP %d", response.StatusCode)
	}
	var account struct {
		ID string `json:"id"`
	}
	if err := common.DecodeJson(io.LimitReader(response.Body, maxStripeVerificationResponseBytes), &account); err != nil {
		return "", errors.New("invalid Stripe account verification response")
	}
	account.ID = strings.TrimSpace(account.ID)
	if !stripeAccountIDPattern.MatchString(account.ID) {
		return "", errors.New("Stripe account verification response is missing a valid account ID")
	}
	return account.ID, nil
}

func newStripeConfigurationVerificationHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Stripe configuration verification redirects are not allowed")
		},
	}
}

func (*stripePaymentProvider) ValidateMethod(method string) error {
	if method != model.PaymentMethodStripe {
		return fmt.Errorf("unsupported stripe payment method: %s", method)
	}
	return nil
}

func (p *stripePaymentProvider) Create(ctx context.Context, order *model.PaymentOrder) (*PaymentStart, error) {
	if order == nil {
		return nil, errors.New("payment order is required")
	}
	if err := p.ValidateMethod(order.PaymentMethod); err != nil {
		return nil, err
	}
	if !validStripeSecret(setting.StripeApiSecret) {
		return nil, errors.New("invalid stripe API secret")
	}
	credentialMode, err := stripeCredentialModeForUse(setting.StripeApiSecret)
	if err != nil {
		return nil, err
	}
	if credentialMode != setting.StripeCredentialLivemode || setting.StripeWebhookCredentialLivemode != credentialMode {
		return nil, errors.New("stripe API credential mode is not verified")
	}
	credentialLivemode := credentialMode == "live"
	if order.ProviderLivemode == nil || *order.ProviderLivemode != credentialLivemode {
		return nil, errors.New("stripe order credential mode does not match the verified configuration")
	}
	expectedFingerprint := StripeCheckoutConfigurationFingerprint(
		setting.StripeApiSecret, setting.StripeCredentialAccountId, setting.StripeAccountId,
		setting.StripePriceId, setting.StripeCurrency, setting.StripeCredentialLivemode,
	)
	if expectedFingerprint == "" || setting.StripeConfigurationVerifiedFingerprint != expectedFingerprint {
		return nil, errors.New("stripe checkout configuration has not been verified")
	}
	user, err := model.GetUserById(order.UserID, false)
	if err != nil || user == nil {
		return nil, errors.New("payment order user does not exist")
	}
	stripeCustomer, err := model.StripeCustomerForCheckout(order.UserID, user.StripeCustomer)
	if err != nil {
		if errors.Is(err, model.ErrPaymentManualReview) {
			return nil, fmt.Errorf("%w: Stripe customer ownership is ambiguous", model.ErrPaymentManualReview)
		}
		return nil, err
	}
	accountID := strings.TrimSpace(setting.StripeAccountId)
	template, err := getStripePriceTemplate(ctx, setting.StripeApiSecret, accountID, setting.StripePriceId, order.Currency)
	if err != nil {
		return nil, err
	}
	api := stripeclient.New(setting.StripeApiSecret, nil)
	successURL, cancelURL, err := stripeCheckoutReturnURLs(order)
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(30 * time.Minute).Unix()
	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(order.TradeNo),
		SuccessURL:        stripe.String(successURL),
		CancelURL:         stripe.String(cancelURL),
		LineItems: []*stripe.CheckoutSessionLineItemParams{{
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:   stripe.String(strings.ToLower(order.Currency)),
				Product:    stripe.String(template.Product.ID),
				UnitAmount: stripe.Int64(order.ExpectedAmountMinor),
			},
			Quantity: stripe.Int64(1),
		}},
		Mode:                stripe.String(string(stripe.CheckoutSessionModePayment)),
		AllowPromotionCodes: stripe.Bool(false),
		ExpiresAt:           stripe.Int64(expiresAt),
		PaymentIntentData:   &stripe.CheckoutSessionPaymentIntentDataParams{},
	}
	params.Context = ctx
	params.SetIdempotencyKey("payment:" + order.TradeNo)
	setStripeAccount(params, accountID)
	params.AddMetadata("trade_no", order.TradeNo)
	params.AddMetadata("order_kind", order.OrderKind)
	params.AddMetadata("pricing_digest", model.PaymentPayloadDigest(order.PricingSnapshot))
	params.PaymentIntentData.AddMetadata("trade_no", order.TradeNo)
	params.PaymentIntentData.AddMetadata("order_kind", order.OrderKind)
	if stripeCustomer != "" {
		params.Customer = stripe.String(stripeCustomer)
	} else {
		if user.Email != "" {
			params.CustomerEmail = stripe.String(user.Email)
		}
		params.CustomerCreation = stripe.String(string(stripe.CheckoutSessionCustomerCreationAlways))
	}
	session, err := api.CheckoutSessions.New(params)
	if err != nil {
		return nil, normalizeStripeCreateError(err)
	}
	if session == nil || session.ID == "" || session.URL == "" {
		return nil, fmt.Errorf("%w: stripe returned an incomplete checkout session", ErrPaymentStateUnknown)
	}
	if err := validateStripeCheckoutURL(session.URL); err != nil {
		return nil, err
	}
	providerOrderKey := "stripe:" + session.ID
	return &PaymentStart{
		Flow: PaymentFlowHostedRedirect, URL: session.URL, ExpiresAt: expiresAt,
		ProviderOrderKey: providerOrderKey,
	}, nil
}

func VerifyStripeCheckoutConfiguration(ctx context.Context, secret, credentialAccountID, connectedAccountID, priceID, currency, mode string) (string, error) {
	secret = strings.TrimSpace(secret)
	credentialAccountID = strings.TrimSpace(credentialAccountID)
	connectedAccountID = strings.TrimSpace(connectedAccountID)
	priceID = strings.TrimSpace(priceID)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	mode = strings.ToLower(strings.TrimSpace(mode))
	credentialMode, err := stripeCredentialModeForUse(secret)
	if err != nil {
		return "", err
	}
	if credentialMode != mode || !stripeAccountIDPattern.MatchString(credentialAccountID) {
		return "", errors.New("Stripe credential identity or test/live mode is invalid")
	}
	if connectedAccountID != "" && !stripeAccountIDPattern.MatchString(connectedAccountID) {
		return "", errors.New("Stripe connected account ID is invalid")
	}
	if _, err := getStripePriceTemplate(ctx, secret, connectedAccountID, priceID, currency); err != nil {
		return "", err
	}
	if err := verifyStripeCheckoutWritePermission(ctx, secret, connectedAccountID, priceID); err != nil {
		return "", err
	}
	fingerprint := StripeCheckoutConfigurationFingerprint(secret, credentialAccountID, connectedAccountID, priceID, currency, mode)
	if fingerprint == "" {
		return "", errors.New("Stripe checkout configuration fingerprint is invalid")
	}
	return fingerprint, nil
}

func StripeCheckoutConfigurationFingerprint(secret, credentialAccountID, connectedAccountID, priceID, currency, mode string) string {
	secret = strings.TrimSpace(secret)
	credentialAccountID = strings.TrimSpace(credentialAccountID)
	connectedAccountID = strings.TrimSpace(connectedAccountID)
	priceID = strings.TrimSpace(priceID)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	mode = strings.ToLower(strings.TrimSpace(mode))
	if !validStripeSecret(secret) || !stripeAccountIDPattern.MatchString(credentialAccountID) ||
		priceID == "" || currency == "" || mode != "test" && mode != "live" {
		return ""
	}
	digest := sha256.New()
	for _, value := range []string{secret, credentialAccountID, connectedAccountID, priceID, currency, mode} {
		_, _ = fmt.Fprintf(digest, "%d:%s|", len(value), value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func getStripePriceTemplate(ctx context.Context, secret, accountID, priceID, currency string) (*stripe.Price, error) {
	secret = strings.TrimSpace(secret)
	accountID = strings.TrimSpace(accountID)
	priceID = strings.TrimSpace(priceID)
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !validStripeSecret(secret) || !strings.HasPrefix(priceID, "price_") || len(priceID) > 128 {
		return nil, errors.New("stripe price template is not configured")
	}
	api := stripeclient.New(secret, nil)
	priceParams := &stripe.PriceParams{}
	priceParams.Context = ctx
	setStripeAccount(priceParams, accountID)
	template, err := api.Prices.Get(priceID, priceParams)
	if err != nil {
		return nil, errors.New("Stripe API key cannot access the configured Price and Checkout account")
	}
	if template == nil || !template.Active || template.Product == nil || template.Product.ID == "" {
		return nil, errors.New("stripe price template is inactive or invalid")
	}
	if !strings.EqualFold(string(template.Currency), currency) {
		return nil, fmt.Errorf("stripe currency mismatch: configured=%s template=%s", currency, template.Currency)
	}
	return template, nil
}

func verifyStripeCheckoutWritePermission(ctx context.Context, secret, accountID, priceID string) error {
	secret = strings.TrimSpace(secret)
	accountID = strings.TrimSpace(accountID)
	priceID = strings.TrimSpace(priceID)
	if !validStripeSecret(secret) || priceID == "" {
		return errors.New("Stripe Checkout permission probe is not configured")
	}
	credentialMode, err := StripeCredentialMode(secret)
	if err != nil {
		return errors.New("Stripe Checkout permission probe has an invalid credential mode")
	}
	probeDigest := sha256.Sum256([]byte(secret + "\x00" + accountID + "\x00" + priceID))
	probeSessionID := "cs_" + credentialMode + "_permission_probe_" + hex.EncodeToString(probeDigest[:16])
	endpoint := strings.TrimRight(stripeCheckoutCapabilityEndpoint, "/") + "/" + url.PathEscape(probeSessionID) + "/expire"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(""))
	if err != nil {
		return err
	}
	request.SetBasicAuth(secret, "")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if accountID != "" {
		request.Header.Set("Stripe-Account", accountID)
	}

	response, err := stripeConfigurationVerificationHTTPClient.Do(request)
	if err != nil {
		return errors.New("failed to verify Stripe Checkout write permission")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxStripeVerificationResponseBytes+1))
	if err != nil || len(body) > maxStripeVerificationResponseBytes {
		return errors.New("invalid Stripe Checkout permission response")
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return errors.New("Stripe API key cannot create Checkout Sessions for the configured account")
	}
	if response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("Stripe Checkout permission probe returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Error struct {
			Type  string `json:"type"`
			Code  string `json:"code"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if err := common.Unmarshal(body, &payload); err != nil || payload.Error.Type != "invalid_request_error" ||
		payload.Error.Code != "resource_missing" || payload.Error.Param != "id" {
		return errors.New("Stripe Checkout permission probe did not reach authenticated resource lookup")
	}
	return nil
}

func stripeCheckoutReturnURLs(order *model.PaymentOrder) (string, string, error) {
	if order == nil {
		return "", "", errors.New("payment order is required")
	}
	if callbackAddress := strings.TrimSpace(operation_setting.CustomCallbackAddress); callbackAddress != "" {
		if err := ValidatePaymentCallbackOrigin(callbackAddress, true); err != nil {
			return "", "", err
		}
	}
	successURL := PaymentReturnURL("/console/topup?payment_result=pending&trade_no=" + url.QueryEscape(order.TradeNo))
	cancelURL := PaymentReturnURL("/console/topup?payment_result=cancelled&trade_no=" + url.QueryEscape(order.TradeNo))
	var snapshot stripeReturnURLSnapshot
	if strings.TrimSpace(order.PricingSnapshot) != "" {
		if err := common.UnmarshalJsonStr(order.PricingSnapshot, &snapshot); err != nil {
			return "", "", errors.New("invalid payment pricing snapshot")
		}
	}
	if strings.TrimSpace(snapshot.SuccessURL) != "" {
		candidate := strings.TrimSpace(snapshot.SuccessURL)
		if len(candidate) > 2048 || common.ValidateRedirectURL(candidate) != nil || ValidateExternalPaymentURL(candidate, true) != nil {
			return "", "", errors.New("Stripe return URL is not an allowed secure destination")
		}
		successURL = candidate
	}
	if strings.TrimSpace(snapshot.CancelURL) != "" {
		candidate := strings.TrimSpace(snapshot.CancelURL)
		if len(candidate) > 2048 || common.ValidateRedirectURL(candidate) != nil || ValidateExternalPaymentURL(candidate, true) != nil {
			return "", "", errors.New("Stripe return URL is not an allowed secure destination")
		}
		cancelURL = candidate
	}
	for _, candidate := range []string{successURL, cancelURL} {
		if len(candidate) > 2048 || ValidateExternalPaymentURL(candidate, true) != nil {
			return "", "", errors.New("Stripe return URL is not an allowed secure destination")
		}
	}
	return successURL, cancelURL, nil
}

func (*stripePaymentProvider) VerifyWebhook(request *http.Request) (*NormalizedPaymentEvent, error) {
	if request == nil {
		return nil, errors.New("request is required")
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxStripeWebhookBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 || len(payload) > maxStripeWebhookBytes {
		return nil, errors.New("invalid stripe webhook body size")
	}
	signature := request.Header.Get("Stripe-Signature")
	event, matchedGeneration, err := constructStripeEvent(payload, signature)
	if err != nil {
		return nil, err
	}
	webhookCredentialMode := setting.StripeWebhookCredentialLivemode
	if webhookCredentialMode == "" && (strings.TrimSpace(setting.StripeWebhookSecret) != "" || setting.StripePreviousWebhookSecretActive()) {
		legacyMode, modeErr := StripeCredentialMode(setting.StripeApiSecret)
		if modeErr == nil && (setting.StripeCredentialLivemode == "" || setting.StripeCredentialLivemode == legacyMode) {
			webhookCredentialMode = legacyMode
			stripeLegacyWebhookModeWarn.Do(func() {
				logger.LogWarn(request.Context(), "Stripe webhook signing secret uses legacy test/live inference; re-save it to persist an explicit mode binding")
			})
		}
	}
	expectedLiveMode := false
	liveModeKnown := true
	switch webhookCredentialMode {
	case "test":
		expectedLiveMode = false
	case "live":
		expectedLiveMode = true
	default:
		liveModeKnown = false
	}
	if !liveModeKnown {
		return nil, errors.New("stripe credential test/live mode is not bound")
	}
	if event.Livemode != expectedLiveMode {
		return nil, errors.New("stripe livemode mismatch")
	}
	configuredAccount := strings.TrimSpace(setting.StripeAccountId)
	eventAccount := strings.TrimSpace(event.Account)
	if configuredAccount == "" {
		if eventAccount != "" {
			return nil, errors.New("unexpected stripe connected-account event")
		}
	} else if eventAccount != configuredAccount {
		return nil, errors.New("stripe connected account mismatch")
	}

	normalized := &NormalizedPaymentEvent{
		Provider:                     model.PaymentProviderStripe,
		EventKey:                     event.ID,
		EventType:                    string(event.Type),
		ProviderCredentialGeneration: matchedGeneration,
		ProviderLivemode:             stripe.Bool(event.Livemode),
		ProviderCreatedAt:            event.Created,
		VerifiedPayload:              append([]byte(nil), payload...),
	}
	dataDigest := model.PaymentPayloadDigest(string(event.Data.Raw))
	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted, stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded,
		stripe.EventTypeCheckoutSessionAsyncPaymentFailed, stripe.EventTypeCheckoutSessionExpired:
		var session stripe.CheckoutSession
		if err := common.Unmarshal(event.Data.Raw, &session); err != nil {
			return nil, err
		}
		isRecurringSession := session.Mode == stripe.CheckoutSessionModeSubscription || session.Subscription != nil && session.Subscription.ID != ""
		if session.ID == "" || !isRecurringSession && session.ClientReferenceID == "" {
			return nil, errors.New("stripe checkout event is missing order identity")
		}
		metadataTradeNo := strings.TrimSpace(session.Metadata["trade_no"])
		if metadataTradeNo != "" && session.ClientReferenceID != "" && metadataTradeNo != session.ClientReferenceID {
			return nil, errors.New("stripe checkout metadata identity mismatch")
		}
		normalized.TradeNo = session.ClientReferenceID
		if normalized.TradeNo == "" {
			normalized.TradeNo = metadataTradeNo
		}
		normalized.ProviderOrderKey = "stripe:" + session.ID
		if session.PaymentIntent != nil && session.PaymentIntent.ID != "" {
			normalized.ProviderPaymentKey = "stripe:" + session.PaymentIntent.ID
		}
		if session.Customer != nil {
			normalized.CustomerID = session.Customer.ID
		}
		normalized.PaidAmountMinor = session.AmountTotal
		normalized.Currency = strings.ToUpper(string(session.Currency))
		normalized.PaymentMethod = model.PaymentMethodStripe
		// Historical recurring Checkout Sessions are inventory-only. Renewals and
		// recurring lifecycle events must never enter the one-time settlement path.
		if !isRecurringSession {
			normalized.Paid = (event.Type == stripe.EventTypeCheckoutSessionCompleted || event.Type == stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded) && session.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid
			normalized.Failed = event.Type == stripe.EventTypeCheckoutSessionAsyncPaymentFailed
			normalized.Expired = event.Type == stripe.EventTypeCheckoutSessionExpired
			normalized.PermanentFailure = normalized.Failed || normalized.Expired
		}
		subscriptionID := ""
		if session.Subscription != nil {
			subscriptionID = session.Subscription.ID
		}
		normalized.NormalizedPayload = common.GetJsonString(map[string]interface{}{
			"event_id":        event.ID,
			"event_type":      event.Type,
			"session_id":      session.ID,
			"trade_no":        session.ClientReferenceID,
			"amount_total":    session.AmountTotal,
			"currency":        session.Currency,
			"payment_status":  session.PaymentStatus,
			"status":          session.Status,
			"mode":            session.Mode,
			"subscription_id": subscriptionID,
			"data_digest":     dataDigest,
		})
	case stripe.EventTypeChargeRefunded:
		var charge stripe.Charge
		if err := common.Unmarshal(event.Data.Raw, &charge); err != nil {
			return nil, err
		}
		paymentIntentID := ""
		if charge.PaymentIntent != nil {
			paymentIntentID = strings.TrimSpace(charge.PaymentIntent.ID)
		}
		if paymentIntentID != "" {
			normalized.ProviderPaymentKey = "stripe:" + paymentIntentID
		} else if charge.ID != "" {
			normalized.ProviderPaymentKey = "stripe:" + charge.ID
		} else {
			return nil, errors.New("stripe refund is missing charge identity")
		}
		normalized.TradeNo = strings.TrimSpace(charge.Metadata["trade_no"])
		normalized.Refunded = true
		normalized.RefundedAmountMinor = charge.AmountRefunded
		normalized.Currency = strings.ToUpper(string(charge.Currency))
		normalized.NormalizedPayload = common.GetJsonString(map[string]interface{}{
			"event_id":        event.ID,
			"event_type":      event.Type,
			"charge_id":       charge.ID,
			"payment_intent":  paymentIntentID,
			"amount_refunded": charge.AmountRefunded,
			"currency":        charge.Currency,
			"data_digest":     dataDigest,
		})
	case stripe.EventTypeChargeDisputeCreated, stripe.EventTypeChargeDisputeClosed:
		var dispute stripe.Dispute
		if err := common.Unmarshal(event.Data.Raw, &dispute); err != nil {
			return nil, err
		}
		paymentIntentID := ""
		if dispute.PaymentIntent != nil {
			paymentIntentID = strings.TrimSpace(dispute.PaymentIntent.ID)
		}
		chargeID := ""
		if dispute.Charge != nil {
			chargeID = strings.TrimSpace(dispute.Charge.ID)
		}
		if paymentIntentID != "" {
			normalized.ProviderPaymentKey = "stripe:" + paymentIntentID
		} else if chargeID != "" {
			normalized.ProviderPaymentKey = "stripe:" + chargeID
		} else {
			return nil, errors.New("stripe dispute is missing charge identity")
		}
		normalized.TradeNo = strings.TrimSpace(dispute.Metadata["trade_no"])
		if normalized.TradeNo == "" && dispute.Charge != nil {
			normalized.TradeNo = strings.TrimSpace(dispute.Charge.Metadata["trade_no"])
		}
		normalized.Disputed = event.Type == stripe.EventTypeChargeDisputeCreated || dispute.Status == stripe.DisputeStatusLost
		normalized.DisputeResolved = event.Type == stripe.EventTypeChargeDisputeClosed
		normalized.DisputeWon = normalized.DisputeResolved && (dispute.Status == stripe.DisputeStatusWon || dispute.Status == stripe.DisputeStatusWarningClosed)
		normalized.ProviderResourceKey = "stripe:" + dispute.ID
		normalized.ProviderState = string(dispute.Status)
		normalized.DisputedAmountMinor = dispute.Amount
		normalized.Currency = strings.ToUpper(string(dispute.Currency))
		normalized.NormalizedPayload = common.GetJsonString(map[string]interface{}{
			"event_id":       event.ID,
			"event_type":     event.Type,
			"dispute_id":     dispute.ID,
			"charge_id":      chargeID,
			"payment_intent": paymentIntentID,
			"amount":         dispute.Amount,
			"currency":       dispute.Currency,
			"status":         dispute.Status,
			"data_digest":    dataDigest,
		})
	default:
		normalized.NormalizedPayload = common.GetJsonString(map[string]interface{}{
			"event_id": event.ID, "event_type": event.Type, "data_digest": dataDigest,
		})
	}
	if normalized.EventKey == "" {
		return nil, errors.New("stripe event ID is missing")
	}
	return normalized, nil
}

func (p *stripePaymentProvider) Query(ctx context.Context, order *model.PaymentOrder) (*NormalizedPaymentEvent, error) {
	if order == nil || order.ProviderOrderKey == nil || !strings.HasPrefix(*order.ProviderOrderKey, "stripe:cs_") {
		return nil, errors.New("stripe order has no checkout session")
	}
	credentialMode, err := stripeCredentialModeForUse(setting.StripeApiSecret)
	if err != nil {
		return nil, err
	}
	credentialLivemode := credentialMode == "live"
	if order.ProviderLivemode == nil || *order.ProviderLivemode != credentialLivemode {
		return nil, errors.New("stripe order credential mode does not match the verified configuration")
	}
	session, err := p.getCheckoutSession(ctx, strings.TrimPrefix(*order.ProviderOrderKey, "stripe:"))
	if err != nil {
		return nil, err
	}
	credentialGeneration := setting.StripeWebhookCredentialGeneration
	if credentialGeneration <= 0 {
		return nil, errors.New("stripe webhook credential generation is invalid")
	}
	providerLivemode := credentialMode == "live"
	result := &NormalizedPaymentEvent{
		Provider:                     model.PaymentProviderStripe,
		ProviderLivemode:             &providerLivemode,
		ProviderCredentialGeneration: credentialGeneration,
		EventType:                    "query",
		TradeNo:                      order.TradeNo,
		ProviderOrderKey:             *order.ProviderOrderKey,
		PaidAmountMinor:              session.AmountTotal,
		Currency:                     strings.ToUpper(string(session.Currency)),
		PaymentMethod:                model.PaymentMethodStripe,
		Paid:                         session.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid,
		Expired:                      session.Status == stripe.CheckoutSessionStatusExpired,
	}
	if session.Customer != nil {
		result.CustomerID = session.Customer.ID
	}
	if session.PaymentIntent != nil && session.PaymentIntent.ID != "" {
		result.ProviderPaymentKey = "stripe:" + session.PaymentIntent.ID
	}
	result.EventKey = model.PaymentEventKey(model.PaymentProviderStripe, "query", result.ProviderOrderKey, order.TradeNo, fmt.Sprintf("%s:%d:%s", session.PaymentStatus, session.AmountTotal, session.Currency))
	return result, nil
}

func (p *stripePaymentProvider) RecoverStart(ctx context.Context, order *model.PaymentOrder) (*PaymentStart, error) {
	if order == nil || order.ProviderOrderKey == nil || !strings.HasPrefix(*order.ProviderOrderKey, "stripe:cs_") {
		return nil, errors.New("stripe order has no checkout session")
	}
	credentialMode, err := stripeCredentialModeForUse(setting.StripeApiSecret)
	if err != nil {
		return nil, err
	}
	credentialLivemode := credentialMode == "live"
	if order.ProviderLivemode == nil || *order.ProviderLivemode != credentialLivemode {
		return nil, errors.New("stripe order credential mode does not match the verified configuration")
	}
	session, err := p.getCheckoutSession(ctx, strings.TrimPrefix(*order.ProviderOrderKey, "stripe:"))
	if err != nil {
		return nil, err
	}
	if session == nil || session.URL == "" {
		return nil, errors.New("stripe checkout session has no recoverable URL")
	}
	if err := validateStripeCheckoutURL(session.URL); err != nil {
		return nil, err
	}
	expiresAt := order.ExpiresAt
	if session.ExpiresAt > 0 {
		expiresAt = session.ExpiresAt
	}
	return &PaymentStart{Flow: PaymentFlowHostedRedirect, URL: session.URL, ExpiresAt: expiresAt}, nil
}

func (p *stripePaymentProvider) getCheckoutSession(ctx context.Context, sessionID string) (*stripe.CheckoutSession, error) {
	if !validStripeSecret(setting.StripeApiSecret) {
		return nil, errors.New("invalid stripe API secret")
	}
	api := stripeclient.New(setting.StripeApiSecret, nil)
	params := &stripe.CheckoutSessionParams{}
	params.Context = ctx
	setStripeAccount(params, setting.StripeAccountId)
	return api.CheckoutSessions.Get(sessionID, params)
}

// confirmPaidCheckoutSession binds entitlement-granting webhook events to the
// account authenticated by the configured Stripe API credential. A webhook
// signing secret does not itself disclose which direct Stripe account issued
// it, so signature verification alone cannot detect an accidentally paired
// secret from another account.
func (p *stripePaymentProvider) confirmPaidCheckoutSession(ctx context.Context, event *NormalizedPaymentEvent) error {
	if event == nil || !event.Paid || event.Provider != model.PaymentProviderStripe {
		return nil
	}
	if event.ProviderLivemode == nil || !strings.HasPrefix(event.ProviderOrderKey, "stripe:cs_") ||
		strings.TrimSpace(event.TradeNo) == "" || event.PaidAmountMinor <= 0 || strings.TrimSpace(event.Currency) == "" {
		return errStripeCheckoutConfirmation
	}
	credentialMode, err := stripeCredentialModeForUse(setting.StripeApiSecret)
	if err != nil || credentialMode != setting.StripeCredentialLivemode ||
		credentialMode != setting.StripeWebhookCredentialLivemode {
		return errStripeCheckoutConfirmation
	}
	expectedFingerprint := StripeCheckoutConfigurationFingerprint(
		setting.StripeApiSecret, setting.StripeCredentialAccountId, setting.StripeAccountId,
		setting.StripePriceId, setting.StripeCurrency, setting.StripeCredentialLivemode,
	)
	if expectedFingerprint == "" || setting.StripeConfigurationVerifiedAt <= 0 ||
		setting.StripeConfigurationVerifiedFingerprint != expectedFingerprint {
		return errStripeCheckoutConfirmation
	}

	sessionID := strings.TrimPrefix(event.ProviderOrderKey, "stripe:")
	session, err := p.getCheckoutSession(ctx, sessionID)
	if err != nil || session == nil {
		return errStripeCheckoutConfirmation
	}
	clientTradeNo := strings.TrimSpace(session.ClientReferenceID)
	metadataTradeNo := strings.TrimSpace(session.Metadata["trade_no"])
	if clientTradeNo != "" && metadataTradeNo != "" && clientTradeNo != metadataTradeNo {
		return errStripeCheckoutConfirmation
	}
	if clientTradeNo == "" {
		clientTradeNo = metadataTradeNo
	}
	providerPaymentKey := ""
	if session.PaymentIntent != nil && strings.TrimSpace(session.PaymentIntent.ID) != "" {
		providerPaymentKey = "stripe:" + strings.TrimSpace(session.PaymentIntent.ID)
	}
	customerID := ""
	if session.Customer != nil {
		customerID = strings.TrimSpace(session.Customer.ID)
	}
	expectedLivemode := credentialMode == "live"
	if session.ID != sessionID || clientTradeNo != strings.TrimSpace(event.TradeNo) ||
		session.Mode != stripe.CheckoutSessionModePayment ||
		session.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid ||
		session.AmountTotal != event.PaidAmountMinor ||
		!strings.EqualFold(string(session.Currency), event.Currency) ||
		session.Livemode != expectedLivemode || *event.ProviderLivemode != expectedLivemode ||
		providerPaymentKey != strings.TrimSpace(event.ProviderPaymentKey) ||
		customerID != strings.TrimSpace(event.CustomerID) {
		return errStripeCheckoutConfirmation
	}
	return nil
}

func constructStripeEvent(payload []byte, signature string) (stripe.Event, int64, error) {
	options := webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true}
	var currentErr error
	if strings.TrimSpace(setting.StripeWebhookSecret) != "" {
		event, err := webhook.ConstructEventWithOptions(payload, signature, setting.StripeWebhookSecret, options)
		if err == nil {
			if setting.StripeWebhookCredentialGeneration <= 0 {
				return stripe.Event{}, 0, errors.New("stripe webhook credential generation is invalid")
			}
			return event, setting.StripeWebhookCredentialGeneration, nil
		}
		currentErr = err
	} else {
		currentErr = errors.New("stripe webhook secret is not configured")
	}
	if !setting.StripePreviousWebhookSecretActive() {
		return stripe.Event{}, 0, currentErr
	}
	event, err := webhook.ConstructEventWithOptions(payload, signature, setting.StripeWebhookSecretPrevious, options)
	if err != nil {
		return stripe.Event{}, 0, err
	}
	previousGeneration := setting.StripeWebhookPreviousCredentialGeneration
	if previousGeneration <= 0 {
		// Upgrade compatibility for an overlap created before generations were
		// persisted. Emergency rotation clears the previous secret entirely.
		previousGeneration = setting.StripeWebhookCredentialGeneration - 1
	}
	if previousGeneration <= 0 {
		return stripe.Event{}, 0, errors.New("previous stripe webhook credential generation is invalid")
	}
	return event, previousGeneration, nil
}

func validStripeSecret(secret string) bool {
	return strings.HasPrefix(secret, "sk_live_") || strings.HasPrefix(secret, "sk_test_") || strings.HasPrefix(secret, "rk_live_") || strings.HasPrefix(secret, "rk_test_")
}

func normalizeStripeCreateError(err error) error {
	if err == nil {
		return nil
	}
	var stripeErr *stripe.Error
	if !errors.As(err, &stripeErr) {
		return fmt.Errorf("%w: stripe checkout request outcome is unknown", ErrPaymentStateUnknown)
	}
	if stripeErr.HTTPStatusCode == http.StatusTooManyRequests || stripeErr.HTTPStatusCode >= http.StatusInternalServerError ||
		stripeErr.Type == stripe.ErrorTypeAPI || stripeErr.Type == stripe.ErrorTypeRateLimit || stripeErr.Type == stripe.ErrorTypeTemporarySessionExpired {
		return fmt.Errorf("%w: stripe checkout request is temporarily unavailable", ErrPaymentStateUnknown)
	}
	return errors.New("stripe rejected the checkout configuration")
}

func stripeExpectedLiveMode(secret string) (bool, bool) {
	switch {
	case strings.HasPrefix(secret, "sk_live_"), strings.HasPrefix(secret, "rk_live_"):
		return true, true
	case strings.HasPrefix(secret, "sk_test_"), strings.HasPrefix(secret, "rk_test_"):
		return false, true
	default:
		return false, false
	}
}

func StripeCredentialMode(secret string) (string, error) {
	live, known := stripeExpectedLiveMode(strings.TrimSpace(secret))
	if !known {
		return "", errors.New("invalid Stripe API secret")
	}
	if live {
		return "live", nil
	}
	return "test", nil
}

func stripeCredentialModeForUse(secret string) (string, error) {
	mode, err := StripeCredentialMode(secret)
	if err != nil {
		return "", err
	}
	if !setting.StripeCredentialModeAllowed(mode) {
		return "", fmt.Errorf("Stripe test mode is disabled; set %s=true only in an isolated sandbox environment", setting.StripeTestModeEnabledEnv)
	}
	return mode, nil
}

func validateStripeCheckoutURL(raw string) error {
	if err := ValidateExternalPaymentURL(raw, false); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	host := strings.ToLower(u.Hostname())
	if host != "checkout.stripe.com" && !strings.HasSuffix(host, ".stripe.com") {
		return errors.New("unexpected stripe checkout host")
	}
	return nil
}
