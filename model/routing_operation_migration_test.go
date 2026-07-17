package model

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMigrateRoutingOperationStateInvariantsRepairsKnownLegacyRows(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}, &RoutingBreakerResetCommand{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))

	activeSpec := RoutingOperationSpec{
		Type: RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat("a", 64),
		SubjectType: RoutingOperationSubjectRoutingProbes, ExpectedRevision: 1,
		ExpectedActivationID: 1, ActorID: 7, Reason: "manual probe",
	}
	running, _, err := CreateRoutingOperationContext(context.Background(), activeSpec)
	require.NoError(t, err)
	claimed, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeActiveProbe, running.CreatedTimeMs, 60_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	runningCreatedTimeMs := claimed.UpdatedTimeMs + 10

	terminalSpec := activeSpec
	terminalSpec.EvaluationHash = strings.Repeat("b", 64)
	terminal, _, err := CreateRoutingOperationContext(context.Background(), terminalSpec)
	require.NoError(t, err)
	terminalClaim, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeActiveProbe, terminal.CreatedTimeMs, 1_000,
	)
	require.NoError(t, err)
	require.NotNil(t, terminalClaim)
	require.NoError(t, SucceedRoutingOperationWithPayloadContext(
		context.Background(), terminalClaim.ID, terminalClaim.ClaimToken,
		terminalClaim.UpdatedTimeMs, map[string]bool{"enabled": true},
	))
	terminalCreatedTimeMs := terminalClaim.UpdatedTimeMs + 10
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", terminal.ID).Updates(map[string]any{
		"created_time_ms":   terminalCreatedTimeMs,
		"updated_time_ms":   terminalClaim.UpdatedTimeMs,
		"completed_time_ms": terminalClaim.UpdatedTimeMs,
	}).Error)
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", running.ID).Updates(map[string]any{
		"created_time_ms": runningCreatedTimeMs,
		"updated_time_ms": claimed.UpdatedTimeMs,
		"claim_until_ms":  claimed.UpdatedTimeMs + 1,
	}).Error)

	canarySpec := routingOperationSpecForTest()
	canarySpec.EvaluationHash = strings.Repeat("c", 64)
	canary, _, err := CreateRoutingOperationContext(context.Background(), canarySpec)
	require.NoError(t, err)
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", canary.ID).Updates(map[string]any{
		"status":               RoutingOperationStatusSucceeded,
		"attempts":             0,
		"result_revision":      12,
		"result_activation_id": 22,
		"result_outbox_id":     32,
		"completed_time_ms":    canary.CreatedTimeMs,
		"updated_time_ms":      canary.CreatedTimeMs,
	}).Error)

	target := routingBreakerResetMemberTargetForTest()
	breaker, _, err := CreateRoutingBreakerResetOperationContext(
		context.Background(), routingBreakerResetOperationSpecForTest(target, "d"), target,
	)
	require.NoError(t, err)
	var command RoutingBreakerResetCommand
	require.NoError(t, db.Where("operation_id = ?", breaker.ID).First(&command).Error)
	require.NoError(t, db.Model(&RoutingBreakerResetCommand{}).Where("id = ?", command.ID).Update(
		"completed_time_ms", command.CreatedTimeMs-1,
	).Error)

	for iteration := 0; iteration < 2; iteration++ {
		require.NoError(t, migrateRoutingOperationStateInvariants(db))
	}

	storedRunning, err := GetRoutingOperationContext(context.Background(), running.ID)
	require.NoError(t, err)
	assert.Equal(t, runningCreatedTimeMs, storedRunning.UpdatedTimeMs)
	assert.Equal(t, runningCreatedTimeMs+1, storedRunning.ClaimUntilMs)

	storedTerminal, err := GetRoutingOperationContext(context.Background(), terminal.ID)
	require.NoError(t, err)
	assert.Equal(t, terminalCreatedTimeMs, storedTerminal.UpdatedTimeMs)
	assert.Equal(t, terminalCreatedTimeMs, storedTerminal.CompletedTimeMs)

	storedCanary, err := GetRoutingOperationContext(context.Background(), canary.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, storedCanary.Attempts)

	storedCommand, err := GetRoutingBreakerResetCommandByOperationContext(context.Background(), breaker.ID)
	require.NoError(t, err)
	assert.Equal(t, storedCommand.CreatedTimeMs, storedCommand.CompletedTimeMs)
}

func TestMigrateRoutingOperationStateInvariantsFailsClosed(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))

	spec := RoutingOperationSpec{
		Type: RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat("e", 64),
		SubjectType: RoutingOperationSubjectRoutingProbes, ExpectedRevision: 1,
		ExpectedActivationID: 1, ActorID: 7, Reason: "manual probe",
	}
	validCandidate, _, err := CreateRoutingOperationContext(context.Background(), spec)
	require.NoError(t, err)
	validFutureTimeMs := validCandidate.CreatedTimeMs + 10
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", validCandidate.ID).Updates(map[string]any{
		"created_time_ms": validFutureTimeMs,
		"updated_time_ms": validCandidate.UpdatedTimeMs,
	}).Error)

	spec.EvaluationHash = strings.Repeat("f", 64)
	corruptCandidate, _, err := CreateRoutingOperationContext(context.Background(), spec)
	require.NoError(t, err)
	corruptFutureTimeMs := corruptCandidate.CreatedTimeMs + 10
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", corruptCandidate.ID).Updates(map[string]any{
		"create_token":    "invalid",
		"created_time_ms": corruptFutureTimeMs,
		"updated_time_ms": corruptCandidate.UpdatedTimeMs,
	}).Error)

	assert.ErrorIs(t, migrateRoutingOperationStateInvariants(db), ErrRoutingOperationCorrupt)
	var unchanged RoutingOperation
	require.NoError(t, db.Where("id = ?", validCandidate.ID).First(&unchanged).Error)
	assert.Equal(t, validCandidate.UpdatedTimeMs, unchanged.UpdatedTimeMs)
}

func TestRetireRoutingCostSyncWorkSQLite(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRetireRoutingCostSyncWorkContract(t, db, common.DatabaseTypeSQLite)
}

func TestRetireRoutingCostSyncWorkExternalDatabaseCompatibility(t *testing.T) {
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
			runRetireRoutingCostSyncWorkContract(t, db, test.dbType)
		})
	}
}

func runRetireRoutingCostSyncWorkContract(t *testing.T, db *gorm.DB, dbType common.DatabaseType) {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}, &SystemTask{}, &SystemTaskLock{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))

	runningOperation, _, err := CreateRoutingOperationContext(
		context.Background(), routingCostSyncOperationSpecForTest("d"),
	)
	require.NoError(t, err)
	claimedOperation, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCostSync, runningOperation.CreatedTimeMs, 60_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimedOperation)

	pendingOperation, _, err := CreateRoutingOperationContext(
		context.Background(), routingCostSyncOperationSpecForTest("e"),
	)
	require.NoError(t, err)
	pendingNextRetryMs := pendingOperation.UpdatedTimeMs + 60_000
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", pendingOperation.ID).Updates(map[string]any{
		"attempts": 2, "last_error": "legacy retry", "next_retry_ms": pendingNextRetryMs,
	}).Error)

	terminalOperation, _, err := CreateFailedRoutingOperationContext(
		context.Background(), routingCostSyncOperationSpecForTest("f"), errors.New("historical failure"),
	)
	require.NoError(t, err)
	var terminalOperationBefore RoutingOperation
	require.NoError(t, db.Where("id = ?", terminalOperation.ID).First(&terminalOperationBefore).Error)

	unrelatedOperationSpec := RoutingOperationSpec{
		Type: RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat("a", 64),
		SubjectType: RoutingOperationSubjectRoutingProbes, ExpectedRevision: 1,
		ExpectedActivationID: 1, ActorID: 7, Reason: "unrelated probe",
	}
	unrelatedOperation, _, err := CreateRoutingOperationContext(context.Background(), unrelatedOperationSpec)
	require.NoError(t, err)
	var unrelatedOperationBefore RoutingOperation
	require.NoError(t, db.Where("id = ?", unrelatedOperation.ID).First(&unrelatedOperationBefore).Error)

	pendingActiveKey := SystemTaskTypeRoutingCostSync
	pendingTask := SystemTask{
		TaskID: "systask_" + strings.Repeat("p", 32), Type: SystemTaskTypeRoutingCostSync,
		Status: SystemTaskStatusPending, ActiveKey: &pendingActiveKey,
		Payload: `{"request":"pending"}`, State: `{"cursor":1}`, Result: `{"partial":true}`,
		Error: "legacy pending error", LockedBy: "pending-runner", CreatedAt: 100, UpdatedAt: 110,
	}
	runningTask := SystemTask{
		TaskID: "systask_" + strings.Repeat("r", 32), Type: SystemTaskTypeRoutingCostSync,
		Status:  SystemTaskStatusRunning,
		Payload: `{"request":"running"}`, State: `{"cursor":2}`, Result: `{"partial":true}`,
		Error: "legacy running error", LockedBy: "running-runner", CreatedAt: 120, UpdatedAt: 130,
	}
	terminalTask := SystemTask{
		TaskID: "systask_" + strings.Repeat("t", 32), Type: SystemTaskTypeRoutingCostSync,
		Status:  SystemTaskStatusSucceeded,
		Payload: `{"request":"terminal"}`, State: `{"cursor":3}`, Result: `{"done":true}`,
		CreatedAt: 140, UpdatedAt: 150,
	}
	unrelatedActiveKey := SystemTaskTypeChannelTest
	unrelatedTask := SystemTask{
		TaskID: "systask_" + strings.Repeat("u", 32), Type: SystemTaskTypeChannelTest,
		Status: SystemTaskStatusPending, ActiveKey: &unrelatedActiveKey,
		Payload: `{"request":"unrelated"}`, State: `{"cursor":4}`, Result: `{"kept":true}`,
		CreatedAt: 160, UpdatedAt: 170,
	}
	require.NoError(t, db.Create(&pendingTask).Error)
	require.NoError(t, db.Create(&runningTask).Error)
	require.NoError(t, db.Create(&terminalTask).Error)
	require.NoError(t, db.Create(&unrelatedTask).Error)

	costSyncLock := SystemTaskLock{
		Type: SystemTaskTypeRoutingCostSync, TaskID: runningTask.TaskID,
		LockedBy: "running-runner", LockedUntil: 500, UpdatedAt: 130,
	}
	unrelatedLock := SystemTaskLock{
		Type: SystemTaskTypeChannelTest, TaskID: unrelatedTask.TaskID,
		LockedBy: "channel-test-runner", LockedUntil: 600, UpdatedAt: 170,
	}
	require.NoError(t, db.Create(&costSyncLock).Error)
	require.NoError(t, db.Create(&unrelatedLock).Error)

	var terminalTaskBefore SystemTask
	require.NoError(t, db.Where("id = ?", terminalTask.ID).First(&terminalTaskBefore).Error)
	var unrelatedTaskBefore SystemTask
	require.NoError(t, db.Where("id = ?", unrelatedTask.ID).First(&unrelatedTaskBefore).Error)

	for iteration := 0; iteration < 2; iteration++ {
		require.NoError(t, retireRoutingCostSyncWork(db))
	}

	for _, operationID := range []int64{claimedOperation.ID, pendingOperation.ID} {
		var stored RoutingOperation
		require.NoError(t, db.Where("id = ?", operationID).First(&stored).Error)
		assert.Equal(t, RoutingOperationStatusSuperseded, stored.Status)
		assert.GreaterOrEqual(t, stored.Attempts, 1)
		assert.Empty(t, stored.ClaimToken)
		assert.Zero(t, stored.ClaimUntilMs)
		assert.Zero(t, stored.NextRetryMs)
		assert.Equal(t, routingCostSyncRetiredReason, stored.LastError)
		assert.Zero(t, stored.ResultRevision)
		assert.Zero(t, stored.ResultActivationID)
		assert.Zero(t, stored.ResultOutboxID)
		assert.Empty(t, stored.ResultPayloadJSON)
		assert.Empty(t, stored.ResultPayloadHash)
		assert.GreaterOrEqual(t, stored.CompletedTimeMs, stored.CreatedTimeMs)
		assert.Equal(t, stored.CompletedTimeMs, stored.UpdatedTimeMs)
		require.NoError(t, validateStoredRoutingOperation(stored))
	}

	var storedTerminalOperation RoutingOperation
	require.NoError(t, db.Where("id = ?", terminalOperation.ID).First(&storedTerminalOperation).Error)
	assert.Equal(t, terminalOperationBefore, storedTerminalOperation)
	var storedUnrelatedOperation RoutingOperation
	require.NoError(t, db.Where("id = ?", unrelatedOperation.ID).First(&storedUnrelatedOperation).Error)
	assert.Equal(t, unrelatedOperationBefore, storedUnrelatedOperation)

	for _, task := range []SystemTask{pendingTask, runningTask} {
		var stored SystemTask
		require.NoError(t, db.Where("id = ?", task.ID).First(&stored).Error)
		assert.Equal(t, SystemTaskStatusFailed, stored.Status)
		assert.Nil(t, stored.ActiveKey)
		assert.Equal(t, routingCostSyncRetiredReason, stored.Error)
		assert.Empty(t, stored.LockedBy)
		assert.Equal(t, task.Payload, stored.Payload)
		assert.Equal(t, task.State, stored.State)
		assert.Equal(t, task.Result, stored.Result)
		assert.Equal(t, task.CreatedAt, stored.CreatedAt)
		assert.GreaterOrEqual(t, stored.UpdatedAt, task.UpdatedAt)
	}

	var storedTerminalTask SystemTask
	require.NoError(t, db.Where("id = ?", terminalTask.ID).First(&storedTerminalTask).Error)
	assert.Equal(t, terminalTaskBefore, storedTerminalTask)
	var storedUnrelatedTask SystemTask
	require.NoError(t, db.Where("id = ?", unrelatedTask.ID).First(&storedUnrelatedTask).Error)
	assert.Equal(t, unrelatedTaskBefore, storedUnrelatedTask)

	var costSyncLockCount int64
	require.NoError(t, db.Model(&SystemTaskLock{}).
		Where("type = ?", SystemTaskTypeRoutingCostSync).Count(&costSyncLockCount).Error)
	assert.Zero(t, costSyncLockCount)
	var storedUnrelatedLock SystemTaskLock
	require.NoError(t, db.Where("type = ?", SystemTaskTypeChannelTest).First(&storedUnrelatedLock).Error)
	assert.Equal(t, unrelatedLock, storedUnrelatedLock)
}
