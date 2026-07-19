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
