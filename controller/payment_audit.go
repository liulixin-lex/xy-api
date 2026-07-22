package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func ListPaymentAudit(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	statuses := []string{
		model.PaymentOrderStatusManualReview, model.PaymentOrderStatusDebt,
		model.PaymentOrderStatusDisputed, model.PaymentOrderStatusRefundPending,
	}
	includeCredentialIncidents := true
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		statuses = []string{status}
		includeCredentialIncidents = false
	}
	orders, total, err := model.ListPaymentAuditOrders(statuses, includeCredentialIncidents, c.Query("provider"), c.Query("trade_no"), pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	unmatchedOffset, unmatchedPage, unmatchedPageSize := paymentAuditUnmatchedPagination(c)
	unmatched, unmatchedTotal, err := model.ListUnmatchedPaymentEventViewsPage(unmatchedOffset, unmatchedPageSize)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"orders": orders, "total": total,
		"unmatched_events": unmatched, "unmatched_total": unmatchedTotal,
		"unmatched_page": unmatchedPage, "unmatched_page_size": unmatchedPageSize,
	})
}

func paymentAuditUnmatchedPagination(c *gin.Context) (offset, page, pageSize int) {
	page = 1
	pageSize = 50
	if parsed, err := strconv.Atoi(strings.TrimSpace(c.Query("unmatched_page"))); err == nil && parsed > 0 {
		page = parsed
	}
	if parsed, err := strconv.Atoi(strings.TrimSpace(c.Query("unmatched_page_size"))); err == nil && parsed > 0 {
		pageSize = parsed
	}
	if pageSize > 100 {
		pageSize = 100
	}
	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/pageSize {
		return maxInt, page, pageSize
	}
	return (page - 1) * pageSize, page, pageSize
}

func GetPaymentAudit(c *gin.Context) {
	detail, err := model.GetPaymentAuditDetail(c.Param("trade_no"))
	if err != nil {
		if errors.Is(err, model.ErrPaymentOrderNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Payment order not found"})
			return
		}
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, detail)
}

type paymentDebtResolutionRequest struct {
	DebtID                    int64  `json:"debt_id"`
	ExpectedOutstandingQuota  int64  `json:"expected_outstanding_quota"`
	ExpectedOutstandingAmount int64  `json:"expected_outstanding_amount_minor"`
	Resolution                string `json:"resolution"`
	Note                      string `json:"note"`
}

type manualPaymentResolutionRequest struct {
	TradeNo                 string `json:"trade_no"`
	ExpectedVersion         int64  `json:"expected_version"`
	Reason                  string `json:"reason"`
	RefundedAmountMinor     int64  `json:"refunded_amount_minor"`
	ProviderRefundReference string `json:"provider_refund_reference"`
}

func ResolveManualPaymentOrder(c *gin.Context) {
	resolvePaymentOrderAuditAction(c, model.PaymentAdminActionFulfill)
}

func RejectManualPaymentOrder(c *gin.Context) {
	resolvePaymentOrderAuditAction(c, model.PaymentAdminActionReject)
}

func VoidManualPaymentOrder(c *gin.Context) {
	resolvePaymentOrderAuditAction(c, model.PaymentAdminActionVoid)
}

func ConfirmExternalPaymentRefund(c *gin.Context) {
	resolvePaymentOrderAuditAction(c, model.PaymentAdminActionExternalRefundConfirmed)
}

func AcknowledgePaymentCredentialIncident(c *gin.Context) {
	reviewPaymentCredentialIncident(c, model.PaymentCredentialIncidentActionAcknowledge)
}

func ResolvePaymentCredentialIncident(c *gin.Context) {
	reviewPaymentCredentialIncident(c, model.PaymentCredentialIncidentActionResolve)
}

func reviewPaymentCredentialIncident(c *gin.Context, action string) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Payment credential incident actions require dashboard session authentication"})
		return
	}
	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request manualPaymentResolutionRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.TradeNo != tradeNo {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid payment credential incident action"})
		return
	}
	result, err := model.ReviewPaymentCredentialIncidentByAdmin(model.PaymentCredentialIncidentActionInput{
		TradeNo: tradeNo, ExpectedVersion: request.ExpectedVersion, AdminID: c.GetInt("id"),
		ActorIP: c.ClientIP(), Action: action, Reason: request.Reason,
	})
	if err != nil {
		writePaymentAuditActionError(c, err)
		return
	}
	if !result.Duplicate {
		recordManageAudit(c, "payment.order.credential_incident_"+action, map[string]interface{}{
			"trade_no": tradeNo, "expected_version": request.ExpectedVersion,
			"reason": strings.TrimSpace(request.Reason),
		})
	} else {
		markAuditLogged(c)
	}
	common.ApiSuccess(c, result.Order)
}

type retireStripeCustomerBindingRequest struct {
	BindingID       int64  `json:"binding_id"`
	UserID          int    `json:"user_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}

func ListStripeCustomerBindings(c *gin.Context) {
	userID, err := strconv.Atoi(strings.TrimSpace(c.Query("user_id")))
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid payment customer binding user"})
		return
	}
	active, retired, err := model.ListStripeCustomerBindingsForAdmin(userID)
	if err != nil {
		writePaymentAuditActionError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"active": active, "retired": retired})
}

func RetireStripeCustomerBinding(c *gin.Context) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Stripe customer binding retirement requires dashboard session authentication"})
		return
	}
	bindingID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || bindingID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid Stripe customer binding"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request retireStripeCustomerBindingRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.BindingID != bindingID {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid Stripe customer binding retirement"})
		return
	}
	result, err := model.RetireStripeCustomerBindingByAdmin(model.RetireStripeCustomerBindingInput{
		BindingID: bindingID, UserID: request.UserID, ExpectedVersion: request.ExpectedVersion,
		AdminID: c.GetInt("id"), ActorIP: c.ClientIP(), Reason: request.Reason,
	})
	if err != nil {
		writePaymentAuditActionError(c, err)
		return
	}
	if !result.Duplicate {
		recordManageAudit(c, "payment.stripe_customer_binding.retire", map[string]interface{}{
			"binding_id": bindingID, "user_id": request.UserID,
			"expected_version": request.ExpectedVersion, "reason": strings.TrimSpace(request.Reason),
		})
	} else {
		markAuditLogged(c)
	}
	common.ApiSuccess(c, result)
}

func resolvePaymentOrderAuditAction(c *gin.Context, action string) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Payment audit actions require dashboard session authentication"})
		return
	}
	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request manualPaymentResolutionRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.TradeNo != tradeNo {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid payment audit action"})
		return
	}
	result, err := model.ResolvePaymentOrderByAdmin(model.PaymentAdminOrderActionInput{
		TradeNo: tradeNo, ExpectedVersion: request.ExpectedVersion, AdminID: c.GetInt("id"),
		ActorIP: c.ClientIP(), Action: action, Reason: request.Reason, RefundedAmountMinor: request.RefundedAmountMinor,
		ProviderRefundReference: request.ProviderRefundReference,
	})
	if err != nil {
		writePaymentAuditActionError(c, err)
		return
	}
	auditAction := "payment.order.manual_" + action
	if action == model.PaymentAdminActionExternalRefundConfirmed {
		auditAction = "payment.order.external_refund_confirmed"
	}
	if !result.Duplicate {
		recordManageAudit(c, auditAction, map[string]interface{}{
			"trade_no": tradeNo, "expected_version": request.ExpectedVersion,
			"reason": strings.TrimSpace(request.Reason), "refunded_amount_minor": request.RefundedAmountMinor,
			"provider_refund_reference": strings.TrimSpace(request.ProviderRefundReference),
		})
	} else {
		markAuditLogged(c)
	}
	common.ApiSuccess(c, result.Order)
}

type unmatchedPaymentEventActionRequest struct {
	EventID               int64  `json:"event_id"`
	ExpectedEventAttempts int    `json:"expected_event_attempts"`
	TargetTradeNo         string `json:"target_trade_no"`
	ExpectedOrderVersion  int64  `json:"expected_order_version"`
	Reason                string `json:"reason"`
}

func DismissUnmatchedPaymentEvent(c *gin.Context) {
	resolveUnmatchedPaymentEventAuditAction(c, model.PaymentUnmatchedEventActionDismiss)
}

func LinkUnmatchedPaymentEvent(c *gin.Context) {
	resolveUnmatchedPaymentEventAuditAction(c, model.PaymentUnmatchedEventActionLink)
}

func RetryLegacyUnmatchedPaymentEvent(c *gin.Context) {
	resolveUnmatchedPaymentEventAuditAction(c, model.PaymentUnmatchedEventActionRetryLegacy)
}

type legacyTopUpResolutionRequest struct {
	EventID                 int64  `json:"event_id"`
	ExpectedEventAttempts   int    `json:"expected_event_attempts"`
	Resolution              string `json:"resolution"`
	CreditQuota             int64  `json:"credit_quota"`
	ProviderRefundReference string `json:"provider_refund_reference"`
	Reason                  string `json:"reason"`
}

func ResolveLegacyTopUpPaymentEvent(c *gin.Context) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Legacy top-up resolution requires dashboard session authentication"})
		return
	}
	eventID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || eventID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid legacy top-up payment event"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request legacyTopUpResolutionRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.EventID != eventID {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid legacy top-up resolution"})
		return
	}
	result, err := model.ResolveLegacyTopUpPaymentEventByAdmin(model.PaymentLegacyTopUpResolutionInput{
		EventID: eventID, ExpectedEventAttempts: request.ExpectedEventAttempts,
		AdminID: c.GetInt("id"), ActorIP: c.ClientIP(), Resolution: request.Resolution,
		CreditQuota: request.CreditQuota, ProviderRefundReference: request.ProviderRefundReference, Reason: request.Reason,
	})
	if err != nil {
		writePaymentAuditActionError(c, err)
		return
	}
	if !result.Duplicate {
		recordManageAudit(c, "payment.event.legacy_topup."+strings.ToLower(strings.TrimSpace(request.Resolution)), map[string]interface{}{
			"event_id": eventID, "expected_event_attempts": request.ExpectedEventAttempts,
			"resolution": strings.ToLower(strings.TrimSpace(request.Resolution)), "credit_quota": request.CreditQuota,
			"provider_refund_reference": strings.TrimSpace(request.ProviderRefundReference),
			"reason":                    strings.TrimSpace(request.Reason),
		})
	} else {
		markAuditLogged(c)
	}
	common.ApiSuccess(c, result)
}

type legacySubscriptionResolutionRequest struct {
	EventID                 int64  `json:"event_id"`
	ExpectedEventAttempts   int    `json:"expected_event_attempts"`
	Resolution              string `json:"resolution"`
	ProviderRefundReference string `json:"provider_refund_reference"`
	Reason                  string `json:"reason"`
}

func ResolveLegacySubscriptionPaymentEvent(c *gin.Context) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Legacy subscription resolution requires dashboard session authentication"})
		return
	}
	eventID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || eventID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid legacy subscription payment event"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request legacySubscriptionResolutionRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.EventID != eventID {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid legacy subscription resolution"})
		return
	}
	result, err := model.ResolveLegacySubscriptionPaymentEventByAdmin(model.PaymentLegacySubscriptionResolutionInput{
		EventID: eventID, ExpectedEventAttempts: request.ExpectedEventAttempts,
		AdminID: c.GetInt("id"), ActorIP: c.ClientIP(), Resolution: request.Resolution,
		ProviderRefundReference: request.ProviderRefundReference, Reason: request.Reason,
	})
	if err != nil {
		writePaymentAuditActionError(c, err)
		return
	}
	if !result.Duplicate {
		recordManageAudit(c, "payment.event.legacy_subscription.external_refund", map[string]interface{}{
			"event_id": eventID, "expected_event_attempts": request.ExpectedEventAttempts,
			"resolution":                strings.ToLower(strings.TrimSpace(request.Resolution)),
			"provider_refund_reference": strings.TrimSpace(request.ProviderRefundReference),
			"reason":                    strings.TrimSpace(request.Reason),
		})
	} else {
		markAuditLogged(c)
	}
	common.ApiSuccess(c, result)
}

func resolveUnmatchedPaymentEventAuditAction(c *gin.Context, action string) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Payment audit actions require dashboard session authentication"})
		return
	}
	eventID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || eventID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid unmatched payment event"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request unmatchedPaymentEventActionRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.EventID != eventID {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid unmatched payment event action"})
		return
	}
	result, err := model.ResolveUnmatchedPaymentEventByAdmin(model.PaymentUnmatchedEventActionInput{
		EventID: eventID, ExpectedEventAttempts: request.ExpectedEventAttempts,
		AdminID: c.GetInt("id"), ActorIP: c.ClientIP(), Action: action, Reason: request.Reason,
		TargetTradeNo: request.TargetTradeNo, ExpectedOrderVersion: request.ExpectedOrderVersion,
	})
	if err != nil {
		writePaymentAuditActionError(c, err)
		return
	}
	auditAction := "payment.event.unmatched_" + action
	if !result.Duplicate {
		params := map[string]interface{}{
			"event_id": eventID, "reason": strings.TrimSpace(request.Reason),
		}
		if action == model.PaymentUnmatchedEventActionRetryLegacy {
			params["expected_event_attempts"] = request.ExpectedEventAttempts
		}
		if result.Order != nil {
			params["target_trade_no"] = result.Order.TradeNo
			params["expected_order_version"] = request.ExpectedOrderVersion
			params["target_user_id"] = result.Order.UserID
		}
		recordManageAudit(c, auditAction, params)
	} else {
		markAuditLogged(c)
	}
	common.ApiSuccess(c, result)
}

func writePaymentAuditActionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrPaymentAuditInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
	case errors.Is(err, model.ErrPaymentAuditNotFound), errors.Is(err, model.ErrPaymentOrderNotFound):
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": err.Error()})
	case errors.Is(err, model.ErrPaymentAuditConflict), errors.Is(err, model.ErrPaymentManualReview),
		errors.Is(err, model.ErrPaymentAmountMismatch), errors.Is(err, model.ErrPaymentCurrencyMismatch),
		errors.Is(err, model.ErrPaymentProviderMismatch):
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to apply payment audit action"})
	}
}

func ResolvePaymentDebt(c *gin.Context) {
	if c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Payment debt resolution requires dashboard session authentication"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	debtID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || debtID <= 0 {
		common.ApiErrorMsg(c, "Invalid payment debt")
		return
	}
	var request paymentDebtResolutionRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.DebtID != debtID {
		common.ApiErrorMsg(c, "Invalid payment debt resolution")
		return
	}
	result, err := model.ResolvePaymentDebtByAdmin(model.PaymentDebtResolutionInput{
		DebtID: debtID, AdminID: c.GetInt("id"), ActorIP: c.ClientIP(),
		ExpectedOutstandingQuota:  request.ExpectedOutstandingQuota,
		ExpectedOutstandingAmount: request.ExpectedOutstandingAmount,
		Resolution:                request.Resolution, Note: request.Note,
	})
	if err != nil {
		writePaymentAuditActionError(c, err)
		return
	}
	if !result.Duplicate {
		recordManageAudit(c, "payment.debt.resolve", map[string]interface{}{
			"debt_id": debtID, "resolution": request.Resolution, "user_id": result.Debt.UserID,
		})
	} else {
		markAuditLogged(c)
	}
	common.ApiSuccess(c, result.Debt)
}
