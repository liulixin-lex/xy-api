package model

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createLegacyTopUpReviewEvent(t *testing.T, userID int, tradeNo string, money float64, paidMinor int64) (*TopUp, PaymentEventInput, *PaymentEvent) {
	t.Helper()
	seedPaymentUser(t, userID, 0)
	topUp := &TopUp{
		UserId: userID, Amount: 2, Money: money, TradeNo: tradeNo,
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp() - 100,
	}
	require.NoError(t, DB.Create(topUp).Error)
	paid := legacyEpayPaidInput(tradeNo, paidMinor, "alipay")
	paid.ProviderPaymentKey = "epay:g1:payment_" + tradeNo
	result, err := ProcessPaymentEvent(paid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	require.NotNil(t, result)
	assert.True(t, result.ManualReview)
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", paid.Provider, paid.EventKey).First(&event).Error)
	require.Equal(t, PaymentEventStatusManualReview, event.Status)
	require.Equal(t, PaymentReviewCodeLegacyTopUpQuotaSnapshotMissing, event.ReviewCode)
	return topUp, paid, &event
}

func TestResolveLegacyTopUpFulfillUsesExplicitQuotaAndIsStrictlyIdempotent(t *testing.T) {
	truncateTables(t)
	topUp, _, event := createLegacyTopUpReviewEvent(t, 976001, "LEGACY_TOPUP_EXPLICIT", 14.60, 1460)

	views, total, err := ListUnmatchedPaymentEventViewsPage(0, 50)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, views, 1)
	assert.Equal(t, PaymentOrderKindTopUp, views[0].LegacyKind)
	assert.Equal(t, []string{PaymentUnmatchedAvailableActionResolveLegacyTopUp}, views[0].AvailableActions)
	_, err = ResolveUnmatchedPaymentEventByAdmin(PaymentUnmatchedEventActionInput{
		EventID: event.ID, AdminID: 90, ActorIP: "192.0.2.90", Action: PaymentUnmatchedEventActionDismiss,
		Reason: "classified paid legacy events cannot use the generic dismissal path",
	})
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)

	originalQPU := common.QuotaPerUnit
	common.QuotaPerUnit = 9_999_999
	t.Cleanup(func() { common.QuotaPerUnit = originalQPU })
	input := PaymentLegacyTopUpResolutionInput{
		EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 91, ActorIP: "192.0.2.91",
		Resolution: PaymentLegacyTopUpResolutionFulfill, CreditQuota: 123_456,
		Reason: "historical invoice confirms this exact quota grant without affiliate reward",
	}
	result, err := ResolveLegacyTopUpPaymentEventByAdmin(input)
	require.NoError(t, err)
	require.NotNil(t, result.Order)
	assert.False(t, result.Duplicate)
	assert.Equal(t, PaymentOrderStatusFulfilled, result.Order.Status)
	assert.EqualValues(t, input.CreditQuota, result.Order.CreditQuota)

	var user User
	require.NoError(t, DB.First(&user, topUp.UserId).Error)
	assert.EqualValues(t, input.CreditQuota, user.Quota)
	var storedTopUp TopUp
	require.NoError(t, DB.First(&storedTopUp, topUp.Id).Error)
	require.NotNil(t, storedTopUp.PaymentOrderId)
	assert.Equal(t, common.TopUpStatusSuccess, storedTopUp.Status)
	var affiliateCount, creditCount, adminLedgerCount, auditCount int64
	require.NoError(t, DB.Model(&AffiliateRewardRecord{}).Where("top_up_id = ?", topUp.Id).Count(&affiliateCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID, PaymentLedgerEntryCredit).Count(&creditCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID, PaymentLedgerEntryAdminLegacyTopUp).Count(&adminLedgerCount).Error)
	require.NoError(t, DB.Model(&PaymentOperationsAudit{}).Where("action = ? AND subject_id = ?", PaymentOperationsActionLegacyTopUpFulfill, event.ID).Count(&auditCount).Error)
	assert.Zero(t, affiliateCount)
	assert.EqualValues(t, 1, creditCount)
	assert.EqualValues(t, 1, adminLedgerCount)
	assert.EqualValues(t, 1, auditCount)
	assert.Contains(t, result.Order.PricingSnapshot, `"provider_order_key"`)
	assert.Contains(t, result.Order.PricingSnapshot, `"admin_id":91`)

	duplicate, err := ResolveLegacyTopUpPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)
	changed := input
	changed.Reason = "a different administrator rationale must not reuse the terminal action"
	_, err = ResolveLegacyTopUpPaymentEventByAdmin(changed)
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
	changed = input
	changed.CreditQuota++
	_, err = ResolveLegacyTopUpPaymentEventByAdmin(changed)
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
}

func TestResolveLegacyTopUpExternalRefundNeverGrantsEntitlement(t *testing.T) {
	truncateTables(t)
	topUp, paid, event := createLegacyTopUpReviewEvent(t, 976002, "LEGACY_TOPUP_REFUND", 25, 2500)
	input := PaymentLegacyTopUpResolutionInput{
		EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 92, ActorIP: "192.0.2.92",
		Resolution: PaymentLegacyTopUpResolutionExternalRefund, ProviderRefundReference: "epay-refund-20260719-001",
		Reason: "provider dashboard confirms the full external refund completed successfully",
	}
	result, err := ResolveLegacyTopUpPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusRefunded, result.Order.Status)
	assert.Zero(t, result.Order.CreditQuota)
	assert.Zero(t, result.Order.PaidAmountMinor)
	assert.Equal(t, result.Order.ExpectedAmountMinor, result.Order.RefundedAmountMinor)

	var user User
	require.NoError(t, DB.First(&user, topUp.UserId).Error)
	assert.Zero(t, user.Quota)
	lateRefund := PaymentEventInput{
		Provider: PaymentProviderEpay, ProviderCredentialGeneration: 1,
		EventKey: "legacy-topup-provider-refund-confirmation", EventType: "refund.completed",
		TradeNo: topUp.TradeNo, ProviderOrderKey: paid.ProviderOrderKey, ProviderPaymentKey: paid.ProviderPaymentKey,
		RefundedAmountMinor: result.Order.ExpectedAmountMinor, Currency: "CNY", Refunded: true,
		NormalizedPayload: `{"refund":"completed"}`,
	}
	lateResult, err := ProcessPaymentEvent(lateRefund)
	require.NoError(t, err)
	require.NotNil(t, lateResult.Order)
	assert.Equal(t, PaymentOrderStatusRefunded, lateResult.Order.Status)
	require.NoError(t, DB.First(&user, topUp.UserId).Error)
	assert.Zero(t, user.Quota)
	var lateEvent PaymentEvent
	require.NoError(t, DB.Where("event_key = ?", lateRefund.EventKey).First(&lateEvent).Error)
	assert.Equal(t, PaymentEventStatusProcessed, lateEvent.Status)
	var grantCount, receiptCount, refundCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID, PaymentLedgerEntryCredit).Count(&grantCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID, PaymentLedgerEntryLegacyPaymentReceived).Count(&receiptCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID, PaymentLedgerEntryAdminExternalRefund).Count(&refundCount).Error)
	assert.Zero(t, grantCount)
	assert.EqualValues(t, 1, receiptCount)
	assert.EqualValues(t, 1, refundCount)
	var refundLedger PaymentLedgerEntry
	require.NoError(t, DB.Where("payment_order_id = ? AND entry_type = ?", result.Order.ID, PaymentLedgerEntryAdminExternalRefund).First(&refundLedger).Error)
	assert.LessOrEqual(t, len(refundLedger.Description), 255)
	assert.NotContains(t, refundLedger.Description, input.ProviderRefundReference)

	duplicate, err := ResolveLegacyTopUpPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)
	replayed, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	require.NotNil(t, replayed)
	assert.True(t, replayed.Duplicate)
	require.NoError(t, DB.First(&user, topUp.UserId).Error)
	assert.Zero(t, user.Quota)

	invalid := input
	invalid.EventID = event.ID + 100
	invalid.ProviderRefundReference = "bad\nreference"
	_, err = ResolveLegacyTopUpPaymentEventByAdmin(invalid)
	assert.ErrorIs(t, err, ErrPaymentAuditInvalid)
}

func TestExternalRefundReferenceCannotBeReusedAcrossResolutionFlows(t *testing.T) {
	truncateTables(t)
	const providerRefundReference = "shared-cross-flow-provider-refund"

	seedPaymentUser(t, 976020, 0)
	canonicalOrder := createTopUpPaymentOrder(t, 976020, PaymentProviderEpay, "alipay", 1200, 600)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", canonicalOrder.ID).
		Updates(map[string]interface{}{"status": PaymentOrderStatusManualReview, "status_reason": "requires review"}).Error)
	require.NoError(t, DB.Model(&TopUp{}).Where("payment_order_id = ?", canonicalOrder.ID).
		Update("status", common.TopUpStatusManualReview).Error)

	_, err := ResolvePaymentOrderByAdmin(PaymentAdminOrderActionInput{
		TradeNo: canonicalOrder.TradeNo, ExpectedVersion: canonicalOrder.Version,
		AdminID: 120, ActorIP: "192.0.2.120", Action: PaymentAdminActionExternalRefundConfirmed,
		Reason:              "provider dashboard confirms the canonical external refund completed",
		RefundedAmountMinor: canonicalOrder.ExpectedAmountMinor, ProviderRefundReference: providerRefundReference,
	})
	require.NoError(t, err)

	legacyTopUp, _, legacyEvent := createLegacyTopUpReviewEvent(t, 976021, "LEGACY_SHARED_REFUND", 12, 1200)
	_, err = ResolveLegacyTopUpPaymentEventByAdmin(PaymentLegacyTopUpResolutionInput{
		EventID: legacyEvent.ID, ExpectedEventAttempts: legacyEvent.Attempts,
		AdminID: 121, ActorIP: "192.0.2.121", Resolution: PaymentLegacyTopUpResolutionExternalRefund,
		ProviderRefundReference: providerRefundReference,
		Reason:                  "the same provider refund reference must not settle another payment",
	})
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)

	var storedLegacyEvent PaymentEvent
	require.NoError(t, DB.First(&storedLegacyEvent, legacyEvent.ID).Error)
	assert.Equal(t, PaymentEventStatusManualReview, storedLegacyEvent.Status)
	assert.Zero(t, storedLegacyEvent.PaymentOrderID)
	var storedLegacyTopUp TopUp
	require.NoError(t, DB.First(&storedLegacyTopUp, legacyTopUp.Id).Error)
	assert.Nil(t, storedLegacyTopUp.PaymentOrderId)
	assert.Equal(t, common.TopUpStatusPending, storedLegacyTopUp.Status)
	var legacyOrderCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", legacyTopUp.TradeNo).Count(&legacyOrderCount).Error)
	assert.Zero(t, legacyOrderCount)
	var legacyUser User
	require.NoError(t, DB.First(&legacyUser, legacyTopUp.UserId).Error)
	assert.Zero(t, legacyUser.Quota)

	seedPaymentUser(t, 976022, 0)
	legacySubscription := &SubscriptionOrder{
		UserId: 976022, PlanId: 986022, Money: 12, TradeNo: "LEGACY_SUB_SHARED_REFUND",
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp() - 100,
	}
	require.NoError(t, DB.Create(legacySubscription).Error)
	legacySubscriptionPaid := legacyEpayPaidInput(legacySubscription.TradeNo, 1200, "alipay")
	legacySubscriptionPaid.ProviderPaymentKey = "epay:g1:subscription_shared_refund"
	_, err = ProcessPaymentEvent(legacySubscriptionPaid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	var legacySubscriptionEvent PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", legacySubscriptionPaid.Provider,
		legacySubscriptionPaid.EventKey).First(&legacySubscriptionEvent).Error)
	require.Equal(t, PaymentReviewCodeLegacySubscriptionContractUnavailable, legacySubscriptionEvent.ReviewCode)
	_, err = ResolveLegacySubscriptionPaymentEventByAdmin(PaymentLegacySubscriptionResolutionInput{
		EventID: legacySubscriptionEvent.ID, ExpectedEventAttempts: legacySubscriptionEvent.Attempts,
		AdminID: 122, ActorIP: "192.0.2.122", Resolution: PaymentLegacyTopUpResolutionExternalRefund,
		ProviderRefundReference: providerRefundReference,
		Reason:                  "the same provider refund reference must not settle a legacy subscription",
	})
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
	require.NoError(t, DB.First(&legacySubscriptionEvent, legacySubscriptionEvent.ID).Error)
	assert.Equal(t, PaymentEventStatusManualReview, legacySubscriptionEvent.Status)
	assert.Zero(t, legacySubscriptionEvent.PaymentOrderID)
	require.NoError(t, DB.First(legacySubscription, legacySubscription.Id).Error)
	assert.Nil(t, legacySubscription.PaymentOrderId)
	var legacySubscriptionOrderCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", legacySubscription.TradeNo).
		Count(&legacySubscriptionOrderCount).Error)
	assert.Zero(t, legacySubscriptionOrderCount)

	var refundEventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).
		Where("provider = ? AND provider_resource_key = ?", "admin", PaymentProviderEpay+":refund:"+providerRefundReference).
		Count(&refundEventCount).Error)
	assert.EqualValues(t, 1, refundEventCount)
}

func TestResolveLegacyTopUpRollsBackAllEffectsWhenAuditInsertFails(t *testing.T) {
	truncateTables(t)
	topUp, _, event := createLegacyTopUpReviewEvent(t, 976003, "LEGACY_TOPUP_AUDIT_ROLLBACK", 10, 1000)
	require.NoError(t, DB.Exec(`CREATE TRIGGER fail_legacy_resolution_audit BEFORE INSERT ON payment_operations_audits BEGIN SELECT RAISE(FAIL, 'audit insert failed'); END`).Error)
	t.Cleanup(func() { _ = DB.Exec(`DROP TRIGGER IF EXISTS fail_legacy_resolution_audit`).Error })

	_, err := ResolveLegacyTopUpPaymentEventByAdmin(PaymentLegacyTopUpResolutionInput{
		EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 93, ActorIP: "192.0.2.93",
		Resolution: PaymentLegacyTopUpResolutionFulfill, CreditQuota: 1000,
		Reason: "verified evidence should roll back atomically when the audit cannot persist",
	})
	require.Error(t, err)
	var user User
	require.NoError(t, DB.First(&user, topUp.UserId).Error)
	assert.Zero(t, user.Quota)
	var storedTopUp TopUp
	require.NoError(t, DB.First(&storedTopUp, topUp.Id).Error)
	assert.Nil(t, storedTopUp.PaymentOrderId)
	assert.Equal(t, common.TopUpStatusPending, storedTopUp.Status)
	var storedEvent PaymentEvent
	require.NoError(t, DB.First(&storedEvent, event.ID).Error)
	assert.Equal(t, PaymentEventStatusManualReview, storedEvent.Status)
	assert.Equal(t, PaymentReviewCodeLegacyTopUpQuotaSnapshotMissing, storedEvent.ReviewCode)
	assert.Equal(t, event.Attempts, storedEvent.Attempts)
	var orderCount, ledgerCount, adminEventCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", topUp.TradeNo).Count(&orderCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Count(&ledgerCount).Error)
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider = ?", "admin").Count(&adminEventCount).Error)
	assert.Zero(t, orderCount)
	assert.Zero(t, ledgerCount)
	assert.Zero(t, adminEventCount)
}

func TestResolveLegacyTopUpConcurrentTerminalDecisionsCreateOneOutcome(t *testing.T) {
	truncateTables(t)
	topUp, _, event := createLegacyTopUpReviewEvent(t, 976004, "LEGACY_TOPUP_RACE", 18, 1800)
	inputs := []PaymentLegacyTopUpResolutionInput{
		{EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 94, ActorIP: "192.0.2.94", Resolution: PaymentLegacyTopUpResolutionFulfill, CreditQuota: 777, Reason: "verified historical quota evidence supports the explicit fulfillment outcome"},
		{EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 95, ActorIP: "192.0.2.95", Resolution: PaymentLegacyTopUpResolutionExternalRefund, ProviderRefundReference: "race-refund-001", Reason: "provider dashboard supports the completed external refund outcome instead"},
	}
	var wait sync.WaitGroup
	errorsSeen := make([]error, len(inputs))
	for index := range inputs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, errorsSeen[index] = ResolveLegacyTopUpPaymentEventByAdmin(inputs[index])
		}(index)
	}
	wait.Wait()
	successes := 0
	conflicts := 0
	for _, err := range errorsSeen {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrPaymentAuditConflict) {
			conflicts++
		}
	}
	assert.Equal(t, 1, successes, fmt.Sprintf("unexpected errors: %v", errorsSeen))
	assert.Equal(t, 1, conflicts, fmt.Sprintf("unexpected errors: %v", errorsSeen))
	var orders []PaymentOrder
	require.NoError(t, DB.Where("trade_no = ?", topUp.TradeNo).Find(&orders).Error)
	require.Len(t, orders, 1)
	var user User
	require.NoError(t, DB.First(&user, topUp.UserId).Error)
	if orders[0].Status == PaymentOrderStatusFulfilled {
		assert.Equal(t, 777, user.Quota)
	} else {
		assert.Equal(t, PaymentOrderStatusRefunded, orders[0].Status)
		assert.Zero(t, user.Quota)
	}
	var auditCount int64
	require.NoError(t, DB.Model(&PaymentOperationsAudit{}).Where("subject_id = ?", event.ID).Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)
}

func TestResolveLegacySubscriptionExternalRefundClosesUnreconstructablePayment(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 976010, 0)
	legacy := &SubscriptionOrder{
		UserId: 976010, PlanId: 986010, Money: 12.34, TradeNo: "LEGACY_SUB_REFUND_ONLY",
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp() - 100,
	}
	require.NoError(t, DB.Create(legacy).Error)
	paid := legacyEpayPaidInput(legacy.TradeNo, 1234, "alipay")
	paid.ProviderPaymentKey = "epay:g1:subscription_payment_001"
	_, err := ProcessPaymentEvent(paid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", paid.Provider, paid.EventKey).First(&event).Error)
	require.Equal(t, PaymentReviewCodeLegacySubscriptionContractUnavailable, event.ReviewCode)
	views, _, err := ListUnmatchedPaymentEventViewsPage(0, 50)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, []string{PaymentUnmatchedAvailableActionResolveLegacySubscription}, views[0].AvailableActions)

	input := PaymentLegacySubscriptionResolutionInput{
		EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 96, ActorIP: "192.0.2.96",
		Resolution: PaymentLegacyTopUpResolutionExternalRefund, ProviderRefundReference: "epay-sub-refund-001",
		Reason: "provider dashboard confirms the legacy subscription payment was fully refunded",
	}
	result, err := ResolveLegacySubscriptionPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusRefunded, result.Order.Status)
	assert.Empty(t, result.Order.ProductSnapshot)
	var user User
	require.NoError(t, DB.First(&user, legacy.UserId).Error)
	assert.Zero(t, user.Quota)
	var entitlementCount, grantLedgerCount, receiptCount, refundCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("payment_order_id = ?", result.Order.ID).Count(&entitlementCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID, PaymentLedgerEntrySubscriptionGranted).Count(&grantLedgerCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID, PaymentLedgerEntryLegacyPaymentReceived).Count(&receiptCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID, PaymentLedgerEntryAdminExternalRefund).Count(&refundCount).Error)
	assert.Zero(t, entitlementCount)
	assert.Zero(t, grantLedgerCount)
	assert.EqualValues(t, 1, receiptCount)
	assert.EqualValues(t, 1, refundCount)

	duplicate, err := ResolveLegacySubscriptionPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)
	replayed, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	require.NotNil(t, replayed)
	assert.True(t, replayed.Duplicate)
	require.NoError(t, DB.First(&user, legacy.UserId).Error)
	assert.Zero(t, user.Quota)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("payment_order_id = ?", result.Order.ID).Count(&entitlementCount).Error)
	assert.Zero(t, entitlementCount)
	lateRefund := PaymentEventInput{
		Provider: PaymentProviderEpay, ProviderCredentialGeneration: 1,
		EventKey: "legacy-subscription-provider-refund-confirmation", EventType: "refund.completed",
		TradeNo: legacy.TradeNo, ProviderOrderKey: paid.ProviderOrderKey, ProviderPaymentKey: paid.ProviderPaymentKey,
		RefundedAmountMinor: result.Order.ExpectedAmountMinor, Currency: "CNY", Refunded: true,
		NormalizedPayload: `{"refund":"completed"}`,
	}
	lateResult, err := ProcessPaymentEvent(lateRefund)
	require.NoError(t, err)
	require.NotNil(t, lateResult.Order)
	assert.Equal(t, PaymentOrderStatusRefunded, lateResult.Order.Status)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("payment_order_id = ?", result.Order.ID).Count(&entitlementCount).Error)
	assert.Zero(t, entitlementCount)
	changed := input
	changed.ProviderRefundReference = "epay-sub-refund-002"
	_, err = ResolveLegacySubscriptionPaymentEventByAdmin(changed)
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
}

func createStripeLegacyRecurringRefundReview(t *testing.T, userID int, suffix string, paidMinor int64) (*SubscriptionOrder, *PaymentEvent) {
	t.Helper()
	seedPaymentUser(t, userID, 0)
	customerID := "cus_legacy_refund_" + suffix
	seedStripeCustomerBinding(t, userID, customerID)
	tradeNo := "STRIPE_LEGACY_REFUND_" + suffix
	sessionID := "cs_legacy_refund_" + suffix
	subscriptionID := "sub_legacy_refund_" + suffix
	providerOrderKey := PaymentProviderStripe + ":" + sessionID
	legacy := &SubscriptionOrder{
		UserId: userID, PlanId: userID + 1000, Money: float64(paidMinor) / 100, TradeNo: tradeNo,
		ExpectedAmountMinor: paidMinor, PaymentCurrency: "USD", ProviderOrderId: sessionID,
		ProviderOrderKey: &providerOrderKey, PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp() - 100,
	}
	require.NoError(t, DB.Create(legacy).Error)
	inventoryUserID, inventoryPlanID := legacy.UserId, legacy.PlanId
	require.NoError(t, DB.Create(&StripeLegacySubscription{
		StripeSubscriptionID: subscriptionID, StripeCustomerID: customerID, CheckoutSessionID: sessionID,
		TradeNo: tradeNo, UserID: &inventoryUserID, SubscriptionPlanID: &inventoryPlanID,
		MappingStatus: StripeLegacyMappingMapped, Status: "active", CancelAtPeriodEnd: true, Livemode: true,
	}).Error)
	payload := common.GetJsonString(map[string]interface{}{
		"session_id": sessionID, "trade_no": tradeNo, "amount_total": paidMinor, "currency": "usd",
		"payment_status": "paid", "status": "complete", "mode": "subscription",
		"subscription_id": subscriptionID, "data_digest": PaymentPayloadDigest("stripe recurring object " + suffix),
	})
	input := PaymentEventInput{
		Provider: PaymentProviderStripe, EventKey: "evt_legacy_refund_" + suffix,
		EventType: "checkout.session.completed", TradeNo: tradeNo,
		ProviderOrderKey: providerOrderKey, ProviderResourceKey: PaymentProviderStripe + ":" + subscriptionID,
		ProviderCredentialGeneration: 1, ProviderLivemode: livePaymentModeForTest(),
		ProviderCreatedAt: common.GetTimestamp() - 90, ProviderState: PaymentProviderStateStripeLegacyRecurringCheckoutPaid,
		CustomerID: customerID, PaidAmountMinor: paidMinor, Currency: "USD", PaymentMethod: PaymentMethodStripe,
		ManualReview: true, NormalizedPayload: payload,
	}
	require.NoError(t, RecordPaymentEventManualReview(input, "verified recurring Checkout requires external refund review"))
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderStripe, input.EventKey).First(&event).Error)
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("id = ?", event.ID).
		Update("review_code", PaymentReviewCodeStripeLegacyRecurringCheckoutPaid).Error)
	require.NoError(t, DB.First(&event, event.ID).Error)
	return legacy, &event
}

func TestResolveStripeLegacyRecurringCheckoutExternalRefundIsStrictlyIdempotent(t *testing.T) {
	truncateTables(t)
	legacy, event := createStripeLegacyRecurringRefundReview(t, 976040, "success", 1250)
	views, total, err := ListUnmatchedPaymentEventViewsPage(0, 50)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, views, 1)
	assert.Equal(t, PaymentOrderKindSubscription, views[0].LegacyKind)
	assert.Equal(t, []string{PaymentUnmatchedAvailableActionResolveLegacySubscription}, views[0].AvailableActions)

	input := PaymentLegacySubscriptionResolutionInput{
		EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 140, ActorIP: "192.0.2.140",
		Resolution: PaymentLegacyTopUpResolutionExternalRefund, ProviderRefundReference: "re_success_001",
		Reason: "Stripe dashboard confirms the recurring Checkout charge was fully refunded",
	}
	result, err := ResolveLegacySubscriptionPaymentEventByAdmin(input)
	require.NoError(t, err)
	require.NotNil(t, result.Order)
	assert.False(t, result.Duplicate)
	assert.Equal(t, PaymentProviderStripe, result.Order.Provider)
	assert.Equal(t, PaymentOrderStatusRefunded, result.Order.Status)
	assert.Equal(t, event.ProviderCredentialGeneration, result.Order.ProviderCredentialGeneration)
	require.NotNil(t, result.Order.ProviderLivemode)
	assert.True(t, *result.Order.ProviderLivemode)
	assert.Equal(t, event.ProviderOrderKey, *result.Order.ProviderOrderKey)
	assert.Equal(t, event.PaidAmountMinor, result.Order.RefundedAmountMinor)
	assert.Zero(t, result.Order.PaidAmountMinor)
	assert.Zero(t, result.Order.SettledAt)

	var storedLegacy SubscriptionOrder
	require.NoError(t, DB.First(&storedLegacy, legacy.Id).Error)
	require.NotNil(t, storedLegacy.PaymentOrderId)
	assert.Equal(t, result.Order.ID, *storedLegacy.PaymentOrderId)
	assert.Equal(t, common.TopUpStatusRefunded, storedLegacy.Status)
	assert.Positive(t, storedLegacy.CompleteTime)
	var user User
	require.NoError(t, DB.First(&user, legacy.UserId).Error)
	assert.Zero(t, user.Quota)
	var entitlementCount, grantLedgerCount, receiptCount, refundCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", legacy.UserId).Count(&entitlementCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID,
		PaymentLedgerEntrySubscriptionGranted).Count(&grantLedgerCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID,
		PaymentLedgerEntryLegacyPaymentReceived).Count(&receiptCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", result.Order.ID,
		PaymentLedgerEntryAdminExternalRefund).Count(&refundCount).Error)
	assert.Zero(t, entitlementCount)
	assert.Zero(t, grantLedgerCount)
	assert.EqualValues(t, 1, receiptCount)
	assert.EqualValues(t, 1, refundCount)
	var adminEvent PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND provider_resource_key = ?", "admin",
		PaymentProviderStripe+":refund:"+input.ProviderRefundReference).First(&adminEvent).Error)
	assert.True(t, adminEvent.Refunded)
	assert.Equal(t, event.PaidAmountMinor, adminEvent.RefundedAmountMinor)

	duplicate, err := ResolveLegacySubscriptionPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)
	assert.Equal(t, result.Order.ID, duplicate.Order.ID)
	var orderCount, ledgerCount, auditCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", legacy.TradeNo).Count(&orderCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ?", result.Order.ID).Count(&ledgerCount).Error)
	require.NoError(t, DB.Model(&PaymentOperationsAudit{}).Where("action = ? AND subject_id = ?",
		PaymentOperationsActionLegacySubscriptionRefund, event.ID).Count(&auditCount).Error)
	assert.EqualValues(t, 1, orderCount)
	assert.EqualValues(t, 2, ledgerCount)
	assert.EqualValues(t, 1, auditCount)
}

func TestResolveStripeLegacyRecurringCheckoutRefundRejectsChangedContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *SubscriptionOrder, *PaymentEvent)
	}{
		{name: "provider", mutate: func(t *testing.T, legacy *SubscriptionOrder, _ *PaymentEvent) {
			require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("id = ?", legacy.Id).
				Update("payment_provider", PaymentProviderEpay).Error)
		}},
		{name: "amount", mutate: func(t *testing.T, legacy *SubscriptionOrder, _ *PaymentEvent) {
			require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("id = ?", legacy.Id).Update("money", 13.75).Error)
		}},
		{name: "Checkout Session identity", mutate: func(t *testing.T, legacy *SubscriptionOrder, _ *PaymentEvent) {
			require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("id = ?", legacy.Id).
				Updates(map[string]interface{}{"provider_order_id": "cs_other", "provider_order_key": "stripe:cs_other"}).Error)
		}},
		{name: "subscription can still renew", mutate: func(t *testing.T, _ *SubscriptionOrder, event *PaymentEvent) {
			subscriptionID := strings.TrimPrefix(event.ProviderResourceKey, PaymentProviderStripe+":")
			require.NoError(t, DB.Model(&StripeLegacySubscription{}).Where("stripe_subscription_id = ?", subscriptionID).
				Updates(map[string]interface{}{"cancel_at_period_end": false, "ended_at": 0, "status": "active"}).Error)
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			legacy, event := createStripeLegacyRecurringRefundReview(t, 976050+index, fmt.Sprintf("changed_%d", index), 1250)
			test.mutate(t, legacy, event)
			_, err := ResolveLegacySubscriptionPaymentEventByAdmin(PaymentLegacySubscriptionResolutionInput{
				EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 150 + index,
				ActorIP: fmt.Sprintf("192.0.2.%d", 150+index), Resolution: PaymentLegacyTopUpResolutionExternalRefund,
				ProviderRefundReference: fmt.Sprintf("re_changed_%03d", index),
				Reason:                  "changed provider evidence must not create a refunded canonical order",
			})
			assert.ErrorIs(t, err, ErrPaymentAuditConflict)
			var orderCount, entitlementCount int64
			require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", legacy.TradeNo).Count(&orderCount).Error)
			require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", legacy.UserId).Count(&entitlementCount).Error)
			assert.Zero(t, orderCount)
			assert.Zero(t, entitlementCount)
			var stored SubscriptionOrder
			require.NoError(t, DB.First(&stored, legacy.Id).Error)
			assert.Nil(t, stored.PaymentOrderId)
			assert.Equal(t, common.TopUpStatusPending, stored.Status)
		})
	}
}

func TestResolveStripeLegacyRecurringCheckoutRefundReferenceCannotBeReused(t *testing.T) {
	truncateTables(t)
	firstLegacy, firstEvent := createStripeLegacyRecurringRefundReview(t, 976060, "reference_first", 1000)
	secondLegacy, secondEvent := createStripeLegacyRecurringRefundReview(t, 976061, "reference_second", 1000)
	const refundReference = "re_shared_legacy_recurring_001"
	_, err := ResolveLegacySubscriptionPaymentEventByAdmin(PaymentLegacySubscriptionResolutionInput{
		EventID: firstEvent.ID, ExpectedEventAttempts: firstEvent.Attempts, AdminID: 160, ActorIP: "192.0.2.160",
		Resolution: PaymentLegacyTopUpResolutionExternalRefund, ProviderRefundReference: refundReference,
		Reason: "first Stripe recurring Checkout external refund is confirmed in the provider dashboard",
	})
	require.NoError(t, err)
	_, err = ResolveLegacySubscriptionPaymentEventByAdmin(PaymentLegacySubscriptionResolutionInput{
		EventID: secondEvent.ID, ExpectedEventAttempts: secondEvent.Attempts, AdminID: 161, ActorIP: "192.0.2.161",
		Resolution: PaymentLegacyTopUpResolutionExternalRefund, ProviderRefundReference: refundReference,
		Reason: "a provider refund reference cannot be applied to a second recurring Checkout",
	})
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
	var firstOrderCount, secondOrderCount, secondEntitlementCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", firstLegacy.TradeNo).Count(&firstOrderCount).Error)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", secondLegacy.TradeNo).Count(&secondOrderCount).Error)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", secondLegacy.UserId).Count(&secondEntitlementCount).Error)
	assert.EqualValues(t, 1, firstOrderCount)
	assert.Zero(t, secondOrderCount)
	assert.Zero(t, secondEntitlementCount)
	var storedSecond SubscriptionOrder
	require.NoError(t, DB.First(&storedSecond, secondLegacy.Id).Error)
	assert.Nil(t, storedSecond.PaymentOrderId)
	assert.Equal(t, common.TopUpStatusPending, storedSecond.Status)
}

func TestLegacySubscriptionRefundRejectsChangedProviderOrderIdentity(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 976011, 0)
	providerOrderKey := "epay:g1:gateway_LEGACY_SUB_IDENTITY"
	legacy := &SubscriptionOrder{
		UserId: 976011, PlanId: 986011, Money: 10, TradeNo: "LEGACY_SUB_IDENTITY",
		PaymentMethod: "alipay", ProviderOrderKey: &providerOrderKey,
		Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp() - 100,
	}
	require.NoError(t, DB.Create(legacy).Error)
	paid := legacyEpayPaidInput(legacy.TradeNo, 1000, "alipay")
	_, err := ProcessPaymentEvent(paid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	var event PaymentEvent
	require.NoError(t, DB.Where("event_key = ?", paid.EventKey).First(&event).Error)
	require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("id = ?", legacy.Id).
		Update("provider_order_key", "epay:g1:different_object").Error)
	_, err = ResolveLegacySubscriptionPaymentEventByAdmin(PaymentLegacySubscriptionResolutionInput{
		EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 97, ActorIP: "192.0.2.97",
		Resolution: PaymentLegacyTopUpResolutionExternalRefund, ProviderRefundReference: "identity-refund-001",
		Reason: "the provider identity must remain exact before recording a refund",
	})
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
	var orderCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", legacy.TradeNo).Count(&orderCount).Error)
	assert.Zero(t, orderCount)
}

func TestResolveLegacySubscriptionRollsBackWhenAuditInsertFails(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 976012, 0)
	legacy := &SubscriptionOrder{
		UserId: 976012, PlanId: 986012, Money: 11, TradeNo: "LEGACY_SUB_AUDIT_ROLLBACK",
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp() - 100,
	}
	require.NoError(t, DB.Create(legacy).Error)
	paid := legacyEpayPaidInput(legacy.TradeNo, 1100, "alipay")
	_, err := ProcessPaymentEvent(paid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	var event PaymentEvent
	require.NoError(t, DB.Where("event_key = ?", paid.EventKey).First(&event).Error)
	require.NoError(t, DB.Exec(`CREATE TRIGGER fail_legacy_subscription_audit BEFORE INSERT ON payment_operations_audits BEGIN SELECT RAISE(FAIL, 'audit insert failed'); END`).Error)
	t.Cleanup(func() { _ = DB.Exec(`DROP TRIGGER IF EXISTS fail_legacy_subscription_audit`).Error })

	_, err = ResolveLegacySubscriptionPaymentEventByAdmin(PaymentLegacySubscriptionResolutionInput{
		EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 99, ActorIP: "192.0.2.99",
		Resolution: PaymentLegacyTopUpResolutionExternalRefund, ProviderRefundReference: "sub-audit-rollback-001",
		Reason: "the full subscription resolution must roll back when audit persistence fails",
	})
	require.Error(t, err)
	var storedEvent PaymentEvent
	require.NoError(t, DB.First(&storedEvent, event.ID).Error)
	assert.Equal(t, PaymentEventStatusManualReview, storedEvent.Status)
	assert.Equal(t, PaymentReviewCodeLegacySubscriptionContractUnavailable, storedEvent.ReviewCode)
	assert.Equal(t, event.Attempts, storedEvent.Attempts)
	var storedSubscription SubscriptionOrder
	require.NoError(t, DB.First(&storedSubscription, legacy.Id).Error)
	assert.Nil(t, storedSubscription.PaymentOrderId)
	assert.Equal(t, common.TopUpStatusPending, storedSubscription.Status)
	var orderCount, ledgerCount, adminEventCount, entitlementCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", legacy.TradeNo).Count(&orderCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Count(&ledgerCount).Error)
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider = ?", "admin").Count(&adminEventCount).Error)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", legacy.UserId).Count(&entitlementCount).Error)
	assert.Zero(t, orderCount)
	assert.Zero(t, ledgerCount)
	assert.Zero(t, adminEventCount)
	assert.Zero(t, entitlementCount)
}

func TestResolveLegacySubscriptionConcurrentRetriesCreateOneTerminalAction(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 976013, 0)
	legacy := &SubscriptionOrder{
		UserId: 976013, PlanId: 986013, Money: 13, TradeNo: "LEGACY_SUB_CONCURRENT_REFUND",
		PaymentMethod: "alipay", Status: common.TopUpStatusPending, CreateTime: common.GetTimestamp() - 100,
	}
	require.NoError(t, DB.Create(legacy).Error)
	paid := legacyEpayPaidInput(legacy.TradeNo, 1300, "alipay")
	_, err := ProcessPaymentEvent(paid)
	require.ErrorIs(t, err, ErrPaymentManualReview)
	var event PaymentEvent
	require.NoError(t, DB.Where("event_key = ?", paid.EventKey).First(&event).Error)
	input := PaymentLegacySubscriptionResolutionInput{
		EventID: event.ID, ExpectedEventAttempts: event.Attempts, AdminID: 100, ActorIP: "192.0.2.100",
		Resolution: PaymentLegacyTopUpResolutionExternalRefund, ProviderRefundReference: "sub-concurrent-refund-001",
		Reason: "concurrent retries must converge on one externally refunded terminal action",
	}
	results := make([]*PaymentUnmatchedEventActionResult, 2)
	errorsSeen := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errorsSeen[index] = ResolveLegacySubscriptionPaymentEventByAdmin(input)
		}(index)
	}
	wait.Wait()
	duplicates := 0
	for index, result := range results {
		require.NoError(t, errorsSeen[index])
		require.NotNil(t, result)
		if result.Duplicate {
			duplicates++
		}
	}
	assert.Equal(t, 1, duplicates)
	var orderCount, auditCount, refundLedgerCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("trade_no = ?", legacy.TradeNo).Count(&orderCount).Error)
	require.NoError(t, DB.Model(&PaymentOperationsAudit{}).Where("action = ? AND subject_id = ?", PaymentOperationsActionLegacySubscriptionRefund, event.ID).Count(&auditCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("entry_type = ?", PaymentLedgerEntryAdminExternalRefund).Count(&refundLedgerCount).Error)
	assert.EqualValues(t, 1, orderCount)
	assert.EqualValues(t, 1, auditCount)
	assert.EqualValues(t, 1, refundLedgerCount)
}

func TestPaymentEventPayloadConflictReviewCodeCannotBeWashedAway(t *testing.T) {
	truncateTables(t)
	input := PaymentEventInput{
		Provider: PaymentProviderEpay, EventKey: "payload-conflict-sticky", EventType: "paid",
		TradeNo: "PAYLOAD_CONFLICT_STICKY", ProviderOrderKey: "epay:g1:sticky", ProviderCredentialGeneration: 1,
		PaidAmountMinor: 100, Currency: "CNY", PaymentMethod: "alipay", Paid: true,
		NormalizedPayload: `{"amount":100}`,
	}
	require.NoError(t, RecordPaymentEventManualReview(input, "first verified payload requires review"))
	conflict := input
	conflict.NormalizedPayload = `{"amount":101}`
	require.NoError(t, RecordPaymentEventManualReview(conflict, "conflicting payload"))
	require.NoError(t, RecordPaymentEventManualReview(input, "original payload replay"))
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", input.Provider, input.EventKey).First(&event).Error)
	assert.Equal(t, PaymentReviewCodeEventKeyPayloadConflict, event.ReviewCode)
	assert.Equal(t, "event key was reused with a different payload", event.LastError)
}

func TestPaymentAuditSerializationNeverIncludesNormalizedPayload(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 976020, 0)
	order := &PaymentOrder{
		TradeNo: "AUDIT_SAFE_DTO", UserID: 976020, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderEpay, PaymentMethod: "alipay", RequestID: "audit-safe-dto-request",
		ExpectedAmountMinor: 100, Currency: "CNY", Status: PaymentOrderStatusManualReview,
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(), Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	event := &PaymentEvent{
		Provider: PaymentProviderEpay, EventKey: "audit-safe-dto-event", EventType: "paid", TradeNo: order.TradeNo,
		PaymentOrderID: order.ID, PayloadDigest: PaymentPayloadDigest(`{"secret":"never-return"}`),
		NormalizedPayload: `{"secret":"never-return"}`, Status: PaymentEventStatusManualReview,
	}
	require.NoError(t, DB.Create(event).Error)
	detail, err := GetPaymentAuditDetail(order.TradeNo)
	require.NoError(t, err)
	encoded, err := common.Marshal(detail)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "normalized_payload")
	assert.NotContains(t, string(encoded), "never-return")
	rawEvent, err := common.Marshal(event)
	require.NoError(t, err)
	assert.NotContains(t, string(rawEvent), "normalized_payload")
	assert.NotContains(t, string(rawEvent), "never-return")
}

func TestResolvePaymentDebtUsesFullSemanticIdempotency(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 976030, 0)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 976030).Updates(map[string]interface{}{
		"payment_frozen": true, "status": common.UserStatusDisabled,
	}).Error)
	order := &PaymentOrder{
		TradeNo: "DEBT_SEMANTIC_IDEMPOTENCY", UserID: 976030, OrderKind: PaymentOrderKindTopUp,
		Provider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, RequestID: "debt-semantic-request",
		ExpectedAmountMinor: 1000, PaidAmountMinor: 1000, Currency: "USD", CreditQuota: 500,
		Status: PaymentOrderStatusDebt, CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(), Version: 1,
	}
	require.NoError(t, DB.Create(order).Error)
	require.NoError(t, DB.Create(&TopUp{
		PaymentOrderId: &order.ID, UserId: order.UserID, Amount: 1, Money: 10, TradeNo: order.TradeNo,
		PaymentMethod: order.PaymentMethod, PaymentProvider: order.Provider, Status: PaymentOrderStatusDebt,
		CreateTime: common.GetTimestamp() - 100,
	}).Error)
	debt := &PaymentDebt{
		PaymentOrderID: order.ID, UserID: order.UserID, DebtKind: PaymentDebtKindBuyer, Currency: order.Currency,
		OriginalAmountMinor: 300, OutstandingAmountMinor: 300, OriginalQuota: 150, OutstandingQuota: 150,
		PreviousUserStatus: common.UserStatusEnabled, FreezeApplied: true, Status: PaymentDebtStatusOpen,
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}
	require.NoError(t, DB.Create(debt).Error)
	input := PaymentDebtResolutionInput{
		DebtID: debt.ID, AdminID: 98, ActorIP: "192.0.2.98", ExpectedOutstandingQuota: 150,
		ExpectedOutstandingAmount: 300, Resolution: "waived", Note: "verified debt waiver approved by financial operations",
	}
	result, err := ResolvePaymentDebtByAdmin(input)
	require.NoError(t, err)
	assert.False(t, result.Duplicate)
	duplicate, err := ResolvePaymentDebtByAdmin(input)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)
	for name, mutate := range map[string]func(*PaymentDebtResolutionInput){
		"note": func(changed *PaymentDebtResolutionInput) {
			changed.Note = "a different waiver note must conflict with the original action"
		},
		"actor ip":        func(changed *PaymentDebtResolutionInput) { changed.ActorIP = "192.0.2.99" },
		"expected quota":  func(changed *PaymentDebtResolutionInput) { changed.ExpectedOutstandingQuota++ },
		"expected amount": func(changed *PaymentDebtResolutionInput) { changed.ExpectedOutstandingAmount++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := input
			mutate(&changed)
			_, err := ResolvePaymentDebtByAdmin(changed)
			assert.ErrorIs(t, err, ErrPaymentAuditConflict)
		})
	}
	var eventCount, ledgerCount, auditCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider = ? AND event_type = ?", "admin", PaymentOperationsActionDebtResolve).Count(&eventCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryDebtResolved).Count(&ledgerCount).Error)
	require.NoError(t, DB.Model(&PaymentOperationsAudit{}).Where("action = ? AND subject_id = ?", PaymentOperationsActionDebtResolve, debt.ID).Count(&auditCount).Error)
	assert.EqualValues(t, 1, eventCount)
	assert.EqualValues(t, 1, ledgerCount)
	assert.EqualValues(t, 1, auditCount)
}

func TestResolvePaymentDebtRejectsInvalidExpectedBalances(t *testing.T) {
	for _, test := range []struct {
		quota  int64
		amount int64
	}{
		{quota: -1, amount: 1},
		{quota: 1, amount: -1},
		{quota: 0, amount: 0},
	} {
		_, err := ResolvePaymentDebtByAdmin(PaymentDebtResolutionInput{
			DebtID: 1, AdminID: 1, ActorIP: "192.0.2.1", ExpectedOutstandingQuota: test.quota,
			ExpectedOutstandingAmount: test.amount, Resolution: "waived", Note: "valid length resolution note",
		})
		assert.ErrorIs(t, err, ErrPaymentAuditInvalid)
	}
}

func TestPaymentProviderReferenceRejectsControlCharactersAndOversizeUTF8(t *testing.T) {
	assert.True(t, validPaymentProviderReference("退款-ref_123/ABC"))
	assert.False(t, validPaymentProviderReference("refund\x00reference"))
	assert.False(t, validPaymentProviderReference("refund\r\nreference"))
	assert.False(t, validPaymentProviderReference(strings.Repeat("界", 86)))
}
