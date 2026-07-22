package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type stripeLegacySubscriptionView struct {
	ID                      int64    `json:"id"`
	StripeSubscriptionID    string   `json:"stripe_subscription_id"`
	StripeCustomerID        string   `json:"stripe_customer_id"`
	CheckoutSessionID       string   `json:"checkout_session_id,omitempty"`
	TradeNo                 string   `json:"trade_no,omitempty"`
	UserID                  *int     `json:"user_id,omitempty"`
	SubscriptionPlanID      *int     `json:"subscription_plan_id,omitempty"`
	MappingStatus           string   `json:"mapping_status"`
	MappingReason           string   `json:"mapping_reason,omitempty"`
	MappingSource           string   `json:"mapping_source,omitempty"`
	ReviewReason            string   `json:"review_reason,omitempty"`
	PriceIDs                []string `json:"price_ids"`
	ProductID               string   `json:"product_id,omitempty"`
	Quantity                int64    `json:"quantity"`
	Currency                string   `json:"currency,omitempty"`
	Status                  string   `json:"status"`
	CollectionMethod        string   `json:"collection_method,omitempty"`
	CancelAtPeriodEnd       bool     `json:"cancel_at_period_end"`
	CurrentPeriodStart      int64    `json:"current_period_start"`
	CurrentPeriodEnd        int64    `json:"current_period_end"`
	CancelAt                int64    `json:"cancel_at"`
	CanceledAt              int64    `json:"canceled_at"`
	EndedAt                 int64    `json:"ended_at"`
	TrialStart              int64    `json:"trial_start"`
	TrialEnd                int64    `json:"trial_end"`
	LatestInvoiceID         string   `json:"latest_invoice_id,omitempty"`
	LatestInvoiceStatus     string   `json:"latest_invoice_status,omitempty"`
	LatestInvoicePaid       bool     `json:"latest_invoice_paid"`
	LatestInvoiceAmountDue  int64    `json:"latest_invoice_amount_due"`
	LatestInvoiceAmountPaid int64    `json:"latest_invoice_amount_paid"`
	LatestInvoiceCurrency   string   `json:"latest_invoice_currency,omitempty"`
	Livemode                bool     `json:"livemode"`
	StripeCreatedAt         int64    `json:"stripe_created_at"`
	StateObservedAt         int64    `json:"state_observed_at"`
	LastSyncedAt            int64    `json:"last_synced_at"`
	SyncSource              string   `json:"sync_source"`
	ExpectedUpdatedAt       int64    `json:"expected_updated_at"`
}

type stripeLegacySubscriptionPageView struct {
	Page     int                            `json:"page"`
	PageSize int                            `json:"page_size"`
	Total    int                            `json:"total"`
	Items    []stripeLegacySubscriptionView `json:"items"`
}

type stripeLegacySubscriptionCancellationRequest struct {
	InventoryID       int64  `json:"inventory_id"`
	ExpectedUpdatedAt int64  `json:"expected_updated_at"`
	Reason            string `json:"reason"`
}

func newStripeLegacySubscriptionView(subscription *model.StripeLegacySubscription) stripeLegacySubscriptionView {
	priceIDs := subscription.PriceIDs()
	if priceIDs == nil {
		priceIDs = []string{}
	}
	return stripeLegacySubscriptionView{
		ID:                      subscription.ID,
		StripeSubscriptionID:    subscription.StripeSubscriptionID,
		StripeCustomerID:        subscription.StripeCustomerID,
		CheckoutSessionID:       subscription.CheckoutSessionID,
		TradeNo:                 subscription.TradeNo,
		UserID:                  subscription.UserID,
		SubscriptionPlanID:      subscription.SubscriptionPlanID,
		MappingStatus:           subscription.MappingStatus,
		MappingReason:           subscription.MappingReason,
		MappingSource:           subscription.MappingSource,
		ReviewReason:            subscription.ReviewReason,
		PriceIDs:                priceIDs,
		ProductID:               subscription.ProductID,
		Quantity:                subscription.Quantity,
		Currency:                subscription.Currency,
		Status:                  subscription.Status,
		CollectionMethod:        subscription.CollectionMethod,
		CancelAtPeriodEnd:       subscription.CancelAtPeriodEnd,
		CurrentPeriodStart:      subscription.CurrentPeriodStart,
		CurrentPeriodEnd:        subscription.CurrentPeriodEnd,
		CancelAt:                subscription.CancelAt,
		CanceledAt:              subscription.CanceledAt,
		EndedAt:                 subscription.EndedAt,
		TrialStart:              subscription.TrialStart,
		TrialEnd:                subscription.TrialEnd,
		LatestInvoiceID:         subscription.LatestInvoiceID,
		LatestInvoiceStatus:     subscription.LatestInvoiceStatus,
		LatestInvoicePaid:       subscription.LatestInvoicePaid,
		LatestInvoiceAmountDue:  subscription.LatestInvoiceAmountDue,
		LatestInvoiceAmountPaid: subscription.LatestInvoiceAmountPaid,
		LatestInvoiceCurrency:   subscription.LatestInvoiceCurrency,
		Livemode:                subscription.Livemode,
		StripeCreatedAt:         subscription.StripeCreatedAt,
		StateObservedAt:         subscription.StateObservedAt,
		LastSyncedAt:            subscription.LastSyncedAt,
		SyncSource:              subscription.SyncSource,
		ExpectedUpdatedAt:       subscription.UpdatedAt,
	}
}

func AdminListStripeLegacySubscriptionInventory(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	if pageInfo.Page < 1 {
		pageInfo.Page = 1
	}
	if pageInfo.PageSize < 1 {
		pageInfo.PageSize = common.ItemsPerPage
	}
	filter := model.StripeLegacySubscriptionFilter{
		Status:         strings.TrimSpace(c.Query("status")),
		MappingStatus:  strings.TrimSpace(c.Query("mapping_status")),
		ReviewReason:   strings.TrimSpace(c.Query("review_reason")),
		CustomerID:     strings.TrimSpace(c.Query("customer_id")),
		SubscriptionID: strings.TrimSpace(c.Query("subscription_id")),
	}
	if len(filter.Status) > 32 || len(filter.MappingStatus) > 32 || len(filter.ReviewReason) > 255 ||
		len(filter.CustomerID) > 128 || len(filter.SubscriptionID) > 128 {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "stripe_inventory_filter_invalid", "", nil)
		return
	}
	if rawUserID := strings.TrimSpace(c.Query("user_id")); rawUserID != "" {
		userID, err := strconv.Atoi(rawUserID)
		if err != nil || userID <= 0 {
			paymentAPIErrorWithCode(c, http.StatusBadRequest, "stripe_inventory_filter_invalid", "", nil)
			return
		}
		filter.UserID = userID
	}
	subscriptions, total, err := model.ListStripeLegacySubscriptions(filter, pageInfo)
	if err != nil {
		if errors.Is(err, model.ErrStripeLegacyInventoryFilterInvalid) {
			paymentAPIErrorWithCode(c, http.StatusBadRequest, "stripe_inventory_filter_invalid", "", nil)
		} else if errors.Is(err, model.ErrStripeLegacyInventorySchemaNotReady) {
			paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "stripe_inventory_schema_not_ready", "", nil)
		} else {
			logger.LogWarn(c.Request.Context(), "failed to load Stripe legacy inventory: "+err.Error())
			paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "stripe_inventory_unavailable", "", nil)
		}
		return
	}
	views := make([]stripeLegacySubscriptionView, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		views = append(views, newStripeLegacySubscriptionView(subscription))
	}
	common.ApiSuccess(c, stripeLegacySubscriptionPageView{
		Page: pageInfo.Page, PageSize: pageInfo.PageSize,
		Total: int(total), Items: views,
	})
}

func AdminSyncStripeLegacySubscriptionInventory(c *gin.Context) {
	result, err := service.SyncStripeLegacySubscriptions(c.Request.Context())
	if err != nil {
		logger.LogWarn(c.Request.Context(), "failed to sync Stripe legacy inventory: "+err.Error())
		switch {
		case errors.Is(err, model.ErrStripeLegacyInventorySchemaNotReady):
			paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "stripe_inventory_schema_not_ready", "", nil)
		case errors.Is(err, service.ErrStripeLegacySyncNotConfigured):
			paymentAPIErrorWithCode(c, http.StatusConflict, "stripe_inventory_sync_not_configured", "", nil)
		case errors.Is(err, service.ErrStripeLegacySyncModeMismatch):
			paymentAPIErrorWithCode(c, http.StatusConflict, "stripe_inventory_sync_mode_mismatch", "", nil)
		default:
			paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "stripe_inventory_sync_unavailable", "", nil)
		}
		return
	}
	common.ApiSuccess(c, result)
}

func AdminCancelStripeLegacySubscriptionAtPeriodEnd(c *gin.Context) {
	if c.GetBool("use_access_token") {
		paymentAPIErrorWithCode(c, http.StatusForbidden, "payment_operations_auth_required", "", nil)
		return
	}
	inventoryID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || inventoryID <= 0 {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "stripe_inventory_cancel_invalid", "", nil)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, paymentRequestBodyLimit)
	var request stripeLegacySubscriptionCancellationRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.InventoryID != inventoryID {
		paymentAPIErrorWithCode(c, http.StatusBadRequest, "stripe_inventory_cancel_invalid", "", nil)
		return
	}
	result, err := service.CancelStripeLegacySubscriptionAtPeriodEnd(c.Request.Context(), service.StripeLegacySubscriptionCancellationInput{
		InventoryID: inventoryID, ExpectedUpdatedAt: request.ExpectedUpdatedAt,
		AdminID: c.GetInt("id"), ActorIP: c.ClientIP(), Reason: request.Reason,
	})
	if err != nil {
		logger.LogWarn(c.Request.Context(), "failed to schedule Stripe legacy subscription cancellation: "+err.Error())
		switch {
		case errors.Is(err, model.ErrStripeLegacySubscriptionCancelInvalid):
			paymentAPIErrorWithCode(c, http.StatusBadRequest, "stripe_inventory_cancel_invalid", "", nil)
		case errors.Is(err, model.ErrStripeLegacySubscriptionNotFound):
			paymentAPIErrorWithCode(c, http.StatusNotFound, "stripe_inventory_subscription_not_found", "", nil)
		case errors.Is(err, model.ErrStripeLegacySubscriptionCancelConflict):
			paymentAPIErrorWithCode(c, http.StatusConflict, "stripe_inventory_cancel_conflict", "", nil)
		case errors.Is(err, model.ErrStripeLegacyInventorySchemaNotReady):
			paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "stripe_inventory_schema_not_ready", "", nil)
		case errors.Is(err, service.ErrStripeLegacyCancellationNotConfigured):
			paymentAPIErrorWithCode(c, http.StatusConflict, "stripe_inventory_cancel_not_configured", "", nil)
		case errors.Is(err, service.ErrStripeLegacyCancellationIdentityMismatch):
			paymentAPIErrorWithCode(c, http.StatusConflict, "stripe_inventory_cancel_account_mismatch", "", nil)
		case errors.Is(err, service.ErrStripeLegacyCancellationModeMismatch):
			paymentAPIErrorWithCode(c, http.StatusConflict, "stripe_inventory_cancel_mode_mismatch", "", nil)
		default:
			paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "stripe_inventory_cancel_unavailable", "", nil)
		}
		return
	}
	if result == nil || result.Subscription == nil {
		paymentAPIErrorWithCode(c, http.StatusServiceUnavailable, "stripe_inventory_cancel_unavailable", "", nil)
		return
	}
	if !result.Duplicate {
		recordManageAudit(c, model.PaymentOperationsActionStripeLegacySubscriptionCancel, map[string]interface{}{
			"inventory_id": inventoryID, "stripe_subscription_id": result.Subscription.StripeSubscriptionID,
			"expected_updated_at": request.ExpectedUpdatedAt, "reason": strings.TrimSpace(request.Reason),
		})
	} else {
		markAuditLogged(c)
	}
	common.ApiSuccess(c, gin.H{
		"subscription": newStripeLegacySubscriptionView(result.Subscription),
		"duplicate":    result.Duplicate,
	})
}
