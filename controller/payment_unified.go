package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type paymentOrderView struct {
	TradeNo             string `json:"trade_no"`
	OrderKind           string `json:"order_kind"`
	Provider            string `json:"provider"`
	PaymentMethod       string `json:"payment_method"`
	Status              string `json:"status"`
	RequestedAmount     int64  `json:"requested_amount"`
	CreditQuota         int64  `json:"credit_quota"`
	ExpectedAmountMinor int64  `json:"expected_amount_minor"`
	PaidAmountMinor     int64  `json:"paid_amount_minor"`
	Currency            string `json:"currency"`
	ExpiresAt           int64  `json:"expires_at"`
	SettledAt           int64  `json:"settled_at"`
	StatusReason        string `json:"status_reason,omitempty"`
}

const paymentRequestBodyLimit = 64 << 10

func CreatePaymentQuote(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request service.PaymentQuoteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		paymentAPIError(c, http.StatusBadRequest, "Invalid payment request")
		return
	}
	quote, err := service.CreatePaymentQuote(c.Request.Context(), c.GetInt("id"), request)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, model.ErrPaymentActiveQuoteLimit) {
			status = http.StatusTooManyRequests
		}
		paymentAPIError(c, status, err.Error())
		return
	}
	common.ApiSuccess(c, quote)
}

func StartPayment(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request service.PaymentStartRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		paymentAPIError(c, http.StatusBadRequest, "Invalid payment request")
		return
	}
	start, err := service.StartPayment(c.Request.Context(), c.GetInt("id"), request)
	if err != nil {
		if errors.Is(err, service.ErrPaymentStateUnknown) && start != nil && start.TradeNo != "" {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment start is awaiting provider confirmation user_id=%d trade_no=%s error=%q", c.GetInt("id"), start.TradeNo, err.Error()))
			c.JSON(http.StatusAccepted, gin.H{
				"success": true,
				"message": "Payment creation is being verified",
				"data":    start,
			})
			return
		}
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment start failed user_id=%d state_unknown=%t error=%q", c.GetInt("id"), errors.Is(err, service.ErrPaymentStateUnknown), err.Error()))
		status := http.StatusBadRequest
		if errors.Is(err, model.ErrPaymentInFlightOrderLimit) {
			status = http.StatusTooManyRequests
		}
		paymentAPIError(c, status, err.Error())
		return
	}
	common.ApiSuccess(c, start)
}

func GetPaymentOrder(c *gin.Context) {
	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	if tradeNo == "" || len(tradeNo) > 128 {
		paymentAPIError(c, http.StatusBadRequest, "Invalid payment order")
		return
	}
	order, err := service.RefreshPaymentOrder(c.Request.Context(), c.GetInt("id"), tradeNo)
	if err != nil {
		if errors.Is(err, model.ErrPaymentOrderNotFound) {
			paymentAPIError(c, http.StatusNotFound, "Payment order not found")
			return
		}
		paymentAPIError(c, http.StatusInternalServerError, "Failed to load payment order")
		return
	}
	common.ApiSuccess(c, paymentOrderView{
		TradeNo:             order.TradeNo,
		OrderKind:           order.OrderKind,
		Provider:            order.Provider,
		PaymentMethod:       order.PaymentMethod,
		Status:              paymentOrderPublicStatus(order.Status),
		RequestedAmount:     order.RequestedAmount,
		CreditQuota:         order.CreditQuota,
		ExpectedAmountMinor: order.ExpectedAmountMinor,
		PaidAmountMinor:     order.PaidAmountMinor,
		Currency:            order.Currency,
		ExpiresAt:           order.ExpiresAt,
		SettledAt:           order.SettledAt,
		StatusReason:        paymentOrderPublicReason(order.Status),
	})
}

func paymentOrderPublicReason(status string) string {
	switch status {
	case model.PaymentOrderStatusManualReview:
		return "Payment requires manual review"
	case model.PaymentOrderStatusFailed:
		return "Payment failed"
	case model.PaymentOrderStatusExpired:
		return "Payment expired"
	case model.PaymentOrderStatusRefundPending:
		return "Refund is being processed"
	case model.PaymentOrderStatusRefunded:
		return "Payment was refunded"
	case model.PaymentOrderStatusDisputed, model.PaymentOrderStatusDebt:
		return "Payment requires support review"
	default:
		return ""
	}
}

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

// processUnifiedPaymentWebhook verifies first, then persists the normalized
// event and accounting transition before acknowledging it. Acknowledging only
// after commit is essential because providers stop retrying on HTTP 200.
func processUnifiedPaymentWebhook(c *gin.Context, providerName string, successBody string) {
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
	if err := service.ProcessVerifiedPaymentWebhook(c.Request.Context(), providerName, event); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("payment webhook compatibility inventory update failed provider=%s event_type=%s error=%q", providerName, event.EventType, err.Error()))
		if successBody != "" {
			c.String(http.StatusInternalServerError, "fail")
		} else {
			c.Status(http.StatusInternalServerError)
		}
		return
	}
	result, err := service.ProcessNormalizedPaymentEvent(event)
	if errors.Is(err, model.ErrPaymentOrderNotFound) || errors.Is(err, model.ErrPaymentAmountMismatch) ||
		errors.Is(err, model.ErrPaymentCurrencyMismatch) || errors.Is(err, model.ErrPaymentProviderMismatch) ||
		errors.Is(err, model.ErrPaymentEventInvalid) {
		reason := "verified provider event could not be mapped to a canonical payment order"
		if recordErr := service.RecordUnmatchedPaymentEvent(event, reason+": "+err.Error()); recordErr == nil {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment webhook retained for manual review provider=%s event_type=%s trade_no=%s", providerName, event.EventType, event.TradeNo))
			writePaymentWebhookSuccess(c, successBody)
			return
		} else {
			err = recordErr
		}
	}
	if err != nil {
		if errors.Is(err, model.ErrPaymentManualReview) || errors.Is(err, model.ErrPaymentEventConflict) {
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
	model.LogPaymentSettlement(result, providerName, c.ClientIP())
	writePaymentWebhookSuccess(c, successBody)
}

func writePaymentWebhookSuccess(c *gin.Context, body string) {
	if body == "" {
		c.Status(http.StatusOK)
		return
	}
	c.String(http.StatusOK, body)
}

func paymentAPIError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"success": false, "message": message})
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
