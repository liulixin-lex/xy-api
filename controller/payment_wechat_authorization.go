package controller

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func BeginWeChatPaymentAuthorization(c *gin.Context) {
	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	if tradeNo == "" || len(tradeNo) > 128 || !strings.Contains(strings.ToLower(c.GetHeader("User-Agent")), "micromessenger") {
		redirectPaymentAuthorizationFailure(c, tradeNo)
		return
	}
	location, err := service.BeginWeChatPaymentAuthorization(c.GetInt("id"), tradeNo)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("WeChat payment authorization start failed user_id=%d trade_no=%s error=%q", c.GetInt("id"), tradeNo, err.Error()))
		redirectPaymentAuthorizationFailure(c, tradeNo)
		return
	}
	c.Redirect(http.StatusSeeOther, location)
}

func CompleteWeChatPaymentAuthorization(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	rawQuery := c.Request.URL.RawQuery
	// XORPay's published OpenID protocol returns the identifier in the query
	// string. Scrub it immediately so application middleware running after the
	// handler cannot retain it. Production ingress access logs must also exclude
	// the query string for this dedicated callback route.
	c.Request.URL.RawQuery = ""
	c.Request.RequestURI = c.Request.URL.Path
	if len(rawQuery) == 0 || len(rawQuery) > 1024 {
		redirectPaymentAuthorizationFailure(c, "")
		return
	}
	query, parseErr := url.ParseQuery(rawQuery)
	if parseErr != nil || len(query["state"]) != 1 || len(query["openid"]) != 1 {
		redirectPaymentAuthorizationFailure(c, "")
		return
	}
	state := strings.TrimSpace(query.Get("state"))
	openID := strings.TrimSpace(query.Get("openid"))
	order, err := service.CompleteWeChatPaymentAuthorization(state, openID)
	if err != nil {
		// OpenID is deliberately excluded from every log line.
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("WeChat payment authorization callback failed state_present=%t error=%q", state != "", err.Error()))
		if order == nil {
			c.Redirect(http.StatusSeeOther, "/wallet?payment_error=payment_authorization_failed")
			return
		}
	}
	c.Redirect(http.StatusSeeOther, "/payment/"+url.PathEscape(order.TradeNo))
}

func redirectPaymentAuthorizationFailure(c *gin.Context, tradeNo string) {
	if tradeNo == "" || len(tradeNo) > 128 {
		c.Redirect(http.StatusSeeOther, "/wallet?payment_error=payment_authorization_failed")
		return
	}
	c.Redirect(http.StatusSeeOther, "/payment/"+url.PathEscape(tradeNo)+"?authorization=failed")
}
