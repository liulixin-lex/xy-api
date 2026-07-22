package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
)

const (
	PaymentQuoteTTLSeconds   = int64(10 * 60)
	MaxPaymentTopUpAmount    = int64(10_000)
	MaxPaymentRequestIDBytes = 128

	PaymentFlowFormPost        = "form_post"
	PaymentFlowHostedRedirect  = "hosted_redirect"
	PaymentFlowAppRedirect     = "app_redirect"
	PaymentFlowQR              = "qr"
	PaymentFlowWeChatAuthorize = "wechat_authorize"
	PaymentFlowJSAPI           = "jsapi"
	PaymentFlowPending         = "pending"
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

type PaymentQuoteRequest struct {
	OrderKind     string `json:"order_kind"`
	Provider      string `json:"provider"`
	PaymentMethod string `json:"payment_method"`
	Amount        int64  `json:"amount,omitempty"`
	PlanID        int    `json:"plan_id,omitempty"`
	ProductID     string `json:"product_id,omitempty"`
	OptionID      string `json:"option_id,omitempty"`
	// Legacy Stripe clients may supply trusted return URLs. They are deliberately
	// not accepted by the unified quote JSON API; compatibility controllers set
	// them in-process and they are then stored in the immutable quote snapshot.
	SuccessURL string `json:"-"`
	CancelURL  string `json:"-"`
}

type PaymentQuoteView struct {
	QuoteID             string `json:"quote_id"`
	OrderKind           string `json:"order_kind"`
	Provider            string `json:"provider"`
	PaymentMethod       string `json:"payment_method"`
	RequestedAmount     int64  `json:"requested_amount"`
	CreditQuota         int64  `json:"credit_quota"`
	ExpectedAmountMinor int64  `json:"expected_amount_minor"`
	PayableAmount       string `json:"payable_amount"`
	Currency            string `json:"currency"`
	ExpiresAt           int64  `json:"expires_at"`
}

type PaymentStartRequest struct {
	QuoteID   string `json:"quote_id"`
	RequestID string `json:"request_id"`
}

type PaymentStart struct {
	Flow               string                  `json:"flow"`
	TradeNo            string                  `json:"trade_no"`
	Action             string                  `json:"action,omitempty"`
	Fields             map[string]string       `json:"fields,omitempty"`
	URL                string                  `json:"url,omitempty"`
	QRContent          string                  `json:"qr_content,omitempty"`
	JSAPI              *PaymentJSAPIParameters `json:"jsapi,omitempty"`
	ExpiresAt          int64                   `json:"expires_at"`
	ProviderOrderKey   string                  `json:"-"`
	ProviderPaymentKey string                  `json:"-"`
	Provider           string                  `json:"-"`
	PaymentMethod      string                  `json:"-"`
}

type PaymentJSAPIParameters struct {
	AppID     string `json:"app_id"`
	Timestamp string `json:"timestamp"`
	NonceStr  string `json:"nonce_str"`
	Package   string `json:"package"`
	SignType  string `json:"sign_type"`
	PaySign   string `json:"pay_sign"`
}

type NormalizedPaymentEvent struct {
	Provider                     string
	EventKey                     string
	EventType                    string
	TradeNo                      string
	ProviderOrderKey             string
	ProviderPaymentKey           string
	ProviderResourceKey          string
	ProviderCredentialGeneration int64
	ProviderLivemode             *bool
	ProviderCreatedAt            int64
	ProviderState                string
	CustomerID                   string
	PaidAmountMinor              int64
	RefundedAmountMinor          int64
	DisputedAmountMinor          int64
	Currency                     string
	PaymentMethod                string
	Paid                         bool
	Failed                       bool
	Expired                      bool
	Refunded                     bool
	Disputed                     bool
	DisputeResolved              bool
	DisputeWon                   bool
	PermanentFailure             bool
	ManualReview                 bool
	NormalizedPayload            string
	// VerifiedPayload is an in-memory copy of the already signature-verified
	// provider event. It is available only to provider-specific compatibility
	// processors and is never persisted in payment logs or returned to clients.
	VerifiedPayload []byte
}

type PaymentProvider interface {
	Name() string
	ValidateMethod(method string) error
	Create(ctx context.Context, order *model.PaymentOrder) (*PaymentStart, error)
	VerifyWebhook(request *http.Request) (*NormalizedPaymentEvent, error)
	Query(ctx context.Context, order *model.PaymentOrder) (*NormalizedPaymentEvent, error)
}

type PaymentStartRecoverer interface {
	RecoverStart(ctx context.Context, order *model.PaymentOrder) (*PaymentStart, error)
}

type PaymentCredentialGenerationProvider interface {
	CredentialGeneration() int64
}

type paymentCredentialScope struct {
	known            bool
	generation       int64
	createdAt        int64
	providerOrderKey string
}

func resolvePaymentCredentialScope(provider, tradeNo string) (paymentCredentialScope, error) {
	order, err := model.GetPaymentOrderByTradeNo(tradeNo)
	if err == nil {
		if order.Provider != provider {
			return paymentCredentialScope{}, errors.New("payment order provider does not match callback provider")
		}
		providerOrderKey := ""
		if order.ProviderOrderKey != nil {
			providerOrderKey = strings.TrimSpace(*order.ProviderOrderKey)
		}
		return paymentCredentialScope{
			known: true, generation: order.ProviderCredentialGeneration, createdAt: order.CreatedAt,
			providerOrderKey: providerOrderKey,
		}, nil
	}
	if !errors.Is(err, model.ErrPaymentOrderNotFound) {
		return paymentCredentialScope{}, err
	}
	createdAt, found, err := model.LegacyPendingPaymentCreatedAt(provider, tradeNo)
	if err != nil {
		return paymentCredentialScope{}, err
	}
	return paymentCredentialScope{known: found, createdAt: createdAt}, nil
}

func (scope paymentCredentialScope) allowsCurrent(generation int64) bool {
	return !scope.known || scope.generation == 0 || scope.generation == generation
}

func (scope paymentCredentialScope) allowsPrevious(generation, validBefore int64) bool {
	if !scope.known {
		return false
	}
	if scope.generation > 0 {
		return scope.generation == generation
	}
	return scope.createdAt > 0 && scope.createdAt <= validBefore
}

// VerifiedPaymentWebhookProcessor handles provider-specific, non-settlement
// compatibility state after signature verification. Implementations must be
// idempotent and must not grant payment entitlements.
type VerifiedPaymentWebhookProcessor interface {
	ProcessVerifiedWebhook(ctx context.Context, event *NormalizedPaymentEvent) error
}

// VerifiedPaymentWebhookValidator performs provider authority checks that are
// free of local persistence side effects. The signature-verified normalized
// event is already in the durable inbox before this hook runs; settlement then
// happens before compatibility inventory is updated.
type VerifiedPaymentWebhookValidator interface {
	ValidateVerifiedWebhook(ctx context.Context, event *NormalizedPaymentEvent) error
}

var paymentProviderRegistry = struct {
	sync.RWMutex
	providers map[string]PaymentProvider
}{providers: make(map[string]PaymentProvider)}

func LockPaymentConfigurationForUpdate() func() {
	return setting.LockPaymentConfigurationForUpdate()
}

func VerifyPaymentWebhook(providerName string, request *http.Request) (*NormalizedPaymentEvent, error) {
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		return nil, fmt.Errorf("failed to synchronize payment configuration: %w", err)
	}
	unlock := setting.LockPaymentConfigurationForRead()
	defer unlock()
	provider, err := GetPaymentProvider(providerName)
	if err != nil {
		return nil, err
	}
	return provider.VerifyWebhook(request)
}

func ProcessVerifiedPaymentWebhook(ctx context.Context, providerName string, event *NormalizedPaymentEvent) error {
	provider, err := GetPaymentProvider(providerName)
	if err != nil {
		return err
	}
	processor, ok := provider.(VerifiedPaymentWebhookProcessor)
	if !ok {
		return nil
	}
	return processor.ProcessVerifiedWebhook(ctx, event)
}

func ValidateVerifiedPaymentWebhook(ctx context.Context, providerName string, event *NormalizedPaymentEvent) error {
	provider, err := GetPaymentProvider(providerName)
	if err != nil {
		return err
	}
	validator, ok := provider.(VerifiedPaymentWebhookValidator)
	if !ok {
		return nil
	}
	return validator.ValidateVerifiedWebhook(ctx, event)
}

func RegisterPaymentProvider(provider PaymentProvider) {
	if provider == nil || strings.TrimSpace(provider.Name()) == "" {
		panic("invalid payment provider")
	}
	paymentProviderRegistry.Lock()
	defer paymentProviderRegistry.Unlock()
	paymentProviderRegistry.providers[provider.Name()] = provider
}

func GetPaymentProvider(name string) (PaymentProvider, error) {
	paymentProviderRegistry.RLock()
	provider := paymentProviderRegistry.providers[name]
	paymentProviderRegistry.RUnlock()
	if provider == nil {
		return nil, fmt.Errorf("unsupported payment provider: %s", name)
	}
	return provider, nil
}

func ValidatePaymentRequestID(requestID string) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || len(requestID) > MaxPaymentRequestIDBytes {
		return errors.New("invalid payment request id")
	}
	for _, r := range requestID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return errors.New("invalid payment request id")
	}
	return nil
}

// NormalizePaymentMethod keeps Epay custom method identifiers byte-for-byte
// (apart from surrounding whitespace) because third-party Epay gateways may
// treat their configured type as case-sensitive. Providers with protocol-
// defined method identifiers continue to use a lowercase canonical form.
func NormalizePaymentMethod(provider, paymentMethod string) string {
	paymentMethod = strings.TrimSpace(paymentMethod)
	if strings.EqualFold(strings.TrimSpace(provider), model.PaymentProviderEpay) {
		return paymentMethod
	}
	return strings.ToLower(paymentMethod)
}

func ValidatePaymentProviderForCreate(providerName, paymentMethod string) error {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		return errors.New("payment compliance confirmation required")
	}
	if !model.PaymentSecretStorageReady() {
		return errors.New("payment credential encryption is not ready; configure PAYMENT_SECRET_KEY and re-save the gateway credentials")
	}
	if strings.TrimSpace(operation_setting.CustomCallbackAddress) == "" {
		return errors.New("payment callback address is not configured")
	}
	if err := ValidatePaymentCallbackOrigin(operation_setting.CustomCallbackAddress, true); err != nil {
		return err
	}
	provider, err := GetPaymentProvider(providerName)
	if err != nil {
		return err
	}
	if err := provider.ValidateMethod(paymentMethod); err != nil {
		return err
	}
	switch providerName {
	case model.PaymentProviderEpay:
		if strings.TrimSpace(operation_setting.PayAddress) == "" || strings.TrimSpace(operation_setting.EpayId) == "" || strings.TrimSpace(operation_setting.EpayKey) == "" {
			return errors.New("epay is not configured")
		}
	case model.PaymentProviderStripe:
		credentialMode, modeErr := StripeCredentialMode(setting.StripeApiSecret)
		verifiedFingerprint := StripeCheckoutConfigurationFingerprint(
			setting.StripeApiSecret, setting.StripeCredentialAccountId, setting.StripeAccountId,
			setting.StripePriceId, setting.StripeCurrency, setting.StripeCredentialLivemode,
			setting.StripeCheckoutAllowedHosts,
		)
		if strings.TrimSpace(setting.StripeApiSecret) == "" || strings.TrimSpace(setting.StripeWebhookSecret) == "" || strings.TrimSpace(setting.StripePriceId) == "" ||
			strings.TrimSpace(setting.StripeCredentialAccountId) == "" || modeErr != nil || credentialMode != setting.StripeCredentialLivemode ||
			!setting.StripeCredentialModeAllowed(credentialMode) ||
			setting.StripeWebhookCredentialLivemode != setting.StripeCredentialLivemode || verifiedFingerprint == "" ||
			setting.StripeConfigurationVerifiedFingerprint != verifiedFingerprint || setting.StripeConfigurationVerifiedAt <= 0 {
			return errors.New("stripe is not configured")
		}
	case model.PaymentProviderXorPay:
		if strings.TrimSpace(setting.XorPayAid) == "" || strings.TrimSpace(setting.XorPayAppSecret) == "" {
			return errors.New("xorpay is not configured")
		}
	case model.PaymentProviderCreem:
		if strings.TrimSpace(setting.CreemApiKey) == "" || strings.TrimSpace(setting.CreemWebhookSecret) == "" {
			return errors.New("creem is not configured")
		}
	case model.PaymentProviderWaffo:
		if !setting.WaffoEnabled || strings.TrimSpace(setting.WaffoMerchantId) == "" {
			return errors.New("waffo is not configured")
		}
		if setting.WaffoSandbox {
			if strings.TrimSpace(setting.WaffoSandboxApiKey) == "" || strings.TrimSpace(setting.WaffoSandboxPrivateKey) == "" ||
				strings.TrimSpace(setting.WaffoSandboxPublicCert) == "" {
				return errors.New("waffo sandbox is not configured")
			}
		} else if strings.TrimSpace(setting.WaffoApiKey) == "" || strings.TrimSpace(setting.WaffoPrivateKey) == "" ||
			strings.TrimSpace(setting.WaffoPublicCert) == "" {
			return errors.New("waffo is not configured")
		}
	case model.PaymentProviderWaffoPancake:
		if strings.TrimSpace(setting.WaffoPancakeMerchantID) == "" || strings.TrimSpace(setting.WaffoPancakePrivateKey) == "" ||
			strings.TrimSpace(setting.WaffoPancakeStoreID) == "" {
			return errors.New("waffo pancake is not configured")
		}
	default:
		return fmt.Errorf("unsupported payment provider: %s", providerName)
	}
	return nil
}

func CreatePaymentQuote(ctx context.Context, userID int, request PaymentQuoteRequest) (*PaymentQuoteView, error) {
	_ = ctx
	quote, view, err := buildPaymentQuote(userID, request)
	if err != nil {
		return nil, err
	}
	if err := model.CreatePaymentQuote(quote); err != nil {
		return nil, err
	}
	return view, nil
}

func PreviewPaymentQuote(ctx context.Context, userID int, request PaymentQuoteRequest) (*PaymentQuoteView, error) {
	_ = ctx
	_, view, err := buildPaymentQuote(userID, request)
	return view, err
}

func buildPaymentQuote(userID int, request PaymentQuoteRequest) (*model.PaymentQuote, *PaymentQuoteView, error) {
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		return nil, nil, fmt.Errorf("failed to synchronize payment configuration: %w", err)
	}
	unlock := setting.LockPaymentConfigurationForRead()
	defer unlock()
	request.OrderKind = strings.ToLower(strings.TrimSpace(request.OrderKind))
	request.Provider = strings.ToLower(strings.TrimSpace(request.Provider))
	request.PaymentMethod = NormalizePaymentMethod(request.Provider, request.PaymentMethod)
	if err := ValidatePaymentProviderForCreate(request.Provider, request.PaymentMethod); err != nil {
		return nil, nil, err
	}

	quoteID, err := model.GeneratePaymentQuoteID()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().Unix()
	quote := &model.PaymentQuote{
		QuoteID:       quoteID,
		UserID:        userID,
		OrderKind:     request.OrderKind,
		Provider:      request.Provider,
		PaymentMethod: request.PaymentMethod,
		ExpiresAt:     now + PaymentQuoteTTLSeconds,
		CreatedAt:     now,
	}
	switch request.Provider {
	case model.PaymentProviderStripe:
		providerLivemode := setting.StripeCredentialLivemode == "live"
		quote.ProviderLivemode = &providerLivemode
	case model.PaymentProviderCreem:
		providerLivemode := !setting.CreemTestMode
		quote.ProviderLivemode = &providerLivemode
	case model.PaymentProviderWaffoPancake:
		providerLivemode := !setting.WaffoPancakeTestMode
		quote.ProviderLivemode = &providerLivemode
	}

	var payable decimal.Decimal
	switch request.OrderKind {
	case model.PaymentOrderKindTopUp:
		if request.Provider == model.PaymentProviderCreem || request.Provider == model.PaymentProviderWaffo ||
			request.Provider == model.PaymentProviderWaffoPancake {
			payable, err = fillRetainedTopUpQuote(quote, request)
		} else {
			payable, err = fillTopUpQuote(quote, request.Amount)
		}
	case model.PaymentOrderKindSubscription:
		payable, err = fillSubscriptionQuote(quote, request.PlanID)
	default:
		err = errors.New("invalid payment order kind")
	}
	if err != nil {
		return nil, nil, err
	}
	if err := applyStripeReturnURLSnapshot(quote, request.SuccessURL, request.CancelURL); err != nil {
		return nil, nil, err
	}
	currencyExponent := common.PaymentProviderCurrencyExponent(quote.Provider, quote.Currency)
	expectedMinor, err := paymentAmountMinorForProvider(payable, quote.Provider, quote.Currency)
	if err != nil {
		return nil, nil, err
	}
	quote.ExpectedAmountMinor = expectedMinor
	if err := model.CheckPaymentLimitForQuote(quote.Provider, quote.PaymentMethod, quote.Currency, expectedMinor, now); err != nil {
		return nil, nil, err
	}
	quote.ExpiresAt, err = model.BoundPaymentQuoteExpiryForLimit(
		quote.Provider, quote.PaymentMethod, quote.Currency, now, quote.ExpiresAt,
	)
	if err != nil {
		return nil, nil, err
	}
	return quote, &PaymentQuoteView{
		QuoteID:             quote.QuoteID,
		OrderKind:           quote.OrderKind,
		Provider:            quote.Provider,
		PaymentMethod:       quote.PaymentMethod,
		RequestedAmount:     quote.RequestedAmount,
		CreditQuota:         quote.CreditQuota,
		ExpectedAmountMinor: quote.ExpectedAmountMinor,
		PayableAmount:       payable.StringFixed(currencyExponent),
		Currency:            quote.Currency,
		ExpiresAt:           quote.ExpiresAt,
	}, nil
}

type stripeReturnURLSnapshot struct {
	SuccessURL string `json:"stripe_success_url,omitempty"`
	CancelURL  string `json:"stripe_cancel_url,omitempty"`
}

func applyStripeReturnURLSnapshot(quote *model.PaymentQuote, successURL, cancelURL string) error {
	if quote == nil || quote.Provider != model.PaymentProviderStripe {
		return nil
	}
	successURL = strings.TrimSpace(successURL)
	cancelURL = strings.TrimSpace(cancelURL)
	if successURL == "" && cancelURL == "" {
		return nil
	}
	for _, candidate := range []string{successURL, cancelURL} {
		if candidate == "" {
			continue
		}
		if len(candidate) > 2048 || common.ValidateRedirectURL(candidate) != nil || ValidateExternalPaymentURL(candidate, true) != nil {
			return errors.New("Stripe return URL is not an allowed secure destination")
		}
	}
	var snapshot map[string]interface{}
	if err := common.UnmarshalJsonStr(quote.PricingSnapshot, &snapshot); err != nil {
		return errors.New("invalid payment pricing snapshot")
	}
	if successURL != "" {
		snapshot["stripe_success_url"] = successURL
	}
	if cancelURL != "" {
		snapshot["stripe_cancel_url"] = cancelURL
	}
	encoded, err := common.Marshal(snapshot)
	if err != nil {
		return err
	}
	quote.PricingSnapshot = string(encoded)
	return nil
}

func fillTopUpQuote(quote *model.PaymentQuote, amount int64) (decimal.Decimal, error) {
	if amount <= 0 || amount > MaxPaymentTopUpAmount {
		return decimal.Zero, fmt.Errorf("top-up amount must be between 1 and %d", MaxPaymentTopUpAmount)
	}
	minTopUp, unitPrice, currency, err := paymentProviderPricing(quote.Provider)
	if err != nil {
		return decimal.Zero, err
	}
	minTopUp, err = EffectivePaymentMethodMinimum(quote.Provider, quote.PaymentMethod)
	if err != nil {
		return decimal.Zero, err
	}
	if amount < minTopUp {
		return decimal.Zero, fmt.Errorf("top-up amount cannot be less than %d", minTopUp)
	}
	baseUnits := decimal.NewFromInt(amount)
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		quotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		if quotaPerUnit.LessThanOrEqual(decimal.Zero) {
			return decimal.Zero, errors.New("invalid quota per unit")
		}
		baseUnits = baseUnits.Div(quotaPerUnit)
	}
	creditQuota, clamp := common.QuotaFromDecimalChecked(baseUnits.Mul(decimal.NewFromFloat(common.QuotaPerUnit)))
	if clamp != nil || creditQuota <= 0 {
		return decimal.Zero, errors.New("top-up quota is outside the supported range")
	}
	group, err := model.GetUserGroup(quote.UserID, true)
	if err != nil {
		return decimal.Zero, err
	}
	groupRatio := common.GetTopupGroupRatio(group)
	if groupRatio == 0 {
		groupRatio = 1
	}
	if math.IsNaN(groupRatio) || math.IsInf(groupRatio, 0) || groupRatio <= 0 {
		return decimal.Zero, errors.New("invalid top-up group ratio")
	}
	discount := 1.0
	if configured, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(amount)]; ok {
		if math.IsNaN(configured) || math.IsInf(configured, 0) || configured <= 0 || configured > 1 {
			return decimal.Zero, errors.New("invalid top-up discount")
		}
		discount = configured
	}
	payable := baseUnits.
		Mul(decimal.NewFromFloat(unitPrice)).
		Mul(decimal.NewFromFloat(groupRatio)).
		Mul(decimal.NewFromFloat(discount))
	minimum, err := minimumPaymentAmount(quote.Provider, currency)
	if err != nil {
		return decimal.Zero, err
	}
	if payable.LessThan(minimum) {
		return decimal.Zero, errors.New("payment amount is too low")
	}
	pricingSnapshot := map[string]interface{}{
		"display_amount": amount,
		"base_units":     baseUnits.String(),
		"unit_price":     decimal.NewFromFloat(unitPrice).String(),
		"group":          group,
		"group_ratio":    decimal.NewFromFloat(groupRatio).String(),
		"discount":       decimal.NewFromFloat(discount).String(),
		"quota_per_unit": decimal.NewFromFloat(common.QuotaPerUnit).String(),
		"currency":       currency,
	}
	snapshot, err := common.Marshal(pricingSnapshot)
	if err != nil {
		return decimal.Zero, err
	}
	quote.RequestedAmount = amount
	quote.CreditQuota = int64(creditQuota)
	quote.Currency = currency
	quote.PricingSnapshot = string(snapshot)
	return payable, nil
}

func fillSubscriptionQuote(quote *model.PaymentQuote, planID int) (decimal.Decimal, error) {
	if planID <= 0 {
		return decimal.Zero, errors.New("invalid subscription plan")
	}
	plan, err := model.GetSubscriptionPlanById(planID)
	if err != nil {
		return decimal.Zero, err
	}
	if err := model.ValidateSubscriptionPlanForExternalPayment(plan); err != nil {
		return decimal.Zero, err
	}
	if !strings.EqualFold(strings.TrimSpace(plan.Currency), "USD") {
		return decimal.Zero, errors.New("external payment subscription plans must use USD as the base currency")
	}
	unitPrice, currency, err := paymentSubscriptionPricing(quote.Provider, plan)
	if err != nil {
		return decimal.Zero, err
	}
	payable := decimal.NewFromFloat(plan.PriceAmount).Mul(decimal.NewFromFloat(unitPrice))
	minimum, err := minimumPaymentAmount(quote.Provider, currency)
	if err != nil {
		return decimal.Zero, err
	}
	if payable.LessThan(minimum) {
		return decimal.Zero, errors.New("payment amount is too low")
	}
	planSnapshot, err := model.NewSubscriptionPlanSnapshot(plan)
	if err != nil {
		return decimal.Zero, err
	}
	productSnapshot, err := common.Marshal(planSnapshot)
	if err != nil {
		return decimal.Zero, err
	}
	pricingSnapshot, err := common.Marshal(map[string]interface{}{
		"base_price":    decimal.NewFromFloat(plan.PriceAmount).String(),
		"base_currency": strings.ToUpper(strings.TrimSpace(plan.Currency)),
		"unit_price":    decimal.NewFromFloat(unitPrice).String(),
		"currency":      currency,
	})
	if err != nil {
		return decimal.Zero, err
	}
	quote.RequestedAmount = int64(planID)
	quote.CreditQuota = plan.TotalAmount
	quote.Currency = currency
	quote.PricingSnapshot = string(pricingSnapshot)
	quote.ProductSnapshot = string(productSnapshot)
	if err := validateRetainedSubscriptionSnapshot(quote.Provider, planSnapshot); err != nil {
		return decimal.Zero, err
	}
	return payable, nil
}

func minimumPaymentAmount(provider, currency string) (decimal.Decimal, error) {
	exponent, ok := common.PaymentProviderCurrencyExponentOK(provider, currency)
	if !ok {
		return decimal.Zero, errors.New("unsupported payment currency")
	}
	return decimal.New(1, -exponent), nil
}

func paymentSubscriptionPricing(provider string, plan *model.SubscriptionPlan) (float64, string, error) {
	var multiplier float64
	var currency string
	switch provider {
	case model.PaymentProviderStripe:
		// Plans keep one USD base price. The provider unit price converts that
		// base into the configured Stripe settlement currency, exactly as it does
		// for a top-up quote; the route currency is not required to equal the
		// plan's base currency.
		multiplier = setting.StripeUnitPrice
		currency = setting.StripeCurrency
	case model.PaymentProviderEpay:
		multiplier = operation_setting.Price
		currency = operation_setting.EpayCurrency
	case model.PaymentProviderXorPay:
		multiplier = setting.XorPayUnitPrice
		currency = setting.XorPayCurrency
	case model.PaymentProviderCreem:
		multiplier = 1
		if plan != nil {
			currency = plan.Currency
		}
	case model.PaymentProviderWaffoPancake:
		multiplier = setting.WaffoPancakeUnitPrice
		currency = "USD"
	default:
		return 0, "", fmt.Errorf("unsupported payment provider: %s", provider)
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 || !currencyPattern.MatchString(currency) {
		return 0, "", fmt.Errorf("invalid %s subscription pricing configuration", provider)
	}
	if _, ok := common.PaymentCurrencyExponentOK(currency); !ok {
		return 0, "", fmt.Errorf("invalid %s subscription currency", provider)
	}
	if provider == model.PaymentProviderEpay && currency != "CNY" {
		return 0, "", errors.New("epay subscription currency must be CNY")
	}
	if provider == model.PaymentProviderXorPay && currency != "CNY" {
		return 0, "", errors.New("xorpay subscription currency must be CNY")
	}
	if provider == model.PaymentProviderCreem && currency != "USD" && currency != "EUR" {
		return 0, "", errors.New("creem subscription currency must be USD or EUR")
	}
	if provider == model.PaymentProviderWaffoPancake && currency != "USD" {
		return 0, "", errors.New("waffo pancake subscription currency must be USD")
	}
	return multiplier, currency, nil
}

func paymentProviderPricing(provider string) (int64, float64, string, error) {
	var minTopUp int64
	var unitPrice float64
	var currency string
	switch provider {
	case model.PaymentProviderEpay:
		minTopUp = int64(operation_setting.MinTopUp)
		unitPrice = operation_setting.Price
		currency = operation_setting.EpayCurrency
	case model.PaymentProviderStripe:
		minTopUp = int64(setting.StripeMinTopUp)
		unitPrice = setting.StripeUnitPrice
		currency = setting.StripeCurrency
	case model.PaymentProviderXorPay:
		minTopUp = int64(setting.XorPayMinTopUp)
		unitPrice = setting.XorPayUnitPrice
		currency = setting.XorPayCurrency
	case model.PaymentProviderWaffo:
		minTopUp = int64(setting.WaffoMinTopUp)
		unitPrice = setting.WaffoUnitPrice
		currency = setting.WaffoCurrency
		if strings.TrimSpace(currency) == "" {
			currency = "USD"
		}
	case model.PaymentProviderWaffoPancake:
		minTopUp = int64(setting.WaffoPancakeMinTopUp)
		unitPrice = setting.WaffoPancakeUnitPrice
		currency = "USD"
	default:
		return 0, 0, "", fmt.Errorf("unsupported payment provider: %s", provider)
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if minTopUp <= 0 || math.IsNaN(unitPrice) || math.IsInf(unitPrice, 0) || unitPrice <= 0 || !currencyPattern.MatchString(currency) {
		return 0, 0, "", fmt.Errorf("invalid %s payment pricing configuration", provider)
	}
	if _, ok := common.PaymentCurrencyExponentOK(currency); !ok {
		return 0, 0, "", fmt.Errorf("invalid %s payment currency", provider)
	}
	if (provider == model.PaymentProviderEpay || provider == model.PaymentProviderXorPay) && currency != "CNY" {
		return 0, 0, "", fmt.Errorf("%s payment currency must be CNY", provider)
	}
	return minTopUp, unitPrice, currency, nil
}

// EffectivePaymentMethodMinimum applies a method-specific minimum on top of
// the provider minimum. The higher value is authoritative on the server, so a
// client cannot bypass a configured custom-method floor.
func EffectivePaymentMethodMinimum(provider, paymentMethod string) (int64, error) {
	providerMinimum, _, _, err := paymentProviderPricing(provider)
	if err != nil {
		return 0, err
	}
	for _, configured := range operation_setting.PayMethods {
		configuredProvider := strings.TrimSpace(configured["provider"])
		if configuredProvider == "" {
			configuredProvider = model.PaymentProviderEpay
		}
		if configuredProvider != provider || strings.TrimSpace(configured["type"]) != paymentMethod {
			continue
		}
		methodMinimum, parseErr := strconv.ParseInt(strings.TrimSpace(configured["min_topup"]), 10, 64)
		if parseErr == nil && methodMinimum > providerMinimum {
			return methodMinimum, nil
		}
		return providerMinimum, nil
	}
	return providerMinimum, nil
}

func paymentAmountMinor(amount decimal.Decimal, exponent int32) (int64, error) {
	if exponent < 0 || exponent > 3 || amount.IsNegative() || amount.IsZero() {
		return 0, errors.New("invalid payment amount")
	}
	factor := decimal.New(1, exponent)
	minor := amount.Round(exponent).Mul(factor)
	if !minor.Equal(minor.Truncate(0)) {
		return 0, errors.New("payment amount has unsupported precision")
	}
	value := minor.IntPart()
	if value <= 0 || value > math.MaxInt32 {
		return 0, errors.New("payment amount is outside the supported range")
	}
	return value, nil
}

func paymentAmountMinorForProvider(amount decimal.Decimal, provider, currency string) (int64, error) {
	exponent, ok := common.PaymentProviderCurrencyExponentOK(provider, currency)
	if !ok {
		return 0, errors.New("unsupported payment currency")
	}
	value, err := paymentAmountMinor(amount, exponent)
	if err != nil {
		return 0, err
	}
	if provider == model.PaymentProviderStripe {
		switch strings.ToUpper(strings.TrimSpace(currency)) {
		case "ISK", "UGX":
			if value%100 != 0 {
				return 0, errors.New("Stripe requires whole-unit amounts for this currency")
			}
		}
	}
	return value, nil
}

func StartPayment(ctx context.Context, userID int, request PaymentStartRequest) (*PaymentStart, error) {
	_ = ctx
	if err := ValidatePaymentRequestID(request.RequestID); err != nil {
		return nil, err
	}
	if err := model.SyncPaymentConfigurationIfStale(); err != nil {
		return nil, fmt.Errorf("failed to synchronize payment configuration: %w", err)
	}
	unlock := setting.LockPaymentConfigurationForRead()
	defer unlock()
	configurationVersion, err := model.CurrentPaymentConfigurationVersion()
	if err != nil {
		return nil, err
	}
	order, err := model.CreatePaymentOrderFromQuoteWithConfigurationVersion(
		userID, strings.TrimSpace(request.QuoteID), strings.TrimSpace(request.RequestID), configurationVersion,
	)
	if err != nil {
		return nil, err
	}
	if order.ExpiresAt > 0 && order.ExpiresAt <= time.Now().Unix() && order.ProviderOrderKey == nil {
		if _, expireErr := model.ExpirePaymentOrderIfDue(userID, order.TradeNo); expireErr != nil {
			return nil, expireErr
		}
		return nil, errors.New("payment order has expired")
	}
	if order.Status != model.PaymentOrderStatusPending && order.Status != model.PaymentOrderStatusProcessing {
		return nil, fmt.Errorf("payment order is no longer startable: %s", order.Status)
	}
	provider, err := GetPaymentProvider(order.Provider)
	if err != nil {
		return nil, err
	}
	if credentialProvider, ok := provider.(PaymentCredentialGenerationProvider); ok && order.StartPayload == "" {
		generation := order.ProviderCredentialGeneration
		if generation > 0 {
			available, availabilityErr := model.PaymentCredentialGenerationAvailable(order.Provider, generation, order.CreatedAt)
			if availabilityErr != nil {
				return nil, availabilityErr
			}
			if !available {
				if _, taskErr := model.EnsurePaymentTask(order.ID, model.PaymentTaskOperationCreate, common.GetTimestamp()); taskErr != nil {
					return nil, taskErr
				}
				notifyPaymentTaskRunner()
				return pendingPaymentStart(order), nil
			}
		} else {
			generation = credentialProvider.CredentialGeneration()
			if generation <= 0 {
				return nil, errors.New("payment provider credential generation is invalid")
			}
			if err := model.BindPaymentOrderCredentialGeneration(order.TradeNo, generation, configurationVersion); err != nil {
				return nil, err
			}
			order.ProviderCredentialGeneration = generation
		}
	}
	if _, err := model.EnsurePaymentTask(order.ID, model.PaymentTaskOperationCreate, common.GetTimestamp()); err != nil {
		return nil, err
	}
	notifyPaymentTaskRunner()
	return pendingPaymentStart(order), nil
}

func pendingPaymentStart(order *model.PaymentOrder) *PaymentStart {
	if order == nil {
		return nil
	}
	return &PaymentStart{
		Flow:      PaymentFlowPending,
		TradeNo:   order.TradeNo,
		ExpiresAt: order.ExpiresAt,
	}
}

func RefreshPaymentOrder(ctx context.Context, userID int, tradeNo string) (*model.PaymentOrder, error) {
	_ = ctx
	// User polling is deliberately local-only. Provider queries are performed by
	// durable reconciliation tasks so browser refreshes and duplicate tabs can
	// never amplify upstream traffic.
	return model.ExpirePaymentOrderIfDue(userID, tradeNo)
}

var ErrPaymentStateUnknown = errors.New("payment provider state is unknown")

func ProcessNormalizedPaymentEvent(event *NormalizedPaymentEvent) (*model.PaymentSettlementResult, error) {
	if event == nil {
		return nil, errors.New("normalized payment event is required")
	}
	return model.ProcessPaymentEvent(normalizedPaymentEventInput(event))
}

func processNormalizedPaymentEventForTask(event *NormalizedPaymentEvent, task *model.PaymentTask,
	runnerID string) (*model.PaymentSettlementResult, error) {
	if event == nil {
		return nil, errors.New("normalized payment event is required")
	}
	return model.ProcessPaymentEventForTask(normalizedPaymentEventInput(event), task, runnerID)
}

func RecordVerifiedPaymentWebhookReceived(event *NormalizedPaymentEvent) error {
	if event == nil {
		return errors.New("normalized payment event is required")
	}
	return model.RecordPaymentEventReceived(normalizedPaymentEventInput(event))
}

func RecordVerifiedRetainedPaymentWebhookReceived(event *NormalizedPaymentEvent) error {
	if event == nil {
		return errors.New("normalized payment event is required")
	}
	return model.RecordRetainedPaymentEventReceived(normalizedPaymentEventInput(event))
}

func MarkVerifiedRetainedPaymentWebhookProcessed(event *NormalizedPaymentEvent) error {
	if event == nil {
		return errors.New("normalized payment event is required")
	}
	return model.MarkRetainedPaymentEventProcessed(normalizedPaymentEventInput(event))
}

func MarkVerifiedPaymentWebhookValidationFailed(event *NormalizedPaymentEvent, reasonCode string) error {
	if event == nil {
		return errors.New("normalized payment event is required")
	}
	return model.MarkPaymentEventValidationFailed(event.Provider, event.EventKey, reasonCode)
}

func AdoptLegacyPaymentOrder(event *NormalizedPaymentEvent) (*model.PaymentOrder, error) {
	if event == nil {
		return nil, errors.New("normalized payment event is required")
	}
	return model.AdoptLegacyPaymentOrder(normalizedPaymentEventInput(event))
}

func RecordUnmatchedPaymentEvent(event *NormalizedPaymentEvent, reason string) error {
	if event == nil {
		return errors.New("normalized payment event is required")
	}
	return model.RecordPaymentEventManualReview(normalizedPaymentEventInput(event), reason)
}

func normalizedPaymentEventInput(event *NormalizedPaymentEvent) model.PaymentEventInput {
	return model.PaymentEventInput{
		Provider:                     event.Provider,
		EventKey:                     event.EventKey,
		EventType:                    event.EventType,
		TradeNo:                      event.TradeNo,
		ProviderOrderKey:             event.ProviderOrderKey,
		ProviderPaymentKey:           event.ProviderPaymentKey,
		ProviderResourceKey:          event.ProviderResourceKey,
		ProviderCredentialGeneration: event.ProviderCredentialGeneration,
		ProviderLivemode:             event.ProviderLivemode,
		ProviderCreatedAt:            event.ProviderCreatedAt,
		ProviderState:                event.ProviderState,
		CustomerID:                   event.CustomerID,
		PaidAmountMinor:              event.PaidAmountMinor,
		RefundedAmountMinor:          event.RefundedAmountMinor,
		DisputedAmountMinor:          event.DisputedAmountMinor,
		Currency:                     event.Currency,
		PaymentMethod:                event.PaymentMethod,
		Paid:                         event.Paid,
		Failed:                       event.Failed,
		Expired:                      event.Expired,
		Refunded:                     event.Refunded,
		Disputed:                     event.Disputed,
		DisputeResolved:              event.DisputeResolved,
		DisputeWon:                   event.DisputeWon,
		PermanentFailure:             event.PermanentFailure,
		ManualReview:                 event.ManualReview,
		NormalizedPayload:            event.NormalizedPayload,
	}
}
