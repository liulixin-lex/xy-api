package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRunAsyncBillingRecoveryCountsOnlyCompletedProjections(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(
		&model.AsyncBillingReservation{},
		&model.AsyncBillingAttempt{},
		&model.AsyncBillingManualResolution{},
		&model.BillingStatsProjection{},
		&model.BillingLogProjection{},
		&model.QuotaData{},
	))
	for _, table := range []string{
		"async_billing_manual_resolutions",
		"async_billing_attempts",
		"async_billing_reservations",
		"billing_stats_projections",
		"billing_log_projections",
		"quota_data",
	} {
		require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
	}
	t.Cleanup(func() {
		for _, table := range []string{
			"async_billing_manual_resolutions",
			"async_billing_attempts",
			"async_billing_reservations",
			"billing_stats_projections",
			"billing_log_projections",
			"quota_data",
		} {
			require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
		}
	})

	now := time.Now()
	require.NoError(t, model.DB.Create(&model.User{
		Id: 9971, Username: "recovery-user", Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 9971, Name: "recovery-channel", Key: "recovery-key", Status: common.ChannelStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		_, _, err := model.CreateBillingStatsProjectionTx(tx, model.BillingStatsProjectionSpec{
			OperationKey: "async:9971:accepted:v1", Kind: model.BillingStatsProjectionKindAccepted,
			ReferenceID: 9971, UserID: 9971, ChannelID: 9971, QuotaDelta: 11, RequestDelta: 1,
		}, now)
		return err
	}))

	var logProjection *model.BillingLogProjection
	require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		logProjection, _, err = model.CreateBillingLogProjectionTx(tx, model.BillingLogProjectionSpec{
			OperationKey: "async:9972:accepted:v1", Kind: model.BillingLogProjectionKindAccepted,
			ReferenceID: 9972, Required: true,
			Entry: &model.Log{
				UserId: 9971, CreatedAt: now.Unix(), Type: model.LogTypeConsume,
				ModelName: "recovery-model", Quota: 1, ChannelId: 9971, RequestId: "request-9972",
			},
		}, now)
		return err
	}))
	require.NotNil(t, logProjection)
	operationKey := logProjection.OperationKey
	require.NoError(t, model.LOG_DB.Create(&model.Log{
		CreatedAt: now.Unix(), BillingOperationKey: &operationKey,
		BillingPayloadHash: fmt.Sprintf("%064x", 1), BillingPayloadProtocol: logProjection.PayloadProtocol,
	}).Error)

	summary := RunAsyncBillingRecoveryOnceWithOwner(context.Background(), "recovery-worker")
	assert.Equal(t, 1, summary.StatsProjectionCompleted)
	assert.Zero(t, summary.LogProjectionCompleted, "a quarantined conflict is not a completed delivery")
	assert.Zero(t, summary.StatsProjectionFailedPending)
	assert.Equal(t, int64(1), summary.LogProjectionFailedPending)
	assert.Zero(t, summary.Errors)
}

func TestRunAsyncBillingRecoveryMovesUnverifiableTerminalUsageToManualReview(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(
		&model.AsyncBillingReservation{}, &model.AsyncBillingAttempt{},
		&model.AsyncBillingManualResolution{}, &model.TaskBillingOperation{},
		&model.BillingStatsProjection{}, &model.BillingLogProjection{},
	))
	for _, table := range []string{
		"async_billing_manual_resolutions", "async_billing_attempts", "async_billing_reservations",
		"task_billing_operations", "billing_stats_projections", "billing_log_projections",
		"tasks", "tokens", "users",
	} {
		require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
	}
	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = nil
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })

	now := time.Now()
	const userID = 9973
	require.NoError(t, model.DB.Create(&model.User{
		Id: userID, Username: "terminal-usage-recovery", AffCode: "terminal-usage-recovery",
		Status: common.UserStatusEnabled, Quota: 900,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		Id: userID, UserId: userID, Key: "sk-terminal-usage-recovery", Name: "terminal-usage-recovery",
		Status: common.TokenStatusEnabled, RemainQuota: 900, UsedQuota: 100, ExpiredTime: -1,
	}).Error)
	reservation := model.AsyncBillingReservation{
		ID: 9973, ReservationKey: "terminal-usage-recovery", ProtocolVersion: model.TaskBillingProtocolVersion,
		Kind: model.AsyncBillingKindTask, PublicTaskID: "task_terminal_usage_recovery",
		State: model.AsyncBillingReservationStateAccepted, UserID: userID, TokenID: userID,
		FundingSource: model.TaskBillingSourceWallet, InitialQuota: 100, CurrentQuota: 100, AcceptedQuota: 100,
		AcceptedProjectionState: model.AsyncBillingAcceptedProjectionCompleted,
		UpstreamTaskID:          "provider-terminal-usage-recovery",
		CreatedTimeMs:           now.Add(-time.Minute).UnixMilli(), UpdatedTimeMs: now.Add(-time.Minute).UnixMilli(),
	}
	require.NoError(t, model.DB.Create(&reservation).Error)
	task := model.Task{
		TaskID: reservation.PublicTaskID, Platform: constant.TaskPlatformSuno,
		UserId: userID, ChannelId: 9973, Status: model.TaskStatusSuccess, Progress: "100%",
		SubmitTime: now.Add(-time.Minute).Unix(), FinishTime: now.Unix(),
		Data: json.RawMessage(`{"usage":"unverifiable"}`),
		PrivateData: model.TaskPrivateData{
			BillingProtocolVersion:    model.TaskBillingProtocolVersion,
			AsyncBillingReservationID: reservation.ID,
			BillingSource:             model.TaskBillingSourceWallet, TokenId: userID,
			BillingContext: &model.TaskBillingContext{ModelRatio: 1, GroupRatio: 1},
		},
	}
	require.NoError(t, task.IsolateV2BillingFromLegacyPollers(100))
	require.NoError(t, model.DB.Create(&task).Error)
	require.NoError(t, model.DB.Model(&model.AsyncBillingReservation{}).Where("id = ?", reservation.ID).
		Update("task_id", task.ID).Error)

	summary := RunAsyncBillingRecoveryOnceWithOwner(context.Background(), "terminal-usage-recovery-worker")
	assert.Equal(t, 1, summary.ManualReviewMarked)
	assert.Zero(t, summary.Errors)
	var reviewed model.AsyncBillingReservation
	require.NoError(t, model.DB.First(&reviewed, reservation.ID).Error)
	assert.Equal(t, model.AsyncBillingReservationStateManualReview, reviewed.State)
	assert.Equal(t, model.AsyncBillingReviewKindTerminalUsage, reviewed.ManualReviewKind)
	assert.Equal(t, 100, reviewed.CurrentQuota)
	version := reviewed.ReviewVersion

	summary = RunAsyncBillingRecoveryOnceWithOwner(context.Background(), "terminal-usage-recovery-worker-retry")
	assert.Zero(t, summary.ManualReviewMarked)
	assert.Zero(t, summary.Errors)
	require.NoError(t, model.DB.First(&reviewed, reservation.ID).Error)
	assert.Equal(t, version, reviewed.ReviewVersion)
	var operationCount int64
	require.NoError(t, model.DB.Model(&model.TaskBillingOperation{}).
		Where("reservation_id = ?", reservation.ID).Count(&operationCount).Error)
	assert.Zero(t, operationCount)
}

func TestRunAsyncBillingRecoveryResetsReceiptCleanupCursorAtEmptyTail(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(
		&model.AsyncBillingReservation{}, &model.AsyncBillingAttempt{},
		&model.AsyncBillingManualResolution{}, &model.BillingStatsProjection{},
		&model.BillingLogProjection{}, &model.IdentityCacheSync{},
	))
	for _, table := range []string{
		"identity_cache_syncs", "async_billing_manual_resolutions", "async_billing_attempts",
		"async_billing_reservations", "billing_stats_projections", "billing_log_projections",
	} {
		require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
	}

	expiredAt := time.Now().Add(-31 * 24 * time.Hour)
	require.NoError(t, model.DB.Create(&model.AsyncBillingReservation{
		ID: 9974, ReservationKey: "receipt-cursor-empty-tail", ProtocolVersion: model.TaskBillingProtocolVersion,
		Kind: model.AsyncBillingKindTask, PublicTaskID: "receipt-cursor-empty-tail",
		State: model.AsyncBillingReservationStateReleased, UserID: 9974,
		FundingSource: model.TaskBillingSourceWallet, TerminalTimeMs: expiredAt.UnixMilli(),
		CreatedTimeMs: expiredAt.UnixMilli(), UpdatedTimeMs: expiredAt.UnixMilli(),
	}).Error)

	summary := RunAsyncBillingRecoveryOnceWithCursor(
		context.Background(), "receipt-cursor-worker",
		AsyncBillingRecoveryCursor{ReceiptCleanupAfterID: 9999},
	)
	assert.Zero(t, summary.Errors)
	assert.Zero(t, summary.ExpiredReceiptsDeleted)
	assert.Zero(t, summary.NextReceiptCleanupAfterID)
}

func TestRunAsyncBillingRecoveryProcessesDurableIdentityCacheSync(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(&model.IdentityCacheSync{}))
	require.NoError(t, model.DB.Exec("DELETE FROM identity_cache_syncs").Error)
	t.Cleanup(func() { require.NoError(t, model.DB.Exec("DELETE FROM identity_cache_syncs").Error) })

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousClient := common.RDB
	previousEnabled := common.RedisEnabled
	common.RDB = client
	common.RedisEnabled = true
	t.Cleanup(func() {
		_ = client.Close()
		common.RDB = previousClient
		common.RedisEnabled = previousEnabled
	})
	require.NoError(t, client.HSet(context.Background(), "user:9975", "Quota", 1000).Err())
	now := time.Now()
	require.NoError(t, model.DB.Create(&model.IdentityCacheSync{
		SubjectKey: "user:9975", EpochKey: "cache_epoch:user:9975", CacheKey: "user:9975",
		Version: 1, NextRetryMs: 0, CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(),
	}).Error)

	summary := RunAsyncBillingRecoveryOnceWithOwner(context.Background(), "identity-cache-recovery-worker")
	assert.Zero(t, summary.Errors)
	assert.Equal(t, 1, summary.IdentityCacheSyncCompleted)
	exists, err := client.Exists(context.Background(), "user:9975").Result()
	require.NoError(t, err)
	assert.Zero(t, exists)
	var pending int64
	require.NoError(t, model.DB.Model(&model.IdentityCacheSync{}).Count(&pending).Error)
	assert.Zero(t, pending)
}

func TestRunAsyncBillingRecoveryCancellationStopsBlockedSelectorWithoutMutation(t *testing.T) {
	truncate(t)
	require.NoError(t, model.DB.AutoMigrate(
		&model.AsyncBillingReservation{}, &model.AsyncBillingAttempt{},
		&model.AsyncBillingManualResolution{}, &model.BillingStatsProjection{},
		&model.BillingLogProjection{}, &model.IdentityCacheSync{}, &model.QuotaData{},
	))
	for _, table := range []string{
		"identity_cache_syncs", "async_billing_manual_resolutions", "async_billing_attempts",
		"async_billing_reservations", "billing_stats_projections", "billing_log_projections", "quota_data",
	} {
		require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
	}
	t.Cleanup(func() {
		for _, table := range []string{
			"identity_cache_syncs", "async_billing_manual_resolutions", "async_billing_attempts",
			"async_billing_reservations", "billing_stats_projections", "billing_log_projections", "quota_data",
		} {
			require.NoError(t, model.DB.Exec("DELETE FROM "+table).Error)
		}
	})

	const userID = 9976
	require.NoError(t, model.DB.Create(&model.User{
		Id: userID, Username: "canceled-recovery", AffCode: "canceled-recovery",
		Status: common.UserStatusEnabled, Quota: 900,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		Id: userID, UserId: userID, Key: "sk-canceled-recovery", Name: "canceled-recovery",
		Status: common.TokenStatusEnabled, RemainQuota: 900, UsedQuota: 100, ExpiredTime: -1,
	}).Error)
	now := time.Now()
	reservation := model.AsyncBillingReservation{
		ID: 9976, ReservationKey: "canceled-recovery", ProtocolVersion: model.TaskBillingProtocolVersion,
		Kind: model.AsyncBillingKindTask, PublicTaskID: "canceled_recovery",
		State: model.AsyncBillingReservationStateReserved, UserID: userID, TokenID: userID,
		FundingSource: model.TaskBillingSourceWallet, InitialQuota: 100, CurrentQuota: 100,
		CacheSyncVersion: 7, CacheSyncedVersion: 6, CacheSyncPending: true,
		CacheSyncAttempts: 2, CacheSyncNextRetryMs: now.Add(time.Minute).UnixMilli(),
		CacheSyncLastError: "retained", CreatedTimeMs: now.Add(-time.Hour).UnixMilli(),
		UpdatedTimeMs: now.Add(-time.Hour).UnixMilli(),
	}
	require.NoError(t, model.DB.Create(&reservation).Error)
	attempt := model.AsyncBillingAttempt{
		ReservationID: reservation.ID, AttemptIndex: 0, State: model.AsyncBillingAttemptStateRejected,
		ChannelID: 9976, AuthorizedMs: now.Add(-time.Hour).UnixMilli(),
		FailureCode: "retained",
	}
	require.NoError(t, model.DB.Create(&attempt).Error)

	queryStarted := make(chan struct{})
	releaseQuery := make(chan struct{})
	var blockOnce sync.Once
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseQuery) }) }
	t.Cleanup(release)
	const callbackName = "test:block_async_billing_recovery_selector"
	require.NoError(t, model.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "async_billing_reservations" {
			return
		}
		blockOnce.Do(func() {
			close(queryStarted)
			select {
			case <-tx.Statement.Context.Done():
				tx.AddError(tx.Statement.Context.Err())
			case <-releaseQuery:
				tx.AddError(context.Canceled)
			}
		})
	}))
	t.Cleanup(func() { _ = model.DB.Callback().Query().Remove(callbackName) })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan AsyncBillingRecoverySummary, 1)
	go func() {
		result <- RunAsyncBillingRecoveryOnceWithOwner(ctx, "canceled-recovery-worker")
	}()
	select {
	case <-queryStarted:
	case <-time.After(time.Second):
		t.Fatal("recovery did not reach the blocked reservation selector")
	}
	cancel()
	var summary AsyncBillingRecoverySummary
	select {
	case summary = <-result:
	case <-time.After(time.Second):
		t.Fatal("recovery did not stop after context cancellation")
	}
	release()
	require.NoError(t, model.DB.Callback().Query().Remove(callbackName))

	assert.Zero(t, summary.Errors)
	assert.Zero(t, summary.ReleasedReservations)
	var persistedReservation model.AsyncBillingReservation
	require.NoError(t, model.DB.First(&persistedReservation, reservation.ID).Error)
	assert.Equal(t, model.AsyncBillingReservationStateReserved, persistedReservation.State)
	assert.Equal(t, 100, persistedReservation.CurrentQuota)
	assert.Equal(t, int64(7), persistedReservation.CacheSyncVersion)
	assert.Equal(t, int64(6), persistedReservation.CacheSyncedVersion)
	assert.True(t, persistedReservation.CacheSyncPending)
	assert.Equal(t, 2, persistedReservation.CacheSyncAttempts)
	assert.Equal(t, reservation.CacheSyncNextRetryMs, persistedReservation.CacheSyncNextRetryMs)
	assert.Equal(t, "retained", persistedReservation.CacheSyncLastError)
	var persistedAttempt model.AsyncBillingAttempt
	require.NoError(t, model.DB.First(&persistedAttempt, attempt.ID).Error)
	assert.Equal(t, model.AsyncBillingAttemptStateRejected, persistedAttempt.State)
	assert.Equal(t, "retained", persistedAttempt.FailureCode)
	var persistedUser model.User
	require.NoError(t, model.DB.First(&persistedUser, userID).Error)
	assert.Equal(t, 900, persistedUser.Quota)
	var persistedToken model.Token
	require.NoError(t, model.DB.First(&persistedToken, userID).Error)
	assert.Equal(t, 900, persistedToken.RemainQuota)
	assert.Equal(t, 100, persistedToken.UsedQuota)
}
