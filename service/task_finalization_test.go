package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func prepareTaskFinalizationTest(t *testing.T) {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.TaskBillingOperation{}))
	require.NoError(t, model.DB.Exec("DELETE FROM task_billing_operations").Error)
	t.Cleanup(func() {
		require.NoError(t, model.DB.Exec("DELETE FROM task_billing_operations").Error)
	})
}

func insertTaskFinalizationTask(t *testing.T, id int, quota int) *model.Task {
	t.Helper()
	task := makeTask(id, id, quota, id, BillingSourceWallet, 0)
	task.TaskID = "task_finalization_" + time.Unix(int64(id), 0).Format("150405")
	task.PrivateData.BillingContext = &model.TaskBillingContext{
		ModelRatio:      2,
		GroupRatio:      3,
		OtherRatios:     map[string]float64{"duration": 4},
		OriginModelName: "snapshot-model",
	}
	task.PrivateData.BillingProtocolVersion = model.TaskBillingLegacyProtocolVersion
	require.NoError(t, model.DB.Create(task).Error)
	return task
}

func TestFinalizeTaskObservationUsesFrozenSnapshotAndExplicitZero(t *testing.T) {
	tests := []struct {
		name         string
		id           int
		status       model.TaskStatus
		configure    func(*model.Task, *TaskFinalizationObservation)
		expected     int
		expectedKind string
	}{
		{
			name:   "frozen token ratios",
			id:     301,
			status: model.TaskStatusSuccess,
			configure: func(_ *model.Task, observation *TaskFinalizationObservation) {
				observation.TotalTokens = 5
			},
			expected:     120,
			expectedKind: model.TaskBillingOperationKindSettle,
		},
		{
			name:   "explicit zero is preserved",
			id:     302,
			status: model.TaskStatusSuccess,
			configure: func(_ *model.Task, observation *TaskFinalizationObservation) {
				zero := 0
				observation.ActualQuota = &zero
			},
			expected:     0,
			expectedKind: model.TaskBillingOperationKindSettle,
		},
		{
			name:   "per call ignores completion adjustment",
			id:     303,
			status: model.TaskStatusSuccess,
			configure: func(task *model.Task, observation *TaskFinalizationObservation) {
				task.PrivateData.BillingContext.PerCallBilling = true
				require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).
					Update("private_data", task.PrivateData).Error)
				actual := 999
				observation.ActualQuota = &actual
			},
			expected:     100,
			expectedKind: model.TaskBillingOperationKindNoop,
		},
		{
			name:   "failure always refunds",
			id:     304,
			status: model.TaskStatusFailure,
			configure: func(_ *model.Task, observation *TaskFinalizationObservation) {
				actual := 999
				observation.ActualQuota = &actual
			},
			expected:     0,
			expectedKind: model.TaskBillingOperationKindRefund,
		},
		{
			name:   "historical task without snapshot preserves precharge",
			id:     305,
			status: model.TaskStatusSuccess,
			configure: func(task *model.Task, observation *TaskFinalizationObservation) {
				task.PrivateData.BillingContext = nil
				require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).
					Update("private_data", task.PrivateData).Error)
				observation.TotalTokens = 500
			},
			expected:     100,
			expectedKind: model.TaskBillingOperationKindNoop,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncate(t)
			prepareTaskFinalizationTest(t)
			task := insertTaskFinalizationTask(t, test.id, 100)
			observation := TaskFinalizationObservation{
				TaskID:            task.ID,
				TerminalStatus:    test.status,
				Progress:          "100%",
				FinishTime:        int64(test.id),
				UpstreamResultURL: "https://result.invalid/video.mp4",
				Data:              json.RawMessage(`{"redacted":true}`),
			}
			test.configure(task, &observation)
			result, err := finalizeTaskObservationAt(context.Background(), observation, time.Unix(int64(test.id), 0))
			require.NoError(t, err)
			require.True(t, result.Transitioned)
			assert.Equal(t, test.expected, result.Operation.TargetQuota)
			assert.Equal(t, test.expectedKind, result.Operation.Kind)
			assert.Equal(t, model.TaskBillingOperationStatePending, result.Operation.State)
			assert.Equal(t, test.status, result.Task.Status)
			assert.Equal(t, "https://result.invalid/video.mp4", result.Task.PrivateData.UpstreamResultURL)
		})
	}
}

func TestTaskTerminalUsageReviewReasonDistinguishesExplicitZeroFromMissing(t *testing.T) {
	task := &model.Task{PrivateData: model.TaskPrivateData{
		BillingProtocolVersion:    model.TaskBillingProtocolVersion,
		AsyncBillingReservationID: 42,
		DurableBillingContext:     &model.TaskBillingContext{ModelRatio: 1, GroupRatio: 1},
	}}
	zero := 0
	assert.Empty(t, taskTerminalUsageReviewReason(task, TaskFinalizationObservation{
		TerminalStatus: model.TaskStatusSuccess,
		ActualQuota:    &zero,
	}))
	assert.NotEmpty(t, taskTerminalUsageReviewReason(task, TaskFinalizationObservation{
		TerminalStatus: model.TaskStatusSuccess,
	}))
}

func TestProcessTaskBillingOperationEndToEndAndReplay(t *testing.T) {
	truncate(t)
	prepareTaskFinalizationTest(t)
	const id = 306
	seedUser(t, id, 900)
	seedToken(t, id, id, "sk-finalization", 900)
	seedChannel(t, id)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", id).Update("used_quota", 100).Error)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", id).Update("used_quota", 20).Error)
	task := insertTaskFinalizationTask(t, id, 100)
	actual := 150
	now := time.Unix(1_800_001_000, 0)
	finalized, err := finalizeTaskObservationAt(context.Background(), TaskFinalizationObservation{
		TaskID:         task.ID,
		TerminalStatus: model.TaskStatusSuccess,
		ActualQuota:    &actual,
	}, now)
	require.NoError(t, err)

	completed, err := processTaskBillingOperationAt(context.Background(), finalized.Operation.ID, "worker-a", now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, completed.State)
	assert.Equal(t, model.TaskBillingOperationLogWritten, completed.LogState)
	assert.Equal(t, 850, getUserQuota(t, id))
	assert.Equal(t, 850, getTokenRemainQuota(t, id))
	assert.Equal(t, 150, getTokenUsedQuota(t, id))

	replayed, err := processTaskBillingOperationAt(context.Background(), completed.ID, "worker-b", now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, replayed.State)
	assert.Equal(t, 850, getUserQuota(t, id))
	assert.Equal(t, 850, getTokenRemainQuota(t, id))
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("billing_operation_key = ?", completed.OperationKey).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
}

func TestHistoricalVersionZeroTaskFailureRefundsOnce(t *testing.T) {
	truncate(t)
	prepareTaskFinalizationTest(t)
	const id = 316
	seedUser(t, id, 900)
	seedToken(t, id, id, "sk-finalization-v0", 900)
	seedChannel(t, id)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", id).Update("used_quota", 100).Error)

	task := makeTask(id, id, 100, id, BillingSourceWallet, 0)
	task.TaskID = "task_finalization_v0"
	require.Equal(t, model.TaskBillingHistoricalProtocolVersion, task.PrivateData.BillingProtocolVersion)
	require.NoError(t, model.DB.Create(task).Error)

	now := time.Unix(1_800_001_100, 0)
	finalized, err := finalizeTaskObservationAt(context.Background(), TaskFinalizationObservation{
		TaskID: task.ID, TerminalStatus: model.TaskStatusFailure,
		Progress: "100%", FinishTime: now.Unix(), FailReason: "historical upstream failure",
	}, now)
	require.NoError(t, err)
	require.True(t, finalized.Transitioned)
	assert.Equal(t, model.TaskBillingOperationKindRefund, finalized.Operation.Kind)
	assert.Equal(t, model.TaskBillingOperationStatePending, finalized.Operation.State)

	completed, err := processTaskBillingOperationAt(
		context.Background(), finalized.Operation.ID, "worker-v0-a", now.Add(time.Second), time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, completed.State)
	assert.Equal(t, 1000, getUserQuota(t, id))
	assert.Equal(t, 1000, getTokenRemainQuota(t, id))
	assert.Zero(t, getTokenUsedQuota(t, id))

	replayed, err := processTaskBillingOperationAt(
		context.Background(), completed.ID, "worker-v0-b", now.Add(2*time.Second), time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, replayed.State)
	assert.Equal(t, 1000, getUserQuota(t, id))
	assert.Equal(t, 1000, getTokenRemainQuota(t, id))
	var logCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).
		Where("billing_operation_key = ?", completed.OperationKey).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
}

func TestHistoricalVersionZeroTaskSuccessSettlesAndReplays(t *testing.T) {
	tests := []struct {
		name          string
		id            int
		actualQuota   int
		expectedQuota int
	}{
		{name: "additional charge", id: 317, actualQuota: 150, expectedQuota: 850},
		{name: "partial refund", id: 318, actualQuota: 50, expectedQuota: 950},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncate(t)
			prepareTaskFinalizationTest(t)
			seedUser(t, test.id, 900)
			seedToken(t, test.id, test.id, "sk-finalization-v0-success", 900)
			seedChannel(t, test.id)
			require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", test.id).Update("used_quota", 100).Error)

			task := makeTask(test.id, test.id, 100, test.id, BillingSourceWallet, 0)
			task.TaskID = "task_finalization_v0_success"
			require.NoError(t, model.DB.Create(task).Error)
			now := time.Unix(1_800_001_200+int64(test.id), 0)
			finalized, err := finalizeTaskObservationAt(context.Background(), TaskFinalizationObservation{
				TaskID: task.ID, TerminalStatus: model.TaskStatusSuccess, ActualQuota: &test.actualQuota,
			}, now)
			require.NoError(t, err)
			assert.Equal(t, model.TaskBillingOperationKindSettle, finalized.Operation.Kind)

			completed, err := processTaskBillingOperationAt(
				context.Background(), finalized.Operation.ID, "worker-v0-success-a", now.Add(time.Second), time.Minute,
			)
			require.NoError(t, err)
			assert.Equal(t, test.expectedQuota, getUserQuota(t, test.id))
			assert.Equal(t, test.expectedQuota, getTokenRemainQuota(t, test.id))
			assert.Equal(t, test.actualQuota, getTokenUsedQuota(t, test.id))

			_, err = processTaskBillingOperationAt(
				context.Background(), completed.ID, "worker-v0-success-b", now.Add(2*time.Second), time.Minute,
			)
			require.NoError(t, err)
			assert.Equal(t, test.expectedQuota, getUserQuota(t, test.id))
			assert.Equal(t, test.expectedQuota, getTokenRemainQuota(t, test.id))
		})
	}
}

func TestHistoricalVersionZeroSunoFailurePreservesAmbiguousCharge(t *testing.T) {
	truncate(t)
	prepareTaskFinalizationTest(t)
	const id = 319
	seedUser(t, id, 900)
	seedToken(t, id, id, "sk-finalization-v0-suno", 900)
	seedChannel(t, id)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", id).Update("used_quota", 100).Error)

	task := makeTask(id, id, 100, id, BillingSourceWallet, 0)
	task.TaskID = "task_finalization_v0_suno"
	task.Platform = constant.TaskPlatformSuno
	require.NoError(t, model.DB.Create(task).Error)
	now := time.Unix(1_800_001_300, 0)
	finalized, err := finalizeTaskObservationAt(context.Background(), TaskFinalizationObservation{
		TaskID: task.ID, TerminalStatus: model.TaskStatusFailure, FailReason: "historical Suno failed",
	}, now)
	require.NoError(t, err)
	require.True(t, finalized.Transitioned)
	assert.Equal(t, model.TaskBillingOperationKindNoop, finalized.Operation.Kind)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, finalized.Operation.State)
	assert.Contains(t, finalized.Operation.LastError, "refund state is ambiguous")
	assert.Equal(t, 900, getUserQuota(t, id))
	assert.Equal(t, 900, getTokenRemainQuota(t, id))
	assert.Equal(t, 100, getTokenUsedQuota(t, id))

	_, err = processTaskBillingOperationAt(
		context.Background(), finalized.Operation.ID, "worker-v0-suno", now.Add(time.Second), time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, 900, getUserQuota(t, id))
	var persisted model.Task
	require.NoError(t, model.DB.First(&persisted, task.ID).Error)
	assert.Equal(t, 100, persisted.Quota)
}

func TestProcessTaskBillingOperationFailureReturnsPendingThenRetries(t *testing.T) {
	truncate(t)
	prepareTaskFinalizationTest(t)
	const id = 307
	seedUser(t, id, 10)
	seedToken(t, id, id, "sk-finalization-retry", 900)
	seedChannel(t, id)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", id).Update("used_quota", 100).Error)
	task := insertTaskFinalizationTask(t, id, 100)
	actual := 150
	now := time.Unix(1_800_002_000, 0)
	finalized, err := finalizeTaskObservationAt(context.Background(), TaskFinalizationObservation{
		TaskID:         task.ID,
		TerminalStatus: model.TaskStatusSuccess,
		ActualQuota:    &actual,
	}, now)
	require.NoError(t, err)

	_, err = processTaskBillingOperationAt(context.Background(), finalized.Operation.ID, "worker-a", now.Add(time.Second), time.Minute)
	assert.ErrorIs(t, err, model.ErrTaskBillingOperationInsufficientQuota)
	operation, err := model.GetTaskBillingOperation(context.Background(), finalized.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStatePending, operation.State)
	assert.NotEmpty(t, operation.LastError)
	assert.Equal(t, 10, getUserQuota(t, id))
	assert.Equal(t, 900, getTokenRemainQuota(t, id))

	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", id).Update("quota", 100).Error)
	require.NoError(t, model.DB.Model(&model.TaskBillingOperation{}).Where("id = ?", operation.ID).
		Update("next_retry_time_ms", 0).Error)
	completed, err := processTaskBillingOperationAt(context.Background(), operation.ID, "worker-b", now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, completed.State)
	assert.Equal(t, 50, getUserQuota(t, id))
}

func TestProcessNextTaskBillingOperationLeavesNoopAuditable(t *testing.T) {
	truncate(t)
	prepareTaskFinalizationTest(t)
	const id = 308
	seedUser(t, id, 900)
	seedToken(t, id, id, "sk-finalization-next", 900)
	seedChannel(t, id)
	task := insertTaskFinalizationTask(t, id, 100)
	now := time.Unix(1_800_003_000, 0)
	finalized, err := finalizeTaskObservationAt(context.Background(), TaskFinalizationObservation{
		TaskID:         task.ID,
		TerminalStatus: model.TaskStatusSuccess,
	}, now)
	require.NoError(t, err)
	assert.Equal(t, model.TaskBillingOperationKindNoop, finalized.Operation.Kind)

	completed, processed, err := processNextTaskBillingOperationAt(context.Background(), "worker", now.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, completed.State)
	assert.Equal(t, model.TaskBillingOperationLogNotRequired, completed.LogState)
	assert.Equal(t, 900, getUserQuota(t, id))
	_, processed, err = processNextTaskBillingOperationAt(context.Background(), "worker", now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	assert.False(t, processed)
}

func TestProcessNextTaskBillingOperationRetriesOnlyCompletedLog(t *testing.T) {
	truncate(t)
	prepareTaskFinalizationTest(t)
	const id = 310
	seedUser(t, id, 900)
	seedToken(t, id, id, "sk-finalization-log-retry", 900)
	seedChannel(t, id)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", id).Update("used_quota", 100).Error)
	task := insertTaskFinalizationTask(t, id, 100)
	actual := 150
	now := time.Unix(1_800_004_000, 0)
	finalized, err := finalizeTaskObservationAt(context.Background(), TaskFinalizationObservation{
		TaskID:         task.ID,
		TerminalStatus: model.TaskStatusSuccess,
		ActualQuota:    &actual,
	}, now)
	require.NoError(t, err)
	_, claimed, err := model.ClaimTaskBillingOperation(context.Background(), finalized.Operation.ID, "billing-worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = model.CompleteTaskBillingOperation(context.Background(), finalized.Operation.ID, "billing-worker", now.Add(time.Second))
	require.NoError(t, err)

	userQuotaBefore := getUserQuota(t, id)
	tokenRemainBefore := getTokenRemainQuota(t, id)
	completed, processed, err := processNextTaskBillingOperationAt(context.Background(), "log-recovery-worker", now.Add(2*time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, model.TaskBillingOperationStateCompleted, completed.State)
	assert.Equal(t, model.TaskBillingOperationLogWritten, completed.LogState)
	assert.Equal(t, userQuotaBefore, getUserQuota(t, id))
	assert.Equal(t, tokenRemainBefore, getTokenRemainQuota(t, id))

	_, processed, err = processNextTaskBillingOperationAt(context.Background(), "log-recovery-worker", now.Add(3*time.Second), time.Minute)
	require.NoError(t, err)
	assert.False(t, processed)
}

func TestFinalizeTaskObservationRejectsInvalidPayloadWithoutTerminalMutation(t *testing.T) {
	truncate(t)
	prepareTaskFinalizationTest(t)
	task := insertTaskFinalizationTask(t, 309, 100)
	negative := -1
	_, err := FinalizeTaskObservation(context.Background(), TaskFinalizationObservation{
		TaskID:         task.ID,
		TerminalStatus: model.TaskStatusSuccess,
		ActualQuota:    &negative,
	})
	require.Error(t, err)
	var stored model.Task
	require.NoError(t, model.DB.First(&stored, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusInProgress), stored.Status)
	var operation model.TaskBillingOperation
	assert.ErrorIs(t, model.DB.Where("task_id = ?", task.ID).First(&operation).Error, gorm.ErrRecordNotFound)
}
