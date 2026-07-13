package model

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRoutingBreakerResetExternalDatabaseCompatibility(t *testing.T) {
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
			require.NoError(t, db.AutoMigrate(
				&RoutingOperation{}, &RoutingBreakerResetCommand{}, &RoutingBreakerResetFence{},
				&RoutingBreakerResetTombstone{}, &RoutingBreakerResetOutbox{}, &RoutingBreakerState{},
				&RoutingEndpointEvidence{}, &RoutingEndpointSharedState{},
				&RoutingPolicyHead{}, &RoutingPolicyPoolRevision{}, &RoutingPolicyMemberRevision{},
			))
			require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))
			assert.True(t, RoutingBreakerResetSchemaReady())

			target := routingBreakerResetMemberTargetForTest()
			seedRoutingBreakerResetPolicyForTest(t, db, target, 1, 1)
			operation, created, err := CreateLegacyRoutingBreakerResetOperationContext(
				context.Background(), routingBreakerResetOperationSpecForTest(target, "a"), target, 101, 0,
			)
			require.NoError(t, err)
			assert.True(t, created)
			execution := claimAndExecuteRoutingBreakerResetForTest(t)
			assert.Equal(t, RoutingOperationStatusSucceeded, execution.Operation.Status)
			assert.Equal(t, int64(1), execution.Event.Generation)
			assert.Positive(t, execution.Outbox.ID)
			decoded, err := execution.Outbox.DecodePayload()
			require.NoError(t, err)
			assert.Equal(t, execution.Event, decoded)
			var result struct {
				Target RoutingBreakerResetTarget `json:"target"`
			}
			require.NoError(t, common.UnmarshalJsonStr(execution.Operation.ResultPayloadJSON, &result))
			assert.Equal(t, target, result.Target)
			storedOperation, command, err := GetLatestLegacyRoutingBreakerResetContext(context.Background(), 101)
			require.NoError(t, err)
			assert.Equal(t, operation.ID, storedOperation.ID)
			assert.Equal(t, 101, command.LegacyBreakerID)
			assert.Zero(t, command.LegacyGeneration)
		})
	}
}

func TestRoutingBreakerResetMemberOperationIsIdempotentAndFencesStaleSnapshots(t *testing.T) {
	db := routingBreakerResetTestDB(t)
	target := routingBreakerResetMemberTargetForTest()
	seedRoutingBreakerResetPolicyForTest(t, db, target, 1, 1)
	eligibility := LegacyRoutingStateEligibility{
		channelID: target.ChannelID, apiKeyIndex: target.APIKeyIndex, supported: true,
	}
	stale := RoutingBreakerState{
		ChannelID: target.ChannelID, APIKeyIndex: target.APIKeyIndex,
		ModelName: target.ModelName, Group: target.GroupName,
		State: RoutingBreakerStateOpen, Reason: "provider_5xx", UpdatedTime: time.Now().Unix(),
	}
	require.NoError(t, eligibility.upsertRoutingBreakerState(db, &stale))

	spec := routingBreakerResetOperationSpecForTest(target, "a")
	first, created, err := CreateRoutingBreakerResetOperationContext(context.Background(), spec, target)
	require.NoError(t, err)
	require.True(t, created)
	second, created, err := CreateRoutingBreakerResetOperationContext(context.Background(), spec, target)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, second.ID)
	driftedSpec := spec
	driftedSpec.ExpectedRevision++
	driftedSpec.ExpectedActivationID++
	drifted, created, err := CreateRoutingBreakerResetOperationContext(context.Background(), driftedSpec, target)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, drifted.ID, "idempotent replay must survive a later control-plane revision")

	var commandCount int64
	require.NoError(t, db.Model(&RoutingBreakerResetCommand{}).Count(&commandCount).Error)
	assert.Equal(t, int64(1), commandCount)

	execution := claimAndExecuteRoutingBreakerResetForTest(t)
	assert.Equal(t, first.ID, execution.Operation.ID)
	assert.Equal(t, RoutingOperationStatusSucceeded, execution.Operation.Status)
	assert.Equal(t, int64(1), execution.Tombstone.Generation)
	assert.Equal(t, int64(1), execution.Event.Generation)
	assert.Equal(t, execution.Outbox.ID, execution.Event.OutboxID)

	var persistedCount int64
	require.NoError(t, db.Model(&RoutingBreakerState{}).
		Where("channel_id = ? AND api_key_index = ? AND model_name = ? AND "+commonGroupCol+" = ?",
			target.ChannelID, target.APIKeyIndex, target.ModelName, target.GroupName).
		Count(&persistedCount).Error)
	assert.Zero(t, persistedCount)

	stale.ID = 0
	stale.UpdatedTime++
	require.NoError(t, eligibility.upsertRoutingBreakerState(db, &stale))
	require.NoError(t, db.Model(&RoutingBreakerState{}).Count(&persistedCount).Error)
	assert.Zero(t, persistedCount, "a pre-reset dirty snapshot must not resurrect the deleted breaker")

	current := stale
	current.ResetGeneration = execution.Tombstone.Generation
	current.State = RoutingBreakerStateHealthy
	current.Reason = ""
	current.UpdatedTime++
	require.NoError(t, eligibility.upsertRoutingBreakerState(db, &current))
	require.NoError(t, db.Model(&RoutingBreakerState{}).Count(&persistedCount).Error)
	assert.Equal(t, int64(1), persistedCount)
}

func TestRoutingBreakerResetEndpointFencesLateEvidenceAndSharedEvaluation(t *testing.T) {
	db := routingBreakerResetTestDB(t)
	target := RoutingBreakerResetTarget{
		Scope: RoutingBreakerResetScopeEndpoint, EndpointHost: "api.reset.test",
		EndpointAuthority: "https://api.reset.test:443", Region: "test-region",
	}
	nowMs, err := RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	bucket := nowMs / 1_000 / 60 * 60
	staleEvidence := RoutingEndpointEvidence{
		NodeID: "node-a", NodeEpochID: "epoch-a", QuorumEligible: true,
		EndpointHost: target.EndpointHost, EndpointAuthority: target.EndpointAuthority, Region: target.Region,
		BucketTs: bucket, RequestCount: 10, ReachableCount: 1, NetworkFailureCount: 9,
		TotalLatencyMs: 100, TtftSumMs: 50, TtftCount: 10,
	}
	_, err = UpsertRoutingEndpointEvidenceContext(context.Background(), []RoutingEndpointEvidence{staleEvidence})
	require.NoError(t, err)
	staleShared := RoutingEndpointSharedState{
		EndpointHost: target.EndpointHost, EndpointAuthority: target.EndpointAuthority, Region: target.Region,
		State: RoutingBreakerStateOpen, Reason: "regional_network_quorum",
		EvidenceCount: 10, NetworkFailureCount: 9, NodeCount: 2, FailureNodeCount: 2,
		CooldownUntilMs: nowMs + 30_000, EvidenceFromMs: nowMs - 60_000,
		EvidenceThroughMs: nowMs - 1_000, EvaluatedAtMs: nowMs,
		ExpiresAtMs: nowMs + 60_000, CreatedTimeMs: nowMs, UpdatedTimeMs: nowMs,
	}
	require.NoError(t, UpsertRoutingEndpointSharedStatesContext(context.Background(), []RoutingEndpointSharedState{staleShared}))

	_, _, err = CreateRoutingBreakerResetOperationContext(
		context.Background(), routingBreakerResetOperationSpecForTest(target, "b"), target,
	)
	require.NoError(t, err)
	execution := claimAndExecuteRoutingBreakerResetForTest(t)
	require.Equal(t, int64(1), execution.Tombstone.Generation)
	var result struct {
		Target RoutingBreakerResetTarget `json:"target"`
	}
	require.NoError(t, common.UnmarshalJsonStr(execution.Operation.ResultPayloadJSON, &result))
	assert.Equal(t, target, result.Target)

	var evidenceCount int64
	var sharedCount int64
	require.NoError(t, db.Model(&RoutingEndpointEvidence{}).Count(&evidenceCount).Error)
	require.NoError(t, db.Model(&RoutingEndpointSharedState{}).Count(&sharedCount).Error)
	assert.Zero(t, evidenceCount)
	assert.Zero(t, sharedCount)

	rows, err := UpsertRoutingEndpointEvidenceContext(context.Background(), []RoutingEndpointEvidence{staleEvidence})
	require.NoError(t, err)
	assert.Zero(t, rows)
	require.NoError(t, UpsertRoutingEndpointSharedStatesContext(context.Background(), []RoutingEndpointSharedState{staleShared}))
	require.NoError(t, db.Model(&RoutingEndpointEvidence{}).Count(&evidenceCount).Error)
	require.NoError(t, db.Model(&RoutingEndpointSharedState{}).Count(&sharedCount).Error)
	assert.Zero(t, evidenceCount, "pre-reset endpoint evidence must be fenced")
	assert.Zero(t, sharedCount, "a pre-reset evaluator must not recreate shared state")

	currentEvidence := staleEvidence
	currentEvidence.ResetGeneration = execution.Tombstone.Generation
	rows, err = UpsertRoutingEndpointEvidenceContext(context.Background(), []RoutingEndpointEvidence{currentEvidence})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
}

func TestRoutingBreakerResetOutboxClaimIsFenced(t *testing.T) {
	routingBreakerResetTestDB(t)
	target := routingBreakerResetMemberTargetForTest()
	seedRoutingBreakerResetPolicyForTest(t, DB, target, 1, 1)
	_, _, err := CreateRoutingBreakerResetOperationContext(
		context.Background(), routingBreakerResetOperationSpecForTest(target, "c"), target,
	)
	require.NoError(t, err)
	execution := claimAndExecuteRoutingBreakerResetForTest(t)
	nowMs, err := RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	claimed, err := ClaimRoutingBreakerResetOutboxContext(context.Background(), nowMs, 30_000)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, execution.Outbox.ID, claimed.ID)
	_, err = claimed.DecodePayload()
	require.NoError(t, err)
	require.ErrorIs(t, MarkRoutingBreakerResetOutboxPublishedContext(
		context.Background(), claimed.ID, strings.Repeat("0", 32), nowMs+1,
	), ErrRoutingBreakerResetClaimLost)
	require.NoError(t, MarkRoutingBreakerResetOutboxPublishedContext(
		context.Background(), claimed.ID, claimed.ClaimToken, nowMs+1,
	))
	none, err := ClaimRoutingBreakerResetOutboxContext(context.Background(), nowMs+2, 30_000)
	require.NoError(t, err)
	assert.Nil(t, none)
}

func TestRoutingBreakerResetSupersedesDriftedMemberTargetAndRemainsReplayable(t *testing.T) {
	db := routingBreakerResetTestDB(t)
	target := routingBreakerResetMemberTargetForTest()
	seedRoutingBreakerResetPolicyForTest(t, db, target, 1, 1)
	spec := routingBreakerResetOperationSpecForTest(target, "d")
	operation, _, err := CreateRoutingBreakerResetOperationContext(context.Background(), spec, target)
	require.NoError(t, err)
	require.NoError(t, db.Model(&RoutingPolicyHead{}).Where("id = ?", routingPolicyHeadID).Updates(map[string]any{
		"current_revision": 2, "current_activation_id": 2,
	}).Error)
	nowMs, err := RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	claimed, err := ClaimRoutingOperationContext(context.Background(), RoutingOperationTypeBreakerReset, nowMs, 30_000)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	execution, err := ExecuteRoutingBreakerResetOperationContext(context.Background(), *claimed)
	require.NoError(t, err)
	assert.Equal(t, RoutingOperationStatusSuperseded, execution.Operation.Status)
	assert.Zero(t, execution.Event.Generation)
	assert.Zero(t, execution.Command.Generation)
	assert.Positive(t, execution.Command.CompletedTimeMs)

	command, err := GetRoutingBreakerResetCommandByOperationContext(context.Background(), operation.ID)
	require.NoError(t, err)
	assert.Zero(t, command.OutboxID)
	replayed, created, err := CreateRoutingBreakerResetOperationContext(context.Background(), spec, target)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, RoutingOperationStatusSuperseded, replayed.Status)
	var outboxCount int64
	require.NoError(t, db.Model(&RoutingBreakerResetOutbox{}).Count(&outboxCount).Error)
	assert.Zero(t, outboxCount)
}

func routingBreakerResetTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	withRoutingTestDB(t, db, common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(
		&RoutingOperation{}, &RoutingBreakerResetCommand{}, &RoutingBreakerResetFence{},
		&RoutingBreakerResetTombstone{}, &RoutingBreakerResetOutbox{}, &RoutingBreakerState{},
		&RoutingEndpointEvidence{}, &RoutingEndpointSharedState{},
		&RoutingPolicyHead{}, &RoutingPolicyPoolRevision{}, &RoutingPolicyMemberRevision{},
	))
	require.NoError(t, ensureRoutingOperationRequestKeyUniqueIndex(db))
	return db
}

func seedRoutingBreakerResetPolicyForTest(
	t *testing.T,
	db *gorm.DB,
	target RoutingBreakerResetTarget,
	revision int64,
	activationID int64,
) {
	t.Helper()
	require.NoError(t, db.Create(&RoutingPolicyHead{
		ID: routingPolicyHeadID, CurrentRevision: revision, CurrentActivationID: activationID,
		CurrentHash: strings.Repeat("f", 64), CurrentStage: RoutingDeploymentStageActive,
		CreatedTime: 1, UpdatedTime: 1,
	}).Error)
	require.NoError(t, db.Create(&RoutingPolicyPoolRevision{
		Revision: revision, PoolID: target.PoolID, GroupKey: strings.Repeat("e", 64),
		GroupName: target.GroupName, DisplayName: target.GroupName, DeploymentStage: RoutingDeploymentStageActive,
		PolicyProfile: "balanced", PolicyJSON: `{}`,
	}).Error)
	require.NoError(t, db.Create(&RoutingPolicyMemberRevision{
		Revision: revision, PoolID: target.PoolID, MemberID: target.MemberID, ChannelID: target.ChannelID,
		Enabled: true, Weight: 1, CredentialIDsJSON: `[]`, OverridesJSON: `{}`,
	}).Error)
}

func routingBreakerResetMemberTargetForTest() RoutingBreakerResetTarget {
	return RoutingBreakerResetTarget{
		Scope: RoutingBreakerResetScopeMember, PoolID: 11, MemberID: 22, ChannelID: 33,
		APIKeyIndex: RoutingMetricSingleKeyIndex, ModelName: "gpt-reset", GroupName: "default",
	}
}

func routingBreakerResetOperationSpecForTest(target RoutingBreakerResetTarget, key string) RoutingOperationSpec {
	subjectType := RoutingOperationSubjectEndpointBreaker
	subjectID := int64(0)
	poolID := 0
	if target.Scope == RoutingBreakerResetScopeMember {
		subjectType = RoutingOperationSubjectMemberBreaker
		subjectID = int64(target.MemberID)
		poolID = target.PoolID
	}
	return RoutingOperationSpec{
		Type: RoutingOperationTypeBreakerReset, EvaluationHash: strings.Repeat(key, 64),
		SubjectType: subjectType, SubjectID: subjectID, PoolID: poolID,
		ExpectedRevision: 1, ExpectedActivationID: 1, ActorID: 7, Reason: "manual reset",
		RequestKeyHash: strings.Repeat(key, 64), RequestPayloadHash: strings.Repeat(string(key[0]+1), 64),
	}
}

func claimAndExecuteRoutingBreakerResetForTest(t *testing.T) RoutingBreakerResetExecution {
	t.Helper()
	nowMs, err := RoutingEndpointDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	claimed, err := ClaimRoutingOperationContext(context.Background(), RoutingOperationTypeBreakerReset, nowMs, 30_000)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, RenewRoutingOperationClaimContext(
		context.Background(), claimed.ID, claimed.ClaimToken, nowMs+1, 30_000,
	))
	execution, err := ExecuteRoutingBreakerResetOperationContext(context.Background(), *claimed)
	require.NoError(t, err)
	return execution
}
