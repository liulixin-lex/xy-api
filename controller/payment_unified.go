package controller

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type paymentOrderView struct {
	TradeNo       string                             `json:"trade_no"`
	RouteID       string                             `json:"route_id"`
	PublicMethod  string                             `json:"public_method"`
	ChannelAlias  string                             `json:"channel_alias"`
	StatusCode    string                             `json:"status_code"`
	PaymentAmount string                             `json:"payment_amount"`
	TopUpAmount   int64                              `json:"top_up_amount,omitempty"`
	PlanID        int64                              `json:"plan_id,omitempty"`
	Currency      string                             `json:"currency"`
	ExpiresAt     int64                              `json:"expires_at"`
	CompletedAt   int64                              `json:"completed_at,omitempty"`
	Checkout      *service.PublicPaymentCheckoutView `json:"checkout"`
}

type publicPaymentQuoteRequest struct {
	OrderKind string `json:"order_kind"`
	RouteID   string `json:"route_id"`
	Amount    int64  `json:"amount,omitempty"`
	PlanID    int    `json:"plan_id,omitempty"`
	ProductID string `json:"product_id,omitempty"`
	OptionID  string `json:"option_id,omitempty"`

	// Deprecated compatibility input. New clients send route_id and neither
	// field is ever reflected in a public response.
	Provider      string `json:"provider,omitempty"`
	PaymentMethod string `json:"payment_method,omitempty"`
}

type publicPaymentQuoteView struct {
	QuoteID       string `json:"quote_id"`
	RouteID       string `json:"route_id"`
	PublicMethod  string `json:"public_method"`
	ChannelAlias  string `json:"channel_alias"`
	TopUpAmount   int64  `json:"top_up_amount,omitempty"`
	PlanID        int64  `json:"plan_id,omitempty"`
	PayableAmount string `json:"payable_amount"`
	Currency      string `json:"currency"`
	ExpiresAt     int64  `json:"expires_at"`
}

type publicPaymentStartView struct {
	Flow      string `json:"flow"`
	TradeNo   string `json:"trade_no"`
	ExpiresAt int64  `json:"expires_at"`
}

const paymentRequestBodyLimit = 64 << 10

func CreatePaymentQuote(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request publicPaymentQuoteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_request_invalid", "Invalid payment request", nil)
		return
	}
	if !ensurePublicPaymentClusterReady(c) {
		return
	}
	var route *service.PublicPaymentRoute
	var err error
	if strings.TrimSpace(request.RouteID) != "" {
		route, err = service.ResolvePublicPaymentRoute(request.RouteID)
	} else {
		route, err = service.ResolveLegacyPublicPaymentRoute(request.Provider, request.PaymentMethod)
	}
	if err != nil {
		if !errors.Is(err, service.ErrPublicPaymentRouteNotFound) {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment route resolution failed user_id=%d error=%q", c.GetInt("id"), err.Error()))
			paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "payment_temporarily_unavailable", "Payment is temporarily unavailable", nil)
			return
		}
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_method_unavailable", "Payment method is unavailable", nil)
		return
	}
	if route == nil {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_method_unavailable", "Payment method is unavailable", nil)
		return
	}
	quote, err := service.CreatePaymentQuote(c.Request.Context(), c.GetInt("id"), service.PaymentQuoteRequest{
		OrderKind:     request.OrderKind,
		Provider:      route.Provider,
		PaymentMethod: route.PaymentMethod,
		Amount:        request.Amount,
		PlanID:        request.PlanID,
		ProductID:     request.ProductID,
		OptionID:      request.OptionID,
	})
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment quote failed user_id=%d route_id=%s error=%q", c.GetInt("id"), route.RouteID, err.Error()))
		paymentServiceAPIError(c, err)
		return
	}
	view := publicPaymentQuoteView{
		QuoteID: quote.QuoteID, RouteID: route.RouteID, PublicMethod: route.PublicMethod,
		ChannelAlias: route.ChannelAlias, PayableAmount: quote.PayableAmount,
		Currency: quote.Currency, ExpiresAt: quote.ExpiresAt,
	}
	if quote.OrderKind == model.PaymentOrderKindSubscription {
		view.PlanID = quote.RequestedAmount
	} else {
		view.TopUpAmount = quote.RequestedAmount
	}
	common.ApiSuccess(c, view)
}

func StartPayment(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request service.PaymentStartRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_request_invalid", "Invalid payment request", nil)
		return
	}
	if !ensurePublicPaymentClusterReady(c) {
		return
	}
	start, err := service.StartPayment(c.Request.Context(), c.GetInt("id"), request)
	if err != nil {
		if errors.Is(err, service.ErrPaymentStateUnknown) && start != nil && start.TradeNo != "" {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment start is awaiting provider confirmation user_id=%d trade_no=%s error=%q", c.GetInt("id"), start.TradeNo, err.Error()))
			c.JSON(http.StatusAccepted, gin.H{
				"success": true,
				"data":    publicPaymentStart(start),
			})
			return
		}
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment start failed user_id=%d state_unknown=%t error=%q", c.GetInt("id"), errors.Is(err, service.ErrPaymentStateUnknown), err.Error()))
		paymentServiceAPIError(c, err)
		return
	}
	common.ApiSuccess(c, publicPaymentStart(start))
}

func publicPaymentStart(start *service.PaymentStart) publicPaymentStartView {
	if start == nil {
		return publicPaymentStartView{Flow: service.PaymentFlowPending}
	}
	return publicPaymentStartView{
		Flow:      start.Flow,
		TradeNo:   start.TradeNo,
		ExpiresAt: start.ExpiresAt,
	}
}

// startRetainedCompatibilityPayment keeps historical provider-specific routes
// functional while routing every new operation through the canonical quote,
// order and durable background-task lifecycle. The compatibility response
// contains only a first-party order reference; provider checkout URLs remain
// encrypted in the canonical order and are served through /continue.
func startRetainedCompatibilityPayment(c *gin.Context, requestID string, request service.PaymentQuoteRequest) {
	requestID = strings.TrimSpace(requestID)
	if err := service.ValidatePaymentRequestID(requestID); err != nil {
		compatibilityPaymentServiceAPIError(c, err)
		return
	}
	if !ensurePublicPaymentClusterReady(c) {
		return
	}
	quote, err := service.CreatePaymentQuote(c.Request.Context(), c.GetInt("id"), request)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"compatibility payment quote failed user_id=%d order_kind=%s error=%q",
			c.GetInt("id"), request.OrderKind, err.Error(),
		))
		compatibilityPaymentServiceAPIError(c, err)
		return
	}
	start, err := service.StartPayment(c.Request.Context(), c.GetInt("id"), service.PaymentStartRequest{
		QuoteID: quote.QuoteID, RequestID: requestID,
	})
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"compatibility payment start failed user_id=%d order_kind=%s error=%q",
			c.GetInt("id"), request.OrderKind, err.Error(),
		))
		compatibilityPaymentServiceAPIError(c, err)
		return
	}
	tradeNo := strings.TrimSpace(start.TradeNo)
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"trade_no":   tradeNo,
			"order_id":   tradeNo,
			"pay_link":   "/payment/" + url.PathEscape(tradeNo),
			"flow":       start.Flow,
			"expires_at": start.ExpiresAt,
		},
	})
}

func GetPaymentOrder(c *gin.Context) {
	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	if tradeNo == "" || len(tradeNo) > 128 {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "payment_request_invalid", "Invalid payment order", nil)
		return
	}
	order, err := model.GetPaymentOrderForUser(c.GetInt("id"), tradeNo)
	if err != nil {
		if errors.Is(err, model.ErrPaymentOrderNotFound) {
			paymentAPIErrorWithCode(c, http.StatusNotFound, "payment_order_not_found", "Payment order not found", nil)
			return
		}
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment order lookup failed user_id=%d trade_no=%s error=%q", c.GetInt("id"), tradeNo, err.Error()))
		paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "payment_temporarily_unavailable", "Payment is temporarily unavailable", nil)
		return
	}
	route := service.PublicPaymentRouteForOrder(order.Provider, order.PaymentMethod)
	checkout, err := service.PublicPaymentCheckout(order)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("public payment checkout decode failed user_id=%d trade_no=%s error=%q", c.GetInt("id"), tradeNo, err.Error()))
		paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "payment_temporarily_unavailable", "Payment is temporarily unavailable", nil)
		return
	}
	view := paymentOrderView{
		TradeNo: order.TradeNo, RouteID: route.RouteID, PublicMethod: route.PublicMethod,
		ChannelAlias: route.ChannelAlias, StatusCode: paymentOrderPublicStatusCode(order),
		PaymentAmount: formatPublicPaymentAmount(order.ExpectedAmountMinor, 0, order.Provider, order.Currency),
		Currency:      order.Currency, ExpiresAt: order.ExpiresAt, CompletedAt: order.SettledAt,
		Checkout: checkout,
	}
	if order.OrderKind == model.PaymentOrderKindSubscription {
		view.PlanID = order.RequestedAmount
	} else {
		view.TopUpAmount = order.RequestedAmount
	}
	common.ApiSuccess(c, view)
}

// formatPublicPaymentAmount converts the server-authoritative expected amount
// into a fixed-point decimal string. The optional decimal fallback exists only
// for historical rows that predate canonical minor-unit snapshots.
func formatPublicPaymentAmount(minor int64, fallback float64, provider, currency string) string {
	exponent := common.PaymentProviderCurrencyExponent(provider, currency)
	if minor > 0 {
		return decimal.New(minor, -exponent).StringFixed(exponent)
	}
	if math.IsNaN(fallback) || math.IsInf(fallback, 0) || fallback < 0 {
		fallback = 0
	}
	return decimal.NewFromFloat(fallback).Round(exponent).StringFixed(exponent)
}

func paymentOrderPublicStatusCode(order *model.PaymentOrder) string {
	if order == nil {
		return "temporarily_unavailable"
	}
	switch order.Status {
	case model.PaymentOrderStatusPending:
		if order.StartedAt > 0 || strings.TrimSpace(order.StartFlow) != "" {
			return "awaiting_payment"
		}
		return "preparing"
	case model.PaymentOrderStatusProcessing:
		return "preparing"
	case model.PaymentOrderStatusPaid:
		return "confirming"
	case model.PaymentOrderStatusFulfilled:
		return "succeeded"
	case model.PaymentOrderStatusExpired:
		return "expired"
	default:
		return "temporarily_unavailable"
	}
}

// paymentOrderPublicStatus is retained for compatibility projections and
// older tests. New user payment APIs expose status_code instead.
func paymentOrderPublicStatus(status string) string {
	switch status {
	case model.PaymentOrderStatusPaid, model.PaymentOrderStatusFulfilled:
		return common.TopUpStatusSuccess
	case model.PaymentOrderStatusRefundPending:
		return model.PaymentOrderStatusRefundPending
	default:
		return status
	}
}

func PaymentEpayNotify(c *gin.Context) {
	processUnifiedPaymentWebhook(c, model.PaymentProviderEpay, "success")
}

func XorPayNotify(c *gin.Context) {
	processUnifiedPaymentWebhook(c, model.PaymentProviderXorPay, "")
}

// processUnifiedPaymentWebhook verifies the provider signature, persists the
// normalized event, then performs any provider authority confirmation and the
// accounting transition before acknowledging it. Acknowledging only after
// commit is essential because providers stop retrying on HTTP 200.
func processUnifiedPaymentWebhook(c *gin.Context, providerName string, successBody string) {
	clusterFailureBody := ""
	if successBody != "" {
		clusterFailureBody = "fail"
	}
	if !ensurePaymentWebhookClusterReady(c, providerName, clusterFailureBody) {
		return
	}
	event, err := service.VerifyPaymentWebhook(providerName, c.Request)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment webhook rejected provider=%s client_ip=%s error=%q", providerName, c.ClientIP(), err.Error()))
		if successBody != "" {
			c.String(http.StatusBadRequest, "fail")
		} else {
			c.Status(http.StatusBadRequest)
		}
		return
	}
	if !validatePersistedPaymentWebhook(c, providerName, successBody, event) {
		return
	}
	result, err := service.ProcessNormalizedPaymentEvent(event)
	if errors.Is(err, model.ErrPaymentOrderNotFound) || errors.Is(err, model.ErrPaymentAmountMismatch) ||
		errors.Is(err, model.ErrPaymentCurrencyMismatch) || errors.Is(err, model.ErrPaymentProviderMismatch) ||
		errors.Is(err, model.ErrPaymentEventInvalid) {
		reason := "verified provider event could not be mapped to a canonical payment order"
		if recordErr := service.RecordUnmatchedPaymentEvent(event, reason+": "+err.Error()); recordErr == nil {
			if !persistPaymentWebhookCompatibilityState(c, providerName, successBody, event) {
				return
			}
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment webhook retained for manual review provider=%s event_type=%s trade_no=%s", providerName, event.EventType, event.TradeNo))
			writePaymentWebhookSuccess(c, successBody)
			return
		} else {
			err = recordErr
		}
	}
	if err != nil {
		if errors.Is(err, model.ErrPaymentManualReview) || errors.Is(err, model.ErrPaymentEventConflict) {
			if !persistPaymentWebhookCompatibilityState(c, providerName, successBody, event) {
				return
			}
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment webhook moved to manual review provider=%s event_type=%s trade_no=%s", providerName, event.EventType, event.TradeNo))
			writePaymentWebhookSuccess(c, successBody)
			return
		}
		status := http.StatusInternalServerError
		if errors.Is(err, model.ErrPaymentOrderNotFound) {
			status = http.StatusServiceUnavailable
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("payment webhook processing failed provider=%s event_type=%s trade_no=%s retryable=true error=%q", providerName, event.EventType, event.TradeNo, err.Error()))
		if successBody != "" {
			c.String(status, "fail")
		} else {
			c.Status(status)
		}
		return
	}
	if !persistPaymentWebhookCompatibilityState(c, providerName, successBody, event) {
		return
	}
	model.LogPaymentSettlement(result, providerName, c.ClientIP())
	writePaymentWebhookSuccess(c, successBody)
}

func ensurePaymentWebhookClusterReady(c *gin.Context, providerName string, failureBody string) bool {
	if err := service.EnsurePaymentClusterReady(); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"payment webhook deferred provider=%s readiness_code=%s",
			providerName, service.PaymentClusterReadinessCode(err),
		))
		if failureBody != "" {
			c.String(http.StatusServiceUnavailable, failureBody)
		} else {
			c.AbortWithStatus(http.StatusServiceUnavailable)
		}
		return false
	}
	return true
}

func validatePersistedPaymentWebhook(c *gin.Context, providerName, successBody string, event *service.NormalizedPaymentEvent) bool {
	if err := service.RecordVerifiedPaymentWebhookReceived(event); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("payment webhook inbox persistence failed provider=%s event_type=%s error=%q", providerName, event.EventType, err.Error()))
		if successBody != "" {
			c.String(http.StatusInternalServerError, "fail")
		} else {
			c.Status(http.StatusInternalServerError)
		}
		return false
	}
	if err := service.ValidateVerifiedPaymentWebhook(c.Request.Context(), providerName, event); err != nil {
		if recordErr := service.MarkVerifiedPaymentWebhookValidationFailed(event, "provider_authority_validation_failed"); recordErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("payment webhook authority failure state update failed provider=%s event_type=%s error=%q", providerName, event.EventType, recordErr.Error()))
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("payment webhook provider authority validation failed provider=%s event_type=%s error=%q", providerName, event.EventType, err.Error()))
		if successBody != "" {
			c.String(http.StatusInternalServerError, "fail")
		} else {
			c.Status(http.StatusInternalServerError)
		}
		return false
	}
	return true
}

func persistPaymentWebhookCompatibilityState(c *gin.Context, providerName, successBody string, event *service.NormalizedPaymentEvent) bool {
	if err := service.ProcessVerifiedPaymentWebhook(c.Request.Context(), providerName, event); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("payment webhook compatibility inventory update failed provider=%s event_type=%s error=%q", providerName, event.EventType, err.Error()))
		if successBody != "" {
			c.String(http.StatusInternalServerError, "fail")
		} else {
			c.Status(http.StatusInternalServerError)
		}
		return false
	}
	return true
}

func writePaymentWebhookSuccess(c *gin.Context, body string) {
	if body == "" {
		c.Status(http.StatusOK)
		return
	}
	c.String(http.StatusOK, body)
}

func paymentAPIError(c *gin.Context, status int, message string) {
	code := "payment_temporarily_unavailable"
	switch status {
	case http.StatusBadRequest:
		code = "payment_request_invalid"
	case http.StatusNotFound:
		code = "payment_order_not_found"
	case http.StatusConflict:
		code = "payment_not_ready"
	}
	paymentAPIErrorWithCode(c, status, code, message, nil)
}

func paymentAPIErrorWithCode(c *gin.Context, status int, code, _ string, params gin.H) {
	// Canonical user payment APIs return stable machine-readable codes only.
	// Default and Classic own their respective localized copy; raw backend or
	// provider messages never cross this boundary.
	payload := gin.H{"success": false, "code": code}
	if len(params) > 0 {
		payload["params"] = params
	}
	c.JSON(status, payload)
}

func ensurePublicPaymentClusterReady(c *gin.Context) bool {
	if err := service.EnsurePaymentClusterReady(); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment request rejected by cluster readiness gate user_id=%d error=%q", c.GetInt("id"), err.Error()))
		paymentServiceAPIError(c, err)
		return false
	}
	return true
}

type publicPaymentError struct {
	status  int
	code    string
	message string
	params  gin.H
}

func classifyPublicPaymentError(err error) publicPaymentError {
	result := publicPaymentError{
		status:  http.StatusServiceUnavailable,
		code:    "payment_temporarily_unavailable",
		message: "Payment is temporarily unavailable",
	}
	if err == nil {
		return result
	}

	switch {
	case errors.Is(err, service.ErrPaymentClusterSQLiteUnsupported),
		errors.Is(err, service.ErrPaymentClusterRedisRequired),
		errors.Is(err, service.ErrPaymentClusterConfigMismatch),
		errors.Is(err, service.ErrPaymentClusterKeyMismatch):
		result.status, result.code, result.message = http.StatusServiceUnavailable, "payment_temporarily_unavailable", "Payment is temporarily unavailable"
	case errors.Is(err, model.ErrPaymentActiveQuoteLimit):
		result.status, result.code, result.message = http.StatusTooManyRequests, "payment_quote_limit_reached", "Too many payment attempts are in progress"
	case errors.Is(err, model.ErrPaymentSingleLimitExceeded):
		result.status, result.code, result.message = http.StatusUnprocessableEntity, "payment_single_limit_exceeded", "Payment amount exceeds the single-payment limit"
	case errors.Is(err, model.ErrPaymentDailyLimitExceeded), errors.Is(err, model.ErrPaymentLimitDayBoundary):
		result.status, result.code, result.message = http.StatusTooManyRequests, "payment_daily_limit_exceeded", "The daily payment limit is currently unavailable"
	case errors.Is(err, model.ErrPaymentQuoteExpired):
		result.status, result.code, result.message = http.StatusGone, "payment_quote_expired", "Payment quote has expired"
	case errors.Is(err, model.ErrPaymentQuoteConsumed):
		result.status, result.code, result.message = http.StatusConflict, "payment_quote_consumed", "Payment quote has already been used"
	case errors.Is(err, model.ErrPaymentQuoteNotFound):
		result.status, result.code, result.message = http.StatusNotFound, "payment_quote_not_found", "Payment quote was not found"
	case errors.Is(err, model.ErrPaymentIdempotencyConflict):
		result.status, result.code, result.message = http.StatusConflict, "payment_request_conflict", "Payment request conflicts with an earlier request"
	case errors.Is(err, model.ErrPaymentInFlightOrderLimit):
		result.status, result.code, result.message = http.StatusTooManyRequests, "payment_order_limit_reached", "Too many payment orders are in progress"
	case errors.Is(err, model.ErrPaymentConfigurationVersionConflict):
		result.status, result.code, result.message = http.StatusConflict, "payment_configuration_changed", "Payment settings changed; please try again"
	case errors.Is(err, model.ErrPaymentManualReview):
		result.status, result.code, result.message = http.StatusConflict, "payment_requires_support", "Payment requires support review"
	case errors.Is(err, model.ErrPaymentUserUnavailable):
		result.status, result.code, result.message = http.StatusForbidden, "payment_account_unavailable", "This account cannot start a payment"
	case errors.Is(err, model.ErrSubscriptionPurchaseLimit):
		result.status, result.code, result.message = http.StatusConflict, "subscription_purchase_limit_reached", "Subscription purchase limit has been reached"
	case errors.Is(err, service.ErrPaymentStateUnknown):
		result.status, result.code, result.message = http.StatusServiceUnavailable, "payment_confirmation_pending", "Payment creation is still being confirmed"
	case strings.Contains(err.Error(), "payment order has expired"):
		result.status, result.code, result.message = http.StatusGone, "payment_order_expired", "Payment order has expired"
	case strings.Contains(err.Error(), "invalid payment request id"),
		strings.Contains(err.Error(), "invalid payment order request"),
		strings.Contains(err.Error(), "invalid payment order kind"),
		strings.Contains(err.Error(), "invalid subscription plan"),
		strings.Contains(err.Error(), "unsupported payment currency"),
		strings.Contains(err.Error(), "unsupported precision"):
		result.status, result.code, result.message = http.StatusBadRequest, "payment_request_invalid", "Invalid payment request"
	case strings.Contains(err.Error(), "top-up amount must be between"),
		strings.Contains(err.Error(), "top-up quota is outside the supported range"):
		result.status, result.code, result.message = http.StatusBadRequest, "payment_amount_invalid", "Payment amount is outside the supported range"
		result.params = gin.H{"min": 1, "max": service.MaxPaymentTopUpAmount}
	case strings.Contains(err.Error(), "top-up amount cannot be less than"),
		strings.Contains(err.Error(), "payment amount is too low"):
		result.status, result.code, result.message = http.StatusUnprocessableEntity, "payment_amount_below_minimum", "Payment amount is below the minimum"
	case strings.Contains(err.Error(), "no longer startable"), strings.Contains(err.Error(), "cannot be continued"):
		result.status, result.code, result.message = http.StatusConflict, "payment_not_ready", "Payment cannot be continued in its current state"
	}
	return result
}

func paymentServiceAPIError(c *gin.Context, err error) {
	result := classifyPublicPaymentError(err)
	paymentAPIErrorWithCode(c, result.status, result.code, result.message, result.params)
}

// legacyPaymentAPIError preserves the old message/data envelope without ever
// returning an upstream or internal error string. Legacy clients can localize
// the stable code while administrator logs retain the diagnostic details.
func legacyPaymentAPIError(c *gin.Context, code string, params gin.H) {
	legacyPaymentAPIErrorStatus(c, http.StatusOK, code, params)
}

func legacyPaymentAPIErrorStatus(c *gin.Context, status int, code string, params gin.H) {
	payload := gin.H{"message": "error", "code": code, "data": code}
	if len(params) > 0 {
		payload["params"] = params
	}
	c.JSON(status, payload)
}

func legacyPaymentServiceAPIError(c *gin.Context, err error) {
	result := classifyPublicPaymentError(err)
	legacyPaymentAPIError(c, result.code, result.params)
}

func compatibilityPaymentAPIError(c *gin.Context, code string, params gin.H) {
	payload := gin.H{"success": false, "message": code, "code": code}
	if len(params) > 0 {
		payload["params"] = params
	}
	c.JSON(http.StatusOK, payload)
}

func compatibilityPaymentServiceAPIError(c *gin.Context, err error) {
	result := classifyPublicPaymentError(err)
	compatibilityPaymentAPIError(c, result.code, result.params)
}

func startLegacyTopUpPayment(c *gin.Context, provider string, paymentMethod string, amount int64, requestID string) (*service.PaymentStart, error) {
	return startLegacyTopUpPaymentWithReturnURLs(c, provider, paymentMethod, amount, requestID, "", "")
}

func startLegacyTopUpPaymentWithReturnURLs(c *gin.Context, provider string, paymentMethod string, amount int64, requestID, successURL, cancelURL string) (*service.PaymentStart, error) {
	return startLegacyPayment(c, service.PaymentQuoteRequest{
		OrderKind:     model.PaymentOrderKindTopUp,
		Provider:      provider,
		PaymentMethod: paymentMethod,
		Amount:        amount,
		SuccessURL:    successURL,
		CancelURL:     cancelURL,
	}, requestID, "legacy_topup_")
}

func startLegacySubscriptionPayment(c *gin.Context, provider, paymentMethod string, planID int, requestID string) (*service.PaymentStart, error) {
	return startLegacyPayment(c, service.PaymentQuoteRequest{
		OrderKind:     model.PaymentOrderKindSubscription,
		Provider:      provider,
		PaymentMethod: paymentMethod,
		PlanID:        planID,
	}, requestID, "legacy_sub_")
}

func startLegacyPayment(c *gin.Context, quoteRequest service.PaymentQuoteRequest, requestedID, fallbackPrefix string) (*service.PaymentStart, error) {
	if err := service.EnsurePaymentClusterReady(); err != nil {
		return nil, err
	}
	requestID, err := legacyPaymentRequestID(requestedID, fallbackPrefix)
	if err != nil {
		return nil, err
	}

	userID := c.GetInt("id")
	if existing, err := model.GetPaymentOrderByRequestID(userID, requestID); err == nil {
		requestedAmount := quoteRequest.Amount
		if quoteRequest.OrderKind == model.PaymentOrderKindSubscription {
			requestedAmount = int64(quoteRequest.PlanID)
		}
		if existing.OrderKind != strings.ToLower(strings.TrimSpace(quoteRequest.OrderKind)) ||
			existing.Provider != strings.ToLower(strings.TrimSpace(quoteRequest.Provider)) ||
			existing.PaymentMethod != service.NormalizePaymentMethod(quoteRequest.Provider, quoteRequest.PaymentMethod) ||
			existing.RequestedAmount != requestedAmount {
			return nil, model.ErrPaymentIdempotencyConflict
		}
		if existing.QuoteID != "" {
			return service.StartPayment(c.Request.Context(), userID, service.PaymentStartRequest{
				QuoteID: existing.QuoteID, RequestID: requestID,
			})
		}
	} else if !errors.Is(err, model.ErrPaymentOrderNotFound) {
		return nil, err
	}

	quote, err := service.CreatePaymentQuote(c.Request.Context(), userID, quoteRequest)
	if err != nil {
		return nil, err
	}
	return service.StartPayment(c.Request.Context(), userID, service.PaymentStartRequest{
		QuoteID:   quote.QuoteID,
		RequestID: requestID,
	})
}

func legacyPaymentPageURL(tradeNo string) string {
	return "/payment/" + url.PathEscape(strings.TrimSpace(tradeNo))
}

func legacyPaymentFormBridgeURL(tradeNo string) string {
	return "/api/user/payment/orders/" + url.PathEscape(strings.TrimSpace(tradeNo)) + "/legacy-continue"
}

func legacyPaymentRequestID(requestedID, fallbackPrefix string) (string, error) {
	requestID := strings.TrimSpace(requestedID)
	if requestID == "" {
		randomID, err := common.GenerateRandomCharsKey(24)
		if err != nil {
			return "", err
		}
		requestID = fallbackPrefix + randomID
	}
	if err := service.ValidatePaymentRequestID(requestID); err != nil {
		return "", err
	}
	return requestID, nil
}
