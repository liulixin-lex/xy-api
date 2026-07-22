package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	waffo "github.com/waffo-com/waffo-go"
	"github.com/waffo-com/waffo-go/config"
	"github.com/waffo-com/waffo-go/core"
	waffoorder "github.com/waffo-com/waffo-go/types/order"
)

type waffoPaymentProvider struct{}

func init() {
	RegisterPaymentProvider(&waffoPaymentProvider{})
}

func (*waffoPaymentProvider) Name() string { return model.PaymentProviderWaffo }

func (*waffoPaymentProvider) ValidateMethod(method string) error {
	if NormalizePaymentMethod(model.PaymentProviderWaffo, method) != model.PaymentMethodWaffo {
		return errors.New("unsupported Waffo payment method")
	}
	return nil
}

func (*waffoPaymentProvider) Create(ctx context.Context, paymentOrder *model.PaymentOrder) (*PaymentStart, error) {
	if paymentOrder == nil {
		return nil, errors.New("payment order is required")
	}
	client, err := newWaffoPaymentClient()
	if err != nil {
		return nil, err
	}
	user, err := model.GetUserById(paymentOrder.UserID, false)
	if err != nil || user == nil {
		return nil, model.ErrPaymentUserUnavailable
	}
	returnURL, err := firstPartyPaymentReturnURL(paymentOrder.TradeNo)
	if err != nil {
		return nil, err
	}
	optionType, optionName, err := waffoOptionForOrder(paymentOrder)
	if err != nil {
		return nil, err
	}
	currency := strings.ToUpper(strings.TrimSpace(paymentOrder.Currency))
	amount := formatProviderMinorAmount(paymentOrder.ExpectedAmountMinor, model.PaymentProviderWaffo, currency)
	requestID := waffoPaymentRequestID(paymentOrder.TradeNo)
	goodsName := retainedOrderDescription(paymentOrder)
	appName := strings.TrimSpace(common.SystemName)
	if appName == "" {
		appName = "New API"
	}
	params := &waffoorder.CreateOrderParams{
		PaymentRequestID:   requestID,
		MerchantOrderID:    paymentOrder.TradeNo,
		OrderAmount:        amount,
		OrderCurrency:      currency,
		OrderDescription:   goodsName,
		OrderRequestedAt:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		OrderExpiredAt:     time.Unix(paymentOrder.ExpiresAt, 0).UTC().Format("2006-01-02T15:04:05.000Z"),
		NotifyURL:          strings.TrimRight(GetCallbackAddress(), "/") + "/api/waffo/webhook",
		SuccessRedirectURL: returnURL,
		FailedRedirectURL:  returnURL,
		CancelRedirectURL:  returnURL,
		MerchantInfo:       &waffoorder.MerchantInfo{MerchantID: setting.WaffoMerchantId},
		UserInfo: &waffoorder.UserInfo{
			UserID:       strconv.Itoa(user.Id),
			UserEmail:    waffoUserEmail(user),
			UserTerminal: "WEB",
		},
		PaymentInfo: &waffoorder.PaymentInfo{
			ProductName:   "ONE_TIME_PAYMENT",
			PayMethodType: optionType,
			PayMethodName: optionName,
		},
		GoodsInfo: &waffoorder.GoodsInfo{GoodsName: goodsName, AppName: appName},
	}
	response, err := client.Order().Create(ctx, params, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: Waffo order creation requires inquiry", ErrPaymentStateUnknown)
	}
	if response == nil || !response.IsSuccess() || response.GetData() == nil {
		return nil, fmt.Errorf("%w: Waffo order creation returned no durable result", ErrPaymentStateUnknown)
	}
	data := response.GetData()
	if strings.TrimSpace(data.PaymentRequestID) != requestID || strings.TrimSpace(data.MerchantOrderID) != paymentOrder.TradeNo {
		return nil, model.ErrPaymentManualReview
	}
	providerOrderKey := retainedProviderAuthorityKey(model.PaymentProviderWaffo, data.AcquiringOrderID)
	providerPaymentKey := "waffo_request:" + requestID
	if strings.TrimSpace(data.OrderAction) == "" {
		return &PaymentStart{
			Flow: PaymentFlowPending, ExpiresAt: paymentOrder.ExpiresAt,
			ProviderOrderKey: providerOrderKey, ProviderPaymentKey: providerPaymentKey,
		}, nil
	}
	flow, paymentURL, err := resolveWaffoPaymentStart(data.OrderAction)
	if err != nil {
		return nil, model.ErrPaymentManualReview
	}
	return &PaymentStart{
		Flow: flow, URL: paymentURL, ExpiresAt: paymentOrder.ExpiresAt,
		ProviderOrderKey: providerOrderKey, ProviderPaymentKey: providerPaymentKey,
	}, nil
}

func (p *waffoPaymentProvider) RecoverStart(ctx context.Context, paymentOrder *model.PaymentOrder) (*PaymentStart, error) {
	data, state, err := p.inquiry(ctx, paymentOrder)
	if err != nil {
		return nil, err
	}
	if state == "not_exist" || data == nil || strings.TrimSpace(data.OrderAction) == "" {
		return nil, ErrPaymentStateUnknown
	}
	flow, paymentURL, err := resolveWaffoPaymentStart(data.OrderAction)
	if err != nil {
		return nil, model.ErrPaymentManualReview
	}
	return &PaymentStart{
		Flow: flow, URL: paymentURL, ExpiresAt: paymentOrder.ExpiresAt,
		ProviderOrderKey:   retainedProviderAuthorityKey(model.PaymentProviderWaffo, data.AcquiringOrderID),
		ProviderPaymentKey: "waffo_request:" + waffoPaymentRequestID(paymentOrder.TradeNo),
	}, nil
}

func (p *waffoPaymentProvider) Query(ctx context.Context, paymentOrder *model.PaymentOrder) (*NormalizedPaymentEvent, error) {
	data, state, err := p.inquiry(ctx, paymentOrder)
	if err != nil {
		return nil, err
	}
	if state == "not_exist" {
		return &NormalizedPaymentEvent{
			Provider:  model.PaymentProviderWaffo,
			EventKey:  model.PaymentEventKey(model.PaymentProviderWaffo, "query:not_exist", "", paymentOrder.TradeNo, ""),
			EventType: "query:not_exist", TradeNo: paymentOrder.TradeNo,
			PaymentMethod: model.PaymentMethodWaffo,
		}, nil
	}
	if data == nil {
		return nil, errors.New("Waffo inquiry returned no order")
	}
	currency := strings.ToUpper(strings.TrimSpace(data.OrderCurrency))
	amount, amountErr := model.ParseProviderPaymentAmountMinor(data.FinalDealAmount, model.PaymentProviderWaffo, currency)
	if amountErr != nil || amount <= 0 {
		amount, _ = model.ParseProviderPaymentAmountMinor(data.OrderAmount, model.PaymentProviderWaffo, currency)
	}
	providerState := strings.ToUpper(strings.TrimSpace(data.OrderStatus))
	providerOrderKey := retainedProviderAuthorityKey(model.PaymentProviderWaffo, data.AcquiringOrderID)
	payload, err := common.Marshal(map[string]interface{}{
		"payment_request_id": data.PaymentRequestID,
		"merchant_order_id":  data.MerchantOrderID,
		"acquiring_order_id": data.AcquiringOrderID,
		"status":             providerState,
		"amount":             amount,
		"currency":           currency,
	})
	if err != nil {
		return nil, err
	}
	event := &NormalizedPaymentEvent{
		Provider:          model.PaymentProviderWaffo,
		EventKey:          model.PaymentEventKey(model.PaymentProviderWaffo, "query:"+providerState, providerOrderKey, paymentOrder.TradeNo, string(payload)),
		EventType:         "query:" + providerState,
		TradeNo:           paymentOrder.TradeNo,
		ProviderOrderKey:  providerOrderKey,
		ProviderState:     providerState,
		PaidAmountMinor:   amount,
		Currency:          currency,
		PaymentMethod:     model.PaymentMethodWaffo,
		NormalizedPayload: string(payload),
	}
	switch providerState {
	case core.OrderStatusPaySuccess:
		event.Paid = true
	case core.OrderStatusOrderClose:
		event.Failed = true
	case core.OrderStatusPayInProgress, core.OrderStatusAuthorizationRequired, core.OrderStatusAuthedWaitingCapture:
	default:
		event.ManualReview = true
	}
	return event, nil
}

func (*waffoPaymentProvider) VerifyWebhook(_ *http.Request) (*NormalizedPaymentEvent, error) {
	return nil, errors.New("Waffo webhooks use the provider-specific verified endpoint")
}

func (*waffoPaymentProvider) inquiry(ctx context.Context, paymentOrder *model.PaymentOrder) (*waffoorder.InquiryOrderData, string, error) {
	if paymentOrder == nil {
		return nil, "", errors.New("payment order is required")
	}
	client, err := newWaffoPaymentClient()
	if err != nil {
		return nil, "", errors.New("Waffo payment configuration is invalid")
	}
	response, err := client.Order().Inquiry(ctx, &waffoorder.InquiryOrderParams{
		PaymentRequestID: waffoPaymentRequestID(paymentOrder.TradeNo),
	}, nil)
	if err != nil {
		return nil, "", errors.New("Waffo inquiry request failed")
	}
	if response == nil || !response.IsSuccess() {
		message := ""
		if response != nil {
			message = strings.ToLower(strings.TrimSpace(response.GetMessage()))
		}
		if strings.Contains(message, "not found") || strings.Contains(message, "not exist") {
			return nil, "not_exist", nil
		}
		return nil, "", errors.New("Waffo inquiry request was rejected")
	}
	data := response.GetData()
	if data == nil || strings.TrimSpace(data.MerchantOrderID) == "" {
		return nil, "not_exist", nil
	}
	if strings.TrimSpace(data.PaymentRequestID) != waffoPaymentRequestID(paymentOrder.TradeNo) ||
		strings.TrimSpace(data.MerchantOrderID) != paymentOrder.TradeNo {
		return nil, "", model.ErrPaymentManualReview
	}
	return data, strings.ToUpper(strings.TrimSpace(data.OrderStatus)), nil
}

func newWaffoPaymentClient() (*waffo.Waffo, error) {
	environment := config.Sandbox
	apiKey := setting.WaffoSandboxApiKey
	privateKey := setting.WaffoSandboxPrivateKey
	publicKey := setting.WaffoSandboxPublicCert
	if !setting.WaffoSandbox {
		environment = config.Production
		apiKey = setting.WaffoApiKey
		privateKey = setting.WaffoPrivateKey
		publicKey = setting.WaffoPublicCert
	}
	builder := config.NewConfigBuilder().APIKey(apiKey).PrivateKey(privateKey).WaffoPublicKey(publicKey).Environment(environment)
	if merchantID := strings.TrimSpace(setting.WaffoMerchantId); merchantID != "" {
		builder = builder.MerchantID(merchantID)
	}
	configuration, err := builder.Build()
	if err != nil {
		return nil, err
	}
	return waffo.New(configuration), nil
}

func waffoOptionForOrder(paymentOrder *model.PaymentOrder) (string, string, error) {
	if paymentOrder.OrderKind != model.PaymentOrderKindTopUp {
		return "", "", nil
	}
	snapshot, err := retainedPaymentSnapshotForOrder(paymentOrder)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(snapshot.WaffoOptionType), strings.TrimSpace(snapshot.WaffoOptionName), nil
}

func resolveWaffoPaymentStart(orderAction string) (string, string, error) {
	var action waffoorder.OrderAction
	if err := common.UnmarshalJsonStr(strings.TrimSpace(orderAction), &action); err != nil {
		return "", "", errors.New("invalid Waffo order action")
	}
	if strings.EqualFold(strings.TrimSpace(action.ActionType), "DEEPLINK") {
		deeplinkURL := strings.TrimSpace(action.DeeplinkURL)
		if deeplinkURL != "" && ValidateConfiguredWaffoAppPaymentURL(deeplinkURL) == nil {
			return PaymentFlowAppRedirect, deeplinkURL, nil
		}
	}
	webURL := strings.TrimSpace(action.WebURL)
	if err := ValidateConfiguredWaffoWebPaymentURL(webURL); err != nil {
		return "", "", err
	}
	return PaymentFlowHostedRedirect, webURL, nil
}

func waffoUserEmail(user *model.User) string {
	if user != nil && strings.TrimSpace(user.Email) != "" {
		return strings.TrimSpace(user.Email)
	}
	if user == nil {
		return ""
	}
	return fmt.Sprintf("%d@examples.com", user.Id)
}

func formatProviderMinorAmount(amountMinor int64, provider, currency string) string {
	exponent := common.PaymentProviderCurrencyExponent(provider, currency)
	return formatPaymentMinor(amountMinor, exponent)
}
