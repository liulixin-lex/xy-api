package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertUserForPaymentGuardTest(t *testing.T, id int, quota int) {
	t.Helper()
	user := &User{
		Id:       id,
		Username: "payment_guard_user",
		Status:   common.UserStatusEnabled,
		Quota:    quota,
	}
	require.NoError(t, DB.Create(user).Error)
}

func insertSubscriptionPlanForPaymentGuardTest(t *testing.T, id int) *SubscriptionPlan {
	t.Helper()
	plan := &SubscriptionPlan{
		Id:            id,
		Title:         "Guard Plan",
		PriceAmount:   9.99,
		Currency:      "USD",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   1000,
	}
	require.NoError(t, DB.Create(plan).Error)
	return plan
}

func insertSubscriptionOrderForPaymentGuardTest(t *testing.T, tradeNo string, userID int, planID int, paymentProvider string) {
	t.Helper()
	order := &SubscriptionOrder{
		UserId:          userID,
		PlanId:          planID,
		Money:           9.99,
		TradeNo:         tradeNo,
		PaymentMethod:   paymentProvider,
		PaymentProvider: paymentProvider,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, order.Insert())
}

func insertTopUpForPaymentGuardTest(t *testing.T, tradeNo string, userID int, paymentProvider string) {
	t.Helper()
	creditQuotaSnapshot := int64(0)
	if paymentProvider == PaymentProviderWaffo || paymentProvider == PaymentProviderWaffoPancake {
		creditQuotaSnapshot = 2
	}
	topUp := &TopUp{
		UserId:              userID,
		Amount:              2,
		Money:               9.99,
		TradeNo:             tradeNo,
		PaymentMethod:       paymentProvider,
		PaymentProvider:     paymentProvider,
		Currency:            "USD",
		ExpectedAmountMinor: 999,
		CreditQuotaSnapshot: creditQuotaSnapshot,
		Status:              common.TopUpStatusPending,
		CreateTime:          time.Now().Unix(),
	}
	require.NoError(t, topUp.Insert())
}

func getTopUpStatusForPaymentGuardTest(t *testing.T, tradeNo string) string {
	t.Helper()
	topUp := GetTopUpByTradeNo(tradeNo)
	require.NotNil(t, topUp)
	return topUp.Status
}

func countUserSubscriptionsForPaymentGuardTest(t *testing.T, userID int) int64 {
	t.Helper()
	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", userID).Count(&count).Error)
	return count
}

func getUserQuotaForPaymentGuardTest(t *testing.T, userID int) int {
	t.Helper()
	var user User
	require.NoError(t, DB.Select("quota").Where("id = ?", userID).First(&user).Error)
	return user.Quota
}

func TestRechargeWaffoPancake_RejectsMismatchedPaymentMethod(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 101, 0)
	insertTopUpForPaymentGuardTest(t, "waffo-pancake-guard", 101, PaymentProviderStripe)

	paidAmountMinor := int64(999)
	err := RechargeWaffoPancake("waffo-pancake-guard", TopUpPaymentConfirmation{
		PaidAmountMinor: &paidAmountMinor,
		Currency:        "USD",
		ProviderOrderId: "pancake_order_guard",
	})
	require.Error(t, err)

	topUp := GetTopUpByTradeNo("waffo-pancake-guard")
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusPending, topUp.Status)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 101))
}

func TestRechargeCreemIsIdempotentAndDoesNotTrustCallbackIdentity(t *testing.T) {
	truncateTables(t)
	user := &User{
		Id: 102, Username: "creem_callback_identity", Email: "",
		Status: common.UserStatusEnabled, Quota: 0,
	}
	require.NoError(t, DB.Create(user).Error)
	insertTopUpForPaymentGuardTest(t, "creem-idempotent", user.Id, PaymentProviderCreem)

	paidAmountMinor := int64(999)
	confirmation := TopUpPaymentConfirmation{
		PaidAmountMinor: &paidAmountMinor,
		Currency:        "USD",
		ProviderOrderId: "creem_order_idempotent",
	}
	require.NoError(t, RechargeCreem("creem-idempotent", confirmation, "127.0.0.1"))
	var afterFirst User
	require.NoError(t, DB.First(&afterFirst, user.Id).Error)
	assert.Empty(t, afterFirst.Email)
	assert.Positive(t, afterFirst.Quota)

	require.NoError(t, RechargeCreem("creem-idempotent", confirmation, "127.0.0.1"))
	var afterSecond User
	require.NoError(t, DB.First(&afterSecond, user.Id).Error)
	assert.Equal(t, afterFirst.Quota, afterSecond.Quota)
	assert.Empty(t, afterSecond.Email)
	assert.Equal(t, common.TopUpStatusSuccess, getTopUpStatusForPaymentGuardTest(t, "creem-idempotent"))
}

func TestRechargeCreemQuarantinesInvalidCreditQuota(t *testing.T) {
	for _, amount := range []int64{-1, int64(common.MaxQuota) + 1} {
		t.Run(fmt.Sprintf("amount_%d", amount), func(t *testing.T) {
			truncateTables(t)
			const userID = 103
			tradeNo := fmt.Sprintf("creem-invalid-credit-%d", amount)
			insertUserForPaymentGuardTest(t, userID, 0)
			insertTopUpForPaymentGuardTest(t, tradeNo, userID, PaymentProviderCreem)
			require.NoError(t, DB.Model(&TopUp{}).Where("trade_no = ?", tradeNo).Update("amount", amount).Error)

			paidAmountMinor := int64(999)
			err := RechargeCreem(tradeNo, TopUpPaymentConfirmation{
				PaidAmountMinor: &paidAmountMinor,
				Currency:        "USD",
				ProviderOrderId: "creem_invalid_credit_order",
			}, "127.0.0.1")

			require.ErrorIs(t, err, ErrTopUpPaymentManualReview)
			assert.Zero(t, getUserQuotaForPaymentGuardTest(t, userID))
			topUp := GetTopUpByTradeNo(tradeNo)
			require.NotNil(t, topUp)
			assert.Equal(t, common.TopUpStatusManualReview, topUp.Status)
			assert.Equal(t, "invalid_credit_quota", topUp.ReviewReason)
		})
	}
}

func TestRetainedTopUpSettlementQuarantinesUnsafeCreditSnapshot(t *testing.T) {
	for _, provider := range []string{PaymentProviderWaffo, PaymentProviderWaffoPancake} {
		for _, snapshot := range []int64{0, int64(common.MaxQuota) + 1} {
			t.Run(fmt.Sprintf("%s/snapshot_%d", provider, snapshot), func(t *testing.T) {
				truncateTables(t)
				const userID = 104
				tradeNo := fmt.Sprintf("%s-unsafe-credit-%d", provider, snapshot)
				insertUserForPaymentGuardTest(t, userID, 0)
				require.NoError(t, DB.Create(&TopUp{
					UserId: userID, Amount: 2, Money: 9.99, TradeNo: tradeNo,
					PaymentMethod: provider, PaymentProvider: provider,
					Currency: "USD", ExpectedAmountMinor: 999, CreditQuotaSnapshot: snapshot,
					Status: common.TopUpStatusPending, CreateTime: time.Now().Unix(),
				}).Error)

				paidAmountMinor := int64(999)
				confirmation := TopUpPaymentConfirmation{
					PaidAmountMinor: &paidAmountMinor,
					Currency:        "USD",
					ProviderOrderId: provider + "_quota_overflow_order",
				}
				var err error
				if provider == PaymentProviderWaffo {
					err = RechargeWaffo(tradeNo, confirmation, "127.0.0.1")
				} else {
					err = RechargeWaffoPancake(tradeNo, confirmation)
				}

				require.ErrorIs(t, err, ErrTopUpPaymentManualReview)
				assert.Zero(t, getUserQuotaForPaymentGuardTest(t, userID))
				topUp := GetTopUpByTradeNo(tradeNo)
				require.NotNil(t, topUp)
				assert.Equal(t, common.TopUpStatusManualReview, topUp.Status)
				assert.Equal(t, "invalid_credit_quota", topUp.ReviewReason)
			})
		}
	}
}

func TestRetainedTopUpSettlementUsesPersistedCreditSnapshot(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })

	for _, provider := range []string{PaymentProviderWaffo, PaymentProviderWaffoPancake} {
		t.Run(provider, func(t *testing.T) {
			truncateTables(t)
			const userID = 105
			tradeNo := provider + "-persisted-credit"
			insertUserForPaymentGuardTest(t, userID, 0)
			common.QuotaPerUnit = 2
			require.NoError(t, (&TopUp{
				UserId: userID, Amount: 3, Money: 9.99, TradeNo: tradeNo,
				PaymentMethod: provider, PaymentProvider: provider,
				Currency: "USD", ExpectedAmountMinor: 999, CreditQuotaSnapshot: 6,
				Status: common.TopUpStatusPending, CreateTime: time.Now().Unix(),
			}).Insert())

			common.QuotaPerUnit = 999_999
			paidAmountMinor := int64(999)
			confirmation := TopUpPaymentConfirmation{
				PaidAmountMinor: &paidAmountMinor,
				Currency:        "USD",
				ProviderOrderId: provider + "_persisted_credit_order",
			}
			var err error
			if provider == PaymentProviderWaffo {
				err = RechargeWaffo(tradeNo, confirmation, "127.0.0.1")
			} else {
				err = RechargeWaffoPancake(tradeNo, confirmation)
			}

			require.NoError(t, err)
			assert.Equal(t, 6, getUserQuotaForPaymentGuardTest(t, userID))
		})
	}
}

func TestRetainedTopUpInsertRequiresCreditSnapshot(t *testing.T) {
	truncateTables(t)
	err := (&TopUp{
		UserId: 106, Amount: 1, Money: 1, TradeNo: "waffo-missing-credit-snapshot",
		PaymentMethod: PaymentProviderWaffo, PaymentProvider: PaymentProviderWaffo,
		Currency: "USD", ExpectedAmountMinor: 100,
		Status: common.TopUpStatusPending, CreateTime: time.Now().Unix(),
	}).Insert()
	require.ErrorIs(t, err, ErrTopUpPaymentSnapshotMissing)
}

func TestTopUpCreditQuotaSnapshotIsNotSerialized(t *testing.T) {
	payload, err := common.Marshal(&TopUp{CreditQuotaSnapshot: 123})
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "credit_quota_snapshot")
}

func TestUpdatePendingTopUpStatus_RejectsMismatchedPaymentProvider(t *testing.T) {
	testCases := []struct {
		name                    string
		tradeNo                 string
		storedPaymentProvider   string
		expectedPaymentProvider string
		targetStatus            string
	}{
		{
			name:                    "stripe expire",
			tradeNo:                 "stripe-expire-guard",
			storedPaymentProvider:   PaymentProviderCreem,
			expectedPaymentProvider: PaymentProviderStripe,
			targetStatus:            common.TopUpStatusExpired,
		},
		{
			name:                    "waffo failed",
			tradeNo:                 "waffo-failed-guard",
			storedPaymentProvider:   PaymentProviderStripe,
			expectedPaymentProvider: PaymentProviderWaffo,
			targetStatus:            common.TopUpStatusFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			insertUserForPaymentGuardTest(t, 150, 0)
			insertTopUpForPaymentGuardTest(t, tc.tradeNo, 150, tc.storedPaymentProvider)

			err := UpdatePendingTopUpStatus(tc.tradeNo, tc.expectedPaymentProvider, tc.targetStatus)
			require.ErrorIs(t, err, ErrPaymentMethodMismatch)
			assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, tc.tradeNo))
		})
	}
}

func TestCompleteSubscriptionOrder_RejectsMismatchedPaymentProvider(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 202, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 301)
	insertSubscriptionOrderForPaymentGuardTest(t, "sub-guard-order", 202, plan.Id, PaymentProviderStripe)

	err := CompleteSubscriptionOrder("sub-guard-order", `{"provider":"epay"}`, PaymentProviderEpay, "alipay")
	require.ErrorIs(t, err, ErrPaymentMethodMismatch)

	order := GetSubscriptionOrderByTradeNo("sub-guard-order")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, 202))

	topUp := GetTopUpByTradeNo("sub-guard-order")
	assert.Nil(t, topUp)
}

func TestExpireSubscriptionOrder_RejectsMismatchedPaymentProvider(t *testing.T) {
	truncateTables(t)

	insertUserForPaymentGuardTest(t, 303, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 401)
	insertSubscriptionOrderForPaymentGuardTest(t, "sub-expire-guard", 303, plan.Id, PaymentProviderStripe)

	err := ExpireSubscriptionOrder("sub-expire-guard", PaymentProviderCreem)
	require.ErrorIs(t, err, ErrPaymentMethodMismatch)

	order := GetSubscriptionOrderByTradeNo("sub-expire-guard")
	require.NotNil(t, order)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
}
