package channelrouting

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanaryNodePresenceHeartbeatIsRolloutScopedAndMonotonic(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	db := openCanaryWindowTestDB(t, true)
	withCanaryWindowTestDB(t, db)

	view := canaryPresenceSnapshotViewForTest()
	currentSnapshot.Store(&runtimeSnapshot{view: view})
	firstObserved := time.Unix(1_700_100_010, 0)
	require.NoError(t, persistRoutingCanaryNodePresenceAtContext(context.Background(), firstObserved))
	identity, err := canaryNodePresenceIdentityFromView(view)
	require.NoError(t, err)
	first, err := model.GetRoutingRuntimeCheckpointContext(
		context.Background(),
		NodeEpochID(),
		RoutingCanaryNodePresenceCheckpointKind,
		canaryNodePresenceScope(identity),
	)
	require.NoError(t, err)
	assert.Equal(t, firstObserved.Unix(), first.ObservedTime)
	assert.Equal(t, firstObserved.Unix(), first.CreatedTime)
	assert.Equal(t, firstObserved.Add(canaryNodePresenceTTL).Unix(), first.ExpiresTime)

	secondObserved := firstObserved.Add(canaryNodePresencePollInterval)
	require.NoError(t, persistRoutingCanaryNodePresenceAtContext(context.Background(), secondObserved))
	second, err := model.GetRoutingRuntimeCheckpointContext(
		context.Background(),
		NodeEpochID(),
		RoutingCanaryNodePresenceCheckpointKind,
		canaryNodePresenceScope(identity),
	)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Greater(t, second.Sequence, first.Sequence)
	assert.Equal(t, first.CreatedTime, second.CreatedTime)
	assert.Equal(t, secondObserved.Unix(), second.ObservedTime)

	view.Revision++
	view.ActivationID++
	view.PolicyHash = strings.Repeat("c", 64)
	currentSnapshot.Store(&runtimeSnapshot{view: view})
	require.NoError(t, persistRoutingCanaryNodePresenceAtContext(
		context.Background(), secondObserved.Add(canaryNodePresencePollInterval),
	))
	var count int64
	require.NoError(t, db.Model(&model.RoutingRuntimeCheckpoint{}).
		Where("checkpoint_kind = ?", RoutingCanaryNodePresenceCheckpointKind).
		Count(&count).Error)
	assert.Equal(t, int64(2), count, "a new rollout must not overwrite the prior rollout's presence history")
}

func TestCanaryEvaluatorUsesPresenceHeartbeatsAcrossZeroTrafficWindows(t *testing.T) {
	ResetSnapshotForTest()
	ResetCanaryControlForTest()
	t.Cleanup(ResetSnapshotForTest)
	t.Cleanup(ResetCanaryControlForTest)
	db := openCanaryWindowTestDB(t, false)
	withCanaryWindowTestDB(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingRuntimeCheckpoint{},
		&model.RoutingCanaryEvaluation{},
		&model.RoutingOperation{},
	))

	base := time.Now().Add(2 * time.Minute).Truncate(time.Minute)
	clock := &routingTestClock{now: base.Add(10 * time.Second)}
	aggregator, err := NewCanaryWindowAggregator(CanaryWindowAggregatorConfig{
		MaxEntries: 8, Shards: 2, TTL: 2 * time.Hour, Clock: clock,
	})
	require.NoError(t, err)
	ResetCanaryWindowAggregatorForTest(aggregator)
	t.Cleanup(func() { ResetCanaryWindowAggregatorForTest() })

	view := canaryPresenceSnapshotViewForTest()
	view.ActivationCreatedTime = base.Add(-time.Hour).Unix()
	policy := model.DefaultRoutingCanaryPolicy()
	policy.Evaluation.WindowSeconds = 60
	policy.Evaluation.EvaluationIntervalSeconds = 10
	policy.Evaluation.RolloutGraceSeconds = 0
	policy.Evaluation.CheckpointLatenessSeconds = 5
	policy.Evaluation.ConsecutiveBreachWindows = 2
	view.Pools[0].CanaryPolicy = policy
	currentSnapshot.Store(&runtimeSnapshot{view: view})
	rolloutKey, err := CanaryRolloutKey(29, view.ActivationID, view.Revision, view.TrafficBasisPoints)
	require.NoError(t, err)
	defaultCanaryEvaluationSchedule.markCompleted(rolloutKey, base.UnixMilli())

	setting := smart_routing_setting.SmartRoutingSetting{Enabled: true}
	require.NoError(t, persistRoutingCanaryNodePresenceAtContext(context.Background(), clock.Now()))
	require.NoError(t, ensureCurrentCanaryOutcomeWindows(aggregator))

	clock.Advance(56 * time.Second)
	flushed, err := FlushCanaryOutcomeCheckpointsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	require.NoError(t, persistRoutingCanaryNodePresenceAtContext(context.Background(), base.Add(70*time.Second)))
	require.NoError(t, evaluateRoutingCanarySweepContext(context.Background(), setting, base.Add(71*time.Second)))

	clock.Advance(5 * time.Second)
	require.NoError(t, ensureCurrentCanaryOutcomeWindows(aggregator))
	clock.Advance(55 * time.Second)
	flushed, err = FlushCanaryOutcomeCheckpointsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	require.NoError(t, persistRoutingCanaryNodePresenceAtContext(context.Background(), base.Add(130*time.Second)))
	require.NoError(t, evaluateRoutingCanarySweepContext(context.Background(), setting, base.Add(131*time.Second)))

	var evaluations []model.RoutingCanaryEvaluation
	require.NoError(t, db.Order("window_end_ms asc").Find(&evaluations).Error)
	require.Len(t, evaluations, 2)
	for index := range evaluations {
		assert.Equal(t, model.RoutingCanaryEvaluationStatusInconclusive, evaluations[index].Status)
		assert.Equal(t, 10_000, evaluations[index].NodeCoverageBasisPoints)
		assert.Equal(t, int64(0), evaluations[index].ControlRequestCount)
		assert.Equal(t, int64(0), evaluations[index].CanaryRequestCount)
	}
	var operationCount int64
	require.NoError(t, db.Model(&model.RoutingOperation{}).Count(&operationCount).Error)
	assert.Zero(t, operationCount, "zero traffic with complete node coverage must not trigger rollback")
	var configCheckpointCount int64
	require.NoError(t, db.Model(&model.RoutingRuntimeCheckpoint{}).
		Where("checkpoint_kind = ?", RoutingConfigCheckpointKind).
		Count(&configCheckpointCount).Error)
	assert.Zero(t, configCheckpointCount, "node coverage must not depend on config events")
}

func TestCanaryNodePresenceHeartbeatFailsClosedWhenPersistenceIsUnavailable(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	db := openCanaryWindowTestDB(t, false)
	withCanaryWindowTestDB(t, db)
	currentSnapshot.Store(&runtimeSnapshot{view: canaryPresenceSnapshotViewForTest()})

	err := persistRoutingCanaryNodePresenceAtContext(context.Background(), time.Now())
	assert.Error(t, err)
}

func TestCanaryNodePresenceSeparatesMismatchedPayload(t *testing.T) {
	db := openCanaryWindowTestDB(t, true)
	withCanaryWindowTestDB(t, db)
	view := canaryPresenceSnapshotViewForTest()
	identity, err := canaryNodePresenceIdentityFromView(view)
	require.NoError(t, err)
	now := time.Now()
	checkpoint, err := model.NewRoutingRuntimeCheckpoint(
		"node-a",
		RoutingCanaryNodePresenceCheckpointKind,
		canaryNodePresenceScope(identity),
		identity.PolicyRevision,
		1,
		canaryNodePresencePayload{
			SchemaVersion:      canaryNodePresenceSchemaVersion,
			PolicyHash:         strings.Repeat("d", 64),
			ActivationID:       identity.ActivationID,
			ActivationStage:    model.RoutingDeploymentStageCanary,
			TrafficBasisPoints: identity.TrafficBasisPoints,
		},
		now.Unix(),
		now.Add(canaryNodePresenceTTL).Unix(),
	)
	require.NoError(t, err)
	checkpoint.CreatedTime = now.Unix()
	checkpoint.UpdatedTime = now.Unix()
	_, err = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), checkpoint)
	require.NoError(t, err)

	target := canaryEvaluationTargetForTest(now.Add(-time.Hour).UnixMilli())
	target.PolicyRevision = identity.PolicyRevision
	target.PolicyHash = identity.PolicyHash
	target.ActivationID = identity.ActivationID
	target.TrafficBasisPoints = identity.TrafficBasisPoints
	matching, invalid, err := loadCanaryNodePresenceCheckpointsContext(context.Background(), target, now)
	require.NoError(t, err)
	assert.Empty(t, matching)
	require.Len(t, invalid, 1)
	assert.Equal(t, "node-a", invalid[0].NodeID)

	windowEnd := now.Add(-time.Minute).Truncate(time.Second)
	invalidNodes, err := activeCanaryNodeIDsForWindow(
		invalid,
		windowEnd.Add(-5*time.Minute).UnixMilli(),
		windowEnd.UnixMilli(),
		now,
	)
	require.NoError(t, err)
	assert.Empty(t, invalidNodes, "presence created after a closed window must not poison that window")
}

func TestCanaryEvaluatorFailsClosedOnSemanticallyInvalidPresence(t *testing.T) {
	ResetSnapshotForTest()
	ResetCanaryControlForTest()
	t.Cleanup(ResetSnapshotForTest)
	t.Cleanup(ResetCanaryControlForTest)
	db := openCanaryWindowTestDB(t, false)
	withCanaryWindowTestDB(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingRuntimeCheckpoint{},
		&model.RoutingCanaryEvaluation{},
		&model.RoutingOperation{},
	))

	windowEnd := time.Unix(1_700_000_100, 0)
	windowStart := windowEnd.Add(-5 * time.Minute)
	now := windowEnd.Add(11 * time.Second)
	policy := model.DefaultRoutingCanaryPolicy()
	policy.Evaluation.WindowSeconds = 300
	policy.Evaluation.EvaluationIntervalSeconds = 60
	policy.Evaluation.CheckpointLatenessSeconds = 5
	policy.Evaluation.RolloutGraceSeconds = 0
	policy.Evaluation.ConsecutiveBreachWindows = 1

	view := canaryPresenceSnapshotViewForTest()
	view.ActivationCreatedTime = windowStart.Add(-time.Hour).Unix()
	view.Pools[0].CanaryPolicy = policy
	currentSnapshot.Store(&runtimeSnapshot{view: view})
	rolloutKey, err := CanaryRolloutKey(
		view.Pools[0].ID,
		view.ActivationID,
		view.Revision,
		view.TrafficBasisPoints,
	)
	require.NoError(t, err)

	identity, err := canaryNodePresenceIdentityFromView(view)
	require.NoError(t, err)
	presence, err := model.NewRoutingRuntimeCheckpoint(
		"node-a",
		RoutingCanaryNodePresenceCheckpointKind,
		canaryNodePresenceScope(identity),
		identity.PolicyRevision,
		1,
		canaryNodePresencePayload{
			SchemaVersion:      canaryNodePresenceSchemaVersion,
			PolicyHash:         strings.Repeat("d", 64),
			ActivationID:       identity.ActivationID,
			ActivationStage:    model.RoutingDeploymentStageCanary,
			TrafficBasisPoints: identity.TrafficBasisPoints,
		},
		now.Unix(),
		now.Add(canaryNodePresenceTTL).Unix(),
	)
	require.NoError(t, err)
	presence.CreatedTime = windowStart.Add(10 * time.Second).Unix()
	presence.UpdatedTime = now.Unix()
	_, err = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), presence)
	require.NoError(t, err)

	stats := canaryWindowAggregateForEvaluatorFixture(t)
	payload := CanaryCohortWindowCheckpoint{
		SchemaVersion:      canaryCohortWindowSchemaVersion,
		PoolID:             view.Pools[0].ID,
		ActivationID:       view.ActivationID,
		PolicyRevision:     view.Revision,
		TrafficBasisPoints: view.TrafficBasisPoints,
		RolloutKey:         rolloutKey,
		WindowSeconds:      policy.Evaluation.WindowSeconds,
		WindowStartUnixMs:  windowStart.UnixMilli(),
		WindowEndUnixMs:    windowEnd.UnixMilli(),
		Control:            canaryWindowStatsForTest(t, stats.Control),
		Canary:             canaryWindowStatsForTest(t, stats.Canary),
	}
	windowCheckpoint, err := model.NewRoutingRuntimeCheckpoint(
		"node-a",
		CanaryCohortWindowCheckpointKind,
		CanaryWindowCheckpointScope(payload),
		int64(view.Revision),
		1,
		payload,
		windowEnd.Unix(),
		now.Add(time.Hour).Unix(),
	)
	require.NoError(t, err)
	_, err = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), windowCheckpoint)
	require.NoError(t, err)

	require.NoError(t, evaluateRoutingCanarySweepContext(
		context.Background(),
		smart_routing_setting.SmartRoutingSetting{Enabled: true},
		now,
	))
	var evaluation model.RoutingCanaryEvaluation
	require.NoError(t, db.First(&evaluation).Error)
	assert.Equal(t, model.RoutingCanaryEvaluationStatusBreached, evaluation.Status)
	assert.Equal(t, "node checkpoint coverage below policy", evaluation.Reason)
	assert.Zero(t, evaluation.NodeCoverageBasisPoints)

	var operationCount int64
	require.NoError(t, db.Model(&model.RoutingOperation{}).Count(&operationCount).Error)
	assert.Equal(t, int64(1), operationCount)
}

func TestCanaryNodePresenceOnlyCountsNodesCreatedBeforeWindowClose(t *testing.T) {
	view := canaryPresenceSnapshotViewForTest()
	identity, err := canaryNodePresenceIdentityFromView(view)
	require.NoError(t, err)
	windowStart := time.Now().Add(time.Minute).Truncate(time.Minute)
	windowEnd := windowStart.Add(time.Minute)
	longLived, err := newCanaryNodePresenceCheckpoint("node-a", identity, 1, windowStart.Add(10*time.Second))
	require.NoError(t, err)
	longLived.ObservedTime = windowEnd.Add(10 * time.Second).Unix()
	lateJoin, err := newCanaryNodePresenceCheckpoint("node-b", identity, 1, windowEnd.Add(time.Second))
	require.NoError(t, err)

	nodes, err := activeCanaryNodeIDsForWindow(
		[]model.RoutingRuntimeCheckpoint{longLived, lateJoin},
		windowStart.UnixMilli(),
		windowEnd.UnixMilli(),
		windowEnd.Add(15*time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, map[string]struct{}{"node-a": {}}, nodes)
}

func TestCanaryPresenceWorkerStopsWithRuntimeContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	runtime := newRuntime(ctx, runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true}
		},
		heartbeatCanary: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		},
		wait: waitRuntime,
	})
	<-started
	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	require.NoError(t, runtime.Wait(waitCtx))
	assert.Equal(t, int64(0), runtime.Stats().CanaryNodePresence.Failures)
}

func canaryPresenceSnapshotViewForTest() SnapshotView {
	return SnapshotView{
		Revision:              11,
		PolicyHash:            strings.Repeat("b", 64),
		ActivationID:          401,
		ActivationStage:       model.RoutingDeploymentStageCanary,
		TrafficBasisPoints:    100,
		ActivationCreatedTime: time.Now().Add(-time.Hour).Unix(),
		Pools: []PoolSnapshot{{
			ID: 29, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageCanary,
			CanaryPolicy: model.DefaultRoutingCanaryPolicy(),
		}},
	}
}
