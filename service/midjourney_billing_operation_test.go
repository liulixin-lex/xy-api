package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prepareMidjourneyBillingOperationServiceTest(t *testing.T) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.Midjourney{}, &model.MidjourneyBillingOperation{}))
	require.NoError(t, model.DB.Exec("DELETE FROM midjourney_billing_operations").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM midjourneys").Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Exec("DELETE FROM midjourney_billing_operations").Error)
		require.NoError(t, model.DB.Exec("DELETE FROM midjourneys").Error)
	})
}

func insertMidjourneyBillingServiceFixture(t *testing.T, id int, walletQuota int, tokenUsed int) *model.Midjourney {
	t.Helper()
	seedUser(t, id, walletQuota)
	seedToken(t, id, id, fmt.Sprintf("sk-midjourney-service-%d", id), 900)
	seedChannel(t, id)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", id).Update("used_quota", tokenUsed).Error)
	task := &model.Midjourney{
		UserId:                 id,
		Action:                 "IMAGINE",
		MjId:                   fmt.Sprintf("task_midjourney_service_%d", id),
		Status:                 "IN_PROGRESS",
		Progress:               "50%",
		ChannelId:              id,
		Quota:                  100,
		Group:                  "default",
		BillingSource:          BillingSourceWallet,
		BillingProtocolVersion: model.TaskBillingLegacyProtocolVersion,
		TokenId:                id,
	}
	require.NoError(t, model.DB.Create(task).Error)
	return task
}

func TestHistoricalVersionZeroMidjourneyFailurePreservesAmbiguousCharge(t *testing.T) {
	truncate(t)
	prepareMidjourneyBillingOperationServiceTest(t)
	const id = 503
	task := insertMidjourneyBillingServiceFixture(t, id, 900, 100)
	task.BillingProtocolVersion = model.TaskBillingHistoricalProtocolVersion
	require.NoError(t, model.DB.Model(&model.Midjourney{}).Where("id = ?", task.Id).
		Update("billing_protocol_version", model.TaskBillingHistoricalProtocolVersion).Error)

	now := time.Unix(1_800_020_200, 0)
	finalized, err := finalizeMidjourneyFailureAt(
		context.Background(), observedMidjourneyFailure(task, "historical upstream failed", now), task.Status, now,
	)
	require.NoError(t, err)
	require.True(t, finalized.Transitioned)
	assert.Equal(t, model.TaskBillingOperationKindNoop, finalized.Operation.Kind)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, finalized.Operation.State)
	assert.Zero(t, finalized.Operation.RefundQuota)
	assert.Contains(t, finalized.Operation.LastError, "charge state is ambiguous")
	assert.Equal(t, 900, getUserQuota(t, id))
	assert.Equal(t, 900, getTokenRemainQuota(t, id))
	assert.Equal(t, 100, getTokenUsedQuota(t, id))

	replayed, err := processMidjourneyBillingOperationAt(
		context.Background(), finalized.Operation.ID, "historical-worker", now.Add(time.Second), time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, replayed.State)
	assert.Equal(t, 900, getUserQuota(t, id))
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("billing_operation_key = ?", finalized.Operation.OperationKey).Count(&logCount).Error)
	assert.Zero(t, logCount)
}

func TestHistoricalVersionZeroMidjourneyUnchargedShapesNeverCredit(t *testing.T) {
	tests := []struct {
		name   string
		id     int
		code   int
		action string
		mjID   string
	}{
		{name: "rejected submission", id: 504, code: 24, action: constant.MjActionImagine},
		{name: "legacy exempt action", id: 505, code: 1, action: constant.MjActionInPaint, mjID: "legacy-inpaint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncate(t)
			prepareMidjourneyBillingOperationServiceTest(t)
			seedUser(t, test.id, 1000)
			seedToken(t, test.id, test.id, fmt.Sprintf("sk-midjourney-v0-uncharged-%d", test.id), 1000)
			seedChannel(t, test.id)
			task := &model.Midjourney{
				UserId: test.id, Code: test.code, Action: test.action, MjId: test.mjID,
				Status: "IN_PROGRESS", Progress: "0%", ChannelId: test.id, Quota: 100,
				BillingProtocolVersion: model.TaskBillingHistoricalProtocolVersion,
			}
			require.NoError(t, model.DB.Create(task).Error)
			now := time.Unix(1_800_020_300+int64(test.id), 0)

			finalized, err := finalizeMidjourneyFailureAt(
				context.Background(), observedMidjourneyFailure(task, "historical failure", now), task.Status, now,
			)
			require.NoError(t, err)
			assert.Equal(t, model.TaskBillingOperationKindNoop, finalized.Operation.Kind)
			assert.Equal(t, model.TaskBillingOperationStateCompleted, finalized.Operation.State)
			assert.Zero(t, finalized.Operation.RefundQuota)
			assert.Equal(t, 1000, getUserQuota(t, test.id))
			assert.Equal(t, 1000, getTokenRemainQuota(t, test.id))
			assert.Zero(t, getTokenUsedQuota(t, test.id))
		})
	}
}

func observedMidjourneyFailure(task *model.Midjourney, reason string, now time.Time) *model.Midjourney {
	observed := *task
	observed.Code = 4
	observed.Description = reason
	observed.FailReason = reason
	observed.Status = "FAILURE"
	observed.Progress = "100%"
	observed.FinishTime = now.UnixMilli()
	return &observed
}

func TestMidjourneyBillingServiceFinalizesProcessesAndReplays(t *testing.T) {
	truncate(t)
	prepareMidjourneyBillingOperationServiceTest(t)
	const id = 501
	task := insertMidjourneyBillingServiceFixture(t, id, 900, 100)
	now := time.Unix(1_800_020_000, 0)
	finalized, err := finalizeMidjourneyFailureAt(
		context.Background(), observedMidjourneyFailure(task, "upstream failed", now), task.Status, now,
	)
	require.NoError(t, err)
	require.True(t, finalized.Transitioned)
	assert.Equal(t, model.TaskBillingOperationStatePending, finalized.Operation.State)
	assert.Equal(t, 100, finalized.Task.Quota)

	completed, err := processMidjourneyBillingOperationAt(
		context.Background(), finalized.Operation.ID, "worker-a", now.Add(time.Second), time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, completed.State)
	assert.Equal(t, model.TaskBillingOperationLogWritten, completed.LogState)
	assert.Equal(t, 1000, getUserQuota(t, id))
	assert.Equal(t, 1000, getTokenRemainQuota(t, id))
	assert.Equal(t, 0, getTokenUsedQuota(t, id))
	var persisted model.Midjourney
	require.NoError(t, model.DB.First(&persisted, task.Id).Error)
	assert.Zero(t, persisted.Quota)

	replayed, err := processMidjourneyBillingOperationAt(
		context.Background(), completed.ID, "worker-b", now.Add(2*time.Second), time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, replayed.State)
	assert.Equal(t, 1000, getUserQuota(t, id))
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("billing_operation_key = ?", completed.OperationKey).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
}

func TestMidjourneyBillingServiceFailureReturnsPendingAndProcessNextRecovers(t *testing.T) {
	truncate(t)
	prepareMidjourneyBillingOperationServiceTest(t)
	const id = 502
	task := insertMidjourneyBillingServiceFixture(t, id, 900, 50)
	now := time.Unix(1_800_020_100, 0)
	finalized, err := finalizeMidjourneyFailureAt(
		context.Background(), observedMidjourneyFailure(task, "upstream failed", now), task.Status, now,
	)
	require.NoError(t, err)
	_, err = processMidjourneyBillingOperationAt(
		context.Background(), finalized.Operation.ID, "worker-a", now.Add(time.Second), time.Minute,
	)
	assert.ErrorIs(t, err, model.ErrMidjourneyBillingOperationInvariant)
	operation, err := model.GetMidjourneyBillingOperation(context.Background(), finalized.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStatePending, operation.State)
	assert.NotEmpty(t, operation.LastError)
	assert.Equal(t, 900, getUserQuota(t, id))

	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", id).Update("used_quota", 100).Error)
	require.NoError(t, model.DB.Model(&model.MidjourneyBillingOperation{}).Where("id = ?", operation.ID).
		Update("next_retry_time_ms", 0).Error)
	completed, processed, err := processNextMidjourneyBillingOperationAt(
		context.Background(), "worker-b", now.Add(2*time.Second), time.Minute,
	)
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, completed.State)
	assert.Equal(t, 1000, getUserQuota(t, id))
	_, processed, err = processNextMidjourneyBillingOperationAt(
		context.Background(), "worker-b", now.Add(3*time.Second), time.Minute,
	)
	require.NoError(t, err)
	assert.False(t, processed)
}
