package model

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func prepareBillingReservationTest(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(
		&BillingReservation{},
		&QuotaLedgerEntry{},
		&SubscriptionPreConsumeRecord{},
		&UserSubscription{},
	))
	require.NoError(t, DB.Exec("DELETE FROM quota_ledger_entries").Error)
	require.NoError(t, DB.Exec("DELETE FROM billing_reservations").Error)
	require.NoError(t, DB.Exec("DELETE FROM subscription_pre_consume_records").Error)
	t.Cleanup(func() {
		DB.Exec("DELETE FROM quota_ledger_entries")
		DB.Exec("DELETE FROM billing_reservations")
		DB.Exec("DELETE FROM subscription_pre_consume_records")
	})
}

func TestCleanupSubscriptionPreConsumeRecordsPreservesOpenReservations(t *testing.T) {
	prepareBillingReservationTest(t)
	oldTimestamp := time.Now().Add(-8 * 24 * time.Hour).Unix()
	for _, requestID := range []string{"billing-cleanup-open", "billing-cleanup-final"} {
		require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
			RequestId: requestID, UserId: 1, UserSubscriptionId: 1, PreConsumed: 10, Status: "consumed",
		}).Error)
		require.NoError(t, DB.Model(&SubscriptionPreConsumeRecord{}).
			Where("request_id = ?", requestID).UpdateColumn("updated_at", oldTimestamp).Error)
	}
	require.NoError(t, DB.Create(&BillingReservation{
		RequestId: "billing-cleanup-open", UserId: 1, FundingSource: BillingFundingSubscription,
		Status: BillingReservationStatusReserved, Version: 1,
	}).Error)
	require.NoError(t, DB.Create(&BillingReservation{
		RequestId: "billing-cleanup-final", UserId: 1, FundingSource: BillingFundingSubscription,
		Status: BillingReservationStatusSettled, Version: 2,
	}).Error)

	deleted, err := CleanupSubscriptionPreConsumeRecords(7 * 24 * 3600)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)
	var openCount, finalCount int64
	require.NoError(t, DB.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", "billing-cleanup-open").Count(&openCount).Error)
	require.NoError(t, DB.Model(&SubscriptionPreConsumeRecord{}).Where("request_id = ?", "billing-cleanup-final").Count(&finalCount).Error)
	assert.EqualValues(t, 1, openCount)
	assert.Zero(t, finalCount)
}

func seedBillingWallet(t *testing.T, userId, tokenId, quota int, tokenKey string) {
	t.Helper()
	require.NoError(t, DB.Create(&User{
		Id:       userId,
		Username: "billing-user-" + tokenKey,
		Quota:    quota,
		Status:   common.UserStatusEnabled,
	}).Error)
	require.NoError(t, DB.Create(&Token{
		Id:          tokenId,
		UserId:      userId,
		Key:         tokenKey,
		Name:        "billing-token",
		Status:      common.TokenStatusEnabled,
		RemainQuota: quota,
	}).Error)
	t.Cleanup(func() {
		DB.Unscoped().Delete(&Token{}, tokenId)
		DB.Unscoped().Delete(&User{}, userId)
	})
}

func seedBillingSubscription(t *testing.T, userId, planId, subscriptionId int, total, used int64) *UserSubscription {
	t.Helper()
	plan := &SubscriptionPlan{
		Id:               planId,
		Title:            "Billing subscription",
		PriceAmount:      10,
		Currency:         "USD",
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		Enabled:          true,
		TotalAmount:      total,
		QuotaResetPeriod: SubscriptionResetNever,
	}
	require.NoError(t, DB.Create(plan).Error)
	subscription := &UserSubscription{
		Id:          subscriptionId,
		UserId:      userId,
		PlanId:      plan.Id,
		AmountTotal: total,
		AmountUsed:  used,
		StartTime:   time.Now().Add(-time.Hour).Unix(),
		EndTime:     time.Now().Add(24 * time.Hour).Unix(),
		Status:      "active",
	}
	require.NoError(t, DB.Create(subscription).Error)
	t.Cleanup(func() {
		DB.Delete(&UserSubscription{}, subscription.Id)
		DB.Delete(&SubscriptionPlan{}, plan.Id)
	})
	return subscription
}

func loadBillingBalances(t *testing.T, userId, tokenId int) (int, int, int) {
	t.Helper()
	var user User
	var token Token
	require.NoError(t, DB.Select("quota").Where("id = ?", userId).First(&user).Error)
	require.NoError(t, DB.Select("remain_quota", "used_quota").Where("id = ?", tokenId).First(&token).Error)
	return user.Quota, token.RemainQuota, token.UsedQuota
}

func TestBillingReservationWalletLifecycleIsIdempotent(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910001, 920001, 200, "billing-wallet-lifecycle")

	input := BillingReservationInput{
		RequestId:     "billing-wallet-lifecycle",
		UserId:        910001,
		TokenId:       920001,
		FundingSource: BillingFundingWallet,
		Quota:         60,
	}
	first, err := PreConsumeBillingReservation(input, "billing-wallet-lifecycle")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusReserved, first.Reservation.Status)
	assert.Equal(t, 60, first.Reservation.TokenReserved)

	_, err = PreConsumeBillingReservation(input, "billing-wallet-lifecycle")
	require.NoError(t, err)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910001, 920001)
	assert.Equal(t, 140, userQuota)
	assert.Equal(t, 140, tokenRemain)
	assert.Equal(t, 60, tokenUsed)

	_, err = ExtendBillingReservation(input.RequestId, 100, "billing-wallet-lifecycle")
	require.NoError(t, err)
	_, err = ExtendBillingReservation(input.RequestId, 100, "billing-wallet-lifecycle")
	require.NoError(t, err)
	userQuota, tokenRemain, tokenUsed = loadBillingBalances(t, 910001, 920001)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Equal(t, 100, tokenUsed)

	settled, err := SettleBillingReservation(input.RequestId, 70, "billing-wallet-lifecycle")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusSettled, settled.Status)
	assert.Equal(t, 70, settled.SettledQuota)
	_, err = SettleBillingReservation(input.RequestId, 70, "billing-wallet-lifecycle")
	require.NoError(t, err)
	_, err = SettleBillingReservation(input.RequestId, 71, "billing-wallet-lifecycle")
	assert.ErrorIs(t, err, ErrBillingReservationConflict)

	userQuota, tokenRemain, tokenUsed = loadBillingBalances(t, 910001, 920001)
	assert.Equal(t, 130, userQuota)
	assert.Equal(t, 130, tokenRemain)
	assert.Equal(t, 70, tokenUsed)

	var ledgerCount int64
	require.NoError(t, DB.Model(&QuotaLedgerEntry{}).Where("request_id = ?", input.RequestId).Count(&ledgerCount).Error)
	assert.EqualValues(t, 3, ledgerCount)
}

func TestBillingSettlementIntentRetargetingIsExplicitAndMonotonic(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910035, 920035, 200, "billing-intent-retarget")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-intent-retarget",
		UserId:        910035,
		TokenId:       920035,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "billing-intent-retarget")
	require.NoError(t, err)

	require.NoError(t, PrepareBillingReservationSettlement("billing-intent-retarget", 50))
	require.NoError(t, PrepareBillingReservationSettlement("billing-intent-retarget", 50))
	assert.ErrorIs(t, PrepareBillingReservationSettlement("billing-intent-retarget", 60), ErrBillingReservationConflict)

	// Realtime usage may only advance an unbound liability; it cannot lower or
	// arbitrarily replace a target through the strict settlement API.
	require.NoError(t, AdvanceUnboundBillingReservationSettlementIntent("billing-intent-retarget", 60))
	assert.ErrorIs(t, AdvanceUnboundBillingReservationSettlementIntent("billing-intent-retarget", 55), ErrBillingReservationConflict)
	_, err = MarkBillingReservationSettlementShortfall("billing-intent-retarget", 55, BillingSettlementFailureUserQuota)
	assert.ErrorIs(t, err, ErrBillingReservationConflict)
	require.NoError(t, DB.Model(&BillingReservation{}).Where("request_id = ?", "billing-intent-retarget").Updates(map[string]interface{}{
		"resource_type": BillingResourceAsyncTask,
		"resource_id":   "task_bound_after_intent",
	}).Error)
	assert.ErrorIs(t, AdvanceUnboundBillingReservationSettlementIntent("billing-intent-retarget", 70), ErrBillingReservationConflict)
	_, err = MarkBillingReservationSettlementShortfall("billing-intent-retarget", 70, BillingSettlementFailureUserQuota)
	assert.ErrorIs(t, err, ErrBillingReservationConflict)
}

func TestBillingShortfallFreezeWaitsForAllPaymentAndBillingLiabilities(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910008, 920008, 50, "billing-cross-freeze")
	result, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-cross-freeze",
		UserId:        910008,
		TokenId:       920008,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "billing-cross-freeze")
	require.NoError(t, err)
	require.NoError(t, PrepareBillingReservationSettlement(result.Reservation.RequestId, 60))
	_, err = MarkBillingReservationSettlementShortfall(result.Reservation.RequestId, 60, BillingSettlementFailureUserQuota)
	require.NoError(t, err)

	debt := PaymentDebt{
		PaymentOrderID:     910008,
		UserID:             910008,
		DebtKind:           PaymentDebtKindBuyer,
		OriginalQuota:      1,
		OutstandingQuota:   1,
		PreviousUserStatus: common.UserStatusEnabled,
		FreezeApplied:      true,
		Status:             PaymentDebtStatusOpen,
		CreatedAt:          common.GetTimestamp(),
		UpdatedAt:          common.GetTimestamp(),
	}
	require.NoError(t, DB.Create(&debt).Error)
	t.Cleanup(func() { DB.Delete(&PaymentDebt{}, debt.ID) })
	assert.ErrorIs(t, EnableUserIfNoPaymentFreeze(910008), ErrUserPaymentFreezeOpen)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 910008).Update("quota", 30).Error)
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", 920008).Update("remain_quota", 30).Error)

	_, err = SettleBillingReservation(result.Reservation.RequestId, 60, "billing-cross-freeze")
	require.NoError(t, err)
	var user User
	require.NoError(t, DB.First(&user, 910008).Error)
	assert.True(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusDisabled, user.Status)

	debt.Status = PaymentDebtStatusResolved
	debt.OutstandingQuota = 0
	debt.ResolvedAt = common.GetTimestamp()
	require.NoError(t, DB.Save(&debt).Error)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		return releasePaymentFreezeTx(tx, &debt)
	}))
	require.NoError(t, DB.First(&user, 910008).Error)
	assert.False(t, user.PaymentFrozen)
	assert.Equal(t, common.UserStatusEnabled, user.Status)
}

func TestBillingSettlementFailureClassificationDoesNotFreezeTransientErrors(t *testing.T) {
	code, deterministic := BillingSettlementFailureCode(errors.New("database temporarily unavailable"))
	assert.False(t, deterministic)
	assert.Empty(t, code)

	code, deterministic = BillingSettlementFailureCode(ErrInsufficientSubscriptionQuota)
	assert.True(t, deterministic)
	assert.Equal(t, BillingSettlementFailureSubscriptionQuota, code)
}

func TestBillingReservationRejectsIdempotencyKeyReuse(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910002, 920002, 100, "billing-conflict")

	input := BillingReservationInput{
		RequestId:     "billing-conflict",
		UserId:        910002,
		TokenId:       920002,
		FundingSource: BillingFundingWallet,
		Quota:         20,
	}
	_, err := PreConsumeBillingReservation(input, "billing-conflict")
	require.NoError(t, err)
	input.Quota = 21
	_, err = PreConsumeBillingReservation(input, "billing-conflict")
	assert.ErrorIs(t, err, ErrBillingReservationConflict)

	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910002, 920002)
	assert.Equal(t, 80, userQuota)
	assert.Equal(t, 80, tokenRemain)
	assert.Equal(t, 20, tokenUsed)
}

func TestBillingReservationRollsBackWhenTokenQuotaIsInsufficient(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910003, 920003, 50, "billing-rollback")
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", 920003).Update("remain_quota", 30).Error)

	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-rollback",
		UserId:        910003,
		TokenId:       920003,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "billing-rollback")
	assert.ErrorIs(t, err, ErrInsufficientTokenQuota)

	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910003, 920003)
	assert.Equal(t, 50, userQuota)
	assert.Equal(t, 30, tokenRemain)
	assert.Zero(t, tokenUsed)
	var reservationCount int64
	require.NoError(t, DB.Model(&BillingReservation{}).Where("request_id = ?", "billing-rollback").Count(&reservationCount).Error)
	assert.Zero(t, reservationCount)
}

func TestBillingReservationRefundIsSynchronousAndIdempotent(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910004, 920004, 100, "billing-refund")

	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-refund",
		UserId:        910004,
		TokenId:       920004,
		FundingSource: BillingFundingWallet,
		Quota:         80,
	}, "billing-refund")
	require.NoError(t, err)
	_, err = RefundBillingReservation("billing-refund", "billing-refund")
	require.NoError(t, err)
	_, err = RefundBillingReservation("billing-refund", "billing-refund")
	require.NoError(t, err)

	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910004, 920004)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)

	reservation, err := GetBillingReservation("billing-refund")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusRefunded, reservation.Status)
	var refundEntries int64
	require.NoError(t, DB.Model(&QuotaLedgerEntry{}).
		Where("request_id = ? AND phase = ?", "billing-refund", QuotaLedgerPhaseRefund).
		Count(&refundEntries).Error)
	assert.EqualValues(t, 1, refundEntries)
}

func TestBillingReservationDerivesUnlimitedTokenModeFromDatabase(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910008, 920008, 100, "billing-unlimited-token")
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", 920008).Updates(map[string]interface{}{
		"unlimited_quota": true,
		"remain_quota":    0,
	}).Error)

	result, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-unlimited-token",
		UserId:        910008,
		TokenId:       920008,
		FundingSource: BillingFundingWallet,
		Quota:         50,
	}, "billing-unlimited-token")
	require.NoError(t, err)
	assert.Equal(t, billingTokenModeUnlimited, result.Reservation.TokenMode)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910008, 920008)
	assert.Equal(t, 50, userQuota)
	assert.Zero(t, tokenRemain)
	assert.Equal(t, 50, tokenUsed)

	_, err = SettleBillingReservation("billing-unlimited-token", 30, "billing-unlimited-token")
	require.NoError(t, err)
	userQuota, tokenRemain, tokenUsed = loadBillingBalances(t, 910008, 920008)
	assert.Equal(t, 70, userQuota)
	assert.Zero(t, tokenRemain)
	assert.Equal(t, 30, tokenUsed)
}

func TestBillingReservationRefundRestoresNegativeLegacyTokenBaseline(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910009, 920009, 100, "billing-negative-used-baseline")
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", 920009).Update("used_quota", -30).Error)

	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-negative-used-baseline",
		UserId:        910009,
		TokenId:       920009,
		FundingSource: BillingFundingWallet,
		Quota:         20,
	}, "billing-negative-used-baseline")
	require.NoError(t, err)
	_, err = RefundBillingReservation("billing-negative-used-baseline", "billing-negative-used-baseline")
	require.NoError(t, err)

	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910009, 920009)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Equal(t, -30, tokenUsed)
}

func TestBillingReservationConcurrentDebitsCannotOverdraw(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910005, 920005, 100, "billing-concurrent")

	results := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for _, requestId := range []string{"billing-concurrent-a", "billing-concurrent-b"} {
		requestId := requestId
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := PreConsumeBillingReservation(BillingReservationInput{
				RequestId:     requestId,
				UserId:        910005,
				TokenId:       920005,
				FundingSource: BillingFundingWallet,
				Quota:         80,
			}, "billing-concurrent")
			results <- err
		}()
	}
	waitGroup.Wait()
	close(results)

	successes := 0
	insufficient := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrInsufficientUserQuota), errors.Is(err, ErrInsufficientTokenQuota):
			insufficient++
		default:
			require.NoError(t, err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, insufficient)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910005, 920005)
	assert.Equal(t, 20, userQuota)
	assert.Equal(t, 20, tokenRemain)
	assert.Equal(t, 80, tokenUsed)
}

func TestBillingQuotaMutationsBypassProcessBatch(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910006, 920006, 100, "billing-no-batch")
	previousBatchSetting := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() { common.BatchUpdateEnabled = previousBatchSetting })

	require.NoError(t, DecreaseUserQuota(910006, 25, false))
	require.NoError(t, DecreaseTokenQuota(920006, "billing-no-batch", 25))
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910006, 920006)
	assert.Equal(t, 75, userQuota)
	assert.Equal(t, 75, tokenRemain)
	assert.Equal(t, 25, tokenUsed)

	require.NoError(t, IncreaseUserQuota(910006, 25, false))
	require.NoError(t, IncreaseTokenQuota(920006, "billing-no-batch", 25))
	userQuota, tokenRemain, tokenUsed = loadBillingBalances(t, 910006, 920006)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)
}

func TestDirectQuotaDebitsRejectOverdraftWithoutPartialMutation(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910010, 920010, 100, "billing-direct-overdraft")

	err := DecreaseUserQuota(910010, 101, false)
	assert.ErrorIs(t, err, ErrInsufficientUserQuota)
	err = DecreaseTokenQuota(920010, "billing-direct-overdraft", 101)
	assert.ErrorIs(t, err, ErrInsufficientTokenQuota)

	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910010, 920010)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)
}

func TestDirectUnlimitedTokenAdjustmentsLeaveRemainQuotaUntouched(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910011, 920011, 100, "billing-direct-unlimited")
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", 920011).Updates(map[string]interface{}{
		"unlimited_quota": true,
		"remain_quota":    0,
	}).Error)

	require.NoError(t, DecreaseTokenQuota(920011, "billing-direct-unlimited", 25))
	require.NoError(t, IncreaseTokenQuota(920011, "billing-direct-unlimited", 25))
	_, tokenRemain, tokenUsed := loadBillingBalances(t, 910011, 920011)
	assert.Zero(t, tokenRemain)
	assert.Zero(t, tokenUsed)
}

func TestSubscriptionBillingReservationUsesOneAtomicLifecycle(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910007, 920007, 100, "billing-subscription")
	subscription := seedBillingSubscription(t, 910007, 930007, 940007, 100, 10)

	result, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-subscription",
		UserId:        910007,
		TokenId:       920007,
		FundingSource: BillingFundingSubscription,
		Quota:         30,
	}, "billing-subscription")
	require.NoError(t, err)
	assert.Equal(t, subscription.Id, result.Reservation.SubscriptionId)
	assert.EqualValues(t, 40, result.SubscriptionAmountUsedAfter)

	_, err = ExtendBillingReservation("billing-subscription", 50, "billing-subscription")
	require.NoError(t, err)
	_, err = RefundBillingReservation("billing-subscription", "billing-subscription")
	require.NoError(t, err)

	require.NoError(t, DB.Where("id = ?", subscription.Id).First(subscription).Error)
	assert.EqualValues(t, 10, subscription.AmountUsed)
	assert.EqualValues(t, 10, subscription.AmountUsedTotal)
	_, tokenRemain, tokenUsed := loadBillingBalances(t, 910007, 920007)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)
	var record SubscriptionPreConsumeRecord
	require.NoError(t, DB.Where("request_id = ?", "billing-subscription").First(&record).Error)
	assert.Equal(t, "refunded", record.Status)
	assert.EqualValues(t, 50, record.PreConsumed)
}

func TestSubscriptionBillingReservationRollsBackWhenTokenDebitFails(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910012, 920012, 100, "billing-subscription-rollback")
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", 920012).Update("remain_quota", 20).Error)
	subscription := seedBillingSubscription(t, 910012, 930012, 940012, 100, 10)

	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-subscription-rollback",
		UserId:        910012,
		TokenId:       920012,
		FundingSource: BillingFundingSubscription,
		Quota:         30,
	}, "billing-subscription-rollback")
	assert.ErrorIs(t, err, ErrInsufficientTokenQuota)

	require.NoError(t, DB.Where("id = ?", subscription.Id).First(subscription).Error)
	assert.EqualValues(t, 10, subscription.AmountUsed)
	var recordCount int64
	require.NoError(t, DB.Model(&SubscriptionPreConsumeRecord{}).
		Where("request_id = ?", "billing-subscription-rollback").
		Count(&recordCount).Error)
	assert.Zero(t, recordCount)
	var reservationCount int64
	require.NoError(t, DB.Model(&BillingReservation{}).
		Where("request_id = ?", "billing-subscription-rollback").
		Count(&reservationCount).Error)
	assert.Zero(t, reservationCount)
}

func TestAsyncTaskBillingReservationSettlesAtomicallyAndIdempotently(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910020, 920020, 200, "billing-async-settle")

	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-async-settle",
		UserId:        910020,
		TokenId:       920020,
		FundingSource: BillingFundingWallet,
		Quota:         60,
	}, "billing-async-settle")
	require.NoError(t, err)
	task := &Task{
		TaskID:   "task_billing_async_settle",
		UserId:   910020,
		Quota:    80,
		Status:   TaskStatusInProgress,
		Progress: "30%",
	}
	require.NoError(t, CreateTaskWithBillingReservation(task, "billing-async-settle", 80, "billing-async-settle"))
	pendingReservation, err := GetBillingReservation("billing-async-settle")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusReserved, pendingReservation.Status)
	assert.Zero(t, pendingReservation.SettledQuota)
	assert.Equal(t, BillingResourceAsyncTask, pendingReservation.ResourceType)
	assert.Equal(t, task.TaskID, pendingReservation.ResourceId)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910020, 920020)
	assert.Equal(t, 120, userQuota)
	assert.Equal(t, 120, tokenRemain)
	assert.Equal(t, 80, tokenUsed)

	_, err = SettleTaskBillingReservation(task.ID, "billing-async-settle", 50, "billing-async-settle")
	assert.ErrorIs(t, err, ErrBillingReservationConflict)
	pendingReservation, err = GetBillingReservation("billing-async-settle")
	require.NoError(t, err)
	assert.False(t, pendingReservation.SettlementPending)

	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("status", TaskStatusSuccess).Error)
	result, err := SettleTaskBillingReservation(task.ID, "billing-async-settle", 50, "billing-async-settle")
	require.NoError(t, err)
	assert.True(t, result.Applied)
	assert.Equal(t, 80, result.PreviousReservedQuota)
	userQuota, tokenRemain, tokenUsed = loadBillingBalances(t, 910020, 920020)
	assert.Equal(t, 150, userQuota)
	assert.Equal(t, 150, tokenRemain)
	assert.Equal(t, 50, tokenUsed)

	var storedTask Task
	require.NoError(t, DB.First(&storedTask, task.ID).Error)
	assert.Equal(t, 50, storedTask.Quota)
	reservation, err := GetBillingReservation("billing-async-settle")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusSettled, reservation.Status)
	assert.Equal(t, 50, reservation.SettledQuota)
	assert.False(t, reservation.SettlementPending)

	repeated, err := SettleTaskBillingReservation(task.ID, "billing-async-settle", 50, "billing-async-settle")
	require.NoError(t, err)
	assert.False(t, repeated.Applied)
	userQuota, tokenRemain, tokenUsed = loadBillingBalances(t, 910020, 920020)
	assert.Equal(t, 150, userQuota)
	assert.Equal(t, 150, tokenRemain)
	assert.Equal(t, 50, tokenUsed)

	var settleEntries int64
	require.NoError(t, DB.Model(&QuotaLedgerEntry{}).
		Where("request_id = ? AND phase = ?", "billing-async-settle", QuotaLedgerPhaseSettle).
		Count(&settleEntries).Error)
	assert.EqualValues(t, 1, settleEntries)
}

func TestAsyncTaskSettlementRecoversPersistedZeroIntentAfterIntentLedgerFailure(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910033, 920033, 100, "billing-task-intent-restart")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-task-intent-restart",
		UserId:        910033,
		TokenId:       920033,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "billing-task-intent-restart")
	require.NoError(t, err)
	task := &Task{
		TaskID:   "task_billing_intent_restart",
		UserId:   910033,
		Status:   TaskStatusInProgress,
		Progress: "50%",
	}
	require.NoError(t, CreateTaskWithBillingReservation(task, "billing-task-intent-restart", 40, "billing-task-intent-restart"))
	t.Cleanup(func() { DB.Delete(&Task{}, task.ID) })

	require.NoError(t, task.PrivateData.RecordBillingTargetQuota(0))
	require.NoError(t, task.PrivateData.RecordBillingTargetQuota(0))
	assert.ErrorIs(t, task.PrivateData.RecordBillingTargetQuota(1), ErrBillingReservationConflict)
	task.Status = TaskStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus(TaskStatusInProgress)
	require.NoError(t, err)
	require.True(t, won)

	// A racing caller cannot replace the target persisted by the terminal CAS.
	_, err = SettleTaskBillingReservation(task.ID, "billing-task-intent-restart", 1, "billing-task-intent-restart")
	assert.ErrorIs(t, err, ErrBillingReservationConflict)

	// Simulate an older reservation-only intent, then force the next intent
	// ledger insert to fail. The transaction must roll back while the task's
	// explicit zero target remains durable for restart reconciliation.
	require.NoError(t, PrepareBillingReservationSettlement("billing-task-intent-restart", 30))
	reservation, err := GetBillingReservation("billing-task-intent-restart")
	require.NoError(t, err)
	blockingRevision := reservation.Version + 1
	require.NoError(t, DB.Create(&QuotaLedgerEntry{
		RequestId:     reservation.RequestId,
		Phase:         QuotaLedgerPhaseIntent,
		Revision:      blockingRevision,
		UserId:        reservation.UserId,
		TokenId:       reservation.TokenId,
		FundingSource: reservation.FundingSource,
		Note:          "test-only conflicting intent ledger",
	}).Error)

	_, err = SettleTaskBillingReservation(task.ID, reservation.RequestId, 0, "billing-task-intent-restart")
	require.Error(t, err)
	reservation, err = GetBillingReservation(reservation.RequestId)
	require.NoError(t, err)
	assert.True(t, reservation.SettlementPending)
	assert.Equal(t, 30, reservation.SettlementTarget)
	var storedTask Task
	require.NoError(t, DB.First(&storedTask, task.ID).Error)
	targetQuota, hasIntent := storedTask.PrivateData.BillingTargetQuotaIntent()
	assert.True(t, hasIntent)
	assert.Zero(t, targetQuota)

	require.NoError(t, DB.Exec(
		"DELETE FROM quota_ledger_entries WHERE request_id = ? AND phase = ? AND revision = ?",
		reservation.RequestId, QuotaLedgerPhaseIntent, blockingRevision,
	).Error)
	old := common.GetTimestamp() - 600
	require.NoError(t, DB.Model(&BillingReservation{}).Where("id = ?", reservation.Id).Updates(map[string]interface{}{
		"updated_at":         old,
		"last_reconciled_at": 0,
	}).Error)

	summary := ReconcileStaleBillingReservations(common.GetTimestamp()-60, 10)
	assert.Equal(t, 1, summary.Settled)
	reservation, err = GetBillingReservation(reservation.RequestId)
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusSettled, reservation.Status)
	assert.Zero(t, reservation.SettledQuota)
	require.NoError(t, DB.First(&storedTask, task.ID).Error)
	assert.Zero(t, storedTask.Quota)
	_, hasIntent = storedTask.PrivateData.BillingTargetQuotaIntent()
	assert.False(t, hasIntent)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910033, 920033)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)

	repeated, err := SettleTaskBillingReservation(task.ID, reservation.RequestId, 0, "billing-task-intent-restart")
	require.NoError(t, err)
	assert.False(t, repeated.Applied)
}

func TestAsyncTaskBillingReservationRefundsAtomicallyAndIdempotently(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910021, 920021, 200, "billing-async-refund")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-async-refund",
		UserId:        910021,
		TokenId:       920021,
		FundingSource: BillingFundingWallet,
		Quota:         60,
	}, "billing-async-refund")
	require.NoError(t, err)
	task := &Task{TaskID: "task_billing_async_refund", UserId: 910021, Status: TaskStatusInProgress}
	require.NoError(t, CreateTaskWithBillingReservation(task, "billing-async-refund", 60, "billing-async-refund"))
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("status", TaskStatusFailure).Error)

	result, err := RefundTaskBillingReservation(task.ID, "billing-async-refund", "billing-async-refund")
	require.NoError(t, err)
	assert.True(t, result.Applied)
	repeated, err := RefundTaskBillingReservation(task.ID, "billing-async-refund", "billing-async-refund")
	require.NoError(t, err)
	assert.False(t, repeated.Applied)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910021, 920021)
	assert.Equal(t, 200, userQuota)
	assert.Equal(t, 200, tokenRemain)
	assert.Zero(t, tokenUsed)

	var refundEntries int64
	require.NoError(t, DB.Model(&QuotaLedgerEntry{}).
		Where("request_id = ? AND phase = ?", "billing-async-refund", QuotaLedgerPhaseRefund).
		Count(&refundEntries).Error)
	assert.EqualValues(t, 1, refundEntries)
}

func TestAsyncTaskSettlementIntentSurvivesInsufficientBalanceAndReconciles(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910022, 920022, 100, "billing-async-retry")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-async-retry",
		UserId:        910022,
		TokenId:       920022,
		FundingSource: BillingFundingWallet,
		Quota:         60,
	}, "billing-async-retry")
	require.NoError(t, err)
	task := &Task{TaskID: "task_billing_async_retry", UserId: 910022, Status: TaskStatusInProgress}
	require.NoError(t, CreateTaskWithBillingReservation(task, "billing-async-retry", 60, "billing-async-retry"))
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("status", TaskStatusSuccess).Error)
	// Simulate the user spending most of the unreserved wallet quota while the
	// async task is running. The extra settlement cannot overdraw the wallet.
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 910022).Update("quota", 10).Error)

	_, err = SettleTaskBillingReservation(task.ID, "billing-async-retry", 80, "billing-async-retry")
	assert.ErrorIs(t, err, ErrInsufficientUserQuota)
	reservation, err := GetBillingReservation("billing-async-retry")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusReserved, reservation.Status)
	assert.True(t, reservation.SettlementPending)
	assert.Equal(t, 80, reservation.SettlementTarget)
	assert.Equal(t, 60, reservation.ReservedQuota)
	_, tokenRemain, tokenUsed := loadBillingBalances(t, 910022, 920022)
	assert.Equal(t, 40, tokenRemain)
	assert.Equal(t, 60, tokenUsed)

	require.NoError(t, DB.Model(&User{}).Where("id = ?", 910022).Update("quota", 30).Error)
	old := common.GetTimestamp() - 600
	require.NoError(t, DB.Model(&BillingReservation{}).Where("request_id = ?", "billing-async-retry").Updates(map[string]interface{}{
		"updated_at":         old,
		"last_reconciled_at": 0,
	}).Error)
	summary := ReconcileStaleBillingReservations(common.GetTimestamp()-60, 10)
	assert.Equal(t, 1, summary.Settled)
	reservation, err = GetBillingReservation("billing-async-retry")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusSettled, reservation.Status)
	assert.Equal(t, 80, reservation.SettledQuota)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910022, 920022)
	assert.Equal(t, 10, userQuota)
	assert.Equal(t, 20, tokenRemain)
	assert.Equal(t, 80, tokenUsed)
}

func TestStaleUnboundBillingReservationIsReviewedWithoutBalanceMutation(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910023, 920023, 100, "billing-stale-review")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-stale-review",
		UserId:        910023,
		TokenId:       920023,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "billing-stale-review")
	require.NoError(t, err)
	old := common.GetTimestamp() - 600
	require.NoError(t, DB.Model(&BillingReservation{}).Where("request_id = ?", "billing-stale-review").Update("updated_at", old).Error)

	summary := ReconcileStaleBillingReservations(common.GetTimestamp()-60, 10)
	assert.Equal(t, 1, summary.ReviewRequired)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910023, 920023)
	assert.Equal(t, 60, userQuota)
	assert.Equal(t, 60, tokenRemain)
	assert.Equal(t, 40, tokenUsed)
	reservation, err := GetBillingReservation("billing-stale-review")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusReserved, reservation.Status)
	assert.NotZero(t, reservation.LastReconciledAt)
	var reviewEntries int64
	require.NoError(t, DB.Model(&QuotaLedgerEntry{}).
		Where("request_id = ? AND phase = ?", "billing-stale-review", QuotaLedgerPhaseReview).
		Count(&reviewEntries).Error)
	assert.EqualValues(t, 1, reviewEntries)
}

func TestStaleTerminalFailureReservationIsRefunded(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910028, 920028, 100, "billing-stale-failure")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-stale-failure",
		UserId:        910028,
		TokenId:       920028,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "billing-stale-failure")
	require.NoError(t, err)
	task := &Task{TaskID: "task_billing_stale_failure", UserId: 910028, Status: TaskStatusInProgress}
	require.NoError(t, CreateTaskWithBillingReservation(task, "billing-stale-failure", 40, "billing-stale-failure"))
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("status", TaskStatusFailure).Error)
	old := common.GetTimestamp() - 600
	require.NoError(t, DB.Model(&BillingReservation{}).Where("request_id = ?", "billing-stale-failure").Update("updated_at", old).Error)

	summary := ReconcileStaleBillingReservations(common.GetTimestamp()-60, 10)
	assert.Equal(t, 1, summary.Refunded)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910028, 920028)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)
	reservation, err := GetBillingReservation("billing-stale-failure")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusRefunded, reservation.Status)
}

func TestStaleNonTerminalTaskRemainsReserved(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910029, 920029, 100, "billing-stale-pending")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-stale-pending",
		UserId:        910029,
		TokenId:       920029,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "billing-stale-pending")
	require.NoError(t, err)
	task := &Task{TaskID: "task_billing_stale_pending", UserId: 910029, Status: TaskStatusInProgress}
	require.NoError(t, CreateTaskWithBillingReservation(task, "billing-stale-pending", 40, "billing-stale-pending"))
	old := common.GetTimestamp() - 600
	require.NoError(t, DB.Model(&BillingReservation{}).Where("request_id = ?", "billing-stale-pending").Update("updated_at", old).Error)

	summary := ReconcileStaleBillingReservations(common.GetTimestamp()-60, 10)
	assert.Equal(t, 1, summary.Pending)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910029, 920029)
	assert.Equal(t, 60, userQuota)
	assert.Equal(t, 60, tokenRemain)
	assert.Equal(t, 40, tokenUsed)
	reservation, err := GetBillingReservation("billing-stale-pending")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusReserved, reservation.Status)
}

func TestSubscriptionReservationRefundAcrossResetPreservesNewPeriodUsage(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910024, 920024, 100, "billing-sub-reset-refund")
	subscription := seedBillingSubscription(t, 910024, 930024, 940024, 100, 10)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{
		"last_reset_time":          100,
		"quota_reset_version":      3,
		"amount_used_total":        10,
		"usage_accounting_version": 1,
	}).Error)
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-sub-reset-refund",
		UserId:        910024,
		TokenId:       920024,
		FundingSource: BillingFundingSubscription,
		Quota:         30,
	}, "billing-sub-reset-refund")
	require.NoError(t, err)
	// A reset occurs, followed by 7 units of new-period usage.
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{
		"last_reset_time":     200,
		"quota_reset_version": 4,
		"amount_used":         7,
		"amount_used_total":   47,
	}).Error)
	_, err = RefundBillingReservation("billing-sub-reset-refund", "billing-sub-reset-refund")
	require.NoError(t, err)
	require.NoError(t, DB.First(subscription, subscription.Id).Error)
	assert.EqualValues(t, 7, subscription.AmountUsed)
	assert.EqualValues(t, 17, subscription.AmountUsedTotal)
}

func TestSubscriptionReservationSettlementAcrossResetDoesNotChargeNewPeriod(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910025, 920025, 100, "billing-sub-reset-settle")
	subscription := seedBillingSubscription(t, 910025, 930025, 940025, 100, 10)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{
		"last_reset_time":          100,
		"quota_reset_version":      5,
		"amount_used_total":        10,
		"usage_accounting_version": 1,
	}).Error)
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-sub-reset-settle",
		UserId:        910025,
		TokenId:       920025,
		FundingSource: BillingFundingSubscription,
		Quota:         30,
	}, "billing-sub-reset-settle")
	require.NoError(t, err)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{
		"last_reset_time":     200,
		"quota_reset_version": 6,
		"amount_used":         5,
		"amount_used_total":   45,
	}).Error)
	_, err = SettleBillingReservation("billing-sub-reset-settle", 40, "billing-sub-reset-settle")
	require.NoError(t, err)
	require.NoError(t, DB.First(subscription, subscription.Id).Error)
	assert.EqualValues(t, 5, subscription.AmountUsed)
	assert.EqualValues(t, 55, subscription.AmountUsedTotal)
}

func TestSubscriptionReservationSettlementAfterManualResetKeepsNewPeriodUsage(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910030, 920030, 100, "billing-sub-manual-reset")
	subscription := seedBillingSubscription(t, 910030, 930030, 940030, 100, 10)
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{
		"last_reset_time":          100,
		"quota_reset_version":      9,
		"amount_used_total":        10,
		"usage_accounting_version": 1,
	}).Error)
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-sub-manual-reset",
		UserId:        910030,
		TokenId:       920030,
		FundingSource: BillingFundingSubscription,
		Quota:         20,
	}, "billing-sub-manual-reset")
	require.NoError(t, err)

	plan, err := getSubscriptionPlanByIdTx(DB, subscription.PlanId)
	require.NoError(t, err)
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var locked UserSubscription
		if err := lockForUpdate(tx).Where("id = ?", subscription.Id).First(&locked).Error; err != nil {
			return err
		}
		return resetUserSubscriptionTx(tx, &locked, plan, 500, false)
	}))
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{
		"amount_used":       50,
		"amount_used_total": 80,
	}).Error)

	_, err = SettleBillingReservation("billing-sub-manual-reset", 10, "billing-sub-manual-reset")
	require.NoError(t, err)
	require.NoError(t, DB.First(subscription, subscription.Id).Error)
	assert.EqualValues(t, 50, subscription.AmountUsed)
	assert.EqualValues(t, 70, subscription.AmountUsedTotal)
	assert.EqualValues(t, 10, subscription.QuotaResetVersion)
	assert.EqualValues(t, 100, subscription.LastResetTime)
}

func TestAdminDeleteSubscriptionRejectsUnfinishedBillingReservation(t *testing.T) {
	prepareBillingReservationTest(t)
	subscription := seedBillingSubscription(t, 910031, 930031, 940031, 100, 0)
	require.NoError(t, DB.Create(&BillingReservation{
		RequestId:      "billing-sub-delete-guard",
		UserId:         subscription.UserId,
		FundingSource:  BillingFundingSubscription,
		SubscriptionId: subscription.Id,
		InitialQuota:   20,
		ReservedQuota:  20,
		Status:         BillingReservationStatusReserved,
		Version:        1,
	}).Error)

	_, err := AdminDeleteUserSubscription(subscription.Id)
	assert.ErrorIs(t, err, ErrSubscriptionBillingInProgress)
	require.NoError(t, DB.First(&UserSubscription{}, subscription.Id).Error)

	require.NoError(t, DB.Model(&BillingReservation{}).
		Where("request_id = ?", "billing-sub-delete-guard").
		Update("status", BillingReservationStatusSettled).Error)
	_, err = AdminDeleteUserSubscription(subscription.Id)
	require.NoError(t, err)
	assert.ErrorIs(t, DB.First(&UserSubscription{}, subscription.Id).Error, gorm.ErrRecordNotFound)
}

func TestUserDeletionRetainsBillingAuditSubject(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910032, 920032, 100, "billing-user-delete-guard")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-user-delete-guard",
		UserId:        910032,
		TokenId:       920032,
		FundingSource: BillingFundingWallet,
		Quota:         20,
	}, "billing-user-delete-guard")
	require.NoError(t, err)

	err = DeleteUserById(910032)
	assert.ErrorIs(t, err, ErrUserFinancialHistoryRequiresRetention)
	err = HardDeleteUserById(910032)
	assert.ErrorIs(t, err, ErrUserFinancialHistoryRequiresRetention)
	require.NoError(t, DB.First(&User{}, 910032).Error)
}

func TestConcurrentAsyncTaskSettlementAppliesOnce(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910026, 920026, 200, "billing-async-concurrent")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-async-concurrent",
		UserId:        910026,
		TokenId:       920026,
		FundingSource: BillingFundingWallet,
		Quota:         60,
	}, "billing-async-concurrent")
	require.NoError(t, err)
	task := &Task{TaskID: "task_billing_async_concurrent", UserId: 910026, Status: TaskStatusInProgress}
	require.NoError(t, CreateTaskWithBillingReservation(task, "billing-async-concurrent", 60, "billing-async-concurrent"))
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("status", TaskStatusSuccess).Error)

	results := make(chan *BillingFinalizationResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, settleErr := SettleTaskBillingReservation(task.ID, "billing-async-concurrent", 80, "billing-async-concurrent")
			results <- result
			errs <- settleErr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for settleErr := range errs {
		require.NoError(t, settleErr)
	}
	applied := 0
	for result := range results {
		require.NotNil(t, result)
		if result.Applied {
			applied++
		}
	}
	assert.Equal(t, 1, applied)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910026, 920026)
	assert.Equal(t, 120, userQuota)
	assert.Equal(t, 120, tokenRemain)
	assert.Equal(t, 80, tokenUsed)
}

func TestConcurrentLegacyTaskSettlementRejectsDifferentTargets(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910034, 920034, 200, "billing-async-target-conflict")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-async-target-conflict",
		UserId:        910034,
		TokenId:       920034,
		FundingSource: BillingFundingWallet,
		Quota:         60,
	}, "billing-async-target-conflict")
	require.NoError(t, err)
	task := &Task{TaskID: "task_billing_target_conflict", UserId: 910034, Status: TaskStatusInProgress}
	require.NoError(t, CreateTaskWithBillingReservation(task, "billing-async-target-conflict", 60, "billing-async-target-conflict"))
	t.Cleanup(func() { DB.Delete(&Task{}, task.ID) })
	// Simulate a legacy terminal row created before task-scoped intents existed.
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("status", TaskStatusSuccess).Error)

	type settlementOutcome struct {
		target int
		result *BillingFinalizationResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan settlementOutcome, 2)
	var wg sync.WaitGroup
	for _, target := range []int{70, 80} {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, settleErr := SettleTaskBillingReservation(task.ID, "billing-async-target-conflict", target, "billing-async-target-conflict")
			outcomes <- settlementOutcome{target: target, result: result, err: settleErr}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	winner := 0
	conflicts := 0
	for outcome := range outcomes {
		if outcome.err != nil {
			assert.ErrorIs(t, outcome.err, ErrBillingReservationConflict)
			conflicts++
			continue
		}
		require.NotNil(t, outcome.result)
		require.True(t, outcome.result.Applied)
		winner = outcome.target
	}
	assert.Contains(t, []int{70, 80}, winner)
	assert.Equal(t, 1, conflicts)

	reservation, err := GetBillingReservation("billing-async-target-conflict")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusSettled, reservation.Status)
	assert.Equal(t, winner, reservation.SettledQuota)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910034, 920034)
	assert.Equal(t, 200-winner, userQuota)
	assert.Equal(t, 200-winner, tokenRemain)
	assert.Equal(t, winner, tokenUsed)
}

func TestAsyncTaskRefundCompletesWhenTokenWasDeleted(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910027, 920027, 100, "billing-async-deleted-token")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-async-deleted-token",
		UserId:        910027,
		TokenId:       920027,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "billing-async-deleted-token")
	require.NoError(t, err)
	task := &Task{TaskID: "task_billing_async_deleted_token", UserId: 910027, Status: TaskStatusInProgress}
	require.NoError(t, CreateTaskWithBillingReservation(task, "billing-async-deleted-token", 40, "billing-async-deleted-token"))
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("status", TaskStatusFailure).Error)
	require.NoError(t, DB.Unscoped().Delete(&Token{}, 920027).Error)

	result, err := RefundTaskBillingReservation(task.ID, "billing-async-deleted-token", "")
	require.NoError(t, err)
	assert.True(t, result.Applied)
	var user User
	require.NoError(t, DB.Select("quota").Where("id = ?", 910027).First(&user).Error)
	assert.Equal(t, 100, user.Quota)
	reservation, err := GetBillingReservation("billing-async-deleted-token")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusRefunded, reservation.Status)
	var ledger QuotaLedgerEntry
	require.NoError(t, DB.Where("request_id = ? AND phase = ?", "billing-async-deleted-token", QuotaLedgerPhaseRefund).First(&ledger).Error)
	assert.Contains(t, ledger.Note, "token deleted")
}

func TestLegacyTaskSettlementRollsBackFundingWhenTokenDeltaFails(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910030, 920030, 100, "billing-legacy-rollback")
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", 920030).Updates(map[string]interface{}{
		"remain_quota": 5,
		"used_quota":   10,
	}).Error)
	task := &Task{
		TaskID: "task_billing_legacy_rollback",
		UserId: 910030,
		Quota:  10,
		Status: TaskStatusSuccess,
		PrivateData: TaskPrivateData{
			BillingSource: BillingFundingWallet,
			TokenId:       920030,
		},
	}
	require.NoError(t, DB.Create(task).Error)

	_, err := SettleLegacyTaskBilling(task.ID, 20)
	assert.ErrorIs(t, err, ErrInsufficientTokenQuota)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910030, 920030)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 5, tokenRemain)
	assert.Equal(t, 10, tokenUsed)
	var stored Task
	require.NoError(t, DB.First(&stored, task.ID).Error)
	assert.Equal(t, 10, stored.Quota)
	assert.False(t, stored.PrivateData.LegacyBillingFinalized)
	var ledgerCount int64
	require.NoError(t, DB.Model(&QuotaLedgerEntry{}).Where("request_id = ?", legacyTaskBillingRequestId(task.ID)).Count(&ledgerCount).Error)
	assert.Zero(t, ledgerCount)
}

func TestLegacyPendingTaskAdoptionDoesNotDoubleChargeAndCanRefund(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910031, 920031, 100, "billing-legacy-adopt")
	// The legacy submit path already charged 40 before the durable baseline is
	// adopted. Adoption itself must be zero-mutation.
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 910031).Update("quota", 60).Error)
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", 920031).Updates(map[string]interface{}{
		"remain_quota": 60,
		"used_quota":   40,
	}).Error)
	task := &Task{
		TaskID: "task_billing_legacy_adopt",
		UserId: 910031,
		Quota:  40,
		Status: TaskStatusInProgress,
		PrivateData: TaskPrivateData{
			BillingSource: BillingFundingWallet,
			TokenId:       920031,
		},
	}
	require.NoError(t, DB.Create(task).Error)

	reservation, err := AdoptLegacyTaskBillingReservation(task.ID)
	require.NoError(t, err)
	assert.True(t, reservation.LegacyAdopted)
	assert.Equal(t, BillingReservationStatusReserved, reservation.Status)
	assert.Equal(t, 40, reservation.ReservedQuota)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910031, 920031)
	assert.Equal(t, 60, userQuota)
	assert.Equal(t, 60, tokenRemain)
	assert.Equal(t, 40, tokenUsed)
	var stored Task
	require.NoError(t, DB.First(&stored, task.ID).Error)
	assert.Equal(t, reservation.RequestId, stored.PrivateData.BillingRequestId)

	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("status", TaskStatusFailure).Error)
	result, err := RefundTaskBillingReservation(task.ID, reservation.RequestId, "billing-legacy-adopt")
	require.NoError(t, err)
	assert.True(t, result.Applied)
	userQuota, tokenRemain, tokenUsed = loadBillingBalances(t, 910031, 920031)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)
}

func TestLegacyPendingTaskAdoptionIsRecoveredByStaleReconciler(t *testing.T) {
	prepareBillingReservationTest(t)
	seedBillingWallet(t, 910032, 920032, 100, "billing-legacy-stale")
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 910032).Update("quota", 60).Error)
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", 920032).Updates(map[string]interface{}{
		"remain_quota": 60,
		"used_quota":   40,
	}).Error)
	task := &Task{
		TaskID: "task_billing_legacy_stale",
		UserId: 910032,
		Quota:  40,
		Status: TaskStatusInProgress,
		PrivateData: TaskPrivateData{
			BillingSource: BillingFundingWallet,
			TokenId:       920032,
		},
	}
	require.NoError(t, DB.Create(task).Error)
	reservation, err := AdoptLegacyTaskBillingReservation(task.ID)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("status", TaskStatusSuccess).Error)
	old := common.GetTimestamp() - 600
	require.NoError(t, DB.Model(&BillingReservation{}).Where("id = ?", reservation.Id).Update("updated_at", old).Error)

	summary := ReconcileStaleBillingReservations(common.GetTimestamp()-60, 10)
	assert.Equal(t, 1, summary.Settled)
	reservation, err = GetBillingReservation(reservation.RequestId)
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusSettled, reservation.Status)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 910032, 920032)
	assert.Equal(t, 60, userQuota)
	assert.Equal(t, 60, tokenRemain)
	assert.Equal(t, 40, tokenUsed)
}
