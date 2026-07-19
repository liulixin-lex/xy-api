package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentAdminOrderActionsWriteOneDurableOperationsAudit(t *testing.T) {
	tests := []struct {
		name           string
		action         string
		auditAction    string
		expectedStatus string
	}{
		{name: "fulfill", action: PaymentAdminActionFulfill, auditAction: PaymentOperationsActionAdminFulfill, expectedStatus: PaymentOrderStatusFulfilled},
		{name: "reject", action: PaymentAdminActionReject, auditAction: PaymentOperationsActionAdminReject, expectedStatus: PaymentOrderStatusFailed},
		{name: "void", action: PaymentAdminActionVoid, auditAction: PaymentOperationsActionAdminVoid, expectedStatus: PaymentOrderStatusFailed},
		{name: "external refund", action: PaymentAdminActionExternalRefundConfirmed, auditAction: PaymentOperationsActionAdminExternalRefund, expectedStatus: PaymentOrderStatusRefunded},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			userID := 973100 + index
			seedPaymentUser(t, userID, 0)
			order := createTopUpPaymentOrder(t, userID, PaymentProviderEpay, "alipay", 1000, 500)
			reason := "provider evidence reviewed for durable administrator action"
			actorIP := fmt.Sprintf("192.0.2.%d", 100+index)
			require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
				Updates(map[string]interface{}{"status": PaymentOrderStatusManualReview, "status_reason": "requires review"}).Error)
			require.NoError(t, DB.Model(&TopUp{}).Where("payment_order_id = ?", order.ID).
				Update("status", common.TopUpStatusManualReview).Error)

			input := PaymentAdminOrderActionInput{
				TradeNo: order.TradeNo, ExpectedVersion: order.Version, AdminID: 70 + index,
				ActorIP: actorIP, Action: test.action, Reason: reason,
			}
			if test.action == PaymentAdminActionExternalRefundConfirmed {
				input.RefundedAmountMinor = order.ExpectedAmountMinor
			}
			result, err := ResolvePaymentOrderByAdmin(input)
			require.NoError(t, err)
			assert.Equal(t, test.expectedStatus, result.Order.Status)

			var audit PaymentOperationsAudit
			require.NoError(t, DB.Where("action = ? AND payment_order_id = ? AND expected_version = ?",
				test.auditAction, order.ID, input.ExpectedVersion).First(&audit).Error)
			assert.Equal(t, input.AdminID, audit.AdminID)
			assert.Equal(t, actorIP, audit.ActorIP)
			assert.Equal(t, reason, audit.Reason)
			assert.EqualValues(t, order.ID, audit.SubjectID)
			var metadata map[string]interface{}
			require.NoError(t, common.UnmarshalJsonStr(audit.Metadata, &metadata))
			assert.Equal(t, order.TradeNo, metadata["trade_no"])
			assert.NotEmpty(t, metadata["admin_event_key"])

			duplicate, err := ResolvePaymentOrderByAdmin(input)
			require.NoError(t, err)
			assert.True(t, duplicate.Duplicate)
			var auditCount int64
			require.NoError(t, DB.Model(&PaymentOperationsAudit{}).
				Where("action = ? AND payment_order_id = ?", test.auditAction, order.ID).Count(&auditCount).Error)
			assert.EqualValues(t, 1, auditCount)
		})
	}
}

func TestPaymentAdminActionRollsBackWhenOperationsAuditCannotPersist(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 973109, 0)
	order := createTopUpPaymentOrder(t, 973109, PaymentProviderEpay, "alipay", 1000, 500)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
		Updates(map[string]interface{}{"status": PaymentOrderStatusManualReview, "status_reason": "requires review"}).Error)
	require.NoError(t, DB.Model(&TopUp{}).Where("payment_order_id = ?", order.ID).
		Update("status", common.TopUpStatusManualReview).Error)
	require.NoError(t, DB.Exec(`CREATE TRIGGER fail_payment_operations_audit BEFORE INSERT ON payment_operations_audits BEGIN SELECT RAISE(FAIL, 'audit insert failed'); END`).Error)
	t.Cleanup(func() { _ = DB.Exec(`DROP TRIGGER IF EXISTS fail_payment_operations_audit`).Error })

	_, err := ResolvePaymentOrderByAdmin(PaymentAdminOrderActionInput{
		TradeNo: order.TradeNo, ExpectedVersion: order.Version, AdminID: 79, ActorIP: "192.0.2.179",
		Action: PaymentAdminActionReject, Reason: "provider dashboard confirms no successful payment",
	})
	require.Error(t, err)

	var stored PaymentOrder
	require.NoError(t, DB.First(&stored, order.ID).Error)
	assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
	assert.EqualValues(t, order.Version, stored.Version)
	var eventCount, ledgerCount, auditCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider = ? AND payment_order_id = ?", "admin", order.ID).Count(&eventCount).Error)
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryAdminReject).Count(&ledgerCount).Error)
	require.NoError(t, DB.Model(&PaymentOperationsAudit{}).Where("payment_order_id = ?", order.ID).Count(&auditCount).Error)
	assert.Zero(t, eventCount)
	assert.Zero(t, ledgerCount)
	assert.Zero(t, auditCount)
}

func TestAdminRejectReleasesSubscriptionPurchaseReservationIdempotently(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 973001, 0)
	plan := &SubscriptionPlan{
		Id: 983001, Title: "Manual review reservation", PriceAmount: 10, Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		TotalAmount: 1000, MaxPurchasePerUser: 1,
	}
	require.NoError(t, DB.Create(plan).Error)
	order := createSubscriptionPaymentOrder(t, 973001, plan, 1000)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
		Updates(map[string]interface{}{"status": PaymentOrderStatusManualReview, "status_reason": "amount requires review"}).Error)
	require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("payment_order_id = ?", order.ID).
		Updates(map[string]interface{}{"status": SubscriptionOrderStatusManualReview, "review_reason": "amount requires review"}).Error)

	before, err := CountUserSubscriptionPurchasesByPlan(973001, plan.Id)
	require.NoError(t, err)
	assert.EqualValues(t, 1, before)

	input := PaymentAdminOrderActionInput{
		TradeNo: order.TradeNo, ExpectedVersion: order.Version, AdminID: 1, ActorIP: "192.0.2.1",
		Action: PaymentAdminActionReject, Reason: "provider dashboard confirms no successful charge",
	}
	result, err := ResolvePaymentOrderByAdmin(input)
	require.NoError(t, err)
	assert.False(t, result.Duplicate)
	assert.Equal(t, PaymentOrderStatusFailed, result.Order.Status)

	after, err := CountUserSubscriptionPurchasesByPlan(973001, plan.Id)
	require.NoError(t, err)
	assert.Zero(t, after)
	var projection SubscriptionOrder
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
	assert.Equal(t, common.TopUpStatusFailed, projection.Status)
	assert.NotZero(t, projection.CompleteTime)

	duplicate, err := ResolvePaymentOrderByAdmin(input)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)
	var ledgerCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).
		Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryAdminReject).
		Count(&ledgerCount).Error)
	assert.EqualValues(t, 1, ledgerCount)

	changed := input
	changed.Reason = "a different administrator reason must not reuse the action"
	_, err = ResolvePaymentOrderByAdmin(changed)
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
}

func TestExternalRefundWithoutEntitlementDoesNotDeductUnrelatedQuota(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 973002, 777)
	order := createTopUpPaymentOrder(t, 973002, PaymentProviderEpay, "alipay", 7300, 5000)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
		Updates(map[string]interface{}{"status": PaymentOrderStatusManualReview, "status_reason": "provider payment needs review"}).Error)
	require.NoError(t, DB.Model(&TopUp{}).Where("payment_order_id = ?", order.ID).
		Update("status", common.TopUpStatusManualReview).Error)

	input := PaymentAdminOrderActionInput{
		TradeNo: order.TradeNo, ExpectedVersion: order.Version, AdminID: 2, ActorIP: "192.0.2.2",
		Action:              PaymentAdminActionExternalRefundConfirmed,
		Reason:              "provider dashboard confirms the full external refund",
		RefundedAmountMinor: order.ExpectedAmountMinor,
	}
	result, err := ResolvePaymentOrderByAdmin(input)
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusRefunded, result.Order.Status)
	assert.Zero(t, result.Order.ReversedQuota)
	assert.Zero(t, result.Order.ReversedAmountMinor)

	var user User
	require.NoError(t, DB.First(&user, 973002).Error)
	assert.Equal(t, 777, user.Quota)
	var projection TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
	assert.Equal(t, PaymentOrderStatusRefunded, projection.Status)

	duplicate, err := ResolvePaymentOrderByAdmin(input)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)
	require.NoError(t, DB.First(&user, 973002).Error)
	assert.Equal(t, 777, user.Quota)
	var ledger PaymentLedgerEntry
	require.NoError(t, DB.Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryAdminExternalRefund).First(&ledger).Error)
	assert.EqualValues(t, order.ExpectedAmountMinor, ledger.AmountMinor)
	assert.Zero(t, ledger.QuotaDelta)
}

func TestExternalRefundAfterEntitlementUsesCompleteReversal(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 973003, 50)
	order := createTopUpPaymentOrder(t, 973003, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	paid := paidPaymentEvent(order, "stripe-admin-refund-paid")
	paid.ProviderOrderKey = "stripe:cs_admin_refund"
	paid.ProviderPaymentKey = "stripe:pi_admin_refund"
	_, err := ProcessPaymentEvent(paid)
	require.NoError(t, err)
	current, err := GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)

	partial, err := ResolvePaymentOrderByAdmin(PaymentAdminOrderActionInput{
		TradeNo: current.TradeNo, ExpectedVersion: current.Version, AdminID: 3, ActorIP: "192.0.2.3",
		Action:              PaymentAdminActionExternalRefundConfirmed,
		Reason:              "partial refund completed and verified in Stripe dashboard",
		RefundedAmountMinor: 400,
	})
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusRefundPending, partial.Order.Status)
	assert.EqualValues(t, 400, partial.Order.ReversedQuota)
	var user User
	require.NoError(t, DB.First(&user, 973003).Error)
	assert.Equal(t, 650, user.Quota)

	current, err = GetPaymentOrderByTradeNo(order.TradeNo)
	require.NoError(t, err)
	result, err := ResolvePaymentOrderByAdmin(PaymentAdminOrderActionInput{
		TradeNo: current.TradeNo, ExpectedVersion: current.Version, AdminID: 3, ActorIP: "192.0.2.3",
		Action:              PaymentAdminActionExternalRefundConfirmed,
		Reason:              "remaining refund completed and verified in Stripe dashboard",
		RefundedAmountMinor: current.ExpectedAmountMinor,
	})
	require.NoError(t, err)
	assert.Equal(t, PaymentOrderStatusRefunded, result.Order.Status)
	assert.EqualValues(t, 1000, result.Order.ReversedQuota)
	assert.EqualValues(t, 1000, result.Order.ReversedAmountMinor)

	require.NoError(t, DB.First(&user, 973003).Error)
	assert.Equal(t, 50, user.Quota)
	var projection TopUp
	require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
	assert.Equal(t, PaymentOrderStatusRefunded, projection.Status)
	var reversalCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).
		Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryRefundReversal).
		Count(&reversalCount).Error)
	assert.EqualValues(t, 2, reversalCount)
}

func TestDismissUnmatchedPaymentEventIsTerminalAndIdempotent(t *testing.T) {
	truncateTables(t)
	payload := `{"type":"checkout.session.completed","customer":"cus_dismissed"}`
	require.NoError(t, RecordPaymentEventManualReview(PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, EventKey: "stripe-unmatched-dismiss", EventType: "checkout.session.completed",
		ProviderLivemode: livePaymentModeForTest(),
		TradeNo:          "PO_UNKNOWN_DISMISS", ProviderOrderKey: "stripe:cs_dismissed",
		ProviderPaymentKey: "stripe:pi_dismissed", CustomerID: "cus_dismissed",
		PaidAmountMinor: 1000, Currency: "USD", PaymentMethod: PaymentMethodStripe, Paid: true,
		NormalizedPayload: payload,
	}, "verified event has no canonical order"))
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderStripe, "stripe-unmatched-dismiss").First(&event).Error)
	assert.Equal(t, "cus_dismissed", event.CustomerID)

	input := PaymentUnmatchedEventActionInput{
		EventID: event.ID, AdminID: 4, ActorIP: "192.0.2.4", Action: PaymentUnmatchedEventActionDismiss,
		Reason: "verified as an unrelated historical provider transaction",
	}
	result, err := ResolveUnmatchedPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.False(t, result.Duplicate)
	assert.Equal(t, PaymentEventStatusDismissed, result.Event.Status)
	assert.Equal(t, payload, result.Event.NormalizedPayload)
	var dismissAudit PaymentOperationsAudit
	require.NoError(t, DB.Where("action = ? AND subject_id = ?", PaymentOperationsActionUnmatchedDismiss, event.ID).
		First(&dismissAudit).Error)
	assert.Equal(t, input.ActorIP, dismissAudit.ActorIP)
	assert.Equal(t, input.Reason, dismissAudit.Reason)

	duplicate, err := ResolveUnmatchedPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)
	var stored PaymentEvent
	require.NoError(t, DB.First(&stored, event.ID).Error)
	assert.Equal(t, payload, stored.NormalizedPayload)
	assert.Equal(t, PaymentEventStatusDismissed, stored.Status)
	events, total, err := ListUnmatchedPaymentEventsPage(0, 50)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, PaymentEventStatusDismissed, events[0].Status)
}

func TestLinkUnmatchedStripePaymentBindsCustomerAndFulfillsExactlyOnce(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 973004, 0)
	order := createTopUpPaymentOrder(t, 973004, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	payload := `{"type":"checkout.session.completed","customer":"cus_admin_link"}`
	require.NoError(t, RecordPaymentEventManualReview(PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, EventKey: "stripe-unmatched-link", EventType: "checkout.session.completed",
		ProviderLivemode: livePaymentModeForTest(),
		TradeNo:          "PO_PROVIDER_METADATA_MISSING", ProviderOrderKey: "stripe:cs_admin_link",
		ProviderPaymentKey: "stripe:pi_admin_link", CustomerID: "cus_admin_link",
		PaidAmountMinor: 1000, Currency: "USD", PaymentMethod: PaymentMethodStripe, Paid: true,
		NormalizedPayload: payload,
	}, "verified paid event could not be mapped by trade number"))
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderStripe, "stripe-unmatched-link").First(&event).Error)
	assert.Equal(t, "cus_admin_link", event.CustomerID)

	input := PaymentUnmatchedEventActionInput{
		EventID: event.ID, AdminID: 5, ActorIP: "192.0.2.5", Action: PaymentUnmatchedEventActionLink,
		Reason:        "provider identities and exact amount match the target order",
		TargetTradeNo: order.TradeNo, ExpectedOrderVersion: order.Version,
	}
	wrongVersion := input
	wrongVersion.ExpectedOrderVersion++
	_, err := ResolveUnmatchedPaymentEventByAdmin(wrongVersion)
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)

	result, err := ResolveUnmatchedPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.False(t, result.Duplicate)
	assert.Equal(t, PaymentOrderStatusFulfilled, result.Order.Status)
	assert.EqualValues(t, order.ID, result.Event.PaymentOrderID)
	assert.Equal(t, PaymentEventStatusProcessed, result.Event.Status)
	var linkAudit PaymentOperationsAudit
	require.NoError(t, DB.Where("action = ? AND subject_id = ?", PaymentOperationsActionUnmatchedLink, event.ID).
		First(&linkAudit).Error)
	assert.Equal(t, input.ActorIP, linkAudit.ActorIP)
	assert.EqualValues(t, order.ID, linkAudit.PaymentOrderID)

	var user User
	require.NoError(t, DB.First(&user, 973004).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, "cus_admin_link", user.StripeCustomer)
	var binding PaymentCustomerBinding
	require.NoError(t, DB.Where("provider = ? AND customer_key = ?", PaymentProviderStripe, "cus_admin_link").First(&binding).Error)
	assert.Equal(t, 973004, binding.UserID)

	duplicate, err := ResolveUnmatchedPaymentEventByAdmin(input)
	require.NoError(t, err)
	assert.True(t, duplicate.Duplicate)
	require.NoError(t, DB.First(&user, 973004).Error)
	assert.Equal(t, 1000, user.Quota)
	var creditCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).
		Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryCredit).
		Count(&creditCount).Error)
	assert.EqualValues(t, 1, creditCount)
}

func TestStripeTestModeCannotBeGrantedThroughAdministratorBypasses(t *testing.T) {
	t.Setenv(setting.StripeTestModeEnabledEnv, "false")

	t.Run("manual fulfill", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 973011, 0)
		order := createTopUpPaymentOrder(t, 973011, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
		setPaymentOrderLivemodeForTest(t, order, false)
		require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
			Updates(map[string]interface{}{"status": PaymentOrderStatusManualReview, "status_reason": "test payment blocked"}).Error)
		require.NoError(t, DB.Model(&TopUp{}).Where("payment_order_id = ?", order.ID).
			Update("status", common.TopUpStatusManualReview).Error)

		_, err := ResolvePaymentOrderByAdmin(PaymentAdminOrderActionInput{
			TradeNo: order.TradeNo, ExpectedVersion: order.Version, AdminID: 11, ActorIP: "192.0.2.11",
			Action: PaymentAdminActionFulfill, Reason: "attempted test payment administrator fulfillment",
		})
		require.ErrorIs(t, err, ErrPaymentAuditConflict)
		var user User
		require.NoError(t, DB.First(&user, order.UserID).Error)
		assert.Zero(t, user.Quota)
		var stored PaymentOrder
		require.NoError(t, DB.First(&stored, order.ID).Error)
		assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
	})

	t.Run("link unmatched", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 973012, 0)
		order := createTopUpPaymentOrder(t, 973012, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
		setPaymentOrderLivemodeForTest(t, order, false)
		testMode := false
		require.NoError(t, RecordPaymentEventManualReview(PaymentEventInput{
			Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, ProviderLivemode: &testMode,
			EventKey: "stripe-test-disabled-unmatched-link", EventType: "checkout.session.completed",
			TradeNo: "PO_TEST_DISABLED_UNMATCHED", ProviderOrderKey: "stripe:cs_test_disabled_unmatched",
			ProviderPaymentKey: "stripe:pi_test_disabled_unmatched", PaidAmountMinor: 1000,
			Currency: "USD", PaymentMethod: PaymentMethodStripe, Paid: true,
			NormalizedPayload: `{"paid":true,"livemode":false}`,
		}, "test payment is blocked while sandbox mode is disabled"))
		var event PaymentEvent
		require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderStripe,
			"stripe-test-disabled-unmatched-link").First(&event).Error)

		_, err := ResolveUnmatchedPaymentEventByAdmin(PaymentUnmatchedEventActionInput{
			EventID: event.ID, AdminID: 12, ActorIP: "192.0.2.12", Action: PaymentUnmatchedEventActionLink,
			Reason:        "attempted test payment unmatched event fulfillment",
			TargetTradeNo: order.TradeNo, ExpectedOrderVersion: order.Version,
		})
		require.ErrorIs(t, err, ErrPaymentAuditConflict)
		var user User
		require.NoError(t, DB.First(&user, order.UserID).Error)
		assert.Zero(t, user.Quota)
		require.NoError(t, DB.First(&event, event.ID).Error)
		assert.Equal(t, PaymentEventStatusManualReview, event.Status)
		assert.Zero(t, event.PaymentOrderID)
	})

	t.Run("live order cannot override test event mismatch", func(t *testing.T) {
		t.Setenv(setting.StripeTestModeEnabledEnv, "true")
		truncateTables(t)
		seedPaymentUser(t, 973013, 0)
		order := createTopUpPaymentOrder(t, 973013, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
		testMode := false
		paid := paidPaymentEvent(order, "stripe-live-order-test-event-mismatch")
		paid.ProviderLivemode = &testMode
		_, err := ProcessPaymentEvent(paid)
		require.ErrorIs(t, err, ErrPaymentManualReview)

		current, err := GetPaymentOrderByTradeNo(order.TradeNo)
		require.NoError(t, err)
		assert.Equal(t, PaymentOrderStatusManualReview, current.Status)
		_, err = ResolvePaymentOrderByAdmin(PaymentAdminOrderActionInput{
			TradeNo: current.TradeNo, ExpectedVersion: current.Version, AdminID: 13, ActorIP: "192.0.2.13",
			Action: PaymentAdminActionFulfill, Reason: "attempted override of mismatched Stripe mode evidence",
		})
		require.ErrorIs(t, err, ErrPaymentAuditConflict)
		var user User
		require.NoError(t, DB.First(&user, order.UserID).Error)
		assert.Zero(t, user.Quota)
	})
}

func TestUnmatchedPaymentEventViewsExposeOnlyCurrentlyExecutableLinkActions(t *testing.T) {
	t.Run("live event with canonical order remains linkable", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 973013, 0)
		order := createTopUpPaymentOrder(t, 973013, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
		event := paidPaymentEvent(order, "stripe-view-canonical-link")
		require.NoError(t, RecordPaymentEventManualReview(event, "verified live event awaits explicit canonical order linking"))

		views, _, err := ListUnmatchedPaymentEventViewsPage(0, 50)
		require.NoError(t, err)
		require.Len(t, views, 1)
		assert.Equal(t, []string{PaymentUnmatchedEventActionLink, PaymentUnmatchedEventActionDismiss}, views[0].AvailableActions)
		require.NotNil(t, views[0].ProviderLivemode)
		assert.True(t, *views[0].ProviderLivemode)
	})

	t.Run("disabled sandbox event does not advertise link", func(t *testing.T) {
		t.Setenv(setting.StripeTestModeEnabledEnv, "false")
		truncateTables(t)
		seedPaymentUser(t, 973014, 0)
		order := createTopUpPaymentOrder(t, 973014, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
		setPaymentOrderLivemodeForTest(t, order, false)
		event := paidPaymentEvent(order, "stripe-view-disabled-sandbox")
		testMode := false
		event.ProviderLivemode = &testMode
		event.NormalizedPayload = `{"paid":true,"livemode":false}`
		require.NoError(t, RecordPaymentEventManualReview(event, "verified sandbox event is blocked while sandbox settlement is disabled"))

		views, _, err := ListUnmatchedPaymentEventViewsPage(0, 50)
		require.NoError(t, err)
		require.Len(t, views, 1)
		assert.Empty(t, views[0].AvailableActions)
		require.NotNil(t, views[0].ProviderLivemode)
		assert.False(t, *views[0].ProviderLivemode)
	})
}

func TestLinkUnmatchedPaymentRejectsAmountAndProviderMismatch(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 973005, 0)
	stripeOrder := createTopUpPaymentOrder(t, 973005, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)

	for _, test := range []struct {
		name     string
		provider string
		amount   int64
		key      string
	}{
		{name: "amount", provider: PaymentProviderStripe, amount: 999, key: "stripe-unmatched-wrong-amount"},
		{name: "provider", provider: PaymentProviderEpay, amount: 1000, key: "epay-unmatched-wrong-provider"},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := `{"paid":true,"case":"` + test.name + `"}`
			var providerLivemode *bool
			if test.provider == PaymentProviderStripe {
				providerLivemode = livePaymentModeForTest()
			}
			require.NoError(t, RecordPaymentEventManualReview(PaymentEventInput{
				Provider: test.provider, EventKey: test.key, EventType: "paid", TradeNo: "PO_WRONG_" + test.name,
				ProviderLivemode:   providerLivemode,
				ProviderOrderKey:   test.provider + ":order_" + test.name,
				ProviderPaymentKey: test.provider + ":payment_" + test.name,
				PaidAmountMinor:    test.amount, Currency: "USD", PaymentMethod: PaymentMethodStripe,
				Paid: true, NormalizedPayload: payload,
			}, "verified paid event requires explicit review"))
			var event PaymentEvent
			require.NoError(t, DB.Where("provider = ? AND event_key = ?", test.provider, test.key).First(&event).Error)
			_, err := ResolveUnmatchedPaymentEventByAdmin(PaymentUnmatchedEventActionInput{
				EventID: event.ID, AdminID: 6, ActorIP: "192.0.2.6", Action: PaymentUnmatchedEventActionLink,
				Reason:        "attempted deterministic contract validation for target order",
				TargetTradeNo: stripeOrder.TradeNo, ExpectedOrderVersion: stripeOrder.Version,
			})
			assert.ErrorIs(t, err, ErrPaymentAuditConflict)
			var stored PaymentEvent
			require.NoError(t, DB.First(&stored, event.ID).Error)
			assert.Equal(t, PaymentEventStatusManualReview, stored.Status)
			assert.Zero(t, stored.PaymentOrderID)
		})
	}
	seedPaymentUser(t, 973007, 0)
	require.NoError(t, DB.Create(&PaymentCustomerBinding{
		Provider: PaymentProviderStripe, CustomerKey: "cus_owned_by_other_user", UserID: 973007,
	}).Error)
	ownedPayload := `{"paid":true,"customer":"cus_owned_by_other_user"}`
	require.NoError(t, RecordPaymentEventManualReview(PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, EventKey: "stripe-unmatched-customer-owner", EventType: "checkout.session.completed",
		ProviderLivemode: livePaymentModeForTest(),
		TradeNo:          "PO_WRONG_CUSTOMER_OWNER", ProviderOrderKey: "stripe:order_customer_owner",
		ProviderPaymentKey: "stripe:payment_customer_owner", CustomerID: "cus_owned_by_other_user",
		PaidAmountMinor: 1000, Currency: "USD", PaymentMethod: PaymentMethodStripe,
		Paid: true, NormalizedPayload: ownedPayload,
	}, "verified paid event has a conflicting customer owner"))
	var ownedEvent PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderStripe, "stripe-unmatched-customer-owner").First(&ownedEvent).Error)
	conflictResult, err := ResolveUnmatchedPaymentEventByAdmin(PaymentUnmatchedEventActionInput{
		EventID: ownedEvent.ID, AdminID: 6, ActorIP: "192.0.2.6", Action: PaymentUnmatchedEventActionLink,
		Reason:        "customer ownership must match before entitlement fulfillment",
		TargetTradeNo: stripeOrder.TradeNo, ExpectedOrderVersion: stripeOrder.Version,
	})
	assert.Nil(t, conflictResult)
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
	require.NoError(t, DB.First(&ownedEvent, ownedEvent.ID).Error)
	assert.Equal(t, PaymentEventStatusManualReview, ownedEvent.Status)
	assert.Zero(t, ownedEvent.PaymentOrderID)

	var user User
	require.NoError(t, DB.First(&user, 973005).Error)
	assert.Zero(t, user.Quota)
}

func TestLinkUnmatchedStripeEventRejectsRevokedWebhookGeneration(t *testing.T) {
	truncateTables(t)
	for key, value := range map[string]string{
		"StripeWebhookCredentialGeneration":         "3",
		"StripeWebhookPreviousCredentialGeneration": "0",
		"StripeWebhookPreviousValidBefore":          "0",
		"StripeWebhookSecretPreviousExpiresAt":      "0",
	} {
		require.NoError(t, DB.Model(&Option{}).
			Where(fmt.Sprintf("%s = ?", optionKeyColumn()), key).Update("value", value).Error)
	}
	seedPaymentUser(t, 973008, 0)
	order := createTopUpPaymentOrder(t, 973008, PaymentProviderStripe, PaymentMethodStripe, 1000, 1000)
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
		Update("provider_credential_generation", 3).Error)
	order.ProviderCredentialGeneration = 3
	payload := `{"paid":true,"generation":2}`
	require.NoError(t, RecordPaymentEventManualReview(PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 2,
		ProviderLivemode: livePaymentModeForTest(),
		EventKey:         "stripe-unmatched-revoked-generation", EventType: "checkout.session.completed",
		TradeNo: "PO_STALE_SIGNED_EVENT", ProviderOrderKey: "stripe:cs_stale_generation",
		ProviderPaymentKey: "stripe:pi_stale_generation", PaidAmountMinor: 1000,
		Currency: "USD", PaymentMethod: PaymentMethodStripe, Paid: true, NormalizedPayload: payload,
	}, "verified before emergency webhook credential revocation"))
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", PaymentProviderStripe, "stripe-unmatched-revoked-generation").First(&event).Error)

	_, err := ResolveUnmatchedPaymentEventByAdmin(PaymentUnmatchedEventActionInput{
		EventID: event.ID, AdminID: 8, ActorIP: "192.0.2.8", Action: PaymentUnmatchedEventActionLink,
		Reason:        "attempted link after emergency webhook credential revocation",
		TargetTradeNo: order.TradeNo, ExpectedOrderVersion: order.Version,
	})
	assert.ErrorIs(t, err, ErrPaymentAuditConflict)
	require.NoError(t, DB.First(&event, event.ID).Error)
	assert.Equal(t, PaymentEventStatusManualReview, event.Status)
	assert.Zero(t, event.PaymentOrderID)
}

func TestExternalSubscriptionRefundRequiresFullAmount(t *testing.T) {
	truncateTables(t)
	seedPaymentUser(t, 973006, 0)
	plan := &SubscriptionPlan{
		Id: 983006, Title: "Full refund only", PriceAmount: 10, Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true, TotalAmount: 1000,
	}
	require.NoError(t, DB.Create(plan).Error)
	order := createSubscriptionPaymentOrder(t, 973006, plan, 1000)
	_, err := ResolvePaymentOrderByAdmin(PaymentAdminOrderActionInput{
		TradeNo: order.TradeNo, ExpectedVersion: order.Version, AdminID: 7, ActorIP: "192.0.2.7",
		Action:              PaymentAdminActionExternalRefundConfirmed,
		Reason:              "provider reports a partial subscription refund for review",
		RefundedAmountMinor: 500,
	})
	assert.ErrorIs(t, err, ErrPaymentAuditInvalid)
}

func TestLatePaidEventAfterAdminRejectOrVoidRequiresManualReview(t *testing.T) {
	t.Run("top-up void", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 973008, 125)
		order := createTopUpPaymentOrder(t, 973008, PaymentProviderEpay, "alipay", 7300, 5000)
		require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
			Updates(map[string]interface{}{"status": PaymentOrderStatusManualReview, "status_reason": "provider payment needs review"}).Error)
		require.NoError(t, DB.Model(&TopUp{}).Where("payment_order_id = ?", order.ID).
			Update("status", common.TopUpStatusManualReview).Error)

		_, err := ResolvePaymentOrderByAdmin(PaymentAdminOrderActionInput{
			TradeNo: order.TradeNo, ExpectedVersion: order.Version, AdminID: 8, ActorIP: "192.0.2.8",
			Action: PaymentAdminActionVoid, Reason: "provider confirms that no charge was captured",
		})
		require.NoError(t, err)

		paid := paidPaymentEvent(order, "epay-paid-after-admin-void")
		paid.ProviderOrderKey = "epay:late_void_order"
		paid.ProviderPaymentKey = "epay:late_void_payment"
		result, err := ProcessPaymentEvent(paid)
		require.ErrorIs(t, err, ErrPaymentManualReview)
		require.NotNil(t, result)
		assert.True(t, result.ManualReview)

		var user User
		require.NoError(t, DB.First(&user, 973008).Error)
		assert.Equal(t, 125, user.Quota)
		stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
		require.NoError(t, lookupErr)
		assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
		assert.Equal(t, "paid_after_admin_reject_or_void", stored.StatusReason)
		var projection TopUp
		require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
		assert.Equal(t, common.TopUpStatusFailed, projection.Status)
		var credits int64
		require.NoError(t, DB.Model(&PaymentLedgerEntry{}).
			Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntryCredit).Count(&credits).Error)
		assert.Zero(t, credits)
	})

	t.Run("subscription reject", func(t *testing.T) {
		truncateTables(t)
		seedPaymentUser(t, 973009, 0)
		plan := &SubscriptionPlan{
			Id: 983009, Title: "Late paid subscription", PriceAmount: 10, Currency: "USD",
			DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
			TotalAmount: 1000, MaxPurchasePerUser: 1,
		}
		require.NoError(t, DB.Create(plan).Error)
		order := createSubscriptionPaymentOrder(t, 973009, plan, 1000)
		require.NoError(t, DB.Model(&PaymentOrder{}).Where("id = ?", order.ID).
			Updates(map[string]interface{}{"status": PaymentOrderStatusManualReview, "status_reason": "provider payment needs review"}).Error)
		require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("payment_order_id = ?", order.ID).
			Updates(map[string]interface{}{"status": SubscriptionOrderStatusManualReview, "review_reason": "provider payment needs review"}).Error)

		_, err := ResolvePaymentOrderByAdmin(PaymentAdminOrderActionInput{
			TradeNo: order.TradeNo, ExpectedVersion: order.Version, AdminID: 9, ActorIP: "192.0.2.9",
			Action: PaymentAdminActionReject, Reason: "provider confirms that no subscription payment completed",
		})
		require.NoError(t, err)

		paid := paidPaymentEvent(order, "stripe-paid-after-admin-reject")
		paid.ProviderOrderKey = "stripe:late_reject_order"
		paid.ProviderPaymentKey = "stripe:late_reject_payment"
		result, err := ProcessPaymentEvent(paid)
		require.ErrorIs(t, err, ErrPaymentManualReview)
		require.NotNil(t, result)
		assert.True(t, result.ManualReview)

		entitlements, countErr := CountUserSubscriptionsByPlan(973009, plan.Id)
		require.NoError(t, countErr)
		assert.Zero(t, entitlements)
		purchaseSlots, countErr := CountUserSubscriptionPurchasesByPlan(973009, plan.Id)
		require.NoError(t, countErr)
		assert.Zero(t, purchaseSlots)
		stored, lookupErr := GetPaymentOrderByTradeNo(order.TradeNo)
		require.NoError(t, lookupErr)
		assert.Equal(t, PaymentOrderStatusManualReview, stored.Status)
		assert.Equal(t, "paid_after_admin_reject_or_void", stored.StatusReason)
		var projection SubscriptionOrder
		require.NoError(t, DB.Where("payment_order_id = ?", order.ID).First(&projection).Error)
		assert.Equal(t, common.TopUpStatusFailed, projection.Status)
		var grants int64
		require.NoError(t, DB.Model(&PaymentLedgerEntry{}).
			Where("payment_order_id = ? AND entry_type = ?", order.ID, PaymentLedgerEntrySubscriptionGranted).Count(&grants).Error)
		assert.Zero(t, grants)
	})
}

func TestDismissedUnmatchedEventCannotBeRevivedByWebhookReplay(t *testing.T) {
	truncateTables(t)
	input := PaymentEventInput{
		Provider: PaymentProviderStripe, ProviderCredentialGeneration: 1, EventKey: "stripe-dismissed-unmatched-replay",
		ProviderLivemode: livePaymentModeForTest(),
		EventType:        "checkout.session.completed", TradeNo: "PO_DISMISSED_UNMATCHED",
		ProviderOrderKey: "stripe:cs_dismissed_unmatched", ProviderPaymentKey: "stripe:pi_dismissed_unmatched",
		PaidAmountMinor: 1000, Currency: "USD", PaymentMethod: PaymentMethodStripe,
		Paid: true, NormalizedPayload: `{"paid":true,"dismissed":true}`,
	}
	require.NoError(t, RecordPaymentEventManualReview(input, "verified event could not be mapped to an order"))
	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND event_key = ?", input.Provider, input.EventKey).First(&event).Error)

	_, err := ResolveUnmatchedPaymentEventByAdmin(PaymentUnmatchedEventActionInput{
		EventID: event.ID, AdminID: 10, ActorIP: "192.0.2.10", Action: PaymentUnmatchedEventActionDismiss,
		Reason: "provider dashboard confirms that this event must remain dismissed",
	})
	require.NoError(t, err)

	result, err := ProcessPaymentEvent(input)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Duplicate)
	assert.False(t, result.ManualReview)
	require.NoError(t, RecordPaymentEventManualReview(input, "replayed webhook must not revive the event"))

	require.NoError(t, DB.First(&event, event.ID).Error)
	assert.Equal(t, PaymentEventStatusDismissed, event.Status)
	assert.Zero(t, event.PaymentOrderID)
	var ledgerCount int64
	require.NoError(t, DB.Model(&PaymentLedgerEntry{}).Where("payment_event_id = ?", event.ID).Count(&ledgerCount).Error)
	assert.Zero(t, ledgerCount)
}

func TestUnmatchedPaymentEventPaginationReturnsTotal(t *testing.T) {
	truncateTables(t)
	for index := 0; index < 3; index++ {
		require.NoError(t, DB.Create(&PaymentEvent{
			Provider: PaymentProviderEpay, EventKey: "unmatched-page-" + string(rune('a'+index)), EventType: "paid",
			Status: PaymentEventStatusManualReview, CreatedAt: time.Now().Unix() + int64(index), UpdatedAt: time.Now().Unix() + int64(index),
		}).Error)
	}
	events, total, err := ListUnmatchedPaymentEventsPage(1, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, events, 1)
}

func TestUnmatchedPaymentEventHistoryIncludesTerminalSecurityEvidence(t *testing.T) {
	truncateTables(t)
	now := time.Now().Unix()
	require.NoError(t, DB.Create(&[]PaymentEvent{
		{
			Provider: PaymentProviderStripe, EventKey: "unmatched-history-manual", EventType: "paid",
			Status: PaymentEventStatusManualReview, CreatedAt: now, UpdatedAt: now,
		},
		{
			Provider: PaymentProviderStripe, EventKey: "unmatched-history-revoked", EventType: "paid",
			Status: PaymentEventStatusCredentialRevoked, CreatedAt: now + 1, UpdatedAt: now + 1,
		},
		{
			Provider: PaymentProviderEpay, EventKey: "unmatched-history-dismissed", EventType: "paid",
			Status: PaymentEventStatusDismissed, CreatedAt: now + 2, UpdatedAt: now + 2,
		},
		{
			Provider: PaymentProviderXorPay, EventKey: "unmatched-history-processed", EventType: "paid",
			Status: PaymentEventStatusProcessed, CreatedAt: now + 3, UpdatedAt: now + 3,
		},
	}).Error)

	events, total, err := ListUnmatchedPaymentEventsPage(0, 50)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, events, 3)
	assert.Equal(t, PaymentEventStatusDismissed, events[0].Status)
	assert.Equal(t, PaymentEventStatusCredentialRevoked, events[1].Status)
	assert.Equal(t, PaymentEventStatusManualReview, events[2].Status)
}
