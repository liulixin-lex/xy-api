package channelrouting

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingselector "github.com/QuantumNous/new-api/service/routing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRunHistoricalSimulationUsesBoundedPoolHistoryAndRebuildsCounterfactualHashes(t *testing.T) {
	db := openHistoricalSimulationTestDB(t)
	validIDs := []string{
		enqueueHistoricalSimulationAudit(t, 5, "simulation-request-1"),
		enqueueHistoricalSimulationAudit(t, 5, "simulation-request-2"),
		enqueueHistoricalSimulationAudit(t, 5, "simulation-request-3"),
	}
	enqueueHistoricalSimulationAudit(t, 6, "other-pool-request")
	_, err := EnqueueDecision(DecisionInput{
		RequestID: "non-replayable", PoolID: 5, GroupName: "group-5", ModelName: "gpt-test", SnapshotRevision: 7,
	})
	require.NoError(t, err)
	flushed, err := FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5, flushed)

	var tampered model.RoutingDecisionAudit
	require.NoError(t, db.Where("decision_id = ?", validIDs[1]).First(&tampered).Error)
	var tamperedInput ShadowReplayInput
	require.NoError(t, common.UnmarshalJsonStr(tampered.ReplayInputJSON, &tamperedInput))
	tamperedInput.Candidates[0].Cost.Cost = 0.25
	encoded, err := common.Marshal(tamperedInput)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.RoutingDecisionAudit{}).
		Where("decision_id = ?", validIDs[1]).Update("replay_input_json", string(encoded)).Error)

	zero := 0.0
	one := 1.0
	options := HistoricalSimulationOptions{
		PoolID: 5,
		Limit:  2,
		Selector: SimulationSelectorOverrides{
			WeightAvailability: &zero,
			WeightLatency:      &zero,
			WeightThroughput:   &zero,
			WeightCost:         &one,
		},
	}
	firstPage, err := RunHistoricalSimulation(context.Background(), options)
	require.NoError(t, err)
	assert.Equal(t, 2, firstPage.ScannedSamples)
	assert.NotZero(t, firstPage.NextCursor)
	assert.Equal(t, firstPage.ScannedSamples, firstPage.EvaluatedSamples+len(firstPage.Skipped))

	options.Cursor = firstPage.NextCursor
	secondPage, err := RunHistoricalSimulation(context.Background(), options)
	require.NoError(t, err)
	assert.Equal(t, 1, secondPage.ScannedSamples)
	assert.Zero(t, secondPage.NextCursor)

	options.Cursor = 0
	options.Limit = 10
	result, err := RunHistoricalSimulation(context.Background(), options)
	require.NoError(t, err)
	assert.Equal(t, 3, result.ScannedSamples)
	assert.Equal(t, 2, result.EvaluatedSamples)
	assert.Equal(t, 1, result.SkipReasons["hash_mismatch"])
	assert.Len(t, result.Skipped, 1)
	assert.Equal(t, validIDs[1], result.Skipped[0].DecisionID)
	assert.Equal(t, 0, result.ActualMatchCount)
	require.NotNil(t, result.ActualMatchRate)
	assert.Equal(t, 0.0, *result.ActualMatchRate)
	assert.Equal(t, 2, result.SelectionChangedCount)
	require.NotNil(t, result.SelectionChangeRate)
	assert.Equal(t, 1.0, *result.SelectionChangeRate)
	assert.Equal(t, 2, result.CostKnownSamples)
	assert.InDelta(t, -18.0, result.TotalExpectedCostDelta, 1e-12)
	require.NotNil(t, result.AverageCostDelta)
	assert.InDelta(t, -9.0, *result.AverageCostDelta, 1e-12)
	for _, sample := range result.Samples {
		assert.Equal(t, 101, sample.BaselineChannelID)
		assert.Equal(t, 102, sample.SimulatedChannelID)
		assert.True(t, sample.SelectionChanged)
		assert.False(t, sample.MatchesActual)
		assert.Len(t, sample.CounterfactualHash, 64)
		assert.InDelta(t, -9.0, sample.ExpectedCostDelta, 1e-12)
	}
}

func TestRunHistoricalSimulationRejectsUnsafeSelectorWindows(t *testing.T) {
	openHistoricalSimulationTestDB(t)
	negativeCursor := HistoricalSimulationOptions{PoolID: 1, Cursor: -1, Limit: 1}
	tooMany := HistoricalSimulationOptions{PoolID: 1, Limit: MaxSimulationLimit + 1}
	nan := math.NaN()
	invalidWeight := HistoricalSimulationOptions{
		PoolID: 1, Limit: 1, Selector: SimulationSelectorOverrides{WeightCost: &nan},
	}
	zero := 0.0
	zeroWeights := HistoricalSimulationOptions{
		PoolID: 1,
		Limit:  1,
		Selector: SimulationSelectorOverrides{
			WeightAvailability: &zero,
			WeightLatency:      &zero,
			WeightThroughput:   &zero,
			WeightCost:         &zero,
		},
	}
	tooLargeTopK := MaxDecisionCandidates + 1
	invalidTopK := HistoricalSimulationOptions{
		PoolID: 1, Limit: 1, Selector: SimulationSelectorOverrides{TopK: &tooLargeTopK},
	}

	for index, options := range []HistoricalSimulationOptions{
		{PoolID: 0, Limit: 1},
		{PoolID: 1, Limit: 0},
		negativeCursor,
		tooMany,
		invalidWeight,
		zeroWeights,
		invalidTopK,
	} {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			_, err := RunHistoricalSimulation(context.Background(), options)
			assert.ErrorIs(t, err, ErrSimulationInvalidOptions)
		})
	}
}

func TestRunHistoricalSimulationReplaysBalancedDraftPolicyAgainstShadowHistory(t *testing.T) {
	openHistoricalSimulationTestDB(t)
	enqueueHistoricalSimulationAudit(t, 5, "balanced-policy-simulation")
	flushed, err := FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	policy, err := resolveBalancedPoolPolicy(model.RoutingPolicyProfileBalanced, []byte(`{
		"weight_availability": 0,
		"weight_latency": 0,
		"weight_throughput": 0,
		"weight_cost": 1,
		"availability_floor": 0,
		"exploration_basis_points": 0,
		"protection_band_basis_points": 0
	}`))
	require.NoError(t, err)

	result, err := RunHistoricalSimulation(context.Background(), HistoricalSimulationOptions{
		PoolID: 5, Limit: 10, BalancedPolicy: &policy,
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAlgorithmBalanced, result.SimulatedAlgorithm)
	assert.Equal(t, 1, result.ScannedSamples)
	assert.Equal(t, 1, result.EvaluatedSamples)
	require.Len(t, result.Samples, 1)
	sample := result.Samples[0]
	assert.Equal(t, DecisionAlgorithmShadow, sample.AlgorithmVersion)
	assert.Equal(t, DecisionAlgorithmBalanced, sample.SimulatedAlgorithm)
	assert.Equal(t, 101, sample.BaselineChannelID)
	assert.Equal(t, 102, sample.SimulatedChannelID)
	assert.True(t, sample.SelectionChanged)
	assert.True(t, sample.BaselineCostKnown)
	assert.True(t, sample.SimulatedCostKnown)
	assert.InDelta(t, 10.0, sample.BaselineExpectedCost, 1e-12)
	assert.InDelta(t, 1.0, sample.SimulatedExpectedCost, 1e-12)
	assert.InDelta(t, -9.0, sample.ExpectedCostDelta, 1e-12)
	assert.Len(t, sample.CounterfactualHash, 64)
}

func TestRunPolicyDocumentSimulationAppliesDraftMembership(t *testing.T) {
	db := openHistoricalSimulationTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.RoutingPool{},
		&model.RoutingPoolMember{},
		&model.RoutingCredentialRef{},
	))
	enqueueHistoricalSimulationAudit(t, 5, "balanced-draft-membership")
	flushed, err := FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)
	document := model.RoutingPolicyDocument{
		SchemaVersion: model.RoutingPolicySchemaVersion,
		Pools: []model.RoutingPolicyPoolContent{{
			PoolID: 5, GroupName: "group-5", DisplayName: "group-5",
			DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile:   model.RoutingPolicyProfileBalanced,
			Policy: json.RawMessage(`{
				"weight_availability": 0,
				"weight_latency": 0,
				"weight_throughput": 0,
				"weight_cost": 1,
				"availability_floor": 0,
				"exploration_basis_points": 0,
				"protection_band_basis_points": 0
			}`),
			Members: []model.RoutingPolicyMemberContent{
				{MemberID: 11, ChannelID: 101, Enabled: false, Priority: 10, Weight: 10, Overrides: json.RawMessage(`{}`)},
				{MemberID: 12, ChannelID: 102, Enabled: true, Priority: 10, Weight: 10, Overrides: json.RawMessage(`{}`)},
			},
		}},
	}

	result, err := RunPolicyDocumentSimulation(context.Background(), document, 5, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, DecisionAlgorithmBalanced, result.SimulatedAlgorithm)
	require.Len(t, result.Samples, 1)
	assert.Equal(t, 102, result.Samples[0].SimulatedChannelID)
	assert.True(t, result.Samples[0].SelectionChanged)
}

func TestRunPolicyDocumentSimulationAgainstBaseAttachesHistoricalRisk(t *testing.T) {
	db := openHistoricalSimulationTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	require.NoError(t, db.Create([]model.Channel{
		{Id: 101, Name: "simulation-101", Models: "gpt-test"},
		{Id: 102, Name: "simulation-102", Models: "gpt-test"},
	}).Error)
	enqueueHistoricalSimulationAudit(t, 5, "balanced-risk-simulation")
	flushed, err := FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)

	base := simulationRiskDocument(model.RoutingPolicyPoolContent{
		PoolID: 5, GroupName: "group-5", DisplayName: "group-5",
		DeploymentStage: model.RoutingDeploymentStageActive,
		PolicyProfile:   model.RoutingPolicyProfileBalanced,
		Policy:          json.RawMessage(`{}`),
		Members: []model.RoutingPolicyMemberContent{
			{MemberID: 11, ChannelID: 101, Enabled: true, Priority: 10, Weight: 10, Overrides: json.RawMessage(`{}`)},
			{MemberID: 12, ChannelID: 102, Enabled: true, Priority: 10, Weight: 10, Overrides: json.RawMessage(`{}`)},
		},
	})
	draft := base
	draft.Pools = append([]model.RoutingPolicyPoolContent(nil), base.Pools...)
	draft.Pools[0].Policy = json.RawMessage(`{
		"weight_availability": 0,
		"weight_latency": 0,
		"weight_throughput": 0,
		"weight_cost": 1,
		"availability_floor": 0,
		"exploration_basis_points": 0,
		"protection_band_basis_points": 0
	}`)

	result, err := RunPolicyDocumentSimulationAgainstBase(context.Background(), base, draft, 5, 0, 10)
	require.NoError(t, err)
	require.NotNil(t, result.Risk)
	assert.Equal(t, PolicySimulationRiskUnknown, result.Risk.State)
	assert.Equal(t, PolicySimulationStatusUnknown, result.Risk.SLO.State)
	assert.Equal(t, "mixed", result.Risk.SLO.Assessment)
	require.NotNil(t, result.Risk.SLO.AverageSuccessRateDelta)
	assert.InDelta(t, -0.20, *result.Risk.SLO.AverageSuccessRateDelta, 1e-12)
	require.NotNil(t, result.Risk.SLO.AverageLatencyDeltaMs)
	assert.InDelta(t, -50, *result.Risk.SLO.AverageLatencyDeltaMs, 1e-12)
	assert.Equal(t, "p95_latency_ms", result.Risk.SLO.LatencyMetric)
	assert.Equal(t, PolicySimulationCapacityUnknown, result.Risk.Capacity.State)
	assert.Equal(t, PolicySimulationTrafficUnknown, result.Risk.Traffic.State)
	assert.Equal(t, []int{5}, result.Risk.Scope.AffectedPoolIDs)
	assert.Equal(t, []int{101, 102}, result.Risk.Scope.AffectedChannelIDs)
	assert.Equal(t, []string{"gpt-test"}, result.Risk.Scope.AffectedModels)
	assert.Contains(t, result.Risk.Reasons, "slo_tradeoff_requires_review")
	assert.Contains(t, result.Risk.Reasons, "capacity_evidence_incomplete")
	assert.Contains(t, result.Risk.Reasons, "traffic_change_rate_limit_unconfigured")
}

func TestRunPolicyDocumentSimulationAgainstBaseMaterializesDynamicPoolDefaults(t *testing.T) {
	db := openHistoricalSimulationTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.Channel{},
		&model.RoutingPool{},
		&model.RoutingPoolMember{},
		&model.RoutingCredentialRef{},
	))
	require.NoError(t, db.Create([]model.Channel{
		{
			Id: 101, Name: "dynamic-default-101", Models: "gpt-test",
			RoutingIdentity: strings.Repeat("1", 32), RoutingGeneration: strings.Repeat("2", 32),
		},
		{
			Id: 102, Name: "dynamic-default-102", Models: "gpt-test",
			RoutingIdentity: strings.Repeat("3", 32), RoutingGeneration: strings.Repeat("4", 32),
		},
	}).Error)
	require.NoError(t, db.Create(&model.RoutingPool{
		ID: 5, GroupKey: "group-5", GroupName: "group-5", DisplayName: "group-5",
		Source: model.RoutingPoolSourceLegacyGroup, Active: true,
		DefaultEnabled: true, DefaultPriority: 10, DefaultWeight: 10,
	}).Error)
	require.NoError(t, db.Create([]model.RoutingPoolMember{
		{
			ID: 11, PoolID: 5, ChannelID: 101, ChannelGeneration: strings.Repeat("2", 32),
			Source: model.RoutingPoolSourceLegacyGroup, Active: true,
		},
		{
			ID: 12, PoolID: 5, ChannelID: 102, ChannelGeneration: strings.Repeat("4", 32),
			Source: model.RoutingPoolSourceLegacyGroup, Active: true,
		},
	}).Error)
	enqueueHistoricalSimulationAudit(t, 5, "dynamic-pool-default-simulation")
	flushed, err := FlushDecisionAuditsContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, flushed)

	defaultEnabled := true
	defaultPriority := int64(10)
	baseWeight := int64(10)
	draftWeight := int64(20)
	base := simulationRiskDocument(model.RoutingPolicyPoolContent{
		PoolID: 5, GroupName: "group-5", DisplayName: "group-5",
		DeploymentStage: model.RoutingDeploymentStageActive,
		PolicyProfile:   model.RoutingPolicyProfileBalanced,
		Policy:          json.RawMessage(`{}`),
		DefaultEnabled:  &defaultEnabled,
		DefaultPriority: &defaultPriority,
		DefaultWeight:   &baseWeight,
		Members:         []model.RoutingPolicyMemberContent{},
	})
	draft := base
	draft.Pools = append([]model.RoutingPolicyPoolContent(nil), base.Pools...)
	draft.Pools[0].DefaultWeight = &draftWeight

	result, err := RunPolicyDocumentSimulationAgainstBase(context.Background(), base, draft, 5, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, result.EvaluatedSamples)
	require.Len(t, result.Samples, 1)
	assert.NotZero(t, result.Samples[0].SimulatedChannelID)
	require.NotNil(t, result.Risk)
	assert.NotEqual(t, PolicySimulationRiskReady, result.Risk.State)
	assert.True(t, result.Risk.Changes.TrafficAffecting)
	assert.Equal(t, 2, result.Risk.Changes.ChangedMembers)
	assert.Equal(t, 2, result.Risk.Changes.MemberWeightChanges)
	assert.Equal(t, []int{5}, result.Risk.Scope.AffectedPoolIDs)
	assert.Equal(t, []int{101, 102}, result.Risk.Scope.AffectedChannelIDs)
	assert.Equal(t, []string{"gpt-test"}, result.Risk.Scope.AffectedModels)
}

func TestPolicySimulationRiskScopesStructuralChangesAndAffectedResources(t *testing.T) {
	db := openHistoricalSimulationTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	require.NoError(t, db.Create([]model.Channel{
		{Id: 101, Name: "scope-101", Models: "gpt-a,gpt-b"},
		{Id: 102, Name: "scope-102", Models: "gpt-b"},
		{Id: 103, Name: "scope-103", Models: "gpt-c"},
		{Id: 104, Name: "scope-104", Models: "gpt-d"},
		{Id: 105, Name: "scope-105", Models: "gpt-e"},
	}).Error)
	base := simulationRiskDocument(
		simulationRiskPool(5, 101, 102),
		simulationRiskPool(6, 104),
	)
	draftPool := simulationRiskPool(5, 101, 102)
	draftPool.DeploymentStage = model.RoutingDeploymentStageActive
	draftPool.PolicyProfile = model.RoutingPolicyProfileEnterpriseSLO
	draftPool.Policy = json.RawMessage(`{"max_capacity_utilization":0.8}`)
	draftPool.Members[0].Weight = 20
	draftPool.Members[1].Enabled = false
	draftPool.Members = append(draftPool.Members, model.RoutingPolicyMemberContent{
		MemberID: 13, ChannelID: 103, Enabled: true, Priority: 10, Weight: 10, Overrides: json.RawMessage(`{}`),
	})
	draft := simulationRiskDocument(draftPool, simulationRiskPool(7, 105))

	scope, changes := policySimulationImpactScope(context.Background(), base, draft, 5)
	assert.Equal(t, []int{5, 6, 7}, scope.AffectedPoolIDs)
	assert.Equal(t, 2, scope.UnsimulatedPoolCount)
	assert.Equal(t, []int{6, 7}, scope.UnsimulatedPoolIDs)
	assert.Equal(t, []int{101, 102, 103, 104, 105}, scope.AffectedChannelIDs)
	assert.Equal(t, []string{"gpt-a", "gpt-b", "gpt-c", "gpt-d", "gpt-e"}, scope.AffectedModels)
	assert.Equal(t, PolicySimulationEvidenceKnown, scope.ModelEvidenceState)
	assert.False(t, scope.Truncated)
	assert.Equal(t, 1, changes.AddedPools)
	assert.Equal(t, 1, changes.RemovedPools)
	assert.Equal(t, 1, changes.PolicyChanges)
	assert.Equal(t, 1, changes.DeploymentStageChanges)
	assert.Equal(t, 1, changes.PolicyProfileChanges)
	assert.Equal(t, 1, changes.PolicyConfigChanges)
	assert.Equal(t, 2, changes.AddedMembers)
	assert.Equal(t, 1, changes.RemovedMembers)
	assert.Equal(t, 2, changes.ChangedMembers)
	assert.Equal(t, 1, changes.MemberEnablementChanges)
	assert.Equal(t, 1, changes.MemberWeightChanges)
	assert.True(t, changes.TrafficAffecting)

	assessment := assessPolicySimulationRisk(context.Background(), base, draft, 5, HistoricalSimulationResult{})
	assert.Equal(t, PolicySimulationRiskUnknown, assessment.State)
	assert.Contains(t, assessment.Reasons, "changed_pools_not_simulated")
}

func TestPolicySimulationRiskNeverTreatsSkippedOrMissingEvidenceAsReady(t *testing.T) {
	base := simulationRiskDocument(simulationRiskPool(5, 101))
	draft := simulationRiskDocument(simulationRiskPool(5, 101))
	draft.Pools[0].Members[0].Weight = 20
	result := HistoricalSimulationResult{
		ScannedSamples: 2, EvaluatedSamples: 1,
		Skipped:             []HistoricalSimulationSkip{{DecisionID: "skipped", Reason: "invalid_replay"}},
		riskSLOKnownSamples: 1, riskLatencyMetric: "p95_latency_ms",
		riskCapacityKnownSamples: 1, riskCapacityLimitKnown: true, riskCapacityLimit: 0.8,
	}

	assessment := assessPolicySimulationRisk(context.Background(), base, draft, 5, result)
	assert.Equal(t, PolicySimulationRiskUnknown, assessment.State)
	assert.Equal(t, PolicySimulationStatusUnknown, assessment.SLO.State)
	assert.Equal(t, 2, assessment.SLO.TotalSamples)
	assert.Equal(t, PolicySimulationCapacityUnknown, assessment.Capacity.State)
	assert.Equal(t, 2, assessment.Capacity.TotalSamples)
	assert.Contains(t, assessment.Reasons, "slo_evidence_incomplete")
	assert.Contains(t, assessment.Reasons, "capacity_evidence_incomplete")
	assert.Contains(t, assessment.Reasons, "traffic_change_rate_limit_unconfigured")
}

func TestPolicySimulationRiskBlocksKnownCapacityAndReportsSLOImpact(t *testing.T) {
	base := simulationRiskDocument(simulationRiskPool(5, 101))
	draft := simulationRiskDocument(simulationRiskPool(5, 101))
	draft.Pools[0].Members[0].Weight = 20
	result := HistoricalSimulationResult{
		ScannedSamples: 2, EvaluatedSamples: 2,
		riskSLOKnownSamples: 2, riskSuccessRateDeltaTotal: -0.10,
		riskLatencyDeltaTotal: 50, riskLatencyMetric: "p95_latency_ms",
		riskCapacityKnownSamples: 2, riskCapacityExceededSamples: 1,
		riskMaxCapacityUtilization: 0.95, riskCapacityLimit: 0.80, riskCapacityLimitKnown: true,
	}

	assessment := assessPolicySimulationRisk(context.Background(), base, draft, 5, result)
	assert.Equal(t, PolicySimulationRiskBlocked, assessment.State)
	assert.Equal(t, PolicySimulationStatusFail, assessment.SLO.State)
	assert.Equal(t, "degraded", assessment.SLO.Assessment)
	require.NotNil(t, assessment.SLO.AverageSuccessRateDelta)
	assert.InDelta(t, -0.05, *assessment.SLO.AverageSuccessRateDelta, 1e-12)
	require.NotNil(t, assessment.SLO.AverageLatencyDeltaMs)
	assert.InDelta(t, 25, *assessment.SLO.AverageLatencyDeltaMs, 1e-12)
	assert.Equal(t, PolicySimulationCapacityInsufficient, assessment.Capacity.State)
	assert.Equal(t, 1, assessment.Capacity.ExceededSamples)
	require.NotNil(t, assessment.Capacity.MaxObservedUtilization)
	assert.InDelta(t, 0.95, *assessment.Capacity.MaxObservedUtilization, 1e-12)
	require.NotNil(t, assessment.Capacity.UtilizationLimit)
	assert.InDelta(t, 0.80, *assessment.Capacity.UtilizationLimit, 1e-12)
	assert.Contains(t, assessment.Reasons, "slo_degradation_detected")
	assert.Contains(t, assessment.Reasons, "capacity_insufficient")
}

func TestPolicySimulationRiskTreatsDisplayOnlyChangeAsReady(t *testing.T) {
	base := simulationRiskDocument(simulationRiskPool(5, 101))
	draft := simulationRiskDocument(simulationRiskPool(5, 101))
	draft.Pools[0].DisplayName = "renamed display"

	assessment := assessPolicySimulationRisk(context.Background(), base, draft, 5, HistoricalSimulationResult{})
	assert.Equal(t, PolicySimulationRiskReady, assessment.State)
	assert.False(t, assessment.Changes.TrafficAffecting)
	assert.Equal(t, PolicySimulationStatusPass, assessment.SLO.State)
	assert.Equal(t, "stable", assessment.SLO.Assessment)
	assert.Equal(t, PolicySimulationCapacitySufficient, assessment.Capacity.State)
	assert.Equal(t, PolicySimulationTrafficWithinLimit, assessment.Traffic.State)
	assert.Empty(t, assessment.Reasons)
}

func TestPolicySimulationRiskBoundsLargeAffectedScopesAcrossQueryBatches(t *testing.T) {
	db := openHistoricalSimulationTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	const channelCount = snapshotMetricRollupPageSize + 1
	channels := make([]model.Channel, channelCount)
	channelIDs := make([]int, channelCount)
	for index := range channels {
		channelID := 1_000 + index
		channelIDs[index] = channelID
		channels[index] = model.Channel{
			Id: channelID, Name: fmt.Sprintf("scope-batch-%03d", index), Models: fmt.Sprintf("model-%03d", index),
		}
	}
	require.NoError(t, db.CreateInBatches(channels, 100).Error)
	draft := simulationRiskDocument(simulationRiskPool(9, channelIDs...))

	scope, changes := policySimulationImpactScope(
		context.Background(), simulationRiskDocument(), draft, 9,
	)
	assert.Equal(t, channelCount, scope.AffectedChannelCount)
	assert.Len(t, scope.AffectedChannelIDs, maxPolicySimulationImpactItems)
	assert.Equal(t, channelCount, scope.AffectedModelCount)
	assert.Len(t, scope.AffectedModels, maxPolicySimulationImpactItems)
	assert.Equal(t, PolicySimulationEvidenceKnown, scope.ModelEvidenceState)
	assert.True(t, scope.Truncated)
	assert.True(t, changes.TrafficAffecting)
}

func TestPolicySimulationRiskExternalDatabaseCompatibility(t *testing.T) {
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
			require.NoError(t, db.AutoMigrate(
				&model.Channel{},
				&model.RoutingPool{},
				&model.RoutingPoolMember{},
				&model.RoutingCredentialRef{},
				&model.RoutingDecisionAudit{},
			))
			require.NoError(t, db.Create([]model.Channel{
				{
					Id: 901, Name: "simulation-risk-901", Models: "gpt-a,gpt-b",
					RoutingIdentity: strings.Repeat("5", 32), RoutingGeneration: strings.Repeat("6", 32),
				},
				{
					Id: 902, Name: "simulation-risk-902", Models: "gpt-b,gpt-c",
					RoutingIdentity: strings.Repeat("7", 32), RoutingGeneration: strings.Repeat("8", 32),
				},
			}).Error)
			require.NoError(t, db.Create(&model.RoutingPool{
				ID: 9, GroupKey: "group-9", GroupName: "group-9", DisplayName: "group-9",
				Source: model.RoutingPoolSourceLegacyGroup, Active: true,
				DefaultEnabled: true, DefaultPriority: 10, DefaultWeight: 10,
			}).Error)
			require.NoError(t, db.Create([]model.RoutingPoolMember{
				{
					ID: 901, PoolID: 9, ChannelID: 901, ChannelGeneration: strings.Repeat("6", 32),
					Source: model.RoutingPoolSourceLegacyGroup, Active: true,
				},
				{
					ID: 902, PoolID: 9, ChannelID: 902, ChannelGeneration: strings.Repeat("8", 32),
					Source: model.RoutingPoolSourceLegacyGroup, Active: true,
				},
			}).Error)
			defaultEnabled := true
			defaultPriority := int64(10)
			baseWeight := int64(10)
			draftWeight := int64(20)
			base := simulationRiskDocument(model.RoutingPolicyPoolContent{
				PoolID: 9, GroupName: "group-9", DisplayName: "group-9",
				DeploymentStage: model.RoutingDeploymentStageActive,
				PolicyProfile:   model.RoutingPolicyProfileBalanced,
				Policy:          json.RawMessage(`{}`),
				DefaultEnabled:  &defaultEnabled,
				DefaultPriority: &defaultPriority,
				DefaultWeight:   &baseWeight,
				Members:         []model.RoutingPolicyMemberContent{},
			})
			draft := base
			draft.Pools = append([]model.RoutingPolicyPoolContent(nil), base.Pools...)
			draft.Pools[0].DefaultWeight = &draftWeight

			result, err := RunPolicyDocumentSimulationAgainstBase(
				context.Background(), base, draft, 9, 0, 10,
			)
			require.NoError(t, err)
			require.NotNil(t, result.Risk)
			assert.Equal(t, []int{9}, result.Risk.Scope.AffectedPoolIDs)
			assert.Equal(t, []int{901, 902}, result.Risk.Scope.AffectedChannelIDs)
			assert.Equal(t, []string{"gpt-a", "gpt-b", "gpt-c"}, result.Risk.Scope.AffectedModels)
			assert.Equal(t, PolicySimulationEvidenceKnown, result.Risk.Scope.ModelEvidenceState)
			assert.False(t, result.Risk.Scope.Truncated)
			assert.Equal(t, 2, result.Risk.Changes.MemberWeightChanges)
			assert.True(t, result.Risk.Changes.TrafficAffecting)
		})
	}
}

func TestBalancedSimulationRiskEvidenceUsesSelectedTTFTAndRuntimeCapacity(t *testing.T) {
	decision := historicalSimulationDecision{channelID: 101}
	input := BalancedReplayInput{
		Settings: BalancedReplaySettings{PreferTTFT: true},
		Candidates: []BalancedReplayCandidate{{
			ChannelID: 101,
			Metric: &ShadowMetricInput{
				ReliabilityRequestCount: 100, ReliabilityFailureCount: 2,
				P95LatencyMs: 200, P95TTFTMs: 80,
			},
		}},
		RuntimeStates: []BalancedReplayRuntimeState{{
			ChannelID: 101, HasCapacityUtilization: true, CapacityUtilization: 0.75,
		}},
	}

	evidence := attachBalancedSimulationEvidence(decision, input)
	assert.True(t, evidence.successRateKnown)
	assert.InDelta(t, 0.98, evidence.successRate, 1e-12)
	assert.True(t, evidence.latencyKnown)
	assert.Equal(t, "p95_ttft_ms", evidence.latencyMetric)
	assert.InDelta(t, 80, evidence.latencyMs, 1e-12)
	assert.True(t, evidence.capacityKnown)
	assert.InDelta(t, 0.75, evidence.capacityUtilization, 1e-12)

	result := HistoricalSimulationResult{riskCapacityLimitKnown: true, riskCapacityLimit: 0.75}
	result.addRiskEvidence(evidence, evidence)
	assert.Equal(t, 1, result.riskSLOKnownSamples)
	assert.Equal(t, 1, result.riskCapacityKnownSamples)
	assert.Equal(t, 1, result.riskCapacityExceededSamples)
}

func enqueueHistoricalSimulationAudit(t *testing.T, poolID int, requestID string) string {
	t.Helper()
	profile, err := NewLegacyRequestProfile("/v1/chat/completions", "group-"+strconv.Itoa(poolID), "gpt-test", false, 0, 1_000, 200)
	require.NoError(t, err)
	seed, err := DeriveShadowSeed(requestID, 7, profile.RetryIndex)
	require.NoError(t, err)
	input, err := BuildShadowReplayInput(poolID, 7, 3, strings.Repeat("a", 64), profile, routingselector.Settings{
		WeightAvailability: 1,
		WeightLatency:      0,
		WeightThroughput:   0,
		WeightCost:         0,
		AvailabilityFloor:  0,
		MinVolume:          1,
		TopK:               1,
		MaxEjectedPct:      50,
		HalfOpenProbes:     1,
		SnapshotStaleSec:   1_800,
		NowUnix:            1_000,
		NowUnixMilli:       1_000_000,
		RandomSeed:         seed,
	}, []ShadowCandidateInput{
		{
			PoolMemberID: 11, ChannelID: 101, Priority: 10, Weight: 10,
			Metric: &ShadowMetricInput{RequestCount: 100, SuccessCount: 100, ReliabilityRequestCount: 100, P95LatencyMs: 300, OutputTokensPerSecond: 50},
			Cost:   &ShadowReplayCostInput{Known: true, Cost: 10, UpdatedUnix: 990},
		},
		{
			PoolMemberID: 12, ChannelID: 102, Priority: 10, Weight: 10,
			Metric: &ShadowMetricInput{RequestCount: 100, SuccessCount: 80, ReliabilityRequestCount: 100, ReliabilityFailureCount: 20, P95LatencyMs: 250, OutputTokensPerSecond: 60},
			Cost:   &ShadowReplayCostInput{Known: true, Cost: 1, UpdatedUnix: 990},
		},
	})
	require.NoError(t, err)
	replay, err := RunShadowReplay(input)
	require.NoError(t, err)
	actualCost, actualCostKnown := ShadowExpectedCostForChannel(input, 101)
	decisionID, err := EnqueueDecision(DecisionInput{
		RequestID:            requestID,
		PoolID:               poolID,
		GroupName:            profile.GroupName,
		ModelName:            profile.ModelName,
		SnapshotRevision:     input.PolicyRevision,
		AlgorithmVersion:     input.AlgorithmVersion,
		RetryIndex:           profile.RetryIndex,
		IsStream:             profile.IsStream,
		ActualChannelID:      101,
		ObservedChannelID:    replay.SelectedChannelID,
		FilteredOpen:         replay.FilteredOpen,
		FilteredCapacity:     replay.FilteredCapacity,
		BreakerBypassed:      replay.BreakerBypassed,
		Candidates:           replay.Candidates,
		ReplayInput:          &input,
		DifferenceType:       ClassifyShadowDifference(101, replay),
		ActualCostKnown:      actualCostKnown,
		ActualExpectedCost:   actualCost,
		ObservedCostKnown:    replay.SelectedCostKnown,
		ObservedExpectedCost: replay.SelectedCost,
	})
	require.NoError(t, err)
	return decisionID
}

func openHistoricalSimulationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RoutingDecisionAudit{}))
	previousDB := model.DB
	model.DB = db
	ResetDecisionAuditsForTest(16)
	t.Cleanup(func() {
		model.DB = previousDB
		ResetDecisionAuditsForTest()
	})
	return db
}

func simulationRiskDocument(pools ...model.RoutingPolicyPoolContent) model.RoutingPolicyDocument {
	return model.RoutingPolicyDocument{SchemaVersion: model.RoutingPolicySchemaVersion, Pools: pools}
}

func simulationRiskPool(poolID int, channelIDs ...int) model.RoutingPolicyPoolContent {
	members := make([]model.RoutingPolicyMemberContent, len(channelIDs))
	for index, channelID := range channelIDs {
		members[index] = model.RoutingPolicyMemberContent{
			MemberID: poolID*100 + index + 1, ChannelID: channelID,
			Enabled: true, Priority: 10, Weight: 10, Overrides: json.RawMessage(`{}`),
		}
	}
	return model.RoutingPolicyPoolContent{
		PoolID: poolID, GroupName: "group-" + strconv.Itoa(poolID), DisplayName: "group-" + strconv.Itoa(poolID),
		DeploymentStage: model.RoutingDeploymentStageShadow,
		PolicyProfile:   model.RoutingPolicyProfileBalanced, Policy: json.RawMessage(`{}`), Members: members,
	}
}
