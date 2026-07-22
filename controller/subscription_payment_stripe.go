package controller

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

type SubscriptionStripePayRequest struct {
	PlanId    int    `json:"plan_id"`
	RequestID string `json:"request_id,omitempty"`
}

func SubscriptionRequestStripePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req SubscriptionStripePayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		compatibilityPaymentAPIError(c, "payment_request_invalid", nil)
		return
	}

	start, err := startLegacySubscriptionPayment(c, model.PaymentProviderStripe, model.PaymentMethodStripe, req.PlanId, req.RequestID)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 一次性固定期限权益购买支付启动失败 plan_id=%d error=%q", req.PlanId, err.Error()))
		compatibilityPaymentServiceAPIError(c, err)
		return
	}
	if start == nil || strings.TrimSpace(start.TradeNo) == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Stripe 一次性固定期限权益购买返回无效本地订单 user_id=%d plan_id=%d", c.GetInt("id"), req.PlanId))
		compatibilityPaymentAPIError(c, "payment_not_ready", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{
		"pay_link": legacyPaymentPageURL(start.TradeNo), "order_id": start.TradeNo,
	}})
}
