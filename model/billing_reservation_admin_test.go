package model

import (
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prepareBillingReservationAdminTest(t *testing.T) {
	t.Helper()
	prepareBillingReservationTest(t)
	require.NoError(t, DB.AutoMigrate(&BillingReservationAdminResolution{}))
	require.NoError(t, DB.Exec("DELETE FROM billing_reservation_admin_resolutions").Error)
	t.Cleanup(func() {
		DB.Exec("DELETE FROM billing_reservation_admin_resolutions")
	})
}

func seedBillingAdminWallet(t *testing.T, userId, tokenId, quota int, tokenKey string) {
	t.Helper()
	require.NoError(t, DB.Create(&User{
		Id:       userId,
		Username: "billing-admin-user-" + tokenKey,
		AffCode:  "billing-admin-aff-" + tokenKey,
		Quota:    quota,
		Status:   common.UserStatusEnabled,
	}).Error)
	require.NoError(t, DB.Create(&Token{
		Id:          tokenId,
		UserId:      userId,
		Key:         tokenKey,
		Name:        "billing-admin-token",
		Status:      common.TokenStatusEnabled,
		RemainQuota: quota,
	}).Error)
	t.Cleanup(func() {
		DB.Unscoped().Delete(&Token{}, tokenId)
		DB.Unscoped().Delete(&User{}, userId)
	})
}

func createReviewedWalletReservation(t *testing.T, requestId string, userId int, tokenId int, quota int) BillingReservation {
	t.Helper()
	seedBillingAdminWallet(t, userId, tokenId, 100, requestId)
	result, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     requestId,
		UserId:        userId,
		TokenId:       tokenId,
		FundingSource: BillingFundingWallet,
		Quota:         quota,
	}, requestId)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&BillingReservation{}).
		Where("request_id = ?", requestId).
		Updates(map[string]interface{}{
			"last_reconciled_at": common.GetTimestamp(),
			"reconcile_note":     "stale reservation requires administrator review",
		}).Error)
	return result.Reservation
}

func TestListBillingReservationsForAdminOnlyReturnsAttentionQueue(t *testing.T) {
	prepareBillingReservationAdminTest(t)
	now := common.GetTimestamp()

	createReviewedWalletReservation(t, "billing-admin-reviewed", 913001, 923001, 10)

	seedBillingAdminWallet(t, 913002, 923002, 100, "billing-admin-stale")
	_, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-admin-stale",
		UserId:        913002,
		TokenId:       923002,
		FundingSource: BillingFundingWallet,
		Quota:         20,
	}, "billing-admin-stale")
	require.NoError(t, err)
	require.NoError(t, DB.Model(&BillingReservation{}).
		Where("request_id = ?", "billing-admin-stale").
		Updates(map[string]interface{}{
			"updated_at":    now - 120,
			"resource_type": BillingResourceAsyncTask,
			"resource_id":   "admin-filter-task",
		}).Error)

	seedBillingAdminWallet(t, 913003, 923003, 100, "billing-admin-pending")
	_, err = PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-admin-pending",
		UserId:        913003,
		TokenId:       923003,
		FundingSource: BillingFundingWallet,
		Quota:         30,
	}, "billing-admin-pending")
	require.NoError(t, err)
	require.NoError(t, PrepareBillingReservationSettlement("billing-admin-pending", 25))

	seedBillingAdminWallet(t, 913004, 923004, 100, "billing-admin-fresh")
	_, err = PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-admin-fresh",
		UserId:        913004,
		TokenId:       923004,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "billing-admin-fresh")
	require.NoError(t, err)

	page, err := ListBillingReservationsForAdmin(BillingReservationAdminFilters{
		StaleBefore: now - 60,
		Limit:       20,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, page.Total)
	require.Len(t, page.Reservations, 3)
	requestIds := []string{
		page.Reservations[0].RequestId,
		page.Reservations[1].RequestId,
		page.Reservations[2].RequestId,
	}
	assert.ElementsMatch(t, []string{
		"billing-admin-reviewed",
		"billing-admin-stale",
		"billing-admin-pending",
	}, requestIds)
	assert.NotContains(t, requestIds, "billing-admin-fresh")

	filtered, err := ListBillingReservationsForAdmin(BillingReservationAdminFilters{
		UserId:       913002,
		RequestId:    "billing-admin-stale",
		ResourceType: BillingResourceAsyncTask,
		StaleBefore:  now - 60,
		Limit:        20,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, filtered.Total)
	require.Len(t, filtered.Reservations, 1)
	assert.Equal(t, "billing-admin-stale", filtered.Reservations[0].RequestId)

	detail, err := GetBillingReservationAdminDetail("billing-admin-pending")
	require.NoError(t, err)
	assert.True(t, detail.Reservation.SettlementPending)
	assert.Equal(t, 25, detail.Reservation.SettlementTarget)
	assert.Len(t, detail.Ledger, 2)
}

func TestResolveBillingReservationByAdminSettlesExactlyOnce(t *testing.T) {
	prepareBillingReservationAdminTest(t)
	reservation := createReviewedWalletReservation(t, "billing-admin-settle", 913011, 923011, 40)
	reason := strings.Repeat("verified provider evidence ", 14)
	actualQuota := int64(25)
	input := BillingReservationAdminResolutionInput{
		RequestId:       reservation.RequestId,
		ExpectedVersion: reservation.Version,
		AdminId:         42,
		ActorIp:         "192.0.2.42",
		Resolution:      BillingReservationAdminSettle,
		ActualQuota:     &actualQuota,
		Reason:          reason,
	}

	results := make(chan *BillingReservationAdminResolutionResult, 2)
	errorsChannel := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := ResolveBillingReservationByAdmin(input)
			results <- result
			errorsChannel <- err
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		require.NoError(t, err)
	}
	appliedCount := 0
	for result := range results {
		require.NotNil(t, result)
		if result.Applied {
			appliedCount++
		}
		assert.Equal(t, BillingReservationStatusSettled, result.Reservation.Status)
	}
	assert.Equal(t, 1, appliedCount)

	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 913011, 923011)
	assert.Equal(t, 75, userQuota)
	assert.Equal(t, 75, tokenRemain)
	assert.Equal(t, 25, tokenUsed)

	var actionCount int64
	require.NoError(t, DB.Model(&BillingReservationAdminResolution{}).
		Where("request_id = ?", reservation.RequestId).
		Count(&actionCount).Error)
	assert.EqualValues(t, 1, actionCount)
	var action BillingReservationAdminResolution
	require.NoError(t, DB.Where("request_id = ?", reservation.RequestId).First(&action).Error)
	assert.Equal(t, strings.TrimSpace(reason), action.Reason)
	assert.Equal(t, input.ActorIp, action.ActorIp)
	require.NotNil(t, action.ActualQuota)
	assert.Equal(t, 25, *action.ActualQuota)

	var adminLedger QuotaLedgerEntry
	require.NoError(t, DB.Where("request_id = ? AND phase = ?", reservation.RequestId, QuotaLedgerPhaseAdminResolution).
		First(&adminLedger).Error)
	assert.Contains(t, adminLedger.Note, "admin_id=42")
	assert.LessOrEqual(t, len(adminLedger.Note), 255)

	conflictingInput := input
	conflictingInput.Reason = "different verified evidence"
	_, err := ResolveBillingReservationByAdmin(conflictingInput)
	assert.ErrorIs(t, err, ErrBillingAdminResolutionConflict)
}

func TestResolveBillingReservationByAdminRefundsReviewedReservation(t *testing.T) {
	prepareBillingReservationAdminTest(t)
	reservation := createReviewedWalletReservation(t, "billing-admin-refund", 913021, 923021, 40)

	result, err := ResolveBillingReservationByAdmin(BillingReservationAdminResolutionInput{
		RequestId:       reservation.RequestId,
		ExpectedVersion: reservation.Version,
		AdminId:         43,
		ActorIp:         "192.0.2.43",
		Resolution:      BillingReservationAdminRefund,
		Reason:          "provider confirmed that no billable work completed",
	})
	require.NoError(t, err)
	assert.True(t, result.Applied)
	assert.Equal(t, BillingReservationStatusRefunded, result.Reservation.Status)
	assert.Nil(t, result.Resolution.ActualQuota)
	userQuota, tokenRemain, tokenUsed := loadBillingBalances(t, 913021, 923021)
	assert.Equal(t, 100, userQuota)
	assert.Equal(t, 100, tokenRemain)
	assert.Zero(t, tokenUsed)
}

func TestResolveBillingReservationByAdminRequiresReviewVersionAndBoundedQuota(t *testing.T) {
	prepareBillingReservationAdminTest(t)
	seedBillingAdminWallet(t, 913031, 923031, 100, "billing-admin-guard")
	created, err := PreConsumeBillingReservation(BillingReservationInput{
		RequestId:     "billing-admin-guard",
		UserId:        913031,
		TokenId:       923031,
		FundingSource: BillingFundingWallet,
		Quota:         40,
	}, "billing-admin-guard")
	require.NoError(t, err)
	actualQuota := int64(25)
	input := BillingReservationAdminResolutionInput{
		RequestId:       created.Reservation.RequestId,
		ExpectedVersion: created.Reservation.Version,
		AdminId:         44,
		ActorIp:         "192.0.2.44",
		Resolution:      BillingReservationAdminSettle,
		ActualQuota:     &actualQuota,
		Reason:          "verified final provider usage",
	}

	_, err = ResolveBillingReservationByAdmin(input)
	assert.ErrorIs(t, err, ErrBillingReservationReviewRequired)

	require.NoError(t, DB.Model(&BillingReservation{}).
		Where("request_id = ?", input.RequestId).
		Update("reconcile_note", "incomplete review marker").Error)
	_, err = ResolveBillingReservationByAdmin(input)
	assert.ErrorIs(t, err, ErrBillingReservationReviewRequired)

	require.NoError(t, DB.Model(&BillingReservation{}).
		Where("request_id = ?", input.RequestId).
		Updates(map[string]interface{}{
			"last_reconciled_at": common.GetTimestamp(),
			"reconcile_note":     "review required",
		}).Error)
	wrongVersion := input
	wrongVersion.ExpectedVersion++
	_, err = ResolveBillingReservationByAdmin(wrongVersion)
	assert.ErrorIs(t, err, ErrBillingReservationVersionConflict)

	overflowQuota := int64(math.MaxInt32) + 1
	input.ActualQuota = &overflowQuota
	_, err = ResolveBillingReservationByAdmin(input)
	assert.ErrorIs(t, err, ErrBillingAdminResolutionInvalid)
	assert.Contains(t, err.Error(), "explicit int32 actual quota")

	refundWithQuota := input
	refundWithQuota.Resolution = BillingReservationAdminRefund
	refundWithQuota.ActualQuota = &actualQuota
	_, err = ResolveBillingReservationByAdmin(refundWithQuota)
	assert.ErrorIs(t, err, ErrBillingAdminResolutionInvalid)
	assert.Contains(t, err.Error(), "must not include an actual quota")
}
