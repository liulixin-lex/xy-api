package model

import (
	"math"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedSubscriptionPaymentUser(t *testing.T, id int, quota int) {
	t.Helper()
	require.NoError(t, DB.Create(&User{
		Id:       id,
		Username: "subscription_payment_user_" + common.GetRandomString(8),
		AffCode:  "sub_aff_" + common.GetRandomString(8),
		Status:   common.UserStatusEnabled,
		Quota:    quota,
	}).Error)
}

func seedSubscriptionPaymentPlan(t *testing.T, id int, maxPurchase int) *SubscriptionPlan {
	t.Helper()
	plan := &SubscriptionPlan{
		Id:                 id,
		Title:              "Snapshot Plan",
		PriceAmount:        10,
		Currency:           "USD",
		DurationUnit:       SubscriptionDurationDay,
		DurationValue:      2,
		Enabled:            true,
		MaxPurchasePerUser: maxPurchase,
		TotalAmount:        100,
		QuotaResetPeriod:   SubscriptionResetNever,
	}
	require.NoError(t, DB.Create(plan).Error)
	return plan
}

func TestValidateSubscriptionPlanForExternalPaymentRejectsUnfulfillableQuota(t *testing.T) {
	plan := &SubscriptionPlan{
		Title:            "External Payment Boundary",
		PriceAmount:      10,
		Currency:         "USD",
		DurationUnit:     SubscriptionDurationDay,
		DurationValue:    1,
		Enabled:          true,
		TotalAmount:      math.MaxInt32,
		QuotaResetPeriod: SubscriptionResetNever,
	}

	require.NoError(t, ValidateSubscriptionPlanForExternalPayment(plan))
	plan.TotalAmount = int64(math.MaxInt32) + 1
	assert.EqualError(t, ValidateSubscriptionPlanForExternalPayment(plan), "套餐总额度超出外部支付可交付范围")
}

func newPendingSubscriptionPaymentOrder(t *testing.T, userId int, plan *SubscriptionPlan, tradeNo string, provider string) *SubscriptionOrder {
	t.Helper()
	order := &SubscriptionOrder{
		UserId:              userId,
		PlanId:              plan.Id,
		Money:               plan.PriceAmount,
		TradeNo:             tradeNo,
		PaymentMethod:       provider,
		PaymentProvider:     provider,
		Status:              common.TopUpStatusPending,
		ExpectedAmountMinor: 1000,
		PaymentCurrency:     "USD",
		ReserveUntil:        time.Now().Add(time.Hour).Unix(),
	}
	require.NoError(t, order.SetPlanSnapshot(plan))
	return order
}

func TestCompleteSubscriptionOrderVerifiedUsesImmutablePlanSnapshot(t *testing.T) {
	truncateTables(t)
	seedSubscriptionPaymentUser(t, 8101, 0)
	plan := seedSubscriptionPaymentPlan(t, 8201, 1)
	order := newPendingSubscriptionPaymentOrder(t, 8101, plan, "snapshot-order", PaymentProviderStripe)
	paymentOrderId := int64(90001)
	order.PaymentOrderId = &paymentOrderId
	require.NoError(t, order.Insert())
	require.NoError(t, SetSubscriptionOrderProviderOrderId(order.TradeNo, PaymentProviderStripe, "cs_snapshot"))
	require.NoError(t, SetSubscriptionOrderProviderOrderId(order.TradeNo, PaymentProviderStripe, "cs_snapshot"))

	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(map[string]any{
		"title":              "Mutated Plan",
		"price_amount":       99,
		"duration_value":     30,
		"total_amount":       999,
		"quota_reset_period": SubscriptionResetDaily,
	}).Error)
	InvalidateSubscriptionPlanCache(plan.Id)

	paidAmountMinor := int64(1000)
	require.NoError(t, CompleteSubscriptionOrderVerified(order.TradeNo, SubscriptionPaymentConfirmation{
		ExpectedPaymentProvider: PaymentProviderStripe,
		PaidAmountMinor:         &paidAmountMinor,
		Currency:                "USD",
		ProviderOrderId:         "cs_snapshot",
	}))

	var subscription UserSubscription
	require.NoError(t, DB.Where("user_id = ?", 8101).First(&subscription).Error)
	assert.EqualValues(t, 100, subscription.AmountTotal)
	assert.InDelta(t, int64(2*24*time.Hour/time.Second), subscription.EndTime-subscription.StartTime, 2)
	assert.Equal(t, SubscriptionResetNever, subscription.QuotaResetPeriod)
	require.NotNil(t, subscription.PaymentOrderId)
	assert.Equal(t, paymentOrderId, *subscription.PaymentOrderId)

	completed := GetSubscriptionOrderByTradeNo(order.TradeNo)
	require.NotNil(t, completed)
	assert.Equal(t, common.TopUpStatusSuccess, completed.Status)
	assert.Equal(t, "cs_snapshot", completed.ProviderOrderId)
}

func TestSubscriptionPlanSnapshotRestoresValidatedPlan(t *testing.T) {
	plan := &SubscriptionPlan{
		Id:                  8301,
		Title:               "Restored Plan",
		PriceAmount:         12.34,
		Currency:            "USD",
		DurationUnit:        SubscriptionDurationMonth,
		DurationValue:       3,
		MaxPurchasePerUser:  2,
		TotalAmount:         1234,
		QuotaResetPeriod:    SubscriptionResetWeekly,
		AllowWalletOverflow: common.GetPointer(false),
	}
	snapshot, err := NewSubscriptionPlanSnapshot(plan)
	require.NoError(t, err)

	restored, err := snapshot.SubscriptionPlan()
	require.NoError(t, err)
	assert.Equal(t, plan.Id, restored.Id)
	assert.Equal(t, plan.Title, restored.Title)
	assert.Equal(t, plan.DurationValue, restored.DurationValue)
	assert.EqualValues(t, plan.TotalAmount, restored.TotalAmount)
	require.NotNil(t, restored.AllowWalletOverflow)
	assert.False(t, *restored.AllowWalletOverflow)
}

func TestCompleteSubscriptionOrderVerifiedMovesLegacyPendingOrderToManualReview(t *testing.T) {
	truncateTables(t)
	seedSubscriptionPaymentUser(t, 8102, 0)
	plan := seedSubscriptionPaymentPlan(t, 8202, 0)
	legacyOrder := &SubscriptionOrder{
		UserId:              8102,
		PlanId:              plan.Id,
		Money:               10,
		TradeNo:             "legacy-no-snapshot",
		PaymentMethod:       PaymentMethodStripe,
		PaymentProvider:     PaymentProviderStripe,
		Status:              common.TopUpStatusPending,
		ExpectedAmountMinor: 1000,
		PaymentCurrency:     "USD",
		ReserveUntil:        time.Now().Add(time.Hour).Unix(),
	}
	require.NoError(t, DB.Create(legacyOrder).Error)

	paidAmountMinor := int64(1000)
	err := CompleteSubscriptionOrderVerified(legacyOrder.TradeNo, SubscriptionPaymentConfirmation{
		ExpectedPaymentProvider: PaymentProviderStripe,
		PaidAmountMinor:         &paidAmountMinor,
		Currency:                "USD",
	})
	require.ErrorIs(t, err, ErrSubscriptionOrderSnapshotMissing)

	stored := GetSubscriptionOrderByTradeNo(legacyOrder.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, SubscriptionOrderStatusManualReview, stored.Status)
	assert.Equal(t, "missing_or_invalid_plan_snapshot", stored.ReviewReason)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, 8102))
}

func TestCompleteSubscriptionOrderVerifiedRejectsAmountMismatch(t *testing.T) {
	truncateTables(t)
	seedSubscriptionPaymentUser(t, 8103, 0)
	plan := seedSubscriptionPaymentPlan(t, 8203, 0)
	order := newPendingSubscriptionPaymentOrder(t, 8103, plan, "amount-mismatch", PaymentProviderEpay)
	require.NoError(t, order.Insert())

	paidAmountMinor := int64(999)
	err := CompleteSubscriptionOrderVerified(order.TradeNo, SubscriptionPaymentConfirmation{
		ExpectedPaymentProvider: PaymentProviderEpay,
		ActualPaymentMethod:     PaymentProviderEpay,
		PaidAmountMinor:         &paidAmountMinor,
		Currency:                "USD",
		ProviderOrderId:         "epay-payment-1",
	})
	require.ErrorIs(t, err, ErrSubscriptionPaymentAmountMismatch)

	stored := GetSubscriptionOrderByTradeNo(order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, SubscriptionOrderStatusManualReview, stored.Status)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, 8103))
}

func TestCompleteSubscriptionOrderVerifiedRejectsReusedProviderOrder(t *testing.T) {
	truncateTables(t)
	seedSubscriptionPaymentUser(t, 8109, 0)
	seedSubscriptionPaymentUser(t, 8110, 0)
	plan := seedSubscriptionPaymentPlan(t, 8209, 0)
	first := newPendingSubscriptionPaymentOrder(t, 8109, plan, "provider-order-first", PaymentProviderEpay)
	second := newPendingSubscriptionPaymentOrder(t, 8110, plan, "provider-order-second", PaymentProviderEpay)
	require.NoError(t, first.Insert())
	require.NoError(t, second.Insert())
	paidAmountMinor := int64(1000)
	confirmation := SubscriptionPaymentConfirmation{
		ExpectedPaymentProvider: PaymentProviderEpay,
		ActualPaymentMethod:     PaymentProviderEpay,
		PaidAmountMinor:         &paidAmountMinor,
		Currency:                "USD",
		ProviderOrderId:         "epay-shared-provider-order",
	}
	require.NoError(t, CompleteSubscriptionOrderVerified(first.TradeNo, confirmation))

	err := CompleteSubscriptionOrderVerified(second.TradeNo, confirmation)
	require.ErrorIs(t, err, ErrPaymentMethodMismatch)
	stored := GetSubscriptionOrderByTradeNo(second.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, SubscriptionOrderStatusManualReview, stored.Status)
	assert.Equal(t, "provider_order_reused", stored.ReviewReason)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, 8110))
}

func TestSubscriptionOrderInsertReservesPurchaseLimit(t *testing.T) {
	truncateTables(t)
	seedSubscriptionPaymentUser(t, 8104, 0)
	plan := seedSubscriptionPaymentPlan(t, 8204, 1)

	first := newPendingSubscriptionPaymentOrder(t, 8104, plan, "reservation-first", PaymentProviderStripe)
	require.NoError(t, first.Insert())
	second := newPendingSubscriptionPaymentOrder(t, 8104, plan, "reservation-second", PaymentProviderStripe)
	require.ErrorIs(t, second.Insert(), ErrSubscriptionPurchaseLimit)

	require.NoError(t, ExpireSubscriptionOrder(first.TradeNo, PaymentProviderStripe))
	third := newPendingSubscriptionPaymentOrder(t, 8104, plan, "reservation-third", PaymentProviderStripe)
	require.NoError(t, third.Insert())
}

func TestBalancePurchaseRespectsPendingReservation(t *testing.T) {
	truncateTables(t)
	seedSubscriptionPaymentUser(t, 8107, 10_000_000)
	plan := seedSubscriptionPaymentPlan(t, 8207, 1)
	pending := newPendingSubscriptionPaymentOrder(t, 8107, plan, "reservation-before-balance", PaymentProviderStripe)
	require.NoError(t, pending.Insert())

	err := PurchaseSubscriptionWithBalance(8107, plan.Id, "balance-reservation-conflict")
	require.ErrorIs(t, err, ErrSubscriptionPurchaseLimit)
	assert.Equal(t, 10_000_000, getUserQuotaForPaymentGuardTest(t, 8107))
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, 8107))
}

func TestBalancePurchaseRequestIsIdempotent(t *testing.T) {
	truncateTables(t)
	seedSubscriptionPaymentUser(t, 8112, 10_000_000)
	plan := seedSubscriptionPaymentPlan(t, 8212, 0)
	requestId := "balance-subscription-idempotent-1"
	require.NoError(t, PurchaseSubscriptionWithBalance(8112, plan.Id, requestId))
	quotaAfterFirst := getUserQuotaForPaymentGuardTest(t, 8112)
	require.NoError(t, PurchaseSubscriptionWithBalance(8112, plan.Id, requestId))
	assert.Equal(t, quotaAfterFirst, getUserQuotaForPaymentGuardTest(t, 8112))
	assert.EqualValues(t, 1, countUserSubscriptionsForPaymentGuardTest(t, 8112))
	var orderCount int64
	require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("user_id = ? AND balance_request_id = ?", 8112, requestId).Count(&orderCount).Error)
	assert.EqualValues(t, 1, orderCount)
}

func TestCanonicalPaymentOrderReservesPurchaseAndExcludesOwnProjectionOnFulfillment(t *testing.T) {
	truncateTables(t)
	seedSubscriptionPaymentUser(t, 8111, 0)
	seedStripeCustomerBinding(t, 8111, "cus_subscription_reservation")
	plan := seedSubscriptionPaymentPlan(t, 8211, 1)
	snapshot, err := NewSubscriptionPlanSnapshot(plan)
	require.NoError(t, err)
	productSnapshot, err := common.Marshal(snapshot)
	require.NoError(t, err)

	createQuote := func(quoteId string) {
		t.Helper()
		require.NoError(t, CreatePaymentQuote(&PaymentQuote{
			QuoteID:             quoteId,
			UserID:              8111,
			OrderKind:           PaymentOrderKindSubscription,
			Provider:            PaymentProviderStripe,
			PaymentMethod:       PaymentMethodStripe,
			ProviderLivemode:    livePaymentModeForTest(),
			RequestedAmount:     int64(plan.Id),
			ExpectedAmountMinor: 1000,
			Currency:            "USD",
			ProductSnapshot:     string(productSnapshot),
			ExpiresAt:           time.Now().Add(time.Hour).Unix(),
		}))
	}
	createQuote("subscription-reservation-quote-1")
	first, err := CreatePaymentOrderFromQuote(8111, "subscription-reservation-quote-1", "subscription-reservation-request-1")
	require.NoError(t, err)
	require.NotNil(t, first)

	createQuote("subscription-reservation-quote-2")
	_, err = CreatePaymentOrderFromQuote(8111, "subscription-reservation-quote-2", "subscription-reservation-request-2")
	require.ErrorIs(t, err, ErrSubscriptionPurchaseLimit)
	var paymentOrderCount int64
	require.NoError(t, DB.Model(&PaymentOrder{}).Where("user_id = ?", 8111).Count(&paymentOrderCount).Error)
	assert.EqualValues(t, 1, paymentOrderCount)

	err = DB.Transaction(func(tx *gorm.DB) error {
		_, createErr := CreateUserSubscriptionFromPaymentOrderTx(tx, 8111, plan, "payment_order", first.ID)
		return createErr
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, countUserSubscriptionsForPaymentGuardTest(t, 8111))
}

func TestCompleteSubscriptionOrderRequiresVerifiedStripeAmount(t *testing.T) {
	truncateTables(t)
	seedSubscriptionPaymentUser(t, 8105, 0)
	plan := seedSubscriptionPaymentPlan(t, 8205, 0)
	order := newPendingSubscriptionPaymentOrder(t, 8105, plan, "amount-required", PaymentProviderStripe)
	require.NoError(t, order.Insert())

	err := CompleteSubscriptionOrder(order.TradeNo, "", PaymentProviderStripe, "")
	require.ErrorIs(t, err, ErrSubscriptionPaymentAmountRequired)
	assert.Equal(t, common.TopUpStatusPending, GetSubscriptionOrderByTradeNo(order.TradeNo).Status)
}

func TestCalcSubscriptionBalanceQuotaRejectsOverflow(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })
	common.QuotaPerUnit = 500_000

	quota, err := calcSubscriptionBalanceQuota(9999)
	require.Error(t, err)
	assert.Zero(t, quota)
}

func TestCalculateSubscriptionPaymentAmountUsesExactMinorSnapshot(t *testing.T) {
	amount, minor, err := CalculateSubscriptionPaymentAmount(9.99, 7.3)
	require.NoError(t, err)
	assert.InDelta(t, 72.93, amount, 0.000001)
	assert.EqualValues(t, 7293, minor)

	parsed, err := ParseSubscriptionAmountMinor("72.93")
	require.NoError(t, err)
	assert.Equal(t, minor, parsed)
	_, err = ParseSubscriptionAmountMinor("72.931")
	require.Error(t, err)
}

func TestRefundSubscriptionPreConsumeUsesOuterTransaction(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&SubscriptionPreConsumeRecord{}))
	require.NoError(t, DB.Exec("DELETE FROM subscription_pre_consume_records").Error)
	t.Cleanup(func() { _ = DB.Exec("DELETE FROM subscription_pre_consume_records").Error })
	seedSubscriptionPaymentUser(t, 8106, 0)
	plan := seedSubscriptionPaymentPlan(t, 8206, 0)
	subscription := &UserSubscription{
		UserId:      8106,
		PlanId:      plan.Id,
		AmountTotal: 100,
		AmountUsed:  50,
		StartTime:   time.Now().Add(-time.Hour).Unix(),
		EndTime:     time.Now().Add(time.Hour).Unix(),
		Status:      "active",
	}
	require.NoError(t, DB.Create(subscription).Error)
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
		RequestId:          "refund-no-nested-tx",
		UserId:             8106,
		UserSubscriptionId: subscription.Id,
		PreConsumed:        50,
		Status:             "consumed",
	}).Error)

	require.NoError(t, RefundSubscriptionPreConsume("refund-no-nested-tx"))
	var storedSub UserSubscription
	require.NoError(t, DB.First(&storedSub, subscription.Id).Error)
	assert.Zero(t, storedSub.AmountUsed)
	var record SubscriptionPreConsumeRecord
	require.NoError(t, DB.Where("request_id = ?", "refund-no-nested-tx").First(&record).Error)
	assert.Equal(t, "refunded", record.Status)
}

func TestResetDueSubscriptionUsesEntitlementSnapshotWithoutPlan(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	subscription := &UserSubscription{
		UserId:              8108,
		PlanId:              999_999,
		AmountTotal:         100,
		AmountUsed:          50,
		StartTime:           now - 2*24*60*60,
		EndTime:             now + 24*60*60,
		Status:              "active",
		LastResetTime:       now - 2*24*60*60,
		NextResetTime:       now - 1,
		QuotaResetPeriod:    SubscriptionResetDaily,
		AllowWalletOverflow: true,
	}
	require.NoError(t, DB.Create(subscription).Error)

	resetCount, err := ResetDueSubscriptions(10)
	require.NoError(t, err)
	assert.Equal(t, 1, resetCount)

	var stored UserSubscription
	require.NoError(t, DB.First(&stored, subscription.Id).Error)
	assert.Zero(t, stored.AmountUsed)
	assert.EqualValues(t, 1, stored.QuotaResetVersion)
	assert.Greater(t, stored.NextResetTime, now)
}

func TestValidateSubscriptionPlanRejectsUnsafeValues(t *testing.T) {
	plan := &SubscriptionPlan{
		PriceAmount:      math.NaN(),
		Currency:         "USD",
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		QuotaResetPeriod: SubscriptionResetNever,
	}
	require.Error(t, ValidateSubscriptionPlan(plan))

	plan.PriceAmount = 10
	plan.DurationValue = maxSubscriptionDurationMonths + 1
	require.Error(t, ValidateSubscriptionPlan(plan))

	plan.DurationValue = 1
	plan.TotalAmount = math.MaxInt64
	require.NoError(t, ValidateSubscriptionPlan(plan))
}
