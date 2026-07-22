package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const (
	PaymentAdminActionFulfill                 = "fulfill"
	PaymentAdminActionReject                  = "reject"
	PaymentAdminActionVoid                    = "void"
	PaymentAdminActionExternalRefundConfirmed = "external_refund_confirmed"

	PaymentUnmatchedEventActionDismiss     = "dismiss"
	PaymentUnmatchedEventActionLink        = "link"
	PaymentUnmatchedEventActionRetryLegacy = "retry_legacy"

	PaymentEventStatusDismissed = "dismissed"

	PaymentLedgerEntryAdminFulfill        = "admin_fulfill"
	PaymentLedgerEntryAdminReject         = "admin_reject"
	PaymentLedgerEntryAdminVoid           = "admin_void"
	PaymentLedgerEntryAdminExternalRefund = "admin_external_refund"
	PaymentLedgerEntryAdminUnmatchedLink  = "admin_event_link"
	PaymentLedgerEntryAdminLegacyRetry    = "admin_legacy_retry"
)

var (
	ErrPaymentAuditInvalid  = errors.New("invalid payment audit action")
	ErrPaymentAuditNotFound = errors.New("payment audit subject not found")
	ErrPaymentAuditConflict = errors.New("payment audit state conflict")
)

type PaymentAdminOrderActionInput struct {
	TradeNo             string
	ExpectedVersion     int64
	AdminID             int
	ActorIP             string
	Action              string
	Reason              string
	RefundedAmountMinor int64
}

type PaymentUnmatchedEventActionInput struct {
	EventID               int64
	ExpectedEventAttempts int
	AdminID               int
	ActorIP               string
	Action                string
	Reason                string
	TargetTradeNo         string
	ExpectedOrderVersion  int64
}

type PaymentUnmatchedEventActionResult struct {
	Event     *PaymentEvent `json:"event"`
	Order     *PaymentOrder `json:"order,omitempty"`
	Duplicate bool          `json:"duplicate"`
}

func ResolvePaymentOrderByAdmin(input PaymentAdminOrderActionInput) (*PaymentSettlementResult, error) {
	input.TradeNo = strings.TrimSpace(input.TradeNo)
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.Reason = strings.TrimSpace(input.Reason)
	input.ActorIP = strings.TrimSpace(input.ActorIP)
	if input.TradeNo == "" || len(input.TradeNo) > 128 || input.ExpectedVersion <= 0 || input.AdminID <= 0 ||
		input.ActorIP == "" || len(input.ActorIP) > 64 || len(input.Reason) < 8 || len(input.Reason) > 512 {
		return nil, ErrPaymentAuditInvalid
	}
	if input.Action != PaymentAdminActionFulfill && input.Action != PaymentAdminActionReject &&
		input.Action != PaymentAdminActionVoid && input.Action != PaymentAdminActionExternalRefundConfirmed {
		return nil, ErrPaymentAuditInvalid
	}
	if input.Action == PaymentAdminActionExternalRefundConfirmed && input.RefundedAmountMinor <= 0 {
		return nil, ErrPaymentAuditInvalid
	}

	result := &PaymentSettlementResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var order PaymentOrder
		if err := lockForUpdate(tx).Where("trade_no = ?", input.TradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentAuditNotFound
			}
			return err
		}
		result.Order = &order
		result.UserID = order.UserID

		payload, err := paymentAdminActionPayload(input, &order)
		if err != nil {
			return err
		}
		eventKey := paymentAdminOrderEventKey(input.Action, order.ID, input.ExpectedVersion)
		duplicate, err := processedAdminActionRetryTx(tx, eventKey, PaymentPayloadDigest(payload))
		if err != nil {
			return err
		}
		if duplicate {
			result.Duplicate = true
			return nil
		}
		if order.Version != input.ExpectedVersion {
			return fmt.Errorf("%w: payment order version changed", ErrPaymentAuditConflict)
		}

		var actionErr error
		switch input.Action {
		case PaymentAdminActionFulfill:
			actionErr = fulfillPaymentOrderByAdminTx(tx, &order, input, payload, eventKey, result)
		case PaymentAdminActionReject, PaymentAdminActionVoid:
			actionErr = rejectPaymentOrderByAdminTx(tx, &order, input, payload, eventKey, result)
		case PaymentAdminActionExternalRefundConfirmed:
			actionErr = confirmExternalPaymentRefundTx(tx, &order, input, payload, eventKey, result)
		default:
			return ErrPaymentAuditInvalid
		}
		if actionErr != nil {
			return actionErr
		}
		return createPaymentOrderActionOperationsAuditTx(tx, &order, input, eventKey)
	})
	if err != nil {
		return nil, err
	}
	applyAdminPaymentSettlementPostCommit(result)
	return result, nil
}

func ResolveUnmatchedPaymentEventByAdmin(input PaymentUnmatchedEventActionInput) (*PaymentUnmatchedEventActionResult, error) {
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.Reason = strings.TrimSpace(input.Reason)
	input.TargetTradeNo = strings.TrimSpace(input.TargetTradeNo)
	input.ActorIP = strings.TrimSpace(input.ActorIP)
	if input.EventID <= 0 || input.AdminID <= 0 || input.ActorIP == "" || len(input.Reason) < 8 || len(input.Reason) > 512 ||
		len(input.ActorIP) > 64 ||
		(input.Action != PaymentUnmatchedEventActionDismiss && input.Action != PaymentUnmatchedEventActionLink &&
			input.Action != PaymentUnmatchedEventActionRetryLegacy) {
		return nil, ErrPaymentAuditInvalid
	}
	if input.Action == PaymentUnmatchedEventActionLink &&
		(input.TargetTradeNo == "" || len(input.TargetTradeNo) > 128 || input.ExpectedOrderVersion <= 0) {
		return nil, ErrPaymentAuditInvalid
	}
	if input.Action == PaymentUnmatchedEventActionRetryLegacy {
		if input.ExpectedEventAttempts <= 0 || input.TargetTradeNo != "" || input.ExpectedOrderVersion != 0 {
			return nil, ErrPaymentAuditInvalid
		}
		return retryLegacyPaymentEventByAdmin(input)
	}

	result := &PaymentUnmatchedEventActionResult{}
	settlement := &PaymentSettlementResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if input.Action == PaymentUnmatchedEventActionLink {
			if _, err := lockPaymentConfigurationFenceTx(tx); err != nil {
				return err
			}
		}
		var event PaymentEvent
		if err := lockForUpdate(tx).First(&event, input.EventID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentAuditNotFound
			}
			return err
		}
		result.Event = &event
		if event.ReviewCode == PaymentReviewCodeLegacyTopUpQuotaSnapshotMissing ||
			event.ReviewCode == PaymentReviewCodeLegacySubscriptionContractUnavailable {
			return fmt.Errorf("%w: payment event requires its classified legacy resolution", ErrPaymentAuditConflict)
		}
		if input.Action == PaymentUnmatchedEventActionDismiss {
			return dismissUnmatchedPaymentEventTx(tx, &event, input, result)
		}

		var order PaymentOrder
		if err := lockForUpdate(tx).Where("trade_no = ?", input.TargetTradeNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentAuditNotFound
			}
			return err
		}
		result.Order = &order
		settlement.Order = &order
		settlement.UserID = order.UserID
		return linkUnmatchedPaymentEventTx(tx, &event, &order, input, result, settlement)
	})
	if err != nil {
		return nil, err
	}
	if result.Order != nil {
		applyAdminPaymentSettlementPostCommit(settlement)
	}
	return result, nil
}

func paymentAdminActionPayload(input PaymentAdminOrderActionInput, order *PaymentOrder) (string, error) {
	payload := map[string]interface{}{
		"source":           "admin",
		"action":           input.Action,
		"admin_id":         input.AdminID,
		"reason":           input.Reason,
		"expected_version": input.ExpectedVersion,
		"trade_no":         input.TradeNo,
	}
	if order != nil {
		payload["payment_order_id"] = order.ID
		payload["provider"] = order.Provider
	}
	if input.Action == PaymentAdminActionExternalRefundConfirmed {
		payload["refunded_amount_minor"] = input.RefundedAmountMinor
	}
	encoded, err := common.Marshal(payload)
	return string(encoded), err
}

func createPaymentOrderActionOperationsAuditTx(tx *gorm.DB, order *PaymentOrder, input PaymentAdminOrderActionInput, eventKey string) error {
	if order == nil {
		return ErrPaymentAuditInvalid
	}
	action := ""
	switch input.Action {
	case PaymentAdminActionFulfill:
		action = PaymentOperationsActionAdminFulfill
	case PaymentAdminActionReject:
		action = PaymentOperationsActionAdminReject
	case PaymentAdminActionVoid:
		action = PaymentOperationsActionAdminVoid
	case PaymentAdminActionExternalRefundConfirmed:
		action = PaymentOperationsActionAdminExternalRefund
	default:
		return ErrPaymentAuditInvalid
	}
	metadata := map[string]interface{}{
		"trade_no": order.TradeNo, "admin_event_key": eventKey, "order_status": order.Status,
	}
	if input.Action == PaymentAdminActionExternalRefundConfirmed {
		metadata["refunded_amount_minor"] = input.RefundedAmountMinor
	}
	return createPaymentOperationsAuditTx(tx, PaymentOperationsAudit{
		Action: action, AdminID: input.AdminID, ActorIP: input.ActorIP,
		PaymentOrderID: order.ID, UserID: order.UserID, SubjectID: order.ID, Provider: order.Provider,
		ExpectedVersion: input.ExpectedVersion, Reason: input.Reason,
	}, metadata)
}

func paymentUnmatchedActionPayload(input PaymentUnmatchedEventActionInput, event *PaymentEvent, order *PaymentOrder) (string, error) {
	payload := map[string]interface{}{
		"source":    "admin",
		"action":    input.Action,
		"admin_id":  input.AdminID,
		"reason":    input.Reason,
		"event_id":  input.EventID,
		"provider":  event.Provider,
		"event_key": event.EventKey,
	}
	if order != nil {
		payload["target_trade_no"] = order.TradeNo
		payload["payment_order_id"] = order.ID
		payload["expected_order_version"] = input.ExpectedOrderVersion
	}
	encoded, err := common.Marshal(payload)
	return string(encoded), err
}

func paymentAdminOrderEventKey(action string, orderID, expectedVersion int64) string {
	return "order_" + action + ":" + strconv.FormatInt(orderID, 10) + ":v" + strconv.FormatInt(expectedVersion, 10)
}

func processedAdminActionRetryTx(tx *gorm.DB, eventKey, payloadDigest string) (bool, error) {
	var event PaymentEvent
	err := tx.Where("provider = ? AND event_key = ?", "admin", eventKey).First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if event.PayloadDigest != payloadDigest {
		return false, fmt.Errorf("%w: administrator action payload changed", ErrPaymentAuditConflict)
	}
	if event.Status != PaymentEventStatusProcessed {
		return false, fmt.Errorf("%w: administrator action is not in a terminal state", ErrPaymentAuditConflict)
	}
	return true, nil
}

func createAdminPaymentEventTx(tx *gorm.DB, eventKey, eventType, payload string, order *PaymentOrder) (*PaymentEvent, error) {
	event, created, err := UpsertPaymentEvent(tx, &PaymentEvent{
		Provider:          "admin",
		EventKey:          eventKey,
		EventType:         eventType,
		TradeNo:           order.TradeNo,
		PaymentOrderID:    order.ID,
		Currency:          order.Currency,
		PaymentMethod:     order.PaymentMethod,
		PayloadDigest:     PaymentPayloadDigest(payload),
		NormalizedPayload: payload,
	})
	if err != nil {
		return nil, err
	}
	if !created {
		return nil, fmt.Errorf("%w: administrator event already exists", ErrPaymentAuditConflict)
	}
	return event, nil
}

func fulfillPaymentOrderByAdminTx(tx *gorm.DB, order *PaymentOrder, input PaymentAdminOrderActionInput, payload, eventKey string, result *PaymentSettlementResult) error {
	if err := validateStripeAdminFulfillmentModeTx(tx, order); err != nil {
		return err
	}
	granted, err := paymentEntitlementGrantedTx(tx, order)
	if err != nil {
		return err
	}
	if order.Status != PaymentOrderStatusManualReview || granted || order.RefundedAmountMinor != 0 ||
		order.DisputedAmountMinor != 0 || order.ReversedAmountMinor != 0 || order.ReversedQuota != 0 {
		return fmt.Errorf("%w: payment order cannot be manually fulfilled", ErrPaymentAuditConflict)
	}
	event, err := createAdminPaymentEventTx(tx, eventKey, PaymentOperationsActionAdminFulfill, payload, order)
	if err != nil {
		return err
	}
	event.Paid = true
	event.PaidAmountMinor = order.ExpectedAmountMinor
	if err := tx.Model(&PaymentEvent{}).Where("id = ?", event.ID).Updates(map[string]interface{}{
		"paid": true, "paid_amount_minor": order.ExpectedAmountMinor,
	}).Error; err != nil {
		return err
	}
	order.Status = PaymentOrderStatusPending
	if err := fulfillPaymentOrderTx(tx, event, order, PaymentEventInput{
		Provider: order.Provider, EventKey: event.EventKey, EventType: event.EventType, TradeNo: order.TradeNo,
		PaidAmountMinor: order.ExpectedAmountMinor, Currency: order.Currency, PaymentMethod: order.PaymentMethod,
		Paid: true, NormalizedPayload: payload,
	}, result); err != nil {
		return paymentAuditSettlementConflict(err, "manual fulfillment failed")
	}
	if err := createPaymentLedgerEntryTx(tx, event, order, PaymentLedgerEntryAdminFulfill,
		order.ExpectedAmountMinor, 0, "payment manually fulfilled by administrator"); err != nil {
		return err
	}
	return finishPaymentEventTx(tx, event.ID, PaymentEventStatusProcessed, "", order.ID)
}

func validateStripeAdminFulfillmentModeTx(tx *gorm.DB, order *PaymentOrder) error {
	if tx == nil || order == nil {
		return ErrPaymentAuditInvalid
	}
	if order.Provider != PaymentProviderStripe {
		return nil
	}
	if order.ProviderLivemode == nil {
		return fmt.Errorf("%w: Stripe order mode is not frozen", ErrPaymentAuditConflict)
	}
	var paidEvents []PaymentEvent
	if err := tx.Select("provider_livemode").
		Where("provider = ? AND payment_order_id = ? AND paid = ?", PaymentProviderStripe, order.ID, true).
		Find(&paidEvents).Error; err != nil {
		return err
	}
	for _, event := range paidEvents {
		if event.ProviderLivemode == nil || !paymentLivemodeEqual(order.ProviderLivemode, event.ProviderLivemode) {
			return fmt.Errorf("%w: Stripe order and verified payment modes do not match", ErrPaymentAuditConflict)
		}
	}
	return nil
}

func rejectPaymentOrderByAdminTx(tx *gorm.DB, order *PaymentOrder, input PaymentAdminOrderActionInput, payload, eventKey string, result *PaymentSettlementResult) error {
	granted, err := paymentEntitlementGrantedTx(tx, order)
	if err != nil {
		return err
	}
	if order.Status != PaymentOrderStatusManualReview || granted || order.RefundedAmountMinor != 0 ||
		order.DisputedAmountMinor != 0 || order.ReversedAmountMinor != 0 || order.ReversedQuota != 0 {
		return fmt.Errorf("%w: payment order cannot be rejected after entitlement or reversal", ErrPaymentAuditConflict)
	}
	eventType := PaymentOperationsActionAdminReject
	ledgerType := PaymentLedgerEntryAdminReject
	description := "payment rejected by administrator"
	if input.Action == PaymentAdminActionVoid {
		eventType = PaymentOperationsActionAdminVoid
		ledgerType = PaymentLedgerEntryAdminVoid
		description = "payment voided by administrator"
	}
	event, err := createAdminPaymentEventTx(tx, eventKey, eventType, payload, order)
	if err != nil {
		return err
	}
	now := common.GetTimestamp()
	updated := tx.Model(&PaymentOrder{}).
		Where("id = ? AND version = ? AND status = ?", order.ID, input.ExpectedVersion, PaymentOrderStatusManualReview).
		Updates(map[string]interface{}{
			"status": PaymentOrderStatusFailed, "status_reason": input.Reason,
			"updated_at": now, "version": input.ExpectedVersion + 1,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("%w: payment order changed", ErrPaymentAuditConflict)
	}
	order.Status = PaymentOrderStatusFailed
	order.StatusReason = input.Reason
	order.UpdatedAt = now
	order.Version = input.ExpectedVersion + 1
	if err := releasePaymentLimitReservationTx(tx, order, now); err != nil {
		return err
	}
	if err := syncPaymentProjectionStatusTx(tx, order); err != nil {
		return err
	}
	if err := completePaymentProjectionTx(tx, order, now); err != nil {
		return err
	}
	if err := createPaymentLedgerEntryTx(tx, event, order, ledgerType, 0, 0, description); err != nil {
		return err
	}
	result.Order = order
	return finishPaymentEventTx(tx, event.ID, PaymentEventStatusProcessed, "", order.ID)
}

func confirmExternalPaymentRefundTx(tx *gorm.DB, order *PaymentOrder, input PaymentAdminOrderActionInput, payload, eventKey string, result *PaymentSettlementResult) error {
	if input.RefundedAmountMinor > order.ExpectedAmountMinor || order.ExpectedAmountMinor <= 0 {
		return ErrPaymentAuditInvalid
	}
	if order.OrderKind == PaymentOrderKindSubscription && input.RefundedAmountMinor != order.ExpectedAmountMinor {
		return fmt.Errorf("%w: subscription refunds must be confirmed in full", ErrPaymentAuditInvalid)
	}
	if input.RefundedAmountMinor <= order.RefundedAmountMinor || order.DisputedAmountMinor != 0 {
		return fmt.Errorf("%w: refund amount did not advance or order has a dispute", ErrPaymentAuditConflict)
	}
	granted, err := paymentEntitlementGrantedTx(tx, order)
	if err != nil {
		return err
	}
	event, err := createAdminPaymentEventTx(tx, eventKey, PaymentOperationsActionAdminExternalRefund, payload, order)
	if err != nil {
		return err
	}
	event.Refunded = true
	event.RefundedAmountMinor = input.RefundedAmountMinor
	if err := tx.Model(&PaymentEvent{}).Where("id = ?", event.ID).Updates(map[string]interface{}{
		"refunded": true, "refunded_amount_minor": input.RefundedAmountMinor,
	}).Error; err != nil {
		return err
	}
	previousRefunded := order.RefundedAmountMinor
	if granted {
		if err := reconcilePaymentReversalTx(tx, event, order, PaymentEventInput{
			Provider: order.Provider, EventKey: event.EventKey, EventType: event.EventType,
			TradeNo: order.TradeNo, RefundedAmountMinor: input.RefundedAmountMinor,
			Currency: order.Currency, Refunded: true, NormalizedPayload: payload,
		}, result); err != nil {
			return paymentAuditSettlementConflict(err, "external refund reconciliation failed")
		}
	} else {
		if order.Status == PaymentOrderStatusFulfilled || order.Status == PaymentOrderStatusPaid ||
			order.Status == PaymentOrderStatusDebt || order.Status == PaymentOrderStatusDisputed ||
			order.SettledAt > 0 || order.PaidAmountMinor > 0 {
			return fmt.Errorf("%w: canonical entitlement state is inconsistent", ErrPaymentAuditConflict)
		}
		now := common.GetTimestamp()
		status := PaymentOrderStatusRefundPending
		if input.RefundedAmountMinor == order.ExpectedAmountMinor {
			status = PaymentOrderStatusRefunded
		}
		updated := tx.Model(&PaymentOrder{}).Where("id = ? AND version = ?", order.ID, input.ExpectedVersion).
			Updates(map[string]interface{}{
				"status": status, "status_reason": input.Reason,
				"refunded_amount_minor": input.RefundedAmountMinor,
				"updated_at":            now, "version": input.ExpectedVersion + 1,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return fmt.Errorf("%w: payment order changed", ErrPaymentAuditConflict)
		}
		order.Status = status
		order.StatusReason = input.Reason
		order.RefundedAmountMinor = input.RefundedAmountMinor
		order.UpdatedAt = now
		order.Version = input.ExpectedVersion + 1
		if err := syncPaymentProjectionStatusTx(tx, order); err != nil {
			return err
		}
		if err := completePaymentProjectionTx(tx, order, now); err != nil {
			return err
		}
	}
	if err := createPaymentLedgerEntryTx(tx, event, order, PaymentLedgerEntryAdminExternalRefund,
		input.RefundedAmountMinor-previousRefunded, 0, "external provider refund confirmed by administrator"); err != nil {
		return err
	}
	result.Order = order
	result.UserID = order.UserID
	return finishPaymentEventTx(tx, event.ID, PaymentEventStatusProcessed, "", order.ID)
}

func dismissUnmatchedPaymentEventTx(tx *gorm.DB, event *PaymentEvent, input PaymentUnmatchedEventActionInput, result *PaymentUnmatchedEventActionResult) error {
	payload, err := paymentUnmatchedActionPayload(input, event, nil)
	if err != nil {
		return err
	}
	eventKey := "unmatched_dismiss:" + strconv.FormatInt(event.ID, 10)
	duplicate, err := processedAdminActionRetryTx(tx, eventKey, PaymentPayloadDigest(payload))
	if err != nil {
		return err
	}
	if duplicate {
		result.Duplicate = true
		return nil
	}
	if event.Status != PaymentEventStatusManualReview || event.PaymentOrderID != 0 {
		return fmt.Errorf("%w: event is no longer unmatched", ErrPaymentAuditConflict)
	}
	adminEvent, _, err := UpsertPaymentEvent(tx, &PaymentEvent{
		Provider: "admin", EventKey: eventKey, EventType: PaymentOperationsActionUnmatchedDismiss,
		TradeNo: event.TradeNo, PayloadDigest: PaymentPayloadDigest(payload), NormalizedPayload: payload,
	})
	if err != nil {
		return err
	}
	now := common.GetTimestamp()
	updated := tx.Model(&PaymentEvent{}).
		Where("id = ? AND status = ? AND payment_order_id = ?", event.ID, PaymentEventStatusManualReview, 0).
		Updates(map[string]interface{}{
			"status": PaymentEventStatusDismissed, "last_error": input.Reason,
			"processed_at": now, "updated_at": now,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("%w: event changed", ErrPaymentAuditConflict)
	}
	event.Status = PaymentEventStatusDismissed
	event.LastError = input.Reason
	event.ProcessedAt = now
	event.UpdatedAt = now
	result.Event = event
	if err := finishPaymentEventTx(tx, adminEvent.ID, PaymentEventStatusProcessed, "", 0); err != nil {
		return err
	}
	return createPaymentOperationsAuditTx(tx, PaymentOperationsAudit{
		Action: PaymentOperationsActionUnmatchedDismiss, AdminID: input.AdminID, ActorIP: input.ActorIP,
		SubjectID: event.ID, Provider: event.Provider, Reason: input.Reason,
	}, map[string]interface{}{
		"provider_event_key": event.EventKey, "admin_event_key": eventKey, "event_status": event.Status,
	})
}

func linkUnmatchedPaymentEventTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder, input PaymentUnmatchedEventActionInput,
	result *PaymentUnmatchedEventActionResult, settlement *PaymentSettlementResult) error {
	payload, err := paymentUnmatchedActionPayload(input, event, order)
	if err != nil {
		return err
	}
	eventKey := "unmatched_link:" + strconv.FormatInt(event.ID, 10) + ":" + strconv.FormatInt(order.ID, 10) +
		":v" + strconv.FormatInt(input.ExpectedOrderVersion, 10)
	duplicate, err := processedAdminActionRetryTx(tx, eventKey, PaymentPayloadDigest(payload))
	if err != nil {
		return err
	}
	if duplicate {
		if event.PaymentOrderID != order.ID || event.Status != PaymentEventStatusProcessed {
			return fmt.Errorf("%w: linked event terminal state is inconsistent", ErrPaymentAuditConflict)
		}
		result.Duplicate = true
		return nil
	}
	if event.Status != PaymentEventStatusManualReview || event.PaymentOrderID != 0 {
		return fmt.Errorf("%w: event is no longer unmatched", ErrPaymentAuditConflict)
	}
	if order.Version != input.ExpectedOrderVersion {
		return fmt.Errorf("%w: payment order version changed", ErrPaymentAuditConflict)
	}
	if event.Provider == "" || event.Provider == "admin" || event.PayloadDigest == "" ||
		event.PayloadDigest != PaymentPayloadDigest(event.NormalizedPayload) || !event.Paid || event.PaidAmountMinor <= 0 ||
		event.Failed || event.Expired || event.Refunded || event.Disputed || event.DisputeResolved || event.ManualReview {
		return fmt.Errorf("%w: event is not an intact verified paid event", ErrPaymentAuditConflict)
	}
	if order.Provider != event.Provider || event.PaidAmountMinor != order.ExpectedAmountMinor ||
		!strings.EqualFold(event.Currency, order.Currency) ||
		(event.PaymentMethod != "" && !strings.EqualFold(event.PaymentMethod, order.PaymentMethod)) {
		return fmt.Errorf("%w: event contract does not match target order", ErrPaymentAuditConflict)
	}
	credentialFenced := event.Provider == PaymentProviderEpay || event.Provider == PaymentProviderStripe || event.Provider == PaymentProviderXorPay
	if credentialFenced {
		if event.ProviderCredentialGeneration <= 0 {
			return fmt.Errorf("%w: event credential generation is not auditable", ErrPaymentAuditConflict)
		}
		if order.ProviderCredentialGeneration == 0 {
			if err := tx.Model(&PaymentOrder{}).Where("id = ? AND provider_credential_generation = 0", order.ID).
				Update("provider_credential_generation", event.ProviderCredentialGeneration).Error; err != nil {
				return err
			}
			order.ProviderCredentialGeneration = event.ProviderCredentialGeneration
		}
		available, err := paymentCredentialGenerationAvailableTx(tx, event.Provider,
			event.ProviderCredentialGeneration, order.CreatedAt, common.GetTimestamp())
		if err != nil {
			return err
		}
		generationMatches := order.ProviderCredentialGeneration == event.ProviderCredentialGeneration
		if event.Provider == PaymentProviderStripe {
			generationMatches = true
		}
		if !available || !generationMatches {
			return fmt.Errorf("%w: event credential generation is revoked or does not match the order", ErrPaymentAuditConflict)
		}
	}
	if order.RefundedAmountMinor != 0 || order.DisputedAmountMinor != 0 || order.ReversedAmountMinor != 0 || order.ReversedQuota != 0 {
		return fmt.Errorf("%w: reversed orders cannot be linked", ErrPaymentAuditConflict)
	}
	granted, err := paymentEntitlementGrantedTx(tx, order)
	if err != nil {
		return err
	}
	if granted || (order.Status != PaymentOrderStatusPending && order.Status != PaymentOrderStatusProcessing &&
		order.Status != PaymentOrderStatusManualReview && order.Status != PaymentOrderStatusFailed && order.Status != PaymentOrderStatusExpired) {
		return fmt.Errorf("%w: target order cannot be safely fulfilled", ErrPaymentAuditConflict)
	}
	if err := validateUnmatchedEventOrderIdentityTx(tx, event, order); err != nil {
		return err
	}
	if err := bindAndValidateProviderIdentityTx(tx, order, PaymentEventInput{
		ProviderOrderKey: event.ProviderOrderKey, ProviderPaymentKey: event.ProviderPaymentKey,
	}); err != nil {
		return fmt.Errorf("%w: provider identity cannot be bound", ErrPaymentAuditConflict)
	}
	customerBound, err := bindStripeCustomerTx(tx, order, event.CustomerID)
	if err != nil {
		return fmt.Errorf("%w: provider customer identity cannot be bound", ErrPaymentAuditConflict)
	}
	settlement.UserCacheChanged = settlement.UserCacheChanged || customerBound
	adminEvent, err := createAdminPaymentEventTx(tx, eventKey, PaymentOperationsActionUnmatchedLink, payload, order)
	if err != nil {
		return err
	}
	claimed := tx.Model(&PaymentEvent{}).
		Where("id = ? AND status = ? AND payment_order_id = ?", event.ID, PaymentEventStatusManualReview, 0).
		Updates(map[string]interface{}{
			"status": PaymentEventStatusProcessing, "last_error": "",
			"attempts": gorm.Expr("attempts + ?", 1), "updated_at": common.GetTimestamp(),
		})
	if claimed.Error != nil {
		return claimed.Error
	}
	if claimed.RowsAffected != 1 {
		return fmt.Errorf("%w: event changed", ErrPaymentAuditConflict)
	}
	if order.Status == PaymentOrderStatusFailed || order.Status == PaymentOrderStatusExpired {
		order.Status = PaymentOrderStatusPending
		if err := syncPaymentProjectionStatusTx(tx, order); err != nil {
			return err
		}
	} else if order.Status == PaymentOrderStatusManualReview {
		order.Status = PaymentOrderStatusPending
	}
	if err := fulfillPaymentOrderTx(tx, event, order, PaymentEventInput{
		Provider: event.Provider, EventKey: event.EventKey, EventType: event.EventType, TradeNo: order.TradeNo,
		ProviderOrderKey: event.ProviderOrderKey, ProviderPaymentKey: event.ProviderPaymentKey,
		ProviderResourceKey: event.ProviderResourceKey, ProviderCredentialGeneration: event.ProviderCredentialGeneration,
		ProviderLivemode:  event.ProviderLivemode,
		ProviderCreatedAt: event.ProviderCreatedAt,
		ProviderState:     event.ProviderState, CustomerID: event.CustomerID, PaidAmountMinor: event.PaidAmountMinor,
		Currency: event.Currency, PaymentMethod: event.PaymentMethod, Paid: true,
		NormalizedPayload: event.NormalizedPayload,
	}, settlement); err != nil {
		return paymentAuditSettlementConflict(err, "linked event fulfillment failed")
	}
	if err := finishPaymentEventTx(tx, event.ID, PaymentEventStatusProcessed, "", order.ID); err != nil {
		return err
	}
	if err := createPaymentLedgerEntryTx(tx, adminEvent, order, PaymentLedgerEntryAdminUnmatchedLink,
		order.ExpectedAmountMinor, 0, "verified unmatched event linked by administrator"); err != nil {
		return err
	}
	if err := finishPaymentEventTx(tx, adminEvent.ID, PaymentEventStatusProcessed, "", order.ID); err != nil {
		return err
	}
	if err := createPaymentOperationsAuditTx(tx, PaymentOperationsAudit{
		Action: PaymentOperationsActionUnmatchedLink, AdminID: input.AdminID, ActorIP: input.ActorIP,
		PaymentOrderID: order.ID, UserID: order.UserID, SubjectID: event.ID, Provider: order.Provider,
		ExpectedVersion: input.ExpectedOrderVersion, Reason: input.Reason,
	}, map[string]interface{}{
		"provider_event_key": event.EventKey, "admin_event_key": eventKey, "trade_no": order.TradeNo,
	}); err != nil {
		return err
	}
	event.Status = PaymentEventStatusProcessed
	event.PaymentOrderID = order.ID
	event.LastError = ""
	result.Event = event
	result.Order = order
	return nil
}

func validateUnmatchedEventOrderIdentityTx(tx *gorm.DB, event *PaymentEvent, order *PaymentOrder) error {
	providerOrderKey := strings.TrimSpace(event.ProviderOrderKey)
	providerPaymentKey := strings.TrimSpace(event.ProviderPaymentKey)
	if order.ProviderOrderKey != nil && (providerOrderKey == "" || *order.ProviderOrderKey != providerOrderKey) {
		return fmt.Errorf("%w: provider order identity mismatch", ErrPaymentAuditConflict)
	}
	if order.ProviderPaymentKey != nil && (providerPaymentKey == "" || *order.ProviderPaymentKey != providerPaymentKey) {
		return fmt.Errorf("%w: provider payment identity mismatch", ErrPaymentAuditConflict)
	}
	if providerOrderKey == "" && providerPaymentKey == "" {
		return fmt.Errorf("%w: linking requires a provider order or payment identity", ErrPaymentAuditConflict)
	}
	if providerOrderKey != "" {
		var count int64
		if err := tx.Model(&PaymentOrder{}).Where("id <> ? AND provider_order_key = ?", order.ID, providerOrderKey).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("%w: provider order identity is already bound", ErrPaymentAuditConflict)
		}
	}
	if providerPaymentKey != "" {
		var count int64
		if err := tx.Model(&PaymentOrder{}).Where("id <> ? AND provider_payment_key = ?", order.ID, providerPaymentKey).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("%w: provider payment identity is already bound", ErrPaymentAuditConflict)
		}
	}
	return nil
}

func paymentEntitlementGrantedTx(tx *gorm.DB, order *PaymentOrder) (bool, error) {
	if tx == nil || order == nil {
		return false, ErrPaymentAuditInvalid
	}
	ledgerType := PaymentLedgerEntryCredit
	if order.OrderKind == PaymentOrderKindSubscription {
		ledgerType = PaymentLedgerEntrySubscriptionGranted
	}
	var ledgerCount int64
	if err := tx.Model(&PaymentLedgerEntry{}).
		Where("payment_order_id = ? AND entry_type = ?", order.ID, ledgerType).Count(&ledgerCount).Error; err != nil {
		return false, err
	}
	if ledgerCount > 1 {
		return false, fmt.Errorf("%w: duplicate entitlement ledger entries", ErrPaymentAuditConflict)
	}
	ledgerGranted := ledgerCount == 1
	canonicalGranted := order.SettledAt > 0 || order.PaidAmountMinor > 0 || order.Status == PaymentOrderStatusFulfilled ||
		order.Status == PaymentOrderStatusPaid || order.Status == PaymentOrderStatusDebt || order.Status == PaymentOrderStatusDisputed
	switch order.OrderKind {
	case PaymentOrderKindTopUp:
		var topUp TopUp
		if err := lockForUpdate(tx).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).First(&topUp).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, fmt.Errorf("%w: top-up projection is missing", ErrPaymentAuditConflict)
			}
			return false, err
		}
		if ledgerGranted != canonicalGranted {
			return false, fmt.Errorf("%w: top-up entitlement sources disagree", ErrPaymentAuditConflict)
		}
		if ledgerGranted {
			switch topUp.Status {
			case common.TopUpStatusSuccess, PaymentOrderStatusRefundPending, PaymentOrderStatusRefunded,
				PaymentOrderStatusDisputed, PaymentOrderStatusDebt:
			default:
				return false, fmt.Errorf("%w: top-up projection does not reflect a granted entitlement", ErrPaymentAuditConflict)
			}
		} else if topUp.Status == common.TopUpStatusSuccess {
			return false, fmt.Errorf("%w: top-up projection has an unledgered entitlement", ErrPaymentAuditConflict)
		}
		return ledgerGranted, nil
	case PaymentOrderKindSubscription:
		var projection SubscriptionOrder
		if err := lockForUpdate(tx).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).First(&projection).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, fmt.Errorf("%w: subscription projection is missing", ErrPaymentAuditConflict)
			}
			return false, err
		}
		var subscriptionCount int64
		if err := tx.Model(&UserSubscription{}).Where("payment_order_id = ?", order.ID).Count(&subscriptionCount).Error; err != nil {
			return false, err
		}
		if subscriptionCount > 1 {
			return false, fmt.Errorf("%w: duplicate subscription entitlements", ErrPaymentAuditConflict)
		}
		if ledgerGranted != canonicalGranted {
			return false, fmt.Errorf("%w: subscription entitlement sources disagree", ErrPaymentAuditConflict)
		}
		if ledgerGranted && subscriptionCount != 1 {
			return false, fmt.Errorf("%w: subscription entitlement projection is missing", ErrPaymentAuditConflict)
		}
		if !ledgerGranted && (subscriptionCount != 0 || projection.Status == common.TopUpStatusSuccess) {
			return false, fmt.Errorf("%w: subscription projection has an unledgered entitlement", ErrPaymentAuditConflict)
		}
		return ledgerGranted, nil
	default:
		return false, ErrPaymentAuditInvalid
	}
}

func completePaymentProjectionTx(tx *gorm.DB, order *PaymentOrder, completedAt int64) error {
	if order.OrderKind == PaymentOrderKindTopUp {
		return tx.Model(&TopUp{}).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			Update("complete_time", completedAt).Error
	}
	if order.OrderKind == PaymentOrderKindSubscription {
		return tx.Model(&SubscriptionOrder{}).Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			Update("complete_time", completedAt).Error
	}
	return nil
}

func paymentAuditSettlementConflict(err error, operation string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrSubscriptionPurchaseLimit) || errors.Is(err, ErrSubscriptionOrderSnapshotMissing) ||
		errors.Is(err, ErrPaymentManualReview) || errors.Is(err, ErrTopUpStatusInvalid) ||
		errors.Is(err, ErrPaymentProviderMismatch) || errors.Is(err, ErrPaymentMethodMismatch) ||
		errors.Is(err, ErrPaymentAmountMismatch) || errors.Is(err, ErrPaymentCurrencyMismatch) ||
		errors.Is(err, ErrQuotaOverflow) || errors.Is(err, ErrBillingAccountNotFound) ||
		errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: %s: %v", ErrPaymentAuditConflict, operation, err)
	}
	return err
}

func applyAdminPaymentSettlementPostCommit(result *PaymentSettlementResult) {
	if result == nil {
		return
	}
	if result.UserID > 0 {
		if err := InvalidateUserCache(result.UserID); err != nil {
			common.SysLog("failed to invalidate administrator payment user cache: " + err.Error())
		}
	}
	for _, userID := range result.CacheUserIDs {
		if userID <= 0 || userID == result.UserID {
			continue
		}
		if err := InvalidateUserCache(userID); err != nil {
			common.SysLog("failed to invalidate administrator payment-related user cache: " + err.Error())
		}
	}
	if result.GroupCacheValue != "" && result.UserID > 0 {
		_ = UpdateUserGroupCache(result.UserID, result.GroupCacheValue)
	}
	if !result.Duplicate && result.AffiliateUserID > 0 && result.AffiliateReward > 0 {
		recordAffiliateTopUpRewardLog(result.AffiliateUserID, result.AffiliateReward)
	}
}
