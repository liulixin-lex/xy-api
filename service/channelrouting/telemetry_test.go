package channelrouting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingmetrics "github.com/QuantumNous/new-api/pkg/routing_metrics"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFlushStableTelemetryPersistsAllBatchesAndRequeuesOnlyFailedTail(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}, &model.RoutingTelemetryReceipt{}))
	withSnapshotTestDB(t, db)
	enableStableTelemetryTest(t)

	snapshots := make([]routingmetrics.StableSnapshot, model.RoutingTelemetryMaxItems+1)
	for index := range snapshots {
		snapshots[index] = routingmetrics.StableSnapshot{
			PoolID: 1, PoolMemberID: index + 1, CredentialID: index + 1, ChannelID: index + 1,
			Model: fmt.Sprintf("model-%03d", index), BucketTs: 60, LastSnapshotRevision: 1,
			RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
		}
	}
	routingmetrics.RequeueStableSnapshots(snapshots)

	var createCalls int
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("routing_rollup_fail_second_batch", func(tx *gorm.DB) {
		if tx.Statement.Table != "routing_telemetry_receipts" {
			return
		}
		createCalls++
		if createCalls == 2 {
			tx.AddError(errors.New("second batch failed"))
		}
	}))

	flushed, err := FlushStableTelemetryContext(context.Background())
	require.ErrorContains(t, err, "second batch failed")
	assert.Equal(t, model.RoutingTelemetryMaxItems, flushed)
	var persisted int64
	require.NoError(t, db.Model(&model.RoutingMetricRollup{}).Count(&persisted).Error)
	assert.Equal(t, int64(model.RoutingTelemetryMaxItems), persisted)
	assert.Empty(t, routingmetrics.StableSnapshots(), "drained transport envelopes must not remain in the live snapshot overlay")
	stats := RoutingTelemetryTransportRuntimeStats()
	assert.Equal(t, 1, stats.PendingEnvelopes)
	assert.Equal(t, 1, stats.PendingItems)

	require.NoError(t, db.Callback().Create().Remove("routing_rollup_fail_second_batch"))
	flushed, err = FlushStableTelemetryContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	assert.Empty(t, routingmetrics.StableSnapshots())
	require.NoError(t, db.Model(&model.RoutingMetricRollup{}).Count(&persisted).Error)
	assert.Equal(t, int64(model.RoutingTelemetryMaxItems+1), persisted)
	assert.Zero(t, RoutingTelemetryTransportRuntimeStats().PendingEnvelopes)
}

func TestFlushStableTelemetryDatabaseFailureKeepsLiveDelta(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	withSnapshotTestDB(t, db)
	enableStableTelemetryTest(t)
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: 1, PoolMemberID: 1, CredentialID: 0, ChannelID: 1,
		Model: "keyless", BucketTs: 60, LastSnapshotRevision: 1, RequestCount: 1, SuccessCount: 1,
	}})

	flushed, err := FlushStableTelemetryContext(context.Background())
	require.Error(t, err)
	assert.Zero(t, flushed)
	assert.Empty(t, routingmetrics.StableSnapshots())
	stats := RoutingTelemetryTransportRuntimeStats()
	assert.Equal(t, 1, stats.PendingEnvelopes)
	assert.Equal(t, 1, stats.PendingItems)
}

func TestFlushStableTelemetryHonorsPerInvocationEnvelopeBudget(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}, &model.RoutingTelemetryReceipt{}))
	withSnapshotTestDB(t, db)
	enableStableTelemetryTest(t)

	snapshotCount := stableTelemetryFlushMaxEnvelopes*model.RoutingTelemetryMaxItems + 1
	snapshots := make([]routingmetrics.StableSnapshot, snapshotCount)
	for index := range snapshots {
		snapshots[index] = routingmetrics.StableSnapshot{
			PoolID: 1, PoolMemberID: index + 1, CredentialID: index + 1, ChannelID: index + 1,
			Model: fmt.Sprintf("model-%03d", index), BucketTs: 60, LastSnapshotRevision: 1,
			RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
		}
	}
	routingmetrics.RequeueStableSnapshots(snapshots)

	flushed, err := FlushStableTelemetryContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, stableTelemetryFlushMaxEnvelopes*model.RoutingTelemetryMaxItems, flushed)
	assert.Equal(t, int64(1), routingmetrics.StableRuntimeStats().Buckets)

	lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, lockRoutingTelemetry(lockCtx), "the flush budget must release snapshot maintenance promptly")
	unlockRoutingTelemetry()

	flushed, err = FlushStableTelemetryContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, flushed)
	assert.Zero(t, routingmetrics.StableRuntimeStats().Buckets)
}

func TestDefaultLocalDrainFlushesStableTelemetryToStream(t *testing.T) {
	enableStableTelemetryTest(t)
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = common.RDB.Close() })
	stub := &routingTelemetryRedisStub{}
	withRoutingTelemetryRedis(t, stub)
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: 1, PoolMemberID: 1, CredentialID: 0, ChannelID: 1,
		Model: "gpt-test", BucketTs: 60, LastSnapshotRevision: 1,
		RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
	}})

	err := defaultRuntimeDeps().drainLocal(
		context.Background(), smart_routing_setting.GetSetting(),
	)
	require.NoError(t, err)
	assert.Empty(t, routingmetrics.StableSnapshots())
	assert.Equal(t, int64(1), RoutingTelemetryTransportRuntimeStats().Published)
}

func TestDefaultLocalDrainDrainsAllDecisionAuditBatches(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingDecisionAudit{}, &model.RoutingMetricRollup{}, &model.RoutingTelemetryReceipt{}))
	withSnapshotTestDB(t, db)
	enableStableTelemetryTest(t)
	ResetDecisionAuditsForTest(model.RoutingDecisionAuditMaxBatch + 1)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	for index := 0; index < model.RoutingDecisionAuditMaxBatch+1; index++ {
		_, err := EnqueueDecision(DecisionInput{
			RequestID:        fmt.Sprintf("request-%03d", index),
			PoolID:           1,
			SnapshotRevision: 1,
			GroupName:        "default",
			ModelName:        "gpt-test",
		})
		require.NoError(t, err)
	}

	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = common.RDB.Close() })
	withRoutingTelemetryRedis(t, &routingTelemetryRedisStub{})

	err = defaultRuntimeDeps().drainLocal(context.Background(), smart_routing_setting.GetSetting())
	require.NoError(t, err)
	var persisted int64
	require.NoError(t, db.Model(&model.RoutingDecisionAudit{}).Count(&persisted).Error)
	assert.Equal(t, int64(model.RoutingDecisionAuditMaxBatch+1), persisted)
	stats := DecisionAuditsStats()
	assert.Zero(t, stats.Entries)
	assert.Equal(t, int64(model.RoutingDecisionAuditMaxBatch+1), stats.Flushed)
}

func TestDefaultLocalDrainFlushesAuditWhenStableTelemetryFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingDecisionAudit{}))
	withSnapshotTestDB(t, db)
	enableStableTelemetryTest(t)
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	routingmetrics.RequeueStableSnapshots([]routingmetrics.StableSnapshot{{
		PoolID: 1, PoolMemberID: 1, CredentialID: 1, ChannelID: 1,
		Model: "gpt-test", BucketTs: 60, LastSnapshotRevision: 1,
		RequestCount: 1, SuccessCount: 1, ReliabilityRequestCount: 1,
	}})
	_, err = EnqueueDecision(DecisionInput{
		PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: "gpt-test",
	})
	require.NoError(t, err)

	err = defaultRuntimeDeps().drainLocal(context.Background(), smart_routing_setting.GetSetting())
	require.Error(t, err)
	assert.Zero(t, DecisionAuditsStats().Entries)
	var persisted int64
	require.NoError(t, db.Model(&model.RoutingDecisionAudit{}).Count(&persisted).Error)
	assert.Equal(t, int64(1), persisted)
	assert.Equal(t, 1, RoutingTelemetryTransportRuntimeStats().PendingEnvelopes)
}

func TestTelemetryStreamConsumerRunsWhenAuditPersistenceFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	withSnapshotTestDB(t, db)
	enableStableTelemetryTest(t)
	ResetDecisionAuditsForTest(2)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })
	_, err = EnqueueDecision(DecisionInput{
		PoolID: 1, SnapshotRevision: 1, GroupName: "default", ModelName: "gpt-test",
	})
	require.NoError(t, err)
	require.Error(t, defaultRuntimeDeps().drainLocal(context.Background(), smart_routing_setting.GetSetting()))

	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = common.RDB.Close() })
	stub := &routingTelemetryRedisStub{}
	withRoutingTelemetryRedis(t, stub)

	require.NoError(t, defaultRuntimeDeps().consumeTelemetry(context.Background(), smart_routing_setting.GetSetting()))
	assert.Equal(t, 1, stub.readCalls)
	assert.Equal(t, 1, DecisionAuditsStats().Entries)
}

func TestDeleteExpiredRoutingHistoryCleansRollupsAndAudits(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingMetricRollup{}, &model.RoutingDecisionAudit{}, &model.RoutingDecisionReplayChunk{}, &model.RoutingTelemetryReceipt{},
		&model.RoutingConfigOutbox{}, &model.RoutingRuntimeCheckpoint{}, &model.RoutingCostSnapshotVersion{},
		&model.RoutingProbeResult{}, &model.RoutingControlLease{},
		&model.RoutingBreakerResetFence{}, &model.RoutingEndpointEvidence{}, &model.RoutingEndpointSharedState{},
		&model.RoutingErrorBudgetCursor{}, &model.RoutingErrorBudgetHistory{},
		&model.RoutingAuditExport{}, &model.RoutingAuditExportChunk{},
		&model.RoutingOperation{}, &model.RoutingBreakerResetCommand{},
		&model.RoutingBreakerResetTombstone{}, &model.RoutingBreakerResetOutbox{},
		&model.RoutingPolicyHead{}, &model.RoutingPolicyDraft{}, &model.RoutingPolicyApproval{},
		&model.RoutingPolicyRollbackApproval{}, &model.RoutingCanaryEvaluation{},
	))
	withSnapshotTestDB(t, db)
	require.NoError(t, db.Create(&model.RoutingPolicyHead{ID: 1}).Error)

	require.NoError(t, model.UpsertRoutingMetricRollupsContext(context.Background(), []model.RoutingMetricRollup{{
		MemberID: 1, CredentialID: 0, ModelName: "old", BucketTs: 1,
		ChannelID: 1, PoolID: 1, LastSnapshotRevision: 1, RequestCount: 1,
	}}))
	require.NoError(t, db.Create(&model.RoutingDecisionAudit{DecisionID: "old", CreatedTime: 1}).Error)
	require.NoError(t, db.Create(&model.RoutingTelemetryReceipt{
		NodeID: "old-node", NodeKey: fmt.Sprintf("%064x", 1), Sequence: 1,
		PayloadHash: fmt.Sprintf("%064x", 2), ApplyToken: fmt.Sprintf("%032x", 3),
		ItemCount: 1, ProducedAtMs: 1, AppliedAt: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingConfigOutbox{
		EventID: "old-event", Revision: 1, EventType: model.RoutingConfigEventPolicyRevision,
		PayloadJSON: `{}`, PayloadHash: fmt.Sprintf("%064x", 4), CreatedTime: 1, PublishedTime: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingRuntimeCheckpoint{
		IdentityKey: fmt.Sprintf("%064x", 5), NodeID: "node", CheckpointKind: "config_stream",
		Scope: "stream", ScopeHash: fmt.Sprintf("%064x", 6), PayloadJSON: `{}`,
		PayloadHash: fmt.Sprintf("%064x", 7), ExpiresTime: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingCostSnapshotVersion{
		SchemaVersion: 1, PricingHash: fmt.Sprintf("%064x", 8), ApplyToken: fmt.Sprintf("%032x", 9),
		AccountID: 1, AccountKey: fmt.Sprintf("%064x", 10), SourceType: model.RoutingUpstreamTypeNewAPI,
		ChannelID: 1, UpstreamGroup: "default", UpstreamGroupKey: fmt.Sprintf("%064x", 11),
		UpstreamModel: "old", UpstreamModelKey: fmt.Sprintf("%064x", 12), LocalModel: "old",
		LocalModelKey: fmt.Sprintf("%064x", 13), ObservedTime: 1, EffectiveTime: 1, ExpiresTime: 2,
		PricingVersion: "old", PricingJSON: `{}`, Confidence: model.RoutingCostConfidenceUnknown,
		Freshness: model.RoutingCostFreshnessUnknown, SourceSyncStatus: model.RoutingUpstreamSyncStatusUnknown,
		CreatedTime: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingProbeResult{
		ProbeID: strings.Repeat("a", 64), TargetKey: strings.Repeat("b", 64), ProbeType: model.RoutingProbeTypeServing,
		SnapshotRevision: 1, PoolID: 1, MemberID: 1, ChannelID: 1, GroupName: "default", ModelName: "old",
		EndpointHost: "old.example", EndpointAuthority: "https://old.example:443", Region: "default",
		BreakerScope: "member", EvidenceCount: 1, NodeCount: 1,
		BreakerState: model.RoutingBreakerStateHealthy, Outcome: model.RoutingProbeOutcomeSuccess,
		StartedTimeMs: 1, FinishedTimeMs: 1, LeaseFencingToken: 1, NodeEpochID: "old-node", CreatedTime: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingControlLease{
		LeaseName: activeProbeLeasePrefix + "old", LeaseUntilMs: 0, LastCompletedMs: 1, UpdatedTimeMs: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingAuditExport{
		ExportID: "rae_" + strings.Repeat("c", 32), OperationID: 1, ActorID: 1,
		FromTime: 1, ToTime: 1, RecordCount: 0, ContentBytes: 2, ContentHash: fmt.Sprintf("%064x", 8),
		ChunkCount: 1, CreatedTimeMs: 1, ExpiresTimeMs: 1,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingAuditExportChunk{
		ExportID: "rae_" + strings.Repeat("c", 32), ChunkIndex: 0, ChunkCount: 1,
		PayloadBytes: 2, PayloadHash: fmt.Sprintf("%064x", 9), Payload: `[]`,
	}).Error)
	require.NoError(t, db.Create(&model.RoutingOperation{
		OperationType: model.RoutingOperationTypeCostSync, IdempotencyHash: fmt.Sprintf("%064x", 10),
		CreateToken: fmt.Sprintf("%032x", 11), EvaluationHash: fmt.Sprintf("%064x", 12),
		SubjectType: model.RoutingOperationSubjectRoutingCosts, Reason: "old operation",
		Status: model.RoutingOperationStatusFailed, Source: model.RoutingOperationSourceSystem,
		RetentionCategory: model.RoutingOperationRetentionExtended, NeedsAttention: true,
		Attempts: 1, LastError: "old failure",
		CreatedTimeMs: 1, UpdatedTimeMs: 1, CompletedTimeMs: 1,
	}).Error)
	resetOutbox := model.RoutingBreakerResetOutbox{
		OperationID: 2, TargetKey: fmt.Sprintf("%064x", 13), Generation: 1,
		PayloadJSON: `{}`, PayloadHash: fmt.Sprintf("%064x", 14),
		CreatedTimeMs: 1, UpdatedTimeMs: 1, PublishedTimeMs: 1,
	}
	require.NoError(t, db.Create(&resetOutbox).Error)
	require.NoError(t, db.Create(&model.RoutingBreakerResetCommand{
		OperationID: 2, TargetKey: resetOutbox.TargetKey, Scope: model.RoutingBreakerResetScopeMember,
		PoolID: 1, MemberID: 1, ChannelID: 1, APIKeyIndex: model.RoutingMetricSingleKeyIndex,
		ModelName: "old", GroupName: "default", Generation: 1, TombstoneID: 1, OutboxID: resetOutbox.ID,
		CreatedTimeMs: 1, CompletedTimeMs: 1,
	}).Error)

	deleted, err := DeleteExpiredRoutingHistoryContext(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, int64(13), deleted)
}

func TestCanaryEvaluationRetentionPreservesCurrentRollout(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingPolicyHead{}, &model.RoutingCanaryEvaluation{}))
	withSnapshotTestDB(t, db)
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)

	require.NoError(t, db.Create(&model.RoutingPolicyHead{
		ID: 1, CurrentRevision: 11, CurrentActivationID: 401,
		CurrentHash: strings.Repeat("b", 64), CurrentStage: model.RoutingDeploymentStageCanary,
		CreatedTime: 1, UpdatedTime: 1,
	}).Error)
	activeRollout, err := CanaryRolloutKey(29, 401, 11, 100)
	require.NoError(t, err)
	rows := []model.RoutingCanaryEvaluation{
		{
			EvaluationHash: strings.Repeat("1", 64), CreateToken: strings.Repeat("1", 32),
			PolicyRevision: 11, ActivationID: 401, PoolID: 29,
			RolloutKey: string(activeRollout), WindowStartMs: 1, WindowEndMs: 2,
			Status: model.RoutingCanaryEvaluationStatusPassed, Reason: "active rollout", CreatedTimeMs: 10,
		},
		{
			EvaluationHash: strings.Repeat("2", 64), CreateToken: strings.Repeat("2", 32),
			PolicyRevision: 10, ActivationID: 400, PoolID: 30,
			RolloutKey: strings.Repeat("d", 64), WindowStartMs: 1, WindowEndMs: 2,
			Status: model.RoutingCanaryEvaluationStatusPassed, Reason: "expired rollout", CreatedTimeMs: 10,
		},
	}
	require.NoError(t, db.Create(&rows).Error)

	deleted, err := deleteExpiredRoutingCanaryEvaluationsContext(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	var remaining model.RoutingCanaryEvaluation
	require.NoError(t, db.First(&remaining).Error)
	assert.Equal(t, string(activeRollout), remaining.RolloutKey)
}

func enableStableTelemetryTest(t *testing.T) {
	t.Helper()
	routingmetrics.ResetForTest()
	ResetRoutingTelemetryTransportForTest()
	smart_routing_setting.ResetForTest()
	previousRedisEnabled := common.RedisEnabled
	previousRedis := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	setting := smart_routing_setting.GetSetting()
	setting.Enabled = true
	smart_routing_setting.UpdateSetting(setting)
	t.Cleanup(func() {
		routingmetrics.ResetForTest()
		ResetRoutingTelemetryTransportForTest()
		smart_routing_setting.ResetForTest()
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRedis
	})
}
