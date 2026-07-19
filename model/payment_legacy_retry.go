package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const paymentLegacyRetryAdminEventType = "payment.unmatched_legacy_retried"

func retryLegacyPaymentEventByAdmin(input PaymentUnmatchedEventActionInput) (*PaymentUnmatchedEventActionResult, error) {
	result := &PaymentUnmatchedEventActionResult{}
	var providerEvent PaymentEvent
	var adminEvent PaymentEvent
	var priorResultErr error
	var settlement *PaymentSettlementResult
	var settlementErr error
	adminActionComplete := false
	resumeFinalization := false
	payload := ""

	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, err := lockPaymentConfigurationFenceTx(tx); err != nil {
			return err
		}
		if err := lockForUpdate(tx).First(&providerEvent, input.EventID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentAuditNotFound
			}
			return err
		}
		result.Event = &providerEvent

		var err error
		payload, err = legacyPaymentRetryAdminPayload(input, &providerEvent)
		if err != nil {
			return err
		}
		eventKey := legacyPaymentRetryAdminEventKey(input.EventID, input.ExpectedEventAttempts)
		claimed, created, err := UpsertPaymentEvent(tx, &PaymentEvent{
			Provider: "admin", EventKey: eventKey, EventType: paymentLegacyRetryAdminEventType,
			TradeNo: providerEvent.TradeNo, PayloadDigest: PaymentPayloadDigest(payload), NormalizedPayload: payload,
		})
		if err != nil {
			return err
		}
		adminEvent = *claimed
		if !created {
			if adminEvent.PayloadDigest != PaymentPayloadDigest(payload) {
				return fmt.Errorf("%w: legacy retry administrator action payload changed", ErrPaymentAuditConflict)
			}
			if adminEvent.Status == PaymentEventStatusProcessed {
				result.Duplicate = true
				adminActionComplete = true
				if adminEvent.PaymentOrderID > 0 {
					var order PaymentOrder
					if err := tx.First(&order, adminEvent.PaymentOrderID).Error; err != nil {
						return err
					}
					result.Order = &order
				}
				if strings.TrimSpace(adminEvent.LastError) != "" {
					priorResultErr = fmt.Errorf("%w: %s", ErrPaymentManualReview, adminEvent.LastError)
				}
				return nil
			}
			if adminEvent.Status != PaymentEventStatusReceived && adminEvent.Status != PaymentEventStatusProcessing &&
				adminEvent.Status != PaymentEventStatusFailed {
				return fmt.Errorf("%w: legacy retry administrator action is not retryable", ErrPaymentAuditConflict)
			}
			if providerEvent.Attempts == input.ExpectedEventAttempts+1 {
				switch providerEvent.Status {
				case PaymentEventStatusProcessed:
					order, err := validateCompletedLegacyPaymentRetryTx(tx, &providerEvent, input.ExpectedEventAttempts)
					if err != nil {
						return err
					}
					result.Order = order
					result.Duplicate = true
					settlement = &PaymentSettlementResult{Order: order, Duplicate: true, UserID: order.UserID}
					resumeFinalization = true
				case PaymentEventStatusManualReview:
					if err := validateLegacyEpayEventEvidence(&providerEvent); err != nil {
						return err
					}
					var order *PaymentOrder
					if providerEvent.PaymentOrderID > 0 {
						var storedOrder PaymentOrder
						if err := tx.First(&storedOrder, providerEvent.PaymentOrderID).Error; err != nil {
							return err
						}
						if storedOrder.Provider != PaymentProviderEpay || storedOrder.TradeNo != providerEvent.TradeNo ||
							storedOrder.Status != PaymentOrderStatusManualReview {
							return fmt.Errorf("%w: linked manual-review order is inconsistent", ErrPaymentAuditConflict)
						}
						order = &storedOrder
						result.Order = order
					}
					result.Duplicate = true
					settlement = &PaymentSettlementResult{Order: order, Duplicate: true, ManualReview: true}
					settlementErr = fmt.Errorf("%w: %s", ErrPaymentManualReview,
						legacyPaymentRetryStoredManualReason(&providerEvent))
					resumeFinalization = true
				}
			}
		}

		if !resumeFinalization {
			if err := validateRetryableLegacyEpayEvent(tx, &providerEvent, input.ExpectedEventAttempts); err != nil {
				return err
			}
		}
		now := common.GetTimestamp()
		if err := tx.Model(&PaymentEvent{}).Where("id = ?", adminEvent.ID).Updates(map[string]interface{}{
			"status": PaymentEventStatusProcessing, "last_error": "",
			"attempts": gorm.Expr("attempts + ?", 1), "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if adminActionComplete {
		return result, priorResultErr
	}

	if !resumeFinalization {
		expectedAttempts := input.ExpectedEventAttempts
		settlement, settlementErr = processPaymentEventWithReplayAttempts(
			paymentEventInputFromStoredEvent(&providerEvent), providerEvent.ID, &expectedAttempts,
		)
		if settlement != nil {
			result.Order = settlement.Order
		}
	}

	finalizeErr := DB.Transaction(func(tx *gorm.DB) error {
		var storedAdminEvent PaymentEvent
		if err := lockForUpdate(tx).First(&storedAdminEvent, adminEvent.ID).Error; err != nil {
			return err
		}
		if storedAdminEvent.PayloadDigest != PaymentPayloadDigest(payload) {
			return fmt.Errorf("%w: legacy retry audit payload changed", ErrPaymentAuditConflict)
		}
		var storedProviderEvent PaymentEvent
		if err := lockForUpdate(tx).First(&storedProviderEvent, providerEvent.ID).Error; err != nil {
			return err
		}
		result.Event = &storedProviderEvent

		if settlementErr != nil && !errors.Is(settlementErr, ErrPaymentManualReview) {
			return finishPaymentEventTx(tx, storedAdminEvent.ID, PaymentEventStatusFailed,
				legacyPaymentRetryResultMessage(settlementErr), 0)
		}

		orderID := int64(0)
		if settlement != nil && settlement.Order != nil {
			orderID = settlement.Order.ID
			var storedOrder PaymentOrder
			if err := lockForUpdate(tx).First(&storedOrder, orderID).Error; err != nil {
				return err
			}
			result.Order = &storedOrder
			if settlementErr == nil && storedOrder.Status == PaymentOrderStatusFulfilled {
				if err := createPaymentLedgerEntryTx(tx, &storedAdminEvent, &storedOrder,
					PaymentLedgerEntryAdminLegacyRetry, storedOrder.ExpectedAmountMinor, 0,
					"administrator retried a verified legacy Epay event"); err != nil {
					return err
				}
			}
		}
		if settlementErr == nil && (result.Order == nil || result.Order.Status != PaymentOrderStatusFulfilled) {
			return fmt.Errorf("%w: legacy retry did not produce a fulfilled payment order", ErrPaymentAuditConflict)
		}
		lastError := ""
		outcome := "fulfilled"
		if settlementErr != nil {
			lastError = legacyPaymentRetryResultMessage(settlementErr)
			outcome = PaymentEventStatusManualReview
		}
		if err := createLegacyPaymentRetryOperationsAuditTx(tx, input, &storedAdminEvent,
			&storedProviderEvent, result.Order, outcome); err != nil {
			return err
		}
		return finishPaymentEventTx(tx, storedAdminEvent.ID, PaymentEventStatusProcessed, lastError, orderID)
	})
	if finalizeErr != nil {
		return nil, finalizeErr
	}
	if settlementErr != nil {
		return result, settlementErr
	}
	return result, nil
}

func validateRetryableLegacyEpayEvent(tx *gorm.DB, event *PaymentEvent, expectedAttempts int) error {
	if tx == nil || event == nil || expectedAttempts <= 0 || event.Status != PaymentEventStatusManualReview ||
		event.PaymentOrderID != 0 || event.Attempts != expectedAttempts {
		return fmt.Errorf("%w: payment event state changed", ErrPaymentAuditConflict)
	}
	if event.ReviewCode != "" && event.ReviewCode != PaymentReviewCodeLegacySubscriptionContractUnavailable {
		return fmt.Errorf("%w: payment event requires its classified terminal resolution", ErrPaymentAuditConflict)
	}
	if err := validateLegacyEpayEventEvidence(event); err != nil {
		return err
	}
	var subscription SubscriptionOrder
	if err := lockForUpdate(tx).Where("trade_no = ?", event.TradeNo).First(&subscription).Error; err == nil {
		var currentPlan *SubscriptionPlan
		if legacySubscriptionNeedsPlanReconstruction(&subscription) {
			var plan SubscriptionPlan
			if err := lockForUpdate(tx).Where("id = ?", subscription.PlanId).First(&plan).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			} else {
				currentPlan = &plan
			}
		}
		plan, err := prepareLegacySubscriptionAdoptionContract(paymentEventInputFromStoredEvent(event), &subscription, currentPlan)
		if err != nil {
			return fmt.Errorf("%w: legacy subscription contract is not retryable: %v", ErrPaymentAuditConflict, err)
		}
		available, err := paymentCredentialGenerationAvailableTx(tx, event.Provider, event.ProviderCredentialGeneration,
			subscription.CreateTime, common.GetTimestamp())
		if err != nil {
			return err
		}
		if !available {
			return fmt.Errorf("%w: legacy subscription credential generation is unavailable", ErrPaymentAuditConflict)
		}
		if plan.MaxPurchasePerUser > 0 {
			usedSlots, err := countSubscriptionPurchaseSlotsTx(tx, subscription.UserId, plan.Id, subscription.Id, 0)
			if err != nil {
				return err
			}
			if usedSlots >= int64(plan.MaxPurchasePerUser) {
				return fmt.Errorf("%w: legacy subscription purchase limit is currently exhausted", ErrPaymentAuditConflict)
			}
		}
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var topUp TopUp
	if err := lockForUpdate(tx).Where("trade_no = ?", event.TradeNo).First(&topUp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: legacy payment projection is missing", ErrPaymentAuditConflict)
		}
		return err
	}
	if err := validateLegacyPaymentProviderAndMethod(topUp.PaymentProvider, topUp.PaymentMethod,
		paymentEventInputFromStoredEvent(event)); err != nil {
		return fmt.Errorf("%w: legacy top-up provider contract is not retryable", ErrPaymentAuditConflict)
	}
	return nil
}

func validateLegacyEpayEventEvidence(event *PaymentEvent) error {
	if event == nil || event.Provider != PaymentProviderEpay || !event.Paid || event.PaidAmountMinor <= 0 ||
		event.Currency != "CNY" || strings.TrimSpace(event.PaymentMethod) == "" ||
		event.ProviderCredentialGeneration <= 0 || strings.TrimSpace(event.ProviderOrderKey) == "" ||
		strings.TrimSpace(event.TradeNo) == "" || strings.TrimSpace(event.NormalizedPayload) == "" ||
		event.PayloadDigest != PaymentPayloadDigest(event.NormalizedPayload) || event.Failed || event.Expired ||
		event.Refunded || event.Disputed || event.DisputeResolved || event.ManualReview {
		return fmt.Errorf("%w: event is not an intact verified legacy Epay paid event", ErrPaymentAuditConflict)
	}
	input := paymentEventInputFromStoredEvent(event)
	if err := input.validate(); err != nil {
		return fmt.Errorf("%w: stored event is invalid", ErrPaymentAuditConflict)
	}
	return nil
}

func validateCompletedLegacyPaymentRetryTx(tx *gorm.DB, event *PaymentEvent, expectedAttempts int) (*PaymentOrder, error) {
	if tx == nil || event == nil || event.Status != PaymentEventStatusProcessed ||
		event.Attempts != expectedAttempts+1 || event.PaymentOrderID <= 0 {
		return nil, fmt.Errorf("%w: completed legacy retry state is inconsistent", ErrPaymentAuditConflict)
	}
	if err := validateLegacyEpayEventEvidence(event); err != nil {
		return nil, err
	}
	var order PaymentOrder
	if err := lockForUpdate(tx).First(&order, event.PaymentOrderID).Error; err != nil {
		return nil, err
	}
	if order.Provider != PaymentProviderEpay || order.TradeNo != event.TradeNo ||
		order.ProviderCredentialGeneration != event.ProviderCredentialGeneration ||
		order.ExpectedAmountMinor != event.PaidAmountMinor || order.PaidAmountMinor != event.PaidAmountMinor ||
		order.Currency != event.Currency || order.PaymentMethod != event.PaymentMethod ||
		order.Status != PaymentOrderStatusFulfilled || order.SettledAt <= 0 {
		return nil, fmt.Errorf("%w: completed legacy retry order contract is inconsistent", ErrPaymentAuditConflict)
	}
	if order.ProviderOrderKey == nil || *order.ProviderOrderKey != event.ProviderOrderKey {
		return nil, fmt.Errorf("%w: completed legacy retry provider order identity is inconsistent", ErrPaymentAuditConflict)
	}
	if event.ProviderPaymentKey != "" && (order.ProviderPaymentKey == nil || *order.ProviderPaymentKey != event.ProviderPaymentKey) {
		return nil, fmt.Errorf("%w: completed legacy retry provider payment identity is inconsistent", ErrPaymentAuditConflict)
	}
	granted, err := paymentEntitlementGrantedTx(tx, &order)
	if err != nil {
		return nil, err
	}
	if !granted {
		return nil, fmt.Errorf("%w: completed legacy retry has no entitlement evidence", ErrPaymentAuditConflict)
	}
	entryType := PaymentLedgerEntryCredit
	if order.OrderKind == PaymentOrderKindSubscription {
		entryType = PaymentLedgerEntrySubscriptionGranted
	}
	var ledger PaymentLedgerEntry
	if err := tx.Where("payment_order_id = ? AND payment_event_id = ? AND entry_type = ?",
		order.ID, event.ID, entryType).First(&ledger).Error; err != nil {
		return nil, err
	}
	if ledger.AmountMinor != order.ExpectedAmountMinor || ledger.Currency != order.Currency || ledger.UserID != order.UserID {
		return nil, fmt.Errorf("%w: completed legacy retry ledger is inconsistent", ErrPaymentAuditConflict)
	}
	return &order, nil
}

func paymentEventInputFromStoredEvent(event *PaymentEvent) PaymentEventInput {
	if event == nil {
		return PaymentEventInput{}
	}
	return PaymentEventInput{
		Provider: event.Provider, EventKey: event.EventKey, EventType: event.EventType,
		TradeNo: event.TradeNo, ProviderOrderKey: event.ProviderOrderKey,
		ProviderPaymentKey: event.ProviderPaymentKey, ProviderResourceKey: event.ProviderResourceKey,
		ProviderCredentialGeneration: event.ProviderCredentialGeneration,
		ProviderLivemode:             event.ProviderLivemode,
		ProviderCreatedAt:            event.ProviderCreatedAt, ProviderState: event.ProviderState, CustomerID: event.CustomerID,
		PaidAmountMinor: event.PaidAmountMinor, RefundedAmountMinor: event.RefundedAmountMinor,
		DisputedAmountMinor: event.DisputedAmountMinor, Currency: event.Currency, PaymentMethod: event.PaymentMethod,
		Paid: event.Paid, Failed: event.Failed, Expired: event.Expired, Refunded: event.Refunded,
		Disputed: event.Disputed, DisputeResolved: event.DisputeResolved, DisputeWon: event.DisputeWon,
		PermanentFailure: event.PermanentFailure, ManualReview: event.ManualReview,
		NormalizedPayload: event.NormalizedPayload,
	}
}

func legacyPaymentRetryAdminPayload(input PaymentUnmatchedEventActionInput, event *PaymentEvent) (string, error) {
	encoded, err := common.Marshal(map[string]interface{}{
		"source": "admin", "action": PaymentUnmatchedEventActionRetryLegacy,
		"admin_id": input.AdminID, "reason": input.Reason, "event_id": input.EventID,
		"expected_event_attempts": input.ExpectedEventAttempts,
		"provider":                event.Provider, "event_key": event.EventKey, "trade_no": event.TradeNo,
		"provider_payload_digest": event.PayloadDigest,
	})
	return string(encoded), err
}

func legacyPaymentRetryAdminEventKey(eventID int64, expectedAttempts int) string {
	return fmt.Sprintf("unmatched_retry_legacy:%d:a%d", eventID, expectedAttempts)
}

func createLegacyPaymentRetryOperationsAuditTx(tx *gorm.DB, input PaymentUnmatchedEventActionInput,
	adminEvent, providerEvent *PaymentEvent, order *PaymentOrder, outcome string) error {
	if tx == nil || adminEvent == nil || providerEvent == nil {
		return ErrPaymentAuditInvalid
	}
	orderID := int64(0)
	userID := 0
	orderStatus := ""
	if order != nil {
		orderID = order.ID
		userID = order.UserID
		orderStatus = order.Status
	}
	metadataBytes, err := common.Marshal(map[string]interface{}{
		"admin_event_id": adminEvent.ID, "provider_event_id": providerEvent.ID,
		"provider_event_key": providerEvent.EventKey, "provider_payload_digest": providerEvent.PayloadDigest,
		"trade_no": providerEvent.TradeNo, "expected_event_attempts": input.ExpectedEventAttempts,
		"provider_event_attempts": providerEvent.Attempts, "provider_event_status": providerEvent.Status,
		"payment_order_id": orderID, "payment_order_status": orderStatus, "outcome": outcome,
	})
	if err != nil {
		return err
	}
	metadata := string(metadataBytes)
	var existing PaymentOperationsAudit
	existingQuery := tx.Where("action = ? AND subject_id = ? AND expected_version = ?",
		PaymentOperationsActionLegacyEpayRetry, providerEvent.ID, int64(input.ExpectedEventAttempts)).
		Limit(1).Find(&existing)
	if existingQuery.Error != nil {
		return existingQuery.Error
	}
	if existingQuery.RowsAffected > 0 {
		if existing.AdminID != input.AdminID || existing.ActorIP != input.ActorIP || existing.PaymentOrderID != orderID ||
			existing.UserID != userID || existing.Provider != PaymentProviderEpay || existing.Reason != input.Reason ||
			existing.Metadata != metadata {
			return fmt.Errorf("%w: legacy retry operations audit payload changed", ErrPaymentAuditConflict)
		}
		return nil
	}
	return tx.Create(&PaymentOperationsAudit{
		Action: PaymentOperationsActionLegacyEpayRetry, AdminID: input.AdminID, ActorIP: input.ActorIP,
		PaymentOrderID: orderID, UserID: userID, SubjectID: providerEvent.ID, Provider: PaymentProviderEpay,
		ExpectedVersion: int64(input.ExpectedEventAttempts), Reason: input.Reason,
		Metadata: metadata, CreatedAt: common.GetTimestamp(),
	}).Error
}

func legacyPaymentRetryStoredManualReason(event *PaymentEvent) string {
	if event == nil || strings.TrimSpace(event.LastError) == "" {
		return "legacy Epay event still requires manual review"
	}
	return strings.TrimSpace(event.LastError)
}

func legacyPaymentRetryResultMessage(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 1024 {
		message = message[:1024]
	}
	return message
}
