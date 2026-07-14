package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupBillingStatsProjectionTest(t *testing.T) {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.AutoMigrate(&BillingStatsProjection{}, &QuotaData{}))
	require.NoError(t, DB.Exec("DELETE FROM billing_stats_projections").Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Exec("DELETE FROM billing_stats_projections").Error)
	})
}

func createBillingStatsProjectionFixture(
	t *testing.T,
	spec BillingStatsProjectionSpec,
	now time.Time,
) *BillingStatsProjection {
	t.Helper()
	var projection *BillingStatsProjection
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		projection, _, err = CreateBillingStatsProjectionTx(tx, spec, now)
		return err
	}))
	require.NotNil(t, projection)
	return projection
}

func billingStatsAcceptedSpec(operationKey string, referenceID int64, quota int, now time.Time) BillingStatsProjectionSpec {
	return BillingStatsProjectionSpec{
		OperationKey:        operationKey,
		Kind:                BillingStatsProjectionKindAccepted,
		ReferenceID:         referenceID,
		UserID:              9901,
		ChannelID:           9901,
		QuotaDelta:          quota,
		RequestDelta:        1,
		DataExportRequired:  true,
		DataExportUsername:  "projection-user",
		DataExportModelName: "projection-model",
		DataExportCreatedAt: now.Unix(),
		DataExportUseGroup:  "projection-group",
		DataExportTokenID:   81,
		DataExportNodeName:  "projection-node",
	}
}

func seedBillingStatsDimensions(t *testing.T, usedQuota, requestCount int, channelUsedQuota int64) {
	t.Helper()
	require.NoError(t, DB.Create(&User{
		Id: 9901, Username: "projection-user", AffCode: "projection-user-aff", Status: common.UserStatusEnabled,
		UsedQuota: usedQuota, RequestCount: requestCount,
	}).Error)
	require.NoError(t, DB.Create(&Channel{
		Id: 9901, Name: "projection-channel", Key: "projection-channel-key", Status: common.ChannelStatusEnabled,
		UsedQuota: channelUsedQuota,
	}).Error)
}

func TestBillingStatsProjectionReceiptClaimCompleteAndAckReplay(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	now := time.Unix(1_800_300_000, 0)
	seedBillingStatsDimensions(t, 10, 2, 20)
	spec := billingStatsAcceptedSpec("async:9901:accepted:v1", 9901, 30, now)
	projection := createBillingStatsProjectionFixture(t, spec, now)

	var replay *BillingStatsProjection
	var created bool
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		replay, created, err = CreateBillingStatsProjectionTx(tx, spec, now.Add(time.Second))
		return err
	}))
	assert.False(t, created)
	assert.Equal(t, projection.ID, replay.ID)

	conflictSpec := spec
	conflictSpec.QuotaDelta++
	err := DB.Transaction(func(tx *gorm.DB) error {
		_, _, createErr := CreateBillingStatsProjectionTx(tx, conflictSpec, now)
		return createErr
	})
	assert.ErrorIs(t, err, ErrBillingStatsProjectionConflict)
	sourceConflictSpec := spec
	sourceConflictSpec.OperationKey = "async:9901:accepted:alternate:v1"
	err = DB.Transaction(func(tx *gorm.DB) error {
		_, _, createErr := CreateBillingStatsProjectionTx(tx, sourceConflictSpec, now)
		return createErr
	})
	assert.ErrorIs(t, err, ErrBillingStatsProjectionConflict)

	claimed, won, err := ClaimBillingStatsProjection(context.Background(), projection.ID, "worker-a", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	assert.Equal(t, 1, claimed.Attempts)
	_, won, err = ClaimBillingStatsProjection(context.Background(), projection.ID, "worker-b", now, time.Minute)
	require.NoError(t, err)
	assert.False(t, won)

	completed, err := CompleteClaimedBillingStatsProjection(context.Background(), projection.ID, "worker-a", now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, BillingStatsProjectionStateCompleted, completed.State)
	assert.Equal(t, BillingUsageOutcomeApplied, completed.UserOutcome)
	assert.Equal(t, BillingUsageOutcomeApplied, completed.ChannelOutcome)
	assert.Equal(t, BillingQuotaDataOutcomeApplied, completed.DataExportOutcome)

	// The commit acknowledgement may be lost. Replaying completion observes the
	// durable receipt and must not apply any counter a second time.
	_, err = CompleteClaimedBillingStatsProjection(context.Background(), projection.ID, "worker-a", now.Add(2*time.Second))
	require.NoError(t, err)
	var user User
	var channel Channel
	require.NoError(t, DB.First(&user, 9901).Error)
	require.NoError(t, DB.First(&channel, 9901).Error)
	assert.Equal(t, 40, user.UsedQuota)
	assert.Equal(t, 3, user.RequestCount)
	assert.Equal(t, int64(50), channel.UsedQuota)
	var exported QuotaData
	require.NoError(t, DB.Where("user_id = ?", 9901).First(&exported).Error)
	assert.Equal(t, 1, exported.Count)
	assert.Equal(t, 30, exported.Quota)
	assert.Equal(t, now.Unix()-now.Unix()%3600, exported.CreatedAt)
}

func TestBillingStatsProjectionAcceptedZeroQuotaStillCountsRequestAndExport(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	now := time.Unix(1_800_300_100, 0)
	seedBillingStatsDimensions(t, 10, 2, 20)
	projection := createBillingStatsProjectionFixture(t,
		billingStatsAcceptedSpec("async:9902:accepted:v1", 9902, 0, now), now)
	_, won, err := ClaimBillingStatsProjection(context.Background(), projection.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	completed, err := CompleteClaimedBillingStatsProjection(context.Background(), projection.ID, "worker", now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, BillingUsageOutcomeApplied, completed.UserOutcome)
	assert.Equal(t, BillingUsageOutcomeNotRequired, completed.ChannelOutcome)

	var user User
	var channel Channel
	require.NoError(t, DB.First(&user, 9901).Error)
	require.NoError(t, DB.First(&channel, 9901).Error)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 3, user.RequestCount)
	assert.Equal(t, int64(20), channel.UsedQuota)
	var exported QuotaData
	require.NoError(t, DB.Where("user_id = ?", 9901).First(&exported).Error)
	assert.Equal(t, 1, exported.Count)
	assert.Zero(t, exported.Quota)
}

func TestBillingStatsProjectionCompletesWithSaturationAndMissingOutcomes(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	now := time.Unix(1_800_300_200, 0)
	seedBillingStatsDimensions(t, math.MaxInt32-1, math.MaxInt32, math.MaxInt64-1)
	spec := billingStatsAcceptedSpec("async:9903:accepted:v1", 9903, 10, now)
	spec.DataExportRequired = false
	projection := createBillingStatsProjectionFixture(t, spec, now)
	_, won, err := ClaimBillingStatsProjection(context.Background(), projection.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	completed, err := CompleteClaimedBillingStatsProjection(context.Background(), projection.ID, "worker", now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, BillingStatsProjectionStateCompleted, completed.State)
	assert.Equal(t, BillingUsageOutcomeSaturated, completed.UserOutcome)
	assert.Equal(t, BillingUsageOutcomeSkippedOverflow, completed.ChannelOutcome)

	missingSpec := billingStatsAcceptedSpec("async:9904:accepted:v1", 9904, 10, now)
	missingSpec.UserID = 999_991
	missingSpec.ChannelID = 999_991
	missingSpec.DataExportRequired = false
	missing := createBillingStatsProjectionFixture(t, missingSpec, now)
	_, won, err = ClaimBillingStatsProjection(context.Background(), missing.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	completed, err = CompleteClaimedBillingStatsProjection(context.Background(), missing.ID, "worker", now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, BillingUsageOutcomeSkippedMissing, completed.UserOutcome)
	assert.Equal(t, BillingUsageOutcomeSkippedMissing, completed.ChannelOutcome)
}

func TestBillingStatsProjectionCrashRollbackAndLeaseRecovery(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	now := time.Unix(1_800_300_300, 0)
	seedBillingStatsDimensions(t, 10, 2, 20)
	spec := billingStatsAcceptedSpec("async:9905:accepted:v1", 9905, 30, now)
	spec.DataExportRequired = false
	projection := createBillingStatsProjectionFixture(t, spec, now)
	_, won, err := ClaimBillingStatsProjection(context.Background(), projection.ID, "worker-a", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)

	crash := errors.New("simulated crash before stats receipt")
	err = DB.Transaction(func(tx *gorm.DB) error {
		_, applyErr := applyBillingUsageProjectionTx(tx, billingUsageProjectionSpec{
			OperationKey: spec.OperationKey, UserID: spec.UserID, ChannelID: spec.ChannelID,
			QuotaDelta: spec.QuotaDelta, RequestDelta: spec.RequestDelta,
		})
		require.NoError(t, applyErr)
		return crash
	})
	assert.ErrorIs(t, err, crash)
	var user User
	require.NoError(t, DB.First(&user, 9901).Error)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 2, user.RequestCount)

	_, won, err = ClaimBillingStatsProjection(context.Background(), projection.ID, "worker-b", now.Add(59*time.Second), time.Minute)
	require.NoError(t, err)
	assert.False(t, won)
	_, won, err = ClaimBillingStatsProjection(context.Background(), projection.ID, "worker-b", now.Add(61*time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = CompleteClaimedBillingStatsProjection(context.Background(), projection.ID, "worker-b", now.Add(62*time.Second))
	require.NoError(t, err)
	require.NoError(t, DB.First(&user, 9901).Error)
	assert.Equal(t, 40, user.UsedQuota)
	assert.Equal(t, 3, user.RequestCount)
}

func TestBillingStatsProjectionNormalizesInvalidExportAndSplitsOverflow(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	now := time.Unix(1_800_300_400, 0)
	seedBillingStatsDimensions(t, 10, 2, 20)
	invalidSpec := billingStatsAcceptedSpec("async:9906:accepted:v1", 9906, 1, now)
	invalidSpec.DataExportModelName = string([]byte{'x', 0xff})
	projection := createBillingStatsProjectionFixture(t, invalidSpec, now)
	assert.False(t, projection.DataExportRequired)
	assert.Empty(t, projection.DataExportModelName)
	assert.Equal(t, BillingQuotaDataOutcomeSkippedInvalid, projection.DataExportOutcome)
	_, won, err := ClaimBillingStatsProjection(context.Background(), projection.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	completed, err := CompleteClaimedBillingStatsProjection(context.Background(), projection.ID, "worker", now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, BillingQuotaDataOutcomeSkippedInvalid, completed.DataExportOutcome)

	require.NoError(t, DB.Create(&QuotaData{
		UserID: 9901, Username: "projection-user", ModelName: "projection-model",
		CreatedAt: now.Unix() - now.Unix()%3600, UseGroup: "projection-group", TokenID: 81,
		ChannelID: 9901, NodeName: "projection-node", Count: math.MaxInt32, Quota: math.MaxInt32,
	}).Error)
	splitSpec := billingStatsAcceptedSpec("async:9907:accepted:v1", 9907, 1, now)
	split := createBillingStatsProjectionFixture(t, splitSpec, now)
	_, won, err = ClaimBillingStatsProjection(context.Background(), split.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	completed, err = CompleteClaimedBillingStatsProjection(context.Background(), split.ID, "worker", now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, BillingQuotaDataOutcomeAppliedSplit, completed.DataExportOutcome)
	var rowCount int64
	var totalCount int64
	var totalQuota int64
	require.NoError(t, DB.Model(&QuotaData{}).Where("user_id = ?", 9901).Count(&rowCount).Error)
	require.NoError(t, DB.Model(&QuotaData{}).Where("user_id = ?", 9901).Select("COALESCE(SUM(count), 0)").Scan(&totalCount).Error)
	require.NoError(t, DB.Model(&QuotaData{}).Where("user_id = ?", 9901).Select("COALESCE(SUM(quota), 0)").Scan(&totalQuota).Error)
	assert.Equal(t, int64(2), rowCount)
	assert.Equal(t, int64(math.MaxInt32)+1, totalCount)
	assert.Equal(t, int64(math.MaxInt32)+1, totalQuota)

	// Once a split row exists, subsequent observations must accumulate into the
	// newest row instead of creating one row per request forever.
	nextSpec := billingStatsAcceptedSpec("async:9907:accepted:v2", 9908, 1, now)
	next := createBillingStatsProjectionFixture(t, nextSpec, now)
	_, won, err = ClaimBillingStatsProjection(context.Background(), next.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	completed, err = CompleteClaimedBillingStatsProjection(context.Background(), next.ID, "worker", now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, BillingQuotaDataOutcomeApplied, completed.DataExportOutcome)
	require.NoError(t, DB.Model(&QuotaData{}).Where("user_id = ?", 9901).Count(&rowCount).Error)
	assert.Equal(t, int64(2), rowCount)
	var newest QuotaData
	require.NoError(t, DB.Where("user_id = ?", 9901).Order("id desc").First(&newest).Error)
	assert.Equal(t, 2, newest.Count)
	assert.Equal(t, 2, newest.Quota)
}

func TestBillingStatsProjectionSplitsCorruptQuotaDataWithoutOverflow(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("the corrupt 64-bit quota-data fixture requires a 64-bit int")
	}
	setupBillingStatsProjectionTest(t)
	now := time.Unix(1_800_300_450, 0)
	seedBillingStatsDimensions(t, 10, 2, 20)
	require.NoError(t, DB.Create(&QuotaData{
		UserID: 9901, Username: "projection-user", ModelName: "projection-model",
		CreatedAt: now.Unix() - now.Unix()%3600, UseGroup: "projection-group", TokenID: 81,
		ChannelID: 9901, NodeName: "projection-node", Count: int(math.MaxInt64),
		Quota: int(math.MaxInt64), TokenUsed: int(math.MaxInt64),
	}).Error)

	spec := billingStatsAcceptedSpec("async:9907:accepted:corrupt", 9909, 1, now)
	spec.DataExportTokenUsed = 1
	projection := createBillingStatsProjectionFixture(t, spec, now)
	_, won, err := ClaimBillingStatsProjection(context.Background(), projection.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	completed, err := CompleteClaimedBillingStatsProjection(
		context.Background(), projection.ID, "worker", now.Add(time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, BillingQuotaDataOutcomeRepairedSplit, completed.DataExportOutcome)

	var rows []QuotaData
	require.NoError(t, DB.Where("user_id = ?", 9901).Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2)
	assert.Equal(t, math.MaxInt32, rows[0].Count)
	assert.Equal(t, math.MaxInt32, rows[0].Quota)
	assert.Equal(t, math.MaxInt32, rows[0].TokenUsed)
	newest := rows[1]
	assert.Equal(t, 1, newest.Count)
	assert.Equal(t, 1, newest.Quota)
	assert.Equal(t, 1, newest.TokenUsed)
}

func TestBillingQuotaDataProjectionSplitsWhenOnlyCountIsFull(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	now := time.Unix(1_800_300_475, 0)
	require.NoError(t, DB.Create(&QuotaData{
		UserID: 9901, Username: "projection-user", ModelName: "projection-model",
		CreatedAt: now.Unix() - now.Unix()%3600, UseGroup: "projection-group", TokenID: 81,
		ChannelID: 9901, NodeName: "projection-node", Count: math.MaxInt32,
	}).Error)

	var outcome string
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		outcome, err = applyBillingQuotaDataProjectionTx(tx, billingQuotaDataProjectionSpec{
			Required: true, UserID: 9901, Username: "projection-user", ModelName: "projection-model",
			CreatedAt: now.Unix(), Quota: 1, UseGroup: "projection-group", TokenID: 81,
			ChannelID: 9901, NodeName: "projection-node",
		})
		return err
	}))
	assert.Equal(t, BillingQuotaDataOutcomeAppliedSplit, outcome)
	var rowCount int64
	require.NoError(t, DB.Model(&QuotaData{}).Where("user_id = ?", 9901).Count(&rowCount).Error)
	assert.Equal(t, int64(2), rowCount)
}

func TestBillingStatsProjectionRetryErrorIsUTF8SafeAndExhaustionFailsClosed(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	now := time.Unix(1_800_300_500, 0)
	seedBillingStatsDimensions(t, 10, 2, 20)
	spec := billingStatsAcceptedSpec("async:9908:accepted:v1", 9908, 1, now)
	spec.DataExportRequired = false
	projection := createBillingStatsProjectionFixture(t, spec, now)
	_, won, err := ClaimBillingStatsProjection(context.Background(), projection.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, RetryClaimedBillingStatsProjection(
		context.Background(), projection.ID, "worker", now.Add(time.Second), errors.New(strings.Repeat("界", 500)),
	))
	retried, err := GetBillingStatsProjection(context.Background(), projection.ID)
	require.NoError(t, err)
	assert.Equal(t, BillingStatsProjectionStatePending, retried.State)
	assert.LessOrEqual(t, len(retried.LastError), billingStatsProjectionErrorMaxBytes)
	assert.True(t, utf8.ValidString(retried.LastError))

	require.NoError(t, DB.Model(&BillingStatsProjection{}).Where("id = ?", projection.ID).Updates(map[string]any{
		"attempts": billingStatsProjectionMaxAttempts, "next_retry_ms": 0,
	}).Error)
	failed, won, err := ClaimBillingStatsProjection(context.Background(), projection.ID, "worker", now.Add(time.Minute), time.Minute)
	require.NoError(t, err)
	assert.False(t, won)
	assert.Equal(t, BillingStatsProjectionStateFailed, failed.State)
	assert.Equal(t, "retry_exhausted", failed.FailureCode)
	assert.False(t, HasRecoverableBillingStatsProjections(now.Add(2*time.Minute)))
}

func TestBillingStatsProjectionFailedAuditCanBeExplicitlyRequeued(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	now := time.Unix(1_800_300_600, 0)
	seedBillingStatsDimensions(t, 10, 2, 20)
	spec := billingStatsAcceptedSpec("async:9909:accepted:v1", 9909, 1, now)
	spec.DataExportRequired = false
	projection := createBillingStatsProjectionFixture(t, spec, now)
	_, won, err := ClaimBillingStatsProjection(context.Background(), projection.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, FailClaimedBillingStatsProjection(
		context.Background(), projection.ID, "worker", now.Add(time.Second), "invalid_frozen_spec",
	))

	failed, err := FindFailedBillingStatsProjections(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, failed, 1)
	assert.Equal(t, projection.ID, failed[0].ID)
	assert.Equal(t, "invalid_frozen_spec", failed[0].FailureCode)
	assert.Empty(t, failed[0].LastError)
	require.NoError(t, RequeueFailedBillingStatsProjection(
		context.Background(), projection.ID, "invalid_frozen_spec", now.Add(2*time.Second),
	))
	requeued, err := GetBillingStatsProjection(context.Background(), projection.ID)
	require.NoError(t, err)
	assert.Equal(t, BillingStatsProjectionStatePending, requeued.State)
	assert.Zero(t, requeued.Attempts)
	assert.Empty(t, requeued.FailureCode)
}

func TestBillingStatsProjectionRetentionKeepsReferencedEvidence(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	require.NoError(t, DB.AutoMigrate(&AsyncBillingReservation{}))
	require.NoError(t, DB.Exec("DELETE FROM async_billing_reservations").Error)
	t.Cleanup(func() { require.NoError(t, DB.Exec("DELETE FROM async_billing_reservations").Error) })
	now := time.Unix(1_900_000_000, 0)
	seedBillingStatsDimensions(t, 10, 2, 20)
	reservation := &AsyncBillingReservation{
		ReservationKey: "retention-reservation", ProtocolVersion: 2, Kind: AsyncBillingKindTask,
		PublicTaskID: "retention-task", State: AsyncBillingReservationStateTerminal,
		UserID: 9901, FundingSource: "wallet", CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}
	require.NoError(t, DB.Create(reservation).Error)
	spec := billingStatsAcceptedSpec("async:9910:accepted:v1", reservation.ID, 1, now)
	spec.DataExportRequired = false
	projection := createBillingStatsProjectionFixture(t, spec, now)
	oldCompletedMs := now.Add(-billingStatsProjectionCompletedRetention - time.Hour).UnixMilli()
	require.NoError(t, DB.Model(&BillingStatsProjection{}).Where("id = ?", projection.ID).Updates(map[string]any{
		"state": BillingStatsProjectionStateCompleted, "completed_time_ms": oldCompletedMs,
	}).Error)

	deleted, err := CleanupExpiredBillingStatsProjections(context.Background(), now, 100)
	require.NoError(t, err)
	assert.Zero(t, deleted, "the authoritative reservation still references the stats receipt")
	require.NoError(t, DB.Delete(&AsyncBillingReservation{}, reservation.ID).Error)

	key := spec.OperationKey
	require.NoError(t, DB.Create(&Log{
		CreatedAt: 1_800_300_700, BillingOperationKey: &key,
		BillingPayloadHash:     strings.Repeat("a", billingLogPayloadHashEncodedBytes),
		BillingPayloadProtocol: billingLogPayloadProtocol,
	}).Error)
	deleted, err = CleanupExpiredBillingStatsProjections(context.Background(), now, 100)
	require.NoError(t, err)
	assert.Zero(t, deleted, "the external log receipt still references the stats evidence")
	require.NoError(t, DB.Where("billing_operation_key = ?", key).Delete(&Log{}).Error)

	deleted, err = CleanupExpiredBillingStatsProjections(context.Background(), now, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	_, err = GetBillingStatsProjection(context.Background(), projection.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestBillingStatsProjectionRetentionCursorSkipsReferencedHead(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	require.NoError(t, DB.AutoMigrate(&AsyncBillingReservation{}))
	require.NoError(t, DB.Exec("DELETE FROM async_billing_reservations").Error)
	t.Cleanup(func() { require.NoError(t, DB.Exec("DELETE FROM async_billing_reservations").Error) })
	now := time.Now()
	seedBillingStatsDimensions(t, 0, 0, 0)
	reservation := &AsyncBillingReservation{
		ReservationKey: "stats-cursor-reservation", ProtocolVersion: 2, Kind: AsyncBillingKindTask,
		PublicTaskID: "stats-cursor-task", State: AsyncBillingReservationStateTerminal,
		UserID: 9901, FundingSource: "wallet", CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}
	require.NoError(t, DB.Create(reservation).Error)
	referencedSpec := billingStatsAcceptedSpec("async:9920:accepted:v1", reservation.ID, 1, now)
	referencedSpec.DataExportRequired = false
	referenced := createBillingStatsProjectionFixture(t, referencedSpec, now)
	orphanSpec := billingStatsAcceptedSpec("async:9921:accepted:v1", reservation.ID+999, 1, now)
	orphanSpec.DataExportRequired = false
	orphan := createBillingStatsProjectionFixture(t, orphanSpec, now)
	oldCompletedMs := now.Add(-billingStatsProjectionCompletedRetention - time.Hour).UnixMilli()
	require.NoError(t, DB.Model(&BillingStatsProjection{}).Where("id IN ?", []int64{referenced.ID, orphan.ID}).
		Updates(map[string]any{"state": BillingStatsProjectionStateCompleted, "completed_time_ms": oldCompletedMs}).Error)

	deleted, nextID, hasMore, err := CleanupExpiredBillingStatsProjectionsPage(context.Background(), now, 0, 1)
	require.NoError(t, err)
	assert.Zero(t, deleted)
	assert.True(t, hasMore)
	assert.Equal(t, referenced.ID, nextID)
	deleted, nextID, hasMore, err = CleanupExpiredBillingStatsProjectionsPage(context.Background(), now, nextID, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	assert.Zero(t, nextID)
	assert.False(t, hasMore)
	_, err = GetBillingStatsProjection(context.Background(), orphan.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = GetBillingStatsProjection(context.Background(), referenced.ID)
	require.NoError(t, err)
}

func TestBillingStatsProjectionSchemaReadinessRequiresUniqueReceipt(t *testing.T) {
	setupBillingStatsProjectionTest(t)
	ready, err := billingStatsProjectionSchemaReady(DB)
	require.NoError(t, err)
	assert.True(t, ready)
	require.NoError(t, waitBillingStatsProjectionSchemaReady(context.Background(), DB, 0))
	require.NoError(t, DB.Migrator().DropIndex(&BillingStatsProjection{}, billingStatsProjectionSourceIndex))
	ready, err = billingStatsProjectionSchemaReady(DB)
	require.NoError(t, err)
	assert.False(t, ready)
	require.NoError(t, DB.Migrator().CreateIndex(&BillingStatsProjection{}, billingStatsProjectionSourceIndex))

	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/projection-schema.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE billing_stats_projections (
		operation_key varchar(64) NOT NULL,
		protocol_version integer, kind varchar(24), reference_id bigint, user_id integer, channel_id integer,
		quota_delta integer, request_delta integer, data_export_required numeric,
		data_export_username varchar(191), data_export_model_name varchar(191), data_export_created_at bigint,
		data_export_token_used integer, data_export_use_group varchar(64), data_export_token_id integer,
		data_export_node_name varchar(128), data_export_outcome varchar(32),
		state varchar(16), lease_owner varchar(128), lease_until_ms bigint, attempts integer, next_retry_ms bigint,
		last_error varchar(1024), failure_code varchar(64), user_outcome varchar(32), channel_outcome varchar(32),
		created_time_ms bigint, updated_time_ms bigint, completed_time_ms bigint
	)`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX `+billingStatsProjectionOperationIndex+
		` ON billing_stats_projections(operation_key)`).Error)
	ready, err = billingStatsProjectionSchemaReady(db)
	require.NoError(t, err)
	assert.False(t, ready)
}

func TestBillingStatsProjectionExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql57", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres96", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			if db.Migrator().HasTable(&BillingStatsProjection{}) || db.Migrator().HasTable(&User{}) ||
				db.Migrator().HasTable(&Channel{}) || db.Migrator().HasTable(&QuotaData{}) {
				t.Skip("refusing to use a non-empty external projection database")
			}
			previousDB := DB
			previousLogDB := LOG_DB
			previousMainType := common.MainDatabaseType()
			previousLogType := common.LogDatabaseType()
			DB = db
			LOG_DB = db
			common.SetDatabaseTypes(test.dbType, test.dbType)
			t.Cleanup(func() {
				_ = db.Migrator().DropTable(&BillingStatsProjection{}, &QuotaData{}, &Channel{}, &User{})
				DB = previousDB
				LOG_DB = previousLogDB
				common.SetDatabaseTypes(previousMainType, previousLogType)
			})
			require.NoError(t, db.AutoMigrate(&User{}, &Channel{}, &QuotaData{}, &BillingStatsProjection{}))
			require.NoError(t, db.AutoMigrate(&User{}, &Channel{}, &QuotaData{}, &BillingStatsProjection{}),
				"billing stats projection migration must be idempotent")
			ready, err := billingStatsProjectionSchemaReady(db)
			require.NoError(t, err)
			require.True(t, ready)

			now := time.Unix(1_800_300_800, 0)
			seedBillingStatsDimensions(t, 10, 2, 20)
			if test.dbType == common.DatabaseTypeMySQL {
				rrSpec := billingStatsAcceptedSpec("async:mysql-rr:accepted:v1", 9910, 5, now)
				rrSpec.DataExportRequired = false
				contender := db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
				require.NoError(t, contender.Error)
				var snapshotCount int64
				require.NoError(t, contender.Model(&BillingStatsProjection{}).
					Where("operation_key = ?", rrSpec.OperationKey).Count(&snapshotCount).Error)
				require.Zero(t, snapshotCount)

				winner := createBillingStatsProjectionFixture(t, rrSpec, now)
				replay, created, replayErr := CreateBillingStatsProjectionTx(contender, rrSpec, now.Add(time.Second))
				require.NoError(t, replayErr)
				assert.False(t, created)
				assert.Equal(t, winner.ID, replay.ID)
				require.NoError(t, contender.Rollback().Error)
			}
			spec := billingStatsAcceptedSpec("async:external:accepted:v1", 9911, 30, now)
			spec.DataExportRequired = false
			projection := createBillingStatsProjectionFixture(t, spec, now)
			const workers = 8
			var wait sync.WaitGroup
			winnerCh := make(chan string, workers)
			errCh := make(chan error, workers)
			for index := 0; index < workers; index++ {
				owner := fmt.Sprintf("worker-%d", index)
				wait.Add(1)
				go func() {
					defer wait.Done()
					_, won, claimErr := ClaimBillingStatsProjection(context.Background(), projection.ID, owner, now, time.Minute)
					if claimErr != nil {
						errCh <- claimErr
						return
					}
					if won {
						winnerCh <- owner
					}
				}()
			}
			wait.Wait()
			close(winnerCh)
			close(errCh)
			for claimErr := range errCh {
				require.NoError(t, claimErr)
			}
			winners := make([]string, 0, 1)
			for owner := range winnerCh {
				winners = append(winners, owner)
			}
			require.Len(t, winners, 1)
			_, err = CompleteClaimedBillingStatsProjection(context.Background(), projection.ID, winners[0], now.Add(time.Second))
			require.NoError(t, err)
			_, err = CompleteClaimedBillingStatsProjection(context.Background(), projection.ID, winners[0], now.Add(2*time.Second))
			require.NoError(t, err)
			var user User
			var channel Channel
			require.NoError(t, db.First(&user, 9901).Error)
			require.NoError(t, db.First(&channel, 9901).Error)
			assert.Equal(t, 40, user.UsedQuota)
			assert.Equal(t, 3, user.RequestCount)
			assert.Equal(t, int64(50), channel.UsedQuota)
		})
	}
}
