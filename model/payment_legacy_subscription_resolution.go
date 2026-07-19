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

		payload, err := legacySubscriptionResolutionPayload(input, &providerEvent)
		if err != nil {
			return err
		}
		adminEventKey := legacySubscriptionResolutionAdminEventKey(input)
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
		if providerEvent.Status != PaymentEventStatusManualReview || providerEvent.PaymentOrderID != 0 ||
			providerEvent.Attempts != input.ExpectedEventAttempts ||
			providerEvent.ReviewCode != PaymentReviewCodeLegacySubscriptionContractUnavailable {
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
		result.Order = order
		adminEvent, err := createAdminPaymentEventTx(tx, adminEventKey, PaymentOperationsActionLegacySubscriptionRefund, payload, order)
		if err != nil {
			return err
		}
		claimed := tx.Model(&PaymentEvent{}).
			Where("id = ? AND status = ? AND payment_order_id = ? AND attempts = ? AND review_code = ?", providerEvent.ID,
				PaymentEventStatusManualReview, 0, input.ExpectedEventAttempts, PaymentReviewCodeLegacySubscriptionContractUnavailable).
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
			PaymentOrderID: order.ID, UserID: order.UserID, SubjectID: providerEvent.ID, Provider: PaymentProviderEpay,
			ExpectedVersion: int64(input.ExpectedEventAttempts), Reason: input.Reason,
		}, metadata)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func lockLegacySubscriptionResolutionContractTx(tx *gorm.DB, event *PaymentEvent) (*SubscriptionOrder, error) {
	if tx == nil || event == nil || event.ReviewCode != PaymentReviewCodeLegacySubscriptionContractUnavailable {
		return nil, fmt.Errorf("%w: payment event is not classified as a legacy subscription contract incident", ErrPaymentAuditConflict)
	}
	if err := validateLegacyEpayEventEvidence(event); err != nil {
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
	if err := validateLegacySubscriptionRefundEvidence(event, &subscription); err != nil {
		return nil, err
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

func createLegacySubscriptionResolutionOrderTx(tx *gorm.DB, input PaymentLegacySubscriptionResolutionInput,
	event *PaymentEvent, subscription *SubscriptionOrder) (*PaymentOrder, error) {
	pricingSnapshot, err := legacySubscriptionResolutionPricingSnapshot(input, event, subscription)
	if err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: event.TradeNo, UserID: subscription.UserId, OrderKind: PaymentOrderKindSubscription,
		Provider: PaymentProviderEpay, PaymentMethod: event.PaymentMethod,
		ProviderCredentialGeneration: event.ProviderCredentialGeneration,
		RequestID:                    legacyPaymentRequestID(PaymentProviderEpay, event.TradeNo),
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
	subscription.PaymentProvider = PaymentProviderEpay
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
	if err := tx.Model(&PaymentEvent{}).Where("id = ?", adminEvent.ID).Updates(map[string]interface{}{
		"refunded": true, "refunded_amount_minor": order.ExpectedAmountMinor,
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
	var adminEvent PaymentEvent
	if err := lockForUpdate(tx).Where("provider = ? AND event_key = ?", "admin", adminEventKey).First(&adminEvent).Error; err != nil {
		return nil, err
	}
	if adminEvent.Status != PaymentEventStatusProcessed || adminEvent.PaymentOrderID != providerEvent.PaymentOrderID ||
		adminEvent.PayloadDigest != PaymentPayloadDigest(payload) || !adminEvent.Refunded ||
		adminEvent.RefundedAmountMinor != providerEvent.PaidAmountMinor {
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
	if order.Provider != PaymentProviderEpay || order.OrderKind != PaymentOrderKindSubscription ||
		order.TradeNo != providerEvent.TradeNo || order.UserID != subscription.UserId ||
		order.LegacyRecordType != PaymentOrderKindSubscription || order.LegacyRecordID != subscription.Id ||
		order.ProviderCredentialGeneration != providerEvent.ProviderCredentialGeneration ||
		order.ExpectedAmountMinor != providerEvent.PaidAmountMinor || order.Currency != providerEvent.Currency ||
		order.PaymentMethod != providerEvent.PaymentMethod || order.RequestedAmount != int64(subscription.PlanId) ||
		order.RequestID != legacyPaymentRequestID(PaymentProviderEpay, providerEvent.TradeNo) ||
		order.Status != PaymentOrderStatusRefunded || order.CreditQuota != 0 || order.PaidAmountMinor != 0 ||
		order.RefundedAmountMinor != order.ExpectedAmountMinor || order.SettledAt != 0 ||
		subscription.Status != common.TopUpStatusRefunded {
		return nil, fmt.Errorf("%w: completed legacy subscription order contract is inconsistent", ErrPaymentAuditConflict)
	}
	if order.ProviderOrderKey == nil || *order.ProviderOrderKey != providerEvent.ProviderOrderKey ||
		providerEvent.ProviderPaymentKey != "" && (order.ProviderPaymentKey == nil || *order.ProviderPaymentKey != providerEvent.ProviderPaymentKey) {
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
		audit.UserID != order.UserID || audit.Provider != PaymentProviderEpay || audit.Reason != input.Reason ||
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
		"provider_credential_generation": providerEvent.ProviderCredentialGeneration,
		"trade_no":                       providerEvent.TradeNo, "payment_order_id": order.ID, "payment_order_status": order.Status,
		"legacy_subscription_id": subscription.Id, "legacy_plan_id": subscription.PlanId, "legacy_money": subscription.Money,
		"resolution": input.Resolution, "provider_refund_reference": input.ProviderRefundReference,
		"expected_event_attempts":                    input.ExpectedEventAttempts,
		"historical_subscription_snapshot_available": false, "entitlement_granted": false,
	}
}

func legacySubscriptionResolutionAdminEventKey(input PaymentLegacySubscriptionResolutionInput) string {
	return "legacy_subscription_external_refund:" + strconv.FormatInt(input.EventID, 10) +
		":a" + strconv.Itoa(input.ExpectedEventAttempts)
}
