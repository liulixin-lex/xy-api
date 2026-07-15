package routing

import (
	"math"
	"sort"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectBalancedUsesAbsoluteUtilitiesIndependentOfOtherCandidates(t *testing.T) {
	settings := balancedSettingsForTest()
	first := balancedCandidateForTest(1, 100, 20, 1, 1)
	second := balancedCandidateForTest(2, 200, 20, 1, 1)

	baseline, err := SelectBalanced([]BalancedCandidate{first, second}, settings)
	require.NoError(t, err)
	withOutlier, err := SelectBalanced([]BalancedCandidate{
		first,
		second,
		balancedCandidateForTest(3, 10_000, 1, 1, 100),
	}, settings)
	require.NoError(t, err)

	assert.Equal(t, balancedRankedByID(t, baseline, 1).UtilityScore, balancedRankedByID(t, withOutlier, 1).UtilityScore)
	assert.Equal(t, balancedRankedByID(t, baseline, 2).UtilityScore, balancedRankedByID(t, withOutlier, 2).UtilityScore)
}

func TestSelectBalancedWilsonAvailabilityDoesNotTreatOneOfOneAsCertain(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.Weights = Weights{Availability: 1}
	settings.LatencyTargetMs = 0
	settings.ThroughputTarget = 0
	settings.CostTarget = 0
	lowSample := balancedCandidateForTest(1, 100, 20, 0, 1)
	lowSample.Candidate.Metric.ReliabilityRequestCount = 1
	lowSample.Candidate.Metric.ReliabilityFailureCount = 0
	highSample := balancedCandidateForTest(2, 100, 20, 0, 1)
	highSample.Candidate.Metric.ReliabilityRequestCount = 1_000
	highSample.Candidate.Metric.ReliabilityFailureCount = 10

	decision, err := SelectBalanced([]BalancedCandidate{lowSample, highSample}, settings)
	require.NoError(t, err)

	low := balancedRankedByID(t, decision, 1)
	high := balancedRankedByID(t, decision, 2)
	assert.Less(t, low.Availability, high.Availability)
	assert.Less(t, low.UtilityScore, high.UtilityScore)
	require.NotNil(t, decision.Selected)
	assert.Equal(t, 2, decision.Selected.Channel.Id)
}

func TestSelectBalancedUsesExplicitUnknownMetricUtilitiesForColdCandidates(t *testing.T) {
	settings := balancedSettingsForTest()
	cold := balancedCandidateForTest(1, 100, 20, 0, 1)
	cold.Candidate.Metric = nil

	decision, err := SelectBalanced([]BalancedCandidate{cold}, settings)
	require.NoError(t, err)

	require.NotNil(t, decision.Selected)
	ranked := balancedRankedByID(t, decision, 1)
	assert.Equal(t, settings.UnknownAvailability, ranked.Availability)
	assert.Equal(t, settings.UnknownLatencyUtility, ranked.LatencyUtility)
	assert.Equal(t, settings.UnknownThroughputUtility, ranked.ThroughputUtility)
	assert.Greater(t, ranked.UtilityScore, 0.0)
	assert.Less(t, ranked.LatencyUtility, 1.0, "missing latency must not look perfect")
}

func TestSelectBalancedGeometricUtilityPreventsCheapUnreliableCandidateFromWinning(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.Weights = Weights{Availability: 0.75, Cost: 0.25}
	settings.LatencyTargetMs = 0
	settings.ThroughputTarget = 0
	unreliable := balancedCandidateForTest(1, 100, 20, 50, 0.1)
	reliable := balancedCandidateForTest(2, 100, 20, 1, 1)

	decision, err := SelectBalanced([]BalancedCandidate{unreliable, reliable}, settings)
	require.NoError(t, err)

	require.NotNil(t, decision.Selected)
	assert.Equal(t, 2, decision.Selected.Channel.Id)
	assert.Less(t, balancedRankedByID(t, decision, 1).UtilityScore, balancedRankedByID(t, decision, 2).UtilityScore)
}

func TestSelectBalancedWeightedP2CChoosesLowerLoadFromDeterministicPair(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 10_000
	settings.RandomSeed = 0
	busy := balancedCandidateForTest(1, 100, 20, 0, 1)
	busy.CapacityUtilization = 0.80
	idle := balancedCandidateForTest(2, 100, 20, 0, 1)
	idle.CapacityUtilization = 0.10

	first, err := SelectBalanced([]BalancedCandidate{busy, idle}, settings)
	require.NoError(t, err)
	second, err := SelectBalanced([]BalancedCandidate{busy, idle}, settings)
	require.NoError(t, err)

	assert.Equal(t, []int{first.SampledChannelIDs[0], first.SampledChannelIDs[1]}, second.SampledChannelIDs)
	require.NotNil(t, first.Selected)
	assert.Equal(t, 2, first.Selected.Channel.Id)
	assert.Greater(t, balancedRankedByID(t, first, 2).LoadUtility, balancedRankedByID(t, first, 1).LoadUtility)
}

func TestSelectBalancedWeightedP2CPreservesExtremeTargetShare(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 10_000
	settings.RandomSeed = 1
	heavy := balancedCandidateForTest(1, 100, 20, 0, 1)
	heavy.TargetWeight = 1_000
	light := balancedCandidateForTest(2, 100, 20, 0, 1)
	light.TargetWeight = 1
	light.CapacityUtilization = 0
	heavy.CapacityUtilization = 0.5

	decision, err := SelectBalanced([]BalancedCandidate{heavy, light}, settings)
	require.NoError(t, err)

	assert.Equal(t, []int{1, 1}, decision.SampledChannelIDs, "independent weighted draws must not force a tiny-share member into every P2C pair")
	require.NotNil(t, decision.Selected)
	assert.Equal(t, 1, decision.Selected.Channel.Id)

	heavy.TargetWeight = math.MaxFloat64
	decision, err = SelectBalanced([]BalancedCandidate{heavy, light}, settings)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 1}, decision.SampledChannelIDs, "finite weight sums must not overflow into uniform sampling")
}

func TestSelectBalancedAffinityRequiresProtectionBandAndCapacityHeadroom(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.PreferredChannelID = 2
	settings.ProtectionBandBasisPoints = 1_000
	best := balancedCandidateForTest(1, 100, 20, 0, 1)
	preferred := balancedCandidateForTest(2, 105, 20, 0, 1)

	withinBand, err := SelectBalanced([]BalancedCandidate{best, preferred}, settings)
	require.NoError(t, err)
	require.NotNil(t, withinBand.Selected)
	assert.Equal(t, 2, withinBand.Selected.Channel.Id)
	assert.True(t, withinBand.AffinityUsed)

	preferred.Candidate.Metric.P95LatencyMs = 1_000
	outsideBand, err := SelectBalanced([]BalancedCandidate{best, preferred}, settings)
	require.NoError(t, err)
	require.NotNil(t, outsideBand.Selected)
	assert.Equal(t, 1, outsideBand.Selected.Channel.Id)
	assert.False(t, outsideBand.AffinityUsed)

	preferred.Candidate.Metric.P95LatencyMs = 105
	preferred.CapacityUtilization = settings.AffinityMaxCapacityUtilization + 0.01
	settings.RandomSeed = 0
	capacityTight, err := SelectBalanced([]BalancedCandidate{best, preferred}, settings)
	require.NoError(t, err)
	require.NotNil(t, capacityTight.Selected)
	assert.Equal(t, 1, capacityTight.Selected.Channel.Id)
	assert.False(t, capacityTight.AffinityUsed)
}

func TestSelectBalancedExplorationNeverBypassesHardConstraints(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.ExplorationBasisPoints = 300
	settings.RandomSeed = 74
	settings.ProtectionBandBasisPoints = 0
	best := balancedCandidateForTest(1, 100, 20, 0, 1)
	exploration := balancedCandidateForTest(2, 250, 20, 0, 1)
	exploration.ExplorationEligible = true
	hardExcluded := balancedCandidateForTest(3, 100, 20, 0, 1)
	hardExcluded.HardExclusionReason = "capability_mismatch"
	hardExcluded.ExplorationEligible = true

	decision, err := SelectBalanced([]BalancedCandidate{best, exploration, hardExcluded}, settings)
	require.NoError(t, err)

	require.True(t, decision.ExplorationUsed, "test seed must deterministically enter the bounded exploration cohort")
	require.NotNil(t, decision.Selected)
	assert.Equal(t, 2, decision.Selected.Channel.Id)
	assert.Equal(t, "capability_mismatch", balancedExclusionByID(t, decision, 3).Reason)
}

func TestSelectBalancedSoftFallbackCannotBypassCredentialOrCapacityHardFailures(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.AllowSoftFailureFallback = true
	open := balancedCandidateForTest(1, 100, 20, 0, 1)
	open.Candidate.Breaker = &BreakerSnapshot{State: BreakerStateOpen, CooldownUntilUnix: 2_000, UpdatedUnix: 900}
	credential := balancedCandidateForTest(2, 100, 20, 0, 1)
	credential.Candidate.Breaker = &BreakerSnapshot{State: BreakerStateOpen, Reason: BreakerReasonAuthFail, UpdatedUnix: 900}
	capacity := balancedCandidateForTest(3, 100, 20, 0, 1)
	capacity.CapacityUtilization = 1

	decision, err := SelectBalanced([]BalancedCandidate{open, credential, capacity}, settings)
	require.NoError(t, err)

	require.True(t, decision.SoftFallback)
	require.NotNil(t, decision.Selected)
	assert.Equal(t, 1, decision.Selected.Channel.Id)
	assert.Equal(t, BalancedExclusionCredentialUnavailable, balancedExclusionByID(t, decision, 2).Reason)
	assert.Equal(t, BalancedExclusionCapacityExhausted, balancedExclusionByID(t, decision, 3).Reason)
}

func TestSelectBalancedStaleHardBreakerStateRemainsUnavailable(t *testing.T) {
	settings := balancedSettingsForTest()
	credential := balancedCandidateForTest(1, 100, 20, 0, 1)
	credential.Candidate.Breaker = &BreakerSnapshot{
		State: BreakerStateOpen, Reason: BreakerReasonAuthFail, UpdatedUnix: 1,
	}

	decision, err := SelectBalanced([]BalancedCandidate{credential}, settings)
	require.NoError(t, err)

	assert.Nil(t, decision.Selected)
	assert.Equal(t, BalancedExclusionCredentialUnavailable, balancedExclusionByID(t, decision, 1).Reason)

	settings.NowUnix = 0
	_, err = SelectBalanced([]BalancedCandidate{credential}, settings)
	assert.ErrorIs(t, err, ErrBalancedPolicyInvalid, "an invalid planning clock must not bypass staleness and cooldown checks")
}

func TestSelectBalancedUnknownCostIsNeverTreatedAsFree(t *testing.T) {
	settings := balancedSettingsForTest()
	known := balancedCandidateForTest(1, 100, 20, 0, 2)
	unknown := balancedCandidateForTest(2, 100, 20, 0, 0)
	unknown.Candidate.Cost = nil
	missingTimestamp := balancedCandidateForTest(3, 100, 20, 0, 0.1)
	missingTimestamp.Candidate.Cost.UpdatedUnix = 0
	futureTimestamp := balancedCandidateForTest(4, 100, 20, 0, 0.1)
	futureTimestamp.Candidate.Cost.UpdatedUnix = settings.NowUnix + 1

	decision, err := SelectBalanced([]BalancedCandidate{known, unknown, missingTimestamp, futureTimestamp}, settings)
	require.NoError(t, err)
	assert.False(t, balancedRankedByID(t, decision, 2).CostKnown)
	assert.Equal(t, settings.UnknownCostUtility, balancedRankedByID(t, decision, 2).CostUtility)
	assert.NotEqual(t, 1.0, balancedRankedByID(t, decision, 2).CostUtility)
	assert.False(t, balancedRankedByID(t, decision, 3).CostKnown, "known cost without an observation timestamp is still unknown")
	assert.False(t, balancedRankedByID(t, decision, 4).CostKnown, "future observations must not bypass freshness")

	settings.RequireKnownCost = true
	strict, err := SelectBalanced([]BalancedCandidate{known, unknown, missingTimestamp, futureTimestamp}, settings)
	require.NoError(t, err)
	assert.Equal(t, BalancedExclusionCostUnknown, balancedExclusionByID(t, strict, 2).Reason)
	assert.Equal(t, BalancedExclusionCostUnknown, balancedExclusionByID(t, strict, 3).Reason)
	assert.Equal(t, BalancedExclusionCostUnknown, balancedExclusionByID(t, strict, 4).Reason)
}

func TestSelectBalancedBusinessTierCascadeDoesNotMutateCandidates(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.EnforceBusinessTierCascade = true
	lowTier := balancedCandidateForTest(1, 100, 20, 0, 1)
	lowTier.BusinessTier = 1
	highTier := balancedCandidateForTest(2, 500, 10, 2, 2)
	highTier.BusinessTier = 2
	beforeLow := lowTier
	beforeLowChannel := *lowTier.Candidate.Channel
	beforeLowMetric := *lowTier.Candidate.Metric
	beforeLowCost := *lowTier.Candidate.Cost

	decision, err := SelectBalanced([]BalancedCandidate{lowTier, highTier}, settings)
	require.NoError(t, err)

	require.NotNil(t, decision.Selected)
	assert.Equal(t, 2, decision.Selected.Channel.Id)
	assert.Equal(t, BalancedExclusionBusinessTier, balancedExclusionByID(t, decision, 1).Reason)
	assert.Equal(t, beforeLow, lowTier)
	assert.Equal(t, beforeLowChannel, *lowTier.Candidate.Channel)
	assert.Equal(t, beforeLowMetric, *lowTier.Candidate.Metric)
	assert.Equal(t, beforeLowCost, *lowTier.Candidate.Cost)
}

func TestSelectBalancedDecisionIsInvariantToCandidateInputOrder(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 10_000
	settings.RandomSeed = 73
	first := balancedCandidateForTest(1, 100.0000001, 20, 0, 1)
	second := balancedCandidateForTest(2, 100.0000002, 20, 0, 1)
	third := balancedCandidateForTest(3, 100.0000003, 20, 0, 1)

	forward, err := SelectBalanced([]BalancedCandidate{first, second, third}, settings)
	require.NoError(t, err)
	reversed, err := SelectBalanced([]BalancedCandidate{third, second, first}, settings)
	require.NoError(t, err)

	assert.Equal(t, balancedRankedIDs(forward), balancedRankedIDs(reversed))
	assert.Equal(t, forward.SampledChannelIDs, reversed.SampledChannelIDs)
	require.NotNil(t, forward.Selected)
	require.NotNil(t, reversed.Selected)
	assert.Equal(t, forward.Selected.Channel.Id, reversed.Selected.Channel.Id)
}

func TestSelectBalancedRejectsInvalidPolicyAndDuplicateChannels(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.ExplorationBasisPoints = 301
	_, err := SelectBalanced([]BalancedCandidate{balancedCandidateForTest(1, 100, 20, 0, 1)}, settings)
	assert.ErrorIs(t, err, ErrBalancedPolicyInvalid)
	settings.ExplorationBasisPoints = 99
	_, err = SelectBalanced([]BalancedCandidate{balancedCandidateForTest(1, 100, 20, 0, 1)}, settings)
	assert.ErrorIs(t, err, ErrBalancedPolicyInvalid)
	settings = balancedSettingsForTest()
	settings.WilsonZ = 11
	_, err = SelectBalanced([]BalancedCandidate{balancedCandidateForTest(1, 100, 20, 0, 1)}, settings)
	assert.ErrorIs(t, err, ErrBalancedPolicyInvalid)
	settings = balancedSettingsForTest()
	settings.WilsonZ = 0.99
	_, err = SelectBalanced([]BalancedCandidate{balancedCandidateForTest(1, 100, 20, 0, 1)}, settings)
	assert.ErrorIs(t, err, ErrBalancedPolicyInvalid)
	settings = balancedSettingsForTest()
	settings.UnknownCostUtility = 1
	_, err = SelectBalanced([]BalancedCandidate{balancedCandidateForTest(1, 100, 20, 0, 1)}, settings)
	assert.ErrorIs(t, err, ErrBalancedPolicyInvalid)
	settings = balancedSettingsForTest()
	settings.UnknownLatencyUtility = 0
	_, err = SelectBalanced([]BalancedCandidate{balancedCandidateForTest(1, 100, 20, 0, 1)}, settings)
	assert.ErrorIs(t, err, ErrBalancedPolicyInvalid)
	settings = balancedSettingsForTest()
	settings.NowUnixMilli += 3_000
	_, err = SelectBalanced([]BalancedCandidate{balancedCandidateForTest(1, 100, 20, 0, 1)}, settings)
	assert.ErrorIs(t, err, ErrBalancedPolicyInvalid)
	settings = balancedSettingsForTest()
	settings.CostBudget = 1
	_, err = SelectBalanced([]BalancedCandidate{balancedCandidateForTest(1, 100, 20, 0, 1)}, settings)
	assert.ErrorIs(t, err, ErrBalancedPolicyInvalid, "a hard cost budget requires known fresh cost")

	settings = balancedSettingsForTest()
	candidate := balancedCandidateForTest(1, 100, 20, 0, 1)
	_, err = SelectBalanced([]BalancedCandidate{candidate, candidate}, settings)
	assert.ErrorIs(t, err, ErrBalancedCandidateInvalid)

	candidates := make([]BalancedCandidate, 129)
	for index := range candidates {
		candidates[index] = balancedCandidateForTest(index+1, 100, 20, 0, 1)
	}
	_, err = SelectBalanced(candidates, balancedSettingsForTest())
	require.NoError(t, err)
}

func TestPreparedBalancedHotPathConsidersFullPoolDuringDynamicCapacityFallback(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 10_000
	candidates := make([]BalancedCandidate, 129)
	for index := range candidates {
		candidates[index] = balancedCandidateForTest(index+1, 100, 20, 0, 1)
	}
	prepared, err := PrepareBalanced(candidates, settings)
	require.NoError(t, err)
	runtimeState := make(map[int]BalancedRuntimeState, 128)
	for channelID := 1; channelID <= 128; channelID++ {
		runtimeState[channelID] = BalancedRuntimeState{CapacityUtilization: 1}
	}

	decision, err := prepared.Select(BalancedRequest{RandomSeed: 73, RuntimeByChannelID: runtimeState})
	require.NoError(t, err)

	require.Empty(t, decision.Ranked, "hot-path decisions must not expose the immutable prepared ranking")
	require.Len(t, prepared.Snapshot().Ranked, 129)
	assert.Equal(t, 129, decision.SelectedChannelID, "precompiled hot-path fallback must retain every legal pool member")
	assert.Nil(t, decision.Selected)
}

func TestPreparedBalancedRetainsCandidatesThatRecoverAfterCompile(t *testing.T) {
	settings := balancedSettingsForTest()
	candidate := balancedCandidateForTest(1, 100, 20, 0, 1)
	candidate.CapacityUtilization = 1
	prepared, err := PrepareBalanced([]BalancedCandidate{candidate}, settings)
	require.NoError(t, err)

	blocked, err := prepared.Select(BalancedRequest{RandomSeed: 1})
	require.NoError(t, err)
	assert.Zero(t, blocked.SelectedChannelID)

	recovered, err := prepared.Select(BalancedRequest{
		RandomSeed: 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {CapacityUtilization: 0, HasCapacityUtilization: true},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, recovered.SelectedChannelID)

	soft := balancedCandidateForTest(2, 100, 20, 0, 1)
	soft.Candidate.Breaker = &BreakerSnapshot{
		State: BreakerStateOpen, CooldownUntilUnix: 2_000, UpdatedUnix: settings.NowUnix,
	}
	preparedSoft, err := PrepareBalanced([]BalancedCandidate{soft}, settings)
	require.NoError(t, err)
	stillSoft, err := preparedSoft.Select(BalancedRequest{RandomSeed: 1})
	require.NoError(t, err)
	assert.Zero(t, stillSoft.SelectedChannelID)
	recoveredSoft, err := preparedSoft.Select(BalancedRequest{
		RandomSeed: 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			2: {Admission: BalancedRuntimeAdmissionHealthy},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, recoveredSoft.SelectedChannelID)
}

func TestPreparedBalancedUsesRequestCostOverrideForBudgetAndSelection(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.Weights = Weights{Availability: 0.1, Latency: 0.1, Throughput: 0.1, Cost: 0.7}
	settings.CostTarget = 1
	settings.CostBudget = 2
	settings.RequireKnownCost = true

	first := balancedCandidateForTest(1, 100, 20, 0, 1)
	second := balancedCandidateForTest(2, 100, 20, 0, 1)
	prepared, err := PrepareBalanced([]BalancedCandidate{first, second}, settings)
	require.NoError(t, err)

	decision, err := prepared.Select(BalancedRequest{
		RandomSeed: 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {Cost: &CostSnapshot{Known: true, Cost: 4, UpdatedUnix: settings.NowUnix}},
			2: {Cost: &CostSnapshot{Known: true, Cost: 0.5, UpdatedUnix: settings.NowUnix}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, decision.SelectedChannelID)

	unknown, err := prepared.Select(BalancedRequest{
		RandomSeed: 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {Cost: &CostSnapshot{Known: false, UpdatedUnix: settings.NowUnix}},
			2: {Cost: &CostSnapshot{Known: false, UpdatedUnix: settings.NowUnix}},
		},
	})
	require.NoError(t, err)
	assert.Zero(t, unknown.SelectedChannelID)
}

func TestPreparedBalancedSelectDetailedMaterializesSnapshotCost(t *testing.T) {
	settings := balancedSettingsForTest()
	candidate := balancedCandidateForTest(1, 100, 20, 0, 0.25)
	prepared, err := PrepareBalanced([]BalancedCandidate{candidate}, settings)
	require.NoError(t, err)

	decision, err := prepared.SelectDetailed(BalancedRequest{RandomSeed: 1})
	require.NoError(t, err)
	require.NotNil(t, decision.Selected)
	require.NotNil(t, decision.Selected.Candidate.Candidate.Cost)
	assert.True(t, decision.Selected.CostKnown)
	assert.InDelta(t, 0.25, decision.Selected.Candidate.Candidate.Cost.Cost, 1e-12)
}

func TestPreparedBalancedFallsBackAcrossSoftStateAndBusinessTiersAtRuntime(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.AllowSoftFailureFallback = true
	settings.EnforceBusinessTierCascade = true
	highTier := balancedCandidateForTest(1, 100, 20, 0, 1)
	highTier.BusinessTier = 2
	lowTier := balancedCandidateForTest(2, 100, 20, 0, 1)
	lowTier.BusinessTier = 1
	soft := balancedCandidateForTest(3, 100, 20, 0, 1)
	soft.BusinessTier = 3
	soft.Candidate.Breaker = &BreakerSnapshot{
		State: BreakerStateOpen, CooldownUntilUnix: 2_000, UpdatedUnix: settings.NowUnix,
	}
	prepared, err := PrepareBalanced([]BalancedCandidate{highTier, lowTier, soft}, settings)
	require.NoError(t, err)

	lowerTier, err := prepared.Select(BalancedRequest{
		RandomSeed: 7,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {CapacityUtilization: 1},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, lowerTier.SelectedChannelID)
	assert.False(t, lowerTier.SoftFallback)

	softFallback, err := prepared.Select(BalancedRequest{
		RandomSeed: 7,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {CapacityUtilization: 1},
			2: {CapacityUtilization: 1},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 3, softFallback.SelectedChannelID)
	assert.True(t, softFallback.SoftFallback)
}

func TestPreparedBalancedRecomputesProtectionBandAfterRuntimeFiltering(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.Weights = Weights{Latency: 1}
	settings.AvailabilityTarget = 1
	settings.ThroughputTarget = 0
	settings.CostTarget = 0
	settings.ProtectionBandBasisPoints = 1_000
	best := balancedCandidateForTest(1, 100, 20, 0, 1)
	newBest := balancedCandidateForTest(2, 250, 20, 0, 1)
	farBelowBand := balancedCandidateForTest(3, 1_000, 20, 0, 1)
	farBelowBand.TargetWeight = math.MaxFloat64
	prepared, err := PrepareBalanced([]BalancedCandidate{best, newBest, farBelowBand}, settings)
	require.NoError(t, err)

	for seed := int64(0); seed < 20; seed++ {
		decision, selectErr := prepared.Select(BalancedRequest{
			RandomSeed: seed,
			RuntimeByChannelID: map[int]BalancedRuntimeState{
				1: {CapacityUtilization: 1},
			},
		})
		require.NoError(t, selectErr)
		assert.Equal(t, 2, decision.SelectedChannelID, "seed=%d", seed)
	}
}

func TestSelectBalancedAndPreparedSelectShareReplaySemantics(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 10_000
	candidates := []BalancedCandidate{
		balancedCandidateForTest(1, 100, 20, 0, 1),
		balancedCandidateForTest(2, 100, 20, 0, 1),
		balancedCandidateForTest(3, 100, 20, 0, 1),
	}
	prepared, err := PrepareBalanced(candidates, settings)
	require.NoError(t, err)

	for _, seed := range []int64{0, 1, 7, 73, 999, -1} {
		settings.RandomSeed = seed
		direct, directErr := SelectBalanced(candidates, settings)
		require.NoError(t, directErr)
		hot, hotErr := prepared.Select(BalancedRequest{RandomSeed: seed})
		require.NoError(t, hotErr)
		require.NotNil(t, direct.Selected)
		assert.Equal(t, direct.SampledChannelIDs, hot.SampledChannelIDs, "seed=%d", seed)
		assert.Equal(t, direct.Selected.Channel.Id, hot.SelectedChannelID, "seed=%d", seed)
		assert.Equal(t, direct.AffinityUsed, hot.AffinityUsed, "seed=%d", seed)
		assert.Equal(t, direct.ExplorationUsed, hot.ExplorationUsed, "seed=%d", seed)
	}
}

func TestPreparedBalancedDynamicWeightedDrawPreservesExtremeShareAndInputOrder(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 10_000
	heavy := balancedCandidateForTest(1, 100, 20, 0, 1)
	heavy.TargetWeight = math.MaxFloat64
	light := balancedCandidateForTest(2, 100, 20, 0, 1)
	light.TargetWeight = 1
	forward, err := PrepareBalanced([]BalancedCandidate{heavy, light}, settings)
	require.NoError(t, err)
	reversed, err := PrepareBalanced([]BalancedCandidate{light, heavy}, settings)
	require.NoError(t, err)
	slowStartFactor := 1.0
	request := BalancedRequest{
		RandomSeed: 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {SlowStartFactor: &slowStartFactor},
		},
	}

	forwardDecision, err := forward.Select(request)
	require.NoError(t, err)
	reversedDecision, err := reversed.Select(request)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 1}, forwardDecision.SampledChannelIDs)
	assert.Equal(t, forwardDecision.SampledChannelIDs, reversedDecision.SampledChannelIDs)
	assert.Equal(t, 1, forwardDecision.SelectedChannelID)
	assert.Equal(t, forwardDecision.SelectedChannelID, reversedDecision.SelectedChannelID)
}

func TestPreparedBalancedRuntimeSlowStartCooldownAndHardAdmission(t *testing.T) {
	settings := balancedSettingsForTest()
	candidate := balancedCandidateForTest(1, 100, 20, 0, 1)
	candidate.SlowStartFactor = 0
	prepared, err := PrepareBalanced([]BalancedCandidate{candidate}, settings)
	require.NoError(t, err)

	withoutRamp, err := prepared.Select(BalancedRequest{RandomSeed: 1})
	require.NoError(t, err)
	assert.Zero(t, withoutRamp.SelectedChannelID)

	ramped := 1.0
	activeCooldown := settings.NowUnixMilli + 10_000
	blocked, err := prepared.Select(BalancedRequest{
		RandomSeed:   1,
		NowUnixMilli: settings.NowUnixMilli,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {SlowStartFactor: &ramped, CooldownUntilUnixMilli: &activeCooldown},
		},
	})
	require.NoError(t, err)
	assert.Zero(t, blocked.SelectedChannelID)

	recovered, err := prepared.Select(BalancedRequest{
		RandomSeed:   1,
		NowUnixMilli: activeCooldown + 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {SlowStartFactor: &ramped, CooldownUntilUnixMilli: &activeCooldown},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, recovered.SelectedChannelID)

	hardBlocked, err := prepared.Select(BalancedRequest{
		RandomSeed: 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {SlowStartFactor: &ramped, Admission: BalancedRuntimeAdmissionBlocked},
		},
	})
	require.NoError(t, err)
	assert.Zero(t, hardBlocked.SelectedChannelID, "failed half-open/probe admission must never enter soft fallback")
}

func TestPreparedBalancedDoesNotLeakMutableSnapshotState(t *testing.T) {
	settings := balancedSettingsForTest()
	candidate := balancedCandidateForTest(1, 100, 20, 0, 1)
	key := "key-a"
	candidate.Candidate.Channel.Keys = []string{key}
	candidate.Candidate.Channel.ChannelInfo.MultiKeyStatusList = map[int]int{0: 1}
	originalWeight := *candidate.Candidate.Channel.Weight
	prepared, err := PrepareBalanced([]BalancedCandidate{candidate}, settings)
	require.NoError(t, err)

	*candidate.Candidate.Channel.Weight = 999
	candidate.Candidate.Channel.Keys[0] = "mutated-input"
	candidate.Candidate.Channel.ChannelInfo.MultiKeyStatusList[0] = 999
	snapshot := prepared.Snapshot()
	require.Len(t, snapshot.Ranked, 1)
	assert.Equal(t, originalWeight, *snapshot.Ranked[0].Channel.Weight)
	assert.Equal(t, key, snapshot.Ranked[0].Channel.Keys[0])
	assert.Equal(t, 1, snapshot.Ranked[0].Channel.ChannelInfo.MultiKeyStatusList[0])

	*snapshot.Ranked[0].Channel.Weight = 777
	snapshot.Ranked[0].Channel.Keys[0] = "mutated-snapshot"
	snapshot.Ranked[0].Channel.ChannelInfo.MultiKeyStatusList[0] = 777
	decision, err := prepared.Select(BalancedRequest{RandomSeed: 1})
	require.NoError(t, err)
	assert.Equal(t, 1, decision.SelectedChannelID)
	assert.Nil(t, decision.Selected)
	require.Empty(t, decision.Ranked)
	prepared.materializeSelected(BalancedRequest{RandomSeed: 1}, &decision)
	require.NotNil(t, decision.Selected)
	*decision.Selected.Channel.Weight = 555
	decision.Selected.Channel.Keys[0] = "mutated-selection"

	after := prepared.Snapshot()
	assert.Equal(t, originalWeight, *after.Ranked[0].Channel.Weight)
	assert.Equal(t, key, after.Ranked[0].Channel.Keys[0])
	assert.Equal(t, 1, after.Ranked[0].Channel.ChannelInfo.MultiKeyStatusList[0])
}

func TestPreparedBalancedAffinityDropsRuntimeDegradedCandidate(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.PreferredChannelID = 2
	settings.ProtectionBandBasisPoints = 10_000
	prepared, err := PrepareBalanced([]BalancedCandidate{
		balancedCandidateForTest(1, 100, 20, 0, 1),
		balancedCandidateForTest(2, 100, 20, 0, 1),
	}, settings)
	require.NoError(t, err)

	decision, err := prepared.Select(BalancedRequest{
		RandomSeed: 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			2: {Admission: BalancedRuntimeAdmissionDegraded},
		},
	})
	require.NoError(t, err)
	assert.NotZero(t, decision.SelectedChannelID)
	assert.False(t, decision.AffinityUsed)
}

func TestPreparedBalancedLiveInflightBreaksTiedP2CSelection(t *testing.T) {
	settings := balancedSettingsForTest()
	prepared, err := PrepareBalanced([]BalancedCandidate{
		balancedCandidateForTest(1, 100, 20, 0, 1),
		balancedCandidateForTest(2, 100, 20, 0, 1),
	}, settings)
	require.NoError(t, err)

	decision, err := prepared.Select(BalancedRequest{
		RandomSeed: 3,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {Inflight: 9, HasInflight: true},
			2: {Inflight: 0, HasInflight: true},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, decision.SampledChannelIDs)
	assert.Equal(t, 2, decision.SelectedChannelID)
}

func TestPreparedBalancedRejectsMalformedRuntimeOverrides(t *testing.T) {
	settings := balancedSettingsForTest()
	prepared, err := PrepareBalanced(
		[]BalancedCandidate{balancedCandidateForTest(1, 100, 20, 0, 1)},
		settings,
	)
	require.NoError(t, err)

	_, err = prepared.Select(BalancedRequest{
		RuntimeByChannelID: map[int]BalancedRuntimeState{2: {}},
	})
	assert.ErrorIs(t, err, ErrBalancedCandidateInvalid)
	_, err = prepared.Select(BalancedRequest{
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {Admission: BalancedRuntimeAdmission("unexpected")},
		},
	})
	assert.ErrorIs(t, err, ErrBalancedCandidateInvalid)
	_, err = prepared.Select(BalancedRequest{
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {Breaker: &BreakerSnapshot{State: "unexpected"}},
		},
	})
	assert.ErrorIs(t, err, ErrBalancedCandidateInvalid)
	_, err = prepared.Select(BalancedRequest{NowUnixMilli: settings.NowUnixMilli - 1})
	assert.ErrorIs(t, err, ErrBalancedCandidateInvalid)
}

func TestPreparedBalancedPartialRuntimeOverridePreservesSnapshotCapacity(t *testing.T) {
	settings := balancedSettingsForTest()
	candidate := balancedCandidateForTest(1, 100, 20, 0, 1)
	candidate.CapacityUtilization = 1
	prepared, err := PrepareBalanced([]BalancedCandidate{candidate}, settings)
	require.NoError(t, err)
	slowStartFactor := 0.5

	stillBlocked, err := prepared.Select(BalancedRequest{
		RandomSeed: 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {SlowStartFactor: &slowStartFactor},
		},
	})
	require.NoError(t, err)
	assert.Zero(t, stillBlocked.SelectedChannelID, "a slow-start-only override must not clear saturated snapshot capacity")

	recovered, err := prepared.Select(BalancedRequest{
		RandomSeed: 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {
				CapacityUtilization:    0,
				HasCapacityUtilization: true,
				SlowStartFactor:        &slowStartFactor,
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, recovered.SelectedChannelID)
	prepared.materializeSelected(BalancedRequest{
		RandomSeed: 1,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {
				CapacityUtilization:    0,
				HasCapacityUtilization: true,
				SlowStartFactor:        &slowStartFactor,
			},
		},
	}, &recovered)
	require.NotNil(t, recovered.Selected)
	assert.Zero(t, recovered.Selected.Candidate.CapacityUtilization)
}

func TestPreparedBalancedRechecksCostFreshnessAtRequestTime(t *testing.T) {
	settings := balancedSettingsForTest()
	settings.Weights = Weights{Cost: 1}
	settings.LatencyTargetMs = 0
	settings.ThroughputTarget = 0
	settings.CostTarget = 1
	candidate := balancedCandidateForTest(1, 100, 20, 0, 1)
	prepared, err := PrepareBalanced([]BalancedCandidate{candidate}, settings)
	require.NoError(t, err)
	expiredAtMillis := (candidate.Candidate.Cost.UpdatedUnix + int64(settings.SnapshotStaleSec) + 1) * 1_000

	unknown, err := prepared.Select(BalancedRequest{RandomSeed: 1, NowUnixMilli: expiredAtMillis})
	require.NoError(t, err)
	assert.Equal(t, 1, unknown.SelectedChannelID)
	prepared.materializeSelected(BalancedRequest{RandomSeed: 1, NowUnixMilli: expiredAtMillis}, &unknown)
	require.NotNil(t, unknown.Selected)
	assert.False(t, unknown.Selected.CostKnown)
	assert.Equal(t, settings.UnknownCostUtility, unknown.Selected.CostUtility)

	settings.RequireKnownCost = true
	strict, err := PrepareBalanced([]BalancedCandidate{candidate}, settings)
	require.NoError(t, err)
	blocked, err := strict.Select(BalancedRequest{RandomSeed: 1, NowUnixMilli: expiredAtMillis})
	require.NoError(t, err)
	assert.Zero(t, blocked.SelectedChannelID)
}

var (
	balancedBenchmarkDecision BalancedDecision
	balancedBenchmarkPrepared *PreparedBalancedPool
)

func BenchmarkSelectBalanced100(b *testing.B) {
	benchmarkSelectBalanced(b, 100)
}

func BenchmarkPrepareBalanced4096(b *testing.B) {
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 2_000
	candidates := balancedBenchmarkCandidates(MaxBalancedCandidates)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		prepared, err := PrepareBalanced(candidates, settings)
		if err != nil {
			b.Fatal(err)
		}
		balancedBenchmarkPrepared = prepared
	}
}

func benchmarkSelectBalanced(b *testing.B, candidateCount int) {
	b.Helper()
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 2_000
	candidates := balancedBenchmarkCandidates(candidateCount)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		decision, err := SelectBalanced(candidates, settings)
		if err != nil {
			b.Fatal(err)
		}
		balancedBenchmarkDecision = decision
	}
}

func BenchmarkSelectPreparedBalanced4096(b *testing.B) {
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 2_000
	candidates := balancedBenchmarkCandidates(MaxBalancedCandidates)
	prepared, err := PrepareBalanced(candidates, settings)
	if err != nil {
		b.Fatal(err)
	}
	request := BalancedRequest{RandomSeed: 73}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		decision, selectErr := prepared.Select(request)
		if selectErr != nil {
			b.Fatal(selectErr)
		}
		balancedBenchmarkDecision = decision
	}
}

func BenchmarkSelectPreparedBalancedDynamic4096(b *testing.B) {
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 2_000
	candidates := balancedBenchmarkCandidates(MaxBalancedCandidates)
	prepared, err := PrepareBalanced(candidates, settings)
	if err != nil {
		b.Fatal(err)
	}
	slowStartFactor := 0.5
	request := BalancedRequest{
		RandomSeed: 73,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {SlowStartFactor: &slowStartFactor},
		},
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		decision, selectErr := prepared.Select(request)
		if selectErr != nil {
			b.Fatal(selectErr)
		}
		balancedBenchmarkDecision = decision
	}
}

func BenchmarkSelectPreparedBalancedDynamic4096Latency(b *testing.B) {
	settings := balancedSettingsForTest()
	settings.ProtectionBandBasisPoints = 2_000
	prepared, err := PrepareBalanced(balancedBenchmarkCandidates(MaxBalancedCandidates), settings)
	if err != nil {
		b.Fatal(err)
	}
	slowStartFactor := 0.5
	request := BalancedRequest{
		RandomSeed: 73,
		RuntimeByChannelID: map[int]BalancedRuntimeState{
			1: {SlowStartFactor: &slowStartFactor},
		},
	}
	latencies := make([]int64, b.N)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		startedAt := time.Now()
		decision, selectErr := prepared.Select(request)
		latencies[index] = time.Since(startedAt).Nanoseconds()
		if selectErr != nil {
			b.Fatal(selectErr)
		}
		balancedBenchmarkDecision = decision
	}
	b.StopTimer()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	percentileIndex := func(percent int) int {
		return max(0, min(len(latencies)-1, (len(latencies)*percent+99)/100-1))
	}
	b.ReportMetric(float64(latencies[percentileIndex(50)]), "p50-ns")
	b.ReportMetric(float64(latencies[percentileIndex(95)]), "p95-ns")
	b.ReportMetric(float64(latencies[percentileIndex(99)]), "p99-ns")
	b.ReportMetric(float64(latencies[len(latencies)-1]), "max-ns")
}

func balancedBenchmarkCandidates(candidateCount int) []BalancedCandidate {
	candidates := make([]BalancedCandidate, candidateCount)
	for index := range candidates {
		candidates[index] = balancedCandidateForTest(
			index+1,
			100+float64(index%20),
			20+float64(index%10),
			int64(index%3),
			0.5+float64(index%10)/10,
		)
		candidates[index].CapacityUtilization = float64(index%50) / 100
	}
	return candidates
}

func balancedSettingsForTest() BalancedSettings {
	return BalancedSettings{
		Weights:                        Weights{Availability: 0.45, Latency: 0.25, Throughput: 0.10, Cost: 0.20},
		AvailabilityTarget:             0.99,
		AvailabilityFloor:              0,
		LatencyTargetMs:                200,
		ThroughputTarget:               20,
		CostTarget:                     1,
		MinimumVolume:                  1,
		WilsonZ:                        1.96,
		UnknownAvailability:            0.5,
		UnknownLatencyUtility:          0.5,
		UnknownThroughputUtility:       0.5,
		UnknownCostUtility:             0.4,
		ProtectionBandBasisPoints:      1_000,
		ExplorationBasisPoints:         0,
		MinimumExplorationScore:        0.05,
		MaxCapacityUtilization:         1,
		AffinityMaxCapacityUtilization: 0.80,
		DegradedMultiplier:             0.5,
		SoftFallbackMultiplier:         0.1,
		HalfOpenProbes:                 1,
		SnapshotStaleSec:               300,
		NowUnix:                        1_000,
		NowUnixMilli:                   1_000_000,
		RandomSeed:                     1,
	}
}

func balancedCandidateForTest(id int, latencyMs float64, throughput float64, failures int64, cost float64) BalancedCandidate {
	weight := uint(100)
	priority := int64(10)
	return BalancedCandidate{
		Candidate: Candidate{
			Channel: &model.Channel{Id: id, Weight: &weight, Priority: &priority},
			Metric: &MetricSnapshot{
				ReliabilityRequestCount: 100, ReliabilityFailureCount: failures,
				P95LatencyMs: latencyMs, TPS: throughput,
			},
			Cost: &CostSnapshot{Known: true, Cost: cost, UpdatedUnix: 900},
		},
		TargetWeight:        100,
		Confidence:          1,
		Freshness:           1,
		SlowStartFactor:     1,
		CapacityUtilization: 0,
	}
}

func balancedRankedByID(t *testing.T, decision BalancedDecision, channelID int) BalancedRankedCandidate {
	t.Helper()
	for _, candidate := range decision.Ranked {
		if candidate.Channel != nil && candidate.Channel.Id == channelID {
			return candidate
		}
	}
	require.Failf(t, "balanced candidate not ranked", "channel_id=%d", channelID)
	return BalancedRankedCandidate{}
}

func balancedExclusionByID(t *testing.T, decision BalancedDecision, channelID int) BalancedExclusion {
	t.Helper()
	for _, exclusion := range decision.Excluded {
		if exclusion.ChannelID == channelID {
			return exclusion
		}
	}
	require.Failf(t, "balanced candidate not excluded", "channel_id=%d", channelID)
	return BalancedExclusion{}
}

func balancedRankedIDs(decision BalancedDecision) []int {
	ids := make([]int, 0, len(decision.Ranked))
	for _, candidate := range decision.Ranked {
		ids = append(ids, candidate.Channel.Id)
	}
	return ids
}
