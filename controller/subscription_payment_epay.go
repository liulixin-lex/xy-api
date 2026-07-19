package controller

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type SubscriptionEpayPayRequest struct {
	PlanId        int    `json:"plan_id"`
	PaymentMethod string `json:"payment_method"`
	RequestID     string `json:"request_id,omitempty"`
}

func isSubscriptionEpayMethod(method string) bool {
	method = strings.TrimSpace(method)
	if method == "" || !operation_setting.ContainsPayMethod(method) {
		return false
	}
	switch method {
	case model.PaymentMethodStripe, model.PaymentMethodCreem, model.PaymentMethodWaffo,
		model.PaymentMethodWaffoPancake, model.PaymentMethodXorPayNative, model.PaymentMethodXorPayAlipay:
		return false
	default:
		return true
	}
}

func SubscriptionRequestEpay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var req SubscriptionEpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	if !isSubscriptionEpayMethod(req.PaymentMethod) {
		common.ApiErrorMsg(c, "支付方式不存在")
		return
	}
	start, err := startLegacySubscriptionPayment(c, model.PaymentProviderEpay, req.PaymentMethod, req.PlanId, req.RequestID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": start.Fields, "url": start.Action, "order_id": start.TradeNo})
}

func SubscriptionEpayNotify(c *gin.Context) {
	PaymentEpayNotify(c)
}

// SubscriptionEpayReturn is display-only. Browser return data must never
// grant an entitlement; only the asynchronous notify callback can do that.
func SubscriptionEpayReturn(c *gin.Context) {
	tradeNo := ""
	if c.Request.Method == "POST" {
		if err := c.Request.ParseForm(); err != nil {
			c.Redirect(http.StatusFound, paymentReturnPath("/console/topup?pay=pending"))
			return
		}
		tradeNo = strings.TrimSpace(c.Request.PostForm.Get("out_trade_no"))
	} else {
		tradeNo = strings.TrimSpace(c.Request.URL.Query().Get("out_trade_no"))
	}
	// Browser returns are display-only and may be replayed or tampered with.
	// Always show a pending state and let the authenticated canonical order
	// status decide the result; asynchronous signed notify is the only path that
	// can grant an entitlement.
	if tradeNo != "" && len(tradeNo) <= service.MaxPaymentRequestIDBytes {
		c.Redirect(http.StatusFound, paymentReturnPath("/console/topup?pay=pending&trade_no="+url.QueryEscape(tradeNo)))
		return
	}
	c.Redirect(http.StatusFound, paymentReturnPath("/console/topup?pay=pending"))
}
