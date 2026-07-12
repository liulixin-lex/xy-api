package channelrouting

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	routingselector "github.com/QuantumNous/new-api/service/routing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeSnapshotPreparesBalancedPoolsForStreamAndRequestCostOverrides(t *testing.T) {
	ResetSnapshotForTest()
	t.Cleanup(ResetSnapshotForTest)
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
	now := time.Now().Unix()
	view := SnapshotView{
		Revision: 1, PolicyHash: string(make([]byte, 64)), BuiltAtUnix: now,
		ActivationID: 1, ActivationStage: model.RoutingDeploymentStageActive,
		Pools: []PoolSnapshot{{
			ID: 1, GroupName: "default", DeploymentStage: model.RoutingDeploymentStageActive,
			PolicyProfile: model.RoutingPolicyProfileBalanced, BalancedPolicy: policy,
			Members: []PoolMemberSnapshot{
				balancedSnapshotMemberForTest(11, 101, now),
				balancedSnapshotMemberForTest(12, 102, now),
			},
		}},
		Channels: []ChannelSnapshot{
			{ID: 101, Name: "first", Status: common.ChannelStatusEnabled},
			{ID: 102, Name: "second", Status: common.ChannelStatusEnabled},
		},
	}
	SetSnapshotForTest(view)
	snapshot := currentSnapshot.Load()
	require.NotNil(t, snapshot)
	assert.Len(t, snapshot.preparedBalancedPools, 2)
	prepared := snapshot.preparedBalancedPools[balancedPoolModelKey{poolID: 1, model: "gpt-test", preferTTFT: true}]
	require.NotNil(t, prepared)
	decision, err := prepared.Select(routingselector.BalancedRequest{
		RandomSeed:   1,
		NowUnixMilli: time.Now().UnixMilli(),
		RuntimeByChannelID: map[int]routingselector.BalancedRuntimeState{
			101: {Cost: &routingselector.CostSnapshot{Known: true, Cost: 4, UpdatedUnix: now}},
			102: {Cost: &routingselector.CostSnapshot{Known: true, Cost: 0.5, UpdatedUnix: now}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 102, decision.SelectedChannelID)
}

func balancedSnapshotMemberForTest(memberID int, channelID int, now int64) PoolMemberSnapshot {
	return PoolMemberSnapshot{
		ID: memberID, PoolID: 1, ChannelID: channelID, PhysicalStatus: common.ChannelStatusEnabled,
		LegacyWeight: 100,
		Models: []ModelSnapshot{{
			ModelName: "gpt-test", MetricKnown: true, RequestCount: 100, SuccessCount: 100,
			ReliabilityRequestCount: 100, P95LatencyKnown: true, P95LatencyMs: 100,
			P95TTFTKnown: true, P95TTFTMs: 80, OutputTokensPerSecond: 20, MetricUpdatedUnix: now,
			CostKnown: true, Cost: 1, CostUpdatedUnix: now, CostGroupRatio: 1,
		}},
	}
}
