package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
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
	}
}

func AdminListStripeLegacySubscriptionInventory(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	filter := model.StripeLegacySubscriptionFilter{
		Status:         strings.TrimSpace(c.Query("status")),
		MappingStatus:  strings.TrimSpace(c.Query("mapping_status")),
		ReviewReason:   strings.TrimSpace(c.Query("review_reason")),
		CustomerID:     strings.TrimSpace(c.Query("customer_id")),
		SubscriptionID: strings.TrimSpace(c.Query("subscription_id")),
	}
	if rawUserID := strings.TrimSpace(c.Query("user_id")); rawUserID != "" {
		userID, err := strconv.Atoi(rawUserID)
		if err != nil || userID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid user ID"})
			return
		}
		filter.UserID = userID
	}
	subscriptions, total, err := model.ListStripeLegacySubscriptions(filter, pageInfo)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	views := make([]stripeLegacySubscriptionView, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		views = append(views, newStripeLegacySubscriptionView(subscription))
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(views)
	common.ApiSuccess(c, pageInfo)
}

func AdminSyncStripeLegacySubscriptionInventory(c *gin.Context) {
	result, err := service.SyncStripeLegacySubscriptions(c.Request.Context())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}
