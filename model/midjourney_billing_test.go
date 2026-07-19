package model

import (
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prepareMidjourneyBillingTest(t *testing.T) {
	t.Helper()
	prepareBillingReservationTest(t)
	require.NoError(t, DB.AutoMigrate(&Midjourney{}))
	require.NoError(t, DB.Exec("DELETE FROM midjourneys").Error)
	t.Cleanup(func() {
		DB.Exec("DELETE FROM midjourneys")
	})
}

func bindMidjourneyWalletReservation(t *testing.T, requestId string, userId, tokenId, quota int) *Midjourney {
	t.Helper()
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     requestId,
		UserId:        userId,
		TokenId:       tokenId,
		FundingSource: BillingFundingWallet,
		Quota:         quota,
	}, requestId)
	require.NoError(t, err)
	claimed, err := ClaimMidjourneyBillingReservation(requestId)
	require.NoError(t, err)
	require.True(t, claimed)
	task := &Midjourney{
		UserId:     userId,
		MjId:       "mj-" + requestId,
		Action:     "IMAGINE",
		SubmitTime: time.Now().UnixMilli(),
		ChannelId:  1,
		Quota:      quota,
		Group:      "default",
		Progress:   "0%",
		PrivateData: TaskPrivateData{
			BillingSource:    BillingFundingWallet,
			TokenId:          tokenId,
			BillingRequestId: requestId,
		},
	}
	require.NoError(t, CreateMidjourneyWithBillingReservation(task, requestId, quota, requestId))
	return task
}

func TestMidjourneyFailureRefundsWalletAndTokenExactlyOnce(t *testing.T) {
	prepareMidjourneyBillingTest(t)
	seedBillingWallet(t, 971001, 972001, 100, "mj-refund-once")
	task := bindMidjourneyWalletReservation(t, "mj-refund-once", 971001, 972001, 40)

	task.Status = MidjourneyStatusFailure
	task.Progress = "100%"
	won, err := task.UpdateWithStatus("")
	require.NoError(t, err)
	require.True(t, won)

	first, err := RefundMidjourneyBillingReservation(task.Id, "mj-refund-once", "mj-refund-once")
	require.NoError(t, err)
	assert.True(t, first.Applied)
	second, err := RefundMidjourneyBillingReservation(task.Id, "mj-refund-once", "mj-refund-once")
	require.NoError(t, err)
	assert.False(t, second.Applied)

	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 971001, 972001)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Equal(t, 0, tokenUsed)

	reservation, err := GetBillingReservation("mj-refund-once")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusRefunded, reservation.Status)
	assert.Equal(t, BillingResourceMidjourneyTask, reservation.ResourceType)
	assert.Equal(t, strconv.Itoa(task.Id), reservation.ResourceId)
	var refundLedgers int64
	require.NoError(t, DB.Model(&QuotaLedgerEntry{}).
		Where("request_id = ? AND phase = ?", "mj-refund-once", QuotaLedgerPhaseRefund).
		Count(&refundLedgers).Error)
	assert.EqualValues(t, 1, refundLedgers)
}

func TestMidjourneySubscriptionSettlementDoesNotPolluteNewResetPeriod(t *testing.T) {
	prepareMidjourneyBillingTest(t)
	seedBillingWallet(t, 971002, 972002, 100, "mj-sub-reset")
	subscription := seedBillingSubscription(t, 971002, 973002, 974002, 100, 0)

	result, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "mj-sub-reset",
		UserId:        971002,
		TokenId:       972002,
		FundingSource: BillingFundingSubscription,
		Quota:         40,
	}, "mj-sub-reset")
	require.NoError(t, err)
	claimed, err := ClaimMidjourneyBillingReservation("mj-sub-reset")
	require.NoError(t, err)
	require.True(t, claimed)
	task := &Midjourney{
		UserId:     971002,
		MjId:       "mj-sub-reset",
		Action:     "IMAGINE",
		SubmitTime: time.Now().UnixMilli(),
		ChannelId:  2,
		Quota:      40,
		Group:      "default",
		Progress:   "0%",
		PrivateData: TaskPrivateData{
			BillingSource:    BillingFundingSubscription,
			SubscriptionId:   result.Reservation.SubscriptionId,
			TokenId:          972002,
			BillingRequestId: "mj-sub-reset",
		},
	}
	require.NoError(t, CreateMidjourneyWithBillingReservation(task, "mj-sub-reset", 40, "mj-sub-reset"))

	newResetAt := common.GetTimestamp() + 60
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", subscription.Id).Updates(map[string]interface{}{
		"amount_used":     0,
		"last_reset_time": newResetAt,
	}).Error)
	task.Status = MidjourneyStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus("")
	require.NoError(t, err)
	require.True(t, won)

	settled, err := SettleMidjourneyBillingReservation(task.Id, "mj-sub-reset", 20, "mj-sub-reset")
	require.NoError(t, err)
	assert.True(t, settled.Applied)
	settledAgain, err := SettleMidjourneyBillingReservation(task.Id, "mj-sub-reset", 20, "mj-sub-reset")
	require.NoError(t, err)
	assert.False(t, settledAgain.Applied)

	var current UserSubscription
	require.NoError(t, DB.Where("id = ?", subscription.Id).First(&current).Error)
	assert.EqualValues(t, 0, current.AmountUsed)
	assert.EqualValues(t, 20, current.AmountUsedTotal)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 971002, 972002)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 80, tokenRemain)
	assert.Equal(t, 20, tokenUsed)
}

func TestMidjourneySettlementRecoversTaskIntentAfterIntentLedgerFailure(t *testing.T) {
	prepareMidjourneyBillingTest(t)
	seedBillingWallet(t, 971008, 972008, 100, "mj-intent-restart")
	task := bindMidjourneyWalletReservation(t, "mj-intent-restart", 971008, 972008, 40)

	require.NoError(t, task.PrivateData.RecordBillingTargetQuota(20))
	require.NoError(t, task.PrivateData.RecordBillingTargetQuota(20))
	assert.ErrorIs(t, task.PrivateData.RecordBillingTargetQuota(21), ErrBillingReservationConflict)
	task.Status = MidjourneyStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus("")
	require.NoError(t, err)
	require.True(t, won)

	_, err = SettleMidjourneyBillingReservation(task.Id, "mj-intent-restart", 21, "mj-intent-restart")
	assert.ErrorIs(t, err, ErrBillingReservationConflict)
	require.NoError(t, PrepareBillingReservationSettlement("mj-intent-restart", 30))
	reservation, err := GetBillingReservation("mj-intent-restart")
	require.NoError(t, err)
	blockingRevision := reservation.Version + 1
	require.NoError(t, DB.Create(&QuotaLedgerEntry{
		RequestId:     reservation.RequestId,
		Phase:         QuotaLedgerPhaseIntent,
		Revision:      blockingRevision,
		UserId:        reservation.UserId,
		TokenId:       reservation.TokenId,
		FundingSource: reservation.FundingSource,
		Note:          "test-only conflicting Midjourney intent ledger",
	}).Error)

	_, err = SettleMidjourneyBillingReservation(task.Id, reservation.RequestId, 20, "mj-intent-restart")
	require.Error(t, err)
	reservation, err = GetBillingReservation(reservation.RequestId)
	require.NoError(t, err)
	assert.True(t, reservation.SettlementPending)
	assert.Equal(t, 30, reservation.SettlementTarget)
	var storedTask Midjourney
	require.NoError(t, DB.First(&storedTask, task.Id).Error)
	targetQuota, hasIntent := storedTask.PrivateData.BillingTargetQuotaIntent()
	assert.True(t, hasIntent)
	assert.Equal(t, 20, targetQuota)

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
	assert.Equal(t, 20, reservation.SettledQuota)
	require.NoError(t, DB.First(&storedTask, task.Id).Error)
	assert.Equal(t, 20, storedTask.Quota)
	_, hasIntent = storedTask.PrivateData.BillingTargetQuotaIntent()
	assert.False(t, hasIntent)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 971008, 972008)
	assert.Equal(t, 80, userQuota)
	assert.Equal(t, 80, tokenRemain)
	assert.Equal(t, 20, tokenUsed)

	repeated, err := SettleMidjourneyBillingReservation(task.Id, reservation.RequestId, 20, "mj-intent-restart")
	require.NoError(t, err)
	assert.False(t, repeated.Applied)
}

func TestMidjourneySubmissionClaimPreventsDuplicateDispatch(t *testing.T) {
	prepareMidjourneyBillingTest(t)
	seedBillingWallet(t, 971006, 972006, 100, "mj-dispatch-claim")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "mj-dispatch-claim",
		UserId:        971006,
		TokenId:       972006,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "mj-dispatch-claim")
	require.NoError(t, err)

	claimed, err := ClaimMidjourneyBillingReservation("mj-dispatch-claim")
	require.NoError(t, err)
	assert.True(t, claimed)
	claimed, err = ClaimMidjourneyBillingReservation("mj-dispatch-claim")
	require.NoError(t, err)
	assert.False(t, claimed)

	reservation, err := GetBillingReservation("mj-dispatch-claim")
	require.NoError(t, err)
	assert.Equal(t, BillingResourceMidjourneySubmission, reservation.ResourceType)
	assert.Equal(t, "mj-dispatch-claim", reservation.ResourceId)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 971006, 972006)
	assert.Equal(t, 60, userQuota)
	assert.Equal(t, 60, tokenRemain)
	assert.Equal(t, 40, tokenUsed)
	var claimLedgers int64
	require.NoError(t, DB.Model(&QuotaLedgerEntry{}).
		Where("request_id = ? AND phase = ?", "mj-dispatch-claim", QuotaLedgerPhaseBind).
		Count(&claimLedgers).Error)
	assert.EqualValues(t, 1, claimLedgers)
}

func TestRefundUnclaimedMidjourneyBillingReservationPreservesClaimedRequest(t *testing.T) {
	prepareMidjourneyBillingTest(t)
	seedBillingWallet(t, 971007, 972007, 100, "mj-claim-refund")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "mj-unclaimed-refund",
		UserId:        971007,
		TokenId:       972007,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "mj-claim-refund")
	require.NoError(t, err)

	applied, err := RefundUnclaimedMidjourneyBillingReservation("mj-unclaimed-refund", "mj-claim-refund")
	require.NoError(t, err)
	assert.True(t, applied)
	reservation, err := GetBillingReservation("mj-unclaimed-refund")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusRefunded, reservation.Status)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 971007, 972007)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)

	_, err = PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "mj-claimed-no-refund",
		UserId:        971007,
		TokenId:       972007,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "mj-claim-refund")
	require.NoError(t, err)
	claimed, err := ClaimMidjourneyBillingReservation("mj-claimed-no-refund")
	require.NoError(t, err)
	require.True(t, claimed)
	applied, err = RefundUnclaimedMidjourneyBillingReservation("mj-claimed-no-refund", "mj-claim-refund")
	require.NoError(t, err)
	assert.False(t, applied)
	reservation, err = GetBillingReservation("mj-claimed-no-refund")
	require.NoError(t, err)
	assert.Equal(t, BillingReservationStatusReserved, reservation.Status)
	userQuota, tokenRemain, tokenUsed = loadBillingBalances(t, 971007, 972007)
	assert.Equal(t, 60, userQuota)
	assert.Equal(t, 60, tokenRemain)
	assert.Equal(t, 40, tokenUsed)
}

func TestMidjourneyRefundSurvivesDeletedToken(t *testing.T) {
	prepareMidjourneyBillingTest(t)
	seedBillingWallet(t, 971003, 972003, 100, "mj-deleted-token")
	task := bindMidjourneyWalletReservation(t, "mj-deleted-token", 971003, 972003, 35)
	require.NoError(t, DB.Unscoped().Delete(&Token{}, 972003).Error)

	task.Status = MidjourneyStatusFailure
	task.Progress = "100%"
	won, err := task.UpdateWithStatus("")
	require.NoError(t, err)
	require.True(t, won)
	result, err := RefundMidjourneyBillingReservation(task.Id, "mj-deleted-token", "")
	require.NoError(t, err)
	assert.True(t, result.Applied)

	var user User
	require.NoError(t, DB.Select("quota").Where("id = ?", 971003).First(&user).Error)
	assert.Equal(t, 100, user.Quota)
	var ledger QuotaLedgerEntry
	require.NoError(t, DB.Where("request_id = ? AND phase = ?", "mj-deleted-token", QuotaLedgerPhaseRefund).First(&ledger).Error)
	assert.Contains(t, ledger.Note, "token deleted")
}

func TestLegacyMidjourneyAdoptionDoesNotMutateUntilTerminalFailure(t *testing.T) {
	prepareMidjourneyBillingTest(t)
	require.NoError(t, DB.Create(&User{
		Id:       971004,
		Username: "legacy-midjourney",
		Quota:    60,
		Status:   common.UserStatusEnabled,
	}).Error)
	t.Cleanup(func() { DB.Unscoped().Delete(&User{}, 971004) })
	task := &Midjourney{
		UserId:     971004,
		MjId:       "legacy-mj-task",
		Action:     "IMAGINE",
		SubmitTime: time.Now().UnixMilli(),
		Quota:      40,
		Progress:   "50%",
	}
	require.NoError(t, DB.Create(task).Error)

	reservation, err := AdoptLegacyMidjourneyBillingReservation(task.Id)
	require.NoError(t, err)
	assert.True(t, reservation.LegacyAdopted)
	assert.Equal(t, billingTokenModeSkipped, reservation.TokenMode)
	var user User
	require.NoError(t, DB.Select("quota").Where("id = ?", 971004).First(&user).Error)
	assert.Equal(t, 60, user.Quota)

	task.PrivateData.BillingRequestId = reservation.RequestId
	task.Status = MidjourneyStatusFailure
	task.Progress = "100%"
	won, err := task.UpdateWithStatus("")
	require.NoError(t, err)
	require.True(t, won)
	refunded, err := RefundMidjourneyBillingReservation(task.Id, reservation.RequestId, "")
	require.NoError(t, err)
	assert.True(t, refunded.Applied)
	require.NoError(t, DB.Select("quota").Where("id = ?", 971004).First(&user).Error)
	assert.Equal(t, 100, user.Quota)
}

func TestStaleMidjourneyReservationWaitsForUnambiguousTerminalState(t *testing.T) {
	prepareMidjourneyBillingTest(t)
	seedBillingWallet(t, 971005, 972005, 100, "mj-stale-pending")
	task := bindMidjourneyWalletReservation(t, "mj-stale-pending", 971005, 972005, 40)
	old := common.GetTimestamp() - 120
	require.NoError(t, DB.Model(&BillingReservation{}).Where("request_id = ?", "mj-stale-pending").Updates(map[string]interface{}{
		"updated_at":         old,
		"last_reconciled_at": 0,
	}).Error)

	summary := ReconcileStaleBillingReservations(common.GetTimestamp()-60, 10)
	assert.Equal(t, 1, summary.Pending)
	assert.Equal(t, 0, summary.Refunded)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 971005, 972005)
	assert.Equal(t, 60, userQuota)
	assert.Equal(t, 60, tokenRemain)
	assert.Equal(t, 40, tokenUsed)

	task.Status = MidjourneyStatusFailure
	task.Progress = "100%"
	won, err := task.UpdateWithStatus("")
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, DB.Model(&BillingReservation{}).Where("request_id = ?", "mj-stale-pending").Updates(map[string]interface{}{
		"updated_at":         old,
		"last_reconciled_at": 0,
	}).Error)
	summary = ReconcileStaleBillingReservations(common.GetTimestamp()-60, 10)
	assert.Equal(t, 1, summary.Refunded)
	userQuota, tokenRemain, tokenUsed = loadBillingBalances(t, 971005, 972005)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Equal(t, 0, tokenUsed)
}
