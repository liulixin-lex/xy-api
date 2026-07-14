package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prepareTaskBillingOperationTest(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&TaskBillingOperation{}))
	require.NoError(t, DB.Exec("DELETE FROM task_billing_operations").Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Exec("DELETE FROM task_billing_operations").Error)
	})
}

func insertTaskBillingFixture(
	t *testing.T,
	id int,
	quota int,
	walletQuota int,
	tokenRemain int,
	tokenUsed int,
	billingSource string,
	subscriptionID int,
) *Task {
	t.Helper()
	require.NoError(t, DB.Create(&User{
		Id:           id,
		Username:     fmt.Sprintf("task-billing-user-%d", id),
		AffCode:      fmt.Sprintf("task-billing-aff-%d", id),
		Quota:        walletQuota,
		UsedQuota:    10,
		RequestCount: 7,
		Status:       common.UserStatusEnabled,
	}).Error)
	require.NoError(t, DB.Create(&Channel{
		Id:        id,
		Name:      fmt.Sprintf("task-billing-channel-%d", id),
		Key:       fmt.Sprintf("sk-channel-%d", id),
		UsedQuota: 20,
		Status:    common.ChannelStatusEnabled,
	}).Error)
	require.NoError(t, DB.Create(&Token{
		Id:          id,
		UserId:      id,
		Key:         fmt.Sprintf("sk-task-billing-%d", id),
		Name:        fmt.Sprintf("task-billing-token-%d", id),
		Status:      common.TokenStatusEnabled,
		RemainQuota: tokenRemain,
		UsedQuota:   tokenUsed,
	}).Error)
	if subscriptionID > 0 {
		require.NoError(t, DB.Create(&UserSubscription{
			Id:          subscriptionID,
			UserId:      id,
			AmountTotal: 1000,
			AmountUsed:  100,
			Status:      "active",
		}).Error)
	}
	task := &Task{
		TaskID:    fmt.Sprintf("task_billing_%d", id),
		UserId:    id,
		ChannelId: id,
		Quota:     quota,
		Status:    TaskStatusInProgress,
		Progress:  "50%",
		Group:     "default",
		PrivateData: TaskPrivateData{
			BillingProtocolVersion: TaskBillingLegacyProtocolVersion,
			BillingSource:          billingSource,
			SubscriptionId:         subscriptionID,
			TokenId:                id,
			BillingContext: &TaskBillingContext{
				ModelRatio:      1,
				GroupRatio:      1,
				OriginModelName: "task-billing-model",
			},
		},
		Properties: Properties{OriginModelName: "task-billing-model"},
	}
	require.NoError(t, DB.Create(task).Error)
	return task
}

func finalizeTaskBillingFixture(t *testing.T, task *Task, status TaskStatus, target int, now time.Time) *TaskBillingOperation {
	t.Helper()
	result, err := FinalizeTaskWithBillingOperation(context.Background(), TaskTerminalUpdate{
		TaskID:         task.ID,
		TerminalStatus: status,
		Progress:       "100%",
		FinishTime:     now.Unix(),
	}, now, func(*Task) (TaskBillingOperationPlan, error) {
		return TaskBillingOperationPlan{TargetQuota: target}, nil
	})
	require.NoError(t, err)
	require.True(t, result.Transitioned)
	return &result.Operation
}

func TestTaskBillingOperationConcurrentTerminalCreatesOneDurableIntent(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 201, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_000_000, 0)

	const workers = 12
	results := make(chan *TaskBillingFinalization, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			var status TaskStatus = TaskStatusSuccess
			target := 140
			if index%2 == 1 {
				status = TaskStatusFailure
				target = 0
			}
			result, err := FinalizeTaskWithBillingOperation(context.Background(), TaskTerminalUpdate{
				TaskID:         task.ID,
				TerminalStatus: status,
			}, now.Add(time.Duration(index)*time.Millisecond), func(*Task) (TaskBillingOperationPlan, error) {
				return TaskBillingOperationPlan{TargetQuota: target}, nil
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
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
	var operationCount int64
	require.NoError(t, DB.Model(&TaskBillingOperation{}).Where("task_id = ?", task.ID).Count(&operationCount).Error)
	assert.Equal(t, int64(1), operationCount)
	var storedTask Task
	require.NoError(t, DB.First(&storedTask, task.ID).Error)
	operation, err := GetTaskBillingOperationByTaskID(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, storedTask.Status, operation.TerminalStatus)
	assert.Equal(t, int64(operation.TargetQuota-operation.PreConsumedQuota), int64(operation.QuotaDelta))
}

func TestTaskBillingOperationLeaseRecoveryAndCompletedReplay(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 202, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_000_100, 0)
	operation := finalizeTaskBillingFixture(t, task, TaskStatusSuccess, 150, now)

	claimed, won, err := ClaimTaskBillingOperation(context.Background(), operation.ID, "worker-a", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	assert.Equal(t, 1, claimed.Attempts)
	_, won, err = ClaimTaskBillingOperation(context.Background(), operation.ID, "worker-b", now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	assert.False(t, won)
	claimed, won, err = ClaimTaskBillingOperation(context.Background(), operation.ID, "worker-b", now.Add(61*time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	assert.Equal(t, 2, claimed.Attempts)

	_, err = CompleteTaskBillingOperation(context.Background(), operation.ID, "worker-a", now.Add(62*time.Second))
	assert.ErrorIs(t, err, ErrTaskBillingOperationNotClaimed)
	completed, err := CompleteTaskBillingOperation(context.Background(), operation.ID, "worker-b", now.Add(62*time.Second))
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationStateCompleted, completed.State)

	var beforeUser User
	var beforeToken Token
	require.NoError(t, DB.First(&beforeUser, 202).Error)
	require.NoError(t, DB.First(&beforeToken, 202).Error)
	replayed, err := CompleteTaskBillingOperation(context.Background(), operation.ID, "unrelated-worker", now.Add(5*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationStateCompleted, replayed.State)
	var afterUser User
	var afterToken Token
	require.NoError(t, DB.First(&afterUser, 202).Error)
	require.NoError(t, DB.First(&afterToken, 202).Error)
	assert.Equal(t, beforeUser.Quota, afterUser.Quota)
	assert.Equal(t, beforeUser.UsedQuota, afterUser.UsedQuota)
	assert.Equal(t, beforeToken.RemainQuota, afterToken.RemainQuota)
	assert.Equal(t, beforeToken.UsedQuota, afterToken.UsedQuota)
}

func TestTaskBillingOperationWalletTokenStatsAndLogAreIdempotent(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 203, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_000_200, 0)
	operation := finalizeTaskBillingFixture(t, task, TaskStatusSuccess, 150, now)
	_, won, err := ClaimTaskBillingOperation(context.Background(), operation.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = CompleteTaskBillingOperation(context.Background(), operation.ID, "worker", now.Add(time.Second))
	require.NoError(t, err)

	var user User
	var token Token
	var channel Channel
	var storedTask Task
	require.NoError(t, DB.First(&user, 203).Error)
	require.NoError(t, DB.First(&token, 203).Error)
	require.NoError(t, DB.First(&channel, 203).Error)
	require.NoError(t, DB.First(&storedTask, task.ID).Error)
	assert.Equal(t, 850, user.Quota)
	assert.Equal(t, 60, user.UsedQuota)
	assert.Equal(t, 7, user.RequestCount, "terminal settlement must not count a second logical request")
	assert.Equal(t, 850, token.RemainQuota)
	assert.Equal(t, 150, token.UsedQuota)
	assert.Equal(t, int64(70), channel.UsedQuota)
	assert.Equal(t, 150, storedTask.Quota)

	require.NoError(t, RecordTaskBillingOperationLog(context.Background(), operation.ID, "log-worker-a", now.Add(2*time.Second), time.Minute))
	require.NoError(t, RecordTaskBillingOperationLog(context.Background(), operation.ID, "log-worker-b", now.Add(3*time.Second), time.Minute))
	var logCount int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_key = ?", operation.OperationKey).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
	storedOperation, err := GetTaskBillingOperation(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationLogWritten, storedOperation.LogState)
}

func TestTaskBillingOperationFailureRefundsSoftDeletedToken(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 204, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	require.NoError(t, DB.Delete(&Token{}, 204).Error)
	now := time.Unix(1_800_000_300, 0)
	operation := finalizeTaskBillingFixture(t, task, TaskStatusFailure, 999, now)
	assert.Equal(t, 0, operation.TargetQuota)
	assert.Equal(t, TaskBillingOperationKindRefund, operation.Kind)
	_, won, err := ClaimTaskBillingOperation(context.Background(), operation.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = CompleteTaskBillingOperation(context.Background(), operation.ID, "worker", now.Add(time.Second))
	require.NoError(t, err)

	var user User
	var token Token
	require.NoError(t, DB.First(&user, 204).Error)
	require.NoError(t, DB.Unscoped().First(&token, 204).Error)
	assert.Equal(t, 1000, user.Quota)
	assert.Equal(t, 1000, token.RemainQuota)
	assert.Equal(t, 0, token.UsedQuota)
	assert.True(t, token.DeletedAt.Valid)
}

func TestTaskBillingOperationSubscriptionPositiveAndNegativeDelta(t *testing.T) {
	for _, test := range []struct {
		name              string
		id                int
		target            int
		expectedSubUsed   int64
		expectedRemain    int
		expectedTokenUsed int
		softDeleteToken   bool
	}{
		{name: "positive", id: 205, target: 150, expectedSubUsed: 150, expectedRemain: 850, expectedTokenUsed: 150},
		{name: "negative", id: 206, target: 50, expectedSubUsed: 50, expectedRemain: 950, expectedTokenUsed: 50},
		{name: "positive after token delete", id: 220, target: 150, expectedSubUsed: 150, expectedRemain: 850, expectedTokenUsed: 150, softDeleteToken: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			prepareTaskBillingOperationTest(t)
			task := insertTaskBillingFixture(t, test.id, 100, 500, 900, 100, TaskBillingSourceSubscription, test.id)
			if test.softDeleteToken {
				require.NoError(t, DB.Delete(&Token{}, test.id).Error)
			}
			now := time.Unix(1_800_000_400+int64(test.id), 0)
			operation := finalizeTaskBillingFixture(t, task, TaskStatusSuccess, test.target, now)
			_, won, err := ClaimTaskBillingOperation(context.Background(), operation.ID, "worker", now, time.Minute)
			require.NoError(t, err)
			require.True(t, won)
			_, err = CompleteTaskBillingOperation(context.Background(), operation.ID, "worker", now.Add(time.Second))
			require.NoError(t, err)

			var subscription UserSubscription
			var token Token
			var user User
			require.NoError(t, DB.First(&subscription, test.id).Error)
			require.NoError(t, DB.Unscoped().First(&token, test.id).Error)
			require.NoError(t, DB.First(&user, test.id).Error)
			assert.Equal(t, test.expectedSubUsed, subscription.AmountUsed)
			assert.Equal(t, test.expectedRemain, token.RemainQuota)
			assert.Equal(t, test.expectedTokenUsed, token.UsedQuota)
			assert.Equal(t, test.softDeleteToken, token.DeletedAt.Valid)
			assert.Equal(t, 500, user.Quota)
		})
	}
}

func TestTaskBillingOperationInsufficientQuotaRollsBackAndRetries(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 207, 100, 10, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_000_500, 0)
	operation := finalizeTaskBillingFixture(t, task, TaskStatusSuccess, 150, now)
	_, won, err := ClaimTaskBillingOperation(context.Background(), operation.ID, "worker-a", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = CompleteTaskBillingOperation(context.Background(), operation.ID, "worker-a", now.Add(time.Second))
	assert.ErrorIs(t, err, ErrTaskBillingOperationInsufficientQuota)

	var user User
	var token Token
	var storedTask Task
	require.NoError(t, DB.First(&user, 207).Error)
	require.NoError(t, DB.First(&token, 207).Error)
	require.NoError(t, DB.First(&storedTask, task.ID).Error)
	assert.Equal(t, 10, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
	assert.Equal(t, 100, token.UsedQuota)
	assert.Equal(t, 100, storedTask.Quota)

	retryAt := now.Add(10 * time.Second)
	retried, err := RetryTaskBillingOperation(context.Background(), operation.ID, "worker-a", now.Add(2*time.Second), retryAt, "secret sk-should-not-leak\ninsufficient")
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationStatePending, retried.State)
	assert.LessOrEqual(t, len(retried.LastError), maxTaskBillingLastErrorBytes)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 207).Update("quota", 100).Error)
	_, won, err = ClaimTaskBillingOperation(context.Background(), operation.ID, "worker-b", retryAt, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = CompleteTaskBillingOperation(context.Background(), operation.ID, "worker-b", retryAt.Add(time.Second))
	require.NoError(t, err)
}

func TestTaskBillingOperationCompletesWhenDerivedUserAndChannelRowsWereDeleted(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 221, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_000_550, 0)
	operation := finalizeTaskBillingFixture(t, task, TaskStatusSuccess, 150, now)
	require.NoError(t, DB.Delete(&User{}, 221).Error)
	require.NoError(t, DB.Delete(&Channel{}, 221).Error)
	_, won, err := ClaimTaskBillingOperation(context.Background(), operation.ID, "worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	completed, err := CompleteTaskBillingOperation(context.Background(), operation.ID, "worker", now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationStateCompleted, completed.State)
	assert.Equal(t, BillingUsageOutcomeSkippedDeleted, completed.UsageUserOutcome)
	assert.Equal(t, BillingUsageOutcomeSkippedMissing, completed.UsageChannelOutcome)
	assert.Contains(t, completed.UsageWarning, "user=skipped_deleted")
	assert.Contains(t, completed.UsageWarning, "channel=skipped_missing")

	var user User
	var token Token
	require.NoError(t, DB.Unscoped().First(&user, 221).Error)
	require.NoError(t, DB.First(&token, 221).Error)
	assert.True(t, user.DeletedAt.Valid)
	assert.Equal(t, 850, user.Quota)
	assert.Equal(t, 10, user.UsedQuota)
	assert.Equal(t, 850, token.RemainQuota)
	assert.Equal(t, 150, token.UsedQuota)
}

func TestTaskBillingOperationLegacyTerminalCreatesNoopMarker(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 208, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status":   TaskStatusSuccess,
		"progress": "100%",
	}).Error)
	now := time.Unix(1_800_000_600, 0)
	result, err := FinalizeTaskWithBillingOperation(context.Background(), TaskTerminalUpdate{
		TaskID:         task.ID,
		TerminalStatus: TaskStatusFailure,
	}, now, func(*Task) (TaskBillingOperationPlan, error) {
		return TaskBillingOperationPlan{}, errors.New("legacy terminal must not rebuild billing")
	})
	require.NoError(t, err)
	assert.False(t, result.Transitioned)
	assert.False(t, result.ObservationMatched)
	assert.Equal(t, TaskBillingOperationStateCompleted, result.Operation.State)
	assert.Equal(t, TaskBillingOperationKindNoop, result.Operation.Kind)
	assert.Equal(t, 100, result.Operation.TargetQuota)
	assert.Equal(t, 0, result.Operation.QuotaDelta)

	var user User
	var token Token
	require.NoError(t, DB.First(&user, 208).Error)
	require.NoError(t, DB.First(&token, 208).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
}

func TestTaskBillingOperationHistoricalVersionZeroTerminalCreatesSingleNoop(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 210, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	task.PrivateData.BillingProtocolVersion = TaskBillingHistoricalProtocolVersion
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": TaskStatusFailure, "progress": "100%", "private_data": task.PrivateData,
	}).Error)
	now := time.Unix(1_800_000_650, 0)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := FinalizeTaskWithBillingOperation(context.Background(), TaskTerminalUpdate{
			TaskID: task.ID, TerminalStatus: TaskStatusFailure,
		}, now.Add(time.Duration(attempt)*time.Second), func(*Task) (TaskBillingOperationPlan, error) {
			return TaskBillingOperationPlan{}, errors.New("historical terminal planner must not run")
		})
		require.NoError(t, err)
		assert.Equal(t, TaskBillingOperationKindNoop, result.Operation.Kind)
		assert.Equal(t, TaskBillingOperationStateCompleted, result.Operation.State)
	}
	var count int64
	require.NoError(t, DB.Model(&TaskBillingOperation{}).Where("task_id = ?", task.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var user User
	var token Token
	require.NoError(t, DB.First(&user, 210).Error)
	require.NoError(t, DB.First(&token, 210).Error)
	assert.Equal(t, 900, user.Quota)
	assert.Equal(t, 900, token.RemainQuota)
}

func TestTaskBillingOperationLegacyTerminalConcurrentMarkersConverge(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 209, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status":   TaskStatusFailure,
		"progress": "100%",
	}).Error)

	const workers = 8
	errCh := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(offset int) {
			defer wait.Done()
			_, err := FinalizeTaskWithBillingOperation(context.Background(), TaskTerminalUpdate{
				TaskID:         task.ID,
				TerminalStatus: TaskStatusFailure,
			}, time.Unix(1_800_000_700+int64(offset), 0), func(*Task) (TaskBillingOperationPlan, error) {
				return TaskBillingOperationPlan{}, errors.New("legacy planner must not run")
			})
			errCh <- err
		}(index)
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	var count int64
	require.NoError(t, DB.Model(&TaskBillingOperation{}).Where("task_id = ?", task.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	operation, err := GetTaskBillingOperationByTaskID(context.Background(), task.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationKindNoop, operation.Kind)
	assert.Equal(t, TaskBillingOperationStateCompleted, operation.State)
}

func TestTaskBillingOperationLogLeasePreventsConcurrentDuplicates(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 210, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_000_800, 0)
	operation := finalizeTaskBillingFixture(t, task, TaskStatusSuccess, 150, now)
	_, won, err := ClaimTaskBillingOperation(context.Background(), operation.ID, "billing-worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = CompleteTaskBillingOperation(context.Background(), operation.ID, "billing-worker", now.Add(time.Second))
	require.NoError(t, err)

	const workers = 10
	errCh := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(offset int) {
			defer wait.Done()
			errCh <- RecordTaskBillingOperationLog(
				context.Background(), operation.ID, fmt.Sprintf("log-worker-%d", offset), now.Add(2*time.Second), time.Minute,
			)
		}(index)
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			assert.ErrorIs(t, err, ErrTaskBillingOperationNotClaimed)
		}
	}
	var logCount int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_key = ?", operation.OperationKey).Count(&logCount).Error)
	assert.Equal(t, int64(1), logCount)
	stored, err := GetTaskBillingOperation(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationLogWritten, stored.LogState)
	assert.Equal(t, 1, stored.LogAttempts)
}

func TestTaskBillingOperationFreezesLogPayloadBeforeDelivery(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 214, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_001_100, 0)
	operation := finalizeTaskBillingFixture(t, task, TaskStatusSuccess, 150, now)
	require.Equal(t, billingLogPayloadProtocol, operation.LogPayloadProtocol)
	require.Len(t, operation.LogPayloadHash, billingLogPayloadHashEncodedBytes)
	require.NotEmpty(t, operation.LogPayload)

	require.NoError(t, DB.Model(&User{}).Where("id = ?", 214).Update("username", "renamed-user").Error)
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", 214).Update("name", "renamed-token").Error)
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).Update("group", "renamed-group").Error)

	_, claimed, err := ClaimTaskBillingOperation(context.Background(), operation.ID, "billing-worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = CompleteTaskBillingOperation(context.Background(), operation.ID, "billing-worker", now.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, RecordTaskBillingOperationLog(context.Background(), operation.ID, "log-worker", now.Add(2*time.Second), time.Minute))

	var stored Log
	require.NoError(t, LOG_DB.Where("billing_operation_key = ?", operation.OperationKey).First(&stored).Error)
	assert.Equal(t, "task-billing-user-214", stored.Username)
	assert.Equal(t, "task-billing-token-214", stored.TokenName)
	assert.Equal(t, "default", stored.Group)
	assert.Equal(t, now.Unix(), stored.CreatedAt)
}

func TestTaskBillingOperationAckLossReplayRepairsLogState(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 215, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_001_200, 0)
	operation := finalizeTaskBillingFixture(t, task, TaskStatusSuccess, 150, now)
	_, claimed, err := ClaimTaskBillingOperation(context.Background(), operation.ID, "billing-worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = CompleteTaskBillingOperation(context.Background(), operation.ID, "billing-worker", now.Add(time.Second))
	require.NoError(t, err)

	claimedLog, claimed, err := ClaimTaskBillingOperationLog(
		context.Background(), operation.ID, "crashed-after-write", now.Add(2*time.Second), time.Minute,
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, writeFrozenBillingLog(context.Background(), claimedLog.OperationKey, claimedLog.LogPayload,
		claimedLog.LogPayloadHash, claimedLog.LogPayloadProtocol))

	recovered, claimed, err := ClaimTaskBillingOperationLog(
		context.Background(), operation.ID, "replay-worker", now.Add(63*time.Second), time.Minute,
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, RecordClaimedTaskBillingOperationLog(
		context.Background(), recovered.ID, "replay-worker", now.Add(63*time.Second),
	))
	storedOperation, err := GetTaskBillingOperation(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationLogWritten, storedOperation.LogState)
	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("billing_operation_key = ?", operation.OperationKey).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestTaskBillingOperationLogFailureAndStaleLeaseRecoverWithoutFinancialReplay(t *testing.T) {
	truncateTables(t)
	prepareTaskBillingOperationTest(t)
	task := insertTaskBillingFixture(t, 211, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	now := time.Unix(1_800_000_900, 0)
	operation := finalizeTaskBillingFixture(t, task, TaskStatusSuccess, 150, now)
	_, won, err := ClaimTaskBillingOperation(context.Background(), operation.ID, "billing-worker", now, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = CompleteTaskBillingOperation(context.Background(), operation.ID, "billing-worker", now.Add(time.Second))
	require.NoError(t, err)

	originalLogDB := LOG_DB
	LOG_DB = nil
	err = RecordTaskBillingOperationLog(context.Background(), operation.ID, "log-worker-a", now.Add(2*time.Second), time.Minute)
	LOG_DB = originalLogDB
	require.Error(t, err)
	failed, err := GetTaskBillingOperation(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationStateCompleted, failed.State)
	assert.Equal(t, TaskBillingOperationLogFailed, failed.LogState)
	assert.NotEmpty(t, failed.LogLastError)

	var userBefore User
	var tokenBefore Token
	require.NoError(t, DB.First(&userBefore, 211).Error)
	require.NoError(t, DB.First(&tokenBefore, 211).Error)
	_, claimed, err := ClaimNextTaskBillingOperationLog(context.Background(), "log-worker-b", now.Add(3*time.Second), time.Minute)
	require.NoError(t, err)
	assert.False(t, claimed, "failed log writes must respect retry backoff")
	logRetryAt := now.Add(2*time.Second + taskBillingLogRetryDelay)
	claimedLog, claimed, err := ClaimNextTaskBillingOperationLog(context.Background(), "log-worker-b", logRetryAt, time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, RecordClaimedTaskBillingOperationLog(context.Background(), claimedLog.ID, "log-worker-b", logRetryAt))

	var userAfter User
	var tokenAfter Token
	require.NoError(t, DB.First(&userAfter, 211).Error)
	require.NoError(t, DB.First(&tokenAfter, 211).Error)
	assert.Equal(t, userBefore.Quota, userAfter.Quota)
	assert.Equal(t, userBefore.UsedQuota, userAfter.UsedQuota)
	assert.Equal(t, tokenBefore.RemainQuota, tokenAfter.RemainQuota)
	assert.Equal(t, tokenBefore.UsedQuota, tokenAfter.UsedQuota)
	stored, err := GetTaskBillingOperation(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, TaskBillingOperationLogWritten, stored.LogState)
	assert.Equal(t, 2, stored.LogAttempts)

	// Simulate a crash after claiming another log but before writing it. The
	// stale writing lease becomes recoverable without touching accounting.
	task2 := insertTaskBillingFixture(t, 212, 100, 900, 900, 100, TaskBillingSourceWallet, 0)
	operation2 := finalizeTaskBillingFixture(t, task2, TaskStatusSuccess, 150, now.Add(10*time.Second))
	_, won, err = ClaimTaskBillingOperation(context.Background(), operation2.ID, "billing-worker", now.Add(10*time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	_, err = CompleteTaskBillingOperation(context.Background(), operation2.ID, "billing-worker", now.Add(11*time.Second))
	require.NoError(t, err)
	_, claimed, err = ClaimTaskBillingOperationLog(context.Background(), operation2.ID, "crashed-log-worker", now.Add(12*time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	_, claimed, err = ClaimTaskBillingOperationLog(context.Background(), operation2.ID, "recovery-worker", now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	assert.False(t, claimed)
	recovered, claimed, err := ClaimTaskBillingOperationLog(context.Background(), operation2.ID, "recovery-worker", now.Add(73*time.Second), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, RecordClaimedTaskBillingOperationLog(context.Background(), recovered.ID, "recovery-worker", now.Add(73*time.Second)))
}
