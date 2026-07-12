package channelrouting

import (
	"context"
	"math"
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

func enqueueHistoricalSimulationAudit(t *testing.T, poolID int, requestID string) string {
	t.Helper()
	profile, err := NewRequestProfile("/v1/chat/completions", "group-"+strconv.Itoa(poolID), "gpt-test", false, 0, 1_000, 200)
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
			Cost:   &ShadowCostInput{Known: true, Cost: 10, UpdatedUnix: 990},
		},
		{
			PoolMemberID: 12, ChannelID: 102, Priority: 10, Weight: 10,
			Metric: &ShadowMetricInput{RequestCount: 100, SuccessCount: 80, ReliabilityRequestCount: 100, ReliabilityFailureCount: 20, P95LatencyMs: 250, OutputTokensPerSecond: 60},
			Cost:   &ShadowCostInput{Known: true, Cost: 1, UpdatedUnix: 990},
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
