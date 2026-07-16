package channelrouting

import (
	"context"
	"strings"
	"testing"

	routingselector "github.com/QuantumNous/new-api/service/routing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecisionCandidateDetailsPageReplaysBeyondStoredSummaryLimit(t *testing.T) {
	ResetDecisionAuditsForTest(4)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	const candidateCount = MaxDecisionCandidates + 6
	requestID := "candidate-page-replay"
	seed, err := DeriveDecisionSeed(requestID, 7, 0)
	require.NoError(t, err)
	profile, err := NewLegacyRequestProfile("/v1/chat/completions", "default", "gpt-test", true, 0, 10, 10)
	require.NoError(t, err)
	candidates := make([]ShadowCandidateInput, candidateCount)
	for index := range candidates {
		candidates[index] = ShadowCandidateInput{
			PoolMemberID: index + 1, ChannelID: index + 101, CredentialID: index + 1,
			Priority: int64(index), Weight: uint(index + 1), SlowStartFactor: 1,
			Metric: &ShadowMetricInput{
				RequestCount: 100, SuccessCount: int64(90 + index%10), ReliabilityRequestCount: 100,
				ReliabilityFailureCount: int64(10 - index%10), P95LatencyMs: float64(100 + index),
				P95TTFTMs: float64(50 + index), OutputTokensPerSecond: float64(20 + index),
			},
		}
	}
	input, err := BuildShadowReplayInput(1, 7, 3, strings.Repeat("a", 64), profile, routingselector.Settings{
		WeightAvailability: 1, AvailabilityFloor: 0, MinVolume: 1, TopK: MaxDecisionCandidates,
		MaxEjectedPct: 100, HalfOpenProbes: 1, SnapshotStaleSec: 1_800,
		NowUnix: 1_000, NowUnixMilli: 1_000_000, RandomSeed: seed,
	}, candidates)
	require.NoError(t, err)
	replay, err := RunShadowReplay(input)
	require.NoError(t, err)
	decisionID, err := EnqueueDecision(DecisionInput{
		RequestID: requestID, PoolID: 1, GroupName: profile.GroupName, ModelName: profile.ModelName,
		SnapshotRevision: 7, AlgorithmVersion: DecisionAlgorithmShadow, IsStream: true,
		ObservedChannelID: replay.SelectedChannelID, FilteredOpen: replay.FilteredOpen,
		FilteredCapacity: replay.FilteredCapacity, BreakerBypassed: replay.BreakerBypassed,
		Candidates: replay.Candidates, ReplayInput: &input,
		DifferenceType:    ClassifyShadowDifference(0, replay),
		ObservedCostKnown: replay.SelectedCostKnown, ObservedExpectedCost: replay.SelectedCost,
	})
	require.NoError(t, err)
	audits := decisionBuffer.drain(1)
	require.Len(t, audits, 1)
	assert.Equal(t, decisionID, audits[0].DecisionID)

	page, err := ListDecisionCandidateDetailsContext(context.Background(), audits[0], MaxDecisionCandidates, 10)
	require.NoError(t, err)
	assert.True(t, page.Complete)
	assert.Equal(t, "shadow_replay", page.Source)
	assert.Equal(t, candidateCount, page.Total)
	assert.Equal(t, candidateCount, page.Available)
	assert.True(t, page.RequestCountKnown)
	assert.Equal(t, float64(1), page.RequestCountCoverage)
	assert.Equal(t, int64(candidateCount*100), page.TotalRequestCount)
	assert.Zero(t, page.NextCursor)
	require.Len(t, page.Items, candidateCount-MaxDecisionCandidates)
	assert.Equal(t, MaxDecisionCandidates+1, page.Items[0].Rank)
	assert.Equal(t, page.Items[0].ChannelID-100, page.Items[0].CredentialID)
	require.NotNil(t, page.Items[0].RequestCount)
	assert.Equal(t, int64(100), *page.Items[0].RequestCount)
}

func TestDecisionCandidateDetailsPageMarksNonReplayableTruncation(t *testing.T) {
	ResetDecisionAuditsForTest(4)
	t.Cleanup(func() { ResetDecisionAuditsForTest() })

	candidates := make([]DecisionCandidate, MaxDecisionCandidates+3)
	for index := range candidates {
		candidates[index] = DecisionCandidate{
			PoolMemberID: index + 1, ChannelID: index + 101, Eligible: true, Score: float64(index),
		}
	}
	_, err := EnqueueDecision(DecisionInput{
		RequestID: "candidate-page-summary", PoolID: 1, SnapshotRevision: 1,
		GroupName: "default", ModelName: "gpt-test", Candidates: candidates,
	})
	require.NoError(t, err)
	audits := decisionBuffer.drain(1)
	require.Len(t, audits, 1)

	page, err := ListDecisionCandidateDetailsContext(context.Background(), audits[0], 0, 10)
	require.NoError(t, err)
	assert.False(t, page.Complete)
	assert.Equal(t, "stored_summary", page.Source)
	assert.Equal(t, MaxDecisionCandidates+3, page.Total)
	assert.Equal(t, MaxDecisionCandidates, page.Available)
	assert.Equal(t, 10, page.NextCursor)
	assert.Equal(t, "non_replayable_candidate_payload_limit", page.TruncationReason)
	require.Len(t, page.Items, 10)
	assert.Equal(t, 101, page.Items[0].ChannelID)
}
