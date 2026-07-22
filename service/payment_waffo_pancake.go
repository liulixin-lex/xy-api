package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

type waffoPancakePaymentProvider struct{}

type waffoPancakePaymentQueryItem struct {
	ID                      string  `json:"id"`
	OrderID                 string  `json:"orderId"`
	Status                  string  `json:"status"`
	TestMode                bool    `json:"testMode"`
	OrderMerchantExternalID *string `json:"orderMerchantExternalId"`
	SnapshotAmountDetails   struct {
		Currency string `json:"currency"`
		Total    string `json:"total"`
	} `json:"snapshotAmountDetails"`
}

type waffoPancakePaymentQuery struct {
	Payments []waffoPancakePaymentQueryItem `json:"payments"`
}

func init() {
	RegisterPaymentProvider(&waffoPancakePaymentProvider{})
}

func (*waffoPancakePaymentProvider) Name() string { return model.PaymentProviderWaffoPancake }

func (*waffoPancakePaymentProvider) ValidateMethod(method string) error {
	if NormalizePaymentMethod(model.PaymentProviderWaffoPancake, method) != model.PaymentMethodWaffoPancake {
		return errors.New("unsupported Waffo Pancake payment method")
	}
	return nil
}

func (*waffoPancakePaymentProvider) Create(ctx context.Context, order *model.PaymentOrder) (*PaymentStart, error) {
	if order == nil {
		return nil, errors.New("payment order is required")
	}
	configuration := currentWaffoPancakeConfigurationSnapshotForContext(ctx)
	if order.ProviderLivemode == nil || *order.ProviderLivemode != !configuration.testMode {
		return nil, model.ErrPaymentManualReview
	}
	productID, err := waffoPancakeProductIDForOrder(order)
	if err != nil {
		return nil, err
	}
	user, err := model.GetUserById(order.UserID, false)
	if err != nil || user == nil {
		return nil, model.ErrPaymentUserUnavailable
	}
	returnURL, err := firstPartyPaymentReturnURL(order.TradeNo)
	if err != nil {
		return nil, err
	}
	expiresInSeconds := int(time.Until(time.Unix(order.ExpiresAt, 0)).Seconds())
	if expiresInSeconds <= 0 {
		return nil, model.ErrPaymentQuoteExpired
	}
	amount := formatProviderMinorAmount(order.ExpectedAmountMinor, model.PaymentProviderWaffoPancake, order.Currency)
	session, err := createWaffoPancakeCheckoutSessionWithConfiguration(ctx, &WaffoPancakeCreateSessionParams{
		ProductID:     productID,
		BuyerIdentity: WaffoPancakeBuyerIdentityFromUserID(user.Id),
		PriceSnapshot: &WaffoPancakePriceSnapshot{
			Amount:      amount,
			TaxCategory: "saas",
		},
		BuyerEmail:              strings.TrimSpace(user.Email),
		SuccessURL:              returnURL,
		ExpiresInSeconds:        &expiresInSeconds,
		OrderMerchantExternalID: order.TradeNo,
	}, configuration)
	if err != nil {
		return nil, fmt.Errorf("%w: Waffo Pancake checkout creation did not return a durable result", ErrPaymentStateUnknown)
	}
	if session == nil || strings.TrimSpace(session.CheckoutURL) == "" {
		return nil, fmt.Errorf("%w: Waffo Pancake checkout creation returned no payment instructions", ErrPaymentStateUnknown)
	}
	return &PaymentStart{
		Flow:      PaymentFlowHostedRedirect,
		URL:       strings.TrimSpace(session.CheckoutURL),
		ExpiresAt: order.ExpiresAt,
	}, nil
}

func (*waffoPancakePaymentProvider) Query(ctx context.Context, order *model.PaymentOrder) (*NormalizedPaymentEvent, error) {
	if order == nil {
		return nil, errors.New("payment order is required")
	}
	configuration := currentWaffoPancakeConfigurationSnapshotForContext(ctx)
	if order.ProviderLivemode == nil || *order.ProviderLivemode != !configuration.testMode {
		return nil, model.ErrPaymentManualReview
	}
	client, err := newWaffoPancakeClientFromConfiguration(configuration)
	if err != nil {
		return nil, errors.New("Waffo Pancake payment configuration is invalid")
	}
	response, err := pancake.GraphQLQuery[waffoPancakePaymentQuery](ctx, client, pancake.GraphQLParams{
		Query: `query ($ref: String!) {
			payments(filter: { orderMerchantExternalId: { eq: $ref } }) {
				id orderId status testMode orderMerchantExternalId
				snapshotAmountDetails { currency total }
			}
		}`,
		Variables: map[string]any{"ref": order.TradeNo},
	})
	if err != nil {
		return nil, errors.New("Waffo Pancake payment query failed")
	}
	if response == nil || len(response.Errors) > 0 {
		return nil, errors.New("Waffo Pancake payment query was rejected")
	}
	if len(response.Data.Payments) == 0 {
		return &NormalizedPaymentEvent{
			Provider:  model.PaymentProviderWaffoPancake,
			EventKey:  model.PaymentEventKey(model.PaymentProviderWaffoPancake, "query:not_exist", "", order.TradeNo, ""),
			EventType: "query:not_exist", TradeNo: order.TradeNo,
			PaymentMethod: model.PaymentMethodWaffoPancake,
		}, nil
	}

	expectedTestMode := !*order.ProviderLivemode
	selected := -1
	succeeded := 0
	mismatched := -1
	for index := range response.Data.Payments {
		payment := &response.Data.Payments[index]
		if payment.OrderMerchantExternalID == nil || strings.TrimSpace(*payment.OrderMerchantExternalID) != order.TradeNo {
			return nil, model.ErrPaymentManualReview
		}
		if payment.TestMode != expectedTestMode {
			if mismatched == -1 {
				mismatched = index
			}
			continue
		}
		if selected == -1 {
			selected = index
		}
		if strings.EqualFold(strings.TrimSpace(payment.Status), string(pancake.PaymentStatusSucceeded)) {
			selected = index
			succeeded++
		}
	}
	if succeeded > 1 {
		return nil, model.ErrPaymentManualReview
	}
	if selected == -1 {
		if mismatched == -1 {
			return nil, model.ErrPaymentManualReview
		}
		return normalizedWaffoPancakeQueryEvent(order, response.Data.Payments[mismatched], true)
	}
	return normalizedWaffoPancakeQueryEvent(order, response.Data.Payments[selected], false)
}

func normalizedWaffoPancakeQueryEvent(
	order *model.PaymentOrder,
	payment waffoPancakePaymentQueryItem,
	environmentMismatch bool,
) (*NormalizedPaymentEvent, error) {
	providerState := strings.ToLower(strings.TrimSpace(payment.Status))
	currency := strings.ToUpper(strings.TrimSpace(payment.SnapshotAmountDetails.Currency))
	amount := int64(0)
	if parsed, parseErr := decimal.NewFromString(strings.TrimSpace(payment.SnapshotAmountDetails.Total)); parseErr == nil {
		amount, _ = paymentAmountMinorForProvider(parsed, model.PaymentProviderWaffoPancake, currency)
	}
	providerOrderKey := retainedProviderAuthorityKey(model.PaymentProviderWaffoPancake, payment.OrderID)
	payload, err := common.Marshal(map[string]interface{}{
		"payment_id":           payment.ID,
		"order_id":             payment.OrderID,
		"status":               providerState,
		"test_mode":            payment.TestMode,
		"environment_mismatch": environmentMismatch,
		"amount":               amount,
		"currency":             currency,
	})
	if err != nil {
		return nil, err
	}
	event := &NormalizedPaymentEvent{
		Provider:          model.PaymentProviderWaffoPancake,
		EventKey:          model.PaymentEventKey(model.PaymentProviderWaffoPancake, "query:"+providerState, providerOrderKey, order.TradeNo, string(payload)),
		EventType:         "query:" + providerState,
		TradeNo:           order.TradeNo,
		ProviderOrderKey:  providerOrderKey,
		ProviderState:     providerState,
		PaidAmountMinor:   amount,
		Currency:          currency,
		PaymentMethod:     model.PaymentMethodWaffoPancake,
		NormalizedPayload: string(payload),
	}
	providerLivemode := !payment.TestMode
	event.ProviderLivemode = &providerLivemode
	if environmentMismatch {
		event.ManualReview = true
		return event, nil
	}
	switch pancake.PaymentStatus(providerState) {
	case pancake.PaymentStatusSucceeded:
		event.Paid = true
	case pancake.PaymentStatusFailed, pancake.PaymentStatusCanceled:
		event.Failed = true
		event.PermanentFailure = true
	case pancake.PaymentStatusPending:
	default:
		event.ManualReview = true
	}
	return event, nil
}

func (*waffoPancakePaymentProvider) VerifyWebhook(_ *http.Request) (*NormalizedPaymentEvent, error) {
	return nil, errors.New("Waffo Pancake webhooks use the provider-specific verified endpoint")
}

func waffoPancakeProductIDForOrder(order *model.PaymentOrder) (string, error) {
	if order.OrderKind == model.PaymentOrderKindSubscription {
		snapshot, err := subscriptionPlanSnapshotForPayment(order)
		if err != nil {
			return "", err
		}
		if productID := strings.TrimSpace(snapshot.WaffoPancakeProductId); productID != "" {
			return productID, nil
		}
		return "", errors.New("Waffo Pancake subscription product is missing")
	}
	snapshot, err := retainedPaymentSnapshotForOrder(order)
	if err != nil {
		return "", err
	}
	if productID := strings.TrimSpace(snapshot.PancakeProductID); productID != "" {
		return productID, nil
	}
	return "", errors.New("Waffo Pancake top-up product is missing")
}
