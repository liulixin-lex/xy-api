package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	PaymentReviewCodeLegacySubscriptionContractUnavailable = "legacy_subscription_contract_unavailable"

	PaymentUnmatchedAvailableActionResolveLegacySubscription = "resolve_legacy_subscription"

	PaymentOperationsActionLegacySubscriptionRefund = "payment.legacy_subscription_external_refund_confirmed"
)

type PaymentLegacySubscriptionResolutionInput struct {
	EventID                 int64
	ExpectedEventAttempts   int
	AdminID                 int
	ActorIP                 string
	Resolution              string
	ProviderRefundReference string
	Reason                  string
}

func ResolveLegacySubscriptionPaymentEventByAdmin(input PaymentLegacySubscriptionResolutionInput) (*PaymentUnmatchedEventActionResult, error) {
	input.Resolution = strings.ToLower(strings.TrimSpace(input.Resolution))
	input.ActorIP = strings.TrimSpace(input.ActorIP)
	input.ProviderRefundReference = strings.TrimSpace(input.ProviderRefundReference)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.EventID <= 0 || input.ExpectedEventAttempts <= 0 || input.AdminID <= 0 || input.ActorIP == "" ||
		len(input.ActorIP) > 64 || input.Resolution != PaymentLegacyTopUpResolutionExternalRefund ||
		!validPaymentProviderReference(input.ProviderRefundReference) || len(input.Reason) < 8 || len(input.Reason) > 512 {
		return nil, ErrPaymentAuditInvalid
	}

	result := &PaymentUnmatchedEventActionResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, err := lockPaymentConfigurationFenceTx(tx); err != nil {
			return err
		}
		var providerEvent PaymentEvent
		if err := lockForUpdate(tx).First(&providerEvent, input.EventID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentAuditNotFound
			}
			return err
		}
		result.Event = &providerEvent
		if providerEvent.Provider == PaymentProviderStripe {
			if normalized, err := normalizeStripeInventoryID(input.ProviderRefundReference, "re_", 128); err != nil ||
				normalized != input.ProviderRefundReference || len(normalized) <= len("re_") {
				return ErrPaymentAuditInvalid
			}
		}

		payload, err := legacySubscriptionResolutionPayload(input, &providerEvent)
		if err != nil {
			return err
		}
		adminEventKey := legacySubscriptionResolutionAdminEventKey(input, &providerEvent)
		duplicate, err := processedAdminActionRetryTx(tx, adminEventKey, PaymentPayloadDigest(payload))
		if err != nil {
			return err
		}
		if duplicate {
			order, err := validateCompletedLegacySubscriptionResolutionTx(tx, input, &providerEvent, adminEventKey, payload)
			if err != nil {
				return err
			}
			result.Order = order
			result.Duplicate = true
			return nil
		}
		reviewCode := providerEvent.ReviewCode
		if providerEvent.Status != PaymentEventStatusManualReview || providerEvent.PaymentOrderID != 0 ||
			providerEvent.Attempts != input.ExpectedEventAttempts ||
			(reviewCode != PaymentReviewCodeLegacySubscriptionContractUnavailable &&
				reviewCode != PaymentReviewCodeStripeLegacyRecurringCheckoutPaid) {
			return fmt.Errorf("%w: legacy subscription event state changed", ErrPaymentAuditConflict)
		}
		subscription, err := lockLegacySubscriptionResolutionContractTx(tx, &providerEvent)
		if err != nil {
			return err
		}
		order, err := createLegacySubscriptionResolutionOrderTx(tx, input, &providerEvent, subscription)
		if err != nil {
			return err
		}
		if providerEvent.Provider == PaymentProviderStripe {
			if _, err := bindStripeCustomerTx(tx, order, providerEvent.CustomerID); err != nil {
				return fmt.Errorf("%w: Stripe customer ownership changed", ErrPaymentAuditConflict)
			}
		}
		result.Order = order
		adminEvent, err := createAdminPaymentEventTx(tx, adminEventKey, PaymentOperationsActionLegacySubscriptionRefund, payload, order)
		if err != nil {
			return err
		}
		claimed := tx.Model(&PaymentEvent{}).
			Where("id = ? AND status = ? AND payment_order_id = ? AND attempts = ? AND review_code = ?", providerEvent.ID,
				PaymentEventStatusManualReview, 0, input.ExpectedEventAttempts, reviewCode).
			Updates(map[string]interface{}{
				"status": PaymentEventStatusProcessing, "review_code": "", "last_error": "",
				"attempts": input.ExpectedEventAttempts + 1, "updated_at": common.GetTimestamp(),
			})
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected != 1 {
			return fmt.Errorf("%w: legacy subscription event state changed", ErrPaymentAuditConflict)
		}
		providerEvent.Status = PaymentEventStatusProcessing
		providerEvent.ReviewCode = ""
		providerEvent.LastError = ""
		providerEvent.Attempts = input.ExpectedEventAttempts + 1

		if err := refundLegacySubscriptionResolutionTx(tx, &providerEvent, adminEvent, order, subscription, input); err != nil {
			return err
		}
		if err := finishPaymentEventTx(tx, providerEvent.ID, PaymentEventStatusProcessed, "", order.ID); err != nil {
			return err
		}
		if err := finishPaymentEventTx(tx, adminEvent.ID, PaymentEventStatusProcessed, "", order.ID); err != nil {
			return err
		}
		providerEvent.Status = PaymentEventStatusProcessed
		providerEvent.PaymentOrderID = order.ID
		providerEvent.ProcessedAt = common.GetTimestamp()
		result.Event = &providerEvent

		metadata := legacySubscriptionResolutionAuditMetadata(input, &providerEvent, adminEvent, order, subscription)
		return createPaymentOperationsAuditTx(tx, PaymentOperationsAudit{
			Action: PaymentOperationsActionLegacySubscriptionRefund, AdminID: input.AdminID, ActorIP: input.ActorIP,
			PaymentOrderID: order.ID, UserID: order.UserID, SubjectID: providerEvent.ID, Provider: providerEvent.Provider,
			ExpectedVersion: int64(input.ExpectedEventAttempts), Reason: input.Reason,
		}, metadata)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func lockLegacySubscriptionResolutionContractTx(tx *gorm.DB, event *PaymentEvent) (*SubscriptionOrder, error) {
	if tx == nil || event == nil ||
		(event.ReviewCode != PaymentReviewCodeLegacySubscriptionContractUnavailable &&
			event.ReviewCode != PaymentReviewCodeStripeLegacyRecurringCheckoutPaid) {
		return nil, fmt.Errorf("%w: payment event is not classified as a legacy subscription contract incident", ErrPaymentAuditConflict)
	}
	if err := validateLegacySubscriptionResolutionEventEvidence(event); err != nil {
		return nil, err
	}
	var existingOrder PaymentOrder
	if query := lockForUpdate(tx).Where("trade_no = ?", event.TradeNo).Limit(1).Find(&existingOrder); query.Error != nil {
		return nil, query.Error
	} else if query.RowsAffected > 0 {
		return nil, fmt.Errorf("%w: legacy subscription already has a canonical order", ErrPaymentAuditConflict)
	}
	var topUp TopUp
	if query := lockForUpdate(tx).Where("trade_no = ?", event.TradeNo).Limit(1).Find(&topUp); query.Error != nil {
		return nil, query.Error
	} else if query.RowsAffected > 0 {
		return nil, fmt.Errorf("%w: legacy payment trade number is ambiguous", ErrPaymentAuditConflict)
	}
	var subscription SubscriptionOrder
	if err := lockForUpdate(tx).Where("trade_no = ?", event.TradeNo).First(&subscription).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentAuditNotFound
		}
		return nil, err
	}
	if err := validateLegacySubscriptionResolutionEvidence(event, &subscription); err != nil {
		return nil, err
	}
	if event.Provider == PaymentProviderStripe {
		if err := validateStripeLegacyRecurringResolutionInventoryTx(tx, event, &subscription); err != nil {
			return nil, err
		}
	}
	var user User
	if err := lockForUpdate(tx).Select("id").Where("id = ?", subscription.UserId).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: legacy subscription user is missing", ErrPaymentAuditConflict)
		}
		return nil, err
	}
	return &subscription, nil
}

func validateLegacySubscriptionResolutionEventEvidence(event *PaymentEvent) error {
	if event == nil {
		return fmt.Errorf("%w: legacy subscription event evidence is missing", ErrPaymentAuditConflict)
	}
	switch event.ReviewCode {
	case PaymentReviewCodeLegacySubscriptionContractUnavailable:
		return validateLegacyEpayEventEvidence(event)
	case PaymentReviewCodeStripeLegacyRecurringCheckoutPaid:
		return validateStripeLegacyRecurringCheckoutEventEvidence(event)
	default:
		return fmt.Errorf("%w: legacy subscription review classification changed", ErrPaymentAuditConflict)
	}
}

func validateLegacySubscriptionResolutionEvidence(event *PaymentEvent, subscription *SubscriptionOrder) error {
	if event == nil {
		return fmt.Errorf("%w: legacy subscription event evidence is missing", ErrPaymentAuditConflict)
	}
	switch event.ReviewCode {
	case PaymentReviewCodeLegacySubscriptionContractUnavailable:
		return validateLegacySubscriptionRefundEvidence(event, subscription)
	case PaymentReviewCodeStripeLegacyRecurringCheckoutPaid:
		return validateStripeLegacyRecurringSubscriptionRefundEvidence(event, subscription)
	default:
		return fmt.Errorf("%w: legacy subscription review classification changed", ErrPaymentAuditConflict)
	}
}

func validateLegacySubscriptionRefundEvidence(event *PaymentEvent, subscription *SubscriptionOrder) error {
	if event == nil || subscription == nil || subscription.PaymentOrderId != nil || subscription.Id <= 0 ||
		subscription.UserId <= 0 || subscription.PlanId <= 0 || subscription.CreateTime <= 0 ||
		subscription.Status != common.TopUpStatusPending {
		return fmt.Errorf("%w: legacy subscription projection is not safely refundable", ErrPaymentAuditConflict)
	}
	if event.ReviewCode != PaymentReviewCodeLegacySubscriptionContractUnavailable {
		return fmt.Errorf("%w: legacy subscription review classification changed", ErrPaymentAuditConflict)
	}
	if err := validateLegacyPaymentProviderAndMethod(subscription.PaymentProvider, subscription.PaymentMethod,
		paymentEventInputFromStoredEvent(event)); err != nil {
		return fmt.Errorf("%w: legacy subscription provider contract changed", ErrPaymentAuditConflict)
	}
	if subscription.ProviderOrderKey != nil && strings.TrimSpace(*subscription.ProviderOrderKey) != event.ProviderOrderKey {
		return fmt.Errorf("%w: legacy subscription provider order identity changed", ErrPaymentAuditConflict)
	}
	if strings.TrimSpace(subscription.ProviderOrderId) != "" &&
		event.ProviderOrderKey != PaymentProviderEpay+":"+strings.TrimSpace(subscription.ProviderOrderId) {
		return fmt.Errorf("%w: legacy subscription provider order identity changed", ErrPaymentAuditConflict)
	}
	minor, err := legacyPaymentMoneyMinorExact(subscription.Money, 2, math.MaxInt32)
	if err != nil || minor != event.PaidAmountMinor || event.Currency != "CNY" {
		return fmt.Errorf("%w: legacy subscription paid amount no longer matches", ErrPaymentAuditConflict)
	}
	if subscription.ExpectedAmountMinor > 0 && subscription.ExpectedAmountMinor != event.PaidAmountMinor {
		return fmt.Errorf("%w: initialized legacy subscription amount conflicts with the provider event", ErrPaymentAuditConflict)
	}
	if currency := strings.ToUpper(strings.TrimSpace(subscription.PaymentCurrency)); currency != "" && currency != event.Currency {
		return fmt.Errorf("%w: initialized legacy subscription currency conflicts with the provider event", ErrPaymentAuditConflict)
	}
	return nil
}

func validateStripeLegacyRecurringCheckoutEventEvidence(event *PaymentEvent) error {
	if event == nil || event.Provider != PaymentProviderStripe ||
		(event.EventType != "checkout.session.completed" && event.EventType != "checkout.session.async_payment_succeeded") ||
		event.ProviderState != PaymentProviderStateStripeLegacyRecurringCheckoutPaid || !event.ManualReview ||
		event.Paid || event.Failed || event.Expired || event.Refunded || event.Disputed || event.DisputeResolved ||
		event.PermanentFailure || event.PaidAmountMinor <= 0 || event.ProviderCredentialGeneration <= 0 ||
		event.ProviderLivemode == nil || event.PaymentMethod != PaymentMethodStripe ||
		strings.TrimSpace(event.TradeNo) == "" || event.TradeNo != strings.TrimSpace(event.TradeNo) ||
		strings.TrimSpace(event.NormalizedPayload) == "" || event.PayloadDigest != PaymentPayloadDigest(event.NormalizedPayload) {
		return fmt.Errorf("%w: event is not an intact verified legacy Stripe recurring Checkout payment", ErrPaymentAuditConflict)
	}
	if _, ok := common.PaymentProviderCurrencyExponentOK(PaymentProviderStripe, event.Currency); !ok ||
		event.Currency != strings.ToUpper(strings.TrimSpace(event.Currency)) {
		return fmt.Errorf("%w: legacy Stripe recurring Checkout currency is invalid", ErrPaymentAuditConflict)
	}
	sessionID := strings.TrimPrefix(event.ProviderOrderKey, PaymentProviderStripe+":")
	if normalized, err := normalizeStripeInventoryID(sessionID, "cs_", 128); err != nil ||
		event.ProviderOrderKey != PaymentProviderStripe+":"+normalized || len(normalized) <= len("cs_") {
		return fmt.Errorf("%w: legacy Stripe Checkout Session identity is invalid", ErrPaymentAuditConflict)
	}
	subscriptionID := strings.TrimPrefix(event.ProviderResourceKey, PaymentProviderStripe+":")
	if normalized, err := normalizeStripeInventoryID(subscriptionID, "sub_", 128); err != nil ||
		event.ProviderResourceKey != PaymentProviderStripe+":"+normalized || len(normalized) <= len("sub_") {
		return fmt.Errorf("%w: legacy Stripe subscription identity is invalid", ErrPaymentAuditConflict)
	}
	if normalized, err := normalizeStripeInventoryID(event.CustomerID, "cus_", 64); err != nil ||
		normalized != event.CustomerID || len(normalized) <= len("cus_") {
		return fmt.Errorf("%w: legacy Stripe customer identity is invalid", ErrPaymentAuditConflict)
	}
	if event.ProviderPaymentKey != "" {
		paymentID := strings.TrimPrefix(event.ProviderPaymentKey, PaymentProviderStripe+":")
		if normalized, err := normalizeStripeInventoryID(paymentID, "pi_", 128); err != nil ||
			event.ProviderPaymentKey != PaymentProviderStripe+":"+normalized || len(normalized) <= len("pi_") {
			return fmt.Errorf("%w: legacy Stripe payment identity is invalid", ErrPaymentAuditConflict)
		}
	}
	var payload struct {
		SessionID      string `json:"session_id"`
		TradeNo        string `json:"trade_no"`
		AmountTotal    int64  `json:"amount_total"`
		Currency       string `json:"currency"`
		PaymentStatus  string `json:"payment_status"`
		Status         string `json:"status"`
		Mode           string `json:"mode"`
		SubscriptionID string `json:"subscription_id"`
		DataDigest     string `json:"data_digest"`
	}
	if err := common.UnmarshalJsonStr(event.NormalizedPayload, &payload); err != nil ||
		payload.SessionID != sessionID || payload.SubscriptionID != subscriptionID ||
		payload.TradeNo != event.TradeNo || payload.AmountTotal != event.PaidAmountMinor ||
		!strings.EqualFold(payload.Currency, event.Currency) || payload.PaymentStatus != "paid" ||
		payload.Status != "complete" || payload.Mode != "subscription" || len(strings.TrimSpace(payload.DataDigest)) != 64 {
		return fmt.Errorf("%w: legacy Stripe recurring Checkout normalized evidence changed", ErrPaymentAuditConflict)
	}
	for _, char := range strings.TrimSpace(payload.DataDigest) {
		if char < '0' || char > '9' && char < 'a' || char > 'f' {
			return fmt.Errorf("%w: legacy Stripe recurring Checkout payload digest is invalid", ErrPaymentAuditConflict)
		}
	}
	if err := paymentEventInputFromStoredEvent(event).validate(); err != nil {
		return fmt.Errorf("%w: stored legacy Stripe recurring Checkout event is invalid", ErrPaymentAuditConflict)
	}
	return nil
}

func validateStripeLegacyRecurringSubscriptionRefundEvidence(event *PaymentEvent, subscription *SubscriptionOrder) error {
	if err := validateStripeLegacyRecurringCheckoutEventEvidence(event); err != nil {
		return err
	}
	if subscription == nil || subscription.PaymentOrderId != nil || subscription.Id <= 0 ||
		subscription.UserId <= 0 || subscription.PlanId <= 0 || subscription.CreateTime <= 0 ||
		subscription.CompleteTime != 0 || subscription.Status != common.TopUpStatusPending ||
		strings.TrimSpace(subscription.TradeNo) != event.TradeNo ||
		strings.ToLower(strings.TrimSpace(subscription.PaymentProvider)) != PaymentProviderStripe ||
		strings.ToLower(strings.TrimSpace(subscription.PaymentMethod)) != PaymentMethodStripe {
		return fmt.Errorf("%w: legacy Stripe subscription projection is not safely refundable", ErrPaymentAuditConflict)
	}
	if subscription.ProviderOrderKey != nil && strings.TrimSpace(*subscription.ProviderOrderKey) != event.ProviderOrderKey {
		return fmt.Errorf("%w: legacy Stripe subscription Checkout Session identity changed", ErrPaymentAuditConflict)
	}
	if providerOrderID := strings.TrimSpace(subscription.ProviderOrderId); providerOrderID != "" &&
		event.ProviderOrderKey != PaymentProviderStripe+":"+providerOrderID {
		return fmt.Errorf("%w: legacy Stripe subscription Checkout Session identity changed", ErrPaymentAuditConflict)
	}
	exponent, ok := common.PaymentProviderCurrencyExponentOK(PaymentProviderStripe, event.Currency)
	minor, err := legacyPaymentMoneyMinorExact(subscription.Money, exponent, math.MaxInt64)
	if !ok || err != nil || minor != event.PaidAmountMinor {
		return fmt.Errorf("%w: legacy Stripe subscription amount no longer matches", ErrPaymentAuditConflict)
	}
	if subscription.ExpectedAmountMinor > 0 && subscription.ExpectedAmountMinor != event.PaidAmountMinor {
		return fmt.Errorf("%w: initialized legacy Stripe subscription amount conflicts with the provider event", ErrPaymentAuditConflict)
	}
	if currency := strings.ToUpper(strings.TrimSpace(subscription.PaymentCurrency)); currency != "" && currency != event.Currency {
		return fmt.Errorf("%w: initialized legacy Stripe subscription currency conflicts with the provider event", ErrPaymentAuditConflict)
	}
	if strings.TrimSpace(subscription.PlanSnapshot) != "" {
		snapshot, err := subscription.planSnapshot()
		if err != nil || strings.ToUpper(strings.TrimSpace(snapshot.Currency)) != event.Currency {
			return fmt.Errorf("%w: initialized legacy Stripe subscription snapshot conflicts with the provider event", ErrPaymentAuditConflict)
		}
	}
	return nil
}

func validateStripeLegacyRecurringResolutionInventoryTx(tx *gorm.DB, event *PaymentEvent, subscription *SubscriptionOrder) error {
	if tx == nil || event == nil || subscription == nil {
		return ErrPaymentAuditInvalid
	}
	var inventory StripeLegacySubscription
	subscriptionID := strings.TrimPrefix(event.ProviderResourceKey, PaymentProviderStripe+":")
	if err := lockForUpdate(tx).Where("stripe_subscription_id = ?", subscriptionID).First(&inventory).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: legacy Stripe subscription inventory is missing", ErrPaymentAuditConflict)
		}
		return err
	}
	sessionID := strings.TrimPrefix(event.ProviderOrderKey, PaymentProviderStripe+":")
	if inventory.CheckoutSessionID != sessionID || inventory.TradeNo != event.TradeNo ||
		inventory.StripeCustomerID != event.CustomerID || event.ProviderLivemode == nil ||
		inventory.Livemode != *event.ProviderLivemode || strings.TrimSpace(inventory.ReviewReason) != "" ||
		inventory.UserID != nil && *inventory.UserID != subscription.UserId ||
		inventory.SubscriptionPlanID != nil && *inventory.SubscriptionPlanID != subscription.PlanId {
		return fmt.Errorf("%w: legacy Stripe subscription inventory identity changed", ErrPaymentAuditConflict)
	}
	status := strings.ToLower(strings.TrimSpace(inventory.Status))
	if !inventory.CancelAtPeriodEnd && inventory.EndedAt <= 0 && status != "canceled" && status != "incomplete_expired" {
		return fmt.Errorf("%w: legacy Stripe subscription can still renew", ErrPaymentAuditConflict)
	}
	return nil
}

func createLegacySubscriptionResolutionOrderTx(tx *gorm.DB, input PaymentLegacySubscriptionResolutionInput,
	event *PaymentEvent, subscription *SubscriptionOrder) (*PaymentOrder, error) {
	pricingSnapshot, err := legacySubscriptionResolutionPricingSnapshot(input, event, subscription)
	if err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: event.TradeNo, UserID: subscription.UserId, OrderKind: PaymentOrderKindSubscription,
		Provider: event.Provider, PaymentMethod: event.PaymentMethod,
		ProviderCredentialGeneration: event.ProviderCredentialGeneration,
		ProviderLivemode:             copyPaymentLivemode(event.ProviderLivemode),
		RequestID:                    legacyPaymentRequestID(event.Provider, event.TradeNo),
		ExpectedAmountMinor:          event.PaidAmountMinor, Currency: event.Currency,
		RequestedAmount: int64(subscription.PlanId), PricingSnapshot: pricingSnapshot,
		LegacyRecordType: PaymentOrderKindSubscription, LegacyRecordID: subscription.Id,
		Status: PaymentOrderStatusPending, CreatedAt: subscription.CreateTime, UpdatedAt: now, Version: 1,
	}
	if err := setAdoptedProviderKeys(order, paymentEventInputFromStoredEvent(event)); err != nil {
		return nil, err
	}
	if err := tx.Create(order).Error; err != nil {
		return nil, err
	}
	subscription.PaymentOrderId = &order.ID
	subscription.PaymentProvider = event.Provider
	return order, nil
}

func refundLegacySubscriptionResolutionTx(tx *gorm.DB, providerEvent, adminEvent *PaymentEvent, order *PaymentOrder,
	subscription *SubscriptionOrder, input PaymentLegacySubscriptionResolutionInput) error {
	if err := createPaymentLedgerEntryTx(tx, providerEvent, order, PaymentLedgerEntryLegacyPaymentReceived,
		order.ExpectedAmountMinor, 0, "verified legacy subscription payment received without entitlement grant"); err != nil {
		return err
	}
	adminEvent.Refunded = true
	adminEvent.RefundedAmountMinor = order.ExpectedAmountMinor
	adminEvent.ProviderResourceKey = order.Provider + ":refund:" + input.ProviderRefundReference
	if err := tx.Model(&PaymentEvent{}).Where("id = ?", adminEvent.ID).Updates(map[string]interface{}{
		"refunded": true, "refunded_amount_minor": order.ExpectedAmountMinor,
		"provider_resource_key": adminEvent.ProviderResourceKey,
	}).Error; err != nil {
		return err
	}
	now := common.GetTimestamp()
	updated := tx.Model(&PaymentOrder{}).
		Where("id = ? AND version = ? AND status = ?", order.ID, order.Version, PaymentOrderStatusPending).
		Updates(map[string]interface{}{
			"status": PaymentOrderStatusRefunded, "status_reason": input.Reason,
			"refunded_amount_minor": order.ExpectedAmountMinor, "updated_at": now, "version": order.Version + 1,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("%w: legacy subscription order changed", ErrPaymentAuditConflict)
	}
	subscription.Status = common.TopUpStatusRefunded
	subscription.CompleteTime = now
	if err := tx.Save(subscription).Error; err != nil {
		return err
	}
	order.Status = PaymentOrderStatusRefunded
	order.StatusReason = input.Reason
	order.RefundedAmountMinor = order.ExpectedAmountMinor
	order.UpdatedAt = now
	order.Version++
	return createPaymentLedgerEntryTx(tx, adminEvent, order, PaymentLedgerEntryAdminExternalRefund,
		order.ExpectedAmountMinor, 0, "external refund confirmed for legacy subscription by administrator")
}

func validateCompletedLegacySubscriptionResolutionTx(tx *gorm.DB, input PaymentLegacySubscriptionResolutionInput,
	providerEvent *PaymentEvent, adminEventKey, payload string) (*PaymentOrder, error) {
	if tx == nil || providerEvent == nil || providerEvent.Status != PaymentEventStatusProcessed ||
		providerEvent.PaymentOrderID <= 0 || providerEvent.Attempts != input.ExpectedEventAttempts+1 {
		return nil, fmt.Errorf("%w: completed legacy subscription resolution is inconsistent", ErrPaymentAuditConflict)
	}
	if providerEvent.Provider != PaymentProviderEpay && providerEvent.Provider != PaymentProviderStripe {
		return nil, fmt.Errorf("%w: completed legacy subscription provider is inconsistent", ErrPaymentAuditConflict)
	}
	completedEvidence := *providerEvent
	if providerEvent.Provider == PaymentProviderEpay {
		completedEvidence.ReviewCode = PaymentReviewCodeLegacySubscriptionContractUnavailable
	} else {
		completedEvidence.ReviewCode = PaymentReviewCodeStripeLegacyRecurringCheckoutPaid
	}
	if err := validateLegacySubscriptionResolutionEventEvidence(&completedEvidence); err != nil {
		return nil, err
	}
	var adminEvent PaymentEvent
	if err := lockForUpdate(tx).Where("provider = ? AND event_key = ?", "admin", adminEventKey).First(&adminEvent).Error; err != nil {
		return nil, err
	}
	if adminEvent.Status != PaymentEventStatusProcessed || adminEvent.PaymentOrderID != providerEvent.PaymentOrderID ||
		adminEvent.PayloadDigest != PaymentPayloadDigest(payload) || !adminEvent.Refunded ||
		adminEvent.RefundedAmountMinor != providerEvent.PaidAmountMinor ||
		adminEvent.ProviderResourceKey != providerEvent.Provider+":refund:"+input.ProviderRefundReference {
		return nil, fmt.Errorf("%w: completed legacy subscription administrator event is inconsistent", ErrPaymentAuditConflict)
	}
	var order PaymentOrder
	if err := lockForUpdate(tx).First(&order, providerEvent.PaymentOrderID).Error; err != nil {
		return nil, err
	}
	var subscription SubscriptionOrder
	if err := lockForUpdate(tx).Where("payment_order_id = ? AND trade_no = ?", order.ID, providerEvent.TradeNo).First(&subscription).Error; err != nil {
		return nil, err
	}
	if order.Provider != providerEvent.Provider || order.OrderKind != PaymentOrderKindSubscription ||
		order.TradeNo != providerEvent.TradeNo || order.UserID != subscription.UserId ||
		order.LegacyRecordType != PaymentOrderKindSubscription || order.LegacyRecordID != subscription.Id ||
		order.ProviderCredentialGeneration != providerEvent.ProviderCredentialGeneration ||
		!paymentLivemodeEqual(order.ProviderLivemode, providerEvent.ProviderLivemode) ||
		order.ExpectedAmountMinor != providerEvent.PaidAmountMinor || order.Currency != providerEvent.Currency ||
		order.PaymentMethod != providerEvent.PaymentMethod || order.RequestedAmount != int64(subscription.PlanId) ||
		order.RequestID != legacyPaymentRequestID(providerEvent.Provider, providerEvent.TradeNo) ||
		order.Status != PaymentOrderStatusRefunded || order.CreditQuota != 0 || order.PaidAmountMinor != 0 ||
		order.RefundedAmountMinor != order.ExpectedAmountMinor || order.SettledAt != 0 ||
		subscription.Status != common.TopUpStatusRefunded || subscription.PaymentProvider != providerEvent.Provider ||
		subscription.PaymentMethod != providerEvent.PaymentMethod || subscription.CompleteTime <= 0 {
		return nil, fmt.Errorf("%w: completed legacy subscription order contract is inconsistent", ErrPaymentAuditConflict)
	}
	if order.ProviderOrderKey == nil || *order.ProviderOrderKey != providerEvent.ProviderOrderKey ||
		(providerEvent.ProviderPaymentKey == "" && order.ProviderPaymentKey != nil) ||
		(providerEvent.ProviderPaymentKey != "" && (order.ProviderPaymentKey == nil || *order.ProviderPaymentKey != providerEvent.ProviderPaymentKey)) {
		return nil, fmt.Errorf("%w: completed legacy subscription provider identity is inconsistent", ErrPaymentAuditConflict)
	}
	pricingSnapshot, err := legacySubscriptionResolutionPricingSnapshot(input, providerEvent, &subscription)
	if err != nil || order.PricingSnapshot != pricingSnapshot {
		return nil, fmt.Errorf("%w: completed legacy subscription pricing evidence is inconsistent", ErrPaymentAuditConflict)
	}
	if err := validateLegacyTopUpLedgerEntryTx(tx, order.ID, providerEvent.ID, PaymentLedgerEntryLegacyPaymentReceived,
		order.ExpectedAmountMinor, 0); err != nil {
		return nil, err
	}
	if err := validateLegacyTopUpLedgerEntryTx(tx, order.ID, adminEvent.ID, PaymentLedgerEntryAdminExternalRefund,
		order.ExpectedAmountMinor, 0); err != nil {
		return nil, err
	}
	var entitlementCount int64
	if err := tx.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?",
		order.ID, PaymentLedgerEntrySubscriptionGranted).Count(&entitlementCount).Error; err != nil {
		return nil, err
	}
	var subscriptionCount int64
	if err := tx.Model(&UserSubscription{}).Where("payment_order_id = ?", order.ID).Count(&subscriptionCount).Error; err != nil {
		return nil, err
	}
	if entitlementCount != 0 || subscriptionCount != 0 {
		return nil, fmt.Errorf("%w: legacy subscription refund created an entitlement", ErrPaymentAuditConflict)
	}
	metadataBytes, err := common.Marshal(legacySubscriptionResolutionAuditMetadata(input, providerEvent, &adminEvent, &order, &subscription))
	if err != nil {
		return nil, err
	}
	var audits []PaymentOperationsAudit
	if err := tx.Where("action = ? AND subject_id = ? AND expected_version = ?", PaymentOperationsActionLegacySubscriptionRefund,
		providerEvent.ID, int64(input.ExpectedEventAttempts)).Find(&audits).Error; err != nil {
		return nil, err
	}
	if len(audits) != 1 {
		return nil, fmt.Errorf("%w: legacy subscription operations audit is missing or duplicated", ErrPaymentAuditConflict)
	}
	audit := audits[0]
	if audit.AdminID != input.AdminID || audit.ActorIP != input.ActorIP || audit.PaymentOrderID != order.ID ||
		audit.UserID != order.UserID || audit.Provider != providerEvent.Provider || audit.Reason != input.Reason ||
		audit.Metadata != string(metadataBytes) {
		return nil, fmt.Errorf("%w: legacy subscription operations audit payload changed", ErrPaymentAuditConflict)
	}
	return &order, nil
}

func legacySubscriptionResolutionPayload(input PaymentLegacySubscriptionResolutionInput, event *PaymentEvent) (string, error) {
	encoded, err := common.Marshal(map[string]interface{}{
		"source": "admin", "action": PaymentUnmatchedAvailableActionResolveLegacySubscription,
		"resolution": input.Resolution, "admin_id": input.AdminID, "actor_ip": input.ActorIP,
		"reason": input.Reason, "event_id": input.EventID, "expected_event_attempts": input.ExpectedEventAttempts,
		"provider_refund_reference": input.ProviderRefundReference,
		"provider":                  event.Provider, "provider_event_key": event.EventKey,
		"provider_payload_digest": event.PayloadDigest, "trade_no": event.TradeNo,
		"provider_order_key": event.ProviderOrderKey, "provider_payment_key": event.ProviderPaymentKey,
		"provider_resource_key": event.ProviderResourceKey, "provider_customer_id": event.CustomerID,
		"provider_livemode": event.ProviderLivemode, "provider_credential_generation": event.ProviderCredentialGeneration,
	})
	return string(encoded), err
}

func legacySubscriptionResolutionPricingSnapshot(input PaymentLegacySubscriptionResolutionInput, event *PaymentEvent,
	subscription *SubscriptionOrder) (string, error) {
	encoded, err := common.Marshal(map[string]interface{}{
		"version": 1, "source": "admin_legacy_subscription_resolution", "resolution": input.Resolution,
		"admin_id": input.AdminID, "reason": input.Reason, "entitlement_granted": false,
		"historical_subscription_snapshot_available": false,
		"legacy_subscription_id":                     subscription.Id, "legacy_plan_id": subscription.PlanId, "legacy_money": subscription.Money,
		"paid_amount_minor": event.PaidAmountMinor, "currency": event.Currency, "payment_method": event.PaymentMethod,
		"provider_event_id": event.ID, "provider_event_key": event.EventKey, "provider_payload_digest": event.PayloadDigest,
		"provider_order_key": event.ProviderOrderKey, "provider_payment_key": event.ProviderPaymentKey,
		"provider_resource_key": event.ProviderResourceKey, "provider_customer_id": event.CustomerID,
		"provider_livemode":              event.ProviderLivemode,
		"provider_credential_generation": event.ProviderCredentialGeneration,
		"provider_refund_reference":      input.ProviderRefundReference,
	})
	return string(encoded), err
}

func legacySubscriptionResolutionAuditMetadata(input PaymentLegacySubscriptionResolutionInput,
	providerEvent, adminEvent *PaymentEvent, order *PaymentOrder, subscription *SubscriptionOrder) map[string]interface{} {
	return map[string]interface{}{
		"admin_event_id": adminEvent.ID, "admin_event_key": adminEvent.EventKey,
		"provider_event_id": providerEvent.ID, "provider_event_key": providerEvent.EventKey,
		"provider_payload_digest": providerEvent.PayloadDigest, "provider_event_attempts": providerEvent.Attempts,
		"provider_order_key": providerEvent.ProviderOrderKey, "provider_payment_key": providerEvent.ProviderPaymentKey,
		"provider_resource_key": providerEvent.ProviderResourceKey, "provider_customer_id": providerEvent.CustomerID,
		"provider_livemode":              providerEvent.ProviderLivemode,
		"provider_credential_generation": providerEvent.ProviderCredentialGeneration,
		"trade_no":                       providerEvent.TradeNo, "payment_order_id": order.ID, "payment_order_status": order.Status,
		"legacy_subscription_id": subscription.Id, "legacy_plan_id": subscription.PlanId, "legacy_money": subscription.Money,
		"resolution": input.Resolution, "provider_refund_reference": input.ProviderRefundReference,
		"expected_event_attempts":                    input.ExpectedEventAttempts,
		"historical_subscription_snapshot_available": false, "entitlement_granted": false,
	}
}

func legacySubscriptionResolutionAdminEventKey(input PaymentLegacySubscriptionResolutionInput, event *PaymentEvent) string {
	if input.Resolution == PaymentLegacyTopUpResolutionExternalRefund {
		provider := ""
		if event != nil {
			provider = event.Provider
		}
		return paymentExternalRefundEventKey(provider, input.ProviderRefundReference)
	}
	return "legacy_subscription_external_refund:" + strconv.FormatInt(input.EventID, 10) +
		":a" + strconv.Itoa(input.ExpectedEventAttempts)
}
