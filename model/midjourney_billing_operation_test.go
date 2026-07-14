package model

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupMidjourneyBillingOperationTest(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupMidjourneyModelTestDB(t)
	previousLogDB := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = previousLogDB })
	require.NoError(t, db.AutoMigrate(
		&MidjourneyBillingOperation{},
		&AsyncBillingReservation{},
		&User{},
		&Token{},
		&UserSubscription{},
		&Channel{},
		&Log{},
	))
	return db
}

func insertMidjourneyBillingFixture(
	t *testing.T,
	db *gorm.DB,
	id int,
	walletQuota int,
	tokenRemain int,
	tokenUsed int,
	billingSource string,
	subscriptionID int,
) *Midjourney {
	t.Helper()
	require.NoError(t, db.Create(&User{
		Id:       id,
		Username: fmt.Sprintf("midjourney-billing-user-%d", id),
		AffCode:  fmt.Sprintf("midjourney-billing-aff-%d", id),
		Quota:    walletQuota,
		Status:   common.UserStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&Token{
		Id:          id,
		UserId:      id,
		Key:         fmt.Sprintf("sk-midjourney-billing-%d", id),
		Name:        fmt.Sprintf("midjourney-billing-token-%d", id),
		Status:      common.TokenStatusEnabled,
		RemainQuota: tokenRemain,
		UsedQuota:   tokenUsed,
	}).Error)
	require.NoError(t, db.Create(&Channel{
		Id:     id,
		Name:   fmt.Sprintf("midjourney-billing-channel-%d", id),
		Key:    fmt.Sprintf("sk-midjourney-channel-%d", id),
		Status: common.ChannelStatusEnabled,
	}).Error)
	if subscriptionID > 0 {
		require.NoError(t, db.Create(&UserSubscription{
			Id:          subscriptionID,
			UserId:      id,
			AmountTotal: 1000,
			AmountUsed:  200,
			Status:      "active",
		}).Error)
	}
	task := &Midjourney{
		UserId:                 id,
		Action:                 "IMAGINE",
		MjId:                   fmt.Sprintf("task_midjourney_billing_%d", id),
		Status:                 "IN_PROGRESS",
		Progress:               "50%",
		ChannelId:              id,
		Quota:                  100,
		Group:                  "default",
		BillingSource:          billingSource,
		BillingProtocolVersion: TaskBillingLegacyProtocolVersion,
		SubscriptionId:         subscriptionID,
		TokenId:                id,
	}
	require.NoError(t, db.Create(task).Error)
	return task
}

func finalizeMidjourneyBillingFixture(t *testing.T, task *Midjourney, now time.Time) *MidjourneyBillingOperation {
	t.Helper()
	observed := *task
	observed.Code = 4
	observed.Description = "upstream failed"
	observed.FailReason = "upstream failed"
	observed.Status = "FAILURE"
	observed.Progress = "100%"
	observed.FinishTime = now.UnixMilli()
	result, err := FinalizeMidjourneyFailureWithOperation(
		context.Background(), &observed, task.Status, "mj_imagine", now,
	)
	require.NoError(t, err)
	require.True(t, result.Transitioned)
	return &result.Operation
}

func TestMidjourneyBillingOperationFinalizationKeepsQuotaAndHasSingleWinner(t *testing.T) {
	db := setupMidjourneyBillingOperationTest(t)
	task := insertMidjourneyBillingFixture(t, db, 401, 900, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_010_000, 0)

	const workers = 10
	results := make(chan *MidjourneyFailureFinalization, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(offset int) {
			defer wait.Done()
			observed := *task
			observed.Status = "FAILURE"
			observed.Progress = "100%"
			observed.FailReason = fmt.Sprintf("failure-%d", offset)
			result, err := FinalizeMidjourneyFailureWithOperation(
				context.Background(), &observed, task.Status, "mj_imagine", now.Add(time.Duration(offset)*time.Millisecond),
			)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(index)
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	winners := 0
	for result := range results {
		if result.Transitioned {
			winners++
		}
	}
	assert.Equal(t, 1, winners)
	var persisted Midjourney
	require.NoError(t, db.First(&persisted, task.Id).Error)
	assert.Equal(t, "FAILURE", persisted.Status)
	assert.Equal(t, "100%", persisted.Progress)
	assert.Equal(t, 100, persisted.Quota, "refund quota remains durable until the financial transaction commits")
	var operationCount int64
	require.NoError(t, db.Model(&MidjourneyBillingOperation{}).Where("midjourney_id = ?", task.Id).Count(&operationCount).Error)
	assert.Equal(t, int64(1), operationCount)
}

func TestMidjourneyBillingOperationWalletSoftDeletedTokenLeaseAndReplay(t *testing.T) {
	db := setupMidjourneyBillingOperationTest(t)
	task := insertMidjourneyBillingFixture(t, db, 402, 900, 900, 100, TaskBillingSourceWallet, 0)
	require.NoError(t, db.Delete(&Token{}, 402).Error)
	now := time.Unix(1_800_010_100, 0)
	operation := finalizeMidjourneyBillingFixture(t, task, now)

	_, claimed, err := ClaimMidjourneyBillingOperation(context.Background(), operation.ID, "worker-a", now, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, claimed, err = ClaimMidjourneyBillingOperation(context.Background(), operation.ID, "worker-b", now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	assert.False(t, claimed)
	_, claimed, err = ClaimMidjourneyBillingOperation(context.Background(), operation.ID, "worker-b", now.Add(61*time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = CompleteMidjourneyBillingOperation(context.Background(), operation.ID, "worker-a", now.Add(62*time.Second))
	assert.ErrorIs(t, err, ErrMidjourneyBillingOperationNotClaimed)
	completed, err := CompleteMidjourneyBillingOperation(context.Background(), operation.ID, "worker-b", now.Add(62*time.Second))
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationStateCompleted, completed.State)

	var user User
	var token Token
	var persisted Midjourney
	require.NoError(t, db.First(&user, 402).Error)
	require.NoError(t, db.Unscoped().First(&token, 402).Error)
	require.NoError(t, db.First(&persisted, task.Id).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Equal(t, 0, token.UsedQuota)
	assert.True(t, token.DeletedAt.Valid)
	assert.Zero(t, persisted.Quota)

	_, err = CompleteMidjourneyBillingOperation(context.Background(), operation.ID, "other-worker", now.Add(5*time.Minute))
	require.NoError(t, err)
	var replayedUser User
	var replayedToken Token
	require.NoError(t, db.First(&replayedUser, 402).Error)
	require.NoError(t, db.Unscoped().First(&replayedToken, 402).Error)
	assert.Equal(t, user.Quota, replayedUser.Quota)
	assert.Equal(t, token.RemainQuota, replayedToken.RemainQuota)
}

func TestMidjourneyBillingOperationSubscriptionRefundIsAtomic(t *testing.T) {
	db := setupMidjourneyBillingOperationTest(t)
	task := insertMidjourneyBillingFixture(t, db, 403, 500, 900, 100, TaskBillingSourceSubscription, 403)
	now := time.Unix(1_800_010_200, 0)
	operation := finalizeMidjourneyBillingFixture(t, task, now)
	_, claimed, err := ClaimMidjourneyBillingOperation(context.Background(), operation.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = CompleteMidjourneyBillingOperation(context.Background(), operation.ID, "worker", now.Add(time.Second))
	require.NoError(t, err)

	var subscription UserSubscription
	var user User
	var token Token
	require.NoError(t, db.First(&subscription, 403).Error)
	require.NoError(t, db.First(&user, 403).Error)
	require.NoError(t, db.First(&token, 403).Error)
	assert.Equal(t, int64(100), subscription.AmountUsed)
	assert.Equal(t, 500, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Equal(t, 0, token.UsedQuota)
}

func TestMidjourneyBillingOperationRollbackAndRetry(t *testing.T) {
	db := setupMidjourneyBillingOperationTest(t)
	task := insertMidjourneyBillingFixture(t, db, 404, 900, 900, 50, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_010_300, 0)
	operation := finalizeMidjourneyBillingFixture(t, task, now)
	_, claimed, err := ClaimMidjourneyBillingOperation(context.Background(), operation.ID, "worker-a", now, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = CompleteMidjourneyBillingOperation(context.Background(), operation.ID, "worker-a", now.Add(time.Second))
	assert.ErrorIs(t, err, ErrMidjourneyBillingOperationInvariant)

	var user User
	var token Token
	var persisted Midjourney
	require.NoError(t, db.First(&user, 404).Error)
	require.NoError(t, db.First(&token, 404).Error)
	require.NoError(t, db.First(&persisted, task.Id).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Equal(t, 50, token.UsedQuota)
	assert.Equal(t, 100, persisted.Quota)

	retryAt := now.Add(10 * time.Second)
	_, err = RetryMidjourneyBillingOperation(context.Background(), operation.ID, "worker-a", now.Add(2*time.Second), retryAt, "token accounting mismatch")
	require.NoError(t, err)
	require.NoError(t, db.Model(&Token{}).Where("id = ?", 404).Update("used_quota", 100).Error)
	_, claimed, err = ClaimMidjourneyBillingOperation(context.Background(), operation.ID, "worker-b", retryAt, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = CompleteMidjourneyBillingOperation(context.Background(), operation.ID, "worker-b", retryAt.Add(time.Second))
	require.NoError(t, err)
}

func TestMidjourneyBillingOperationLegacyTerminalCreatesNoopMarker(t *testing.T) {
	db := setupMidjourneyBillingOperationTest(t)
	task := insertMidjourneyBillingFixture(t, db, 405, 900, 900, 100, TaskBillingSourceWallet, 0)
	require.NoError(t, db.Model(&Midjourney{}).Where("id = ?", task.Id).Updates(map[string]any{
		"status": "FAILURE", "progress": "100%",
	}).Error)
	observed := *task
	observed.Status = "FAILURE"
	observed.Progress = "100%"
	result, err := FinalizeMidjourneyFailureWithOperation(
		context.Background(), &observed, task.Status, "mj_imagine", time.Unix(1_800_010_400, 0),
	)
	require.NoError(t, err)
	assert.False(t, result.Transitioned)
	assert.Equal(t, TaskBillingOperationKindNoop, result.Operation.Kind)
	assert.Equal(t, TaskBillingOperationStateCompleted, result.Operation.State)
	assert.Zero(t, result.Operation.RefundQuota)

	var user User
	var token Token
	require.NoError(t, db.First(&user, 405).Error)
	require.NoError(t, db.First(&token, 405).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
}

func TestMidjourneyBillingOperationHistoricalVersionZeroTerminalCreatesSingleNoop(t *testing.T) {
	db := setupMidjourneyBillingOperationTest(t)
	task := insertMidjourneyBillingFixture(t, db, 408, 900, 900, 100, TaskBillingSourceWallet, 0)
	require.NoError(t, db.Model(&Midjourney{}).Where("id = ?", task.Id).Updates(map[string]any{
		"status": "FAILURE", "progress": "100%",
		"billing_protocol_version": TaskBillingHistoricalProtocolVersion,
	}).Error)
	require.NoError(t, db.First(task, task.Id).Error)
	observed := *task
	observed.FailReason = "historical terminal failure"
	now := time.Unix(1_800_010_425, 0)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := FinalizeMidjourneyFailureWithOperation(
			context.Background(), &observed, "FAILURE", "mj_imagine", now.Add(time.Duration(attempt)*time.Second),
		)
		require.NoError(t, err)
		assert.Equal(t, TaskBillingOperationKindNoop, result.Operation.Kind)
		assert.Equal(t, TaskBillingOperationStateCompleted, result.Operation.State)
		assert.Zero(t, result.Operation.RefundQuota)
		assert.Contains(t, result.Operation.LastError, "charge state is ambiguous")
	}
	var count int64
	require.NoError(t, db.Model(&MidjourneyBillingOperation{}).Where("midjourney_id = ?", task.Id).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var user User
	require.NoError(t, db.First(&user, 408).Error)
	assert.Equal(t, 900, user.Quota)
}

func TestMidjourneyHistoricalVersionZeroSuccessCreatesAuditNoop(t *testing.T) {
	db := setupMidjourneyBillingOperationTest(t)
	task := insertMidjourneyBillingFixture(t, db, 409, 900, 900, 100, TaskBillingSourceWallet, 0)
	task.BillingProtocolVersion = TaskBillingHistoricalProtocolVersion
	require.NoError(t, db.Model(&Midjourney{}).Where("id = ?", task.Id).
		Update("billing_protocol_version", TaskBillingHistoricalProtocolVersion).Error)
	observed := *task
	observed.Status = "SUCCESS"
	observed.Progress = "100%"
	observed.FinishTime = time.Unix(1_800_010_430, 0).UnixMilli()
	now := time.Unix(1_800_010_430, 0)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := FinalizeMidjourneySuccessWithOperation(
			context.Background(), &observed, task.Status, now.Add(time.Duration(attempt)*time.Second),
		)
		require.NoError(t, err)
		assert.Equal(t, TaskBillingOperationKindNoop, result.Operation.Kind)
		assert.Equal(t, TaskBillingOperationStateCompleted, result.Operation.State)
		assert.Zero(t, result.Operation.RefundQuota)
	}
	var count int64
	require.NoError(t, db.Model(&MidjourneyBillingOperation{}).Where("midjourney_id = ?", task.Id).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var user User
	var token Token
	require.NoError(t, db.First(&user, 409).Error)
	require.NoError(t, db.First(&token, 409).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Equal(t, 100, token.UsedQuota)
}

func TestMidjourneyV2ZeroQuotaFailureCompletesReservationAndRepairsReplay(t *testing.T) {
	db := setupMidjourneyBillingOperationTest(t)
	task := insertMidjourneyBillingFixture(t, db, 407, 900, 900, 0, TaskBillingSourceWallet, 0)
	require.NoError(t, db.Model(&Midjourney{}).Where("id = ?", task.Id).Updates(map[string]any{
		"quota": 0, "durable_quota": 0, "billing_protocol_version": TaskBillingProtocolVersion,
		"async_billing_reservation_id": int64(407),
	}).Error)
	require.NoError(t, db.First(task, task.Id).Error)
	now := time.Unix(1_800_010_450, 0)
	require.NoError(t, db.Create(&AsyncBillingReservation{
		ID: 407, ReservationKey: "midjourney-zero-quota-failure", ProtocolVersion: TaskBillingProtocolVersion,
		Kind: AsyncBillingKindMidjourney, PublicTaskID: task.MjId, State: AsyncBillingReservationStateAccepted,
		UserID: 407, TokenID: 407, FundingSource: TaskBillingSourceWallet, MidjourneyID: task.Id,
		CreatedTimeMs: now.Add(-time.Minute).UnixMilli(), UpdatedTimeMs: now.Add(-time.Minute).UnixMilli(),
	}).Error)

	observed := *task
	observed.Status = "FAILURE"
	observed.Progress = "100%"
	observed.FailReason = "zero quota upstream failure"
	result, err := FinalizeMidjourneyFailureWithOperation(
		context.Background(), &observed, task.Status, "mj_imagine", now,
	)
	require.NoError(t, err)
	assert.True(t, result.Transitioned)
	assert.Equal(t, TaskBillingOperationKindNoop, result.Operation.Kind)
	assert.Equal(t, TaskBillingOperationStateCompleted, result.Operation.State)
	assert.Zero(t, result.Operation.RefundQuota)

	var reservation AsyncBillingReservation
	require.NoError(t, db.First(&reservation, 407).Error)
	assert.Equal(t, AsyncBillingReservationStateTerminal, reservation.State)
	assert.Equal(t, now.UnixMilli(), reservation.TerminalTimeMs)
	assert.True(t, reservation.CacheSyncPending)

	// Recreate the historical drift left by the old zero-quota noop path. A
	// replay must repair the reservation without creating another operation.
	require.NoError(t, db.Model(&AsyncBillingReservation{}).Where("id = ?", 407).Updates(map[string]any{
		"state": AsyncBillingReservationStateAccepted, "terminal_time_ms": 0, "cache_sync_pending": false,
	}).Error)
	replayed, err := FinalizeMidjourneyFailureWithOperation(
		context.Background(), &observed, "FAILURE", "mj_imagine", now.Add(time.Second),
	)
	require.NoError(t, err)
	assert.False(t, replayed.Transitioned)
	require.NoError(t, db.First(&reservation, 407).Error)
	assert.Equal(t, AsyncBillingReservationStateTerminal, reservation.State)
	assert.Equal(t, now.Add(time.Second).UnixMilli(), reservation.TerminalTimeMs)
	assert.True(t, reservation.CacheSyncPending)
	var operationCount int64
	require.NoError(t, db.Model(&MidjourneyBillingOperation{}).
		Where("midjourney_id = ?", task.Id).Count(&operationCount).Error)
	assert.Equal(t, int64(1), operationCount)
	drifts, err := FindAcceptedAsyncBillingTerminalDrifts(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, drifts)
}

func TestMidjourneyBillingOperationLogFailureRecoversWithoutRefundReplay(t *testing.T) {
	db := setupMidjourneyBillingOperationTest(t)
	task := insertMidjourneyBillingFixture(t, db, 406, 900, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_010_500, 0)
	operation := finalizeMidjourneyBillingFixture(t, task, now)
	_, claimed, err := ClaimMidjourneyBillingOperation(context.Background(), operation.ID, "billing-worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = CompleteMidjourneyBillingOperation(context.Background(), operation.ID, "billing-worker", now.Add(time.Second))
	require.NoError(t, err)

	originalLogDB := LOG_DB
	LOG_DB = nil
	err = RecordMidjourneyBillingOperationLog(context.Background(), operation.ID, "log-worker-a", now.Add(2*time.Second), time.Minute)
	LOG_DB = originalLogDB
	require.Error(t, err)
	failed, err := GetMidjourneyBillingOperation(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationLogFailed, failed.LogState)
	assert.Equal(t, now.Add(2*time.Second+taskBillingLogRetryDelay).UnixMilli(), failed.LogNextRetryMs)
	assert.False(t, hasPendingMidjourneyBillingOperationsAt(now.Add(3*time.Second)))

	var userBefore User
	var tokenBefore Token
	require.NoError(t, db.First(&userBefore, 406).Error)
	require.NoError(t, db.First(&tokenBefore, 406).Error)
	claimedLog, claimed, err := ClaimNextMidjourneyBillingOperationLog(context.Background(), "log-worker-b", now.Add(3*time.Second), time.Minute)
	require.NoError(t, err)
	assert.False(t, claimed, "a failed log must not be reclaimed before its retry deadline")
	assert.Nil(t, claimedLog)
	retryAt := time.UnixMilli(failed.LogNextRetryMs)
	assert.True(t, hasPendingMidjourneyBillingOperationsAt(retryAt))
	claimedLog, claimed, err = ClaimNextMidjourneyBillingOperationLog(context.Background(), "log-worker-b", retryAt, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, RecordClaimedMidjourneyBillingOperationLog(context.Background(), claimedLog.ID, "log-worker-b", retryAt))
	var userAfter User
	var tokenAfter Token
	require.NoError(t, db.First(&userAfter, 406).Error)
	require.NoError(t, db.First(&tokenAfter, 406).Error)
	assert.Equal(t, userBefore.Quota, userAfter.Quota)
	assert.Equal(t, tokenBefore.RemainQuota, tokenAfter.RemainQuota)
	var logCount int64
	require.NoError(t, db.Model(&Log{}).Where("billing_operation_key = ?", operation.OperationKey).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
	assert.False(t, hasPendingMidjourneyBillingOperationsAt(retryAt.Add(time.Second)))
}

func TestMidjourneyBillingOperationExternalDatabaseCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		dbType common.DatabaseType
	}{
		{name: "mysql", envKey: "ROUTING_TEST_MYSQL_DSN", dbType: common.DatabaseTypeMySQL},
		{name: "postgres", envKey: "ROUTING_TEST_POSTGRES_DSN", dbType: common.DatabaseTypePostgreSQL},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn := os.Getenv(test.envKey)
			if dsn == "" {
				t.Skipf("%s is not set", test.envKey)
			}
			db := openRoutingExternalTestDB(t, test.dbType, dsn)
			models := []interface{}{
				&Log{},
				&MidjourneyBillingOperation{},
				&Midjourney{},
				&Token{},
				&UserSubscription{},
				&Channel{},
				&User{},
			}
			for _, databaseModel := range models {
				if db.Migrator().HasTable(databaseModel) {
					t.Skipf("refusing to use non-empty external database: %T already exists", databaseModel)
				}
			}
			withRoutingTestDB(t, db, test.dbType)
			t.Cleanup(func() {
				for _, databaseModel := range models {
					_ = db.Migrator().DropTable(databaseModel)
				}
			})

			require.NoError(t, db.AutoMigrate(models...))
			require.NoError(t, db.AutoMigrate(models...), "Midjourney billing migration must be idempotent")

			now := time.Unix(1_800_020_000, 0)
			walletTask := insertMidjourneyBillingFixture(t, db, 451, 900, 900, 100, TaskBillingSourceWallet, 0)
			require.NoError(t, db.Delete(&Token{}, 451).Error)
			walletOperation := finalizeMidjourneyBillingFixture(t, walletTask, now)
			_, claimed, err := ClaimMidjourneyBillingOperation(context.Background(), walletOperation.ID, "external-worker-a", now, time.Minute)
			require.NoError(t, err)
			require.True(t, claimed)
			_, claimed, err = ClaimMidjourneyBillingOperation(context.Background(), walletOperation.ID, "external-worker-b", now.Add(61*time.Second), time.Minute)
			require.NoError(t, err)
			require.True(t, claimed, "an expired financial lease must be recoverable")
			completed, err := CompleteMidjourneyBillingOperation(context.Background(), walletOperation.ID, "external-worker-b", now.Add(62*time.Second))
			require.NoError(t, err)
			require.Equal(t, TaskBillingOperationStateCompleted, completed.State)
			_, err = CompleteMidjourneyBillingOperation(context.Background(), walletOperation.ID, "replay-worker", now.Add(5*time.Minute))
			require.NoError(t, err)
			logFailureAt := now.Add(63 * time.Second)
			LOG_DB = nil
			logErr := RecordMidjourneyBillingOperationLog(context.Background(), walletOperation.ID, "external-log-worker", logFailureAt, time.Minute)
			LOG_DB = db
			require.Error(t, logErr)
			failedLog, err := GetMidjourneyBillingOperation(context.Background(), walletOperation.ID)
			require.NoError(t, err)
			assert.Equal(t, logFailureAt.Add(taskBillingLogRetryDelay).UnixMilli(), failedLog.LogNextRetryMs)
			_, claimed, err = ClaimNextMidjourneyBillingOperationLog(context.Background(), "external-log-early", logFailureAt.Add(time.Second), time.Minute)
			require.NoError(t, err)
			assert.False(t, claimed)
			logRetryAt := time.UnixMilli(failedLog.LogNextRetryMs)
			claimedLog, claimed, err := ClaimNextMidjourneyBillingOperationLog(context.Background(), "external-log-retry", logRetryAt, time.Minute)
			require.NoError(t, err)
			require.True(t, claimed)
			require.NoError(t, RecordClaimedMidjourneyBillingOperationLog(context.Background(), claimedLog.ID, "external-log-retry", logRetryAt))
			require.NoError(t, RecordMidjourneyBillingOperationLog(context.Background(), walletOperation.ID, "external-log-replay", now.Add(3*time.Minute), time.Minute))

			var walletUser User
			var walletToken Token
			var walletPersisted Midjourney
			require.NoError(t, db.First(&walletUser, 451).Error)
			require.NoError(t, db.Unscoped().First(&walletToken, 451).Error)
			require.NoError(t, db.First(&walletPersisted, walletTask.Id).Error)
			assert.Equal(t, 1000, walletUser.Quota)
			assert.Equal(t, 1000, walletToken.RemainQuota)
			assert.Zero(t, walletToken.UsedQuota)
			assert.True(t, walletToken.DeletedAt.Valid)
			assert.Zero(t, walletPersisted.Quota)
			var walletLogCount int64
			require.NoError(t, db.Model(&Log{}).
				Where("billing_operation_key = ?", walletOperation.OperationKey).Count(&walletLogCount).Error)
			assert.Equal(t, int64(1), walletLogCount)

			subscriptionTask := insertMidjourneyBillingFixture(t, db, 452, 500, 900, 100, TaskBillingSourceSubscription, 452)
			subscriptionOperation := finalizeMidjourneyBillingFixture(t, subscriptionTask, now.Add(10*time.Minute))
			_, claimed, err = ClaimMidjourneyBillingOperation(context.Background(), subscriptionOperation.ID, "external-subscription-worker", now.Add(10*time.Minute), time.Minute)
			require.NoError(t, err)
			require.True(t, claimed)
			_, err = CompleteMidjourneyBillingOperation(context.Background(), subscriptionOperation.ID, "external-subscription-worker", now.Add(10*time.Minute+time.Second))
			require.NoError(t, err)
			var subscription UserSubscription
			var subscriptionUser User
			require.NoError(t, db.First(&subscription, 452).Error)
			require.NoError(t, db.First(&subscriptionUser, 452).Error)
			assert.Equal(t, int64(100), subscription.AmountUsed)
			assert.Equal(t, 500, subscriptionUser.Quota)

			rollbackTask := insertMidjourneyBillingFixture(t, db, 453, 900, 900, 50, TaskBillingSourceWallet, 0)
			rollbackOperation := finalizeMidjourneyBillingFixture(t, rollbackTask, now.Add(20*time.Minute))
			_, claimed, err = ClaimMidjourneyBillingOperation(context.Background(), rollbackOperation.ID, "external-rollback-worker", now.Add(20*time.Minute), time.Minute)
			require.NoError(t, err)
			require.True(t, claimed)
			_, err = CompleteMidjourneyBillingOperation(context.Background(), rollbackOperation.ID, "external-rollback-worker", now.Add(20*time.Minute+time.Second))
			assert.ErrorIs(t, err, ErrMidjourneyBillingOperationInvariant)
			var rollbackUser User
			var rollbackToken Token
			var rollbackPersisted Midjourney
			require.NoError(t, db.First(&rollbackUser, 453).Error)
			require.NoError(t, db.First(&rollbackToken, 453).Error)
			require.NoError(t, db.First(&rollbackPersisted, rollbackTask.Id).Error)
			assert.Equal(t, 900, rollbackUser.Quota)
			assert.Equal(t, 900, rollbackToken.RemainQuota)
			assert.Equal(t, 50, rollbackToken.UsedQuota)
			assert.Equal(t, 100, rollbackPersisted.Quota)

			concurrentTask := insertMidjourneyBillingFixture(t, db, 454, 900, 900, 100, TaskBillingSourceWallet, 0)
			const workers = 8
			results := make(chan *MidjourneyFailureFinalization, workers)
			errs := make(chan error, workers)
			var wait sync.WaitGroup
			for index := 0; index < workers; index++ {
				wait.Add(1)
				go func(offset int) {
					defer wait.Done()
					observed := *concurrentTask
					observed.Status = "FAILURE"
					observed.Progress = "100%"
					observed.FailReason = fmt.Sprintf("external-failure-%d", offset)
					result, finalizeErr := FinalizeMidjourneyFailureWithOperation(
						context.Background(), &observed, concurrentTask.Status, "mj_imagine", now.Add(30*time.Minute+time.Duration(offset)*time.Millisecond),
					)
					if finalizeErr != nil {
						errs <- finalizeErr
						return
					}
					results <- result
				}(index)
			}
			wait.Wait()
			close(results)
			close(errs)
			for finalizeErr := range errs {
				require.NoError(t, finalizeErr)
			}
			winners := 0
			for result := range results {
				if result.Transitioned {
					winners++
				}
			}
			assert.Equal(t, 1, winners)
			var operationCount int64
			require.NoError(t, db.Model(&MidjourneyBillingOperation{}).Where("midjourney_id = ?", concurrentTask.Id).Count(&operationCount).Error)
			assert.Equal(t, int64(1), operationCount)
		})
	}
}
