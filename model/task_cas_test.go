package model

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic("failed to open test db: " + err.Error())
	}
	DB = db
	LOG_DB = db

	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.LogConsumeEnabled = true
	initCol()

	sqlDB, err := db.DB()
	if err != nil {
		panic("failed to get sql.DB: " + err.Error())
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(
		&Task{},
		&User{},
		&Token{},
		&Log{},
		&Channel{},
		&QuotaData{},
		&Ability{},
		&TopUp{},
		&AffiliateRewardRecord{},
		&InviteInitialQuotaRecord{},
		&InviteLinkBatch{},
		&SubscriptionPlan{},
		&SubscriptionOrder{},
		&UserSubscription{},
		&UserOAuthBinding{},
		&PerfMetric{},
		&RoutingChannelBinding{},
		&RoutingCostSnapshot{},
		&RoutingChannelMetric{},
		&RoutingBreakerState{},
		&RoutingAgentRecommendation{},
		&SystemInstance{},
		&SystemTask{},
		&SystemTaskLock{},
	); err != nil {
		panic("failed to migrate: " + err.Error())
	}

	os.Exit(m.Run())
}

func truncateTables(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		DB.Exec("DELETE FROM tasks")
		DB.Exec("DELETE FROM users")
		DB.Exec("DELETE FROM tokens")
		DB.Exec("DELETE FROM logs")
		DB.Exec("DELETE FROM channels")
		DB.Exec("DELETE FROM quota_data")
		DB.Exec("DELETE FROM abilities")
		DB.Exec("DELETE FROM top_ups")
		DB.Exec("DELETE FROM affiliate_reward_records")
		DB.Exec("DELETE FROM invite_initial_quota_records")
		DB.Exec("DELETE FROM invite_link_batches")
		DB.Exec("DELETE FROM subscription_orders")
		DB.Exec("DELETE FROM subscription_plans")
		DB.Exec("DELETE FROM user_subscriptions")
		DB.Exec("DELETE FROM user_oauth_bindings")
		DB.Exec("DELETE FROM perf_metrics")
		DB.Exec("DELETE FROM routing_channel_bindings")
		DB.Exec("DELETE FROM routing_cost_snapshots")
		DB.Exec("DELETE FROM routing_channel_metrics")
		DB.Exec("DELETE FROM routing_breaker_states")
		DB.Exec("DELETE FROM routing_agent_recommendations")
		DB.Exec("DELETE FROM system_instances")
		DB.Exec("DELETE FROM system_task_locks")
		DB.Exec("DELETE FROM system_tasks")
	})
}

func insertTask(t *testing.T, task *Task) {
	t.Helper()
	task.CreatedAt = time.Now().Unix()
	task.UpdatedAt = time.Now().Unix()
	require.NoError(t, DB.Create(task).Error)
}

func TestTaskLookupFailsClosedForAmbiguousPublicIdentity(t *testing.T) {
	truncateTables(t)
	insertTask(t, &Task{TaskID: "task_duplicate_user", UserId: 11, Status: TaskStatusInProgress})
	insertTask(t, &Task{TaskID: "task_duplicate_user", UserId: 11, Status: TaskStatusInProgress})
	insertTask(t, &Task{TaskID: "task_duplicate_global", UserId: 21, Status: TaskStatusInProgress})
	insertTask(t, &Task{TaskID: "task_duplicate_global", UserId: 22, Status: TaskStatusInProgress})
	insertTask(t, &Task{TaskID: "task_unique", UserId: 11, Status: TaskStatusInProgress})

	_, exists, err := GetByTaskId(11, "task_duplicate_user")
	assert.ErrorIs(t, err, ErrTaskIdentityAmbiguous)
	assert.False(t, exists)
	_, exists, err = GetByOnlyTaskId("task_duplicate_global")
	assert.ErrorIs(t, err, ErrTaskIdentityAmbiguous)
	assert.False(t, exists)
	_, err = GetByTaskIds(11, []any{"task_duplicate_user", "task_unique"})
	assert.ErrorIs(t, err, ErrTaskIdentityAmbiguous)

	unique, exists, err := GetByTaskId(11, "task_unique")
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, "task_unique", unique.TaskID)
}

func TestTaskRestoreV2PrivateDataMergesOnlyLegacyTerminalResultFields(t *testing.T) {
	truncateTables(t)
	task := &Task{
		TaskID: "task_old_poller_result", UserId: 71, Status: TaskStatusSuccess, Progress: "100%",
		PrivateData: TaskPrivateData{
			BillingProtocolVersion: TaskBillingProtocolVersion, AsyncBillingReservationID: 91001,
			RoutingCredentialID: 83, RoutingChannelGeneration: "generation-7",
			UpstreamTaskID: "provider-task-91001", BillingSource: TaskBillingSourceWallet,
			TokenId: 71, NodeName: "submit-node",
			BillingContext: &TaskBillingContext{
				OriginModelName: "video-model", ModelPrice: 2, GroupRatio: 1, ModelRatio: 1,
			},
		},
	}
	require.NoError(t, task.IsolateV2BillingFromLegacyPollers(120))
	insertTask(t, task)
	originalDurableHash := task.DurablePrivateDataHash

	legacyPrivateData := `{
		"key":"must-not-merge",
		"result_url":"https://provider.example/result-91001.mp4",
		"billing_source":"subscription",
		"subscription_id":999,
		"token_id":999,
		"billing_context":{"origin_model_name":"tampered-model","model_price":999}
	}`
	require.NoError(t, DB.Exec("UPDATE tasks SET private_data = ? WHERE id = ?", []byte(legacyPrivateData), task.ID).Error)

	var restored Task
	require.NoError(t, DB.First(&restored, task.ID).Error)
	assert.Equal(t, "https://provider.example/result-91001.mp4", restored.PrivateData.ResultURL)
	assert.Equal(t, "https://provider.example/result-91001.mp4", restored.GetUpstreamResultURL())
	assert.Equal(t, TaskBillingProtocolVersion, restored.PrivateData.BillingProtocolVersion)
	assert.Equal(t, int64(91001), restored.PrivateData.AsyncBillingReservationID)
	assert.Equal(t, 83, restored.PrivateData.RoutingCredentialID)
	assert.Equal(t, "generation-7", restored.PrivateData.RoutingChannelGeneration)
	assert.Equal(t, "provider-task-91001", restored.PrivateData.UpstreamTaskID)
	assert.Equal(t, TaskBillingSourceWallet, restored.PrivateData.BillingSource)
	assert.Equal(t, 71, restored.PrivateData.TokenId)
	assert.Empty(t, restored.PrivateData.Key)
	require.NotNil(t, restored.PrivateData.DurableBillingContext)
	assert.Equal(t, "video-model", restored.PrivateData.DurableBillingContext.OriginModelName)
	assert.Equal(t, 120, restored.EffectiveBillingQuota())
	assert.Equal(t, originalDurableHash, restored.DurablePrivateDataHash)
}

// ---------------------------------------------------------------------------
// Snapshot / Equal — pure logic tests (no DB)
// ---------------------------------------------------------------------------

func TestSnapshotEqual_Same(t *testing.T) {
	s := taskSnapshot{
		Status:            TaskStatusInProgress,
		Progress:          "50%",
		StartTime:         1000,
		FinishTime:        0,
		FailReason:        "",
		UpstreamResultURL: "",
		Data:              json.RawMessage(`{"key":"value"}`),
	}
	assert.True(t, s.Equal(s))
}

func TestSnapshotEqual_DifferentStatus(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage(`{}`)}
	b := taskSnapshot{Status: TaskStatusSuccess, Data: json.RawMessage(`{}`)}
	assert.False(t, a.Equal(b))
}

func TestSnapshotEqual_DifferentProgress(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Progress: "30%", Data: json.RawMessage(`{}`)}
	b := taskSnapshot{Status: TaskStatusInProgress, Progress: "60%", Data: json.RawMessage(`{}`)}
	assert.False(t, a.Equal(b))
}

func TestSnapshotEqual_DifferentData(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage(`{"a":1}`)}
	b := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage(`{"a":2}`)}
	assert.False(t, a.Equal(b))
}

func TestSnapshotEqual_NilVsEmpty(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Data: nil}
	b := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage{}}
	// bytes.Equal(nil, []byte{}) == true
	assert.True(t, a.Equal(b))
}

func TestSnapshot_Roundtrip(t *testing.T) {
	task := &Task{
		Status:     TaskStatusInProgress,
		Progress:   "42%",
		StartTime:  1234,
		FinishTime: 5678,
		FailReason: "timeout",
		PrivateData: TaskPrivateData{
			UpstreamResultURL: "https://example.com/result.mp4",
		},
		Data: json.RawMessage(`{"model":"test-model"}`),
	}
	snap := task.Snapshot()
	assert.Equal(t, task.Status, snap.Status)
	assert.Equal(t, task.Progress, snap.Progress)
	assert.Equal(t, task.StartTime, snap.StartTime)
	assert.Equal(t, task.FinishTime, snap.FinishTime)
	assert.Equal(t, task.FailReason, snap.FailReason)
	assert.Equal(t, task.PrivateData.UpstreamResultURL, snap.UpstreamResultURL)
	assert.JSONEq(t, string(task.Data), string(snap.Data))
}

// ---------------------------------------------------------------------------
// UpdateWithStatus CAS — DB integration tests
// ---------------------------------------------------------------------------

func TestUpdateWithStatus_Win(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID:   "task_cas_win",
		Status:   TaskStatusInProgress,
		Progress: "50%",
		Data:     json.RawMessage(`{}`),
	}
	insertTask(t, task)

	task.Status = TaskStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus(TaskStatusInProgress)
	require.NoError(t, err)
	assert.True(t, won)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, TaskStatusSuccess, reloaded.Status)
	assert.Equal(t, "100%", reloaded.Progress)
}

func TestUpdateWithStatus_Lose(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID: "task_cas_lose",
		Status: TaskStatusFailure,
		Data:   json.RawMessage(`{}`),
	}
	insertTask(t, task)

	task.Status = TaskStatusSuccess
	won, err := task.UpdateWithStatus(TaskStatusInProgress) // wrong fromStatus
	require.NoError(t, err)
	assert.False(t, won)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, TaskStatusFailure, reloaded.Status) // unchanged
}

func TestUpdateWithStatus_ConcurrentWinner(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID: "task_cas_race",
		Status: TaskStatusInProgress,
		Quota:  1000,
		Data:   json.RawMessage(`{}`),
	}
	insertTask(t, task)

	const goroutines = 5
	wins := make([]bool, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			t := &Task{}
			*t = Task{
				ID:       task.ID,
				TaskID:   task.TaskID,
				Status:   TaskStatusSuccess,
				Progress: "100%",
				Quota:    task.Quota,
				Data:     json.RawMessage(`{}`),
			}
			t.CreatedAt = task.CreatedAt
			t.UpdatedAt = time.Now().Unix()
			won, err := t.UpdateWithStatus(TaskStatusInProgress)
			if err == nil {
				wins[idx] = won
			}
		}(i)
	}
	wg.Wait()

	winCount := 0
	for _, w := range wins {
		if w {
			winCount++
		}
	}
	assert.Equal(t, 1, winCount, "exactly one goroutine should win the CAS")
}
