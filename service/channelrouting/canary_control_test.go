package channelrouting

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	routingdistribution "github.com/QuantumNous/new-api/pkg/routing_distribution"
	"github.com/QuantumNous/new-api/setting/smart_routing_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBuildRoutingCanaryEvaluationSpecAppliesSafetyGates(t *testing.T) {
	windowStartMs := int64(1_700_000_000_000)
	windowEndMs := windowStartMs + int64(5*time.Minute/time.Millisecond)
	target := canaryEvaluationTargetForTest(windowStartMs - int64(time.Hour/time.Millisecond))
	passing := canaryWindowAggregateForTest(t)

	tests := []struct {
		name       string
		mutate     func(*canaryEvaluationTarget, *canaryWindowAggregate)
		wantStatus model.RoutingCanaryEvaluationStatus
		wantReason string
	}{
		{name: "passed", wantStatus: model.RoutingCanaryEvaluationStatusPassed, wantReason: "canary window passed"},
		{
			name: "node telemetry gap fails closed", wantStatus: model.RoutingCanaryEvaluationStatusBreached,
			wantReason: "node checkpoint coverage below policy",
			mutate: func(_ *canaryEvaluationTarget, aggregate *canaryWindowAggregate) {
				aggregate.NodeCoverageBasisPoints = 5_000
			},
		},
		{
			name: "rollout grace", wantStatus: model.RoutingCanaryEvaluationStatusRolloutGrace,
			wantReason: "window overlaps rollout grace",
			mutate: func(target *canaryEvaluationTarget, _ *canaryWindowAggregate) {
				target.ActivationCreatedMs = windowStartMs - int64(time.Minute/time.Millisecond)
			},
		},
		{
			name: "low sample is inconclusive", wantStatus: model.RoutingCanaryEvaluationStatusInconclusive,
			wantReason: "logical request sample count below policy",
			mutate: func(_ *canaryEvaluationTarget, aggregate *canaryWindowAggregate) {
				aggregate.Canary.LogicalRequests = 99
				aggregate.Canary.Successes = 98
				aggregate.Canary.Failures = 1
				aggregate.Canary.Attempts = 99
			},
		},
		{
			name: "retry amplification", wantStatus: model.RoutingCanaryEvaluationStatusBreached,
			wantReason: "retry amplification breached",
			mutate: func(_ *canaryEvaluationTarget, aggregate *canaryWindowAggregate) {
				aggregate.Control.Attempts = 1_050
				aggregate.Canary.Attempts = 120
			},
		},
		{
			name: "ttft ratio and delta", wantStatus: model.RoutingCanaryEvaluationStatusBreached,
			wantReason: "p95 ttft regression breached",
			mutate: func(_ *canaryEvaluationTarget, aggregate *canaryWindowAggregate) {
				aggregate.Canary.TTFT = durationSketchForTest(t, aggregate.Canary.TTFTSampleCount, 500)
			},
		},
		{
			name: "cost ratio", wantStatus: model.RoutingCanaryEvaluationStatusBreached,
			wantReason: "expected cost ratio breached",
			mutate: func(_ *canaryEvaluationTarget, aggregate *canaryWindowAggregate) {
				aggregate.Canary.ExpectedPlatformCostNanoUSD *= 2
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caseTarget := target
			aggregate := cloneCanaryWindowAggregateForTest(passing)
			if test.mutate != nil {
				test.mutate(&caseTarget, &aggregate)
			}
			spec, err := buildRoutingCanaryEvaluationSpec(caseTarget, aggregate, windowStartMs, windowEndMs)
			require.NoError(t, err)
			assert.Equal(t, test.wantStatus, spec.Status)
			assert.Contains(t, spec.Reason, test.wantReason)
		})
	}
}

func TestBuildRoutingCanaryEvaluationSpecCountsRetriesAfterRoutingFailures(t *testing.T) {
	windowStartMs := int64(1_700_000_000_000)
	windowEndMs := windowStartMs + int64(5*time.Minute/time.Millisecond)
	target := canaryEvaluationTargetForTest(windowStartMs - int64(time.Hour/time.Millisecond))
	target.Policy.MinCostCoverageBasisPoints = 0
	target.Policy.HardMinSuccessRateBasisPoints = 0
	target.Policy.MaxSuccessRateDropBasisPoints = 5_000
	aggregate := canaryWindowAggregateForTest(t)
	aggregate.Control.Attempts = 1_100
	aggregate.Canary.Successes = 50
	aggregate.Canary.Failures = 50
	aggregate.Canary.RoutingFailures = 50
	aggregate.Canary.Attempts = 70
	aggregate.Canary.CostKnownRequests = 50
	aggregate.Canary.ExpectedPlatformCostNanoUSD = 50_000_000
	aggregate.Canary.TTFTSampleCount = 50
	aggregate.Canary.TTFT = durationSketchForTest(t, 50, 100)

	spec, err := buildRoutingCanaryEvaluationSpec(target, aggregate, windowStartMs, windowEndMs)
	require.NoError(t, err)
	assert.Equal(t, int64(20), spec.Canary.RetryCount)
	assert.Equal(t, int64(20_000), spec.RetryAmplificationRatioBasisPoints)
	assert.Equal(t, model.RoutingCanaryEvaluationStatusBreached, spec.Status)
	assert.Equal(t, "retry amplification breached", spec.Reason)
}

func TestPendingCanaryEvaluationWindowsBoundsCatchUpAndPreservesOrder(t *testing.T) {
	latestWindowEnd := time.Unix(1_700_000_100, 0)
	now := latestWindowEnd.Add(11 * time.Second)
	target := canaryEvaluationTargetForTest(latestWindowEnd.Add(-time.Hour).UnixMilli())
	target.Policy.WindowSeconds = 60
	target.Policy.CheckpointLatenessSeconds = 5
	target.Policy.ConsecutiveBreachWindows = canaryEvaluatorMaxCatchUpWindows

	windows, err := pendingCanaryEvaluationWindows(target, now, 0)
	require.NoError(t, err)
	require.Len(t, windows, canaryEvaluatorMaxCatchUpWindows)
	for index := range windows {
		wantEndMs := latestWindowEnd.Add(
			time.Duration(index-canaryEvaluatorMaxCatchUpWindows+1) * time.Minute,
		).UnixMilli()
		assert.Equal(t, wantEndMs-int64(time.Minute/time.Millisecond), windows[index].windowStartMs)
		assert.Equal(t, wantEndMs, windows[index].windowEndMs)
	}

	resumed, err := pendingCanaryEvaluationWindows(target, now, windows[7].windowEndMs)
	require.NoError(t, err)
	require.Len(t, resumed, 2)
	assert.Equal(t, windows[8].windowEndMs, resumed[0].windowEndMs)
	assert.Equal(t, windows[9].windowEndMs, resumed[1].windowEndMs)

	completed, err := pendingCanaryEvaluationWindows(target, now, windows[9].windowEndMs)
	require.NoError(t, err)
	assert.Empty(t, completed)

	target.Policy.ConsecutiveBreachWindows = canaryEvaluatorMaxCatchUpWindows + 1
	_, err = pendingCanaryEvaluationWindows(target, now, 0)
	assert.ErrorIs(t, err, ErrCanaryControlInvalid)
}

func TestCanaryEvaluatorAggregatesActiveNodesWithoutTreatingMissingDataAsPassing(t *testing.T) {
	for _, test := range []struct {
		name             string
		reportedNodeB    bool
		wantStatus       model.RoutingCanaryEvaluationStatus
		wantNodeCoverage int
	}{
		{name: "all active nodes reported", reportedNodeB: true, wantStatus: model.RoutingCanaryEvaluationStatusPassed, wantNodeCoverage: 10_000},
		{name: "one active node missing", wantStatus: model.RoutingCanaryEvaluationStatusBreached, wantNodeCoverage: 5_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			evaluation := runCanaryEvaluatorFixture(t, test.reportedNodeB)
			assert.Equal(t, test.wantStatus, evaluation.Status)
			assert.Equal(t, test.wantNodeCoverage, evaluation.NodeCoverageBasisPoints)
		})
	}
}

func TestCanaryEvaluatorCatchesUpClosedWindowsAndTriggersRollbackWithoutDelay(t *testing.T) {
	ResetSnapshotForTest()
	ResetCanaryControlForTest()
	t.Cleanup(ResetSnapshotForTest)
	t.Cleanup(ResetCanaryControlForTest)
	db := openCanaryWindowTestDB(t, false)
	withCanaryWindowTestDB(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingRuntimeCheckpoint{}, &model.RoutingCanaryEvaluation{}, &model.RoutingOperation{},
	))

	latestWindowEnd := time.Unix(1_700_000_100, 0)
	firstWindowStart := latestWindowEnd.Add(-2 * time.Minute)
	now := latestWindowEnd.Add(11 * time.Second)
	policy := model.DefaultRoutingCanaryPolicy()
	policy.Evaluation.WindowSeconds = 60
	policy.Evaluation.EvaluationIntervalSeconds = 10
	policy.Evaluation.CheckpointLatenessSeconds = 5
	policy.Evaluation.RolloutGraceSeconds = 0
	policy.Evaluation.MinCanaryRequests = 10
	policy.Evaluation.MinControlRequests = 10
	policy.Evaluation.MinTTFTSamples = 10
	policy.Evaluation.ConsecutiveBreachWindows = 2

	view := SnapshotView{
		Revision:              11,
		PolicyHash:            strings.Repeat("b", 64),
		ActivationID:          401,
		ActivationStage:       model.RoutingDeploymentStageCanary,
		TrafficBasisPoints:    100,
		ActivationCreatedTime: firstWindowStart.Add(-time.Hour).Unix(),
		Pools: []PoolSnapshot{{
			ID: 29, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageCanary,
			CanaryPolicy: policy,
		}},
	}
	currentSnapshot.Store(&runtimeSnapshot{view: view})
	rolloutKey, err := CanaryRolloutKey(29, view.ActivationID, view.Revision, view.TrafficBasisPoints)
	require.NoError(t, err)

	presenceIdentity, err := canaryNodePresenceIdentityFromView(view)
	require.NoError(t, err)
	presence, err := newCanaryNodePresenceCheckpoint("node-a", presenceIdentity, 1, firstWindowStart.Add(10*time.Second))
	require.NoError(t, err)
	_, err = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), presence)
	require.NoError(t, err)
	presence, err = newCanaryNodePresenceCheckpoint("node-a", presenceIdentity, 2, now)
	require.NoError(t, err)
	_, err = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), presence)
	require.NoError(t, err)

	stats := canaryWindowAggregateForEvaluatorFixture(t)
	stats.Canary.Attempts = 20
	for windowStart := firstWindowStart; windowStart.Before(latestWindowEnd); windowStart = windowStart.Add(time.Minute) {
		windowEnd := windowStart.Add(time.Minute)
		payload := CanaryCohortWindowCheckpoint{
			SchemaVersion: canaryCohortWindowSchemaVersion,
			PoolID:        29, ActivationID: view.ActivationID, PolicyRevision: view.Revision,
			TrafficBasisPoints: view.TrafficBasisPoints, RolloutKey: rolloutKey,
			WindowSeconds: 60, WindowStartUnixMs: windowStart.UnixMilli(), WindowEndUnixMs: windowEnd.UnixMilli(),
			Control: canaryWindowStatsForTest(t, stats.Control),
			Canary:  canaryWindowStatsForTest(t, stats.Canary),
		}
		checkpoint, checkpointErr := model.NewRoutingRuntimeCheckpoint(
			"node-a", CanaryCohortWindowCheckpointKind, CanaryWindowCheckpointScope(payload),
			int64(view.Revision), 1, payload, windowEnd.Unix(), now.Add(time.Hour).Unix(),
		)
		require.NoError(t, checkpointErr)
		_, checkpointErr = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), checkpoint)
		require.NoError(t, checkpointErr)
	}

	setting := smart_routing_setting.SmartRoutingSetting{Enabled: true}
	require.NoError(t, evaluateRoutingCanarySweepContext(context.Background(), setting, now))

	var evaluations []model.RoutingCanaryEvaluation
	require.NoError(t, db.Order("window_end_ms asc").Find(&evaluations).Error)
	require.Len(t, evaluations, 2)
	for index := range evaluations {
		assert.Equal(t, firstWindowStart.Add(time.Duration(index+1)*time.Minute).UnixMilli(), evaluations[index].WindowEndMs)
		assert.Equal(t, 10_000, evaluations[index].NodeCoverageBasisPoints)
		assert.Equal(t, model.RoutingCanaryEvaluationStatusBreached, evaluations[index].Status)
	}

	var operation model.RoutingOperation
	require.NoError(t, db.First(&operation).Error)
	assert.Equal(t, evaluations[1].EvaluationHash, operation.EvaluationHash)

	ResetCanaryControlForTest()
	require.NoError(t, evaluateRoutingCanarySweepContext(context.Background(), setting, now))
	var evaluationCount int64
	require.NoError(t, db.Model(&model.RoutingCanaryEvaluation{}).Count(&evaluationCount).Error)
	assert.Equal(t, int64(2), evaluationCount)
	assertRoutingOperationCount(t, db, 1)
}

func TestCanaryRollbackOperationRequiresAdjacentBreachWindows(t *testing.T) {
	db := openCanaryWindowTestDB(t, false)
	withCanaryWindowTestDB(t, db)
	require.NoError(t, db.AutoMigrate(&model.RoutingCanaryEvaluation{}, &model.RoutingOperation{}))

	windowStartMs := int64(1_700_000_000_000)
	windowSizeMs := int64(5 * time.Minute / time.Millisecond)
	target := canaryEvaluationTargetForTest(windowStartMs - int64(time.Hour/time.Millisecond))
	target.Policy.ConsecutiveBreachWindows = 2
	aggregate := canaryWindowAggregateForTest(t)
	aggregate.Control.Attempts = 1_050
	aggregate.Canary.Attempts = 120

	firstSpec, err := buildRoutingCanaryEvaluationSpec(target, aggregate, windowStartMs, windowStartMs+windowSizeMs)
	require.NoError(t, err)
	require.Equal(t, model.RoutingCanaryEvaluationStatusBreached, firstSpec.Status)
	first, _, err := model.CreateRoutingCanaryEvaluationContext(context.Background(), firstSpec)
	require.NoError(t, err)
	require.NoError(t, ensureCanaryRollbackOperationContext(context.Background(), target, first))
	assertRoutingOperationCount(t, db, 0)

	secondSpec, err := buildRoutingCanaryEvaluationSpec(
		target, aggregate, firstSpec.WindowEndMs, firstSpec.WindowEndMs+windowSizeMs,
	)
	require.NoError(t, err)
	second, _, err := model.CreateRoutingCanaryEvaluationContext(context.Background(), secondSpec)
	require.NoError(t, err)
	require.NoError(t, ensureCanaryRollbackOperationContext(context.Background(), target, second))
	require.NoError(t, ensureCanaryRollbackOperationContext(context.Background(), target, second))
	assertRoutingOperationCount(t, db, 1)
}

func TestRuntimeUsesOneFixedWorkerForCanaryControl(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan string, 3)
	runtime := newRuntime(ctx, runtimeDeps{
		getSetting: func() smart_routing_setting.SmartRoutingSetting {
			return smart_routing_setting.SmartRoutingSetting{Enabled: true}
		},
		heartbeatCanary: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			events <- "presence"
			<-ctx.Done()
			return ctx.Err()
		},
		evaluateCanary: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			events <- "evaluate"
			<-ctx.Done()
			return ctx.Err()
		},
		executeCanary: func(ctx context.Context, _ smart_routing_setting.SmartRoutingSetting) error {
			events <- "operate"
			<-ctx.Done()
			return ctx.Err()
		},
		wait: waitRuntime,
	})

	seen := map[string]int{}
	seen[<-events]++
	seen[<-events]++
	seen[<-events]++
	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	require.NoError(t, runtime.Wait(waitCtx))
	assert.Equal(t, map[string]int{"evaluate": 1, "operate": 1, "presence": 1}, seen)
}

func TestCanaryOperationWorkerExecutesPoolScopedRollback(t *testing.T) {
	db := openCanaryWindowTestDB(t, false)
	withCanaryWindowTestDB(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingPolicyHead{},
		&model.RoutingPolicyRevision{},
		&model.RoutingPolicyPoolRevision{},
		&model.RoutingPolicyMemberRevision{},
		&model.RoutingPolicyActivation{},
		&model.RoutingConfigOutbox{},
		&model.RoutingControlLease{},
		&model.RoutingCanaryEvaluation{},
		&model.RoutingOperation{},
	))
	require.NoError(t, model.EnsureRoutingPolicyHeadContext(context.Background()))
	document := model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicySchemaVersion,
		Pools: []model.RoutingPolicyPoolContent{{
			PoolID: 29, GroupName: "default", DisplayName: "Default",
			DeploymentStage: model.RoutingDeploymentStageCanary,
			PolicyProfile:   model.RoutingPolicyProfileBalanced,
			Members: []model.RoutingPolicyMemberContent{{
				MemberID: 101, ChannelID: 201, Enabled: true, Priority: 1, Weight: 100,
			}},
		}},
	}
	published, err := model.PublishRoutingPolicyRevisionContext(
		context.Background(), 0, document,
		model.RoutingPolicyActivationSpec{
			Stage: model.RoutingDeploymentStageCanary, TrafficBasisPoints: 100,
			ActorID: 1, Reason: "canary rollout",
		},
	)
	require.NoError(t, err)
	rolloutKey, err := CanaryRolloutKey(
		29, published.Activation.ID, uint64(published.Revision.Revision), published.Activation.TrafficBasisPoints,
	)
	require.NoError(t, err)
	target := canaryEvaluationTargetForTest(time.Now().Add(-time.Hour).UnixMilli())
	target.PolicyRevision = published.Revision.Revision
	target.ActivationID = published.Activation.ID
	target.RolloutKey = rolloutKey
	target.Policy.ConsecutiveBreachWindows = 1
	aggregate := canaryWindowAggregateForTest(t)
	aggregate.Control.Attempts = 1_050
	aggregate.Canary.Attempts = 120
	windowEndMs := time.Now().Add(-time.Minute).UnixMilli()
	windowStartMs := windowEndMs - int64(5*time.Minute/time.Millisecond)
	spec, err := buildRoutingCanaryEvaluationSpec(target, aggregate, windowStartMs, windowEndMs)
	require.NoError(t, err)
	require.Equal(t, model.RoutingCanaryEvaluationStatusBreached, spec.Status)
	evaluation, _, err := model.CreateRoutingCanaryEvaluationContext(context.Background(), spec)
	require.NoError(t, err)
	require.NoError(t, ensureCanaryRollbackOperationContext(context.Background(), target, evaluation))

	blockedAtMs := time.Now().UnixMilli()
	evaluatorLease, acquired, err := model.TryAcquireRoutingControlLeaseContext(
		context.Background(), routingCanaryEvaluatorLeaseName, "other-node", blockedAtMs,
		int64(time.Minute/time.Millisecond), 0, true,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, executeRoutingCanaryOperationContext(
		context.Background(), smart_routing_setting.SmartRoutingSetting{Enabled: true},
	))
	var operation model.RoutingOperation
	require.NoError(t, db.First(&operation).Error)
	assert.Equal(t, model.RoutingOperationStatusPending, operation.Status)
	require.NoError(t, model.ReleaseRoutingControlLeaseContext(context.Background(), evaluatorLease, time.Now().UnixMilli()))

	require.NoError(t, executeRoutingCanaryOperationContext(
		context.Background(), smart_routing_setting.SmartRoutingSetting{Enabled: true},
	))
	require.NoError(t, db.First(&operation).Error)
	assert.Equal(t, model.RoutingOperationStatusSucceeded, operation.Status)
	head, err := model.GetRoutingPolicyHeadContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, published.Revision.Revision+1, head.CurrentRevision)
	assert.Equal(t, model.RoutingDeploymentStageShadow, head.CurrentStage)
	rolledBack, _, err := model.LoadRoutingPolicyRevisionContext(context.Background(), head.CurrentRevision)
	require.NoError(t, err)
	require.Len(t, rolledBack.Pools, 1)
	assert.Equal(t, model.RoutingDeploymentStageShadow, rolledBack.Pools[0].DeploymentStage)
}

func runCanaryEvaluatorFixture(t *testing.T, reportNodeB bool) model.RoutingCanaryEvaluation {
	t.Helper()
	ResetSnapshotForTest()
	ResetCanaryControlForTest()
	t.Cleanup(ResetSnapshotForTest)
	t.Cleanup(ResetCanaryControlForTest)
	db := openCanaryWindowTestDB(t, false)
	withCanaryWindowTestDB(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingRuntimeCheckpoint{}, &model.RoutingCanaryEvaluation{}, &model.RoutingOperation{},
	))

	windowEnd := time.Unix(1_700_000_100, 0)
	windowStart := windowEnd.Add(-5 * time.Minute)
	now := windowEnd.Add(11 * time.Second)
	policy := model.DefaultRoutingCanaryPolicy()
	policy.Evaluation.WindowSeconds = 300
	policy.Evaluation.EvaluationIntervalSeconds = 60
	policy.Evaluation.CheckpointLatenessSeconds = 5
	policy.Evaluation.RolloutGraceSeconds = 0
	policy.Evaluation.MinCanaryRequests = 10
	policy.Evaluation.MinControlRequests = 10
	policy.Evaluation.MinTTFTSamples = 10
	policy.Evaluation.ConsecutiveBreachWindows = 2

	view := SnapshotView{
		Revision:              11,
		PolicyHash:            strings.Repeat("b", 64),
		ActivationID:          401,
		ActivationStage:       model.RoutingDeploymentStageCanary,
		TrafficBasisPoints:    100,
		ActivationCreatedTime: windowStart.Add(-time.Hour).Unix(),
		Pools: []PoolSnapshot{{
			ID: 29, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageCanary,
			CanaryPolicy: policy,
		}},
	}
	currentSnapshot.Store(&runtimeSnapshot{view: view})
	rolloutKey, err := CanaryRolloutKey(29, view.ActivationID, view.Revision, view.TrafficBasisPoints)
	require.NoError(t, err)
	defaultCanaryEvaluationSchedule.markCompleted(rolloutKey, windowStart.UnixMilli())

	presenceIdentity, err := canaryNodePresenceIdentityFromView(view)
	require.NoError(t, err)
	for _, nodeID := range []string{"node-a", "node-b"} {
		checkpoint, checkpointErr := newCanaryNodePresenceCheckpoint(
			nodeID, presenceIdentity, 1, windowStart.Add(10*time.Second),
		)
		require.NoError(t, checkpointErr)
		_, checkpointErr = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), checkpoint)
		require.NoError(t, checkpointErr)
		if nodeID == "node-a" {
			checkpoint, checkpointErr = newCanaryNodePresenceCheckpoint(nodeID, presenceIdentity, 2, now)
			require.NoError(t, checkpointErr)
			_, checkpointErr = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), checkpoint)
			require.NoError(t, checkpointErr)
		}
	}

	stats := canaryWindowAggregateForEvaluatorFixture(t)
	reportedNodes := []string{"node-a"}
	if reportNodeB {
		reportedNodes = append(reportedNodes, "node-b")
	}
	for _, nodeID := range reportedNodes {
		payload := CanaryCohortWindowCheckpoint{
			SchemaVersion: canaryCohortWindowSchemaVersion,
			PoolID:        29, ActivationID: view.ActivationID, PolicyRevision: view.Revision,
			TrafficBasisPoints: view.TrafficBasisPoints, RolloutKey: rolloutKey,
			WindowSeconds: 300, WindowStartUnixMs: windowStart.UnixMilli(), WindowEndUnixMs: windowEnd.UnixMilli(),
			Control: canaryWindowStatsForTest(t, stats.Control),
			Canary:  canaryWindowStatsForTest(t, stats.Canary),
		}
		checkpoint, checkpointErr := model.NewRoutingRuntimeCheckpoint(
			nodeID, CanaryCohortWindowCheckpointKind, CanaryWindowCheckpointScope(payload),
			int64(view.Revision), 1, payload, windowEnd.Unix(), now.Add(time.Hour).Unix(),
		)
		require.NoError(t, checkpointErr)
		_, checkpointErr = model.UpsertRoutingRuntimeCheckpointContext(context.Background(), checkpoint)
		require.NoError(t, checkpointErr)
	}

	setting := smart_routing_setting.SmartRoutingSetting{Enabled: true, HotcacheRefreshSec: 30}
	require.NoError(t, evaluateRoutingCanarySweepContext(context.Background(), setting, now))
	var evaluations []model.RoutingCanaryEvaluation
	require.NoError(t, db.Order("id asc").Find(&evaluations).Error)
	require.Len(t, evaluations, 1)
	return evaluations[0]
}

func canaryEvaluationTargetForTest(activationCreatedMs int64) canaryEvaluationTarget {
	policy := model.DefaultRoutingCanaryPolicy().Evaluation
	return canaryEvaluationTarget{
		PolicyRevision: 11, PolicyHash: strings.Repeat("b", 64), ActivationID: 401,
		ActivationCreatedMs: activationCreatedMs, PoolID: 29, TrafficBasisPoints: 100,
		RolloutKey: RolloutKey(strings.Repeat("a", 64)), Policy: policy,
	}
}

func canaryWindowAggregateForTest(t *testing.T) canaryWindowAggregate {
	t.Helper()
	return canaryWindowAggregate{
		Control: canaryCohortAggregate{
			LogicalRequests: 1_000, Successes: 990, Failures: 10, Attempts: 1_000,
			CostKnownRequests: 900, ExpectedPlatformCostNanoUSD: 900_000_000,
			TTFTSampleCount: 60, TTFT: durationSketchForTest(t, 60, 100),
		},
		Canary: canaryCohortAggregate{
			LogicalRequests: 100, Successes: 99, Failures: 1, Attempts: 100,
			CostKnownRequests: 90, ExpectedPlatformCostNanoUSD: 90_000_000,
			TTFTSampleCount: 60, TTFT: durationSketchForTest(t, 60, 100),
		},
		ExpectedNodes: 1, ReportedNodes: 1, NodeCoverageBasisPoints: 10_000,
	}
}

func canaryWindowAggregateForEvaluatorFixture(t *testing.T) canaryWindowAggregate {
	t.Helper()
	return canaryWindowAggregate{
		Control: canaryCohortAggregate{
			LogicalRequests: 100, Successes: 99, Failures: 1, Attempts: 100,
			CostKnownRequests: 100, ExpectedPlatformCostNanoUSD: 100_000_000,
			TTFTSampleCount: 10, TTFT: durationSketchForTest(t, 10, 100),
		},
		Canary: canaryCohortAggregate{
			LogicalRequests: 10, Successes: 10, Attempts: 10,
			CostKnownRequests: 10, ExpectedPlatformCostNanoUSD: 10_000_000,
			TTFTSampleCount: 10, TTFT: durationSketchForTest(t, 10, 100),
		},
	}
}

func cloneCanaryWindowAggregateForTest(source canaryWindowAggregate) canaryWindowAggregate {
	cloned := source
	cloned.Control.TTFT = source.Control.TTFT.Clone()
	cloned.Canary.TTFT = source.Canary.TTFT.Clone()
	return cloned
}

func durationSketchForTest(t *testing.T, count int64, milliseconds int64) *routingdistribution.DurationSketch {
	t.Helper()
	sketch := routingdistribution.NewDurationSketch()
	for index := int64(0); index < count; index++ {
		_, err := sketch.AddMillis(milliseconds)
		require.NoError(t, err)
	}
	return sketch
}

func canaryWindowStatsForTest(t *testing.T, aggregate canaryCohortAggregate) CanaryCohortWindowStats {
	t.Helper()
	stats := CanaryCohortWindowStats{
		LogicalRequests: aggregate.LogicalRequests, Successes: aggregate.Successes, Failures: aggregate.Failures,
		RoutingFailures: aggregate.RoutingFailures, Attempts: aggregate.Attempts,
		CostKnownRequests: aggregate.CostKnownRequests, ExpectedPlatformCostNanoUSD: aggregate.ExpectedPlatformCostNanoUSD,
		TTFTSampleCount: aggregate.TTFTSampleCount,
	}
	if aggregate.TTFTSampleCount > 0 {
		encoded, err := aggregate.TTFT.MarshalBinary()
		require.NoError(t, err)
		stats.TTFTSketchCodecVersion = routingdistribution.SketchCodecVersion
		stats.TTFTSketch = encoded
	}
	return stats
}

func assertRoutingOperationCount(t *testing.T, db *gorm.DB, want int64) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.RoutingOperation{}).Count(&count).Error)
	assert.Equal(t, want, count)
}
