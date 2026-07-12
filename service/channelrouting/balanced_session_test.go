package channelrouting

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestRoutingSessionPlanBalancedUsesExactRequestCostAndPinnedSnapshot(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	now := time.Now().Unix()
	view := balancedActiveSnapshotForTest(t, now)
	SetSnapshotForTest(view)
	session, err := NewRequestRoutingSession("balanced-request", "default")
	require.NoError(t, err)

	changed := balancedActiveSnapshotForTest(t, now)
	changed.Revision = 2
	changed.Pools[0].Members[0].Models[0].CostBaseRatio = 0.1
	changed.Pools[0].Members[1].Models[0].CostBaseRatio = 4
	SetSnapshotForTest(changed)

	plan, active, err := session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test", IsStream: true,
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, uint64(1), plan.PolicyRevision)
	assert.Equal(t, 102, plan.SelectedChannelID)
	assert.Equal(t, 12, plan.SelectedIdentity.MemberID)
	assert.True(t, plan.SelectedCostKnown)
	assert.InDelta(t, 0.5, plan.SelectedCost, 1e-9)
}

func TestRequestRoutingSessionPlanBalancedUsesAffinityOnlyInsideProtectionBand(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
	now := time.Now().Unix()
	view := balancedActiveSnapshotForTest(t, now)
	view.Pools[0].BalancedPolicy.CostBudget = 0
	view.Pools[0].BalancedPolicy.RequireKnownCost = false
	view.Pools[0].BalancedPolicy.ProtectionBandBasisPoints = 10_000
	view.Pools[0].Members[0].Models[0].CostBaseRatio = 0.5
	view.Pools[0].Members[1].Models[0].CostBaseRatio = 4
	SetSnapshotForTest(view)
	session, err := NewRequestRoutingSession("balanced-affinity", "default")
	require.NoError(t, err)

	plan, active, err := session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
		PreferredChannelID: 102,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 102, plan.SelectedChannelID)
	assert.True(t, plan.AffinityUsed)

	view.Pools[0].BalancedPolicy.ProtectionBandBasisPoints = 0
	SetSnapshotForTest(view)
	session, err = NewRequestRoutingSession("balanced-affinity-outside", "default")
	require.NoError(t, err)
	plan, active, err = session.PlanBalanced(BalancedRoutingPlanInput{
		RequestRoutingPlanInput: RequestRoutingPlanInput{
			RequestPath: "/v1/chat/completions", ModelName: "gpt-test",
			PromptTokenEstimate: int(common.QuotaPerUnit),
		},
		PreferredChannelID: 102,
	})
	require.NoError(t, err)
	require.True(t, active)
	assert.Equal(t, 101, plan.SelectedChannelID)
	assert.False(t, plan.AffinityUsed)
}

func balancedActiveSnapshotForTest(t *testing.T, now int64) SnapshotView {
	t.Helper()
	policy, err := resolveBalancedPoolPolicy(model.RoutingPolicyProfileBalanced, []byte(`{
		"weight_availability": 0.1,
		"weight_latency": 0.1,
		"weight_throughput": 0.1,
		"weight_cost": 0.7,
		"cost_budget": 2,
		"require_known_cost": true,
		"exploration_basis_points": 0
	}`))
	require.NoError(t, err)
	return SnapshotView{
		Revision: 1, PolicyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RuntimeGeneration: 1, ActivationID: 1,
		ActivationStage: model.RoutingDeploymentStageActive, BuiltAtUnix: now,
		Pools: []PoolSnapshot{{
			ID: 1, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile: model.RoutingPolicyProfileBalanced, BalancedPolicy: policy,
			Members: []PoolMemberSnapshot{
				balancedSessionMemberForTest(11, 101, 3, now),
				balancedSessionMemberForTest(12, 102, 0.5, now),
			},
		}},
		Channels: []ChannelSnapshot{
			{ID: 101, Name: "first", Status: common.ChannelStatusEnabled},
			{ID: 102, Name: "second", Status: common.ChannelStatusEnabled},
		},
	}
}

func balancedSessionMemberForTest(memberID int, channelID int, baseRatio float64, now int64) PoolMemberSnapshot {
	return PoolMemberSnapshot{
		ID: memberID, PoolID: 1, ChannelID: channelID, PhysicalStatus: common.ChannelStatusEnabled,
		LegacyWeight: 100,
		Models: []ModelSnapshot{{
			ModelName: "gpt-test", MetricKnown: true, RequestCount: 100, SuccessCount: 100,
			ReliabilityRequestCount: 100, P95LatencyKnown: true, P95LatencyMs: 100,
			P95TTFTKnown: true, P95TTFTMs: 80, OutputTokensPerSecond: 20, MetricUpdatedUnix: now,
			CostKnown: true, Cost: 1, CostUpdatedUnix: now, CostGroupRatio: 1,
			CostBaseRatio: baseRatio, CostCompletionRatio: 1, CostBillingMode: "token",
		}},
	}
}
