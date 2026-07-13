package channelrouting

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBalancedReplayRoundTripsPreparedSelectionAndRejectsTampering(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	view := balancedActiveSnapshotForTest(t, time.Now().Unix())
	SetSnapshotForTest(view)
	session, err := NewRequestRoutingSession("balanced-replay", "default")
	require.NoError(t, err)
	plan, active, err := session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test", IsStream: true,
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
		PreferredChannelID: 102,
	})
	require.NoError(t, err)
	require.True(t, active)
	require.NoError(t, plan.Replay.Validate())

	replayed, err := RunBalancedReplay(plan.Replay)
	require.NoError(t, err)
	assert.Equal(t, plan.SelectedChannelID, replayed.SelectedChannelID)
	assert.Equal(t, plan.SelectedIdentity.MemberID, replayed.SelectedMemberID)
	assert.Equal(t, plan.SelectedCostKnown, replayed.SelectedCostKnown)
	assert.Equal(t, plan.SelectedCost, replayed.SelectedCost)
	assert.Equal(t, plan.AffinityUsed, replayed.AffinityUsed)
	assert.Equal(t, plan.ExplorationUsed, replayed.ExplorationUsed)
	assert.Equal(t, plan.Candidates, replayed.Candidates)

	tampered := plan.Replay
	tampered.Candidates = append([]BalancedReplayCandidate(nil), plan.Replay.Candidates...)
	require.NotEmpty(t, tampered.Candidates)
	tampered.Candidates[0].TargetWeight++
	_, err = RunBalancedReplay(tampered)
	assert.ErrorIs(t, err, ErrBalancedReplayHash)
}

func TestBalancedReplayValidationRejectsInvalidDynamicState(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	view := balancedActiveSnapshotForTest(t, time.Now().Unix())
	SetSnapshotForTest(view)
	session, err := NewRequestRoutingSession("balanced-replay-validation", "default")
	require.NoError(t, err)
	plan, active, err := session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	require.NotEmpty(t, plan.Replay.Candidates)
	require.NotEmpty(t, plan.Replay.RuntimeStates)

	tests := []struct {
		name   string
		mutate func(*BalancedReplayInput)
	}{
		{
			name: "invalid runtime admission",
			mutate: func(input *BalancedReplayInput) {
				input.RuntimeStates[0].Admission = "unbounded"
			},
		},
		{
			name: "negative metric latency",
			mutate: func(input *BalancedReplayInput) {
				input.Candidates[0].Metric.P95LatencyMs = -1
			},
		},
		{
			name: "unknown breaker state",
			mutate: func(input *BalancedReplayInput) {
				input.RuntimeStates[0].Breaker = &ShadowBreakerInput{State: "unknown"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := plan.Replay
			input.Candidates = append([]BalancedReplayCandidate(nil), plan.Replay.Candidates...)
			for index := range input.Candidates {
				if input.Candidates[index].Metric != nil {
					metric := *input.Candidates[index].Metric
					input.Candidates[index].Metric = &metric
				}
			}
			input.RuntimeStates = append([]BalancedReplayRuntimeState(nil), plan.Replay.RuntimeStates...)
			test.mutate(&input)
			assert.ErrorIs(t, input.Validate(), ErrBalancedReplayInvalid)
		})
	}
}
