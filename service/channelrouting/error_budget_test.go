package channelrouting

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateErrorBudgetBurnRequiresMultiWindowAgreement(t *testing.T) {
	now := time.Unix(100_000, 0)
	const policyRevision int64 = 11
	tests := []struct {
		name       string
		bucketTime time.Time
		requests   int64
		failures   int64
		status     string
		reason     string
	}{
		{
			name: "critical fast burn", bucketTime: now.Add(-time.Minute), requests: 400, failures: 8,
			status: ErrorBudgetStatusCritical, reason: "fast_multi_window_burn",
		},
		{
			name: "warning slow burn", bucketTime: now.Add(-20 * time.Minute), requests: 400, failures: 4,
			status: ErrorBudgetStatusWarning, reason: "slow_multi_window_burn",
		},
		{
			name: "healthy", bucketTime: now.Add(-time.Minute), requests: 400, failures: 0,
			status: ErrorBudgetStatusHealthy, reason: "within_multi_window_budget",
		},
		{
			name: "insufficient volume", bucketTime: now.Add(-time.Minute), requests: 10, failures: 1,
			status: ErrorBudgetStatusInsufficientData, reason: "insufficient_reliability_volume",
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openSnapshotTestDB(t)
			withSnapshotTestDB(t, db)
			require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}))
			require.NoError(t, db.Create(&model.RoutingMetricRollup{
				MemberID: 100 + index, CredentialID: 200 + index,
				ModelName: "gpt-test", ModelKey: "model-" + test.name,
				BucketTs: test.bucketTime.Unix(), ChannelID: 300 + index, PoolID: 7,
				SchemaVersion:        model.RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion,
				LastSnapshotRevision: policyRevision,
				RequestCount:         test.requests, ReliabilityRequestCount: test.requests,
				ReliabilityFailureCount: test.failures,
			}).Error)

			result, err := EvaluateErrorBudgetBurnForRevisionContext(
				context.Background(), 7, policyRevision, 0.999, now,
			)
			require.NoError(t, err)
			assert.Equal(t, test.status, result.Status)
			assert.Equal(t, test.reason, result.Reason)
			assert.Equal(t, policyRevision, result.PolicyRevision)
			assert.True(t, result.SlowLong.RevisionIsolated)
			assert.Equal(t, test.requests, result.SlowLong.RequestCount)
			assert.InDelta(t, 0.001, result.ErrorBudget, 0.0000001)
		})
	}
}

func TestEvaluateErrorBudgetBurnSeparatesPolicyRevisions(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}))
	now := time.Unix(100_000, 0)
	require.NoError(t, db.Create(&[]model.RoutingMetricRollup{
		{
			MemberID: 1, CredentialID: 1, ModelName: "gpt-revision-one", ModelKey: "revision-one",
			BucketTs: now.Add(-time.Minute).Unix(), ChannelID: 1, PoolID: 7,
			SchemaVersion:        model.RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion,
			LastSnapshotRevision: 1, RequestCount: 400, ReliabilityRequestCount: 400,
			ReliabilityFailureCount: 8,
		},
		{
			MemberID: 2, CredentialID: 2, ModelName: "gpt-revision-two", ModelKey: "revision-two",
			BucketTs: now.Add(-time.Minute).Unix(), ChannelID: 2, PoolID: 7,
			SchemaVersion:        model.RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion,
			LastSnapshotRevision: 2, RequestCount: 400, ReliabilityRequestCount: 400,
			ReliabilityFailureCount: 0,
		},
	}).Error)

	revisionOne, err := EvaluateErrorBudgetBurnForRevisionContext(context.Background(), 7, 1, 0.999, now)
	require.NoError(t, err)
	assert.Equal(t, ErrorBudgetStatusCritical, revisionOne.Status)
	assert.Equal(t, int64(400), revisionOne.SlowLong.RequestCount)

	revisionTwo, err := EvaluateErrorBudgetBurnForRevisionContext(context.Background(), 7, 2, 0.999, now)
	require.NoError(t, err)
	assert.Equal(t, ErrorBudgetStatusHealthy, revisionTwo.Status)
	assert.Equal(t, int64(400), revisionTwo.SlowLong.RequestCount)
}

func TestEvaluateErrorBudgetBurnFailsClosedForLegacyMixedRevisionRollups(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}))
	now := time.Unix(100_000, 0)
	require.NoError(t, db.Create(&model.RoutingMetricRollup{
		MemberID: 1, CredentialID: 1, ModelName: "gpt-legacy", ModelKey: "legacy",
		BucketTs: now.Add(-time.Minute).Unix(), ChannelID: 1, PoolID: 7,
		SchemaVersion: 2, LastSnapshotRevision: 2,
		RequestCount: 400, ReliabilityRequestCount: 400,
	}).Error)

	result, err := EvaluateErrorBudgetBurnForRevisionContext(context.Background(), 7, 2, 0.999, now)
	require.NoError(t, err)
	assert.Equal(t, ErrorBudgetStatusInsufficientData, result.Status)
	assert.Equal(t, "revision_isolation_unavailable", result.Reason)
	assert.False(t, result.SlowLong.RevisionIsolated)
	assert.False(t, result.SlowLong.Sufficient)
	assert.Equal(t, int64(400), result.SlowLong.UnisolatedRequestCount)
	assert.Zero(t, result.SlowLong.RequestCount)
}

func TestEvaluateErrorBudgetBurnExternalDatabaseCompatibility(t *testing.T) {
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
			db := openSnapshotExternalTestDB(t, test.dbType, dsn)
			withSnapshotTestDBType(t, db, test.dbType)
			require.NoError(t, db.AutoMigrate(&model.RoutingMetricRollup{}))
			const poolID = 970_007
			require.NoError(t, db.Where("pool_id = ?", poolID).Delete(&model.RoutingMetricRollup{}).Error)
			t.Cleanup(func() {
				_ = db.Where("pool_id = ?", poolID).Delete(&model.RoutingMetricRollup{}).Error
			})
			now := time.Unix(100_000, 0)
			require.NoError(t, db.Create(&model.RoutingMetricRollup{
				MemberID: 970_001, CredentialID: 970_002,
				ModelName: "gpt-error-budget-matrix", ModelKey: "error-budget-matrix",
				BucketTs: now.Add(-time.Minute).Unix(), ChannelID: 970_003, PoolID: poolID,
				SchemaVersion:        model.RoutingErrorBudgetRevisionIsolatedRollupSchemaVersion,
				LastSnapshotRevision: 17, RequestCount: 400,
				ReliabilityRequestCount: 400, ReliabilityFailureCount: 8,
			}).Error)

			result, err := EvaluateErrorBudgetBurnForRevisionContext(
				context.Background(), poolID, 17, 0.999, now,
			)
			require.NoError(t, err)
			assert.Equal(t, ErrorBudgetStatusCritical, result.Status)
			assert.True(t, result.SlowLong.RevisionIsolated)
			assert.Equal(t, int64(400), result.SlowLong.RequestCount)
		})
	}
}

func TestEvaluateErrorBudgetBurnRejectsInvalidTargetAndPool(t *testing.T) {
	_, err := EvaluateErrorBudgetBurnForRevisionContext(context.Background(), 0, 1, 0.999, time.Now())
	assert.ErrorIs(t, err, ErrErrorBudgetInvalid)
	_, err = EvaluateErrorBudgetBurnForRevisionContext(context.Background(), 1, 0, 0.999, time.Now())
	assert.ErrorIs(t, err, ErrErrorBudgetInvalid)
	_, err = EvaluateErrorBudgetBurnForRevisionContext(context.Background(), 1, 1, 1, time.Now())
	assert.ErrorIs(t, err, ErrErrorBudgetInvalid)
}

func TestEnterpriseErrorBudgetEvaluationPersistsFrozenRevisionCursor(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingPolicyHead{}, &model.RoutingPolicyRevision{}, &model.RoutingPolicyPoolRevision{},
		&model.RoutingPolicyMemberRevision{}, &model.RoutingPolicyActivation{}, &model.RoutingConfigOutbox{},
		&model.RoutingControlLease{}, &model.RoutingErrorBudgetCursor{}, &model.RoutingErrorBudgetState{},
		&model.RoutingErrorBudgetHistory{}, &model.RoutingMetricRollup{},
	))
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	document := model.RoutingPolicyDocument{SchemaVersion: model.RoutingPolicySchemaVersion}
	for poolID := 1; poolID <= errorBudgetEvaluationBatch+1; poolID++ {
		document.Pools = append(document.Pools, model.RoutingPolicyPoolContent{
			PoolID: poolID, GroupName: fmt.Sprintf("enterprise-%03d", poolID), DisplayName: fmt.Sprintf("Enterprise %03d", poolID),
			DeploymentStage: model.RoutingDeploymentStageActive, PolicyProfile: model.RoutingPolicyProfileEnterpriseSLO,
		})
	}
	first, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 0, document,
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageActive, ActorID: 1, Reason: "first"},
	)
	require.NoError(t, err)
	previousSnapshot := currentSnapshot.Load()
	currentSnapshot.Store(&runtimeSnapshot{view: SnapshotView{Revision: uint64(first.Revision.Revision)}})
	t.Cleanup(func() { currentSnapshot.Store(previousSnapshot) })

	transitions, err := EvaluateEnterpriseErrorBudgetsWithTransitionsContext(
		context.Background(), smart_routing_setting.SmartRoutingSetting{Enabled: true},
	)
	require.NoError(t, err)
	require.Len(t, transitions, errorBudgetEvaluationBatch)
	cursor, err := model.GetRoutingErrorBudgetCursorContext(context.Background(), model.RoutingErrorBudgetEvaluatorCursor)
	require.NoError(t, err)
	assert.Equal(t, first.Revision.Revision, cursor.PolicyRevision)
	assert.Equal(t, int64(errorBudgetEvaluationBatch), cursor.PositionID)

	document.Pools[len(document.Pools)-1].DisplayName = "Enterprise final revision"
	second, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), first.Revision.Revision, document,
		model.RoutingPolicyActivationSpec{Stage: model.RoutingDeploymentStageActive, ActorID: 1, Reason: "second"},
	)
	require.NoError(t, err)
	currentSnapshot.Store(&runtimeSnapshot{view: SnapshotView{Revision: uint64(second.Revision.Revision)}})
	require.NoError(t, db.Model(&model.RoutingControlLease{}).
		Where("lease_name = ?", errorBudgetEvaluationLease).Update("last_completed_ms", 0).Error)

	transitions, err = EvaluateEnterpriseErrorBudgetsWithTransitionsContext(
		context.Background(), smart_routing_setting.SmartRoutingSetting{Enabled: true},
	)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, first.Revision.Revision, transitions[0].PolicyRevision)
	cursor, err = model.GetRoutingErrorBudgetCursorContext(context.Background(), model.RoutingErrorBudgetEvaluatorCursor)
	require.NoError(t, err)
	assert.Zero(t, cursor.PolicyRevision)
	assert.Zero(t, cursor.PositionID)

	require.NoError(t, db.Model(&model.RoutingControlLease{}).
		Where("lease_name = ?", errorBudgetEvaluationLease).Update("last_completed_ms", 0).Error)
	transitions, err = EvaluateEnterpriseErrorBudgetsWithTransitionsContext(
		context.Background(), smart_routing_setting.SmartRoutingSetting{Enabled: true},
	)
	require.NoError(t, err)
	require.Len(t, transitions, errorBudgetEvaluationBatch)
	for _, transition := range transitions {
		assert.Equal(t, second.Revision.Revision, transition.PolicyRevision)
	}
}

func TestErrorBudgetPublisherRecoversFromPersistentHistoryCursor(t *testing.T) {
	db := openSnapshotTestDB(t)
	withSnapshotTestDB(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingControlLease{}, &model.RoutingErrorBudgetCursor{}, &model.RoutingErrorBudgetHistory{},
	))
	ResetRoutingEventsForTest()
	ResetRoutingEventTransportForTest()
	redisEnabled := common.RedisEnabled
	redisClient := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = redisEnabled
		common.RDB = redisClient
		ResetRoutingEventsForTest()
		ResetRoutingEventTransportForTest()
	})
	nowMs, err := model.RoutingDatabaseNowMsContext(context.Background())
	require.NoError(t, err)
	history := []model.RoutingErrorBudgetHistory{
		errorBudgetHistoryForPublisherTest(1, nowMs-2),
		errorBudgetHistoryForPublisherTest(2, nowMs-1),
	}
	require.NoError(t, db.Create(&history).Error)

	published, err := PublishPendingErrorBudgetTransitionsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, published)
	cursor, err := model.GetRoutingErrorBudgetCursorContext(context.Background(), model.RoutingErrorBudgetPublisherCursor)
	require.NoError(t, err)
	assert.Equal(t, history[1].ID, cursor.PositionID)

	ResetRoutingEventsForTest()
	third := errorBudgetHistoryForPublisherTest(3, nowMs)
	require.NoError(t, db.Create(&third).Error)
	common.RedisEnabled = true
	published, err = PublishPendingErrorBudgetTransitionsContext(context.Background())
	assert.ErrorIs(t, err, ErrRoutingEventTransportUnavailable)
	assert.Zero(t, published)
	cursor, err = model.GetRoutingErrorBudgetCursorContext(context.Background(), model.RoutingErrorBudgetPublisherCursor)
	require.NoError(t, err)
	assert.Equal(t, history[1].ID, cursor.PositionID)

	common.RedisEnabled = false
	published, err = PublishPendingErrorBudgetTransitionsContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, published)
	replay, _, cancel, err := SubscribeRoutingEvents(0)
	require.NoError(t, err)
	defer cancel()
	require.Len(t, replay.Events, 1)
	var transition model.RoutingErrorBudgetTransition
	require.NoError(t, common.Unmarshal(replay.Events[0].PayloadJSON, &transition))
	assert.Equal(t, third.ID, transition.HistoryID)
}

func errorBudgetHistoryForPublisherTest(poolID int, evaluatedAtMs int64) model.RoutingErrorBudgetHistory {
	return model.RoutingErrorBudgetHistory{
		PoolID: poolID, PolicyRevision: 1, Status: ErrorBudgetStatusHealthy, Reason: "within_multi_window_budget",
		AvailabilityTarget: 0.999, EvaluationJSON: `{}`, LeaseFencingToken: 1,
		FirstObservedAtMs: evaluatedAtMs, EvaluatedAtMs: evaluatedAtMs, CreatedTime: evaluatedAtMs / 1_000,
	}
}
