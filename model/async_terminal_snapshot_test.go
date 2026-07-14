package model

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskTerminalSnapshotRepairRetriesFailureBehindCursor(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	task := Task{
		TaskID: "terminal-repair-task", UserId: 88401, ChannelId: 88401,
		Status: TaskStatusSuccess, Progress: "100%", SubmitTime: now.Add(-time.Minute).Unix(),
		StartTime: now.Add(-30 * time.Second).Unix(), FinishTime: now.Unix(), Data: json.RawMessage(`{"ok":true}`),
	}
	require.NoError(t, db.Create(&task).Error)
	operation := TaskBillingOperation{
		TaskID: task.ID, OperationKey: "terminal-repair-task", TerminalStatus: TaskStatusSuccess,
		Kind: TaskBillingOperationKindNoop, State: TaskBillingOperationStateCompleted,
		UserID: task.UserId, ChannelID: task.ChannelId, BillingSource: TaskBillingSourceWallet,
		CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(), CompletedTimeMs: now.UnixMilli(),
		LogState: TaskBillingOperationLogNotRequired,
	}
	require.NoError(t, freezeTaskTerminalSnapshot(&operation, &task))
	validHash := operation.TerminalPayloadHash
	operation.TerminalPayloadHash = ""
	require.NoError(t, db.Create(&operation).Error)
	assert.True(t, HasAsyncBillingRecoveryWork(now.Add(time.Second)))

	first, err := RepairTaskTerminalSnapshotPage(context.Background(), 0, 1, now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, 1, first.Failed)
	assert.Equal(t, operation.ID, first.NextID)

	require.NoError(t, db.Model(&TaskBillingOperation{}).Where("id = ?", operation.ID).
		Update("terminal_payload_hash", validHash).Error)
	require.NoError(t, db.Model(&Task{}).Where("id = ?", task.ID).Update("progress", "0%").Error)
	assert.True(t, HasAsyncBillingRecoveryWork(now.Add(2*time.Second)))
	second, err := RepairTaskTerminalSnapshotPage(
		context.Background(), operation.ID, 4, now.Add(2*time.Second),
	)
	require.NoError(t, err)
	assert.Zero(t, second.Failed)
	assert.Equal(t, 1, second.Repaired)

	require.NoError(t, db.First(&task, task.ID).Error)
	require.NoError(t, db.First(&operation, operation.ID).Error)
	assert.Equal(t, "100%", task.Progress)
	assert.Empty(t, operation.LastError)
}

func TestMidjourneyTerminalSnapshotRepairRetriesFailureBehindCursor(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	task := Midjourney{
		MjId: "terminal-repair-mj", UserId: 88402, ChannelId: 88402,
		Status: "SUCCESS", Progress: "100%", SubmitTime: now.Add(-time.Minute).Unix(),
		StartTime: now.Add(-30 * time.Second).Unix(), FinishTime: now.Unix(),
	}
	require.NoError(t, db.Create(&task).Error)
	operation := MidjourneyBillingOperation{
		MidjourneyID: task.Id, OperationKey: "terminal-repair-mj", TerminalStatus: "SUCCESS",
		Kind: TaskBillingOperationKindNoop, State: TaskBillingOperationStateCompleted,
		UserID: task.UserId, ChannelID: task.ChannelId, BillingSource: TaskBillingSourceWallet,
		CreatedTimeMs: now.UnixMilli(), UpdatedTimeMs: now.UnixMilli(), CompletedTimeMs: now.UnixMilli(),
		LogState: TaskBillingOperationLogNotRequired,
	}
	require.NoError(t, freezeMidjourneyTerminalSnapshot(&operation, &task))
	validHash := operation.TerminalPayloadHash
	operation.TerminalPayloadHash = ""
	require.NoError(t, db.Create(&operation).Error)
	assert.True(t, HasAsyncBillingRecoveryWork(now.Add(time.Second)))

	first, err := RepairMidjourneyTerminalSnapshotPage(context.Background(), 0, 1, now.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, 1, first.Failed)
	assert.Equal(t, int64(operation.ID), first.NextID)

	require.NoError(t, db.Model(&MidjourneyBillingOperation{}).Where("id = ?", operation.ID).
		Update("terminal_payload_hash", validHash).Error)
	require.NoError(t, db.Model(&Midjourney{}).Where("id = ?", task.Id).Update("progress", "0%").Error)
	assert.True(t, HasAsyncBillingRecoveryWork(now.Add(2*time.Second)))
	second, err := RepairMidjourneyTerminalSnapshotPage(
		context.Background(), int64(operation.ID), 4, now.Add(2*time.Second),
	)
	require.NoError(t, err)
	assert.Zero(t, second.Failed)
	assert.Equal(t, 1, second.Repaired)

	require.NoError(t, db.First(&task, task.Id).Error)
	require.NoError(t, db.First(&operation, operation.ID).Error)
	assert.Equal(t, "100%", task.Progress)
	assert.Empty(t, operation.LastError)
}

func TestTerminalSnapshotAuditWakesForNonEmptyPayloadCorruption(t *testing.T) {
	db := setupAsyncBillingReservationTest(t)
	now := time.Now()
	task := Task{
		TaskID: "terminal-payload-audit", UserId: 88403, ChannelId: 88403,
		Status: TaskStatusSuccess, Progress: "100%", FinishTime: now.Unix(),
		Data: json.RawMessage(`{"ok":true}`),
	}
	require.NoError(t, db.Create(&task).Error)
	operation := TaskBillingOperation{
		TaskID: task.ID, OperationKey: "terminal-payload-audit", TerminalStatus: TaskStatusSuccess,
		Kind: TaskBillingOperationKindNoop, State: TaskBillingOperationStateCompleted,
		UserID: task.UserId, ChannelID: task.ChannelId, BillingSource: TaskBillingSourceWallet,
		CreatedTimeMs: now.Add(-25 * time.Hour).UnixMilli(),
		UpdatedTimeMs: now.Add(-25 * time.Hour).UnixMilli(), CompletedTimeMs: now.Add(-25 * time.Hour).UnixMilli(),
		LogState: TaskBillingOperationLogNotRequired,
	}
	require.NoError(t, freezeTaskTerminalSnapshot(&operation, &task))
	operation.TerminalPayload = []byte(`{"corrupt":`)
	require.NoError(t, db.Create(&operation).Error)

	assert.True(t, HasAsyncBillingRecoveryWork(now))
	page, err := RepairTaskTerminalSnapshotPage(context.Background(), 0, 10, now)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Failed)
	require.NoError(t, db.First(&operation, operation.ID).Error)
	assert.Contains(t, operation.LastError, terminalSnapshotRepairError)
	assert.True(t, HasAsyncBillingRecoveryWork(now.Add(time.Minute)), "failed integrity audits must remain recoverable")
}
