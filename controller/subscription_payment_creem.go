package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type SubscriptionCreemPayRequest struct {
	PlanId    int    `json:"plan_id"`
	RequestID string `json:"request_id"`
}

func SubscriptionRequestCreemPay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req SubscriptionCreemPayRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		legacyPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}

	startRetainedCompatibilityPayment(c, req.RequestID, service.PaymentQuoteRequest{
		OrderKind:     model.PaymentOrderKindSubscription,
		Provider:      model.PaymentProviderCreem,
		PaymentMethod: model.PaymentMethodCreem,
		PlanID:        req.PlanId,
	})
}
