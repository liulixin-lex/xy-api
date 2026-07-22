package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type PaymentAuditDetail struct {
	Order                      *PaymentOrder                      `json:"order,omitempty"`
	LegacyReviewReason         string                             `json:"legacy_review_reason,omitempty"`
	Events                     []PaymentEventAuditView            `json:"events"`
	Debts                      []PaymentDebt                      `json:"debts"`
	Ledger                     []PaymentLedgerEntry               `json:"ledger"`
	Tasks                      []PaymentTask                      `json:"tasks,omitempty"`
	LimitReservation           *PaymentLimitReservation           `json:"limit_reservation,omitempty"`
	CustomerBindings           []PaymentCustomerBinding           `json:"customer_bindings,omitempty"`
	CustomerBindingRetirements []PaymentCustomerBindingRetirement `json:"customer_binding_retirements,omitempty"`
	OperationsAudits           []PaymentOperationsAudit           `json:"operations_audits,omitempty"`
}

type PaymentEventAuditView struct {
	ID                  int64    `json:"id"`
	Provider            string   `json:"provider"`
	EventKey            string   `json:"event_key"`
	EventType           string   `json:"event_type"`
	TradeNo             string   `json:"trade_no"`
	PaymentOrderID      int64    `json:"payment_order_id,omitempty"`
	ProviderLivemode    *bool    `json:"provider_livemode,omitempty"`
	ProviderCreatedAt   int64    `json:"provider_created_at,omitempty"`
	ProviderState       string   `json:"provider_state,omitempty"`
	PaidAmountMinor     int64    `json:"paid_amount_minor"`
	RefundedAmountMinor int64    `json:"refunded_amount_minor"`
	DisputedAmountMinor int64    `json:"disputed_amount_minor"`
	Currency            string   `json:"currency,omitempty"`
	PaymentMethod       string   `json:"payment_method,omitempty"`
	Paid                bool     `json:"paid"`
	Failed              bool     `json:"failed"`
	Expired             bool     `json:"expired"`
	Refunded            bool     `json:"refunded"`
	Disputed            bool     `json:"disputed"`
	DisputeResolved     bool     `json:"dispute_resolved"`
	DisputeWon          bool     `json:"dispute_won"`
	PermanentFailure    bool     `json:"permanent_failure"`
	ManualReview        bool     `json:"manual_review"`
	PayloadDigest       string   `json:"payload_digest"`
	Status              string   `json:"status"`
	ReviewCode          string   `json:"review_code,omitempty"`
	Attempts            int      `json:"attempts"`
	LastError           string   `json:"last_error,omitempty"`
	CreatedAt           int64    `json:"created_at"`
	ProcessedAt         int64    `json:"processed_at"`
	UpdatedAt           int64    `json:"updated_at"`
	LegacyKind          string   `json:"legacy_kind,omitempty"`
	AvailableActions    []string `json:"available_actions,omitempty"`
}

func ResolveManualPaymentOrderByAdmin(tradeNo string, expectedVersion int64, adminID int, actorIP, reason string) (*PaymentSettlementResult, error) {
	return ResolvePaymentOrderByAdmin(PaymentAdminOrderActionInput{
		TradeNo: tradeNo, ExpectedVersion: expectedVersion, AdminID: adminID,
		ActorIP: actorIP, Action: PaymentAdminActionFulfill, Reason: reason,
	})
}

func ListPaymentAuditOrders(statuses []string, includeCredentialIncidents bool, provider, tradeNo string, offset, limit int) ([]PaymentOrder, int64, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	query := DB.Model(&PaymentOrder{})
	if len(statuses) > 0 && includeCredentialIncidents {
		query = query.Where("status IN ? OR credential_incident = ?", statuses, true)
	} else if len(statuses) > 0 {
		query = query.Where("status IN ?", statuses)
	} else if includeCredentialIncidents {
		query = query.Where("credential_incident = ?", true)
	}
	if provider = strings.TrimSpace(provider); provider != "" {
		query = query.Where("provider = ?", provider)
	}
	if tradeNo = strings.TrimSpace(tradeNo); tradeNo != "" {
		query = query.Where("trade_no = ?", tradeNo)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var orders []PaymentOrder
	if err := query.Order("updated_at desc, id desc").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		return nil, 0, err
	}
	return orders, total, nil
}

func ListUnmatchedPaymentEvents(limit int) ([]PaymentEvent, error) {
	events, _, err := ListUnmatchedPaymentEventsPage(0, limit)
	return events, err
}

func ListUnmatchedPaymentEventsPage(offset, limit int) ([]PaymentEvent, int64, error) {
	if offset < 0 {
		offset = 0
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	query := DB.Model(&PaymentEvent{}).Where(
		"status IN ? AND payment_order_id = ?",
		[]string{PaymentEventStatusManualReview, PaymentEventStatusCredentialRevoked, PaymentEventStatusDismissed},
		0,
	)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var events []PaymentEvent
	err := query.Order("updated_at desc, id desc").Offset(offset).Limit(limit).Find(&events).Error
	return events, total, err
}

func ListUnmatchedPaymentEventViewsPage(offset, limit int) ([]PaymentEventAuditView, int64, error) {
	events, total, err := ListUnmatchedPaymentEventsPage(offset, limit)
	if err != nil || len(events) == 0 {
		return nil, total, err
	}
	views, err := buildUnmatchedPaymentEventAuditViews(events)
	return views, total, err
}

func buildUnmatchedPaymentEventAuditViews(events []PaymentEvent) ([]PaymentEventAuditView, error) {
	tradeNos := make([]string, 0, len(events))
	seenTradeNos := make(map[string]struct{}, len(events))
	for index := range events {
		tradeNo := strings.TrimSpace(events[index].TradeNo)
		if tradeNo == "" {
			continue
		}
		if _, seen := seenTradeNos[tradeNo]; seen {
			continue
		}
		seenTradeNos[tradeNo] = struct{}{}
		tradeNos = append(tradeNos, tradeNo)
	}

	topUpsByTradeNo := make(map[string]TopUp, len(tradeNos))
	subscriptionsByTradeNo := make(map[string]SubscriptionOrder, len(tradeNos))
	ordersByTradeNo := make(map[string]PaymentOrder, len(tradeNos))
	usersByID := make(map[int]struct{}, len(tradeNos))
	if len(tradeNos) > 0 {
		var topUps []TopUp
		if err := DB.Where("trade_no IN ?", tradeNos).Find(&topUps).Error; err != nil {
			return nil, err
		}
		for index := range topUps {
			topUpsByTradeNo[topUps[index].TradeNo] = topUps[index]
		}
		var subscriptions []SubscriptionOrder
		if err := DB.Where("trade_no IN ?", tradeNos).Find(&subscriptions).Error; err != nil {
			return nil, err
		}
		for index := range subscriptions {
			subscriptionsByTradeNo[subscriptions[index].TradeNo] = subscriptions[index]
		}
		var orders []PaymentOrder
		if err := DB.Where("trade_no IN ?", tradeNos).Find(&orders).Error; err != nil {
			return nil, err
		}
		for index := range orders {
			ordersByTradeNo[orders[index].TradeNo] = orders[index]
		}
		userIDs := make([]int, 0, len(topUps)+len(subscriptions))
		seenUserIDs := make(map[int]struct{}, cap(userIDs))
		for index := range topUps {
			if topUps[index].UserId > 0 {
				if _, seen := seenUserIDs[topUps[index].UserId]; !seen {
					seenUserIDs[topUps[index].UserId] = struct{}{}
					userIDs = append(userIDs, topUps[index].UserId)
				}
			}
		}
		for index := range subscriptions {
			if subscriptions[index].UserId > 0 {
				if _, seen := seenUserIDs[subscriptions[index].UserId]; !seen {
					seenUserIDs[subscriptions[index].UserId] = struct{}{}
					userIDs = append(userIDs, subscriptions[index].UserId)
				}
			}
		}
		if len(userIDs) > 0 {
			var userIDsFound []int
			if err := DB.Model(&User{}).Where("id IN ?", userIDs).Pluck("id", &userIDsFound).Error; err != nil {
				return nil, err
			}
			for _, userID := range userIDsFound {
				usersByID[userID] = struct{}{}
			}
		}
	}

	views := make([]PaymentEventAuditView, 0, len(events))
	for index := range events {
		event := &events[index]
		view := paymentEventAuditView(event)
		topUp, hasTopUp := topUpsByTradeNo[event.TradeNo]
		subscription, hasSubscription := subscriptionsByTradeNo[event.TradeNo]
		_, hasOrder := ordersByTradeNo[event.TradeNo]
		switch {
		case hasTopUp && !hasSubscription:
			view.LegacyKind = PaymentOrderKindTopUp
		case hasSubscription && !hasTopUp:
			view.LegacyKind = PaymentOrderKindSubscription
		}

		if event.Status != PaymentEventStatusManualReview || event.PaymentOrderID != 0 {
			views = append(views, view)
			continue
		}
		if event.ReviewCode == PaymentReviewCodeLegacyTopUpQuotaSnapshotMissing {
			_, userExists := usersByID[topUp.UserId]
			if hasTopUp && !hasSubscription && !hasOrder && userExists && validateLegacyTopUpResolutionEvidence(event, &topUp) == nil {
				view.LegacyKind = PaymentOrderKindTopUp
				view.AvailableActions = []string{PaymentUnmatchedAvailableActionResolveLegacyTopUp}
			}
			views = append(views, view)
			continue
		}
		if event.ReviewCode == PaymentReviewCodeLegacySubscriptionContractUnavailable {
			_, userExists := usersByID[subscription.UserId]
			if hasSubscription && !hasTopUp && !hasOrder && userExists && legacySubscriptionRetryCurrentlySafe(event, &subscription) {
				view.LegacyKind = PaymentOrderKindSubscription
				view.AvailableActions = []string{PaymentUnmatchedEventActionRetryLegacy}
			} else if hasSubscription && !hasTopUp && !hasOrder && userExists && validateLegacySubscriptionRefundEvidence(event, &subscription) == nil {
				view.LegacyKind = PaymentOrderKindSubscription
				view.AvailableActions = []string{PaymentUnmatchedAvailableActionResolveLegacySubscription}
			}
			views = append(views, view)
			continue
		}
		if event.ReviewCode == PaymentReviewCodeEventKeyPayloadConflict {
			view.AvailableActions = []string{PaymentUnmatchedEventActionDismiss}
			views = append(views, view)
			continue
		}
		if hasSubscription && !hasTopUp && !hasOrder && event.ReviewCode == "" && event.Attempts > 0 &&
			legacySubscriptionRetryCurrentlySafe(event, &subscription) {
			view.LegacyKind = PaymentOrderKindSubscription
			view.AvailableActions = []string{PaymentUnmatchedEventActionRetryLegacy}
			views = append(views, view)
			continue
		}
		if hasOrder && paymentEventMayBeLinkedByAdmin(event) {
			view.AvailableActions = []string{PaymentUnmatchedEventActionLink, PaymentUnmatchedEventActionDismiss}
		} else if !hasTopUp && !hasSubscription {
			if paymentEventMayBeLinkedByAdmin(event) {
				view.AvailableActions = []string{PaymentUnmatchedEventActionLink, PaymentUnmatchedEventActionDismiss}
			} else {
				view.AvailableActions = []string{PaymentUnmatchedEventActionDismiss}
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func legacySubscriptionRetryCurrentlySafe(event *PaymentEvent, subscription *SubscriptionOrder) bool {
	if event == nil || subscription == nil || validateLegacyEpayEventEvidence(event) != nil {
		return false
	}
	prepared := *subscription
	var currentPlan *SubscriptionPlan
	if legacySubscriptionNeedsPlanReconstruction(&prepared) {
		var plan SubscriptionPlan
		if err := DB.Where("id = ?", prepared.PlanId).First(&plan).Error; err != nil {
			return false
		}
		currentPlan = &plan
	}
	plan, err := prepareLegacySubscriptionAdoptionContract(paymentEventInputFromStoredEvent(event), &prepared, currentPlan)
	if err != nil {
		return false
	}
	available, err := paymentCredentialGenerationAvailableTx(DB, event.Provider, event.ProviderCredentialGeneration,
		prepared.CreateTime, common.GetTimestamp())
	if err != nil || !available {
		return false
	}
	var userCount int64
	if err := DB.Model(&User{}).Where("id = ?", prepared.UserId).Count(&userCount).Error; err != nil || userCount != 1 {
		return false
	}
	if plan.MaxPurchasePerUser > 0 {
		usedSlots, err := countSubscriptionPurchaseSlotsTx(DB, prepared.UserId, plan.Id, prepared.Id, 0)
		if err != nil || usedSlots >= int64(plan.MaxPurchasePerUser) {
			return false
		}
	}
	return true
}

func paymentEventMayBeLinkedByAdmin(event *PaymentEvent) bool {
	if event == nil || event.Provider == "" || event.Provider == "admin" || event.PayloadDigest == "" ||
		event.PayloadDigest != PaymentPayloadDigest(event.NormalizedPayload) || !event.Paid || event.PaidAmountMinor <= 0 ||
		event.Failed || event.Expired || event.Refunded || event.Disputed || event.DisputeResolved || event.ManualReview {
		return false
	}
	if (event.Provider == PaymentProviderEpay || event.Provider == PaymentProviderStripe || event.Provider == PaymentProviderXorPay) &&
		event.ProviderCredentialGeneration <= 0 {
		return false
	}
	if event.Provider == PaymentProviderStripe && !stripePaidEventModeAllowed(paymentEventInputFromStoredEvent(event)) {
		return false
	}
	return true
}

func paymentEventAuditView(event *PaymentEvent) PaymentEventAuditView {
	if event == nil {
		return PaymentEventAuditView{}
	}
	return PaymentEventAuditView{
		ID: event.ID, Provider: event.Provider, EventKey: event.EventKey, EventType: event.EventType,
		TradeNo: event.TradeNo, PaymentOrderID: event.PaymentOrderID, ProviderLivemode: event.ProviderLivemode,
		ProviderCreatedAt: event.ProviderCreatedAt, ProviderState: event.ProviderState,
		PaidAmountMinor: event.PaidAmountMinor, RefundedAmountMinor: event.RefundedAmountMinor,
		DisputedAmountMinor: event.DisputedAmountMinor, Currency: event.Currency, PaymentMethod: event.PaymentMethod,
		Paid: event.Paid, Failed: event.Failed, Expired: event.Expired, Refunded: event.Refunded,
		Disputed: event.Disputed, DisputeResolved: event.DisputeResolved, DisputeWon: event.DisputeWon,
		PermanentFailure: event.PermanentFailure, ManualReview: event.ManualReview,
		PayloadDigest: event.PayloadDigest, Status: event.Status, ReviewCode: event.ReviewCode,
		Attempts: event.Attempts, LastError: event.LastError, CreatedAt: event.CreatedAt,
		ProcessedAt: event.ProcessedAt, UpdatedAt: event.UpdatedAt,
	}
}

func GetPaymentAuditDetail(tradeNo string) (*PaymentAuditDetail, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return nil, ErrPaymentOrderNotFound
	}
	var order PaymentOrder
	if err := DB.Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentOrderNotFound
		}
		return nil, err
	}
	detail := &PaymentAuditDetail{Order: &order}
	switch order.OrderKind {
	case PaymentOrderKindTopUp:
		var projection TopUp
		if err := DB.Select("review_reason").
			Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			First(&projection).Error; err == nil {
			detail.LegacyReviewReason = strings.TrimSpace(projection.ReviewReason)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	case PaymentOrderKindSubscription:
		var projection SubscriptionOrder
		if err := DB.Select("review_reason").
			Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
			First(&projection).Error; err == nil {
			detail.LegacyReviewReason = strings.TrimSpace(projection.ReviewReason)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	var events []PaymentEvent
	if err := DB.Where("payment_order_id = ? OR trade_no = ?", order.ID, order.TradeNo).
		Order("id asc").Find(&events).Error; err != nil {
		return nil, err
	}
	detail.Events = make([]PaymentEventAuditView, 0, len(events))
	for index := range events {
		detail.Events = append(detail.Events, paymentEventAuditView(&events[index]))
	}
	if err := DB.Where("payment_order_id = ?", order.ID).Order("id asc").Find(&detail.Debts).Error; err != nil {
		return nil, err
	}
	if err := DB.Where("payment_order_id = ?", order.ID).Order("id asc").Find(&detail.Ledger).Error; err != nil {
		return nil, err
	}
	if err := DB.Where("payment_order_id = ?", order.ID).Order("id asc").Find(&detail.Tasks).Error; err != nil {
		return nil, err
	}
	var limitReservation PaymentLimitReservation
	if err := DB.Where("payment_order_id = ?", order.ID).First(&limitReservation).Error; err == nil {
		detail.LimitReservation = &limitReservation
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if order.Provider == PaymentProviderStripe {
		if err := DB.Where("provider = ? AND user_id = ?", PaymentProviderStripe, order.UserID).
			Order("id asc").Find(&detail.CustomerBindings).Error; err != nil {
			return nil, err
		}
		if err := DB.Where("provider = ? AND user_id = ?", PaymentProviderStripe, order.UserID).
			Order("retired_at asc, id asc").Find(&detail.CustomerBindingRetirements).Error; err != nil {
			return nil, err
		}
	}
	if err := DB.Where("payment_order_id = ?", order.ID).Order("id asc").Find(&detail.OperationsAudits).Error; err != nil {
		return nil, err
	}
	return detail, nil
}

type PaymentDebtResolutionInput struct {
	DebtID                    int64
	AdminID                   int
	ActorIP                   string
	ExpectedOutstandingQuota  int64
	ExpectedOutstandingAmount int64
	Resolution                string
	Note                      string
}

type PaymentDebtResolutionResult struct {
	Debt      *PaymentDebt `json:"debt"`
	Duplicate bool         `json:"duplicate"`
}

func ResolvePaymentDebtByAdmin(input PaymentDebtResolutionInput) (*PaymentDebtResolutionResult, error) {
	input.Resolution = strings.ToLower(strings.TrimSpace(input.Resolution))
	input.Note = strings.TrimSpace(input.Note)
	input.ActorIP = strings.TrimSpace(input.ActorIP)
	if input.DebtID <= 0 || input.AdminID <= 0 || input.ActorIP == "" || len(input.ActorIP) > 64 ||
		input.ExpectedOutstandingQuota < 0 || input.ExpectedOutstandingAmount < 0 ||
		input.ExpectedOutstandingQuota == 0 && input.ExpectedOutstandingAmount == 0 ||
		(input.Resolution != "repaid" && input.Resolution != "waived") || len(input.Note) < 8 || len(input.Note) > 512 {
		return nil, ErrPaymentAuditInvalid
	}
	var debtReference PaymentDebt
	if err := DB.Select("payment_order_id").First(&debtReference, input.DebtID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPaymentAuditNotFound
		}
		return nil, err
	}
	result := &PaymentDebtResolutionResult{}
	var resolved PaymentDebt
	var affectedUserID int
	err := DB.Transaction(func(tx *gorm.DB) error {
		var order PaymentOrder
		if err := lockForUpdate(tx).First(&order, debtReference.PaymentOrderID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentAuditNotFound
			}
			return err
		}
		if err := lockForUpdate(tx).First(&resolved, input.DebtID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPaymentAuditNotFound
			}
			return err
		}
		result.Debt = &resolved
		if resolved.PaymentOrderID != order.ID {
			return fmt.Errorf("%w: payment debt order changed", ErrPaymentAuditConflict)
		}
		payload, err := paymentDebtResolutionPayload(input, &order, &resolved)
		if err != nil {
			return err
		}
		adminEventKey := "debt_resolved:" + strconv.FormatInt(input.DebtID, 10)
		duplicate, err := processedAdminActionRetryTx(tx, adminEventKey, PaymentPayloadDigest(payload))
		if err != nil {
			return err
		}
		if resolved.Status == PaymentDebtStatusResolved {
			if !duplicate {
				return fmt.Errorf("%w: resolved payment debt has no matching administrator event", ErrPaymentAuditConflict)
			}
			if err := validateCompletedPaymentDebtResolutionTx(tx, input, &order, &resolved, adminEventKey, payload); err != nil {
				return err
			}
			result.Duplicate = true
			return nil
		}
		if duplicate {
			return fmt.Errorf("%w: administrator event is terminal while payment debt remains open", ErrPaymentAuditConflict)
		}
		if resolved.Status != PaymentDebtStatusOpen {
			return fmt.Errorf("%w: payment debt is not open", ErrPaymentAuditConflict)
		}
		if resolved.OutstandingQuota != input.ExpectedOutstandingQuota || resolved.OutstandingAmountMinor != input.ExpectedOutstandingAmount {
			return fmt.Errorf("%w: payment debt changed; refresh before resolving", ErrPaymentAuditConflict)
		}
		resolvedQuota := resolved.OutstandingQuota
		resolvedAmount := resolved.OutstandingAmountMinor
		event, err := createAdminPaymentEventTx(tx, adminEventKey, PaymentOperationsActionDebtResolve, payload, &order)
		if err != nil {
			return err
		}
		now := common.GetTimestamp()
		resolved.OutstandingQuota = 0
		resolved.OutstandingAmountMinor = 0
		resolved.Status = PaymentDebtStatusResolved
		resolved.Resolution = input.Resolution
		resolved.ResolutionNote = input.Note
		resolved.ResolvedBy = input.AdminID
		resolved.ResolvedAt = now
		resolved.UpdatedAt = now
		if err := tx.Save(&resolved).Error; err != nil {
			return err
		}
		if err := createPaymentLedgerEntryTx(tx, event, &order, PaymentLedgerEntryDebtResolved,
			resolvedAmount, resolvedQuota, "payment debt "+input.Resolution+" by administrator"); err != nil {
			return err
		}
		if err := releasePaymentFreezeTx(tx, &resolved); err != nil {
			return err
		}
		hasOpenDebt, err := hasAnyOpenPaymentDebtTx(tx, order.ID)
		if err != nil {
			return err
		}
		if !hasOpenDebt {
			status := PaymentOrderStatusFulfilled
			switch {
			case order.DisputedAmountMinor > 0:
				status = PaymentOrderStatusDisputed
			case order.RefundedAmountMinor >= order.ExpectedAmountMinor:
				status = PaymentOrderStatusRefunded
			case order.RefundedAmountMinor > 0:
				status = PaymentOrderStatusRefundPending
			}
			if err := tx.Model(&PaymentOrder{}).Where("id = ?", order.ID).Updates(map[string]interface{}{
				"status": status, "status_reason": "", "updated_at": now, "version": gorm.Expr("version + ?", 1),
			}).Error; err != nil {
				return err
			}
			order.Status = status
			order.StatusReason = ""
			if err := syncPaymentProjectionStatusTx(tx, &order); err != nil {
				return err
			}
		}
		metadata := paymentDebtResolutionAuditMetadata(input, adminEventKey)
		if err := createPaymentOperationsAuditTx(tx, PaymentOperationsAudit{
			Action: PaymentOperationsActionDebtResolve, AdminID: input.AdminID, ActorIP: input.ActorIP,
			PaymentOrderID: order.ID, UserID: resolved.UserID, SubjectID: resolved.ID, Provider: order.Provider,
			Reason: input.Note,
		}, metadata); err != nil {
			return err
		}
		if err := finishPaymentEventTx(tx, event.ID, PaymentEventStatusProcessed, "", order.ID); err != nil {
			return err
		}
		result.Debt = &resolved
		affectedUserID = resolved.UserID
		return nil
	})
	if err != nil {
		return nil, err
	}
	if affectedUserID > 0 {
		_ = InvalidateUserCache(affectedUserID)
	}
	return result, nil
}

func paymentDebtResolutionPayload(input PaymentDebtResolutionInput, order *PaymentOrder, debt *PaymentDebt) (string, error) {
	if order == nil || debt == nil {
		return "", ErrPaymentAuditInvalid
	}
	encoded, err := common.Marshal(map[string]interface{}{
		"source": "admin", "action": PaymentOperationsActionDebtResolve,
		"admin_id": input.AdminID, "actor_ip": input.ActorIP, "debt_id": input.DebtID,
		"resolution": input.Resolution, "note": input.Note,
		"expected_outstanding_quota":        input.ExpectedOutstandingQuota,
		"expected_outstanding_amount_minor": input.ExpectedOutstandingAmount,
		"payment_order_id":                  order.ID, "trade_no": order.TradeNo, "provider": order.Provider,
		"user_id": debt.UserID, "debt_kind": debt.DebtKind,
	})
	return string(encoded), err
}

func paymentDebtResolutionAuditMetadata(input PaymentDebtResolutionInput, adminEventKey string) map[string]interface{} {
	return map[string]interface{}{
		"admin_event_key": adminEventKey, "resolution": input.Resolution,
		"expected_outstanding_quota":        input.ExpectedOutstandingQuota,
		"expected_outstanding_amount_minor": input.ExpectedOutstandingAmount,
	}
}

func validateCompletedPaymentDebtResolutionTx(tx *gorm.DB, input PaymentDebtResolutionInput, order *PaymentOrder,
	debt *PaymentDebt, adminEventKey, payload string) error {
	if tx == nil || order == nil || debt == nil || debt.Status != PaymentDebtStatusResolved || debt.OutstandingQuota != 0 ||
		debt.OutstandingAmountMinor != 0 || debt.Resolution != input.Resolution || debt.ResolutionNote != input.Note ||
		debt.ResolvedBy != input.AdminID || debt.ResolvedAt <= 0 {
		return fmt.Errorf("%w: completed payment debt resolution is inconsistent", ErrPaymentAuditConflict)
	}
	var event PaymentEvent
	if err := tx.Where("provider = ? AND event_key = ?", "admin", adminEventKey).First(&event).Error; err != nil {
		return err
	}
	if event.Status != PaymentEventStatusProcessed || event.PaymentOrderID != order.ID || event.TradeNo != order.TradeNo ||
		event.EventType != PaymentOperationsActionDebtResolve || event.PayloadDigest != PaymentPayloadDigest(payload) {
		return fmt.Errorf("%w: completed payment debt administrator event is inconsistent", ErrPaymentAuditConflict)
	}
	var ledgers []PaymentLedgerEntry
	if err := tx.Where("payment_order_id = ? AND payment_event_id = ? AND entry_type = ?",
		order.ID, event.ID, PaymentLedgerEntryDebtResolved).Find(&ledgers).Error; err != nil {
		return err
	}
	if len(ledgers) != 1 || ledgers[0].UserID != debt.UserID || ledgers[0].Currency != order.Currency ||
		ledgers[0].AmountMinor != input.ExpectedOutstandingAmount || ledgers[0].QuotaDelta != input.ExpectedOutstandingQuota {
		return fmt.Errorf("%w: completed payment debt ledger is inconsistent", ErrPaymentAuditConflict)
	}
	metadataBytes, err := common.Marshal(paymentDebtResolutionAuditMetadata(input, adminEventKey))
	if err != nil {
		return err
	}
	var audits []PaymentOperationsAudit
	if err := tx.Where("action = ? AND subject_id = ?", PaymentOperationsActionDebtResolve, debt.ID).Find(&audits).Error; err != nil {
		return err
	}
	if len(audits) != 1 {
		return fmt.Errorf("%w: payment debt operations audit is missing or duplicated", ErrPaymentAuditConflict)
	}
	audit := audits[0]
	if audit.AdminID != input.AdminID || audit.ActorIP != input.ActorIP || audit.PaymentOrderID != order.ID ||
		audit.UserID != debt.UserID || audit.Provider != order.Provider || audit.Reason != input.Note ||
		audit.Metadata != string(metadataBytes) {
		return fmt.Errorf("%w: payment debt operations audit payload changed", ErrPaymentAuditConflict)
	}
	return nil
}
