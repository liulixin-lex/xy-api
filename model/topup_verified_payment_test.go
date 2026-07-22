package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedVerifiedTopUpTest(t *testing.T, userID int, tradeNo string, provider string, expectedMinor int64, currency string) {
	t.Helper()
	require.NoError(t, DB.Create(&User{
		Id:       userID,
		Username: "verified_topup_" + tradeNo,
		AffCode:  "verified_aff_" + tradeNo,
		Status:   common.UserStatusEnabled,
	}).Error)
	creditQuotaSnapshot := int64(0)
	if provider == PaymentProviderWaffo || provider == PaymentProviderWaffoPancake {
		creditQuotaSnapshot = 2
	}
	require.NoError(t, (&TopUp{
		UserId:              userID,
		Amount:              2,
		Money:               10,
		TradeNo:             tradeNo,
		PaymentMethod:       provider,
		PaymentProvider:     provider,
		Currency:            currency,
		ExpectedAmountMinor: expectedMinor,
		CreditQuotaSnapshot: creditQuotaSnapshot,
		Status:              common.TopUpStatusPending,
		CreateTime:          time.Now().Unix(),
	}).Insert())
}

func verifiedTopUpConfirmation(amountMinor int64, currency string, providerOrderID string) TopUpPaymentConfirmation {
	return TopUpPaymentConfirmation{
		PaidAmountMinor: &amountMinor,
		Currency:        currency,
		ProviderOrderId: providerOrderID,
	}
}

func TestVerifiedTopUpAmountMismatchMovesOrderToManualReview(t *testing.T) {
	truncateTables(t)
	seedVerifiedTopUpTest(t, 801, "verified-creem-amount", PaymentProviderCreem, 1000, "USD")

	err := RechargeCreem(
		"verified-creem-amount",
		verifiedTopUpConfirmation(999, "USD", "creem_order_amount_mismatch"),
		"127.0.0.1",
	)
	require.ErrorIs(t, err, ErrTopUpPaymentAmountMismatch)

	topUp := GetTopUpByTradeNo("verified-creem-amount")
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusManualReview, topUp.Status)
	assert.Equal(t, "paid_amount_mismatch", topUp.ReviewReason)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 801))
}

func TestVerifiedTopUpCurrencyMismatchMovesOrderToManualReview(t *testing.T) {
	truncateTables(t)
	seedVerifiedTopUpTest(t, 802, "verified-waffo-currency", PaymentProviderWaffo, 1000, "USD")

	err := RechargeWaffo(
		"verified-waffo-currency",
		verifiedTopUpConfirmation(1000, "EUR", "waffo_order_currency_mismatch"),
		"127.0.0.1",
	)
	require.ErrorIs(t, err, ErrTopUpPaymentCurrencyMismatch)

	topUp := GetTopUpByTradeNo("verified-waffo-currency")
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusManualReview, topUp.Status)
	assert.Equal(t, "payment_currency_mismatch", topUp.ReviewReason)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 802))
}

func TestVerifiedTopUpDuplicateCallbackCreditsOnlyOnce(t *testing.T) {
	truncateTables(t)
	seedVerifiedTopUpTest(t, 803, "verified-pancake-duplicate", PaymentProviderWaffoPancake, 1000, "USD")
	confirmation := verifiedTopUpConfirmation(1000, "USD", "pancake_order_duplicate")

	require.NoError(t, RechargeWaffoPancake("verified-pancake-duplicate", confirmation))
	quotaAfterFirst := getUserQuotaForPaymentGuardTest(t, 803)
	require.Positive(t, quotaAfterFirst)
	require.NoError(t, RechargeWaffoPancake("verified-pancake-duplicate", confirmation))
	assert.Equal(t, quotaAfterFirst, getUserQuotaForPaymentGuardTest(t, 803))

	topUp := GetTopUpByTradeNo("verified-pancake-duplicate")
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusSuccess, topUp.Status)
	assert.Empty(t, topUp.ReviewReason)
	assert.Equal(t, "pancake_order_duplicate", topUp.ProviderOrderId)
	require.NotNil(t, topUp.ProviderOrderKey)
	assert.Equal(t, PaymentProviderWaffoPancake+":pancake_order_duplicate", *topUp.ProviderOrderKey)
}

func TestVerifiedTopUpPaidCallbackRecoversFailedOrExpiredOrder(t *testing.T) {
	for index, status := range []string{common.TopUpStatusFailed, common.TopUpStatusExpired} {
		t.Run(status, func(t *testing.T) {
			truncateTables(t)
			userID := 813 + index
			tradeNo := "verified-creem-recovery-" + status
			seedVerifiedTopUpTest(t, userID, tradeNo, PaymentProviderCreem, 1000, "USD")
			require.NoError(t, DB.Model(&TopUp{}).Where("trade_no = ?", tradeNo).Update("status", status).Error)

			require.NoError(t, RechargeCreem(
				tradeNo,
				verifiedTopUpConfirmation(1000, "USD", "creem_order_recovery_"+status),
				"127.0.0.1",
			))

			stored := GetTopUpByTradeNo(tradeNo)
			require.NotNil(t, stored)
			assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
			assert.Positive(t, getUserQuotaForPaymentGuardTest(t, userID))
		})
	}
}

func TestVerifiedTopUpCompletedCallbackConflictPreservesCreditAndRecordsReview(t *testing.T) {
	tests := []struct {
		name           string
		userID         int
		tradeNo        string
		mutate         func(*TopUpPaymentConfirmation)
		expectedErr    error
		expectedReason string
	}{
		{
			name: "amount mismatch", userID: 807, tradeNo: "verified-completed-amount",
			mutate: func(confirmation *TopUpPaymentConfirmation) {
				amount := int64(999)
				confirmation.PaidAmountMinor = &amount
			},
			expectedErr: ErrTopUpPaymentAmountMismatch, expectedReason: "completed_callback_amount_mismatch",
		},
		{
			name: "currency mismatch", userID: 808, tradeNo: "verified-completed-currency",
			mutate: func(confirmation *TopUpPaymentConfirmation) {
				confirmation.Currency = "EUR"
			},
			expectedErr: ErrTopUpPaymentCurrencyMismatch, expectedReason: "completed_callback_currency_mismatch",
		},
		{
			name: "provider order mismatch", userID: 809, tradeNo: "verified-completed-provider-order",
			mutate: func(confirmation *TopUpPaymentConfirmation) {
				confirmation.ProviderOrderId = "different_pancake_order"
			},
			expectedErr: ErrTopUpPaymentManualReview, expectedReason: "completed_callback_provider_order_id_mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			seedVerifiedTopUpTest(t, test.userID, test.tradeNo, PaymentProviderWaffoPancake, 1000, "USD")
			correct := verifiedTopUpConfirmation(1000, "USD", "pancake_order_"+test.tradeNo)
			require.NoError(t, RechargeWaffoPancake(test.tradeNo, correct))
			quotaAfterCompletion := getUserQuotaForPaymentGuardTest(t, test.userID)
			require.Positive(t, quotaAfterCompletion)

			conflicting := correct
			test.mutate(&conflicting)
			err := RechargeWaffoPancake(test.tradeNo, conflicting)
			require.ErrorIs(t, err, test.expectedErr)
			assert.Equal(t, quotaAfterCompletion, getUserQuotaForPaymentGuardTest(t, test.userID))

			stored := GetTopUpByTradeNo(test.tradeNo)
			require.NotNil(t, stored)
			assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
			assert.Equal(t, test.expectedReason, stored.ReviewReason)
			assert.Equal(t, correct.ProviderOrderId, stored.ProviderOrderId)

			// A later exact duplicate remains a successful no-op and cannot erase
			// the durable incident evidence from the conflicting callback.
			require.NoError(t, RechargeWaffoPancake(test.tradeNo, correct))
			assert.Equal(t, quotaAfterCompletion, getUserQuotaForPaymentGuardTest(t, test.userID))
			stored = GetTopUpByTradeNo(test.tradeNo)
			require.NotNil(t, stored)
			assert.Equal(t, test.expectedReason, stored.ReviewReason)
		})
	}
}

func TestVerifiedTopUpLegacyPendingOrderWithoutSnapshotRequiresManualReview(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&User{
		Id:       804,
		Username: "verified_topup_legacy",
		AffCode:  "verified_aff_legacy",
		Status:   common.UserStatusEnabled,
	}).Error)
	// DB.Create deliberately simulates a pre-migration pending row. New
	// retained-provider orders use TopUp.Insert, which rejects this shape.
	require.NoError(t, DB.Create(&TopUp{
		UserId:          804,
		Amount:          2,
		Money:           10,
		TradeNo:         "verified-waffo-legacy",
		PaymentMethod:   PaymentMethodWaffo,
		PaymentProvider: PaymentProviderWaffo,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}).Error)

	err := RechargeWaffo(
		"verified-waffo-legacy",
		verifiedTopUpConfirmation(1000, "USD", "waffo_order_legacy"),
		"127.0.0.1",
	)
	require.ErrorIs(t, err, ErrTopUpPaymentSnapshotMissing)

	topUp := GetTopUpByTradeNo("verified-waffo-legacy")
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusManualReview, topUp.Status)
	assert.Equal(t, "missing_payment_snapshot", topUp.ReviewReason)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 804))
}

func TestVerifiedTopUpProviderOrderCannotCreditTwoLocalOrders(t *testing.T) {
	truncateTables(t)
	seedVerifiedTopUpTest(t, 805, "verified-creem-provider-first", PaymentProviderCreem, 1000, "USD")
	seedVerifiedTopUpTest(t, 806, "verified-creem-provider-second", PaymentProviderCreem, 1000, "USD")
	confirmation := verifiedTopUpConfirmation(1000, "USD", "creem_order_reused")

	require.NoError(t, RechargeCreem("verified-creem-provider-first", confirmation, "127.0.0.1"))
	err := RechargeCreem("verified-creem-provider-second", confirmation, "127.0.0.1")
	require.ErrorIs(t, err, ErrTopUpPaymentManualReview)

	second := GetTopUpByTradeNo("verified-creem-provider-second")
	require.NotNil(t, second)
	assert.Equal(t, common.TopUpStatusManualReview, second.Status)
	assert.Equal(t, "provider_order_reused", second.ReviewReason)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, 806))
}

func TestParseProviderPaymentAmountMinorUsesCurrencyExponent(t *testing.T) {
	tests := []struct {
		name     string
		amount   string
		currency string
		expected int64
	}{
		{name: "two decimals", amount: "10.25", currency: "USD", expected: 1025},
		{name: "zero decimals", amount: "109", currency: "JPY", expected: 109},
		{name: "three decimals", amount: "1.234", currency: "KWD", expected: 1234},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := ParseProviderPaymentAmountMinor(test.amount, PaymentProviderWaffo, test.currency)
			require.NoError(t, err)
			assert.Equal(t, test.expected, actual)
		})
	}

	_, err := ParseProviderPaymentAmountMinor("10.001", PaymentProviderWaffo, "USD")
	require.Error(t, err)
}
