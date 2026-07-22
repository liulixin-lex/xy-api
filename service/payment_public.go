package service

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type PublicPaymentCheckoutView struct {
	Flow        string                  `json:"flow"`
	QRContent   string                  `json:"qr_content,omitempty"`
	ContinueURL string                  `json:"continue_url,omitempty"`
	JSAPI       *PaymentJSAPIParameters `json:"jsapi,omitempty"`
	ExpiresAt   int64                   `json:"expires_at"`
}

// PublicPaymentCheckout returns only the minimum browser instructions needed
// by the first-party checkout page. Hosted URLs, signed form fields and all
// provider identity stay server-side behind the authenticated continuation
// endpoint.
func PublicPaymentCheckout(order *model.PaymentOrder) (*PublicPaymentCheckoutView, error) {
	if order == nil || strings.TrimSpace(order.TradeNo) == "" {
		return nil, model.ErrPaymentOrderNotFound
	}
	view := &PublicPaymentCheckoutView{
		Flow:      PaymentFlowPending,
		ExpiresAt: order.ExpiresAt,
	}
	if order.Status != model.PaymentOrderStatusPending && order.Status != model.PaymentOrderStatusProcessing {
		return view, nil
	}
	if order.StartPayload == "" {
		if order.Provider == model.PaymentProviderXorPay && order.PaymentMethod == model.PaymentMethodXorPayJSAPI &&
			order.BrowserAuthorizedAt == 0 {
			view.Flow = PaymentFlowWeChatAuthorize
			view.ContinueURL = "/api/user/payment/orders/" + url.PathEscape(order.TradeNo) + "/wechat-authorize"
		}
		return view, nil
	}
	start, err := storedPaymentStart(order)
	if err != nil {
		return nil, err
	}
	view.Flow = start.Flow
	if start.ExpiresAt > 0 {
		view.ExpiresAt = start.ExpiresAt
	}
	switch start.Flow {
	case PaymentFlowQR:
		if strings.TrimSpace(start.QRContent) == "" || len(start.QRContent) > 4096 {
			return nil, errors.New("invalid stored payment QR state")
		}
		if order.Provider == model.PaymentProviderXorPay {
			method, methodErr := xorPayUpstreamMethod(order.PaymentMethod)
			if methodErr != nil || validateXorPayQR(method, start.QRContent) != nil {
				return nil, errors.New("invalid stored payment QR state")
			}
		}
		view.QRContent = start.QRContent
	case PaymentFlowHostedRedirect, PaymentFlowAppRedirect, PaymentFlowFormPost:
		// App redirects use the same first-party browser instruction as hosted
		// pages. The provider URL and custom scheme remain encrypted server-side.
		if start.Flow == PaymentFlowAppRedirect {
			view.Flow = PaymentFlowHostedRedirect
		}
		view.ContinueURL = "/api/user/payment/orders/" + url.PathEscape(order.TradeNo) + "/continue"
	case PaymentFlowJSAPI:
		if start.JSAPI == nil {
			return nil, errors.New("invalid stored JSAPI payment state")
		}
		parameters := *start.JSAPI
		view.JSAPI = &parameters
	case PaymentFlowPending:
	default:
		return nil, fmt.Errorf("unsupported stored payment flow: %s", start.Flow)
	}
	return view, nil
}

func PaymentContinuation(userID int, tradeNo string) (*PaymentStart, error) {
	order, err := model.GetPaymentOrderForUser(userID, strings.TrimSpace(tradeNo))
	if err != nil {
		return nil, err
	}
	if order.ExpiresAt > 0 && order.ExpiresAt <= common.GetTimestamp() {
		return nil, model.ErrPaymentQuoteExpired
	}
	if order.Status != model.PaymentOrderStatusPending && order.Status != model.PaymentOrderStatusProcessing {
		return nil, fmt.Errorf("payment order cannot be continued: %s", order.Status)
	}
	start, err := storedPaymentStart(order)
	if err != nil {
		return nil, err
	}
	if start.Flow != PaymentFlowHostedRedirect && start.Flow != PaymentFlowAppRedirect && start.Flow != PaymentFlowFormPost {
		return nil, errors.New("payment order does not use browser continuation")
	}
	start.Provider = order.Provider
	start.PaymentMethod = order.PaymentMethod
	return start, nil
}

func storedPaymentStart(order *model.PaymentOrder) (*PaymentStart, error) {
	if order == nil || order.StartPayload == "" {
		return nil, errors.New("payment is not ready")
	}
	plaintext, err := model.DecryptPaymentOrderStartPayload(order.TradeNo, order.StartPayload)
	if err != nil {
		return nil, errors.New("invalid stored payment start state")
	}
	var start PaymentStart
	if err := common.UnmarshalJsonStr(plaintext, &start); err != nil {
		return nil, errors.New("invalid stored payment start state")
	}
	if start.TradeNo != "" && start.TradeNo != order.TradeNo {
		return nil, errors.New("stored payment start order mismatch")
	}
	start.TradeNo = order.TradeNo
	return &start, nil
}
