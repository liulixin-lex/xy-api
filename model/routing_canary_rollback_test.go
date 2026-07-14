package model

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingCanaryAutoRollbackDemotesOnlyTargetPool(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingCanaryAutoRollbackContract(
		t, db, common.DatabaseTypeSQLite,
		[]string{RoutingDeploymentStageCanary, RoutingDeploymentStageCanary, RoutingDeploymentStageShadow},
		RoutingDeploymentStageCanary, 250,
	)
}

func TestRoutingCanaryAutoRollbackLastCanaryChangesActivationToShadow(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	runRoutingCanaryAutoRollbackContract(
		t, db, common.DatabaseTypeSQLite,
		[]string{RoutingDeploymentStageCanary, RoutingDeploymentStageShadow},
		RoutingDeploymentStageShadow, 0,
	)
}

func TestRoutingCanaryAutoRollbackBatchesBreachedPoolsFromSameRevision(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	fixture := newRoutingCanaryRollbackFixture(
		t, db, common.DatabaseTypeSQLite,
		[]string{RoutingDeploymentStageCanary, RoutingDeploymentStageCanary, RoutingDeploymentStageCanary},
	)
	secondOperation := createRoutingCanaryRollbackOperationForPool(
		t,
		fixture,
		fixture.document.Pools[1].PoolID,
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	)

	result, err := AutoRollbackRoutingCanaryPoolContext(context.Background(), RoutingCanaryAutoRollbackRequest{
		Operation:            fixture.claimed,
		Lease:                fixture.lease,
		ExpectedRevision:     fixture.published.Revision.Revision,
		ExpectedActivationID: fixture.published.Activation.ID,
		PoolID:               fixture.targetPoolID,
		NowMs:                20_001,
	})
	require.NoError(t, err)
	assert.False(t, result.Superseded)

	rolledBack, _, err := LoadRoutingPolicyRevisionContext(context.Background(), result.Publish.Revision.Revision)
	require.NoError(t, err)
	wantDocument := cloneRoutingPolicyDocumentForTest(t, fixture.document)
	wantDocument.Pools[0].DeploymentStage = RoutingDeploymentStageShadow
	wantDocument.Pools[1].DeploymentStage = RoutingDeploymentStageShadow
	assert.Equal(t, wantDocument, rolledBack)
	assert.Equal(t, RoutingDeploymentStageCanary, result.Publish.Activation.Stage)
	assert.Equal(t, 250, result.Publish.Activation.TrafficBasisPoints)

	var event RoutingConfigEvent
	require.NoError(t, result.Publish.Outbox.DecodePayload(&event))
	assert.Equal(t, []int{fixture.document.Pools[0].PoolID, fixture.document.Pools[1].PoolID}, event.ChangedPoolIDs)

	var operations []RoutingOperation
	require.NoError(t, DB.Where(
		"expected_revision = ? AND expected_activation_id = ?",
		fixture.published.Revision.Revision,
		fixture.published.Activation.ID,
	).Order("id asc").Find(&operations).Error)
	require.Len(t, operations, 2)
	for index := range operations {
		assert.Equal(t, RoutingOperationStatusSucceeded, operations[index].Status)
		assert.Equal(t, result.Publish.Revision.Revision, operations[index].ResultRevision)
		assert.Equal(t, result.Publish.Activation.ID, operations[index].ResultActivationID)
		assert.Equal(t, result.Publish.Outbox.ID, operations[index].ResultOutboxID)
		stored, getErr := GetRoutingOperationContext(context.Background(), operations[index].ID)
		require.NoError(t, getErr)
		assert.GreaterOrEqual(t, stored.Attempts, 1)
		assert.GreaterOrEqual(t, stored.CompletedTimeMs, stored.CreatedTimeMs)
	}
	assert.Equal(t, secondOperation.ID, operations[1].ID)

	_, err = AutoRollbackRoutingCanaryPoolContext(context.Background(), RoutingCanaryAutoRollbackRequest{
		Operation:            fixture.claimed,
		Lease:                fixture.lease,
		ExpectedRevision:     fixture.published.Revision.Revision,
		ExpectedActivationID: fixture.published.Activation.ID,
		PoolID:               fixture.targetPoolID,
		NowMs:                20_002,
	})
	assert.ErrorIs(t, err, ErrRoutingOperationClaimLost)
	assertRoutingPolicyRowCounts(t, 2, 6, 6, 2, 2)
}

func TestRoutingCanaryAutoRollbackExternalDatabaseCompatibility(t *testing.T) {
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
			runRoutingCanaryAutoRollbackContract(
				t, db, test.dbType,
				[]string{RoutingDeploymentStageCanary, RoutingDeploymentStageCanary},
				RoutingDeploymentStageCanary, 250,
			)
		})
	}
}

func runRoutingCanaryAutoRollbackContract(
	t *testing.T,
	db *gorm.DB,
	dbType common.DatabaseType,
	stages []string,
	wantActivationStage string,
	wantTrafficBasisPoints int,
) {
	t.Helper()
	fixture := newRoutingCanaryRollbackFixture(t, db, dbType, stages)
	var initialEvent RoutingConfigEvent
	require.NoError(t, fixture.published.Outbox.DecodePayload(&initialEvent))
	initialPoolIDs := make([]int, len(fixture.document.Pools))
	for index := range fixture.document.Pools {
		initialPoolIDs[index] = fixture.document.Pools[index].PoolID
	}
	assert.Equal(t, initialPoolIDs, initialEvent.ChangedPoolIDs)

	result, err := AutoRollbackRoutingCanaryPoolContext(context.Background(), RoutingCanaryAutoRollbackRequest{
		Operation:            fixture.claimed,
		Lease:                fixture.lease,
		ExpectedRevision:     fixture.published.Revision.Revision,
		ExpectedActivationID: fixture.published.Activation.ID,
		PoolID:               fixture.targetPoolID,
		NowMs:                20_001,
	})
	require.NoError(t, err)
	assert.False(t, result.Superseded)
	assert.Equal(t, RoutingOperationStatusSucceeded, result.Operation.Status)
	assert.Equal(t, result.Publish.Revision.Revision, result.Operation.ResultRevision)
	assert.Equal(t, result.Publish.Activation.ID, result.Operation.ResultActivationID)
	assert.Equal(t, result.Publish.Outbox.ID, result.Operation.ResultOutboxID)
	assert.Equal(t, fixture.published.Revision.Revision, result.Publish.Revision.RollbackOfRevision)
	assert.Equal(t, wantActivationStage, result.Publish.Activation.Stage)
	assert.Equal(t, wantTrafficBasisPoints, result.Publish.Activation.TrafficBasisPoints)

	rolledBack, _, err := LoadRoutingPolicyRevisionContext(context.Background(), result.Publish.Revision.Revision)
	require.NoError(t, err)
	wantDocument := cloneRoutingPolicyDocumentForTest(t, fixture.document)
	foundTarget := false
	for index := range wantDocument.Pools {
		if wantDocument.Pools[index].PoolID == fixture.targetPoolID {
			wantDocument.Pools[index].DeploymentStage = RoutingDeploymentStageShadow
			foundTarget = true
		}
	}
	require.True(t, foundTarget)
	assert.Equal(t, wantDocument, rolledBack)

	var event RoutingConfigEvent
	require.NoError(t, result.Publish.Outbox.DecodePayload(&event))
	assert.Equal(t, []int{fixture.targetPoolID}, event.ChangedPoolIDs)

	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, result.Publish.Revision.Revision, head.CurrentRevision)
	assert.Equal(t, result.Publish.Activation.ID, head.CurrentActivationID)
}

func TestRoutingCanaryAutoRollbackSupersedesChangedHead(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	fixture := newRoutingCanaryRollbackFixture(
		t, db, common.DatabaseTypeSQLite,
		[]string{RoutingDeploymentStageCanary, RoutingDeploymentStageCanary},
	)
	createRoutingCanaryRollbackOperationForPool(
		t,
		fixture,
		fixture.document.Pools[1].PoolID,
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	)

	changed := cloneRoutingPolicyDocumentForTest(t, fixture.document)
	changed.Pools[1].Members[0].Weight++
	second, err := PublishRoutingPolicyRevisionContext(
		context.Background(),
		fixture.published.Revision.Revision,
		changed,
		RoutingPolicyActivationSpec{
			Stage: RoutingDeploymentStageCanary, TrafficBasisPoints: 250,
			ActorID: 7, Reason: "unrelated pool update",
		},
	)
	require.NoError(t, err)

	result, err := AutoRollbackRoutingCanaryPoolContext(context.Background(), RoutingCanaryAutoRollbackRequest{
		Operation:            fixture.claimed,
		Lease:                fixture.lease,
		ExpectedRevision:     fixture.published.Revision.Revision,
		ExpectedActivationID: fixture.published.Activation.ID,
		PoolID:               fixture.targetPoolID,
		NowMs:                20_001,
	})
	require.NoError(t, err)
	assert.True(t, result.Superseded)
	assert.Equal(t, RoutingOperationStatusSuperseded, result.Operation.Status)
	assert.Zero(t, result.Publish.Revision.Revision)
	var operations []RoutingOperation
	require.NoError(t, DB.Where(
		"expected_revision = ? AND expected_activation_id = ?",
		fixture.published.Revision.Revision,
		fixture.published.Activation.ID,
	).Order("id asc").Find(&operations).Error)
	require.Len(t, operations, 2)
	for index := range operations {
		assert.Equal(t, RoutingOperationStatusSuperseded, operations[index].Status)
		assert.Equal(t, "routing policy head or activation changed", operations[index].LastError)
		stored, getErr := GetRoutingOperationContext(context.Background(), operations[index].ID)
		require.NoError(t, getErr)
		assert.GreaterOrEqual(t, stored.Attempts, 1)
		assert.GreaterOrEqual(t, stored.CompletedTimeMs, stored.CreatedTimeMs)
	}

	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, second.Revision.Revision, head.CurrentRevision)
	assert.Equal(t, second.Activation.ID, head.CurrentActivationID)
	assertRoutingPolicyRowCounts(t, 2, 4, 4, 2, 2)
}

func TestRoutingCanaryAutoRollbackSupersedesChangedActivation(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	fixture := newRoutingCanaryRollbackFixture(
		t, db, common.DatabaseTypeSQLite,
		[]string{RoutingDeploymentStageCanary},
	)

	replacement := RoutingPolicyActivation{
		Revision:           fixture.published.Revision.Revision,
		PreviousRevision:   0,
		RollbackOfRevision: 0,
		Stage:              RoutingDeploymentStageCanary,
		TrafficBasisPoints: 250,
		ActorID:            8,
		Reason:             "activation replaced",
		CreatedTime:        common.GetTimestamp(),
	}
	require.NoError(t, DB.Create(&replacement).Error)
	require.NoError(t, DB.Model(&RoutingPolicyHead{}).
		Where("id = ?", routingPolicyHeadID).
		Update("current_activation_id", replacement.ID).Error)

	result, err := AutoRollbackRoutingCanaryPoolContext(context.Background(), RoutingCanaryAutoRollbackRequest{
		Operation:            fixture.claimed,
		Lease:                fixture.lease,
		ExpectedRevision:     fixture.published.Revision.Revision,
		ExpectedActivationID: fixture.published.Activation.ID,
		PoolID:               fixture.targetPoolID,
		NowMs:                20_001,
	})
	require.NoError(t, err)
	assert.True(t, result.Superseded)
	assert.Equal(t, RoutingOperationStatusSuperseded, result.Operation.Status)
	assertRoutingPolicyRowCounts(t, 1, 1, 1, 2, 1)
}

func TestRoutingCanaryAutoRollbackSupersedesChangedHeadHash(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	fixture := newRoutingCanaryRollbackFixture(
		t, db, common.DatabaseTypeSQLite,
		[]string{RoutingDeploymentStageCanary},
	)
	require.NoError(t, DB.Model(&RoutingPolicyHead{}).
		Where("id = ?", routingPolicyHeadID).
		Update("current_hash", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff").Error)

	result, err := AutoRollbackRoutingCanaryPoolContext(context.Background(), RoutingCanaryAutoRollbackRequest{
		Operation:            fixture.claimed,
		Lease:                fixture.lease,
		ExpectedRevision:     fixture.published.Revision.Revision,
		ExpectedActivationID: fixture.published.Activation.ID,
		PoolID:               fixture.targetPoolID,
		NowMs:                20_001,
	})
	require.NoError(t, err)
	assert.True(t, result.Superseded)
	assert.Equal(t, RoutingOperationStatusSuperseded, result.Operation.Status)
	assert.Equal(t, "routing policy head hash changed", result.Operation.LastError)
	assertRoutingPolicyRowCounts(t, 1, 1, 1, 1, 1)
}

func TestRoutingCanaryAutoRollbackRejectsStaleFencingLeaseWithoutMutation(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	fixture := newRoutingCanaryRollbackFixture(
		t, db, common.DatabaseTypeSQLite,
		[]string{RoutingDeploymentStageCanary},
	)
	require.NoError(t, db.Model(&RoutingControlLease{}).
		Where("lease_name = ?", fixture.lease.LeaseName).Update("lease_until_ms", 0).Error)
	newer, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), fixture.lease.LeaseName, "node-b", 10_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	assert.Greater(t, newer.FencingToken, fixture.lease.FencingToken)

	headBefore, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	var operationBefore RoutingOperation
	require.NoError(t, DB.First(&operationBefore, fixture.claimed.ID).Error)

	_, err = AutoRollbackRoutingCanaryPoolContext(context.Background(), RoutingCanaryAutoRollbackRequest{
		Operation:            fixture.claimed,
		Lease:                fixture.lease,
		ExpectedRevision:     fixture.published.Revision.Revision,
		ExpectedActivationID: fixture.published.Activation.ID,
		PoolID:               fixture.targetPoolID,
		NowMs:                30_002,
	})
	assert.ErrorIs(t, err, ErrRoutingControlLeaseLost)

	headAfter, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	var operationAfter RoutingOperation
	require.NoError(t, DB.First(&operationAfter, fixture.claimed.ID).Error)
	assert.Equal(t, headBefore, headAfter)
	assert.Equal(t, operationBefore, operationAfter)
	assertRoutingPolicyRowCounts(t, 1, 1, 1, 1, 1)
}

func TestRoutingCanaryAutoRollbackPublishesAndCompletesOperationAtomically(t *testing.T) {
	db := openRoutingSQLiteTestDB(t)
	fixture := newRoutingCanaryRollbackFixture(
		t, db, common.DatabaseTypeSQLite,
		[]string{RoutingDeploymentStageCanary},
	)
	require.NoError(t, DB.Exec(`
		CREATE TRIGGER fail_routing_operation_success
		BEFORE UPDATE OF status ON routing_operations
		WHEN NEW.status = 'succeeded'
		BEGIN
			SELECT RAISE(FAIL, 'forced operation completion failure');
		END
	`).Error)

	_, err := AutoRollbackRoutingCanaryPoolContext(context.Background(), RoutingCanaryAutoRollbackRequest{
		Operation:            fixture.claimed,
		Lease:                fixture.lease,
		ExpectedRevision:     fixture.published.Revision.Revision,
		ExpectedActivationID: fixture.published.Activation.ID,
		PoolID:               fixture.targetPoolID,
		NowMs:                20_001,
	})
	require.Error(t, err)

	head, err := GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, fixture.published.Revision.Revision, head.CurrentRevision)
	assert.Equal(t, fixture.published.Activation.ID, head.CurrentActivationID)
	var operation RoutingOperation
	require.NoError(t, DB.First(&operation, fixture.claimed.ID).Error)
	assert.Equal(t, RoutingOperationStatusRunning, operation.Status)
	assert.Equal(t, fixture.claimed.ClaimToken, operation.ClaimToken)
	assertRoutingPolicyRowCounts(t, 1, 1, 1, 1, 1)
}

type routingCanaryRollbackFixture struct {
	document     RoutingPolicyDocument
	published    RoutingPolicyPublishResult
	lease        RoutingControlLease
	claimed      RoutingOperation
	targetPoolID int
}

func newRoutingCanaryRollbackFixture(
	t *testing.T,
	db *gorm.DB,
	dbType common.DatabaseType,
	stages []string,
) routingCanaryRollbackFixture {
	t.Helper()
	withRoutingTestDB(t, db, dbType)
	require.NoError(t, migrateRoutingCanaryRollbackModelsForTest(db))
	require.NoError(t, EnsureRoutingPolicyHeadContext(context.Background()))

	document := routingCanaryRollbackDocumentForTest(stages)
	require.NoError(t, seedRoutingPolicyLiveReferencesForTest(db, document))
	published, err := PublishRoutingPolicyRevisionContext(
		context.Background(), 0, document,
		RoutingPolicyActivationSpec{
			Stage: RoutingDeploymentStageCanary, TrafficBasisPoints: 250,
			ActorID: 6, Reason: "canary rollout",
		},
	)
	require.NoError(t, err)
	document, _, err = LoadRoutingPolicyRevisionContext(context.Background(), published.Revision.Revision)
	require.NoError(t, err)

	targetPoolID := document.Pools[0].PoolID
	evaluationSpec := routingCanaryEvaluationSpecForTest()
	evaluationSpec.PolicyRevision = published.Revision.Revision
	evaluationSpec.ActivationID = published.Activation.ID
	evaluationSpec.PoolID = targetPoolID
	evaluationSpec.RolloutKey = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	evaluation, _, err := CreateRoutingCanaryEvaluationContext(context.Background(), evaluationSpec)
	require.NoError(t, err)
	operation, _, err := CreateRoutingOperationContext(context.Background(), RoutingOperationSpec{
		Type:                 RoutingOperationTypeCanaryAutoRollback,
		EvaluationHash:       evaluation.EvaluationHash,
		PoolID:               targetPoolID,
		ExpectedRevision:     published.Revision.Revision,
		ExpectedActivationID: published.Activation.ID,
		ActorID:              0,
		Reason:               evaluation.Reason,
	})
	require.NoError(t, err)
	claimed, err := ClaimRoutingOperationContext(
		context.Background(), RoutingOperationTypeCanaryAutoRollback, operation.CreatedTimeMs, 60_000,
	)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, operation.ID, claimed.ID)

	lease, acquired, err := TryAcquireRoutingControlLeaseContext(
		context.Background(), "canary-auto-rollback", "node-a", 10_000, 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)

	return routingCanaryRollbackFixture{
		document: document, published: published, lease: lease, claimed: *claimed, targetPoolID: targetPoolID,
	}
}

func createRoutingCanaryRollbackOperationForPool(
	t *testing.T,
	fixture routingCanaryRollbackFixture,
	poolID int,
	rolloutKey string,
) RoutingOperation {
	t.Helper()
	evaluationSpec := routingCanaryEvaluationSpecForTest()
	evaluationSpec.PolicyRevision = fixture.published.Revision.Revision
	evaluationSpec.ActivationID = fixture.published.Activation.ID
	evaluationSpec.PoolID = poolID
	evaluationSpec.RolloutKey = rolloutKey
	evaluation, _, err := CreateRoutingCanaryEvaluationContext(context.Background(), evaluationSpec)
	require.NoError(t, err)
	operation, _, err := CreateRoutingOperationContext(context.Background(), RoutingOperationSpec{
		Type:                 RoutingOperationTypeCanaryAutoRollback,
		EvaluationHash:       evaluation.EvaluationHash,
		PoolID:               poolID,
		ExpectedRevision:     fixture.published.Revision.Revision,
		ExpectedActivationID: fixture.published.Activation.ID,
		ActorID:              0,
		Reason:               evaluation.Reason,
	})
	require.NoError(t, err)
	return operation
}

func routingCanaryRollbackDocumentForTest(stages []string) RoutingPolicyDocument {
	document := RoutingPolicyDocument{
		SchemaVersion: RoutingPolicySchemaVersion,
		Pools:         make([]RoutingPolicyPoolContent, len(stages)),
	}
	for index, stage := range stages {
		document.Pools[index] = RoutingPolicyPoolContent{
			PoolID:          101 + index,
			GroupName:       "canary-group-" + string(rune('a'+index)),
			DisplayName:     "Canary Group " + string(rune('A'+index)),
			DeploymentStage: stage,
			PolicyProfile:   RoutingPolicyProfileBalanced,
			Policy:          json.RawMessage(`{"z":2,"nested":{"b":2,"a":1},"a":1}`),
			Members: []RoutingPolicyMemberContent{{
				MemberID:      1_001 + index,
				ChannelID:     2_001 + index,
				Enabled:       true,
				Priority:      int64(index + 1),
				Weight:        int64(100 + index),
				CredentialIDs: []int{3_002 + index*2, 3_001 + index*2},
				Overrides:     json.RawMessage(`{"timeout_ms":900,"headers":{"z":"last","a":"first"}}`),
			}},
		}
	}
	return document
}

func cloneRoutingPolicyDocumentForTest(t *testing.T, document RoutingPolicyDocument) RoutingPolicyDocument {
	t.Helper()
	encoded, err := common.Marshal(document)
	require.NoError(t, err)
	var cloned RoutingPolicyDocument
	require.NoError(t, common.Unmarshal(encoded, &cloned))
	return cloned
}

func migrateRoutingCanaryRollbackModelsForTest(db *gorm.DB) error {
	return db.AutoMigrate(
		&Channel{},
		&RoutingCredentialRef{},
		&RoutingPolicyHead{},
		&RoutingPolicyRevision{},
		&RoutingPolicyPoolRevision{},
		&RoutingPolicyMemberRevision{},
		&RoutingPolicyActivation{},
		&RoutingConfigOutbox{},
		&RoutingControlLease{},
		&RoutingCanaryEvaluation{},
		&RoutingOperation{},
	)
}
