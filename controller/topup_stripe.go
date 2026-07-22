package controller

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

var stripeAdaptor = &StripeAdaptor{}

// StripePayRequest represents a payment request for Stripe checkout.
type StripePayRequest struct {
	// Amount is the quantity of units to purchase.
	Amount int64 `json:"amount"`
	// PaymentMethod specifies the payment method (e.g., "stripe").
	PaymentMethod string `json:"payment_method"`
	// SuccessURL is the optional custom URL to redirect after successful payment.
	// If empty, defaults to the server's console log page.
	SuccessURL string `json:"success_url,omitempty"`
	// CancelURL is the optional custom URL to redirect when payment is canceled.
	// If empty, defaults to the server's console topup page.
	CancelURL string `json:"cancel_url,omitempty"`
	// RequestID is an optional client idempotency key. Older clients may omit it
	// and receive a random compatibility key.
	RequestID string `json:"request_id,omitempty"`
}

type StripeAdaptor struct {
}

func (*StripeAdaptor) RequestAmount(c *gin.Context, req *StripePayRequest) {
	quote, err := service.PreviewPaymentQuote(c.Request.Context(), c.GetInt("id"), service.PaymentQuoteRequest{
		OrderKind:     model.PaymentOrderKindTopUp,
		Provider:      model.PaymentProviderStripe,
		PaymentMethod: model.PaymentMethodStripe,
		Amount:        req.Amount,
	})
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Stripe 报价失败 user_id=%d amount=%d error=%q", c.GetInt("id"), req.Amount, err.Error()))
		legacyPaymentServiceAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": quote.PayableAmount})
}

func (*StripeAdaptor) RequestPay(c *gin.Context, req *StripePayRequest) {
	if req.PaymentMethod != model.PaymentMethodStripe {
		legacyPaymentAPIError(c, "payment_method_unavailable", nil)
		return
	}
	if req.SuccessURL != "" && common.ValidateRedirectURL(req.SuccessURL) != nil {
		legacyPaymentAPIErrorStatus(c, http.StatusBadRequest, "payment_redirect_invalid", nil)
		return
	}

	if req.CancelURL != "" && common.ValidateRedirectURL(req.CancelURL) != nil {
		legacyPaymentAPIErrorStatus(c, http.StatusBadRequest, "payment_redirect_invalid", nil)
		return
	}

	start, err := startLegacyTopUpPaymentWithReturnURLs(c, model.PaymentProviderStripe, model.PaymentMethodStripe, req.Amount, req.RequestID, req.SuccessURL, req.CancelURL)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Stripe 创建 Checkout Session 失败 user_id=%d amount=%d error=%q", c.GetInt("id"), req.Amount, err.Error()))
		legacyPaymentServiceAPIError(c, err)
		return
	}
	if start == nil || strings.TrimSpace(start.TradeNo) == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Stripe 返回无效本地订单 user_id=%d", c.GetInt("id")))
		legacyPaymentAPIError(c, "payment_not_ready", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"pay_link": legacyPaymentPageURL(start.TradeNo),
			"trade_no": start.TradeNo,
		},
	})
}

func RequestStripeAmount(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req StripePayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}
	stripeAdaptor.RequestAmount(c, &req)
}

func RequestStripePay(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req StripePayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}
	stripeAdaptor.RequestPay(c, &req)
}

func StripeWebhook(c *gin.Context) {
	processUnifiedPaymentWebhook(c, model.PaymentProviderStripe, "")
}
