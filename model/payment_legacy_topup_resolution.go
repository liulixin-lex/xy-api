package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	PaymentReviewCodeEventKeyPayloadConflict         = "event_key_payload_conflict"
	PaymentReviewCodeLegacyTopUpQuotaSnapshotMissing = "legacy_topup_quota_snapshot_missing"

	PaymentLegacyTopUpResolutionFulfill        = "fulfill"
	PaymentLegacyTopUpResolutionExternalRefund = "external_refund"

	PaymentUnmatchedAvailableActionResolveLegacyTopUp = "resolve_legacy_topup"

	PaymentOperationsActionLegacyTopUpFulfill = "payment.legacy_topup_fulfilled"
	PaymentOperationsActionLegacyTopUpRefund  = "payment.legacy_topup_external_refund_confirmed"

	PaymentLedgerEntryLegacyPaymentReceived = "legacy_payment_received"
	PaymentLedgerEntryAdminLegacyTopUp      = "admin_legacy_topup_fulfill"
)

type PaymentLegacyTopUpResolutionInput struct {
	EventID                 int64
	ExpectedEventAttempts   int
	AdminID                 int
	ActorIP                 string
	Resolution              string
	CreditQuota             int64
	ProviderRefundReference string
	Reason                  string
}

func ResolveLegacyTopUpPaymentEventByAdmin(input PaymentLegacyTopUpResolutionInput) (*PaymentUnmatchedEventActionResult, error) {
	input.Resolution = strings.ToLower(strings.TrimSpace(input.Resolution))
	input.ActorIP = strings.TrimSpace(input.ActorIP)
	input.ProviderRefundReference = strings.TrimSpace(input.ProviderRefundReference)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.EventID <= 0 || input.ExpectedEventAttempts <= 0 || input.AdminID <= 0 || input.ActorIP == "" ||
		len(input.ActorIP) > 64 || len(input.Reason) < 8 || len(input.Reason) > 512 {
		return nil, ErrPaymentAuditInvalid
	}
	switch input.Resolution {
	case PaymentLegacyTopUpResolutionFulfill:
		if input.CreditQuota <= 0 || input.CreditQuota > math.MaxInt32 || input.ProviderRefundReference != "" {
			return nil, ErrPaymentAuditInvalid
		}
	case PaymentLegacyTopUpResolutionExternalRefund:
		if input.CreditQuota != 0 || !validPaymentProviderReference(input.ProviderRefundReference) {
			return nil, ErrPaymentAuditInvalid
		}
	default:
		return nil, ErrPaymentAuditInvalid
	}

	result := &PaymentUnmatchedEventActionResult{}
	settlement := &PaymentSettlementResult{}
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

		payload, err := legacyTopUpResolutionPayload(input, &providerEvent)
		if err != nil {
			return err
		}
		adminEventKey := legacyTopUpResolutionAdminEventKey(input)
		duplicate, err := processedAdminActionRetryTx(tx, adminEventKey, PaymentPayloadDigest(payload))
		if err != nil {
			return err
		}
		if duplicate {
			order, err := validateCompletedLegacyTopUpResolutionTx(tx, input, &providerEvent, adminEventKey, payload)
			if err != nil {
				return err
			}
			result.Event = &providerEvent
			result.Order = order
			result.Duplicate = true
			settlement.Order = order
			settlement.UserID = order.UserID
			settlement.Duplicate = true
			return nil
		}

		if providerEvent.Status != PaymentEventStatusManualReview || providerEvent.PaymentOrderID != 0 ||
			providerEvent.Attempts != input.ExpectedEventAttempts {
			return fmt.Errorf("%w: legacy top-up event state changed", ErrPaymentAuditConflict)
		}
		legacyTopUp, err := lockLegacyTopUpResolutionContractTx(tx, &providerEvent)
		if err != nil {
			return err
		}
		if input.Resolution == PaymentLegacyTopUpResolutionFulfill {
			available, err := paymentCredentialGenerationAvailableTx(tx, PaymentProviderEpay,
				providerEvent.ProviderCredentialGeneration, legacyTopUp.CreateTime, common.GetTimestamp())
			if err != nil {
				return err
			}
			if !available {
				return fmt.Errorf("%w: verified Epay credential generation is no longer available", ErrPaymentAuditConflict)
			}
		}

		var affiliateCount int64
		if err := tx.Model(&AffiliateRewardRecord{}).Where("top_up_id = ?", legacyTopUp.Id).Count(&affiliateCount).Error; err != nil {
			return err
		}
		if affiliateCount != 0 {
			return fmt.Errorf("%w: legacy top-up already has affiliate reward effects", ErrPaymentAuditConflict)
		}

		order, err := createLegacyTopUpResolutionOrderTx(tx, input, &providerEvent, legacyTopUp)
		if err != nil {
			return err
		}
		result.Order = order
		settlement.Order = order
		settlement.UserID = order.UserID

		adminEvent, err := createAdminPaymentEventTx(tx, adminEventKey, legacyTopUpResolutionAuditAction(input.Resolution), payload, order)
		if err != nil {
			return err
		}
		claimed := tx.Model(&PaymentEvent{}).
			Where("id = ? AND status = ? AND payment_order_id = ? AND attempts = ?", providerEvent.ID,
				PaymentEventStatusManualReview, 0, input.ExpectedEventAttempts).
			Updates(map[string]interface{}{
				"status": PaymentEventStatusProcessing, "review_code": "", "last_error": "",
				"attempts": input.ExpectedEventAttempts + 1, "updated_at": common.GetTimestamp(),
			})
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected != 1 {
			return fmt.Errorf("%w: legacy top-up event state changed", ErrPaymentAuditConflict)
		}
		providerEvent.Status = PaymentEventStatusProcessing
		providerEvent.ReviewCode = ""
		providerEvent.LastError = ""
		providerEvent.Attempts = input.ExpectedEventAttempts + 1

		switch input.Resolution {
		case PaymentLegacyTopUpResolutionFulfill:
			if err := fulfillLegacyTopUpResolutionTx(tx, &providerEvent, adminEvent, order, legacyTopUp, settlement); err != nil {
				return err
			}
		case PaymentLegacyTopUpResolutionExternalRefund:
			if err := refundLegacyTopUpResolutionTx(tx, &providerEvent, adminEvent, order, legacyTopUp, input); err != nil {
				return err
			}
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
		result.Order = order

		metadata := legacyTopUpResolutionAuditMetadata(input, &providerEvent, adminEvent, order, legacyTopUp)
		if err := createPaymentOperationsAuditTx(tx, PaymentOperationsAudit{
			Action: legacyTopUpResolutionAuditAction(input.Resolution), AdminID: input.AdminID, ActorIP: input.ActorIP,
			PaymentOrderID: order.ID, UserID: order.UserID, SubjectID: providerEvent.ID, Provider: PaymentProviderEpay,
			ExpectedVersion: int64(input.ExpectedEventAttempts), Reason: input.Reason,
		}, metadata); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	applyAdminPaymentSettlementPostCommit(settlement)
	if input.Resolution == PaymentLegacyTopUpResolutionFulfill && !result.Duplicate {
		LogPaymentSettlement(settlement, PaymentProviderEpay, input.ActorIP)
	}
	return result, nil
}

func lockLegacyTopUpResolutionContractTx(tx *gorm.DB, event *PaymentEvent) (*TopUp, error) {
	if tx == nil || event == nil {
		return nil, ErrPaymentAuditInvalid
	}
	if event.ReviewCode != PaymentReviewCodeLegacyTopUpQuotaSnapshotMissing {
		return nil, fmt.Errorf("%w: payment event is not classified as a legacy top-up quota snapshot incident", ErrPaymentAuditConflict)
	}
	if err := validateLegacyEpayEventEvidence(event); err != nil {
		return nil, err
	}

	var existingOrder PaymentOrder
	orderQuery := lockForUpdate(tx).Where("trade_no = ?", event.TradeNo).Limit(1).Find(&existingOrder)
	if orderQuery.Error != nil {
		return nil, orderQuery.Error
	}
	if orderQuery.RowsAffected > 0 {
		return nil, fmt.Errorf("%w: legacy top-up already has a canonical order", ErrPaymentAuditConflict)
	}
	var subscription SubscriptionOrder
	subscriptionQuery := lockForUpdate(tx).Where("trade_no = ?", event.TradeNo).Limit(1).Find(&subscription)
	if subscriptionQuery.Error != nil {
		return nil, subscriptionQuery.Error
	}
	if subscriptionQuery.RowsAffected > 0 {
		return nil, fmt.Errorf("%w: legacy payment trade number is ambiguous", ErrPaymentAuditConflict)
	}

	var topUp TopUp
	if err := lockForUpdate(tx).Where("trade_no = ?", event.TradeNo).First(&topUp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentAuditNotFound
		}
		return nil, err
	}
	if err := validateLegacyTopUpResolutionEvidence(event, &topUp); err != nil {
		return nil, err
	}
	var user User
	if err := lockForUpdate(tx).Select("id").Where("id = ?", topUp.UserId).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: legacy top-up user is missing", ErrPaymentAuditConflict)
		}
		return nil, err
	}
	return &topUp, nil
}

func validateLegacyTopUpResolutionEvidence(event *PaymentEvent, topUp *TopUp) error {
	if event == nil || topUp == nil || topUp.PaymentOrderId != nil || topUp.UserId <= 0 || topUp.Amount <= 0 ||
		topUp.CreateTime <= 0 || topUp.Status != common.TopUpStatusPending {
		return fmt.Errorf("%w: legacy top-up projection is not safely resolvable", ErrPaymentAuditConflict)
	}
	if event.ReviewCode == PaymentReviewCodeEventKeyPayloadConflict {
		return fmt.Errorf("%w: conflicting provider payload evidence cannot be resolved", ErrPaymentAuditConflict)
	}
	if err := validateLegacyPaymentProviderAndMethod(topUp.PaymentProvider, topUp.PaymentMethod,
		paymentEventInputFromStoredEvent(event)); err != nil {
		return fmt.Errorf("%w: legacy top-up provider contract changed", ErrPaymentAuditConflict)
	}
	exponent, ok := common.PaymentProviderCurrencyExponentOK(PaymentProviderEpay, event.Currency)
	if !ok || event.Currency != "CNY" {
		return fmt.Errorf("%w: legacy top-up currency is invalid", ErrPaymentAuditConflict)
	}
	expectedMinor, err := legacyPaymentMoneyMinorExact(topUp.Money, exponent, math.MaxInt32)
	if err != nil || expectedMinor != event.PaidAmountMinor {
		return fmt.Errorf("%w: legacy top-up paid amount no longer matches", ErrPaymentAuditConflict)
	}
	return nil
}

func createLegacyTopUpResolutionOrderTx(tx *gorm.DB, input PaymentLegacyTopUpResolutionInput,
	event *PaymentEvent, topUp *TopUp) (*PaymentOrder, error) {
	if tx == nil || event == nil || topUp == nil {
		return nil, ErrPaymentAuditInvalid
	}
	pricingSnapshot, err := legacyTopUpResolutionPricingSnapshot(input, event, topUp)
	if err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	order := &PaymentOrder{
		TradeNo: event.TradeNo, UserID: topUp.UserId, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderEpay, PaymentMethod: event.PaymentMethod,
		ProviderCredentialGeneration: event.ProviderCredentialGeneration,
		RequestID:                    legacyPaymentRequestID(PaymentProviderEpay, event.TradeNo),
		ExpectedAmountMinor:          event.PaidAmountMinor, Currency: event.Currency,
		RequestedAmount: topUp.Amount, CreditQuota: input.CreditQuota, PricingSnapshot: pricingSnapshot,
		LegacyRecordType: PaymentOrderKindTopUp, LegacyRecordID: topUp.Id,
		Status: PaymentOrderStatusPending, CreatedAt: topUp.CreateTime, UpdatedAt: now, Version: 1,
	}
	if err := setAdoptedProviderKeys(order, paymentEventInputFromStoredEvent(event)); err != nil {
		return nil, err
	}
	if err := tx.Create(order).Error; err != nil {
		return nil, err
	}
	topUp.PaymentOrderId = &order.ID
	topUp.PaymentProvider = PaymentProviderEpay
	return order, nil
}

func fulfillLegacyTopUpResolutionTx(tx *gorm.DB, providerEvent, adminEvent *PaymentEvent,
	order *PaymentOrder, topUp *TopUp, result *PaymentSettlementResult) error {
	if tx == nil || providerEvent == nil || adminEvent == nil || order == nil || topUp == nil || result == nil ||
		order.CreditQuota <= 0 || order.CreditQuota > math.MaxInt32 {
		return ErrPaymentAuditInvalid
	}
	quota := int(order.CreditQuota)
	if err := applyUserQuotaDeltaTx(tx, order.UserID, quota); err != nil {
		return err
	}
	now := common.GetTimestamp()
	topUp.Status = common.TopUpStatusSuccess
	topUp.CompleteTime = now
	if err := tx.Save(topUp).Error; err != nil {
		return err
	}
	if err := createPaymentLedgerEntryTx(tx, providerEvent, order, PaymentLedgerEntryCredit,
		order.ExpectedAmountMinor, order.CreditQuota, "legacy top-up credit explicitly resolved by administrator; affiliate reward suppressed"); err != nil {
		return err
	}
	if err := createPaymentLedgerEntryTx(tx, adminEvent, order, PaymentLedgerEntryAdminLegacyTopUp,
		order.ExpectedAmountMinor, 0, "administrator resolved legacy top-up without historical quota snapshot"); err != nil {
		return err
	}
	updated := tx.Model(&PaymentOrder{}).
		Where("id = ? AND version = ? AND status = ?", order.ID, order.Version, PaymentOrderStatusPending).
		Updates(map[string]interface{}{
			"status": PaymentOrderStatusFulfilled, "status_reason": "", "paid_amount_minor": order.ExpectedAmountMinor,
			"settled_at": now, "updated_at": now, "version": order.Version + 1,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("%w: legacy top-up order changed", ErrPaymentAuditConflict)
	}
	order.Status = PaymentOrderStatusFulfilled
	order.PaidAmountMinor = order.ExpectedAmountMinor
	order.SettledAt = now
	order.UpdatedAt = now
	order.Version++
	result.Order = order
	result.UserID = order.UserID
	result.QuotaDelta = quota
	result.UserCacheChanged = true
	return nil
}

func refundLegacyTopUpResolutionTx(tx *gorm.DB, providerEvent, adminEvent *PaymentEvent,
	order *PaymentOrder, topUp *TopUp, input PaymentLegacyTopUpResolutionInput) error {
	if tx == nil || providerEvent == nil || adminEvent == nil || order == nil || topUp == nil ||
		input.ProviderRefundReference == "" || input.CreditQuota != 0 {
		return ErrPaymentAuditInvalid
	}
	if err := createPaymentLedgerEntryTx(tx, providerEvent, order, PaymentLedgerEntryLegacyPaymentReceived,
		order.ExpectedAmountMinor, 0, "verified legacy payment received without entitlement grant"); err != nil {
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
		return fmt.Errorf("%w: legacy top-up order changed", ErrPaymentAuditConflict)
	}
	topUp.Status = common.TopUpStatusRefunded
	topUp.CompleteTime = now
	if err := tx.Save(topUp).Error; err != nil {
		return err
	}
	order.Status = PaymentOrderStatusRefunded
	order.StatusReason = input.Reason
	order.RefundedAmountMinor = order.ExpectedAmountMinor
	order.UpdatedAt = now
	order.Version++
	return createPaymentLedgerEntryTx(tx, adminEvent, order, PaymentLedgerEntryAdminExternalRefund,
		order.ExpectedAmountMinor, 0, "external refund confirmed for legacy top-up by administrator")
}

func validateCompletedLegacyTopUpResolutionTx(tx *gorm.DB, input PaymentLegacyTopUpResolutionInput,
	providerEvent *PaymentEvent, adminEventKey, payload string) (*PaymentOrder, error) {
	if tx == nil || providerEvent == nil || providerEvent.Status != PaymentEventStatusProcessed ||
		providerEvent.PaymentOrderID <= 0 || providerEvent.Attempts != input.ExpectedEventAttempts+1 {
		return nil, fmt.Errorf("%w: completed legacy top-up resolution is inconsistent", ErrPaymentAuditConflict)
	}
	var adminEvent PaymentEvent
	if err := lockForUpdate(tx).Where("provider = ? AND event_key = ?", "admin", adminEventKey).First(&adminEvent).Error; err != nil {
		return nil, err
	}
	if adminEvent.Status != PaymentEventStatusProcessed || adminEvent.PaymentOrderID != providerEvent.PaymentOrderID ||
		adminEvent.PayloadDigest != PaymentPayloadDigest(payload) {
		return nil, fmt.Errorf("%w: completed legacy top-up administrator event is inconsistent", ErrPaymentAuditConflict)
	}
	var order PaymentOrder
	if err := lockForUpdate(tx).First(&order, providerEvent.PaymentOrderID).Error; err != nil {
		return nil, err
	}
	var topUp TopUp
	if err := lockForUpdate(tx).Where("payment_order_id = ? AND trade_no = ?", order.ID, providerEvent.TradeNo).First(&topUp).Error; err != nil {
		return nil, err
	}
	if order.Provider != PaymentProviderEpay || order.OrderKind != PaymentOrderKindTopUp || order.TradeNo != providerEvent.TradeNo ||
		order.UserID != topUp.UserId || order.LegacyRecordType != PaymentOrderKindTopUp || order.LegacyRecordID != topUp.Id ||
		order.ProviderCredentialGeneration != providerEvent.ProviderCredentialGeneration ||
		order.ExpectedAmountMinor != providerEvent.PaidAmountMinor || order.Currency != providerEvent.Currency ||
		order.PaymentMethod != providerEvent.PaymentMethod || order.RequestID != legacyPaymentRequestID(PaymentProviderEpay, providerEvent.TradeNo) {
		return nil, fmt.Errorf("%w: completed legacy top-up order contract is inconsistent", ErrPaymentAuditConflict)
	}
	if order.ProviderOrderKey == nil || *order.ProviderOrderKey != providerEvent.ProviderOrderKey ||
		providerEvent.ProviderPaymentKey != "" && (order.ProviderPaymentKey == nil || *order.ProviderPaymentKey != providerEvent.ProviderPaymentKey) {
		return nil, fmt.Errorf("%w: completed legacy top-up provider identity is inconsistent", ErrPaymentAuditConflict)
	}
	pricingSnapshot, err := legacyTopUpResolutionPricingSnapshot(input, providerEvent, &topUp)
	if err != nil || order.PricingSnapshot != pricingSnapshot {
		return nil, fmt.Errorf("%w: completed legacy top-up pricing evidence is inconsistent", ErrPaymentAuditConflict)
	}

	var affiliateCount int64
	if err := tx.Model(&AffiliateRewardRecord{}).Where("top_up_id = ?", topUp.Id).Count(&affiliateCount).Error; err != nil {
		return nil, err
	}
	if affiliateCount != 0 {
		return nil, fmt.Errorf("%w: legacy top-up resolution created an affiliate reward", ErrPaymentAuditConflict)
	}

	switch input.Resolution {
	case PaymentLegacyTopUpResolutionFulfill:
		if order.Status != PaymentOrderStatusFulfilled || order.CreditQuota != input.CreditQuota ||
			order.PaidAmountMinor != order.ExpectedAmountMinor || order.SettledAt <= 0 ||
			order.RefundedAmountMinor != 0 || topUp.Status != common.TopUpStatusSuccess {
			return nil, fmt.Errorf("%w: fulfilled legacy top-up terminal state is inconsistent", ErrPaymentAuditConflict)
		}
		if err := validateLegacyTopUpLedgerEntryTx(tx, order.ID, providerEvent.ID, PaymentLedgerEntryCredit,
			order.ExpectedAmountMinor, input.CreditQuota); err != nil {
			return nil, err
		}
		if err := validateLegacyTopUpLedgerEntryTx(tx, order.ID, adminEvent.ID, PaymentLedgerEntryAdminLegacyTopUp,
			order.ExpectedAmountMinor, 0); err != nil {
			return nil, err
		}
	case PaymentLegacyTopUpResolutionExternalRefund:
		if order.Status != PaymentOrderStatusRefunded || order.CreditQuota != 0 || order.PaidAmountMinor != 0 ||
			order.RefundedAmountMinor != order.ExpectedAmountMinor || order.SettledAt != 0 ||
			topUp.Status != common.TopUpStatusRefunded || !adminEvent.Refunded ||
			adminEvent.RefundedAmountMinor != order.ExpectedAmountMinor {
			return nil, fmt.Errorf("%w: refunded legacy top-up terminal state is inconsistent", ErrPaymentAuditConflict)
		}
		if err := validateLegacyTopUpLedgerEntryTx(tx, order.ID, providerEvent.ID, PaymentLedgerEntryLegacyPaymentReceived,
			order.ExpectedAmountMinor, 0); err != nil {
			return nil, err
		}
		if err := validateLegacyTopUpLedgerEntryTx(tx, order.ID, adminEvent.ID, PaymentLedgerEntryAdminExternalRefund,
			order.ExpectedAmountMinor, 0); err != nil {
			return nil, err
		}
	default:
		return nil, ErrPaymentAuditInvalid
	}

	metadataBytes, err := common.Marshal(legacyTopUpResolutionAuditMetadata(input, providerEvent, &adminEvent, &order, &topUp))
	if err != nil {
		return nil, err
	}
	var audits []PaymentOperationsAudit
	if err := tx.Where("action = ? AND subject_id = ? AND expected_version = ?",
		legacyTopUpResolutionAuditAction(input.Resolution), providerEvent.ID, int64(input.ExpectedEventAttempts)).Find(&audits).Error; err != nil {
		return nil, err
	}
	if len(audits) != 1 {
		return nil, fmt.Errorf("%w: legacy top-up operations audit is missing or duplicated", ErrPaymentAuditConflict)
	}
	audit := audits[0]
	if audit.AdminID != input.AdminID || audit.ActorIP != input.ActorIP || audit.PaymentOrderID != order.ID ||
		audit.UserID != order.UserID || audit.Provider != PaymentProviderEpay || audit.Reason != input.Reason ||
		audit.Metadata != string(metadataBytes) {
		return nil, fmt.Errorf("%w: legacy top-up operations audit payload changed", ErrPaymentAuditConflict)
	}
	return &order, nil
}

func validateLegacyTopUpLedgerEntryTx(tx *gorm.DB, orderID, eventID int64, entryType string, amountMinor, quotaDelta int64) error {
	var entries []PaymentLedgerEntry
	if err := tx.Where("payment_order_id = ? AND payment_event_id = ? AND entry_type = ?", orderID, eventID, entryType).
		Find(&entries).Error; err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].AmountMinor != amountMinor || entries[0].QuotaDelta != quotaDelta {
		return fmt.Errorf("%w: legacy top-up ledger evidence is inconsistent", ErrPaymentAuditConflict)
	}
	return nil
}

func legacyTopUpResolutionPayload(input PaymentLegacyTopUpResolutionInput, event *PaymentEvent) (string, error) {
	if event == nil {
		return "", ErrPaymentAuditInvalid
	}
	encoded, err := common.Marshal(map[string]interface{}{
		"source": "admin", "action": PaymentUnmatchedAvailableActionResolveLegacyTopUp,
		"resolution": input.Resolution, "admin_id": input.AdminID, "actor_ip": input.ActorIP,
		"reason": input.Reason, "event_id": input.EventID, "expected_event_attempts": input.ExpectedEventAttempts,
		"credit_quota": input.CreditQuota, "provider_refund_reference": input.ProviderRefundReference,
		"provider": event.Provider, "provider_event_key": event.EventKey,
		"provider_payload_digest": event.PayloadDigest, "trade_no": event.TradeNo,
	})
	return string(encoded), err
}

func legacyTopUpResolutionPricingSnapshot(input PaymentLegacyTopUpResolutionInput, event *PaymentEvent, topUp *TopUp) (string, error) {
	if event == nil || topUp == nil {
		return "", ErrPaymentAuditInvalid
	}
	encoded, err := common.Marshal(map[string]interface{}{
		"version": 1, "source": "admin_legacy_topup_resolution", "resolution": input.Resolution,
		"admin_id": input.AdminID, "reason": input.Reason,
		"historical_quota_snapshot_available": false, "affiliate_reward_suppressed": true,
		"credit_quota": input.CreditQuota, "legacy_requested_amount": topUp.Amount, "legacy_money": topUp.Money,
		"paid_amount_minor": event.PaidAmountMinor, "currency": event.Currency, "payment_method": event.PaymentMethod,
		"provider_event_id": event.ID, "provider_event_key": event.EventKey, "provider_payload_digest": event.PayloadDigest,
		"provider_order_key": event.ProviderOrderKey, "provider_payment_key": event.ProviderPaymentKey,
		"provider_credential_generation": event.ProviderCredentialGeneration,
		"provider_refund_reference":      input.ProviderRefundReference,
	})
	return string(encoded), err
}

func legacyTopUpResolutionAuditMetadata(input PaymentLegacyTopUpResolutionInput, providerEvent, adminEvent *PaymentEvent,
	order *PaymentOrder, topUp *TopUp) map[string]interface{} {
	return map[string]interface{}{
		"admin_event_id": adminEvent.ID, "admin_event_key": adminEvent.EventKey,
		"provider_event_id": providerEvent.ID, "provider_event_key": providerEvent.EventKey,
		"provider_payload_digest": providerEvent.PayloadDigest, "provider_event_attempts": providerEvent.Attempts,
		"provider_order_key": providerEvent.ProviderOrderKey, "provider_payment_key": providerEvent.ProviderPaymentKey,
		"provider_credential_generation": providerEvent.ProviderCredentialGeneration,
		"trade_no":                       providerEvent.TradeNo, "payment_order_id": order.ID, "payment_order_status": order.Status,
		"legacy_topup_id": topUp.Id, "legacy_requested_amount": topUp.Amount, "legacy_money": topUp.Money,
		"resolution": input.Resolution, "credit_quota": input.CreditQuota,
		"provider_refund_reference":           input.ProviderRefundReference,
		"expected_event_attempts":             input.ExpectedEventAttempts,
		"historical_quota_snapshot_available": false, "affiliate_reward_suppressed": true,
	}
}

func validPaymentProviderReference(reference string) bool {
	if reference == "" || len(reference) > 255 || !utf8.ValidString(reference) {
		return false
	}
	for _, char := range reference {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func legacyTopUpResolutionAdminEventKey(input PaymentLegacyTopUpResolutionInput) string {
	if input.Resolution == PaymentLegacyTopUpResolutionExternalRefund {
		return paymentExternalRefundEventKey(PaymentProviderEpay, input.ProviderRefundReference)
	}
	return "legacy_topup_" + input.Resolution + ":" + strconv.FormatInt(input.EventID, 10) +
		":a" + strconv.Itoa(input.ExpectedEventAttempts)
}

func legacyTopUpResolutionAuditAction(resolution string) string {
	if resolution == PaymentLegacyTopUpResolutionExternalRefund {
		return PaymentOperationsActionLegacyTopUpRefund
	}
	return PaymentOperationsActionLegacyTopUpFulfill
}
