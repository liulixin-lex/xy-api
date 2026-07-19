package model

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	StripeLegacyMappingMapped        = "mapped"
	StripeLegacyMappingUnmapped      = "unmapped"
	StripeLegacyMappingUnmappedUser  = "unmapped_user"
	StripeLegacyMappingUnmappedPlan  = "unmapped_plan"
	StripeLegacyMappingAmbiguousUser = "ambiguous_user"
	StripeLegacyMappingAmbiguousPlan = "ambiguous_plan"

	StripeLegacySyncSourceWebhook  = "webhook"
	StripeLegacySyncSourceAPI      = "api_sync"
	StripeLegacySyncSourceCheckout = "checkout"
	maxStripeLegacyPriceIDs        = 100
)

// StripeLegacySubscription is a read-only local inventory of recurring Stripe
// subscriptions. It is deliberately separate from UserSubscription: observing
// a Stripe lifecycle event must never create, extend, cancel, or otherwise
// mutate local entitlements.
type StripeLegacySubscription struct {
	ID                      int64  `json:"id" gorm:"primaryKey"`
	StripeSubscriptionID    string `json:"stripe_subscription_id" gorm:"type:varchar(128);uniqueIndex"`
	StripeCustomerID        string `json:"stripe_customer_id" gorm:"type:varchar(128);index"`
	CheckoutSessionID       string `json:"checkout_session_id,omitempty" gorm:"type:varchar(128);index"`
	TradeNo                 string `json:"trade_no,omitempty" gorm:"type:varchar(128);index"`
	UserID                  *int   `json:"user_id,omitempty" gorm:"index"`
	SubscriptionPlanID      *int   `json:"subscription_plan_id,omitempty" gorm:"index"`
	MappingStatus           string `json:"mapping_status" gorm:"type:varchar(32);index"`
	MappingReason           string `json:"mapping_reason,omitempty" gorm:"type:varchar(255)"`
	MappingSource           string `json:"mapping_source,omitempty" gorm:"type:varchar(32)"`
	ReviewReason            string `json:"review_reason,omitempty" gorm:"type:varchar(255);index"`
	PrimaryPriceID          string `json:"primary_price_id,omitempty" gorm:"type:varchar(128);index"`
	PriceIDsJSON            string `json:"-" gorm:"type:text"`
	ProductID               string `json:"product_id,omitempty" gorm:"type:varchar(128);index"`
	Quantity                int64  `json:"quantity"`
	Currency                string `json:"currency,omitempty" gorm:"type:varchar(8)"`
	Status                  string `json:"status" gorm:"type:varchar(32);index"`
	CollectionMethod        string `json:"collection_method,omitempty" gorm:"type:varchar(32)"`
	CancelAtPeriodEnd       bool   `json:"cancel_at_period_end"`
	CurrentPeriodStart      int64  `json:"current_period_start"`
	CurrentPeriodEnd        int64  `json:"current_period_end" gorm:"index"`
	CancelAt                int64  `json:"cancel_at"`
	CanceledAt              int64  `json:"canceled_at"`
	EndedAt                 int64  `json:"ended_at"`
	TrialStart              int64  `json:"trial_start"`
	TrialEnd                int64  `json:"trial_end"`
	LatestInvoiceID         string `json:"latest_invoice_id,omitempty" gorm:"type:varchar(128);index"`
	LatestInvoiceStatus     string `json:"latest_invoice_status,omitempty" gorm:"type:varchar(32)"`
	LatestInvoicePaid       bool   `json:"latest_invoice_paid"`
	LatestInvoiceAmountDue  int64  `json:"latest_invoice_amount_due"`
	LatestInvoiceAmountPaid int64  `json:"latest_invoice_amount_paid"`
	LatestInvoiceCurrency   string `json:"latest_invoice_currency,omitempty" gorm:"type:varchar(8)"`
	LatestInvoiceObservedAt int64  `json:"-"`
	Livemode                bool   `json:"livemode" gorm:"index"`
	StripeCreatedAt         int64  `json:"stripe_created_at"`
	LastStripeEventID       string `json:"last_stripe_event_id,omitempty" gorm:"type:varchar(255)"`
	LastStripeEventType     string `json:"last_stripe_event_type,omitempty" gorm:"type:varchar(128)"`
	LastStripePayloadDigest string `json:"-" gorm:"type:varchar(64)"`
	StateObservedAt         int64  `json:"state_observed_at" gorm:"index"`
	LastSyncedAt            int64  `json:"last_synced_at"`
	SyncSource              string `json:"sync_source" gorm:"type:varchar(32)"`
	CreatedAt               int64  `json:"created_at"`
	UpdatedAt               int64  `json:"updated_at"`
}

func (s *StripeLegacySubscription) BeforeCreate(_ *gorm.DB) error {
	now := time.Now().Unix()
	if s.CreatedAt == 0 {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	return nil
}

func (s *StripeLegacySubscription) BeforeUpdate(_ *gorm.DB) error {
	s.UpdatedAt = time.Now().Unix()
	return nil
}

// PriceIDs returns a defensive copy of the recurring price identifiers stored
// for the subscription. Malformed historical JSON is treated as unavailable.
func (s *StripeLegacySubscription) PriceIDs() []string {
	if s == nil || strings.TrimSpace(s.PriceIDsJSON) == "" {
		return nil
	}
	var ids []string
	if err := common.UnmarshalJsonStr(s.PriceIDsJSON, &ids); err != nil {
		return nil
	}
	return append([]string(nil), ids...)
}

// StripeLegacyInvoice is an audit inventory for invoice lifecycle events. It
// records facts from Stripe but is never used as an entitlement source.
type StripeLegacyInvoice struct {
	ID                         int64  `json:"id" gorm:"primaryKey"`
	StripeInvoiceID            string `json:"stripe_invoice_id" gorm:"type:varchar(128);uniqueIndex"`
	StripeSubscriptionID       string `json:"stripe_subscription_id,omitempty" gorm:"type:varchar(128);index"`
	StripeLegacySubscriptionID *int64 `json:"stripe_legacy_subscription_id,omitempty" gorm:"index"`
	StripeCustomerID           string `json:"stripe_customer_id,omitempty" gorm:"type:varchar(128);index"`
	Status                     string `json:"status,omitempty" gorm:"type:varchar(32);index"`
	BillingReason              string `json:"billing_reason,omitempty" gorm:"type:varchar(64)"`
	Currency                   string `json:"currency,omitempty" gorm:"type:varchar(8)"`
	AmountDue                  int64  `json:"amount_due"`
	AmountPaid                 int64  `json:"amount_paid"`
	AmountRemaining            int64  `json:"amount_remaining"`
	AttemptCount               int64  `json:"attempt_count"`
	Paid                       bool   `json:"paid"`
	PeriodStart                int64  `json:"period_start"`
	PeriodEnd                  int64  `json:"period_end"`
	Livemode                   bool   `json:"livemode" gorm:"index"`
	StripeCreatedAt            int64  `json:"stripe_created_at"`
	LastStripeEventID          string `json:"last_stripe_event_id,omitempty" gorm:"type:varchar(255)"`
	LastStripeEventType        string `json:"last_stripe_event_type,omitempty" gorm:"type:varchar(128)"`
	LastStripePayloadDigest    string `json:"-" gorm:"type:varchar(64)"`
	ReviewReason               string `json:"review_reason,omitempty" gorm:"type:varchar(255);index"`
	StateObservedAt            int64  `json:"state_observed_at" gorm:"index"`
	CreatedAt                  int64  `json:"created_at"`
	UpdatedAt                  int64  `json:"updated_at"`
}

func (i *StripeLegacyInvoice) BeforeCreate(_ *gorm.DB) error {
	now := time.Now().Unix()
	if i.CreatedAt == 0 {
		i.CreatedAt = now
	}
	i.UpdatedAt = now
	return nil
}

func (i *StripeLegacyInvoice) BeforeUpdate(_ *gorm.DB) error {
	i.UpdatedAt = time.Now().Unix()
	return nil
}

type StripeLegacySubscriptionSnapshot struct {
	StripeSubscriptionID    string
	StripeCustomerID        string
	CheckoutSessionID       string
	TradeNo                 string
	MetadataUserID          int
	MetadataPlanID          int
	PriceIDs                []string
	PriceIDsTruncated       bool
	ProductID               string
	Quantity                int64
	Currency                string
	Status                  string
	CollectionMethod        string
	CancelAtPeriodEnd       bool
	CurrentPeriodStart      int64
	CurrentPeriodEnd        int64
	CancelAt                int64
	CanceledAt              int64
	EndedAt                 int64
	TrialStart              int64
	TrialEnd                int64
	LatestInvoiceID         string
	Livemode                bool
	StripeCreatedAt         int64
	LastStripeEventID       string
	LastStripeEventType     string
	LastStripePayloadDigest string
	StateObservedAt         int64
	SyncSource              string
	FullState               bool
}

type StripeLegacyInvoiceSnapshot struct {
	StripeInvoiceID         string
	StripeSubscriptionID    string
	StripeCustomerID        string
	Status                  string
	BillingReason           string
	Currency                string
	AmountDue               int64
	AmountPaid              int64
	AmountRemaining         int64
	AttemptCount            int64
	Paid                    bool
	PeriodStart             int64
	PeriodEnd               int64
	Livemode                bool
	StripeCreatedAt         int64
	LastStripeEventID       string
	LastStripeEventType     string
	LastStripePayloadDigest string
	StateObservedAt         int64
}

func normalizeStripeInventoryID(value, prefix string, max int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max || !strings.HasPrefix(value, prefix) {
		return "", fmt.Errorf("invalid Stripe identifier")
	}
	for _, ch := range value {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' {
			continue
		}
		return "", fmt.Errorf("invalid Stripe identifier")
	}
	return value, nil
}

func normalizeStripeOptionalID(value, prefix string, max int) string {
	value, err := normalizeStripeInventoryID(value, prefix, max)
	if err != nil {
		return ""
	}
	return value
}

func normalizeStripePriceIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if normalized := normalizeStripeOptionalID(id, "price_", 128); normalized != "" {
			id = normalized
		} else {
			id = normalizeStripeOptionalID(id, "plan_", 128)
		}
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		if len(result) >= maxStripeLegacyPriceIDs {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func normalizeStripeInventoryText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

func validateStripeLegacySubscriptionSnapshot(snapshot *StripeLegacySubscriptionSnapshot) error {
	if snapshot == nil {
		return errors.New("Stripe subscription snapshot is required")
	}
	var err error
	snapshot.StripeSubscriptionID, err = normalizeStripeInventoryID(snapshot.StripeSubscriptionID, "sub_", 128)
	if err != nil {
		return err
	}
	snapshot.StripeCustomerID = normalizeStripeOptionalID(snapshot.StripeCustomerID, "cus_", 128)
	snapshot.CheckoutSessionID = normalizeStripeOptionalID(snapshot.CheckoutSessionID, "cs_", 128)
	snapshot.TradeNo = normalizeStripeInventoryText(snapshot.TradeNo, 128)
	if snapshot.PriceIDs != nil {
		if len(snapshot.PriceIDs) > maxStripeLegacyPriceIDs {
			snapshot.PriceIDsTruncated = true
		}
		snapshot.PriceIDs = normalizeStripePriceIDs(snapshot.PriceIDs)
	}
	snapshot.ProductID = normalizeStripeOptionalID(snapshot.ProductID, "prod_", 128)
	snapshot.Currency = strings.ToUpper(normalizeStripeInventoryText(snapshot.Currency, 8))
	snapshot.Status = strings.ToLower(normalizeStripeInventoryText(snapshot.Status, 32))
	snapshot.CollectionMethod = strings.ToLower(normalizeStripeInventoryText(snapshot.CollectionMethod, 32))
	snapshot.LastStripeEventID = normalizeStripeInventoryText(snapshot.LastStripeEventID, 255)
	snapshot.LastStripeEventType = normalizeStripeInventoryText(snapshot.LastStripeEventType, 128)
	snapshot.LastStripePayloadDigest = normalizeStripeInventoryText(snapshot.LastStripePayloadDigest, 64)
	snapshot.SyncSource = normalizeStripeInventoryText(snapshot.SyncSource, 32)
	if snapshot.StateObservedAt <= 0 {
		snapshot.StateObservedAt = time.Now().Unix()
	}
	if snapshot.Quantity < 0 {
		return errors.New("invalid Stripe subscription quantity")
	}
	return nil
}

func ensureStripeLegacySubscriptionTx(tx *gorm.DB, subscriptionID string) (*StripeLegacySubscription, error) {
	seed := &StripeLegacySubscription{
		StripeSubscriptionID: subscriptionID,
		Status:               "unknown",
		MappingStatus:        StripeLegacyMappingUnmapped,
		MappingReason:        "subscription has not been mapped",
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "stripe_subscription_id"}},
		DoNothing: true,
	}).Create(seed).Error; err != nil {
		return nil, err
	}
	var inventory StripeLegacySubscription
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stripe_subscription_id = ?", subscriptionID).First(&inventory).Error; err != nil {
		return nil, err
	}
	return &inventory, nil
}

func UpsertStripeLegacySubscription(snapshot StripeLegacySubscriptionSnapshot) (*StripeLegacySubscription, error) {
	if err := validateStripeLegacySubscriptionSnapshot(&snapshot); err != nil {
		return nil, err
	}
	var result StripeLegacySubscription
	err := DB.Transaction(func(tx *gorm.DB) error {
		inventory, err := ensureStripeLegacySubscriptionTx(tx, snapshot.StripeSubscriptionID)
		if err != nil {
			return err
		}
		metadataUserID, metadataPlanID := snapshot.MetadataUserID, snapshot.MetadataPlanID
		if snapshot.LastStripeEventID != "" && snapshot.LastStripeEventID == inventory.LastStripeEventID &&
			inventory.LastStripePayloadDigest != "" && snapshot.LastStripePayloadDigest != "" &&
			inventory.LastStripePayloadDigest != snapshot.LastStripePayloadDigest {
			inventory.ReviewReason = "stripe_event_payload_conflict"
			if err := resolveStripeLegacyMappingTx(tx, inventory, 0, 0, true); err != nil {
				return err
			}
			if err := tx.Save(inventory).Error; err != nil {
				return err
			}
			result = *inventory
			return nil
		}
		if snapshot.FullState && snapshot.StateObservedAt < inventory.StateObservedAt {
			result = *inventory
			return nil
		}
		mergeStripeLegacySubscriptionSnapshot(inventory, snapshot)
		if err := resolveStripeLegacyMappingTx(tx, inventory, metadataUserID, metadataPlanID, !snapshot.FullState); err != nil {
			return err
		}
		if err := tx.Save(inventory).Error; err != nil {
			return err
		}
		result = *inventory
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func mergeStripeLegacySubscriptionSnapshot(inventory *StripeLegacySubscription, snapshot StripeLegacySubscriptionSnapshot) {
	if snapshot.StripeCustomerID != "" {
		inventory.StripeCustomerID = snapshot.StripeCustomerID
	}
	if snapshot.CheckoutSessionID != "" {
		inventory.CheckoutSessionID = snapshot.CheckoutSessionID
	}
	if snapshot.TradeNo != "" {
		inventory.TradeNo = snapshot.TradeNo
	}
	if !snapshot.FullState || snapshot.StateObservedAt < inventory.StateObservedAt {
		return
	}
	if snapshot.PriceIDs != nil {
		priceIDs, _ := common.Marshal(snapshot.PriceIDs)
		inventory.PriceIDsJSON = string(priceIDs)
		inventory.PrimaryPriceID = ""
		if len(snapshot.PriceIDs) > 0 {
			inventory.PrimaryPriceID = snapshot.PriceIDs[0]
		}
	}
	if snapshot.ProductID != "" {
		inventory.ProductID = snapshot.ProductID
	}
	if snapshot.PriceIDs != nil {
		inventory.Quantity = snapshot.Quantity
	}
	if snapshot.Currency != "" {
		inventory.Currency = snapshot.Currency
	}
	if snapshot.Status != "" {
		inventory.Status = snapshot.Status
	}
	inventory.CollectionMethod = snapshot.CollectionMethod
	inventory.CancelAtPeriodEnd = snapshot.CancelAtPeriodEnd
	inventory.CurrentPeriodStart = snapshot.CurrentPeriodStart
	inventory.CurrentPeriodEnd = snapshot.CurrentPeriodEnd
	inventory.CancelAt = snapshot.CancelAt
	inventory.CanceledAt = snapshot.CanceledAt
	inventory.EndedAt = snapshot.EndedAt
	inventory.TrialStart = snapshot.TrialStart
	inventory.TrialEnd = snapshot.TrialEnd
	if snapshot.LatestInvoiceID != "" {
		inventory.LatestInvoiceID = snapshot.LatestInvoiceID
	}
	inventory.Livemode = snapshot.Livemode
	inventory.StripeCreatedAt = snapshot.StripeCreatedAt
	if snapshot.LastStripeEventID != "" {
		inventory.LastStripeEventID = snapshot.LastStripeEventID
	}
	if snapshot.LastStripeEventType != "" {
		inventory.LastStripeEventType = snapshot.LastStripeEventType
	}
	if snapshot.LastStripePayloadDigest != "" {
		inventory.LastStripePayloadDigest = snapshot.LastStripePayloadDigest
	}
	if snapshot.PriceIDsTruncated {
		inventory.ReviewReason = "stripe_price_list_truncated"
	} else if snapshot.SyncSource == StripeLegacySyncSourceAPI {
		inventory.ReviewReason = ""
	}
	inventory.StateObservedAt = snapshot.StateObservedAt
	inventory.LastSyncedAt = time.Now().Unix()
	inventory.SyncSource = snapshot.SyncSource
}

func resolveStripeLegacyMappingTx(tx *gorm.DB, inventory *StripeLegacySubscription, metadataUserID, metadataPlanID int, preserveExisting bool) error {
	// Local trade/order identity is the strongest mapping because it was
	// created by this service. Otherwise prefer Stripe's customer and price
	// identifiers; metadata is only a fallback when those identifiers are not
	// available, so an operator-edited metadata value cannot override a bound
	// customer or a configured plan price.
	var userID, planID int
	var userSource, planSource string
	var userReason, planReason string

	if inventory.TradeNo != "" {
		var paymentOrder PaymentOrder
		paymentOrderQuery := tx.Where("trade_no = ? AND order_kind = ?", inventory.TradeNo, PaymentOrderKindSubscription).Limit(1).Find(&paymentOrder)
		if paymentOrderQuery.Error != nil {
			return paymentOrderQuery.Error
		}
		if paymentOrderQuery.RowsAffected == 1 {
			userID, userSource = paymentOrder.UserID, "trade_no"
			var snapshot SubscriptionPlanSnapshot
			if common.UnmarshalJsonStr(paymentOrder.ProductSnapshot, &snapshot) == nil && snapshot.PlanId > 0 {
				planID, planSource = snapshot.PlanId, "trade_no"
			}
		}
		if userID == 0 || planID == 0 {
			var order SubscriptionOrder
			orderQuery := tx.Where("trade_no = ?", inventory.TradeNo).Limit(1).Find(&order)
			if orderQuery.Error != nil {
				return orderQuery.Error
			}
			if orderQuery.RowsAffected == 1 {
				if userID == 0 {
					userID, userSource = order.UserId, "trade_no"
				}
				if planID == 0 {
					planID, planSource = order.PlanId, "trade_no"
				}
			}
		}
	}

	if userID == 0 && inventory.StripeCustomerID != "" {
		var users []User
		if err := tx.Select("id").Where("stripe_customer = ?", inventory.StripeCustomerID).Limit(2).Find(&users).Error; err != nil {
			return err
		}
		switch len(users) {
		case 1:
			userID, userSource = users[0].Id, "customer"
		case 0:
			userReason = "Stripe customer is not bound to a local user"
		default:
			userReason = "Stripe customer is bound to multiple local users"
		}
	}
	if userID == 0 && metadataUserID > 0 && inventory.StripeCustomerID == "" {
		if valid, err := stripeLegacyUserExistsTx(tx, metadataUserID); err != nil {
			return err
		} else if valid {
			userID, userSource = metadataUserID, "metadata"
		}
	}
	if userID == 0 && preserveExisting && inventory.UserID != nil {
		if valid, err := stripeLegacyUserExistsTx(tx, *inventory.UserID); err != nil {
			return err
		} else if valid {
			userID, userSource = *inventory.UserID, "existing"
		}
	}
	priceIDs := inventory.PriceIDs()
	if planID == 0 && len(priceIDs) > 0 {
		var plans []SubscriptionPlan
		if err := tx.Select("id", "stripe_price_id").Where("stripe_price_id IN ?", priceIDs).Limit(len(priceIDs) + 1).Find(&plans).Error; err != nil {
			return err
		}
		distinct := make(map[int]struct{}, len(plans))
		for _, plan := range plans {
			distinct[plan.Id] = struct{}{}
		}
		switch len(distinct) {
		case 1:
			for id := range distinct {
				planID, planSource = id, "price"
			}
		case 0:
			planReason = "Stripe prices are not mapped to a local subscription plan"
		default:
			planReason = "Stripe prices map to multiple local subscription plans"
		}
	}
	if planID == 0 && metadataPlanID > 0 && len(priceIDs) == 0 {
		if valid, err := stripeLegacyPlanExistsTx(tx, metadataPlanID); err != nil {
			return err
		} else if valid {
			planID, planSource = metadataPlanID, "metadata"
		}
	}
	if planID == 0 && preserveExisting && inventory.SubscriptionPlanID != nil {
		if valid, err := stripeLegacyPlanExistsTx(tx, *inventory.SubscriptionPlanID); err != nil {
			return err
		} else if valid {
			planID, planSource = *inventory.SubscriptionPlanID, "existing"
		}
	}

	if userID > 0 {
		inventory.UserID = &userID
	} else {
		inventory.UserID = nil
	}
	if planID > 0 {
		inventory.SubscriptionPlanID = &planID
	} else {
		inventory.SubscriptionPlanID = nil
	}

	if userID > 0 && planID > 0 {
		inventory.MappingStatus = StripeLegacyMappingMapped
		inventory.MappingReason = ""
		inventory.MappingSource = stripeLegacyMappingSource(userSource, planSource)
		return nil
	}
	if userID == 0 && userReason == "" {
		if inventory.StripeCustomerID == "" {
			userReason = "Stripe subscription has no customer identifier"
		} else {
			userReason = "Stripe customer could not be mapped"
		}
	}
	if planID == 0 && planReason == "" {
		if len(priceIDs) == 0 {
			planReason = "Stripe subscription has no recurring price identifier"
		} else {
			planReason = "Stripe prices could not be mapped"
		}
	}

	switch {
	case userID == 0 && strings.Contains(userReason, "multiple"):
		inventory.MappingStatus = StripeLegacyMappingAmbiguousUser
	case planID == 0 && strings.Contains(planReason, "multiple"):
		inventory.MappingStatus = StripeLegacyMappingAmbiguousPlan
	case userID == 0 && planID > 0:
		inventory.MappingStatus = StripeLegacyMappingUnmappedUser
	case userID > 0 && planID == 0:
		inventory.MappingStatus = StripeLegacyMappingUnmappedPlan
	default:
		inventory.MappingStatus = StripeLegacyMappingUnmapped
	}
	inventory.MappingReason = normalizeStripeInventoryText(strings.Trim(strings.Join([]string{userReason, planReason}, "; "), "; "), 255)
	inventory.MappingSource = stripeLegacyMappingSource(userSource, planSource)
	return nil
}

func stripeLegacyMappingSource(userSource, planSource string) string {
	if userSource == "" {
		return planSource
	}
	if planSource == "" || planSource == userSource {
		return userSource
	}
	return normalizeStripeInventoryText(userSource+"+"+planSource, 32)
}

func stripeLegacyUserExistsTx(tx *gorm.DB, userID int) (bool, error) {
	var count int64
	err := tx.Model(&User{}).Where("id = ?", userID).Count(&count).Error
	return count == 1, err
}

func stripeLegacyPlanExistsTx(tx *gorm.DB, planID int) (bool, error) {
	var count int64
	err := tx.Model(&SubscriptionPlan{}).Where("id = ?", planID).Count(&count).Error
	return count == 1, err
}

func validateStripeLegacyInvoiceSnapshot(snapshot *StripeLegacyInvoiceSnapshot) error {
	if snapshot == nil {
		return errors.New("Stripe invoice snapshot is required")
	}
	var err error
	snapshot.StripeInvoiceID, err = normalizeStripeInventoryID(snapshot.StripeInvoiceID, "in_", 128)
	if err != nil {
		return err
	}
	snapshot.StripeSubscriptionID = normalizeStripeOptionalID(snapshot.StripeSubscriptionID, "sub_", 128)
	snapshot.StripeCustomerID = normalizeStripeOptionalID(snapshot.StripeCustomerID, "cus_", 128)
	snapshot.Status = strings.ToLower(normalizeStripeInventoryText(snapshot.Status, 32))
	snapshot.BillingReason = strings.ToLower(normalizeStripeInventoryText(snapshot.BillingReason, 64))
	snapshot.Currency = strings.ToUpper(normalizeStripeInventoryText(snapshot.Currency, 8))
	snapshot.LastStripeEventID = normalizeStripeInventoryText(snapshot.LastStripeEventID, 255)
	snapshot.LastStripeEventType = normalizeStripeInventoryText(snapshot.LastStripeEventType, 128)
	snapshot.LastStripePayloadDigest = normalizeStripeInventoryText(snapshot.LastStripePayloadDigest, 64)
	if snapshot.StateObservedAt <= 0 {
		snapshot.StateObservedAt = time.Now().Unix()
	}
	return nil
}

func UpsertStripeLegacyInvoice(snapshot StripeLegacyInvoiceSnapshot) (*StripeLegacyInvoice, error) {
	if err := validateStripeLegacyInvoiceSnapshot(&snapshot); err != nil {
		return nil, err
	}
	var result StripeLegacyInvoice
	err := DB.Transaction(func(tx *gorm.DB) error {
		seed := &StripeLegacyInvoice{StripeInvoiceID: snapshot.StripeInvoiceID}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "stripe_invoice_id"}},
			DoNothing: true,
		}).Create(seed).Error; err != nil {
			return err
		}
		var invoice StripeLegacyInvoice
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("stripe_invoice_id = ?", snapshot.StripeInvoiceID).First(&invoice).Error; err != nil {
			return err
		}
		eventConflict := false
		if snapshot.StateObservedAt >= invoice.StateObservedAt {
			if snapshot.LastStripeEventID != "" && snapshot.LastStripeEventID == invoice.LastStripeEventID &&
				invoice.LastStripePayloadDigest != "" && snapshot.LastStripePayloadDigest != "" &&
				invoice.LastStripePayloadDigest != snapshot.LastStripePayloadDigest {
				eventConflict = true
				invoice.ReviewReason = "stripe_event_payload_conflict"
			} else {
				if snapshot.StripeSubscriptionID != "" {
					invoice.StripeSubscriptionID = snapshot.StripeSubscriptionID
				}
				if snapshot.StripeCustomerID != "" {
					invoice.StripeCustomerID = snapshot.StripeCustomerID
				}
				invoice.Status = snapshot.Status
				invoice.BillingReason = snapshot.BillingReason
				invoice.Currency = snapshot.Currency
				invoice.AmountDue = snapshot.AmountDue
				invoice.AmountPaid = snapshot.AmountPaid
				invoice.AmountRemaining = snapshot.AmountRemaining
				invoice.AttemptCount = snapshot.AttemptCount
				invoice.Paid = snapshot.Paid
				invoice.PeriodStart = snapshot.PeriodStart
				invoice.PeriodEnd = snapshot.PeriodEnd
				invoice.Livemode = snapshot.Livemode
				invoice.StripeCreatedAt = snapshot.StripeCreatedAt
				invoice.LastStripeEventID = snapshot.LastStripeEventID
				invoice.LastStripeEventType = snapshot.LastStripeEventType
				invoice.LastStripePayloadDigest = snapshot.LastStripePayloadDigest
				invoice.StateObservedAt = snapshot.StateObservedAt
			}
		}
		if snapshot.StripeSubscriptionID != "" {
			inventory, err := ensureStripeLegacySubscriptionTx(tx, snapshot.StripeSubscriptionID)
			if err != nil {
				return err
			}
			if !eventConflict && snapshot.StripeCustomerID != "" {
				inventory.StripeCustomerID = snapshot.StripeCustomerID
			}
			if err := resolveStripeLegacyMappingTx(tx, inventory, 0, 0, true); err != nil {
				return err
			}
			if !eventConflict && snapshot.StateObservedAt >= inventory.LatestInvoiceObservedAt {
				inventory.LatestInvoiceID = snapshot.StripeInvoiceID
				inventory.LatestInvoiceStatus = snapshot.Status
				inventory.LatestInvoicePaid = snapshot.Paid
				inventory.LatestInvoiceAmountDue = snapshot.AmountDue
				inventory.LatestInvoiceAmountPaid = snapshot.AmountPaid
				inventory.LatestInvoiceCurrency = snapshot.Currency
				inventory.LatestInvoiceObservedAt = snapshot.StateObservedAt
			}
			if err := tx.Save(inventory).Error; err != nil {
				return err
			}
			invoice.StripeLegacySubscriptionID = &inventory.ID
		}
		if err := tx.Save(&invoice).Error; err != nil {
			return err
		}
		result = invoice
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

type StripeLegacySubscriptionFilter struct {
	UserID         int
	Status         string
	MappingStatus  string
	ReviewReason   string
	CustomerID     string
	SubscriptionID string
}

func ListStripeLegacySubscriptions(filter StripeLegacySubscriptionFilter, pageInfo *common.PageInfo) ([]*StripeLegacySubscription, int64, error) {
	if pageInfo == nil {
		return nil, 0, errors.New("page information is required")
	}
	query := DB.Model(&StripeLegacySubscription{})
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if status := strings.ToLower(strings.TrimSpace(filter.Status)); status != "" {
		query = query.Where("status = ?", status)
	}
	if mappingStatus := strings.ToLower(strings.TrimSpace(filter.MappingStatus)); mappingStatus != "" {
		query = query.Where("mapping_status = ?", mappingStatus)
	}
	if reviewReason := strings.ToLower(strings.TrimSpace(filter.ReviewReason)); reviewReason != "" {
		query = query.Where("review_reason = ?", reviewReason)
	}
	if rawCustomerID := strings.TrimSpace(filter.CustomerID); rawCustomerID != "" {
		customerID := normalizeStripeOptionalID(rawCustomerID, "cus_", 128)
		if customerID == "" {
			return nil, 0, errors.New("invalid Stripe customer ID")
		}
		query = query.Where("stripe_customer_id = ?", customerID)
	}
	if rawSubscriptionID := strings.TrimSpace(filter.SubscriptionID); rawSubscriptionID != "" {
		subscriptionID := normalizeStripeOptionalID(rawSubscriptionID, "sub_", 128)
		if subscriptionID == "" {
			return nil, 0, errors.New("invalid Stripe subscription ID")
		}
		query = query.Where("stripe_subscription_id = ?", subscriptionID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var subscriptions []*StripeLegacySubscription
	if err := query.Order("id desc").Limit(pageInfo.GetPageSize()).Offset(pageInfo.GetStartIdx()).Find(&subscriptions).Error; err != nil {
		return nil, 0, err
	}
	return subscriptions, total, nil
}

func GetStripeLegacySubscriptionByStripeID(subscriptionID string) (*StripeLegacySubscription, error) {
	subscriptionID = normalizeStripeOptionalID(subscriptionID, "sub_", 128)
	if subscriptionID == "" {
		return nil, errors.New("invalid Stripe subscription ID")
	}
	var inventory StripeLegacySubscription
	if err := DB.Where("stripe_subscription_id = ?", subscriptionID).First(&inventory).Error; err != nil {
		return nil, err
	}
	return &inventory, nil
}

func ParseStripeLegacyMetadataID(value string) int {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 10 {
		return 0
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}
