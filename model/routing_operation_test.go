package model

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type legacyRoutingOperationForMigration struct {
	ID                   int64                  `gorm:"primaryKey"`
	OperationType        string                 `gorm:"column:operation_type;type:varchar(64);index;not null"`
	IdempotencyHash      string                 `gorm:"type:char(64);uniqueIndex;not null"`
	CreateToken          string                 `gorm:"type:char(32);not null"`
	EvaluationHash       string                 `gorm:"type:char(64);index;not null"`
	PoolID               int                    `gorm:"index;not null"`
	ExpectedRevision     int64                  `gorm:"bigint;index;not null"`
	ExpectedActivationID int64                  `gorm:"bigint;index;not null"`
	ActorID              int                    `gorm:"index;not null"`
	Reason               string                 `gorm:"type:varchar(512);not null"`
	Status               RoutingOperationStatus `gorm:"type:varchar(24);index;not null"`
	ClaimToken           string                 `gorm:"type:char(32);index;not null"`
	ClaimUntilMs         int64                  `gorm:"bigint;index;not null"`
	Attempts             int                    `gorm:"not null"`
	NextRetryMs          int64                  `gorm:"bigint;index;not null"`
	LastError            string                 `gorm:"type:text;not null"`
	ResultRevision       int64                  `gorm:"bigint;index;not null"`
	ResultActivationID   int64                  `gorm:"bigint;index;not null"`
	ResultOutboxID       int64                  `gorm:"bigint;index;not null"`
	CreatedTimeMs        int64                  `gorm:"bigint;index;not null"`
	UpdatedTimeMs        int64                  `gorm:"bigint;index;not null"`
	CompletedTimeMs      int64                  `gorm:"bigint;index;not null"`
}

func (legacyRoutingOperationForMigration) TableName() string {
	return "routing_operations"
}

func TestRoutingOperationExternalDatabaseCompatibility(t *testing.T) {
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
			withRoutingTestDB(t, db, test.dbType)
			require.NoError(t, db.AutoMigrate(&legacyRoutingOperationForMigration{}))
			legacySpec := routingOperationSpecForTest()
			legacySpec.EvaluationHash = strings.Repeat("f", 64)
			_, legacyIdempotencyHash, err := normalizeRoutingOperationSpec(legacySpec)
			require.NoError(t, err)
			legacy := legacyRoutingOperationForMigration{
				OperationType: legacySpec.Type, IdempotencyHash: legacyIdempotencyHash,
				CreateToken: strings.Repeat("a", 32), EvaluationHash: legacySpec.EvaluationHash,
				PoolID: legacySpec.PoolID, ExpectedRevision: legacySpec.ExpectedRevision,
				ExpectedActivationID: legacySpec.ExpectedActivationID, ActorID: legacySpec.ActorID,
				Reason: legacySpec.Reason, Status: RoutingOperationStatusSucceeded, Attempts: 1,
				ResultRevision: 12, ResultActivationID: 22, ResultOutboxID: 32,
				CreatedTimeMs: 1_000, UpdatedTimeMs: 1_100, CompletedTimeMs: 1_100,
			}
			require.NoError(t, db.Create(&legacy).Error)

			require.NoError(t, db.AutoMigrate(&RoutingOperation{}, &SystemTask{}, &SystemTaskLock{}))
			require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))
			storedLegacy, err := GetRoutingOperationContext(context.Background(), legacy.ID)
			require.NoError(t, err)
			assert.Equal(t, RoutingOperationStatusSucceeded, storedLegacy.Status)
			assert.Empty(t, storedLegacy.ClaimToken)
			assert.Nil(t, storedLegacy.RequestKeyHash)

			columnTypes, err := db.Migrator().ColumnTypes(&RoutingOperation{})
			require.NoError(t, err)
			varcharColumns := map[string]bool{
				"idempotency_hash": false,
				"create_token":     false,
				"evaluation_hash":  false,
				"claim_token":      false,
			}
			for _, columnType := range columnTypes {
				name := strings.ToLower(columnType.Name())
				if _, exists := varcharColumns[name]; exists {
					varcharColumns[name] = strings.Contains(strings.ToLower(columnType.DatabaseTypeName()), "varchar")
				}
			}
			assert.Equal(t, map[string]bool{
				"idempotency_hash": true,
				"create_token":     true,
				"evaluation_hash":  true,
				"claim_token":      true,
			}, varcharColumns)

			operation, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
			require.NoError(t, err)
			baseTimeMs := operation.CreatedTimeMs
			claimed, err := ClaimRoutingOperationContext(
				context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs, 100,
			)
			require.NoError(t, err)
			require.NotNil(t, claimed)
			require.NoError(t, RetryRoutingOperationContext(
				context.Background(), claimed.ID, claimed.ClaimToken,
				baseTimeMs+50, baseTimeMs+100, errors.New("retry"),
			))
			recovered, err := ClaimRoutingOperationContext(
				context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs+100, 100,
			)
			require.NoError(t, err)
			require.NotNil(t, recovered)
			require.NoError(t, SupersedeRoutingOperationContext(
				context.Background(), recovered.ID, recovered.ClaimToken, baseTimeMs+150, "head changed",
			))

			legacyRepairSpec := routingOperationSpecForTest()
			legacyRepairSpec.EvaluationHash = strings.Repeat("e", 64)
			legacyRepair, _, err := CreateRoutingOperationContext(context.Background(), legacyRepairSpec)
			require.NoError(t, err)
			require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", legacyRepair.ID).Updates(map[string]any{
				"status":               RoutingOperationStatusSucceeded,
				"attempts":             0,
				"result_revision":      12,
				"result_activation_id": 22,
				"result_outbox_id":     32,
				"completed_time_ms":    legacyRepair.CreatedTimeMs,
				"updated_time_ms":      legacyRepair.CreatedTimeMs,
			}).Error)
			require.NoError(t, migrateRoutingOperationStateInvariants(db))
			repaired, err := GetRoutingOperationContext(context.Background(), legacyRepair.ID)
			require.NoError(t, err)
			assert.Equal(t, 1, repaired.Attempts)

			costOperation, _, err := CreateRoutingOperationContext(
				context.Background(), routingCostSyncOperationSpecForTest("d"),
			)
			require.NoError(t, err)
			task, created, err := AttachRoutingCostSyncOperationContext(context.Background(), costOperation.ID)
			require.NoError(t, err)
			assert.True(t, created)
			const runnerID = "routing-cost-external-runner"
			claimedTask, costClaimed, err := ClaimSystemTask(
				task.ID, task.Type, runnerID, common.GetTimestamp()+60,
			)
			require.NoError(t, err)
			require.True(t, costClaimed)
			nowMs := time.Now().UnixMilli()
			require.NoError(t, ClaimRoutingCostSyncOperationsContext(
				context.Background(), task.TaskID, runnerID, nowMs, 60_000,
			))
			finished, err := FinishRoutingCostSyncTaskContext(
				context.Background(), claimedTask.TaskID, runnerID, SystemTaskStatusSucceeded,
				map[string]int{"snapshots": 1}, "", nowMs+1_000,
			)
			require.NoError(t, err)
			assert.Equal(t, int64(1), finished)
			stored, err := GetRoutingOperationContext(context.Background(), costOperation.ID)
			require.NoError(t, err)
			assert.Equal(t, RoutingOperationStatusSucceeded, stored.Status)
		})
	}
}

func TestRoutingOperationIsIdempotentAndClaimIsCAS(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))

	spec := routingOperationSpecForTest()
	first, created, err := CreateRoutingOperationContext(context.Background(), spec)
	require.NoError(t, err)
	assert.True(t, created)
	retry, created, err := CreateRoutingOperationContext(context.Background(), spec)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, retry.ID)
	require.NoError(t, db.Exec(
		"UPDATE routing_operations SET subject_type = NULL, subject_id = NULL, result_payload_json = NULL, result_payload_hash = NULL WHERE id = ?",
		first.ID,
	).Error)
	legacyCompatible, err := GetRoutingOperationContext(context.Background(), first.ID)
	require.NoError(t, err)
	assert.Equal(t, first.IdempotencyHash, legacyCompatible.IdempotencyHash)

	const claimers = 2
	start := make(chan struct{})
	claimed := make([]*RoutingOperation, claimers)
	errs := make([]error, claimers)
	var wait sync.WaitGroup
	wait.Add(claimers)
	for index := 0; index < claimers; index++ {
		go func(index int) {
			defer wait.Done()
			<-start
			claimed[index], errs[index] = ClaimRoutingOperationContext(
				context.Background(), RoutingOperationTypeCanaryAutoRollback, first.CreatedTimeMs, 100,
			)
		}(index)
	}
	close(start)
	wait.Wait()

	winners := 0
	for index := range claimed {
		require.NoError(t, errs[index])
		if claimed[index] != nil {
			winners++
			assert.Equal(t, RoutingOperationStatusRunning, claimed[index].Status)
			assert.Len(t, claimed[index].ClaimToken, 32)
			assert.Equal(t, 1, claimed[index].Attempts)
		}
	}
	assert.Equal(t, 1, winners)
}

func TestRoutingOperationPersistsOnlySanitizedErrors(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))

	operation, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
	require.NoError(t, err)
	claimed, err := ClaimRoutingOperationContext(
		context.Background(), operation.OperationType, operation.CreatedTimeMs, 1_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	unsafe := "Authorization: Bearer bearer-secret\n" +
		"api_key=key-secret Cookie: session=cookie-secret\n" +
		"request https://api.secret.test/v1?access_token=query-secret failed at 203.0.113.10"
	require.NoError(t, RetryRoutingOperationContext(
		context.Background(), claimed.ID, claimed.ClaimToken,
		claimed.UpdatedTimeMs+1, claimed.UpdatedTimeMs+2, errors.New(unsafe),
	))

	stored, err := GetRoutingOperationContext(context.Background(), claimed.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.LastError, "***")
	for _, secret := range []string{
		"bearer-secret", "key-secret", "cookie-secret", "query-secret",
		"api.secret.test", "/v1", "203.0.113.10",
	} {
		assert.NotContains(t, stored.LastError, secret)
	}
	assert.NotContains(t, stored.LastError, "\n")
}

func TestRoutingOperationPersistsIdempotentPolicySimulationResult(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))
	spec := RoutingOperationSpec{
		Type: RoutingOperationTypePolicySimulation, EvaluationHash: strings.Repeat("c", 64),
		SubjectType: RoutingOperationSubjectPolicyDraft, SubjectID: 41, PoolID: 31,
		ExpectedRevision: 11, ExpectedActivationID: 21, ActorID: 7, Reason: "simulate draft",
	}
	payload := map[string]any{"evaluated_samples": 10, "selection_changed_count": 2}
	first, created, err := CreateSucceededRoutingOperationContext(
		context.Background(), spec, RoutingOperationResult{}, payload,
	)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, RoutingOperationStatusSucceeded, first.Status)
	assert.Len(t, first.ResultPayloadHash, 64)
	retry, created, err := CreateSucceededRoutingOperationContext(
		context.Background(), spec, RoutingOperationResult{}, payload,
	)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, retry.ID)

	encoded, err := first.ResultPayload()
	require.NoError(t, err)
	var decoded map[string]int
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	assert.Equal(t, 10, decoded["evaluated_samples"])

	operations, hasMore, err := ListRoutingOperationsContext(context.Background(), RoutingOperationFilter{
		OperationType: RoutingOperationTypePolicySimulation, Status: RoutingOperationStatusSucceeded, Limit: 10,
	})
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, operations, 1)
	assert.Equal(t, first.ID, operations[0].ID)
	assert.Empty(t, operations[0].ResultPayloadJSON, "operation lists must not load result payload bodies")
	assert.Equal(t, first.ResultPayloadHash, operations[0].ResultPayloadHash)

	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", first.ID).
		Update("result_payload_json", `{"evaluated_samples":11}`).Error)
	_, err = GetRoutingOperationContext(context.Background(), first.ID)
	assert.ErrorIs(t, err, ErrRoutingOperationCorrupt)
}

func TestRoutingOperationClaimCanSucceedWithPayload(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))
	spec := RoutingOperationSpec{
		Type: RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat("a", 64),
		SubjectType:      RoutingOperationSubjectRoutingProbes,
		ExpectedRevision: 1, ExpectedActivationID: 1, ActorID: 7, Reason: "manual probe",
		RequestKeyHash: strings.Repeat("b", 64), RequestPayloadHash: strings.Repeat("c", 64),
	}
	operation, created, err := CreateRoutingOperationContext(context.Background(), spec)
	require.NoError(t, err)
	require.True(t, created)
	nowMs := time.Now().UnixMilli() + 1
	claimed, err := ClaimRoutingOperationContext(context.Background(), RoutingOperationTypeActiveProbe, nowMs, 30_000)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, operation.ID, claimed.ID)
	payload := map[string]any{"enabled": true, "executed": 3}
	require.NoError(t, SucceedRoutingOperationWithPayloadContext(
		context.Background(), claimed.ID, claimed.ClaimToken, nowMs+1, payload,
	))

	stored, err := GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationStatusSucceeded, stored.Status)
	var result map[string]any
	require.NoError(t, common.UnmarshalJsonStr(stored.ResultPayloadJSON, &result))
	assert.EqualValues(t, 3, result["executed"])
	assert.Equal(t, true, result["enabled"])
	assert.ErrorIs(t, SucceedRoutingOperationWithPayloadContext(
		context.Background(), claimed.ID, claimed.ClaimToken, nowMs+2, payload,
	), ErrRoutingOperationClaimLost)
}

func TestRoutingOperationConcurrentActiveProbeCreateIsIdempotent(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))
	spec := RoutingOperationSpec{
		Type: RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat("d", 64),
		SubjectType:      RoutingOperationSubjectRoutingProbes,
		ExpectedRevision: 1, ExpectedActivationID: 1, ActorID: 7, Reason: "manual probe",
		RequestKeyHash: strings.Repeat("e", 64), RequestPayloadHash: strings.Repeat("f", 64),
	}
	const workers = 8
	operations := make([]RoutingOperation, workers)
	created := make([]bool, workers)
	errs := make([]error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func(index int) {
			defer wait.Done()
			operations[index], created[index], errs[index] = CreateRoutingOperationContext(context.Background(), spec)
		}(index)
	}
	wait.Wait()
	createdCount := 0
	for index := range operations {
		require.NoError(t, errs[index])
		assert.Equal(t, operations[0].ID, operations[index].ID)
		if created[index] {
			createdCount++
		}
	}
	assert.Equal(t, 1, createdCount)
	var count int64
	require.NoError(t, db.Model(&RoutingOperation{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestRoutingOperationPersistsIdempotentControlFailure(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))
	spec := RoutingOperationSpec{
		Type: RoutingOperationTypeActiveProbe, EvaluationHash: strings.Repeat("d", 64),
		SubjectType:      RoutingOperationSubjectRoutingProbes,
		ExpectedRevision: 11, ExpectedActivationID: 21, ActorID: 7, Reason: "manual active probe",
		RequestKeyHash: strings.Repeat("e", 64), RequestPayloadHash: strings.Repeat("f", 64),
	}

	first, created, err := CreateFailedRoutingOperationContext(context.Background(), spec, errors.New("probe unavailable"))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, RoutingOperationStatusFailed, first.Status)
	assert.Equal(t, "probe unavailable", first.LastError)
	assert.Equal(t, 1, first.Attempts)

	retry, created, err := CreateFailedRoutingOperationContext(context.Background(), spec, errors.New("probe unavailable"))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, retry.ID)
	_, _, err = CreateFailedRoutingOperationContext(context.Background(), spec, errors.New("different failure"))
	assert.ErrorIs(t, err, ErrRoutingOperationInvalid)
}

func TestRoutingOperationRequestKeyIsGloballyUnique(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))

	first := routingCostSyncOperationSpecForTest("a")
	_, created, err := CreateRoutingOperationContext(context.Background(), first)
	require.NoError(t, err)
	assert.True(t, created)
	conflict := first
	conflict.EvaluationHash = strings.Repeat("b", 64)
	conflict.RequestPayloadHash = strings.Repeat("b", 64)
	_, _, err = CreateRoutingOperationContext(context.Background(), conflict)
	assert.ErrorIs(t, err, ErrRoutingOperationIdempotencyConflict)

	var count int64
	require.NoError(t, db.Model(&RoutingOperation{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestRoutingOperationAcceptsBoundedControlSubjects(t *testing.T) {
	tests := []RoutingOperationSpec{
		{
			Type: RoutingOperationTypeCostSync, SubjectType: RoutingOperationSubjectRoutingCosts,
			EvaluationHash: strings.Repeat("a", 64), ExpectedRevision: 1, ExpectedActivationID: 2,
		},
		{
			Type: RoutingOperationTypeActiveProbe, SubjectType: RoutingOperationSubjectRoutingProbes,
			EvaluationHash: strings.Repeat("b", 64), ExpectedRevision: 1, ExpectedActivationID: 2,
		},
		{
			Type: RoutingOperationTypeAuditExport, SubjectType: RoutingOperationSubjectDecisionAudit,
			EvaluationHash: strings.Repeat("c", 64), ExpectedRevision: 1, ExpectedActivationID: 2,
		},
	}
	for _, spec := range tests {
		_, _, err := normalizeRoutingOperationSpec(spec)
		require.NoError(t, err, spec.Type)
		spec.SubjectID = 1
		_, _, err = normalizeRoutingOperationSpec(spec)
		assert.ErrorIs(t, err, ErrRoutingOperationInvalid, spec.Type)
	}
}

func TestRoutingCostSyncOperationsShareTaskAndConvergeWithFencing(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}, &SystemTask{}, &SystemTaskLock{}))

	first, _, err := CreateRoutingOperationContext(context.Background(), routingCostSyncOperationSpecForTest("d"))
	require.NoError(t, err)
	second, _, err := CreateRoutingOperationContext(context.Background(), routingCostSyncOperationSpecForTest("e"))
	require.NoError(t, err)
	firstTask, created, err := AttachRoutingCostSyncOperationContext(context.Background(), first.ID)
	require.NoError(t, err)
	assert.True(t, created)
	secondTask, created, err := AttachRoutingCostSyncOperationContext(context.Background(), second.ID)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, firstTask.TaskID, secondTask.TaskID)

	const runnerID = "routing-cost-runner-a"
	claimedTask, claimed, err := ClaimSystemTask(
		firstTask.ID, firstTask.Type, runnerID, common.GetTimestamp()+60,
	)
	require.NoError(t, err)
	require.True(t, claimed)
	nowMs := time.Now().UnixMilli()
	require.NoError(t, ClaimRoutingCostSyncOperationsContext(
		context.Background(), claimedTask.TaskID, runnerID, nowMs, 60_000,
	))

	third, _, err := CreateRoutingOperationContext(context.Background(), routingCostSyncOperationSpecForTest("f"))
	require.NoError(t, err)
	thirdTask, created, err := AttachRoutingCostSyncOperationContext(context.Background(), third.ID)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, firstTask.TaskID, thirdTask.TaskID)
	third, err = GetRoutingOperationContext(context.Background(), third.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationStatusRunning, third.Status)
	assert.Equal(t, 1, third.Attempts)

	_, err = FinishRoutingCostSyncTaskContext(
		context.Background(), claimedTask.TaskID, "routing-cost-runner-b",
		SystemTaskStatusSucceeded, map[string]int{"snapshots": 3}, "", nowMs+500,
	)
	assert.ErrorIs(t, err, ErrSystemTaskLockLost)

	operationCount, err := FinishRoutingCostSyncTaskContext(
		context.Background(), claimedTask.TaskID, runnerID,
		SystemTaskStatusSucceeded, map[string]any{
			"snapshots": 3, "execution_state": RoutingCostSyncExecutionStatePartial,
		}, "", nowMs+1_000,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(3), operationCount)

	for _, operationID := range []int64{first.ID, second.ID, third.ID} {
		stored, err := GetRoutingOperationContext(context.Background(), operationID)
		require.NoError(t, err)
		assert.Equal(t, RoutingOperationStatusSucceeded, stored.Status)
		assert.Equal(t, firstTask.TaskID, stored.SystemTaskID)
		payload, err := stored.ResultPayload()
		require.NoError(t, err)
		var result RoutingCostSyncOperationResult
		require.NoError(t, common.Unmarshal(payload, &result))
		assert.Equal(t, RoutingCostSyncExecutionStatePartial, result.ExecutionState)
		assert.Equal(t, SystemTaskStatusSucceeded, result.TaskStatus)
	}
	finishedTask, err := GetSystemTaskByTaskID(firstTask.TaskID)
	require.NoError(t, err)
	require.NotNil(t, finishedTask)
	assert.Equal(t, SystemTaskStatusSucceeded, finishedTask.Status)
}

func TestRoutingCostSyncOperationsPreserveGroupLogicalTime(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}, &SystemTask{}, &SystemTaskLock{}))

	first, _, err := CreateRoutingOperationContext(context.Background(), routingCostSyncOperationSpecForTest("4"))
	require.NoError(t, err)
	second, _, err := CreateRoutingOperationContext(context.Background(), routingCostSyncOperationSpecForTest("5"))
	require.NoError(t, err)
	task, created, err := AttachRoutingCostSyncOperationContext(context.Background(), first.ID)
	require.NoError(t, err)
	require.True(t, created)
	secondTask, created, err := AttachRoutingCostSyncOperationContext(context.Background(), second.ID)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, task.TaskID, secondTask.TaskID)

	const runnerID = "routing-cost-logical-clock-runner"
	claimedTask, claimed, err := ClaimSystemTask(task.ID, task.Type, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	observedTimeMs := time.Now().UnixMilli()
	futureTimeMs := observedTimeMs + 1_000
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", second.ID).Updates(map[string]any{
		"created_time_ms": futureTimeMs,
		"updated_time_ms": futureTimeMs,
	}).Error)

	require.NoError(t, ClaimRoutingCostSyncOperationsContext(
		context.Background(), claimedTask.TaskID, runnerID, observedTimeMs, 100,
	))
	for _, operationID := range []int64{first.ID, second.ID} {
		stored, getErr := GetRoutingOperationContext(context.Background(), operationID)
		require.NoError(t, getErr)
		assert.Equal(t, RoutingOperationStatusRunning, stored.Status)
		assert.Equal(t, futureTimeMs, stored.UpdatedTimeMs)
		assert.Equal(t, futureTimeMs+100, stored.ClaimUntilMs)
	}

	finished, err := FinishRoutingCostSyncTaskContext(
		context.Background(), claimedTask.TaskID, runnerID, SystemTaskStatusSucceeded,
		map[string]int{"snapshots": 2}, "", observedTimeMs+1,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), finished)
	for _, operationID := range []int64{first.ID, second.ID} {
		stored, getErr := GetRoutingOperationContext(context.Background(), operationID)
		require.NoError(t, getErr)
		assert.Equal(t, RoutingOperationStatusSucceeded, stored.Status)
		assert.Equal(t, futureTimeMs, stored.UpdatedTimeMs)
		assert.Equal(t, futureTimeMs, stored.CompletedTimeMs)
	}
}

func TestRoutingCostSyncLeaseExpiryFailsAssociatedOperations(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}, &SystemTask{}, &SystemTaskLock{}))

	operation, _, err := CreateRoutingOperationContext(context.Background(), routingCostSyncOperationSpecForTest("9"))
	require.NoError(t, err)
	task, _, err := AttachRoutingCostSyncOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	const runnerID = "routing-cost-expired-runner"
	_, claimed, err := ClaimSystemTask(task.ID, task.Type, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	nowMs := time.Now().UnixMilli()
	require.NoError(t, ClaimRoutingCostSyncOperationsContext(
		context.Background(), task.TaskID, runnerID, nowMs, 60_000,
	))
	require.NoError(t, db.Model(&SystemTaskLock{}).
		Where("task_id = ?", task.TaskID).
		Update("locked_until", common.GetTimestamp()-1).Error)
	require.NoError(t, MarkSystemTaskLeaseExpired(task.TaskID))

	stored, err := GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationStatusFailed, stored.Status)
	assert.Equal(t, "task lease expired", stored.LastError)
	finishedTask, err := GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, finishedTask)
	assert.Equal(t, SystemTaskStatusFailed, finishedTask.Status)
	_, err = FinishRoutingCostSyncTaskContext(
		context.Background(), task.TaskID, runnerID,
		SystemTaskStatusSucceeded, map[string]int{"snapshots": 1}, "", nowMs+1_000,
	)
	assert.ErrorIs(t, err, ErrSystemTaskLockLost)
}

func TestRoutingOperationSQLiteMigrationPreservesLegacyRows(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&legacyRoutingOperationForMigration{}))
	spec := routingOperationSpecForTest()
	_, idempotencyHash, err := normalizeRoutingOperationSpec(spec)
	require.NoError(t, err)
	legacy := legacyRoutingOperationForMigration{
		OperationType: spec.Type, IdempotencyHash: idempotencyHash, CreateToken: strings.Repeat("a", 32),
		EvaluationHash: spec.EvaluationHash, PoolID: spec.PoolID, ExpectedRevision: spec.ExpectedRevision,
		ExpectedActivationID: spec.ExpectedActivationID, ActorID: spec.ActorID, Reason: spec.Reason,
		Status: RoutingOperationStatusSucceeded, Attempts: 1,
		ResultRevision: 12, ResultActivationID: 22, ResultOutboxID: 32,
		CreatedTimeMs: 1_000, UpdatedTimeMs: 1_100, CompletedTimeMs: 1_100,
	}
	require.NoError(t, db.Create(&legacy).Error)

	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))
	assert.True(t, db.Migrator().HasIndex(&RoutingOperation{}, routingOperationRequestKeyUniqueIndex))
	for _, column := range []string{"subject_type", "subject_id", "result_payload_json", "result_payload_hash"} {
		assert.True(t, db.Migrator().HasColumn(&RoutingOperation{}, column), column)
	}
	stored, err := GetRoutingOperationContext(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationTypeCanaryAutoRollback, stored.OperationType)
	assert.Empty(t, stored.SubjectType)
	assert.Zero(t, stored.SubjectID)
	assert.Empty(t, stored.ResultPayloadJSON)
	assert.Empty(t, stored.ResultPayloadHash)

	type sqliteColumnInfo struct {
		Name    string `gorm:"column:name"`
		NotNull int    `gorm:"column:notnull"`
	}
	var columns []sqliteColumnInfo
	require.NoError(t, db.Raw("PRAGMA table_info(routing_operations)").Scan(&columns).Error)
	nullable := map[string]bool{}
	for _, column := range columns {
		if column.Name == "subject_type" || column.Name == "subject_id" ||
			column.Name == "result_payload_json" || column.Name == "result_payload_hash" {
			nullable[column.Name] = column.NotNull == 0
		}
	}
	assert.Equal(t, map[string]bool{
		"subject_type": true, "subject_id": true,
		"result_payload_json": true, "result_payload_hash": true,
	}, nullable)
}

func TestRoutingOperationExpiredClaimIsRecoverableAndFenced(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))
	operation, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
	require.NoError(t, err)
	baseTimeMs := operation.CreatedTimeMs

	first, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs, 100,
	)
	require.NoError(t, err)
	require.NotNil(t, first)

	recovered, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs+101, 100,
	)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	assert.NotEqual(t, first.ClaimToken, recovered.ClaimToken)
	assert.Equal(t, 2, recovered.Attempts)

	result := RoutingOperationResult{Revision: 12, ActivationID: 22, OutboxID: 32}
	err = SucceedRoutingOperationContext(context.Background(), first.ID, first.ClaimToken, baseTimeMs+102, result)
	assert.ErrorIs(t, err, ErrRoutingOperationClaimLost)
	require.NoError(t, SucceedRoutingOperationContext(
		context.Background(), recovered.ID, recovered.ClaimToken, baseTimeMs+102, result,
	))

	claimed, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs+1_000, 100,
	)
	require.NoError(t, err)
	assert.Nil(t, claimed)
}

func TestRoutingOperationClaimPreservesMonotonicTimestamps(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))

	operation, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
	require.NoError(t, err)
	futureTimeMs := operation.CreatedTimeMs + 1
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
		"created_time_ms": futureTimeMs,
		"updated_time_ms": futureTimeMs,
	}).Error)

	claimed, err := ClaimRoutingOperationContext(
		context.Background(), operation.OperationType, futureTimeMs-1, 100,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, futureTimeMs, claimed.UpdatedTimeMs)
	assert.Equal(t, futureTimeMs+100, claimed.ClaimUntilMs)

	stored, err := GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, claimed.ClaimToken, stored.ClaimToken)
}

func TestRoutingOperationTransitionsPreserveMonotonicTimestamps(t *testing.T) {
	t.Run("renew", func(t *testing.T) {
		db := openRoutingSQLiteTestDB(t)
		withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
		require.NoError(t, db.AutoMigrate(&RoutingOperation{}))

		operation, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
		require.NoError(t, err)
		futureTimeMs := operation.CreatedTimeMs + 1
		require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
			"created_time_ms": futureTimeMs,
			"updated_time_ms": futureTimeMs,
		}).Error)
		claimed, err := ClaimRoutingOperationContext(
			context.Background(), operation.OperationType, futureTimeMs-1, 100,
		)
		require.NoError(t, err)
		require.NotNil(t, claimed)

		require.NoError(t, RenewRoutingOperationClaimContext(
			context.Background(), claimed.ID, claimed.ClaimToken, futureTimeMs-1, 200,
		))
		stored, err := GetRoutingOperationContext(context.Background(), operation.ID)
		require.NoError(t, err)
		assert.Equal(t, futureTimeMs, stored.UpdatedTimeMs)
		assert.Equal(t, futureTimeMs+200, stored.ClaimUntilMs)

		require.NoError(t, RenewRoutingOperationClaimContext(
			context.Background(), claimed.ID, claimed.ClaimToken, futureTimeMs-1, 100,
		))
		unchanged, err := GetRoutingOperationContext(context.Background(), operation.ID)
		require.NoError(t, err)
		assert.Equal(t, stored.ClaimUntilMs, unchanged.ClaimUntilMs)
	})

	t.Run("retry", func(t *testing.T) {
		db := openRoutingSQLiteTestDB(t)
		withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
		require.NoError(t, db.AutoMigrate(&RoutingOperation{}))

		operation, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
		require.NoError(t, err)
		futureTimeMs := operation.CreatedTimeMs + 1
		require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
			"created_time_ms": futureTimeMs,
			"updated_time_ms": futureTimeMs,
		}).Error)
		claimed, err := ClaimRoutingOperationContext(
			context.Background(), operation.OperationType, futureTimeMs-1, 1_000,
		)
		require.NoError(t, err)
		require.NotNil(t, claimed)

		require.NoError(t, RetryRoutingOperationContext(
			context.Background(), claimed.ID, claimed.ClaimToken,
			futureTimeMs-1, futureTimeMs+49, errors.New("retry"),
		))
		stored, err := GetRoutingOperationContext(context.Background(), operation.ID)
		require.NoError(t, err)
		assert.Equal(t, futureTimeMs, stored.UpdatedTimeMs)
		assert.Equal(t, futureTimeMs+50, stored.NextRetryMs)
	})

	t.Run("finish", func(t *testing.T) {
		db := openRoutingSQLiteTestDB(t)
		withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
		require.NoError(t, db.AutoMigrate(&RoutingOperation{}))

		operation, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
		require.NoError(t, err)
		futureTimeMs := operation.CreatedTimeMs + 1
		require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
			"created_time_ms": futureTimeMs,
			"updated_time_ms": futureTimeMs,
		}).Error)
		claimed, err := ClaimRoutingOperationContext(
			context.Background(), operation.OperationType, futureTimeMs-1, 1_000,
		)
		require.NoError(t, err)
		require.NotNil(t, claimed)

		result := RoutingOperationResult{Revision: 12, ActivationID: 22, OutboxID: 32}
		require.NoError(t, SucceedRoutingOperationContext(
			context.Background(), claimed.ID, claimed.ClaimToken, futureTimeMs-1, result,
		))
		stored, err := GetRoutingOperationContext(context.Background(), operation.ID)
		require.NoError(t, err)
		assert.Equal(t, futureTimeMs, stored.UpdatedTimeMs)
		assert.Equal(t, futureTimeMs, stored.CompletedTimeMs)
	})
}

func TestRoutingOperationLogicalClockOverflowDoesNotMutateClaim(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))

	operation, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
	require.NoError(t, err)
	require.NoError(t, db.Model(&RoutingOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
		"created_time_ms": int64(math.MaxInt64),
		"updated_time_ms": int64(math.MaxInt64),
	}).Error)

	claimed, err := ClaimRoutingOperationContext(
		context.Background(), operation.OperationType, 1, 1,
	)
	assert.ErrorIs(t, err, ErrRoutingOperationInvalid)
	assert.Nil(t, claimed)
	stored, err := GetRoutingOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationStatusPending, stored.Status)
	assert.Equal(t, int64(math.MaxInt64), stored.UpdatedTimeMs)
}

func TestRoutingOperationRetryAndTerminalTransitionsAreCAS(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&RoutingOperation{}))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))

	operation, _, err := CreateRoutingOperationContext(context.Background(), routingOperationSpecForTest())
	require.NoError(t, err)
	baseTimeMs := operation.CreatedTimeMs
	runnable, err := HasRunnableRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs,
	)
	require.NoError(t, err)
	assert.True(t, runnable)
	claimed, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs, 200,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, RetryRoutingOperationContext(
		context.Background(), operation.ID, claimed.ClaimToken,
		baseTimeMs+50, baseTimeMs+100, errors.New("transient"),
	))

	runnable, err = HasRunnableRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs+99,
	)
	require.NoError(t, err)
	assert.False(t, runnable)
	notDue, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs+99, 100,
	)
	require.NoError(t, err)
	assert.Nil(t, notDue)
	due, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs+100, 100,
	)
	require.NoError(t, err)
	require.NotNil(t, due)
	assert.Equal(t, 2, due.Attempts)
	require.NoError(t, FailRoutingOperationContext(
		context.Background(), due.ID, due.ClaimToken, baseTimeMs+150, errors.New("permanent"),
	))
	runnable, err = HasRunnableRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs+1_000,
	)
	require.NoError(t, err)
	assert.False(t, runnable)

	var failed RoutingOperation
	require.NoError(t, db.First(&failed, due.ID).Error)
	assert.Equal(t, RoutingOperationStatusFailed, failed.Status)
	assert.Equal(t, "permanent", failed.LastError)
	assert.Empty(t, failed.ClaimToken)
	assert.Equal(t, baseTimeMs+150, failed.CompletedTimeMs)

	err = FailRoutingOperationContext(
		context.Background(), due.ID, due.ClaimToken, baseTimeMs+151, errors.New("again"),
	)
	assert.ErrorIs(t, err, ErrRoutingOperationClaimLost)
	terminal, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, baseTimeMs+1_000, 100,
	)
	require.NoError(t, err)
	assert.Nil(t, terminal)

	supersedeSpec := routingOperationSpecForTest()
	supersedeSpec.EvaluationHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	supersedeOperation, _, err := CreateRoutingOperationContext(context.Background(), supersedeSpec)
	require.NoError(t, err)
	supersedeBaseTimeMs := supersedeOperation.CreatedTimeMs
	superseded, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, supersedeBaseTimeMs, 100,
	)
	require.NoError(t, err)
	require.NotNil(t, superseded)
	require.NoError(t, SupersedeRoutingOperationContext(
		context.Background(), superseded.ID, superseded.ClaimToken, supersedeBaseTimeMs+50, "head changed",
	))
	var storedSuperseded RoutingOperation
	require.NoError(t, db.First(&storedSuperseded, superseded.ID).Error)
	assert.Equal(t, RoutingOperationStatusSuperseded, storedSuperseded.Status)
}

func routingOperationSpecForTest() RoutingOperationSpec {
	return RoutingOperationSpec{
		Type:                 RoutingOperationTypeCanaryAutoRollback,
		EvaluationHash:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PoolID:               31,
		ExpectedRevision:     11,
		ExpectedActivationID: 21,
		ActorID:              0,
		Reason:               "automatic canary rollback",
	}
}

func routingCostSyncOperationSpecForTest(keyCharacter string) RoutingOperationSpec {
	return RoutingOperationSpec{
		Type: RoutingOperationTypeCostSync, EvaluationHash: strings.Repeat("c", 64),
		SubjectType:      RoutingOperationSubjectRoutingCosts,
		ExpectedRevision: 11, ExpectedActivationID: 21, ActorID: 7, Reason: "manual routing cost sync",
		RequestKeyHash: strings.Repeat(keyCharacter, 64), RequestPayloadHash: strings.Repeat("c", 64),
	}
}
