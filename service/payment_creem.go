package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
)

const creemMaximumResponseBytes = 256 << 10

type creemPaymentProvider struct {
	client *http.Client
}

type creemConfigurationSnapshot struct {
	apiKey   string
	testMode bool
}

func currentCreemConfigurationSnapshot(ctx context.Context) creemConfigurationSnapshot {
	var unlockPaymentConfiguration func()
	if !paymentConfigurationReadLockHeld(ctx) {
		unlockPaymentConfiguration = setting.LockPaymentConfigurationForRead()
	}
	snapshot := creemConfigurationSnapshot{
		apiKey:   setting.CreemApiKey,
		testMode: setting.CreemTestMode,
	}
	if unlockPaymentConfiguration != nil {
		unlockPaymentConfiguration()
	}
	return snapshot
}

type creemCheckoutRequest struct {
	ProductID string `json:"product_id"`
	RequestID string `json:"request_id"`
	Customer  struct {
		Email string `json:"email"`
	} `json:"customer"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type creemCheckoutResponse struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	CheckoutURL string `json:"checkout_url"`
	RequestID   string `json:"request_id"`
	Mode        string `json:"mode"`
	Order       struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		Amount     int64  `json:"amount"`
		AmountPaid int64  `json:"amount_paid"`
		Currency   string `json:"currency"`
		Mode       string `json:"mode"`
	} `json:"order"`
}

func ParseCreemAPIEnvironment(raw string) (string, bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "prod":
		return normalized, true, nil
	case "test", "sandbox":
		return normalized, false, nil
	default:
		return "", false, fmt.Errorf("unsupported Creem API environment %q", strings.TrimSpace(raw))
	}
}

func ParseCreemWebhookEnvironment(raw string) (string, bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "local":
		return normalized, true, nil
	case "test", "sandbox":
		return normalized, false, nil
	default:
		return "", false, fmt.Errorf("unsupported Creem webhook environment %q", strings.TrimSpace(raw))
	}
}

func init() {
	RegisterPaymentProvider(&creemPaymentProvider{client: &http.Client{Timeout: paymentProviderCallTimeout}})
}

func (*creemPaymentProvider) Name() string { return model.PaymentProviderCreem }

func (*creemPaymentProvider) ValidateMethod(method string) error {
	if NormalizePaymentMethod(model.PaymentProviderCreem, method) != model.PaymentMethodCreem {
		return errors.New("unsupported Creem payment method")
	}
	return nil
}

func (p *creemPaymentProvider) Create(ctx context.Context, order *model.PaymentOrder) (*PaymentStart, error) {
	if order == nil {
		return nil, errors.New("payment order is required")
	}
	productID, err := creemProductIDForOrder(order)
	if err != nil {
		return nil, err
	}
	user, err := model.GetUserById(order.UserID, false)
	if err != nil || user == nil {
		return nil, model.ErrPaymentUserUnavailable
	}
	request := creemCheckoutRequest{
		ProductID: productID,
		RequestID: order.TradeNo,
		Metadata: map[string]string{
			"reference_id": order.TradeNo,
			"order_kind":   order.OrderKind,
		},
	}
	request.Customer.Email = strings.TrimSpace(user.Email)
	body, err := common.Marshal(request)
	if err != nil {
		return nil, err
	}
	configuration := currentCreemConfigurationSnapshot(ctx)
	if order.ProviderLivemode == nil || *order.ProviderLivemode != !configuration.testMode {
		return nil, model.ErrPaymentManualReview
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, creemAPIBaseURL(configuration.testMode)+"/v1/checkouts", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("x-api-key", configuration.apiKey)
	response, err := p.httpClient().Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("%w: Creem checkout creation did not return a durable identity", ErrPaymentStateUnknown)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, creemMaximumResponseBytes+1))
	if err != nil || len(responseBody) > creemMaximumResponseBytes {
		return nil, fmt.Errorf("%w: Creem checkout response was incomplete", ErrPaymentStateUnknown)
	}
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%w: Creem checkout returned HTTP %d", ErrPaymentStateUnknown, response.StatusCode)
	}
	var checkout creemCheckoutResponse
	if err := common.Unmarshal(responseBody, &checkout); err != nil {
		return nil, fmt.Errorf("%w: Creem checkout response was invalid", ErrPaymentStateUnknown)
	}
	checkout.ID = strings.TrimSpace(checkout.ID)
	checkout.CheckoutURL = strings.TrimSpace(checkout.CheckoutURL)
	if checkout.ID == "" || len(checkout.ID) > 255 || checkout.CheckoutURL == "" {
		return nil, fmt.Errorf("%w: Creem checkout response was incomplete", ErrPaymentStateUnknown)
	}
	if checkout.RequestID != "" && strings.TrimSpace(checkout.RequestID) != order.TradeNo {
		return nil, model.ErrPaymentManualReview
	}
	if err := ValidateCreemCheckoutURL(checkout.CheckoutURL); err != nil {
		return nil, model.ErrPaymentManualReview
	}
	return &PaymentStart{
		Flow:             PaymentFlowHostedRedirect,
		URL:              checkout.CheckoutURL,
		ExpiresAt:        order.ExpiresAt,
		ProviderOrderKey: retainedProviderAuthorityKey(model.PaymentProviderCreem, checkout.ID),
	}, nil
}

func (p *creemPaymentProvider) RecoverStart(ctx context.Context, order *model.PaymentOrder) (*PaymentStart, error) {
	checkout, err := p.retrieve(ctx, order)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(checkout.CheckoutURL) == "" {
		return nil, ErrPaymentStateUnknown
	}
	if err := ValidateCreemCheckoutURL(checkout.CheckoutURL); err != nil {
		return nil, model.ErrPaymentManualReview
	}
	return &PaymentStart{
		Flow:             PaymentFlowHostedRedirect,
		URL:              strings.TrimSpace(checkout.CheckoutURL),
		ExpiresAt:        order.ExpiresAt,
		ProviderOrderKey: retainedProviderAuthorityKey(model.PaymentProviderCreem, checkout.ID),
	}, nil
}

func (p *creemPaymentProvider) Query(ctx context.Context, order *model.PaymentOrder) (*NormalizedPaymentEvent, error) {
	checkout, err := p.retrieve(ctx, order)
	if err != nil {
		return nil, err
	}
	state := strings.ToLower(strings.TrimSpace(checkout.Status))
	orderState := strings.ToLower(strings.TrimSpace(checkout.Order.Status))
	paid := orderState == "paid"
	expired := state == "expired"
	manualReview := state == "completed" && !paid
	environment, providerLivemode, environmentErr := ParseCreemAPIEnvironment(checkout.Mode)
	orderEnvironment, orderLivemode, orderEnvironmentErr := ParseCreemAPIEnvironment(checkout.Order.Mode)
	if environmentErr != nil || orderEnvironmentErr != nil || providerLivemode != orderLivemode {
		paid = false
		expired = false
		manualReview = true
	}
	amount := checkout.Order.AmountPaid
	if amount <= 0 {
		amount = checkout.Order.Amount
	}
	currency := strings.ToUpper(strings.TrimSpace(checkout.Order.Currency))
	providerOrderKey := retainedProviderAuthorityKey(model.PaymentProviderCreem, checkout.ID)
	payload, err := common.Marshal(map[string]interface{}{
		"checkout_id":                  checkout.ID,
		"status":                       state,
		"order_status":                 orderState,
		"amount":                       amount,
		"currency":                     currency,
		"environment":                  strings.TrimSpace(checkout.Mode),
		"order_environment":            strings.TrimSpace(checkout.Order.Mode),
		"normalized_environment":       environment,
		"normalized_order_environment": orderEnvironment,
	})
	if err != nil {
		return nil, err
	}
	event := &NormalizedPaymentEvent{
		Provider:          model.PaymentProviderCreem,
		EventKey:          model.PaymentEventKey(model.PaymentProviderCreem, "query:"+state+":"+orderState, providerOrderKey, order.TradeNo, string(payload)),
		EventType:         "query:" + state,
		TradeNo:           order.TradeNo,
		ProviderOrderKey:  providerOrderKey,
		ProviderState:     orderState,
		PaidAmountMinor:   amount,
		Currency:          currency,
		PaymentMethod:     model.PaymentMethodCreem,
		Paid:              paid,
		Expired:           expired,
		ManualReview:      manualReview,
		NormalizedPayload: string(payload),
	}
	if environmentErr == nil && orderEnvironmentErr == nil && providerLivemode == orderLivemode {
		event.ProviderLivemode = &providerLivemode
	}
	return event, nil
}

func (*creemPaymentProvider) VerifyWebhook(_ *http.Request) (*NormalizedPaymentEvent, error) {
	return nil, errors.New("Creem webhooks use the provider-specific verified endpoint")
}

func (p *creemPaymentProvider) retrieve(ctx context.Context, order *model.PaymentOrder) (*creemCheckoutResponse, error) {
	if order == nil || order.ProviderOrderKey == nil {
		return nil, ErrPaymentStateUnknown
	}
	checkoutID := retainedProviderAuthorityValue(model.PaymentProviderCreem, *order.ProviderOrderKey)
	if checkoutID == "" || len(checkoutID) > 255 {
		return nil, model.ErrPaymentManualReview
	}
	configuration := currentCreemConfigurationSnapshot(ctx)
	if order.ProviderLivemode == nil || *order.ProviderLivemode != !configuration.testMode {
		return nil, model.ErrPaymentManualReview
	}
	endpoint := creemAPIBaseURL(configuration.testMode) + "/v1/checkouts?checkout_id=" + url.QueryEscape(checkoutID)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("x-api-key", configuration.apiKey)
	response, err := p.httpClient().Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, creemMaximumResponseBytes+1))
	if err != nil || len(body) > creemMaximumResponseBytes {
		return nil, errors.New("Creem checkout retrieval response was incomplete")
	}
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Creem checkout retrieval returned HTTP %d", response.StatusCode)
	}
	var checkout creemCheckoutResponse
	if err := common.Unmarshal(body, &checkout); err != nil {
		return nil, err
	}
	checkout.ID = strings.TrimSpace(checkout.ID)
	if checkout.ID != checkoutID || checkout.RequestID != "" && strings.TrimSpace(checkout.RequestID) != order.TradeNo {
		return nil, model.ErrPaymentManualReview
	}
	return &checkout, nil
}

func (p *creemPaymentProvider) httpClient() *http.Client {
	if p != nil && p.client != nil {
		return p.client
	}
	return &http.Client{Timeout: 25 * time.Second}
}

func creemAPIBaseURL(testMode bool) string {
	if testMode {
		return "https://test-api.creem.io"
	}
	return "https://api.creem.io"
}

func creemProductIDForOrder(order *model.PaymentOrder) (string, error) {
	if order.OrderKind == model.PaymentOrderKindSubscription {
		snapshot, err := subscriptionPlanSnapshotForPayment(order)
		if err != nil {
			return "", err
		}
		if productID := strings.TrimSpace(snapshot.CreemProductId); productID != "" {
			return productID, nil
		}
		return "", errors.New("Creem subscription product is missing")
	}
	snapshot, err := retainedPaymentSnapshotForOrder(order)
	if err != nil {
		return "", err
	}
	if productID := strings.TrimSpace(snapshot.CreemProductID); productID != "" {
		return productID, nil
	}
	return "", errors.New("Creem top-up product is missing")
}
