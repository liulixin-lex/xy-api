package controller

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAsyncTaskPollHandlerKeepsDurableBillingRecoveryEnabled(t *testing.T) {
	previousDB := model.DB
	previousUpdateTask := constant.UpdateTask
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/async-task-handler.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskBillingOperation{}))
	model.DB = db
	constant.UpdateTask = false
	t.Cleanup(func() {
		model.DB = previousDB
		constant.UpdateTask = previousUpdateTask
	})

	handler := asyncTaskPollHandler{}
	assert.False(t, handler.Enabled())

	require.NoError(t, db.Create(&model.TaskBillingOperation{
		TaskID: 1, OperationKey: "task:1:terminal:v1",
		State:    model.TaskBillingOperationStatePending,
		LogState: model.TaskBillingOperationLogPending,
	}).Error)
	assert.True(t, handler.Enabled())

	require.NoError(t, db.Model(&model.TaskBillingOperation{}).Where("task_id = ?", 1).Updates(map[string]any{
		"state":             model.TaskBillingOperationStateCompleted,
		"log_state":         model.TaskBillingOperationLogFailed,
		"log_next_retry_ms": time.Now().Add(time.Minute).UnixMilli(),
	}).Error)
	assert.False(t, handler.Enabled(), "a deferred log retry must not schedule empty polling passes")
	require.NoError(t, db.Model(&model.TaskBillingOperation{}).Where("task_id = ?", 1).
		Update("log_next_retry_ms", time.Now().Add(-time.Second).UnixMilli()).Error)
	assert.True(t, handler.Enabled())
	require.NoError(t, db.Model(&model.TaskBillingOperation{}).Where("task_id = ?", 1).
		Update("log_state", model.TaskBillingOperationLogWritten).Error)
	assert.False(t, handler.Enabled())

	require.NoError(t, db.Create(&model.Task{
		TaskID: "unfinished-task", Status: model.TaskStatusInProgress, Progress: "50%",
	}).Error)
	assert.False(t, handler.Enabled())
	constant.UpdateTask = true
	assert.True(t, handler.Enabled())
}

func TestAsyncBillingRecoveryHandlerWakesForTerminalDriftAndRetention(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/async-billing-recovery-handler.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.AsyncBillingReservation{}, &model.Task{}, &model.Midjourney{},
		&model.BillingStatsProjection{}, &model.BillingLogProjection{},
	))
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	t.Setenv("ASYNC_BILLING_RECEIPT_RETENTION_DAYS", "30")

	handler := asyncBillingRecoveryHandler{}
	assert.False(t, handler.Enabled())

	require.NoError(t, db.Create(&model.Task{
		TaskID: "terminal-drift", UserId: 9101, Status: model.TaskStatusSuccess, Progress: "100%",
		PrivateData: model.TaskPrivateData{
			BillingProtocolVersion: model.TaskBillingProtocolVersion, AsyncBillingReservationID: 9101,
		},
	}).Error)
	var task model.Task
	require.NoError(t, db.Where("task_id = ?", "terminal-drift").First(&task).Error)
	require.NoError(t, db.Create(&model.AsyncBillingReservation{
		ID: 9101, ReservationKey: "terminal-drift", ProtocolVersion: model.TaskBillingProtocolVersion,
		Kind: model.AsyncBillingKindTask, PublicTaskID: "terminal-drift", State: model.AsyncBillingReservationStateAccepted,
		UserID: 9101, FundingSource: model.TaskBillingSourceWallet, TaskID: task.ID,
		CreatedTimeMs: time.Now().UnixMilli(), UpdatedTimeMs: time.Now().UnixMilli(),
	}).Error)
	assert.True(t, handler.Enabled(), "an old-poller terminal drift must wake recovery")

	require.NoError(t, db.Exec("DELETE FROM async_billing_reservations").Error)
	require.NoError(t, db.Exec("DELETE FROM tasks").Error)
	assert.False(t, handler.Enabled())

	expiredAt := time.Now().Add(-31 * 24 * time.Hour)
	require.NoError(t, db.Create(&model.AsyncBillingReservation{
		ID: 9102, ReservationKey: "expired-receipt", ProtocolVersion: model.TaskBillingProtocolVersion,
		Kind: model.AsyncBillingKindTask, PublicTaskID: "expired-receipt", State: model.AsyncBillingReservationStateReleased,
		UserID: 9102, FundingSource: model.TaskBillingSourceWallet, CacheSyncPending: true,
		TerminalTimeMs: expiredAt.UnixMilli(),
		CreatedTimeMs:  expiredAt.UnixMilli(), UpdatedTimeMs: expiredAt.UnixMilli(),
	}).Error)
	assert.True(t, handler.Enabled(), "a protected receipt past retention must keep recovery alive until the cursor wraps")

	cutoff := model.AsyncBillingReceiptRetentionCutoff(time.Now())
	assert.WithinDuration(t, time.Now().Add(-30*24*time.Hour), cutoff, time.Second)
}
