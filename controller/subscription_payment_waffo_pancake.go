package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type SubscriptionWaffoPancakePayRequest struct {
	PlanId    int    `json:"plan_id"`
	RequestID string `json:"request_id"`
}

func SubscriptionRequestWaffoPancakePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req SubscriptionWaffoPancakePayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		compatibilityPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}

	startRetainedCompatibilityPayment(c, req.RequestID, service.PaymentQuoteRequest{
		OrderKind:     model.PaymentOrderKindSubscription,
		Provider:      model.PaymentProviderWaffoPancake,
		PaymentMethod: model.PaymentMethodWaffoPancake,
		PlanID:        req.PlanId,
	})
}
